package format

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/layout"
	"github.com/pyck-ai/bpmnfmt/internal/lint"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

var fixtureNames = []string{
	"item-stock-placement.bpmn",
	"order-created.bpmn",
	"tour-creation.bpmn",
	"001_mc_creation.bpmn",
	"001_mc_workflow_assigned.bpmn",
	"tour-execution.bpmn",
	"picking-subprocess.bpmn",
}

// fixtureOpts holds the formatting options a fixture is exercised with.
// Fixtures that are absent use the zero Options.
var fixtureOpts = map[string]Options{}

func optsFor(name string) Options { return fixtureOpts[name] }

// graphOpts mirrors the layout-relevant part of optsFor for tests that build
// the graph themselves.
func graphOpts(name string) graph.Options {
	o := optsFor(name)
	return graph.Options{HappyEnd: o.HappyEnd, HappyFlows: o.HappyFlows}
}

func fixture(t *testing.T, name string) *model.File {
	t.Helper()
	f, err := model.ParseFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// maxCrossings: budget for FORBIDDEN crossings (pairs not involving a
// way-back edge; crossings on way-back lines are allowed by design). Zero
// everywhere.
var maxCrossings = map[string]int{}

func TestFormatFixtures(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			f := fixture(t, name)
			res, err := File(f, optsFor(name))
			if err != nil {
				t.Fatal(err)
			}
			if !res.Formatted {
				t.Fatalf("not formatted; findings: %+v", res.Findings)
			}

			// 1. Output must re-parse.
			out, err := model.Parse(res.Output, name+" (formatted)")
			if err != nil {
				t.Fatalf("formatted output does not parse: %v", err)
			}

			// 2. Splice safety: bytes outside the DI block are untouched.
			if got, want := stripDI(t, out), stripDI(t, f); !bytes.Equal(got, want) {
				t.Error("bytes outside the DI block changed")
			}

			// 3. Layout invariants.
			p := f.Processes[0]
			g := graph.BuildOpts(p, graphOpts(name))
			lay, err := layout.Compute(p, g)
			if err != nil {
				t.Fatal(err)
			}
			for _, v := range layout.Validate(p, g, lay) {
				t.Errorf("invariant: %s", v)
			}
			if got, allowed := layout.CountCrossings(p, lay), maxCrossings[name]; got > allowed {
				t.Errorf("forbidden crossings = %d, allowed %d", got, allowed)
			}

			// 4. Completeness: no missing/orphaned DI findings on the output.
			for _, fd := range lint.Check(out) {
				if fd.Rule == "W6" || fd.Rule == "W7" {
					t.Errorf("output DI incomplete: %s %s %s", fd.Rule, fd.Element, fd.Message)
				}
			}

			// 5. Idempotency: formatting the output reproduces it exactly.
			res2, err := File(out, optsFor(name))
			if err != nil {
				t.Fatal(err)
			}
			if !res2.Formatted || !bytes.Equal(res2.Output, res.Output) {
				t.Error("formatting is not idempotent")
			}

			// 6. Determinism: a fresh run over the input is byte-identical.
			res3, err := File(fixture(t, name), optsFor(name))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(res3.Output, res.Output) {
				t.Error("formatting is not deterministic")
			}
		})
	}
}

// TestFormatPickingSubProcess is the acceptance test for expanded embedded
// sub-processes: the picking workflow that used to crash the tool must now
// format cleanly, satisfy every layout invariant, keep its interior shapes
// inside the container rectangle, and be idempotent.
func TestFormatPickingSubProcess(t *testing.T) {
	name := "picking-subprocess.bpmn"
	f := fixture(t, name)
	res, err := File(f, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Formatted {
		t.Fatalf("not formatted; findings: %+v", res.Findings)
	}

	// Re-parses, and bytes outside the DI block are untouched.
	out, err := model.Parse(res.Output, name+" (formatted)")
	if err != nil {
		t.Fatalf("formatted output does not parse: %v", err)
	}
	if got, want := stripDI(t, out), stripDI(t, f); !bytes.Equal(got, want) {
		t.Error("bytes outside the DI block changed")
	}

	// Layout invariants and zero crossings (interior included).
	p := f.Processes[0]
	g := graph.Build(p)
	lay, err := layout.Compute(p, g)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range layout.Validate(p, g, lay) {
		t.Errorf("invariant: %s", v)
	}
	if got := layout.CountCrossings(p, lay); got > 0 {
		t.Errorf("crossings = %d, want 0", got)
	}

	// The container rectangle must enclose every interior shape.
	container := p.NodeByID["SubProcess_PickItems"]
	if container == nil || container.Sub == nil {
		t.Fatal("SubProcess_PickItems not laid out as an expanded container")
	}
	box := lay.Shapes["SubProcess_PickItems"]
	for _, n := range container.Sub.Nodes {
		sh, ok := lay.Shapes[n.ID]
		if !ok {
			t.Errorf("interior node %s has no shape", n.ID)
			continue
		}
		if sh.X < box.X || sh.Y < box.Y || sh.Right() > box.Right() || sh.Bottom() > box.Bottom() {
			t.Errorf("interior node %s (%.0f,%.0f %.0fx%.0f) escapes container (%.0f,%.0f %.0fx%.0f)",
				n.ID, sh.X, sh.Y, sh.W, sh.H, box.X, box.Y, box.W, box.H)
		}
	}

	// No missing/orphaned DI on the formatted output.
	for _, fd := range lint.Check(out) {
		if fd.Rule == "W6" || fd.Rule == "W7" {
			t.Errorf("output DI incomplete: %s %s %s", fd.Rule, fd.Element, fd.Message)
		}
	}

	// Idempotency and determinism.
	res2, err := File(out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Formatted || !bytes.Equal(res2.Output, res.Output) {
		t.Error("formatting is not idempotent")
	}
}

func stripDI(t *testing.T, f *model.File) []byte {
	t.Helper()
	if len(f.DiagramSpans) != 1 {
		t.Fatalf("diagram spans = %d", len(f.DiagramSpans))
	}
	sp := f.DiagramSpans[0]
	return append(append([]byte{}, f.Raw[:sp.Start]...), f.Raw[sp.End:]...)
}

// TestTourExecutionAcceptance encodes the reference layout description:
// spine on one row, branch tiers below, way-back edges on dedicated lines
// below their rows entering the target's bottom, annotations above their
// anchors.
func TestTourExecutionAcceptance(t *testing.T) {
	f := fixture(t, "tour-execution.bpmn")
	p := f.Processes[0]
	g := graph.Build(p)
	lay, err := layout.Compute(p, g)
	if err != nil {
		t.Fatal(err)
	}

	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }
	spineY := cy("Event_05zjcyb")

	// Spine row membership.
	for _, id := range []string{
		"Activity_fetch_tour", "Gateway_any_movements", "Activity_0t4iewd",
		"Activity_scan_trolley", "Activity_scan_order_box", "Activity_1b1t68m",
		"Activity_pick_item", "Gateway_shelf_empty", "Activity_pick_box",
		"Gateway_1mr53mh", "Gateway_08wsh14", "Gateway_02fo3jw",
		"Activity_1jpf51l", "Activity_deliver_trolley", "Activity_05s8j5b", "Event_1jvr8va",
	} {
		if d := cy(id) - spineY; d > 0.5 || d < -0.5 {
			t.Errorf("%s not on spine row (dy=%.0f)", id, d)
		}
	}

	// Branch content strictly below the spine.
	for _, id := range []string{
		"Event_empty_tour", "Activity_confirm_shelf_empty", "Gateway_confirm_empty",
		"Activity_delete_original", "Activity_enumerate_alternatives", "Gateway_02z33cs",
		"Activity_0bgjw4w", "Activity_0xazvfn", "Activity_partial_reconcile",
		"Activity_reconcile_shortage", "Activity_0jtm7jf",
		"Activity_0e37vw3", "Activity_insert_shortfall",
	} {
		if cy(id) <= spineY+layout.RowH/2 {
			t.Errorf("%s should hang below the spine (cy=%.0f, spine=%.0f)", id, cy(id), spineY)
		}
	}

	// The write-off chain hangs below the shortage chain.
	if cy("Activity_reconcile_shortage") <= cy("Activity_delete_original") {
		t.Error("write-off tier should be below the shortage tier")
	}

	// Straight rejoin: Confirmed? rises into Scan box (x-aligned).
	cxOf := func(id string) float64 { r := lay.Shapes[id]; return r.X + r.W/2 }
	if d := cxOf("Gateway_confirm_empty") - cxOf("Activity_pick_box"); d > 0.5 || d < -0.5 {
		t.Errorf("Confirmed? not aligned under Scan box (dx=%.0f)", d)
	}

	// The pick loop runs on a way-back line below the spine row and rises
	// into its target's bottom; nothing of it goes above the spine.
	loop := lay.Edges["Flow_0tcw3fm"]
	if len(loop) == 0 {
		t.Fatal("pick loop has no waypoints")
	}
	tgt := lay.Shapes["Activity_1b1t68m"]
	for _, pt := range loop {
		if pt.Y < spineY-layout.RowH/2 {
			t.Errorf("pick loop must stay below the spine's top (y=%.0f)", pt.Y)
		}
	}
	if last := loop[len(loop)-1]; last.Y != tgt.Bottom() {
		t.Errorf("pick loop should enter the target's bottom (y=%.0f, want %.0f)", last.Y, tgt.Bottom())
	}
	if maxY := loop[1].Y; maxY <= tgt.Bottom() {
		t.Error("pick loop's line should run below its rows, not beside them")
	}
}
