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

func TestTourCreationBatchLoop(t *testing.T) {
	g := load(t, "tour-creation.bpmn")
	if len(g.Components) != 1 {
		t.Fatalf("components = %d", len(g.Components))
	}
	if got := backEdges(g); !reflect.DeepEqual(got, []string{"Flow_loop_back"}) {
		t.Errorf("back edges = %v", got)
	}
	spine := g.Components[0].Spine
	last := spine[len(spine)-1]
	if last != "Event_0ruyaxs" {
		t.Errorf("spine must end at the all-tours-created end event, got %v", spine)
	}
}
