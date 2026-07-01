package model

import (
	"path/filepath"
	"testing"
)

// TestParseExpandedSubProcess verifies that an expanded embedded sub-process
// is parsed into a KindSubProcess container node carrying its interior scope
// in Sub, that the container keeps its own incoming/outgoing, and that
// process-level annotations anchored to interior elements are relocated into
// the interior scope.
func TestParseExpandedSubProcess(t *testing.T) {
	f, err := ParseFile(filepath.Join("..", "..", "testdata", "picking-subprocess.bpmn"))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Processes[0]

	c := p.NodeByID["SubProcess_PickItems"]
	if c == nil {
		t.Fatal("SubProcess_PickItems not registered as a node")
	}
	if c.Kind != KindSubProcess {
		t.Errorf("container kind = %v, want KindSubProcess", c.Kind)
	}
	if c.Sub == nil {
		t.Fatal("container has no interior Sub scope (expected expanded)")
	}
	if len(c.Incoming) != 1 || c.Incoming[0] != "Flow_assignee_to_subprocess" {
		t.Errorf("container incoming = %v, want [Flow_assignee_to_subprocess]", c.Incoming)
	}
	if len(c.Outgoing) != 1 || c.Outgoing[0] != "Flow_subprocess_to_end" {
		t.Errorf("container outgoing = %v, want [Flow_subprocess_to_end]", c.Outgoing)
	}

	// Interior scope contents.
	if got := len(c.Sub.Nodes); got != 10 {
		t.Errorf("interior nodes = %d, want 10", got)
	}
	if got := len(c.Sub.Flows); got != 10 {
		t.Errorf("interior flows = %d, want 10", got)
	}
	for _, id := range []string{"Event_InnerStart", "best-before-ok", "Gateway_1rng1ve", "Event_InnerEnd"} {
		if c.Sub.NodeByID[id] == nil {
			t.Errorf("interior node %q not indexed", id)
		}
	}
	// Interior flows must resolve against the interior node index.
	for _, fl := range c.Sub.Flows {
		if c.Sub.NodeByID[fl.SourceRef] == nil {
			t.Errorf("interior flow %s: unresolved sourceRef %s", fl.ID, fl.SourceRef)
		}
		if c.Sub.NodeByID[fl.TargetRef] == nil {
			t.Errorf("interior flow %s: unresolved targetRef %s", fl.ID, fl.TargetRef)
		}
	}

	// The outer process must NOT contain interior nodes/flows.
	if p.NodeByID["best-before-ok"] != nil {
		t.Error("interior node leaked into the outer NodeByID")
	}

	// Annotation relocation: 5 anchored to interior nodes move down, 2 stay.
	if got := len(p.Annotations); got != 2 {
		t.Errorf("outer annotations = %d, want 2", got)
	}
	if got := len(c.Sub.Annotations); got != 5 {
		t.Errorf("interior annotations = %d, want 5", got)
	}
	if got := len(p.Associations); got != 2 {
		t.Errorf("outer associations = %d, want 2", got)
	}
	if got := len(c.Sub.Associations); got != 5 {
		t.Errorf("interior associations = %d, want 5", got)
	}
	if c.Sub.AnnByID["TextAnnotation_BestBeforeOk"] == nil {
		t.Error("relocated annotation not indexed in interior AnnByID")
	}
	if p.AnnByID["TextAnnotation_BestBeforeOk"] != nil {
		t.Error("relocated annotation still indexed in outer AnnByID")
	}
}

// TestParseCollapsedSubProcessRejected verifies that a sub-process whose DI
// shape is not expanded is demoted to Unsupported (so lint reports E7) and
// carries no interior scope.
func TestParseCollapsedSubProcessRejected(t *testing.T) {
	const src = `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" xmlns:bpmndi="http://www.omg.org/spec/BPMN/20100524/DI" xmlns:dc="http://www.omg.org/spec/DD/20100524/DC" xmlns:di="http://www.omg.org/spec/DD/20100524/DI" id="d" targetNamespace="x">
  <bpmn:process id="P">
    <bpmn:startEvent id="S"><bpmn:outgoing>F1</bpmn:outgoing></bpmn:startEvent>
    <bpmn:subProcess id="SP" name="collapsed">
      <bpmn:incoming>F1</bpmn:incoming>
      <bpmn:outgoing>F2</bpmn:outgoing>
      <bpmn:startEvent id="IS"><bpmn:outgoing>IF</bpmn:outgoing></bpmn:startEvent>
      <bpmn:endEvent id="IE"><bpmn:incoming>IF</bpmn:incoming></bpmn:endEvent>
      <bpmn:sequenceFlow id="IF" sourceRef="IS" targetRef="IE" />
    </bpmn:subProcess>
    <bpmn:endEvent id="E"><bpmn:incoming>F2</bpmn:incoming></bpmn:endEvent>
    <bpmn:sequenceFlow id="F1" sourceRef="S" targetRef="SP" />
    <bpmn:sequenceFlow id="F2" sourceRef="SP" targetRef="E" />
  </bpmn:process>
  <bpmndi:BPMNDiagram id="D">
    <bpmndi:BPMNPlane id="PL" bpmnElement="P">
      <bpmndi:BPMNShape id="SP_di" bpmnElement="SP" isExpanded="false">
        <dc:Bounds x="0" y="0" width="100" height="80" />
      </bpmndi:BPMNShape>
    </bpmndi:BPMNPlane>
  </bpmndi:BPMNDiagram>
</bpmn:definitions>`
	f, err := Parse([]byte(src), "collapsed.bpmn")
	if err != nil {
		t.Fatal(err)
	}
	p := f.Processes[0]
	sp := p.NodeByID["SP"]
	if sp == nil {
		t.Fatal("SP node missing")
	}
	if sp.Sub != nil {
		t.Error("collapsed sub-process must not keep an interior Sub scope")
	}
	found := false
	for _, u := range p.Unsupported {
		if u.ID == "SP" && u.Tag == "subProcess" {
			found = true
		}
	}
	if !found {
		t.Errorf("collapsed sub-process not recorded in Unsupported: %+v", p.Unsupported)
	}
}
