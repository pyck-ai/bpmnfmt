package graph

import (
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"github.com/pyck-ai/bpmnfmt/internal/model"
)

func load(t *testing.T, name string) *Graph {
	t.Helper()
	f, err := model.ParseFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return Build(f.Processes[0])
}

func backEdges(g *Graph) []string {
	var ids []string
	for id := range g.Back {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func TestTourExecutionSpineAndLoops(t *testing.T) {
	g := load(t, "tour-execution.bpmn")

	if len(g.Components) != 1 {
		t.Fatalf("components = %d, want 1", len(g.Components))
	}

	wantBack := []string{"Flow_0tcw3fm", "Flow_confirmgw_no_item", "Flow_insert_to_src"}
	if got := backEdges(g); !reflect.DeepEqual(got, wantBack) {
		t.Errorf("back edges = %v, want %v", got, wantBack)
	}

	// The acceptance spine from the reference layout.
	wantSpine := []string{
		"Event_05zjcyb",         // start
		"Activity_fetch_tour",   // fetch tour details
		"Gateway_any_movements", // movements?
		"Activity_0t4iewd",      // print labels
		"Activity_scan_trolley",
		"Activity_scan_order_box",
		"Activity_1b1t68m", // scan source
		"Activity_pick_item",
		"Gateway_shelf_empty",
		"Activity_pick_box", // scan destination box
		"Gateway_1mr53mh",   // expected quantity?
		"Gateway_08wsh14",   // remaining positions?
		"Gateway_02fo3jw",   // complete orders?
		"Activity_1jpf51l",  // bring to packing
		"Activity_deliver_trolley",
		"Activity_05s8j5b", // book into packing zone
		"Event_1jvr8va",    // end
	}
	if !reflect.DeepEqual(g.Components[0].Spine, wantSpine) {
		t.Errorf("spine =\n%v\nwant\n%v", g.Components[0].Spine, wantSpine)
	}
}

func TestOrderCreatedLoop(t *testing.T) {
	g := load(t, "order-created.bpmn")
	if got := backEdges(g); !reflect.DeepEqual(got, []string{"Flow_0m830hg"}) {
		t.Errorf("back edges = %v", got)
	}
	want := []string{"StartEvent_1", "Activity_1h7v8uq", "Gateway_0xcskz4", "Activity_1jcm9kt", "Event_0icc7ya"}
	if !reflect.DeepEqual(g.Components[0].Spine, want) {
		t.Errorf("spine = %v, want %v", g.Components[0].Spine, want)
	}
}

func TestMCCreationComponents(t *testing.T) {
	g := load(t, "001_mc_creation.bpmn")
	if len(g.Components) != 2 {
		t.Fatalf("components = %d, want 2", len(g.Components))
	}
	// Second component: scheduled tour creation, two start events.
	c1 := g.Components[1]
	if len(c1.Starts) != 2 || c1.Starts[0].ID != "Event_083gpo3" {
		t.Fatalf("starts = %+v", c1.Starts)
	}
	want := []string{"Event_083gpo3", "Activity_1ojjtgp", "Activity_0dkimtk", "Event_0ruyaxs"}
	if !reflect.DeepEqual(c1.Spine, want) {
		t.Errorf("component-2 spine = %v, want %v", c1.Spine, want)
	}
	// First component keeps the retry loop as a back edge.
	if !g.Back["Flow_0m830hg"] {
		t.Errorf("Flow_0m830hg should be a back edge; back = %v", backEdges(g))
	}
}

// TestBuildSkipsFlowsToUnsupportedNodes guards against a nil pointer panic in
// selectSpine when the process contains sequence flows that point at elements
// the parser classifies as unsupported (parallelGateway, subProcess,
// boundaryEvent, ...) and therefore does not register in NodeByID. Before the
// fix, BuildOpts -> selectSpine dereferenced g.Proc.NodeByID[cur] which was
// nil for the unsupported gateway.
func TestBuildSkipsFlowsToUnsupportedNodes(t *testing.T) {
	const src = `<?xml version="1.0" encoding="UTF-8"?>
<bpmn:definitions xmlns:bpmn="http://www.omg.org/spec/BPMN/20100524/MODEL" id="d" targetNamespace="x">
  <bpmn:process id="P" isExecutable="true">
    <bpmn:task id="T1"><bpmn:outgoing>F1</bpmn:outgoing></bpmn:task>
    <bpmn:endEvent id="E1"><bpmn:incoming>F2</bpmn:incoming></bpmn:endEvent>
    <bpmn:sequenceFlow id="F1" sourceRef="T1" targetRef="PG"/>
    <bpmn:sequenceFlow id="F2" sourceRef="PG" targetRef="E1"/>
    <bpmn:parallelGateway id="PG">
      <bpmn:incoming>F1</bpmn:incoming>
      <bpmn:outgoing>F2</bpmn:outgoing>
    </bpmn:parallelGateway>
  </bpmn:process>
</bpmn:definitions>`
	f, err := model.Parse([]byte(src), "inline.bpmn")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Build panicked on flows to unsupported node: %v", r)
		}
	}()
	g := Build(f.Processes[0])
	// Flows that reference the unsupported parallelGateway must be filtered
	// out of the adjacency lists so downstream consumers don't follow them.
	if got := len(g.Out["T1"]); got != 0 {
		t.Errorf("Out[T1] = %d flows, want 0 (F1 targets unsupported PG)", got)
	}
	if got := len(g.In["E1"]); got != 0 {
		t.Errorf("In[E1] = %d flows, want 0 (F2 sources from unsupported PG)", got)
	}
	// Spines for the two surviving (disconnected) nodes must be set.
	for _, c := range g.Components {
		if c.Spine == nil {
			t.Errorf("component with nodes %v has nil Spine", c.Nodes)
		}
	}
}

func TestTourCreationBatchLoop(t *testing.T) {
	g := load(t, "tour-creation.bpmn")
	if len(g.Components) != 1 {
		t.Fatalf("components = %d", len(g.Components))
	}
	if got := backEdges(g); !reflect.DeepEqual(got, []string{"Flow_loop_back"}) {
		t.Errorf("back edges = %v", got)
	}
	// Gateway_batches is a loop header: Flow_loop_back returns to it. The
	// loop body (Yes) stays on the spine and the exit (No -> end event)
	// becomes an alternate, so the spine ends at the loop's last activity.
	want := []string{
		"Event_083gpo3", "Activity_1ojjtgp", "Activity_fetch_route_table",
		"Activity_fetch_pick_locations", "Gateway_batches",
		"Activity_create_tour", "Activity_update_status",
	}
	if got := g.Components[0].Spine; !reflect.DeepEqual(got, want) {
		t.Errorf("spine = %v, want %v", got, want)
	}
	if g.Components[0].SpineSet["Event_0ruyaxs"] {
		t.Error("the loop exit must leave the spine, not continue it")
	}
}
