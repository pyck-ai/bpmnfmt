package format

import (
	"bytes"
	"fmt"
	"math"
	"path/filepath"
	"sort"
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
	"corridor-row-ranges.bpmn",
	"lift-only-terminal.bpmn",
	"cross-link-adjacent.bpmn",
	"gateway-cluster-columns.bpmn",
	"rejoin-bundle-lane.bpmn",
	"rejoin-right-then-up.bpmn",
	"loop-branch-backwards.bpmn",
	"sky-over-the-spine.bpmn",
	"rejoin-riser-depth.bpmn",
	"riser-depth-blocked.bpmn",
	"return-bundle-merge.bpmn",
	"sky-over-annotation.bpmn",
	"loop-return-lift.bpmn",
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

// TestLoopReturnLift: rule N3. A branch whose subtree has NO forward exit —
// every path ends in a way-back edge to a spine node earlier than its
// split — is a loop-return detour. Rule L3's retrograde walk is preferred,
// but when the exact fill fails the branch is LIFTED above the spine
// instead of hanging below: below-spine gaps belong to forward branches
// and their rejoins; the sky belongs to returns. The lifted body reads
// RIGHT TO LEFT (rule N3b): head one column left of the split, entry into
// its right face, return out of the tail's left face into the target's top.
func TestLoopReturnLift(t *testing.T) {
	_, _, lay := layoutOf(t, "loop-return-lift.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }
	spine := cy("G_Decide")

	// Only the entry hop reads leftwards by design; the back edge is not
	// retrograde — rule L3's walk stayed rejected.
	if len(lay.Retrograde) != 1 || !lay.Retrograde["Flow_decide_redo"] {
		t.Errorf("Retrograde should name exactly the entry flow, got %v", lay.Retrograde)
	}
	if cy("Task_Redo") >= spine {
		t.Errorf("Task_Redo must be lifted above the spine (cy=%.0f, spine=%.0f)",
			cy("Task_Redo"), spine)
	}
	redo, prepare, gw := lay.Shapes["Task_Redo"], lay.Shapes["Task_Prepare"], lay.Shapes["G_Decide"]
	if d := redo.CX() - (gw.CX() - layout.ColPitch); d > 0.5 || d < -0.5 {
		t.Errorf("the head must sit one column left of its split (cx=%.0f, want %.0f)",
			redo.CX(), gw.CX()-layout.ColPitch)
	}
	// The entry rises out of the gateway's top corner in its own column
	// and turns once into the head's right face.
	entry := lay.Edges["Flow_decide_redo"]
	if len(entry) != 3 {
		t.Fatalf("Flow_decide_redo should be a 3-point rise-then-left elbow, got %v", entry)
	}
	if entry[0].X != gw.CX() || entry[0].Y != gw.Y {
		t.Errorf("the entry must leave the gateway's top corner (got %v, want %.0f,%.0f)",
			entry[0], gw.CX(), gw.Y)
	}
	if entry[1].X != entry[0].X || entry[1].Y >= entry[0].Y {
		t.Errorf("the entry must rise in the gateway's column (%v -> %v)", entry[0], entry[1])
	}
	if last := entry[2]; last.X != redo.Right() || last.Y != entry[1].Y {
		t.Errorf("the entry must turn once into the head's right face (got %v, want %.0f,%.0f)",
			last, redo.Right(), entry[1].Y)
	}
	// The return leaves the tail's LEFT face, runs back along its own row
	// and drops once into the target's top — a single arrowhead.
	back := lay.Edges["Flow_redo_back"]
	if len(back) != 3 {
		t.Fatalf("Flow_redo_back should be a 3-point left-then-down elbow, got %v", back)
	}
	if back[0].X != redo.X || back[0].Y != redo.CY() {
		t.Errorf("Flow_redo_back must leave Task_Redo's left face (got %v, want %.0f,%.0f)",
			back[0], redo.X, redo.CY())
	}
	if back[1].Y != back[0].Y || back[1].X >= back[0].X {
		t.Errorf("Flow_redo_back must run leftwards along its own row (%v -> %v)", back[0], back[1])
	}
	last := back[2]
	if last.X != prepare.CX() || last.Y != prepare.Y {
		t.Errorf("Flow_redo_back must sink into Task_Prepare's top (got %v, want %.0f,%.0f)",
			last, prepare.CX(), prepare.Y)
	}
	if back[1].X != last.X {
		t.Errorf("Flow_redo_back must drop straight in the target's column (%v -> %v)", back[1], last)
	}
}

// TestBackEdgeRunsBelow: rule E. A way-back edge leaves the source's
// bottom, runs on a line below the rows, and rises into the target's
// bottom. The subtree here reaches a forward dead end too, so it is no
// pure loop-return: rule N3 does not lift it and the line stays below.
func TestBackEdgeRunsBelow(t *testing.T) {
	_, _, lay := layoutOf(t, "return-bundle-merge.bpmn")
	src := lay.Shapes["G_More"]
	dst := lay.Shapes["Task_Target"]
	pts := lay.Edges["Flow_more_back"]

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

// vertOverlap returns the length two axis-parallel segments share when both
// are vertical and sit in the same column (0 otherwise).
func vertOverlap(a1, a2, b1, b2 layout.Point) float64 {
	if math.Abs(a1.X-a2.X) > 0.5 || math.Abs(b1.X-b2.X) > 0.5 || math.Abs(a1.X-b1.X) > 0.5 {
		return 0
	}
	lo := math.Max(math.Min(a1.Y, a2.Y), math.Min(b1.Y, b2.Y))
	hi := math.Min(math.Max(a1.Y, a2.Y), math.Max(b1.Y, b2.Y))
	return hi - lo
}

// TestCorridorRowRanges: rule L2. A corridor reservation covers a column and
// the band of rows the vertical crosses, so no two verticals ever share a
// stroke — two lines in one column would merge into one and hide whatever
// arrowheads they carry. Verticals whose row bands are disjoint may share a
// column, which is what keeps rejoins from stepping sideways forever.
func TestCorridorRowRanges(t *testing.T) {
	p, _, lay := layoutOf(t, "corridor-row-ranges.bpmn")

	// UNDER-RESERVATION GUARD: no two distinct edges may run collinearly in
	// one column, unless they leave the same point (a split's shared trunk).
	var ids []string
	for _, fl := range p.Flows {
		if len(lay.Edges[fl.ID]) > 0 {
			ids = append(ids, fl.ID)
		}
	}
	sort.Strings(ids)
	for i, a := range ids {
		pa := lay.Edges[a]
		for _, b := range ids[i+1:] {
			pb := lay.Edges[b]
			if math.Abs(pa[0].X-pb[0].X) < 0.5 && math.Abs(pa[0].Y-pb[0].Y) < 0.5 {
				continue // one trunk, both edges peel off it
			}
			for x := 0; x+1 < len(pa); x++ {
				for y := 0; y+1 < len(pb); y++ {
					if ov := vertOverlap(pa[x], pa[x+1], pb[y], pb[y+1]); ov > 0.5 {
						t.Errorf("%s and %s share a %.0fpx vertical run at x=%.0f",
							a, b, ov, pa[x].X)
					}
				}
			}
		}
	}

	// No route may double back on itself: a horizontal run that reverses
	// direction is the staircase an under-reserved corridor produces.
	for _, id := range ids {
		pts := lay.Edges[id]
		prev := 0.0
		for i := 0; i+1 < len(pts); i++ {
			dx := pts[i+1].X - pts[i].X
			if math.Abs(dx) < 0.5 {
				continue
			}
			if prev != 0 && (dx > 0) != (prev > 0) {
				t.Errorf("%s reverses direction at %v: %v", id, pts[i], pts)
			}
			prev = dx
		}
	}

	// The loop's back edge leaves Task_Join's bottom, runs below every row
	// and rises into G_Top's bottom corner. Task_Join is reached by a second
	// inbound flow, so rule L3 does NOT lay this branch out backwards — the
	// way-back line stays where R6 puts it.
	gTop, tJoin := lay.Shapes["G_Top"], lay.Shapes["Task_Join"]
	back := lay.Edges["Flow_join_top"]
	if len(back) < 4 {
		t.Fatalf("Flow_join_top should be a way-back route, got %v", back)
	}
	if back[0].X != tJoin.CX() || back[0].Y != tJoin.Bottom() {
		t.Errorf("Flow_join_top must leave Task_Join's bottom (got %v)", back[0])
	}
	if last := back[len(back)-1]; last.X != gTop.CX() || last.Y != gTop.Bottom() {
		t.Errorf("Flow_join_top must enter G_Top's bottom corner (got %v, want %.0f,%.0f)",
			last, gTop.CX(), gTop.Bottom())
	}
	for _, pt := range back {
		if pt.Y < gTop.Bottom() {
			t.Errorf("Flow_join_top must stay below the rows it passes (y=%.0f, spine row ends at %.0f)",
				pt.Y, gTop.Bottom())
		}
	}
	lowest := 0.0
	for _, id := range []string{"G_Top", "G_Mid", "Task_After", "Task_Low", "Task_Join"} {
		lowest = math.Max(lowest, lay.Shapes[id].Bottom())
	}
	if back[1].Y <= lowest {
		t.Errorf("the way-back line should run below every row (y=%.0f, lowest shape ends at %.0f)",
			back[1].Y, lowest)
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

// TestRejoinRiserDepth: rule N1b. Two rejoins into one target's bottom face
// coming from DIFFERENT rows do not step apart — they merge onto a single
// riser standing in the target's own column, so the pair reads as one line
// growing as the second arc joins it and ending in one arrowhead. Depth
// still decides something, just not the column: the DEEPEST member owns the
// riser, because its run reaches furthest down and contains every shallower
// one. Rule M4's rank is suppressed for members that merge — there is no
// horizontal approach left to cut once the two share a line.
//
// Asserted by ROW, not by branch name: which alternate lands on which tier
// is rule C's business (longest-first), not N1b's.
func TestRejoinRiserDepth(t *testing.T) {
	_, _, lay := layoutOf(t, "rejoin-riser-depth.bpmn")
	join := lay.Shapes["G_Join"]

	type riser struct {
		id  string
		pts []layout.Point
	}
	var risers []riser
	for _, id := range []string{"Flow_near_join", "Flow_far2_join"} {
		pts := lay.Edges[id]
		if len(pts) != 3 {
			t.Fatalf("%s should be a 3-point right-then-up elbow, got %v", id, pts)
		}
		risers = append(risers, riser{id, pts})
	}
	shallow, deep := risers[0], risers[1]
	if shallow.pts[0].Y > deep.pts[0].Y {
		shallow, deep = deep, shallow
	}
	// Different rows: this is the cross-gap case rule M2's g1 key never
	// grouped, and the one rule M4 used to step apart.
	if math.Abs(shallow.pts[0].Y-deep.pts[0].Y) < 0.5 {
		t.Fatalf("the two rejoins must come from different rows (both at y=%.0f)",
			shallow.pts[0].Y)
	}

	// One riser column, and it is the target's OWN — not a slot beside it.
	if math.Abs(shallow.pts[1].X-deep.pts[1].X) > 0.5 {
		t.Errorf("the rejoins must share one riser column (got %.0f vs %.0f)",
			shallow.pts[1].X, deep.pts[1].X)
	}
	if math.Abs(deep.pts[1].X-join.CX()) > 0.5 {
		t.Errorf("the shared riser must stand in G_Join's own column (x=%.0f, cx=%.0f)",
			deep.pts[1].X, join.CX())
	}

	// One arrowhead: both end on the identical point, and on a diamond that
	// point is the bottom VERTEX — the merged entry carries no offset, so it
	// never slides along the slanted face (rule G6).
	es, ed := shallow.pts[2], deep.pts[2]
	if math.Abs(es.X-ed.X) > 0.5 || math.Abs(es.Y-ed.Y) > 0.5 {
		t.Errorf("the rejoins must share one entry point (got %v vs %v)", es, ed)
	}
	if math.Abs(es.X-join.CX()) > 0.5 || math.Abs(es.Y-join.Bottom()) > 0.5 {
		t.Errorf("the shared entry must sit on G_Join's bottom vertex (got %v, want %.0f,%.0f)",
			es, join.CX(), join.Bottom())
	}

	// The shallower run is CONTAINED in the deeper one: that containment is
	// what makes the two read as a single stroke rather than as two lines
	// that happen to overlap.
	if shallow.pts[1].Y >= deep.pts[1].Y {
		t.Errorf("%s (turns up at y=%.0f) must lie inside %s's run (turns up at y=%.0f)",
			shallow.id, shallow.pts[1].Y, deep.id, deep.pts[1].Y)
	}

	// Declared, never inferred: the shallower rejoin follows the deeper one,
	// which owns the riser.
	if lay.Merged[shallow.id] != deep.id {
		t.Errorf("%s should be declared merged into %s, got %v", shallow.id, deep.id, lay.Merged)
	}
	if lay.Merged[deep.id] != "" {
		t.Errorf("%s owns the riser and must follow nothing, got %q", deep.id, lay.Merged[deep.id])
	}
}

// TestRiserDepthBlocked: rule M4, the half rule N1b did not swallow. A
// rejoin whose target-column riser is blocked by a shape cannot lie on the
// bundle's line, so it keeps its own riser and its own arrowhead — and then
// the depth rank is what decides where that riser stands.
//
// Here a dead-end branch parks an end event in Settle's own column on the
// tier BETWEEN the two rejoins: the shallow one turns up above it and is
// unaffected, the deep one would have to pass straight through it. The deep
// rejoin must step RIGHT, never left, or its riser would cut the shallow
// one's horizontal approach. Its rank is what buys it the slot far enough
// out to clear the blocker: every offset a rank-0 rejoin may try still ends
// inside the blocker's clearance.
func TestRiserDepthBlocked(t *testing.T) {
	_, _, lay := layoutOf(t, "riser-depth-blocked.bpmn")
	settle := lay.Shapes["Task_Settle"]

	shallow := lay.Edges["Flow_shallow_join"] // rejoins from the upper tier
	deep := lay.Edges["Flow_deep_join"]       // rejoins from the lower tier
	if len(shallow) != 3 || len(deep) != 3 {
		t.Fatalf("both rejoins should be 3-point right-then-up elbows, got %v / %v", shallow, deep)
	}
	if shallow[0].Y >= deep[0].Y {
		t.Fatalf("Flow_deep_join's row (y=%.0f) must lie below Flow_shallow_join's (y=%.0f)",
			deep[0].Y, shallow[0].Y)
	}

	// The blocker is really in Settle's column, and really on a tier between
	// the two: that is what makes this the blocked case rather than a plain
	// two-rejoin bundle.
	blocker := lay.Shapes["End_Abort"]
	if math.Abs(blocker.CX()-settle.CX()) > 0.5 {
		t.Fatalf("End_Abort must sit in Settle's own column (cx=%.0f, want %.0f)",
			blocker.CX(), settle.CX())
	}
	if blocker.CY() <= shallow[0].Y || blocker.CY() >= deep[0].Y {
		t.Fatalf("End_Abort (y=%.0f) must sit between the two rejoin rows (%.0f and %.0f)",
			blocker.CY(), shallow[0].Y, deep[0].Y)
	}

	// The shallow rejoin is unobstructed, so it turns up in Settle's own
	// column exactly as rule L1 wants.
	if math.Abs(shallow[1].X-settle.CX()) > 0.5 {
		t.Errorf("Flow_shallow_join must turn up in Settle's own column (x=%.0f, cx=%.0f)",
			shallow[1].X, settle.CX())
	}
	// The deep one steps right — past the blocker, not around its left.
	if deep[1].X <= shallow[1].X {
		t.Errorf("the deeper rejoin must turn up right of the shallower: got x=%.0f vs %.0f",
			deep[1].X, shallow[1].X)
	}
	if deep[1].X < blocker.Right() {
		t.Errorf("Flow_deep_join's riser (x=%.0f) must clear the blocker (right=%.0f)",
			deep[1].X, blocker.Right())
	}

	// Two risers, two arrowheads, both on Settle's bottom face.
	for _, e := range []struct {
		id  string
		pts []layout.Point
	}{{"Flow_shallow_join", shallow}, {"Flow_deep_join", deep}} {
		last := e.pts[2]
		if math.Abs(last.Y-settle.Bottom()) > 0.5 {
			t.Errorf("%s must land on Settle's bottom face (y=%.0f, want %.0f)",
				e.id, last.Y, settle.Bottom())
		}
		if d := math.Abs(last.X - settle.CX()); d > settle.W/2-4+0.5 {
			t.Errorf("%s lands %.0fpx off Settle's center, past its bottom face", e.id, d)
		}
		if owner := lay.Merged[e.id]; owner != "" {
			t.Errorf("%s must keep its own riser, got merged into %q", e.id, owner)
		}
	}
	if math.Abs(shallow[2].X-deep[2].X) < 0.5 {
		t.Errorf("the two rejoins must carry separate arrowheads, both land at x=%.0f",
			shallow[2].X)
	}
}

// TestSkyOverTheSpine: rule M3. A same-row detour arches ABOVE its row when
// the sky over the spanned columns is free, and dips below when something
// occupies it. Below is the fallback, not a deprecated shape.
func TestSkyOverTheSpine(t *testing.T) {
	_, _, lay := layoutOf(t, "sky-over-the-spine.bpmn")
	spine := lay.Shapes["G_A"].CY()

	// The free-sky arc arches over the row.
	ga, gb := lay.Shapes["G_A"], lay.Shapes["G_B"]
	up := lay.Edges["Flow_a_b"]
	if len(up) != 4 {
		t.Fatalf("Flow_a_b should be a 4-point arch, got %v", up)
	}
	if up[0].X != ga.CX() || up[0].Y != ga.Y {
		t.Errorf("Flow_a_b must leave G_A's top corner (got %v, want %.0f,%.0f)",
			up[0], ga.CX(), ga.Y)
	}
	if up[1].Y >= up[0].Y {
		t.Errorf("Flow_a_b must rise out of the row (%v -> %v)", up[0], up[1])
	}
	for _, pt := range up {
		if pt.Y > spine {
			t.Errorf("Flow_a_b must stay above the spine (y=%.0f, spine=%.0f)", pt.Y, spine)
		}
	}
	if last := up[len(up)-1]; math.Abs(last.X-gb.CX()) > gb.W/2-4+0.5 || last.Y > gb.CY() {
		t.Errorf("Flow_a_b must land on G_B's top face (got %v, cx=%.0f)", last, gb.CX())
	}

	// The blocked arc dips below it — and the blocker is really up there,
	// really overlapping the span.
	blocker := lay.Shapes["Task_L"]
	gc, gd := lay.Shapes["G_C"], lay.Shapes["G_D"]
	if blocker.CY() >= spine {
		t.Fatalf("Task_L must be lifted into the sky (cy=%.0f, spine=%.0f)", blocker.CY(), spine)
	}
	if blocker.Right() < gc.CX() || blocker.X > gd.CX() {
		t.Fatalf("Task_L (%.0f..%.0f) must overlap the G_C..G_D span (%.0f..%.0f)",
			blocker.X, blocker.Right(), gc.CX(), gd.CX())
	}
	down := lay.Edges["Flow_c_d"]
	if len(down) != 4 {
		t.Fatalf("Flow_c_d should be a 4-point dip, got %v", down)
	}
	for _, pt := range down {
		if pt.Y < spine {
			t.Errorf("Flow_c_d must stay below the spine (y=%.0f, spine=%.0f)", pt.Y, spine)
		}
	}

	// Concentric arches: the CONTAINING arc sits further from the row.
	inner := lay.Edges["Flow_ac_ad"]
	if len(inner) != 4 {
		t.Fatalf("Flow_ac_ad should be a 4-point arch, got %v", inner)
	}
	if !(inner[1].X >= up[1].X && inner[2].X <= up[2].X) {
		t.Fatalf("Flow_ac_ad must be nested inside Flow_a_b (%v vs %v)", inner, up)
	}
	if up[1].Y >= inner[1].Y {
		t.Errorf("the containing arch must sit further from the row (outer y=%.0f, inner y=%.0f)",
			up[1].Y, inner[1].Y)
	}
}

// TestColumnGrid: rule M1 / README rule 2. Every flow-node center sits on
// the 160px column grid of its own component, so nodes on different rows
// line up vertically. The clearances the router reserves are minimums, not
// positions — a raw pixel distance must always be rounded out to a column.
func TestColumnGrid(t *testing.T) {
	onGrid := func(t *testing.T, scope string, ids []string, lay *layout.Result) {
		t.Helper()
		base := math.Inf(1)
		for _, id := range ids {
			if r, ok := lay.Shapes[id]; ok {
				base = math.Min(base, r.CX())
			}
		}
		if math.IsInf(base, 1) {
			return
		}
		for _, id := range ids {
			r, ok := lay.Shapes[id]
			if !ok {
				continue
			}
			off := math.Mod(r.CX()-base, layout.ColPitch)
			if off > 0.5 && off < layout.ColPitch-0.5 {
				t.Errorf("%s: %s is %.0fpx off the column grid (cx=%.0f, leftmost=%.0f)",
					scope, id, off, r.CX(), base)
			}
		}
	}

	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			p, g, lay := layoutOf(t, name)
			for ci, comp := range g.Components {
				var ids []string
				for _, n := range comp.Nodes {
					ids = append(ids, n.ID)
				}
				onGrid(t, fmt.Sprintf("component %d", ci), ids, lay)
			}
			// A sub-process interior is shifted into its container, so it
			// carries its own origin; check it against its own leftmost node.
			for _, n := range p.Nodes {
				if n.Sub == nil {
					continue
				}
				var ids []string
				for _, in := range n.Sub.Nodes {
					ids = append(ids, in.ID)
				}
				onGrid(t, "interior of "+n.ID, ids, lay)
			}
		})
	}
}

// TestRetrogradeLoopBranch: rule L3. A branch whose only exit is a back edge
// upstream on its own chain, with no sub-branches, is laid out right to
// left: the branch IS the way-back line, so the loop reads as a rectangle
// instead of a forward run with a return line slung underneath it.
func TestRetrogradeLoopBranch(t *testing.T) {
	_, _, lay := layoutOf(t, "loop-branch-backwards.bpmn")
	cx := func(id string) float64 { r := lay.Shapes[id]; return r.X + r.W/2 }

	if d := cx("Task_Log") - cx("G_Available"); d > 0.5 || d < -0.5 {
		t.Errorf("the branch head must sit in the split's column (dx=%.0f)", d)
	}
	if d := cx("Task_Wait") - cx("Task_Check"); d > 0.5 || d < -0.5 {
		t.Errorf("the branch tail must land in the loop target's column (dx=%.0f)", d)
	}

	gw, log := lay.Shapes["G_Available"], lay.Shapes["Task_Log"]
	entry := lay.Edges["Flow_gw_log"]
	if len(entry) != 2 {
		t.Fatalf("Flow_gw_log must drop straight into the head's top, got %v", entry)
	}
	if entry[0].X != gw.CX() || entry[0].Y != gw.Bottom() ||
		entry[1].X != log.CX() || entry[1].Y != log.Y {
		t.Errorf("Flow_gw_log must run from the split's bottom corner to the head's top (%v)", entry)
	}

	body := lay.Edges["Flow_log_wait"]
	if len(body) != 2 || body[1].X >= body[0].X {
		t.Errorf("the branch body must read right to left (%v)", body)
	}

	wait, check := lay.Shapes["Task_Wait"], lay.Shapes["Task_Check"]
	back := lay.Edges["Flow_wait_back"]
	if len(back) != 2 {
		t.Fatalf("Flow_wait_back must be a straight rise, got %v", back)
	}
	if back[0].X != wait.CX() || back[0].Y != wait.Y ||
		back[1].X != check.CX() || back[1].Y != check.Bottom() {
		t.Errorf("Flow_wait_back must rise from the tail's top into the target's bottom (%v)", back)
	}

	// The leftward runs are exempted by name, not by disabling the check.
	if got := len(lay.Retrograde); got != 1 {
		t.Errorf("Retrograde should name the chain's 1 internal flow, got %d: %v", got, lay.Retrograde)
	}
	if !lay.Retrograde["Flow_log_wait"] {
		t.Error("Flow_log_wait must be registered as retrograde")
	}
}

// TestBackEdgeBelowStaysBelow is rule L3's negative control and is
// load-bearing: back-edge-below has one branch node across a two-column
// loop, so the fill condition rejects it — the branch is never walked
// backwards. If the fill condition is ever loosened, this trips. The
// rejected branch is a pure loop-return, so rule N3 lifts it above the
// spine, reading right to left (N3b): head one column left of the split,
// return out of the left face into the target's top.
func TestBackEdgeBelowStaysBelow(t *testing.T) {
	_, _, lay := layoutOf(t, "back-edge-below.bpmn")
	// Only the entry hop is registered as leftward-by-design; the back
	// edge itself is not retrograde — rule L3's walk stayed rejected.
	if len(lay.Retrograde) != 1 || !lay.Retrograde["Flow_valid_no"] {
		t.Errorf("Retrograde should name exactly the entry flow, got %v", lay.Retrograde)
	}
	src, dst := lay.Shapes["Task_Correct"], lay.Shapes["Task_EnterData"]
	gw := lay.Shapes["Gateway_Valid"]
	if src.CY() >= dst.CY() {
		t.Errorf("the rejected loop-return must be lifted above the spine (src cy=%.0f, spine cy=%.0f)",
			src.CY(), dst.CY())
	}
	if d := src.CX() - (gw.CX() - layout.ColPitch); d > 0.5 || d < -0.5 {
		t.Errorf("the head must sit one column left of its split (cx=%.0f, want %.0f)",
			src.CX(), gw.CX()-layout.ColPitch)
	}
	pts := lay.Edges["Flow_correct_back"]
	if len(pts) != 3 {
		t.Fatalf("the return should be a 3-point left-then-down elbow, got %v", pts)
	}
	if pts[0].X != src.X || pts[0].Y != src.CY() {
		t.Errorf("the return must leave the lifted source's left face (got %v, want %.0f,%.0f)",
			pts[0], src.X, src.CY())
	}
	if pts[1].Y != pts[0].Y || pts[1].X >= pts[0].X {
		t.Errorf("the return must run leftwards along its own row (%v -> %v)", pts[0], pts[1])
	}
	if last := pts[2]; last.X != dst.CX() || last.Y != dst.Y {
		t.Errorf("the return must sink into the target's top (got %v, want %.0f,%.0f)",
			last, dst.CX(), dst.Y)
	}
}

// TestRejoinRightThenUp: rule L1. A rejoin leaves its source's right border,
// runs along its own row and turns once in the target's column, entering the
// target's bottom face. An activity tail is de-aligned to leave room for
// that turn; a gateway tail, having no flat right border, stays aligned and
// leaves its top corner.
func TestRejoinRightThenUp(t *testing.T) {
	_, _, lay := layoutOf(t, "rejoin-right-then-up.bpmn")
	altB, join, check := lay.Shapes["Task_AltB"], lay.Shapes["G_Join"], lay.Shapes["G_Check"]

	// The activity tail stops short, with a full gap to turn up in.
	if altB.Right()+layout.GapX > join.X+0.5 {
		t.Errorf("Task_AltB (ends %.0f) must clear G_Join (starts %.0f) by GapX=%.0f",
			altB.Right(), join.X, layout.GapX)
	}
	pts := lay.Edges["Flow_altB_join"]
	if len(pts) != 3 {
		t.Fatalf("Flow_altB_join must be a 3-point right-then-up elbow, got %v", pts)
	}
	if pts[0].X != altB.Right() || pts[0].Y != altB.CY() {
		t.Errorf("Flow_altB_join must leave the tail's right border (got %v, want %.0f,%.0f)",
			pts[0], altB.Right(), altB.CY())
	}
	if pts[1].Y != pts[0].Y || pts[1].X != pts[2].X {
		t.Errorf("Flow_altB_join must run along its row then turn once up (%v)", pts)
	}
	if d := math.Abs(pts[2].X - join.CX()); d > join.W/2-4+0.5 {
		t.Errorf("Flow_altB_join must turn inside G_Join's bottom face (x=%.0f, cx=%.0f)",
			pts[2].X, join.CX())
	}
	if pts[2].Y > join.Bottom()+0.5 || pts[2].Y >= pts[1].Y {
		t.Errorf("Flow_altB_join must rise into G_Join's bottom (%v)", pts)
	}

	// The gateway tail keeps the aligned vertical out of its top corner.
	if d := check.CX() - join.CX(); d > 0.5 || d < -0.5 {
		t.Errorf("G_Check must stay aligned under G_Join (dx=%.0f)", d)
	}
	cj := lay.Edges["Flow_check_join"]
	if len(cj) < 2 {
		t.Fatalf("Flow_check_join has too few waypoints: %v", cj)
	}
	if cj[0].X != check.CX() || cj[0].Y != check.Y {
		t.Errorf("Flow_check_join must leave G_Check's top corner (got %v, want %.0f,%.0f)",
			cj[0], check.CX(), check.Y)
	}
}

// TestNoRiserOutOfAnActivityTop: rule L1's negative half, over every fixture.
// A line leaving the flat top edge of a rectangle reads as a different kind
// of connection than the sequence flow it is; rectangles are left through
// their right border.
func TestNoRiserOutOfAnActivityTop(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			p, g, lay := layoutOf(t, name)
			for _, sc := range []*model.Process{p} {
				for _, fl := range sc.Flows {
					n := sc.NodeByID[fl.SourceRef]
					if n == nil || n.Kind.IsEvent() || n.Kind.IsGateway() {
						continue
					}
					if g.Back[fl.ID] {
						// Back edges are rule R6's, not L1's: a retrograde
						// branch closes its loop by rising out of its tail
						// (rule L3), which is the shape that reads as "and
						// now back to here".
						continue
					}
					pts := lay.Edges[fl.ID]
					if len(pts) < 2 {
						continue
					}
					src := lay.Shapes[fl.SourceRef]
					if pts[0].Y == src.Y && pts[1].X == pts[0].X && pts[1].Y < pts[0].Y {
						t.Errorf("%s rises out of %s's top edge at %v", fl.ID, fl.SourceRef, pts[0])
					}
				}
			}
		})
	}
}

// TestNoDownwardRejoin is the L5/L1 interlock: L5 guarantees no chain tail
// rejoins downward, which is why L1 needs no right-then-DOWN shape. An edge
// arriving at a bottom face must therefore always be rising.
func TestNoDownwardRejoin(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			p, _, lay := layoutOf(t, name)
			for _, fl := range p.Flows {
				pts := lay.Edges[fl.ID]
				if len(pts) < 2 {
					continue
				}
				dst := lay.Shapes[fl.TargetRef]
				last, prev := pts[len(pts)-1], pts[len(pts)-2]
				if last.Y < dst.CY() || last.Y > dst.Bottom()+0.5 {
					continue // not a bottom-face arrival
				}
				if prev.Y < last.Y-0.5 {
					t.Errorf("%s enters %s's bottom face from ABOVE (%v -> %v): "+
						"a downward rejoin would need a right-then-down shape",
						fl.ID, fl.TargetRef, prev, last)
				}
			}
		})
	}
}

// TestRejoinBundleShareLane: rule L6. Runs in one gap that end at the same
// target and rise into it are one bundle: they share a lane, so what the
// reader sees is a single line beside the row with short risers peeling off
// at the target's face, not a staircase of near-identical arcs.
func TestRejoinBundleShareLane(t *testing.T) {
	_, _, lay := layoutOf(t, "rejoin-bundle-lane.bpmn")
	arcs := []string{"Flow_g1_join", "Flow_g2_join", "Flow_g3_join"}

	// The long run of each arc sits at one shared y.
	var laneY []float64
	var riser []float64
	for _, id := range arcs {
		pts := lay.Edges[id]
		if len(pts) != 4 {
			t.Fatalf("%s should be a 4-point arc, got %v", id, pts)
		}
		best, y, x := 0.0, 0.0, 0.0
		for i := 0; i+1 < len(pts); i++ {
			if math.Abs(pts[i].Y-pts[i+1].Y) > 0.5 {
				continue
			}
			if w := math.Abs(pts[i+1].X - pts[i].X); w > best {
				best, y, x = w, pts[i].Y, pts[i+1].X
			}
		}
		laneY = append(laneY, y)
		riser = append(riser, x)
	}
	for i := 1; i < len(laneY); i++ {
		if math.Abs(laneY[i]-laneY[0]) > 0.5 {
			t.Errorf("%s runs at y=%.0f but %s runs at y=%.0f: one bundle, one lane",
				arcs[i], laneY[i], arcs[0], laneY[0])
		}
	}

	// ONE riser, in the target's own column, and one arrowhead: every arc
	// ends at the identical point on G_Join's top CORNER (the sky over the
	// span is free, so the bundle arches over the row — rules M3/N2).
	// Offset 0 means no stub, so this is the corner itself and not a point
	// on a face.
	join := lay.Shapes["G_Join"]
	for i, x := range riser {
		if math.Abs(x-join.CX()) > 0.5 {
			t.Errorf("%s rises at x=%.0f, not in G_Join's own column (cx=%.0f): "+
				"a bundle enters as one arrow", arcs[i], x, join.CX())
		}
	}
	want := lay.Edges[arcs[0]][len(lay.Edges[arcs[0]])-1]
	if math.Abs(want.X-join.CX()) > 0.5 || math.Abs(want.Y-join.Y) > 0.5 {
		t.Errorf("the bundle must land on G_Join's top corner (got %v, want %.0f,%.0f)",
			want, join.CX(), join.Y)
	}
	for _, id := range arcs[1:] {
		pts := lay.Edges[id]
		if last := pts[len(pts)-1]; math.Abs(last.X-want.X) > 0.5 || math.Abs(last.Y-want.Y) > 0.5 {
			t.Errorf("%s ends at %v, not on the bundle's shared point %v", id, last, want)
		}
	}

	// The merge is declared, and declared exactly once per follower.
	if len(lay.Merged) != 2 {
		t.Errorf("Merged should hold the 2 followers, got %d: %v", len(lay.Merged), lay.Merged)
	}
	for _, id := range arcs[1:] {
		if lay.Merged[id] != arcs[0] {
			t.Errorf("%s should be declared merged into %s, got %q", id, arcs[0], lay.Merged[id])
		}
	}
}

// TestReturnBundleMergeAcrossGaps: rule M2b. Way-back edges entering the
// same node face merge into ONE riser at the target's column even when they
// lie in DIFFERENT gaps: the shallower return's riser is contained in the
// deeper one's, so the deeper return owns the trunk and both end on one
// point with one arrowhead.
func TestReturnBundleMergeAcrossGaps(t *testing.T) {
	_, _, lay := layoutOf(t, "return-bundle-merge.bpmn")
	target := lay.Shapes["Task_Target"]

	shallow := lay.Edges["Flow_retry_back"] // returns from row 1
	deep := lay.Edges["Flow_more_back"]     // returns from row 2
	if len(shallow) < 4 || len(deep) < 4 {
		t.Fatalf("both returns should be way-back routes, got %v / %v", shallow, deep)
	}
	// The two returns lie in different gaps: their lines run at different
	// depths, which is what makes this a CROSS-gap merge.
	if ys, yd := shallow[1].Y, deep[1].Y; yd <= ys {
		t.Fatalf("Flow_more_back's line (y=%.0f) must run below Flow_retry_back's (y=%.0f)", yd, ys)
	}
	// One shared entry point on the target's bottom, in its own column.
	es, ed := shallow[len(shallow)-1], deep[len(deep)-1]
	if math.Abs(es.X-ed.X) > 0.5 || math.Abs(es.Y-ed.Y) > 0.5 {
		t.Errorf("the returns must share one entry point (got %v vs %v)", es, ed)
	}
	if math.Abs(es.X-target.CX()) > 0.5 || math.Abs(es.Y-target.Bottom()) > 0.5 {
		t.Errorf("the shared entry must sit on Task_Target's bottom center (got %v, want %.0f,%.0f)",
			es, target.CX(), target.Bottom())
	}
	// The merge is declared: the shallower return follows the deeper one,
	// which reaches furthest down and owns the riser.
	if lay.Merged["Flow_retry_back"] != "Flow_more_back" {
		t.Errorf("Flow_retry_back should be declared merged into Flow_more_back, got %v", lay.Merged)
	}
}

// TestRejoinBundleMergeAcrossGaps: rule N1b, the forward counterpart of
// M2b. Several forward rejoins arriving at one target's bottom face merge
// into ONE riser in the target's column even though their horizontal runs
// lie on DIFFERENT rows, so rule M2's g1 key never grouped them and rule M4
// used to step them apart. The deepest rejoin owns the riser and the
// shallower one's final run is contained in it.
//
// The gateway's THIRD inflow is the discriminator: it arrives along the
// spine into the LEFT face, so it must be left exactly where it is. A left
// -face spine arrival is not a riser and never joins a bundle.
func TestRejoinBundleMergeAcrossGaps(t *testing.T) {
	_, _, lay := layoutOf(t, "001_mc_workflow_assigned.bpmn")
	join := lay.Shapes["Gateway_08wsh14"]

	shallow := lay.Edges["Flow_08gou0k"] // rejoins from the upper tier
	deep := lay.Edges["Flow_0rm1m90"]    // rejoins from the lower tier
	if len(shallow) != 3 || len(deep) != 3 {
		t.Fatalf("both rejoins should be 3-point right-then-up elbows, got %v / %v", shallow, deep)
	}
	// Different rows: this is what makes the merge a CROSS-gap one.
	if ys, yd := shallow[0].Y, deep[0].Y; yd <= ys {
		t.Fatalf("Flow_0rm1m90's row (y=%.0f) must lie below Flow_08gou0k's (y=%.0f)", yd, ys)
	}
	// One shared riser column and one shared terminal point. On a diamond
	// that point is the bottom VERTEX: the merged entry carries no offset,
	// so it never slides along the slanted face (rule G6).
	if math.Abs(shallow[1].X-deep[1].X) > 0.5 {
		t.Errorf("the rejoins must share one riser column (got %.0f vs %.0f)", shallow[1].X, deep[1].X)
	}
	es, ed := shallow[2], deep[2]
	if math.Abs(es.X-ed.X) > 0.5 || math.Abs(es.Y-ed.Y) > 0.5 {
		t.Errorf("the rejoins must share one entry point (got %v vs %v)", es, ed)
	}
	if math.Abs(es.X-join.CX()) > 0.5 || math.Abs(es.Y-join.Bottom()) > 0.5 {
		t.Errorf("the shared entry must sit on the gateway's bottom vertex (got %v, want %.0f,%.0f)",
			es, join.CX(), join.Bottom())
	}
	// Declared, never inferred: the shallower rejoin follows the deeper one.
	if lay.Merged["Flow_08gou0k"] != "Flow_0rm1m90" {
		t.Errorf("Flow_08gou0k should be declared merged into Flow_0rm1m90, got %v", lay.Merged)
	}
	// The spine arrival is untouched — straight along its row into the
	// gateway's left face, sharing nothing with the bundle.
	spine := lay.Edges["Flow_08lrpnf"]
	if len(spine) != 2 {
		t.Fatalf("Flow_08lrpnf should run straight along the spine, got %v", spine)
	}
	if last := spine[1]; math.Abs(last.X-join.X) > 0.5 || math.Abs(last.Y-join.CY()) > 0.5 {
		t.Errorf("Flow_08lrpnf must enter the gateway's left face (got %v, want %.0f,%.0f)",
			last, join.X, join.CY())
	}
	if lay.Merged["Flow_08lrpnf"] != "" {
		t.Errorf("a left-face spine arrival must never join a riser bundle, got %v",
			lay.Merged["Flow_08lrpnf"])
	}
}

// TestAccidentalPileupStillFails is M2's load-bearing negative control. Two
// INDEPENDENT inflows sharing a terminal point is the round-0 pileup bug;
// only a DECLARED merge may share one. A geometric test — "collinear final
// run, so it must be deliberate" — would have excused that very bug, since
// process.joining-flows' two inflows were already collinear over their last
// 25px before it was fixed.
func TestAccidentalPileupStillFails(t *testing.T) {
	for _, name := range fixtureNames {
		t.Run(name, func(t *testing.T) {
			p, _, lay := layoutOf(t, name)
			var ids []string
			for _, fl := range p.Flows {
				if len(lay.Edges[fl.ID]) > 0 {
					ids = append(ids, fl.ID)
				}
			}
			sort.Strings(ids)
			for i, a := range ids {
				pa := lay.Edges[a]
				for _, b := range ids[i+1:] {
					// The exemption is the DECLARATION, nothing else.
					if lay.Merged[a] == b || lay.Merged[b] == a ||
						(lay.Merged[a] != "" && lay.Merged[a] == lay.Merged[b]) {
						continue
					}
					pb := lay.Edges[b]
					ea, eb := pa[len(pa)-1], pb[len(pb)-1]
					if math.Abs(ea.X-eb.X) < 0.5 && math.Abs(ea.Y-eb.Y) < 0.5 {
						t.Errorf("%s and %s pile up on %v without a declared merge", a, b, ea)
					}
				}
			}
		})
	}
}

// TestGatewayClusterColumns: rule L4. Consecutive gateways form one cluster
// and share one branch-head column — the column of the first non-gateway
// node following the cluster on the parent chain. G1 and G2 sit back to
// back, so aligning each split to its own immediate successor would put
// G1's alternate a column left of G2's two.
func TestGatewayClusterColumns(t *testing.T) {
	_, _, lay := layoutOf(t, "gateway-cluster-columns.bpmn")
	cx := func(id string) float64 { r := lay.Shapes[id]; return r.X + r.W/2 }

	want := cx("Task_Main")
	for _, id := range []string{"Task_A", "Task_B", "Task_C"} {
		if d := cx(id) - want; d > 0.5 || d < -0.5 {
			t.Errorf("%s must align to the column after the gateway cluster (cx=%.0f, want %.0f)",
				id, cx(id), want)
		}
	}
	// The alignment carries through the rows: the end events line up too.
	wantEnd := cx("End_Main")
	for _, id := range []string{"End_A", "End_B", "End_C"} {
		if d := cx(id) - wantEnd; d > 0.5 || d < -0.5 {
			t.Errorf("%s must line up with End_Main (cx=%.0f, want %.0f)", id, cx(id), wantEnd)
		}
	}
	// The cluster itself is two gateways deep, which is what makes this
	// different from the cluster-size-1 case in branch-entry-elbow.
	if cx("G1") >= cx("G2") || cx("G2") >= want {
		t.Errorf("fixture no longer has a two-gateway cluster before the alignment column (G1=%.0f, G2=%.0f, col=%.0f)",
			cx("G1"), cx("G2"), want)
	}
}

// assertNoMarginRoute fails when any flow strays left of the leftmost shape,
// which is what a margin route around the whole diagram looks like.
func assertNoMarginRoute(t *testing.T, p *model.Process, lay *layout.Result) {
	t.Helper()
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

// TestCrossLinkedChainContinuation: rule L7. A non-spine chain walk reaching
// a split continues into the successor whose subtree links back into
// already-chained territory, not the first-declared one. Task_L is declared
// first on purpose, so the two readings disagree.
func TestCrossLinkedChainContinuation(t *testing.T) {
	p, _, lay := layoutOf(t, "cross-link-adjacent.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }

	// Task_H carries the cross link into the spine, so it continues Task_F's
	// chain; Task_L (first-declared, no link back) becomes the branch below.
	if d := cy("Task_H") - cy("Task_F"); d > 0.5 || d < -0.5 {
		t.Errorf("Task_H must continue Task_F's row (dy=%.0f): the cross-linked successor wins", d)
	}
	if cy("Task_L") <= cy("Task_F")+0.5 {
		t.Errorf("Task_L must drop below Task_F's row (L=%.0f, F=%.0f)", cy("Task_L"), cy("Task_F"))
	}

	// The cross link is a short elbow, not a trip around the diagram.
	link := lay.Edges["Flow_H_I"]
	if len(link) > 4 {
		t.Errorf("Flow_H_I should be a short elbow, got %d waypoints: %v", len(link), link)
	}
	assertNoMarginRoute(t, p, lay)
}

// TestLiftOnlyTerminalBranches: rule L5. A branch routes above its split
// only when its whole subtree is terminal. Both alternates here re-merge at
// G_Join, so neither may lift however short it is — a lifted branch would
// have to come back down across the spine to reach its merge node.
func TestLiftOnlyTerminalBranches(t *testing.T) {
	p, _, lay := layoutOf(t, "lift-only-terminal.bpmn")
	cy := func(id string) float64 { r := lay.Shapes[id]; return r.Y + r.H/2 }
	spine := cy("G_Split")

	for _, n := range p.Nodes {
		if cy(n.ID) < spine-0.5 {
			t.Errorf("%s sits above the split (cy=%.0f, split=%.0f); a re-merging branch must hang below",
				n.ID, cy(n.ID), spine)
		}
	}
	// The two re-merging alternates stack on two distinct rows below.
	a, b := cy("Task_A1"), cy("Task_B1")
	if a <= spine || b <= spine {
		t.Errorf("both alternates must hang below the spine (A1=%.0f, B1=%.0f, spine=%.0f)", a, b, spine)
	}
	if math.Abs(a-b) < 0.5 {
		t.Errorf("the two alternates must occupy distinct rows (both cy=%.0f)", a)
	}
	if cy("Task_A2") != a || cy("Task_B2") != b {
		t.Error("each alternate must keep its two tasks on one row")
	}
	// Both rejoins rise into G_Join from below.
	join := lay.Shapes["G_Join"]
	for _, id := range []string{"Flow_a2_join", "Flow_b2_join"} {
		pts := lay.Edges[id]
		if len(pts) < 2 {
			t.Fatalf("%s has too few waypoints: %v", id, pts)
		}
		last, prev := pts[len(pts)-1], pts[len(pts)-2]
		if last.Y > join.Bottom()+0.5 || prev.Y <= last.Y {
			t.Errorf("%s must rise into G_Join from below (%v -> %v, gateway bottom %.0f)",
				id, prev, last, join.Bottom())
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
// clear of their rows (below between rows, above for a free-sky same-row
// loop), annotations above their anchors.
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
	} {
		if cy(id) <= spineY+layout.RowH/2 {
			t.Errorf("%s should hang below the spine (cy=%.0f, spine=%.0f)", id, cy(id), spineY)
		}
	}

	// The replenish/insert-shortfall branch exists only to return: every
	// path ends in the back edge to the pick-source scan, an earlier spine
	// node. Rule N3 lifts it above the spine.
	for _, id := range []string{"Activity_0e37vw3", "Activity_insert_shortfall"} {
		if cy(id) >= spineY-layout.RowH/2 {
			t.Errorf("%s is a loop-return branch and should sit above the spine (cy=%.0f, spine=%.0f)",
				id, cy(id), spineY)
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

	// The pick loop is a same-row way-back edge. Its sky is occupied: the
	// lifted loop-return body (rule N3b) sits one column left of its
	// split, inside the loop's span. So the loop keeps rule 6's fallback —
	// it leaves the gateway's bottom corner, travels back on a line below
	// the rows it passes under and rises into its target's bottom.
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

	// The lifted body's return takes the sky instead: out of the tail's
	// left face, back along its own row, one drop into the target's top.
	ret := lay.Edges["Flow_insert_to_src"]
	if len(ret) != 3 {
		t.Fatalf("Flow_insert_to_src should be a 3-point left-then-down elbow, got %v", ret)
	}
	tail := lay.Shapes["Activity_insert_shortfall"]
	if ret[0].X != tail.X || ret[0].Y != tail.CY() {
		t.Errorf("Flow_insert_to_src must leave the tail's left face (got %v, want %.0f,%.0f)",
			ret[0], tail.X, tail.CY())
	}
	if last := ret[2]; last.X != tgt.CX() || last.Y != tgt.Y {
		t.Errorf("Flow_insert_to_src must sink into the target's top (got %v, want %.0f,%.0f)",
			last, tgt.CX(), tgt.Y)
	}

	// Rule L2: the shortage rejoin owns one column for the rows it crosses
	// and enters the gateway in that same column, so it is a single jog and
	// a straight run rather than a staircase that steps back and forth.
	rejoin := lay.Edges["Flow_0rm1m90"]
	if len(rejoin) > 4 {
		t.Errorf("Flow_0rm1m90 should need at most 4 waypoints, got %d: %v", len(rejoin), rejoin)
	}
	prevDX := 0.0
	for i := 0; i+1 < len(rejoin); i++ {
		dx := rejoin[i+1].X - rejoin[i].X
		if math.Abs(dx) < 0.5 {
			continue
		}
		if prevDX != 0 && (dx > 0) != (prevDX > 0) {
			t.Errorf("Flow_0rm1m90 reverses direction at %v: %v", rejoin[i], rejoin)
		}
		prevDX = dx
	}
	if n := len(rejoin); n >= 2 && math.Abs(rejoin[n-1].X-rejoin[n-2].X) > 0.5 {
		t.Errorf("Flow_0rm1m90 should approach the gateway straight (%v -> %v)",
			rejoin[n-2], rejoin[n-1])
	}
}
