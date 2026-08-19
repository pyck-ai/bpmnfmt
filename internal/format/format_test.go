package format

import (
	"bytes"
	"math"
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
	"split-three-corners.bpmn",
	"split-four-below.bpmn",
	"below-stack-order.bpmn",
	"branch-entry-elbow.bpmn",
	"back-edge-below.bpmn",
	"lifted-subtree.bpmn",
	// split-last-in-chain guards the rule-D cycle fallback: a regression
	// there surfaces as a hard "forward flows contain a cycle" error.
	"split-last-in-chain.bpmn",
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

// layoutOf parses a fixture and computes its layout, or fails the test.
func layoutOf(t *testing.T, name string) (*model.Process, *graph.Graph, *layout.Result) {
	t.Helper()
	f := fixture(t, name)
	p := f.Processes[0]
	g := graph.Build(p)
	lay, err := layout.Compute(p, g)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return p, g, lay
}

// TestThreeWaySplitCorners: rule A. A spine gateway with exactly three
// outgoing flows uses all three corners — happy straight through, the
// shorter alternate out of the top, the longer out of the bottom.
func TestThreeWaySplitCorners(t *testing.T) {
	_, _, lay := layoutOf(t, "split-three-corners.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }
	spine := cy("Gateway_ThreeWaySplit")

	if d := cy("Task_Approve") - spine; d > 0.5 || d < -0.5 {
		t.Errorf("happy path must run straight through the gateway (dy=%.0f)", d)
	}
	// Short alternate (2 tasks) above, long alternate (4 tasks) below.
	for _, id := range []string{"Task_ShortNotify", "Task_ShortClose", "End_Withdrawn"} {
		if cy(id) >= spine {
			t.Errorf("%s: shorter alternate must sit above the spine (cy=%.0f, spine=%.0f)", id, cy(id), spine)
		}
	}
	for _, id := range []string{"Task_LongCollect", "Task_LongArchive", "End_Disputed"} {
		if cy(id) <= spine {
			t.Errorf("%s: longer alternate must sit below the spine (cy=%.0f, spine=%.0f)", id, cy(id), spine)
		}
	}
	// The two alternates leave through opposite corners of the diamond.
	gw := lay.Shapes["Gateway_ThreeWaySplit"]
	if up := lay.Edges["Flow_split_short"]; len(up) == 0 || up[0].Y != gw.Y {
		t.Errorf("lifted branch must leave the gateway's top corner (y=%v, want %.0f)", up, gw.Y)
	}
	if dn := lay.Edges["Flow_split_long"]; len(dn) == 0 || dn[0].Y != gw.Bottom() {
		t.Errorf("below branch must leave the gateway's bottom corner (y=%v, want %.0f)", dn, gw.Bottom())
	}
}

// TestFourWaySplitAllBelow: rule B. With four or more outgoing flows the
// top corner stays unused and every alternate stacks below.
func TestFourWaySplitAllBelow(t *testing.T) {
	p, _, lay := layoutOf(t, "split-four-below.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }
	spine := cy("Gateway_FourWaySplit")

	for _, n := range p.Nodes {
		if cy(n.ID) < spine-0.5 {
			t.Errorf("%s sits above the spine; rule B forbids using the top corner", n.ID)
		}
	}
	rows := map[float64]bool{}
	for _, id := range []string{"Task_HoldInspect", "Task_ReturnLabel", "Task_CancelParcel"} {
		if cy(id) <= spine {
			t.Errorf("%s must hang below the spine", id)
		}
		rows[cy(id)] = true
	}
	if len(rows) != 3 {
		t.Errorf("the three alternates must occupy three distinct rows, got %d", len(rows))
	}
}

// TestBelowStackOrderedByLength: rule C. Below-alternates stack
// longest-first, so the longest sits directly under the spine.
func TestBelowStackOrderedByLength(t *testing.T) {
	_, _, lay := layoutOf(t, "below-stack-order.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }

	// Heads of the 4-, 3-, 2- and 1-task branches, longest first.
	order := []string{"Task_Four1", "Task_Three1", "Task_Two1", "Task_One1"}
	for i := 1; i < len(order); i++ {
		if cy(order[i]) <= cy(order[i-1]) {
			t.Errorf("%s (cy=%.0f) must sit below %s (cy=%.0f): longer branches stack nearer the spine",
				order[i], cy(order[i]), order[i-1], cy(order[i-1]))
		}
	}
}

// TestBranchEntryElbow: rule D. An alternate's entry edge drops in the
// gateway's own column and turns once into the head's left side.
func TestBranchEntryElbow(t *testing.T) {
	_, _, lay := layoutOf(t, "branch-entry-elbow.bpmn")
	gw := lay.Shapes["Gateway_ElbowSplit"]
	head := lay.Shapes["Task_AltReview"]
	pts := lay.Edges["Flow_elbow_alt"]

	if len(pts) != 3 {
		t.Fatalf("branch entry must be a 3-point elbow, got %d points: %v", len(pts), pts)
	}
	if pts[0].X != pts[1].X {
		t.Errorf("elbow must drop vertically (x %.1f -> %.1f)", pts[0].X, pts[1].X)
	}
	if d := pts[0].X - gw.CX(); d > 0.5 || d < -0.5 {
		t.Errorf("elbow must run in the gateway's own column (dx=%.1f)", d)
	}
	if pts[0].Y != gw.Bottom() {
		t.Errorf("elbow must leave the gateway's bottom corner (y=%.0f, want %.0f)", pts[0].Y, gw.Bottom())
	}
	if pts[2].X != head.X || pts[2].Y != head.Y+head.H/2 {
		t.Errorf("elbow must enter the head's left side (got %v, want %.0f,%.0f)",
			pts[2], head.X, head.Y+head.H/2)
	}
}

// TestBackEdgeRunsBelow: rule E. A way-back edge leaves the source's
// bottom, runs on a line below the rows, and rises into the target's bottom.
func TestBackEdgeRunsBelow(t *testing.T) {
	_, _, lay := layoutOf(t, "back-edge-below.bpmn")
	src := lay.Shapes["Task_Correct"]
	dst := lay.Shapes["Task_EnterData"]
	pts := lay.Edges["Flow_correct_back"]

	if len(pts) < 4 {
		t.Fatalf("way-back edge should have at least 4 points, got %v", pts)
	}
	if pts[0].Y != src.Bottom() {
		t.Errorf("must leave the source's bottom (y=%.0f, want %.0f)", pts[0].Y, src.Bottom())
	}
	if last := pts[len(pts)-1]; last.Y != dst.Bottom() {
		t.Errorf("must enter the target's bottom (y=%.0f, want %.0f)", last.Y, dst.Bottom())
	}
	if pts[1].Y <= src.Bottom() {
		t.Errorf("the way-back line must run below the source's row (y=%.0f, row bottom=%.0f)",
			pts[1].Y, src.Bottom())
	}
	// Nothing of it may stray above the rows it passes under.
	for _, pt := range pts {
		if pt.Y < dst.Y {
			t.Errorf("way-back edge must stay below the rows (y=%.0f)", pt.Y)
		}
	}
}

// TestLoopHeaderKeepsBodyStraight: rule B at a loop-header gateway. The
// branch whose subtree feeds the back edge continues straight on the spine
// row; the back edge owns the lane below it, so the loop exit leaves through
// the top corner and is routed above.
func TestLoopHeaderKeepsBodyStraight(t *testing.T) {
	_, _, lay := layoutOf(t, "tour-creation.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }
	gw := lay.Shapes["Gateway_batches"]

	for _, id := range []string{"Activity_create_tour", "Activity_update_status"} {
		if d := cy(id) - gw.CY(); d > 0.5 || d < -0.5 {
			t.Errorf("%s: the loop body must stay on the spine row (dy=%.0f)", id, d)
		}
	}
	if got := cy("Event_0ruyaxs"); got >= gw.CY() {
		t.Errorf("the loop exit must sit above the spine (cy=%.0f, spine=%.0f)", got, gw.CY())
	}
	exit := lay.Edges["Flow_no_batch"]
	if len(exit) < 2 {
		t.Fatalf("loop exit has too few waypoints: %v", exit)
	}
	if exit[0].X != gw.CX() || exit[0].Y != gw.Y {
		t.Errorf("the loop exit must leave the gateway's top corner (got %v, want %.0f,%.0f)",
			exit[0], gw.CX(), gw.Y)
	}
	if exit[1].X != exit[0].X || exit[1].Y >= exit[0].Y {
		t.Errorf("the loop exit must rise out of the gateway (%v -> %v)", exit[0], exit[1])
	}
	// The back edge keeps running below the spine and into the diamond's
	// bottom corner, which is why the exit had to go up.
	back := lay.Edges["Flow_loop_back"]
	if len(back) < 2 {
		t.Fatalf("back edge has too few waypoints: %v", back)
	}
	if last := back[len(back)-1]; last.X != gw.CX() || last.Y != gw.Bottom() {
		t.Errorf("the back edge must enter the gateway's bottom corner (got %v, want %.0f,%.0f)",
			last, gw.CX(), gw.Bottom())
	}
	for _, pt := range back {
		if pt.Y < gw.CY() {
			t.Errorf("the back edge must stay below the spine row (y=%.0f)", pt.Y)
		}
	}
}

// TestLiftedSubtreeStaysAbove: a lifted branch takes its whole subtree with
// it. If descendants fell back to the default downward placement they would
// land below the spine and their entry edge would have to cross it, which
// has no corridor — the router would fall through to a margin route.
func TestLiftedSubtreeStaysAbove(t *testing.T) {
	p, _, lay := layoutOf(t, "lifted-subtree.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }
	spine := cy("Gateway_Severity")

	// The lifted branch itself.
	for _, id := range []string{"Task_Escalate", "Gateway_Urgency", "Task_PageOncall", "End_Paged"} {
		if cy(id) >= spine {
			t.Errorf("%s: lifted branch must sit above the spine (cy=%.0f, spine=%.0f)", id, cy(id), spine)
		}
	}
	// Its descendant chain must inherit the upward direction, not fall back
	// below the spine.
	for _, id := range []string{"Task_QueueIncident", "End_Queued"} {
		if cy(id) >= spine {
			t.Errorf("%s: descendant of a lifted branch must stay above the spine (cy=%.0f, spine=%.0f)",
				id, cy(id), spine)
		}
	}
	// The longer alternate stays below.
	if cy("Task_Investigate") <= spine {
		t.Errorf("the longer alternate must hang below the spine (cy=%.0f)", cy("Task_Investigate"))
	}
	// No flow may be margin-routed across the diagram.
	minX := math.Inf(1)
	for _, n := range p.Nodes {
		minX = math.Min(minX, lay.Shapes[n.ID].X)
	}
	for _, fl := range p.Flows {
		for _, pt := range lay.Edges[fl.ID] {
			if pt.X < minX-1 {
				t.Errorf("%s is margin-routed at x=%.0f (leftmost node x=%.0f)", fl.ID, pt.X, minX)
				break
			}
		}
	}
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

	// The pick loop is a way-back edge: it leaves the gateway's bottom
	// corner, travels back on a line below the rows it passes under and
	// rises into its target's bottom. Nothing of it goes above the spine.
	loop := lay.Edges["Flow_0tcw3fm"]
	if len(loop) < 4 {
		t.Fatalf("pick loop should be a way-back route, got %v", loop)
	}
	src := lay.Shapes["Gateway_08wsh14"]
	tgt := lay.Shapes["Activity_1b1t68m"]
	rowBottom := math.Max(src.Bottom(), tgt.Bottom())

	if first := loop[0]; first.X != src.CX() || first.Y != src.Bottom() {
		t.Errorf("pick loop must leave the gateway's bottom corner (got %v, want %.0f,%.0f)",
			first, src.CX(), src.Bottom())
	}
	for _, pt := range loop {
		if pt.Y < spineY-layout.RowH/2 {
			t.Errorf("pick loop must stay below the spine's top (y=%.0f)", pt.Y)
		}
	}
	// The way-back line is the loop's longest horizontal run: it carries
	// the edge back across the diagram and must lie below the rows rather
	// than beside them. (The short jog off the diamond's corner is not it.)
	var line [2]layout.Point
	widest := 0.0
	for i := 0; i+1 < len(loop); i++ {
		if loop[i].Y != loop[i+1].Y {
			continue
		}
		if w := math.Abs(loop[i+1].X - loop[i].X); w > widest {
			widest, line = w, [2]layout.Point{loop[i], loop[i+1]}
		}
	}
	if line[1].X >= line[0].X {
		t.Errorf("the way-back line must run right to left (%v -> %v)", line[0], line[1])
	}
	if line[0].Y <= rowBottom {
		t.Errorf("the way-back line should run below its rows, not beside them (y=%.0f, rows end at %.0f)",
			line[0].Y, rowBottom)
	}
	// It ends by rising straight into the target's bottom.
	last, prev := loop[len(loop)-1], loop[len(loop)-2]
	if last.X != tgt.CX() || last.Y != tgt.Bottom() {
		t.Errorf("pick loop should enter the target's bottom (got %v, want %.0f,%.0f)",
			last, tgt.CX(), tgt.Bottom())
	}
	if prev.X != last.X || prev.Y <= last.Y {
		t.Errorf("pick loop should rise into its target (%v -> %v)", prev, last)
	}
}
