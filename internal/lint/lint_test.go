package lint

import (
	"path/filepath"
	"testing"

	"github.com/pyck-ai/bpmnfmt/internal/model"
)

func check(t *testing.T, name string) []Finding {
	t.Helper()
	f, err := model.ParseFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	fs := Check(f)
	for _, fd := range fs {
		t.Logf("%-7s %-4s %-28s %s", fd.Sev, fd.Rule, fd.Element, fd.Message)
	}
	return fs
}

func has(fs []Finding, rule, element string) bool {
	for _, f := range fs {
		if f.Rule == rule && f.Element == element {
			return true
		}
	}
	return false
}

func countSev(fs []Finding, sev Severity) int {
	n := 0
	for _, f := range fs {
		if f.Severity == sev {
			n++
		}
	}
	return n
}

func TestTourExecutionFindings(t *testing.T) {
	fs := check(t, "tour-execution.bpmn")
	if !has(fs, "W2", "Flow_1a8fb2c") {
		t.Error("expected W2 for unlabeled branch Flow_1a8fb2c")
	}
	if !has(fs, "W4", "Gateway_08wsh14") {
		t.Error("expected W4 for mixed gateway Gateway_08wsh14")
	}
	if n := countSev(fs, SevError); n != 0 {
		t.Errorf("errors = %d, want 0", n)
	}
}

func TestMCCreationFindings(t *testing.T) {
	fs := check(t, "001_mc_creation.bpmn")
	if !has(fs, "W3", "Gateway_0xcskz4") {
		t.Error("expected W3 for unnamed gateway Gateway_0xcskz4")
	}
	if !has(fs, "W5", "Process_0vf1lkj") {
		t.Error("expected W5 for disconnected components")
	}
	if !has(fs, "I3", "Process_0vf1lkj") {
		t.Error("expected I3 for multiple start events")
	}
	if n := countSev(fs, SevError); n != 0 {
		t.Errorf("errors = %d, want 0", n)
	}
}

func TestCleanFixtures(t *testing.T) {
	for _, name := range []string{"item-stock-placement.bpmn", "order-created.bpmn"} {
		fs := check(t, name)
		if n := countSev(fs, SevError) + countSev(fs, SevWarning); n != 0 {
			t.Errorf("%s: errors+warnings = %d, want 0", name, n)
		}
	}
}

func TestTourCreationFindings(t *testing.T) {
	fs := check(t, "tour-creation.bpmn")
	if !has(fs, "W4", "Gateway_batches") {
		t.Error("expected W4 for loop-back into decision gateway")
	}
	if n := countSev(fs, SevError); n != 0 {
		t.Errorf("errors = %d, want 0", n)
	}
}

func TestWorkflowAssignedFindings(t *testing.T) {
	fs := check(t, "001_mc_workflow_assigned.bpmn")
	if n := countSev(fs, SevError); n != 0 {
		t.Errorf("errors = %d, want 0 (file is legacy but structurally sound)", n)
	}
}

func TestBrokenModels(t *testing.T) {
	src := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" id="d" targetNamespace="http://x">
  <bpmn:process id="P">
    <bpmn:startEvent id="S"><bpmn:outgoing>F1</bpmn:outgoing></bpmn:startEvent>
    <bpmn:serviceTask id="T1" name="wired"><bpmn:incoming>F1</bpmn:incoming><bpmn:outgoing>F2</bpmn:outgoing></bpmn:serviceTask>
    <bpmn:serviceTask id="T2" name="floating" />
    <bpmn:endEvent id="E"><bpmn:incoming>F2</bpmn:incoming></bpmn:endEvent>
    <bpmn:sequenceFlow id="F1" sourceRef="S" targetRef="T1" />
    <bpmn:sequenceFlow id="F2" sourceRef="T1" targetRef="E" />
    <bpmn:sequenceFlow id="F3" sourceRef="T1" targetRef="GHOST" />
  </bpmn:process>
</bpmn:definitions>
`
	f, err := model.Parse([]byte(src), "broken.bpmn")
	if err != nil {
		t.Fatal(err)
	}
	fs := Check(f)
	for _, fd := range fs {
		t.Logf("%-7s %-4s %-10s %s", fd.Sev, fd.Rule, fd.Element, fd.Message)
	}
	if !has(fs, "E1", "F3") {
		t.Error("expected E1 for dangling targetRef")
	}
	if !has(fs, "E4", "T2") {
		t.Error("expected E4 for floating task T2")
	}
	if !HasErrors(fs) {
		t.Error("HasErrors must be true")
	}
}

func TestDuplicateIDs(t *testing.T) {
	src := `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" id="d" targetNamespace="http://x">
  <bpmn:process id="P">
    <bpmn:startEvent id="X"><bpmn:outgoing>F1</bpmn:outgoing></bpmn:startEvent>
    <bpmn:endEvent id="X"><bpmn:incoming>F1</bpmn:incoming></bpmn:endEvent>
    <bpmn:sequenceFlow id="F1" sourceRef="X" targetRef="X" />
  </bpmn:process>
</bpmn:definitions>
`
	f, err := model.Parse([]byte(src), "dup.bpmn")
	if err != nil {
		t.Fatal(err)
	}
	if fs := Check(f); !has(fs, "E2", "X") {
		t.Error("expected E2 duplicate id finding")
	}
}
