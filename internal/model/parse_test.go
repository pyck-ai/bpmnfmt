package model

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixtures(t *testing.T) []string {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*.bpmn"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no fixtures found: %v", err)
	}
	return paths
}

func TestParseFixtures(t *testing.T) {
	for _, p := range fixtures(t) {
		t.Run(filepath.Base(p), func(t *testing.T) {
			f, err := ParseFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if len(f.Processes) != 1 {
				t.Fatalf("processes = %d, want 1", len(f.Processes))
			}
			proc := f.Processes[0]
			if len(proc.Nodes) == 0 || len(proc.Flows) == 0 {
				t.Fatalf("empty process: %d nodes, %d flows", len(proc.Nodes), len(proc.Flows))
			}
			if len(f.DiagramSpans) != 1 {
				t.Fatalf("diagram spans = %d, want 1", len(f.DiagramSpans))
			}
			sp := f.DiagramSpans[0]
			got := string(f.Raw[sp.Start:sp.End])
			if !strings.HasPrefix(got, "<bpmndi:BPMNDiagram") || !strings.HasSuffix(got, "</bpmndi:BPMNDiagram>") {
				t.Errorf("diagram span boundaries wrong:\nstart: %.40q\nend: %.40q", got, got[len(got)-40:])
			}
			for _, ns := range []string{NSBPMNDI, NSDC, NSDI} {
				if _, ok := f.Prefix(ns); !ok {
					t.Errorf("missing prefix declaration for %s", ns)
				}
			}
			// Every flow endpoint resolves in these known-good files.
			for _, fl := range proc.Flows {
				if proc.NodeByID[fl.SourceRef] == nil {
					t.Errorf("flow %s: unresolved sourceRef %s", fl.ID, fl.SourceRef)
				}
				if proc.NodeByID[fl.TargetRef] == nil {
					t.Errorf("flow %s: unresolved targetRef %s", fl.ID, fl.TargetRef)
				}
			}
		})
	}
}

func TestParseTourExecutionDetails(t *testing.T) {
	f, err := ParseFile(filepath.Join("..", "..", "testdata", "tour-execution.bpmn"))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Processes[0]

	n := p.NodeByID["Activity_1b1t68m"]
	if n == nil {
		t.Fatal("Activity_1b1t68m not found")
	}
	if n.Kind != KindTask || n.Tag != "userTask" {
		t.Errorf("kind/tag = %v/%s", n.Kind, n.Tag)
	}
	wantIn := []string{"Flow_orderbox_to_src", "Flow_0tcw3fm", "Flow_insert_to_src"}
	if len(n.Incoming) != len(wantIn) {
		t.Fatalf("incoming = %v, want %v", n.Incoming, wantIn)
	}
	for i := range wantIn {
		if n.Incoming[i] != wantIn[i] {
			t.Errorf("incoming[%d] = %s, want %s (declared order must be preserved)", i, n.Incoming[i], wantIn[i])
		}
	}

	gw := p.NodeByID["Gateway_shelf_empty"]
	if gw == nil || gw.Kind != KindExclusiveGateway {
		t.Fatalf("Gateway_shelf_empty: %+v", gw)
	}
	if len(gw.Outgoing) != 2 || gw.Outgoing[0] != "Flow_emptygw_no_box" {
		t.Errorf("outgoing order = %v, want Flow_emptygw_no_box first", gw.Outgoing)
	}

	if len(p.Annotations) != 12 {
		t.Errorf("annotations = %d, want 12", len(p.Annotations))
	}
	if len(p.Associations) != 12 {
		t.Errorf("associations = %d, want 12", len(p.Associations))
	}

	// Color carry-over source: the print-labels task is colored.
	colors := f.DI.ShapeColors["Activity_0t4iewd"]
	if len(colors) != 4 {
		t.Errorf("shape colors for Activity_0t4iewd = %v, want 4 attrs", colors)
	}

	start := p.NodeByID["Event_05zjcyb"]
	if start == nil || start.Kind != KindStartEvent || start.Name != "MC / Tour created" {
		t.Errorf("start event: %+v", start)
	}
	timer := p.NodeByID["Event_empty_tour"]
	if timer == nil || timer.Kind != KindEndEvent {
		t.Errorf("empty tour end: %+v", timer)
	}
}

func TestParseEventDefinitions(t *testing.T) {
	f, err := ParseFile(filepath.Join("..", "..", "testdata", "001_mc_creation.bpmn"))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Processes[0]
	if n := p.NodeByID["Event_0hj0xec"]; n == nil || n.EventDef != "timer" || n.Kind != KindIntermediateCatchEvent {
		t.Errorf("wait event: %+v", n)
	}
	if n := p.NodeByID["Event_083gpo3"]; n == nil || n.EventDef != "timer" || n.Kind != KindStartEvent {
		t.Errorf("timer start: %+v", n)
	}
}

// Splicing the original DI block back in must reproduce the input bytes
// exactly — this is the guarantee that comments and formatting survive.
func TestSpliceRoundTrip(t *testing.T) {
	for _, p := range fixtures(t) {
		t.Run(filepath.Base(p), func(t *testing.T) {
			f, err := ParseFile(p)
			if err != nil {
				t.Fatal(err)
			}
			sp := f.DiagramSpans[0]
			block := f.Raw[sp.Start:sp.End]
			out, err := f.SpliceDI(block)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out, f.Raw) {
				t.Error("splice round-trip is not byte-identical")
			}
		})
	}
}

func TestSpliceInsertWhenNoDI(t *testing.T) {
	src := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" id="d1" targetNamespace="http://bpmn.io/schema/bpmn">
  <bpmn:process id="P1">
    <bpmn:startEvent id="S1" />
  </bpmn:process>
</bpmn:definitions>
`)
	f, err := Parse(src, "mem.bpmn")
	if err != nil {
		t.Fatal(err)
	}
	if f.DI != nil || len(f.DiagramSpans) != 0 {
		t.Fatalf("unexpected DI: %+v", f.DiagramSpans)
	}
	out, err := f.SpliceDI([]byte("<bpmndi:BPMNDiagram id=\"BPMNDiagram_1\"></bpmndi:BPMNDiagram>"))
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(string(src),
		"</bpmn:definitions>",
		"  <bpmndi:BPMNDiagram id=\"BPMNDiagram_1\"></bpmndi:BPMNDiagram>\n</bpmn:definitions>", 1)
	if string(out) != want {
		t.Errorf("insert splice:\n got: %q\nwant: %q", out, want)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
