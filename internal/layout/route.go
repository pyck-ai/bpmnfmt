package layout

import (
	"fmt"
	"math"
	"sort"
)

// Side of a node shape.
type nodeSide int

const (
	sTop nodeSide = iota
	sBottom
	sLeft
	sRight
)

type sideKey struct {
	node string
	side nodeSide
}

// docking is one edge endpoint on a node side. Offsets are relative to the
// node center along the side's own axis (x on top/bottom, y on the left) and
// resolved after all plans exist. Two edges may not share one docking point:
// an arrowhead landing where another already sits is invisible.
type docking struct {
	edge string
	side nodeSide
	// approach orders dockings along the side: the x of the far end for
	// top/bottom, the row the edge comes from for the left side.
	approach float64
	pinned   bool // must sit exactly at fixed (alignment / corridor)
	fixed    float64
	off      float64 // resolved
	stub     bool    // touch the shape at its corner and jog to off (gateways)
	// shared marks a split's own trunk: every alternate of one gateway
	// leaves the same corner in the same column by design and peels off at
	// its own row, so these dockings are not spread apart.
	shared bool
	// merged marks a rejoin bundle's shared riser: every member of the
	// bundle enters the target on the same point, so the reader sees one
	// line growing as each arc joins it and exactly one arrowhead.
	merged bool
}

// laneSeg is a horizontal run inside a gap channel. marginSentinel stands in
// for the (not yet known) margin corridor x during lane packing.
type laneSeg struct {
	edge   string
	x1, x2 float64
	lane   int
	// sky marks a run arching OVER its row rather than dipping under it
	// (rule M3). Sky and dip runs share a gap but never contend for lanes.
	sky bool
}

const marginSentinel = -100.0

func (s *laneSeg) lo() float64 { return math.Min(s.x1, s.x2) }
func (s *laneSeg) hi() float64 { return math.Max(s.x1, s.x2) }

// corridor reserves a column over the band of rows a vertical traverses
// (lo..hi inclusive). Two lines sharing one column merge into a single
// stroke, and whatever arrowheads they carry stack on top of it — but only
// where they actually overlap, so verticals whose row bands are disjoint may
// share a column.
type corridor struct {
	x      float64
	lo, hi int
}

type planKind int

const (
	pkH          planKind = iota // straight horizontal along a row
	pkVDown                      // straight vertical, source above target
	pkVUp                        // straight vertical, source below target
	pkDownLeftIn                 // exit bottom, drop beside the source, enter left side
	pkUpLeftIn                   // exit top, rise beside the source, enter left side
	pkDownJog                    // exit bottom, lane below source row, corridor down, enter left side
	pkDownTop                    // exit bottom, lane below source row, corridor down, lane above target row, enter top
	pkRightUp                    // exit right, horizontal to the target column, straight up into the bottom
	pkUpBottom                   // exit top, corridor up (optional jog), lane below target row, enter bottom
	pkRootMerge                  // exit right, bend before the target, enter left side
	pkUnderRow                   // exit bottom, lane below own row, enter target bottom
	pkBackBottom                 // exit bottom, way-back line below the lower row, corridor up, enter bottom
	pkBackMargin                 // around the outside via the margin corridor
	pkHLeft                      // straight horizontal along a row, right to left (retrograde chain)
	pkOverRow                    // exit top, lane above own row, enter target top (sky skip arc)
	pkBackTop                    // exit top, way-back line above the row, enter target top (sky loop)
	pkUpRightIn                  // exit top, rise beside the source, enter right side (sky-retro entry)
	pkLeftDown                   // exit left, horizontal to the target column, straight down into the top
)

type edgePlan struct {
	id        string
	kind      planKind
	src, dst  string
	exit      *docking
	entry     *docking
	seg1      *laneSeg
	seg2      *laneSeg
	g1, g2    int
	corrX     float64
	marginIdx int
	backEntry bool // pkBackMargin: enter the target's bottom (way-back edges)
}

// detourGroup is a set of same-row detours sharing one target: they leave
// their row, travel, and come back into the same node, so they move as a
// unit when the router decides whether to arch over the row or dip under
// it. Grouping by target also gives later rules a per-target view of the
// source rows involved.
type detourGroup struct {
	dst    string
	flows  []string
	rows   []int // source row per flow, same order
	row    int   // the shared row
	lo, hi float64
	risers [2]float64
}

// sameRowDetourGroups collects the flows whose source and target sit on one
// row and which therefore have to leave the row and come back: a forward
// skip arc over intervening nodes, or a way-back edge between two nodes of
// one row. They are grouped by target and returned in a deterministic order.
func (cl *compLayout) sameRowDetourGroups() []*detourGroup {
	byKey := map[string]*detourGroup{}
	var order []*detourGroup
	for _, fl := range cl.componentFlows() {
		src, dst := fl.SourceRef, fl.TargetRef
		sr, dr := cl.rowOf[src], cl.rowOf[dst]
		if sr != dr {
			continue
		}
		switch classify(cl.g, cl.chains, cl.chainOf, fl) {
		case fcExit, fcCross:
			if !cl.nodesBetween(sr, cl.right(src), cl.left(dst)) {
				continue // runs straight along the row; no detour needed
			}
		case fcBack:
			if cl.retroChain(src) {
				continue // rule L3 already walked this one back
			}
		default:
			continue
		}
		sx, dx := cl.x[src], cl.x[dst]
		g := byKey[dst]
		if g == nil {
			g = &detourGroup{dst: dst, row: sr,
				lo: math.Min(sx, dx), hi: math.Max(sx, dx),
				risers: [2]float64{sx, dx}}
			byKey[dst] = g
			order = append(order, g)
		}
		g.lo = math.Min(g.lo, math.Min(sx, dx))
		g.hi = math.Max(g.hi, math.Max(sx, dx))
		g.flows = append(g.flows, fl.ID)
		g.rows = append(g.rows, sr)
	}
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].lo != order[j].lo {
			return order[i].lo < order[j].lo
		}
		if order[i].hi != order[j].hi {
			return order[i].hi > order[j].hi
		}
		return order[i].flows[0] < order[j].flows[0]
	})
	return order
}

// planSky decides, before any route is planned, which same-row detours arch
// OVER their row instead of dipping under it (rule M3). Below is the
// fallback, not a deprecated shape: an edge whose sky is occupied keeps it.
//
// Two spans that overlap without either containing the other cost exactly
// one crossing if they share a band, whichever order they are stacked in.
// So a group is skied only when no already-skied group partially overlaps
// it; the other one stays below and the crossing disappears.
func (cl *compLayout) planSky() {
	partial := func(a, b *detourGroup) bool {
		if a.hi <= b.lo || b.hi <= a.lo {
			return false // disjoint
		}
		contains := (a.lo <= b.lo && b.hi <= a.hi) || (b.lo <= a.lo && a.hi <= b.hi)
		return !contains
	}
	var skied []*detourGroup
	for _, g := range cl.sameRowDetourGroups() {
		if !cl.freeSky(g.lo, g.hi, g.row, g.risers[0], g.risers[1]) {
			continue
		}
		clash := false
		for _, o := range skied {
			if partial(g, o) {
				clash = true
				break
			}
		}
		if clash {
			continue
		}
		skied = append(skied, g)
		for _, id := range g.flows {
			cl.skyEdge[id] = true
		}
	}
}

// rankUpwardRisers ranks, per target, the source rows of the rejoins coming
// up into its bottom face: the shallowest row gets 0 and each deeper one the
// next index (rule M4). Every rejoin approaches from the left along its own
// row, so a riser from a deeper row crosses the horizontal approach of every
// shallower one unless it turns up further right — the rank IS how many
// slots right of the target's column it must start looking.
//
// This is the upward counterpart of sameRowDetourGroups; the two cover
// disjoint sets of flows (sr > dr here, sr == dr there).
func (cl *compLayout) rankUpwardRisers() {
	rows := map[string]map[int]bool{}
	for _, fl := range cl.componentFlows() {
		sr, dr := cl.rowOf[fl.SourceRef], cl.rowOf[fl.TargetRef]
		if sr <= dr {
			continue
		}
		switch classify(cl.g, cl.chains, cl.chainOf, fl) {
		case fcExit, fcCross, fcRootExit:
		default:
			continue
		}
		if rows[fl.TargetRef] == nil {
			rows[fl.TargetRef] = map[int]bool{}
		}
		rows[fl.TargetRef][sr] = true
	}
	for dst, set := range rows {
		ordered := make([]int, 0, len(set))
		for r := range set {
			ordered = append(ordered, r)
		}
		sort.Ints(ordered)
		for rank, r := range ordered {
			cl.riserRank[riserKey(dst, r)] = rank
		}
	}
}

func riserKey(dst string, row int) string { return fmt.Sprintf("%s/%d", dst, row) }

// riserBundle is a set of forward rejoins that enter one target's bottom
// face on a SHARED riser standing in that target's own column (rule N1b).
type riserBundle struct {
	deep  int  // deepest member's source row: how far the riser has to reach
	owned bool // the shared column has been reserved
}

// targetColumnRiser reports whether a rejoin can turn up in its target's
// OWN column: rule L1's right-then-up shape at zero offset. This is the
// eligibility gate for rule N1b. A rejoin whose column is blocked by a
// shape — for its full depth, not just near the target — cannot lie on the
// bundle's line and keeps its own riser and its own arrowhead.
func (cl *compLayout) targetColumnRiser(src, dst string, sr, dr int) bool {
	dx := cl.x[dst]
	return dx > cl.x[src] && dx > cl.right(src)+10 &&
		!cl.nodesBetween(sr, cl.right(src), dx) &&
		cl.corridorClear(dx, dr+1, sr)
}

// planRiserMerges decides, before any route is planned, which forward
// rejoins into one target share a single riser in that target's own column
// (rule N1b). This is the forward counterpart of rule M2b: way-back lines
// already merge across gaps because every return rises in the target's own
// column, and a rejoin that CAN stand in that column is in exactly the same
// position — the shallower member's final run is contained in the deeper
// one's, so the two read as one line ending in one arrowhead.
//
// The decision has to be made up front rather than in mergeBundleDockings,
// because it changes the route: members skip rule M4's rank (see
// planMergedRiser) and share one reserved column instead of contending for
// it. Only shape clearance is consulted here, which is known before any
// corridor exists; the column's availability is settled when the bundle's
// first member is planned.
func (cl *compLayout) planRiserMerges() {
	byDst := map[string][]string{}
	deepest := map[string]int{}
	var order []string
	for _, fl := range cl.componentFlows() {
		src, dst := fl.SourceRef, fl.TargetRef
		sr, dr := cl.rowOf[src], cl.rowOf[dst]
		if sr <= dr {
			continue
		}
		switch classify(cl.g, cl.chains, cl.chainOf, fl) {
		case fcExit, fcCross, fcRootExit:
		default:
			continue
		}
		if !cl.targetColumnRiser(src, dst, sr, dr) {
			continue
		}
		if _, seen := byDst[dst]; !seen {
			order = append(order, dst)
		}
		byDst[dst] = append(byDst[dst], fl.ID)
		if sr > deepest[dst] {
			deepest[dst] = sr
		}
	}
	for _, dst := range order {
		if len(byDst[dst]) < 2 {
			continue // one rejoin is not a bundle; rule L1 routes it as usual
		}
		for _, id := range byDst[dst] {
			cl.riserMerge[id] = dst
		}
		cl.riserBundles[dst] = &riserBundle{deep: deepest[dst]}
	}
}

// planMergedRiser routes one member of a merged rejoin bundle (rule N1b):
// out of the source's right border, along its own row, and up in the
// TARGET'S own column — the same column every member uses, so the reader
// sees one line growing as each arc joins it and a single arrowhead on the
// target's face. On a diamond that face point is the bottom vertex, since
// the entry carries no offset to slide along the slant.
//
// Rule M4's rank is deliberately NOT applied. Stepping a deeper riser to
// the right keeps it off the horizontal approach of the shallower ones;
// members of one bundle share a single line, so there is no approach left
// to cut. M4 still ranks every rejoin that does not merge.
func (cl *compLayout) planMergedRiser(pl *edgePlan, dr int, dx float64) bool {
	b := cl.riserBundles[cl.riserMerge[pl.id]]
	if b == nil {
		return false
	}
	if !b.owned {
		// The column has to carry the whole bundle, not just this member.
		// If an earlier route already holds it the bundle cannot form, and
		// every member falls back to its own M4-ranked riser.
		if !cl.corridorFree(dx, dr, b.deep) {
			dst := cl.riserMerge[pl.id]
			for id, d := range cl.riserMerge {
				if d == dst {
					delete(cl.riserMerge, id)
				}
			}
			delete(cl.riserBundles, dst)
			return false
		}
		cl.useCorridor(dx, dr, b.deep)
		b.owned = true
	}
	pl.kind = pkRightUp
	pl.corrX = dx
	pl.entry = cl.dock(pl.id, pl.dst, sBottom, cl.x[pl.src], true, 0)
	return true
}

// planRoutes builds a symbolic routing plan per sequence flow.
func (cl *compLayout) planRoutes() error {
	cl.gapLanes = make([][]*laneSeg, len(cl.rows)+1)
	cl.planSky()
	cl.rankUpwardRisers()
	cl.planRiserMerges()
	for _, fl := range cl.componentFlows() {
		pl := &edgePlan{id: fl.ID, src: fl.SourceRef, dst: fl.TargetRef}
		cl.plans = append(cl.plans, pl)
		cl.planByID[fl.ID] = pl

		sr, dr := cl.rowOf[pl.src], cl.rowOf[pl.dst]
		sx, dx := cl.x[pl.src], cl.x[pl.dst]
		aligned := math.Abs(sx-dx) < 0.5

		switch classify(cl.g, cl.chains, cl.chainOf, fl) {
		case fcChainInternal:
			if cl.retroChain(pl.src) || cl.skyRetroChain(pl.src) {
				// The chain reads backwards: the body IS the way-back line
				// (rule L3 below the row, rule N3b in the sky).
				pl.kind = pkHLeft
				cl.res.Retrograde[pl.id] = true
				break
			}
			pl.kind = pkH
			pl.entry = cl.dockLeft(pl, true)

		case fcEntry:
			if cl.retroChain(pl.dst) && aligned {
				// The head sits in the split's own column: drop straight
				// out of the bottom corner into its top.
				pl.kind = pkVDown
				pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, true, 0)
				pl.entry = cl.dock(pl.id, pl.dst, sTop, sx, true, 0)
				cl.useCorridor(sx, sr, dr)
				break
			}
			if cl.skyRetroChain(pl.dst) {
				cl.planSkyRetroEntry(pl, sr, dr)
				break
			}
			cl.planBranchEntry(pl, sr, dr)

		case fcExit, fcCross:
			switch {
			case sr == dr:
				if cl.nodesBetween(sr, cl.right(pl.src), cl.left(pl.dst)) {
					cl.planUnderRow(pl, sr)
				} else {
					pl.kind = pkH
					pl.entry = cl.dockLeft(pl, true)
				}
			case sr > dr: // target above
				cl.planUpward(pl, sr, dr, sx, dx, aligned)
			default: // target below
				cl.planDownward(pl, sr, dr, sx, dx, aligned)
			}

		case fcRootExit:
			cl.planRootMerge(pl, sr, dr)

		case fcBack:
			cl.planBack(pl, sr, dr, sx, dx)
		}
	}
	cl.assignLanes()
	cl.mergeBundleDockings()
	cl.resolveDockings()
	return nil
}

// retroChain reports whether a node belongs to a chain laid out right to left.
func (cl *compLayout) retroChain(id string) bool {
	i, ok := cl.chainOf[id]
	return ok && i >= 0 && i < len(cl.chains) && cl.chains[i].retro
}

// skyRetroChain reports whether a node belongs to a lifted loop-return
// body laid out right to left (rule N3b).
func (cl *compLayout) skyRetroChain(id string) bool {
	i, ok := cl.chainOf[id]
	return ok && i >= 0 && i < len(cl.chains) && cl.chains[i].skyRetro
}

// planSkyRetroEntry routes the split's entry into a lifted loop-return
// body (rule N3b): the body reads right to left with its head one column
// LEFT of the split, so the entry rises out of the gateway's top corner in
// the gateway's own column and turns once into the head's RIGHT face. The
// hop is leftward by design, so it is registered as retrograde.
func (cl *compLayout) planSkyRetroEntry(pl *edgePlan, sr, dr int) {
	gx := cl.x[pl.src]
	pl.kind = pkUpRightIn
	pl.corrX = gx
	pl.exit = cl.dock(pl.id, pl.src, sTop, gx, true, 0)
	pl.exit.shared = true
	pl.entry = cl.dock(pl.id, pl.dst, sRight, float64(cl.rowOf[pl.src]), false, 0)
	lo, hi := rowBand(sr, dr)
	cl.useCorridor(gx, lo, hi)
	cl.res.Retrograde[pl.id] = true
}

func (cl *compLayout) left(id string) float64  { return cl.x[id] - cl.width(id)/2 }
func (cl *compLayout) right(id string) float64 { return cl.x[id] + cl.width(id)/2 }

// planDownward: source row above target row, forward direction.
// planBranchEntry routes a split's alternate branch: leave the gateway
// through the top or bottom corner, run vertically in the gateway's own
// column, then turn once into the branch head's left side. Every alternate
// of one gateway shares that column, forming a single trunk.
func (cl *compLayout) planBranchEntry(pl *edgePlan, sr, dr int) {
	gx := cl.x[pl.src]
	side := sBottom
	pl.kind = pkDownLeftIn
	if sr > dr { // branch lifted above its split node
		side = sTop
		pl.kind = pkUpLeftIn
	}
	pl.corrX = gx
	pl.exit = cl.dock(pl.id, pl.src, side, gx, true, 0)
	pl.exit.shared = true
	pl.entry = cl.dockLeft(pl, false)
	lo, hi := rowBand(sr, dr)
	cl.useCorridor(gx, lo, hi)
}

func (cl *compLayout) planDownward(pl *edgePlan, sr, dr int, sx, dx float64, aligned bool) {
	if aligned && cl.corridorClear(sx, sr+1, dr-1) && cl.corridorFree(sx, sr, dr) {
		pl.kind = pkVDown
		pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, true, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sTop, sx, true, 0)
		cl.useCorridor(sx, sr, dr)
		return
	}
	// Drop straight down at the source, enter the target's left side.
	if cl.corridorClear(sx, sr+1, dr-1) && cl.corridorFree(sx, sr, dr) &&
		sx < cl.left(pl.dst) && !cl.nodesBetween(dr, sx, cl.left(pl.dst)) {
		pl.kind = pkDownLeftIn
		pl.corrX = sx
		pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, false, 0)
		pl.entry = cl.dockLeft(pl, false)
		cl.useCorridor(sx, sr, dr)
		return
	}
	// Jog through the gap below the source row to a corridor, then down into
	// the target's left side.
	if corr, ok := cl.findCorridor([]float64{cl.left(pl.dst) - 35, sx}, sr+1, dr-1,
		func(x float64) bool { return x < cl.left(pl.dst) && !cl.nodesBetween(dr, x, cl.left(pl.dst)) }); ok {
		pl.kind = pkDownJog
		pl.corrX = corr
		pl.g1 = sr + 1
		pl.seg1 = cl.lane(sr+1, pl.id, sx, corr)
		pl.exit = cl.dock(pl.id, pl.src, sBottom, corr, false, 0)
		pl.entry = cl.dockLeft(pl, false)
		return
	}
	// Fall back to entering the top via the gap above the target row.
	if corr, ok := cl.findCorridor([]float64{sx, dx}, sr+1, dr-1, nil); ok {
		pl.kind = pkDownTop
		pl.corrX = corr
		pl.g1 = sr + 1
		pl.seg1 = cl.lane(sr+1, pl.id, sx, corr)
		pl.g2 = dr
		pl.seg2 = cl.lane(dr, pl.id, corr, dx)
		pl.exit = cl.dock(pl.id, pl.src, sBottom, corr, false, 0)
		// The approach runs in the corridor's own column, so the entry
		// docking is pinned there: no tail jog off the vertical.
		pl.entry = cl.dockAtCorridor(pl.id, pl.dst, sTop, corr, dx)
		return
	}
	cl.planMargin(pl, sr, dr, false)
}

// planUpward: source row below target row, forward direction (rejoin).
//
// Rule L1 makes right-then-up the preferred shape: leave the source through
// its right border, run along its own row and turn once in the TARGET'S
// column, entering the target's bottom face. Only when that is impossible
// does an aligned tail rise straight out of its top — which is the right
// shape for a gateway or event tail, whose corner or point has no flat
// border to leave sideways from, and whose `dx > sx` gate is false anyway
// because assignX aligns it under the target.
func (cl *compLayout) planUpward(pl *edgePlan, sr, dr int, sx, dx float64, aligned bool) {
	if cl.planMergedRiser(pl, dr, dx) {
		return // rule N1b: this one shares the bundle's riser
	}
	if dx > sx {
		// Offsets to the RIGHT, starting at this source's depth rank. Every
		// rejoin approaches from the left along its own row, so a riser
		// coming from a deeper row has to clear the horizontal approach of
		// every shallower one: it can only do that by turning up further
		// right than they do. Starting each rank one slot further right
		// makes that ordering explicit instead of leaving it to the order
		// the flows happen to be declared in.
		halfW := cl.width(pl.dst)/2 - 4
		rank := cl.riserRank[riserKey(pl.dst, sr)]
		offs := []float64{
			float64(rank) * SlotStep,
			float64(rank+1) * SlotStep,
			float64(rank+2) * SlotStep,
			-SlotStep, -2 * SlotStep,
		}
		for _, off := range offs {
			if math.Abs(off) > halfW {
				continue
			}
			x := dx + off
			if x > cl.right(pl.src)+10 &&
				!cl.nodesBetween(sr, cl.right(pl.src), x) &&
				cl.corridorClear(x, dr+1, sr) && cl.corridorFree(x, dr, sr) {
				cl.useCorridor(x, dr, sr)
				pl.kind = pkRightUp
				pl.corrX = x
				pl.entry = cl.dock(pl.id, pl.dst, sBottom, sx, true, x-dx)
				return
			}
		}
	}
	if aligned && cl.corridorClear(sx, dr+1, sr-1) && cl.corridorFree(sx, dr, sr) {
		pl.kind = pkVUp
		pl.exit = cl.dock(pl.id, pl.src, sTop, dx, true, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sBottom, sx, true, 0)
		cl.useCorridor(sx, dr, sr)
		return
	}
	if corr, ok := cl.findCorridor([]float64{sx, dx}, dr+1, sr-1, nil); ok {
		pl.kind = pkUpBottom
		pl.corrX = corr
		// Gap sr and gap dr+1 are the same channel when the rows are
		// adjacent; jogging would lay two lanes of one edge into it. Leave
		// through the corridor column directly and use the single lane.
		if corr != sx && sr != dr+1 {
			pl.g1 = sr
			pl.seg1 = cl.lane(sr, pl.id, sx, corr)
		}
		pl.g2 = dr + 1
		pl.seg2 = cl.lane(dr+1, pl.id, corr, dx)
		if pl.seg1 == nil {
			pl.exit = cl.dockAtCorridor(pl.id, pl.src, sTop, corr, sx)
		} else {
			pl.exit = cl.dock(pl.id, pl.src, sTop, corr, false, 0)
		}
		pl.entry = cl.dockAtCorridor(pl.id, pl.dst, sBottom, corr, dx)
		return
	}
	cl.planMargin(pl, sr, dr, false)
}

// planRootMerge: a secondary-start chain rejoins with the same shape as any
// other (rule L1) — right out of its tail, up in the target's column, into
// the target's bottom face. The old left-side fan-in is kept only as a last
// resort, because a target's left side carries a single docking point on an
// event or a gateway: two inflows there land one arrowhead on top of another.
func (cl *compLayout) planRootMerge(pl *edgePlan, sr, dr int) {
	sx, dx := cl.x[pl.src], cl.x[pl.dst]
	cl.planUpward(pl, sr, dr, sx, dx, math.Abs(sx-dx) < 0.5)
	if pl.kind != pkBackMargin {
		return
	}
	bend := cl.left(pl.dst) - BendBeforeTarget
	lo, hi := rowBand(sr, dr)
	if cl.corridorClear(bend, dr+1, sr-1) && cl.corridorFree(bend, lo, hi) &&
		!cl.nodesBetween(sr, cl.right(pl.src), bend) && !cl.nodesBetween(dr, bend, cl.left(pl.dst)) {
		cl.undoMargin(pl)
		pl.kind = pkRootMerge
		pl.corrX = bend
		pl.entry = cl.dockLeft(pl, false)
		cl.useCorridor(bend, lo, hi)
	}
}

// undoMargin releases the lane segments and dockings a margin route
// reserved, so a better plan can replace it.
func (cl *compLayout) undoMargin(pl *edgePlan) {
	drop := func(g int, s *laneSeg) {
		if s == nil || g < 0 || g >= len(cl.gapLanes) {
			return
		}
		for i, o := range cl.gapLanes[g] {
			if o == s {
				cl.gapLanes[g] = append(cl.gapLanes[g][:i], cl.gapLanes[g][i+1:]...)
				return
			}
		}
	}
	drop(pl.g1, pl.seg1)
	drop(pl.g2, pl.seg2)
	pl.seg1, pl.seg2 = nil, nil
	for _, d := range []*docking{pl.exit, pl.entry} {
		if d == nil {
			continue
		}
		k := sideKey{pl.src, d.side}
		if d == pl.entry {
			k = sideKey{pl.dst, d.side}
		}
		for i, o := range cl.sides[k] {
			if o == d {
				cl.sides[k] = append(cl.sides[k][:i], cl.sides[k][i+1:]...)
				break
			}
		}
	}
	pl.exit, pl.entry = nil, nil
	cl.marginUse--
}

// planUnderRow: forward hop past intervening nodes on the shared row,
// dipping through the gap below the row (the space above rows is reserved
// for lifted branches).
func (cl *compLayout) planUnderRow(pl *edgePlan, row int) {
	side, gap := sBottom, row+1
	pl.kind = pkUnderRow
	if cl.skyEdge[pl.id] { // rule M3: the sky over this span is free
		side, gap = sTop, row
		pl.kind = pkOverRow
	}
	pl.g1 = gap
	pl.seg1 = cl.lane(gap, pl.id, cl.x[pl.src], cl.x[pl.dst])
	pl.seg1.sky = pl.kind == pkOverRow
	pl.exit = cl.dock(pl.id, pl.src, side, cl.x[pl.dst], false, 0)
	pl.entry = cl.dock(pl.id, pl.dst, side, cl.x[pl.src], false, 0)
}

// planBack: every way-back edge drops from the source's bottom to a
// dedicated line in the gap below the lower of the two rows, runs backward,
// and rises into the target's bottom.
func (cl *compLayout) planBack(pl *edgePlan, sr, dr int, sx, dx float64) {
	// A retrograde branch has already walked the edge back to the target's
	// column, so the way-back line has zero length: rise straight out of the
	// source's top into the target's bottom.
	if math.Abs(sx-dx) < 0.5 && dr < sr &&
		cl.corridorClear(sx, dr+1, sr-1) && cl.corridorFree(sx, dr, sr) {
		cl.useCorridor(sx, dr, sr)
		pl.kind = pkVUp
		pl.exit = cl.dock(pl.id, pl.src, sTop, dx, true, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sBottom, sx, true, 0)
		return
	}
	// A loop-return branch lifted above the spine (markLoopReturns, rule
	// N3) returns through the SKY: out of the source's bottom, back along
	// the gap above the target's row, and down into the target's TOP. The
	// lane is a sky lane, so rule M2 merges it with the target's other
	// top-face returns.
	if dr > sr && sr < cl.chains[0].row {
		// The body read right to left (rule N3b): the tail's return leaves
		// its LEFT face, runs back along its own row and turns once down
		// into the target's top — rule L1's right-then-up, pointing home.
		// A tail that overshot the target's column, or whose row or drop
		// is blocked, falls back to the sky-lane shape below.
		if ch := cl.chains[cl.chainOf[pl.src]]; ch.skyRetro &&
			ch.nodes[len(ch.nodes)-1] == pl.src &&
			dx < cl.left(pl.src) && !cl.nodesBetween(sr, dx, cl.left(pl.src)) &&
			cl.corridorClear(dx, sr+1, dr-1) {
			lo, hi := rowBand(sr, dr)
			cl.useCorridor(dx, lo, hi)
			pl.kind = pkLeftDown
			pl.corrX = dx
			pl.exit = cl.dock(pl.id, pl.src, sLeft, float64(dr), false, 0)
			pl.entry = cl.dock(pl.id, pl.dst, sTop, dx, true, 0)
			return
		}
		band := dr // the gap above the target's row
		if cl.corridorClear(sx, sr+1, band-1) {
			lo, hi := rowBand(sr, dr)
			cl.useCorridor(dx, lo, hi)
			pl.kind = pkBackTop
			pl.corrX = dx
			pl.g1 = band
			pl.seg1 = cl.lane(band, pl.id, sx, dx)
			pl.seg1.sky = true
			pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, false, 0)
			pl.entry = cl.dock(pl.id, pl.dst, sTop, dx, true, 0)
			return
		}
		cl.planMargin(pl, sr, dr, true)
		return
	}
	band := sr + 1
	if dr > sr {
		band = dr + 1
	}
	// The drop from the source crosses every row between the source row and
	// the band; the rise to the target likewise. Both columns must be clear
	// of shapes. Vertical-vs-vertical proximity is not checked: way-back
	// lines may share columns and may be crossed.
	halfW := cl.width(pl.dst)/2 - 4
	for _, off := range []float64{0, -SlotStep, SlotStep, -2 * SlotStep, 2 * SlotStep} {
		if math.Abs(off) > halfW {
			continue
		}
		x := dx + off
		if cl.corridorClear(x, dr+1, band-1) && cl.corridorClear(sx, sr+1, band-1) {
			lo, hi := rowBand(sr, dr)
			cl.useCorridor(x, lo, hi)
			side := sBottom
			pl.kind = pkBackBottom
			if cl.skyEdge[pl.id] { // rule M3: arch over the row instead
				side = sTop
				pl.kind = pkBackTop
				band = sr
			}
			pl.corrX = x
			pl.g1 = band
			pl.seg1 = cl.lane(band, pl.id, sx, x)
			pl.seg1.sky = pl.kind == pkBackTop
			pl.exit = cl.dock(pl.id, pl.src, side, x, false, 0)
			pl.entry = cl.dock(pl.id, pl.dst, side, x, true, x-dx)
			return
		}
	}
	cl.planMargin(pl, sr, dr, true)
}

// planMargin routes around the far left of the component. Back edges enter
// the target's bottom; forward flows keep entering the top.
func (cl *compLayout) planMargin(pl *edgePlan, sr, dr int, backEntry bool) {
	pl.kind = pkBackMargin
	pl.backEntry = backEntry
	pl.marginIdx = cl.marginUse
	cl.marginUse++
	if backEntry || sr >= dr { // exit below the source row
		pl.g1 = sr + 1
		pl.exit = cl.dock(pl.id, pl.src, sBottom, marginSentinel, false, 0)
	} else { // exit above the source row, travel down at the margin
		pl.g1 = sr
		pl.exit = cl.dock(pl.id, pl.src, sTop, marginSentinel, false, 0)
	}
	if backEntry {
		pl.g2 = dr + 1
		pl.entry = cl.dock(pl.id, pl.dst, sBottom, marginSentinel, false, 0)
	} else {
		pl.g2 = dr
		pl.entry = cl.dock(pl.id, pl.dst, sTop, marginSentinel, false, 0)
	}
	pl.seg1 = cl.lane(pl.g1, pl.id, cl.x[pl.src], marginSentinel)
	pl.seg2 = cl.lane(pl.g2, pl.id, marginSentinel, cl.x[pl.dst])
}

// --- helpers -----------------------------------------------------------------

func (cl *compLayout) dock(edge, node string, side nodeSide, approach float64, pinned bool, fixed float64) *docking {
	d := &docking{edge: edge, side: side, approach: approach, pinned: pinned, fixed: fixed}
	k := sideKey{node, side}
	cl.sides[k] = append(cl.sides[k], d)
	return d
}

// dockAtCorridor pins a docking to the corridor's column so the final run
// into (or out of) the shape is straight. A corridor further out than the
// side can reach keeps a free docking: the edge has to step across anyway,
// and a pin the shape cannot honour drifts to an arbitrary slot instead.
func (cl *compLayout) dockAtCorridor(edge, node string, side nodeSide, corr, cx float64) *docking {
	if math.Abs(corr-cx) <= cl.width(node)/2-4 {
		return cl.dock(edge, node, side, corr, true, corr-cx)
	}
	return cl.dock(edge, node, side, corr, false, 0)
}

// dockLeft registers an edge arriving at its target's left side, ordered by
// the row it comes from: an edge dropping in from above takes a slot above
// the centerline, one rising from below takes one under it. A flow running
// straight along its row is pinned to the centerline.
func (cl *compLayout) dockLeft(pl *edgePlan, pinned bool) *docking {
	return cl.dock(pl.id, pl.dst, sLeft, float64(cl.rowOf[pl.src]), pinned, 0)
}

func (cl *compLayout) lane(gap int, edge string, x1, x2 float64) *laneSeg {
	s := &laneSeg{edge: edge, x1: x1, x2: x2}
	cl.gapLanes[gap] = append(cl.gapLanes[gap], s)
	return s
}

// corridorClear: no nodes in the strip across rows lo..hi (inclusive).
func (cl *compLayout) corridorClear(x float64, lo, hi int) bool {
	for r := lo; r <= hi; r++ {
		if !cl.rowClear(x, r) {
			return false
		}
	}
	return true
}

// corridorFree reports whether a vertical spanning rows lo..hi can own the
// column x. Only a corridor whose own band overlaps lo..hi conflicts.
func (cl *compLayout) corridorFree(x float64, lo, hi int) bool {
	for _, c := range cl.corridors {
		if math.Abs(c.x-x) < 12 && !(hi < c.lo || lo > c.hi) {
			return false
		}
	}
	return true
}

func (cl *compLayout) useCorridor(x float64, lo, hi int) {
	cl.corridors = append(cl.corridors, corridor{x, lo, hi})
}

// rowBand is the inclusive row range a vertical between two rows traverses.
func rowBand(a, b int) (int, int) {
	if a > b {
		return b, a
	}
	return a, b
}

// findCorridor scans candidate x positions (each preference expanded
// outwards in 20px steps) for a vertical corridor across rows lo..hi that
// also satisfies extra (may be nil). The accepted corridor is registered.
func (cl *compLayout) findCorridor(prefs []float64, lo, hi int, extra func(float64) bool) (float64, bool) {
	for _, pref := range prefs {
		for k := 0; k <= 14; k++ {
			for _, s := range []float64{1, -1} {
				if k == 0 && s < 0 {
					continue
				}
				x := pref + s*float64(k)*20
				if cl.corridorClear(x, lo, hi) && cl.corridorFree(x, lo, hi) && (extra == nil || extra(x)) {
					cl.useCorridor(x, lo, hi)
					return x, true
				}
			}
		}
	}
	return 0, false
}

// nodesBetween reports whether any node of the row intersects the open
// horizontal interval (a, b).
func (cl *compLayout) nodesBetween(row int, a, b float64) bool {
	lo, hi := math.Min(a, b)+5, math.Max(a, b)-5
	if row < 0 || row >= len(cl.rows) {
		return false
	}
	for _, id := range cl.rows[row] {
		half := cl.width(id) / 2
		if cl.x[id]+half > lo && cl.x[id]-half < hi {
			return true
		}
	}
	return false
}

// laneBundles keys each lane segment that carries an edge into a target's
// top or bottom face by that target and face (rule L6a). Runs sharing a key
// are the same rejoin bundle: they end at one node and rise into it, so they
// belong on one line with short risers peeling off at their own offsets
// rather than on a staircase of parallel lines. Way-back edges are excluded;
// their stacking is rule R6's and stays as it is.
func (cl *compLayout) laneBundles() map[*laneSeg]string {
	out := map[*laneSeg]string{}
	for _, pl := range cl.plans {
		switch pl.kind {
		case pkBackBottom, pkBackMargin:
			continue
		}
		if pl.entry == nil || (pl.entry.side != sTop && pl.entry.side != sBottom) {
			continue
		}
		// The run adjacent to the target is the last one the route uses.
		seg := pl.seg2
		if seg == nil {
			seg = pl.seg1
		}
		if seg == nil {
			continue
		}
		out[seg] = fmt.Sprintf("%s/%d", pl.dst, pl.entry.side)
	}
	return out
}

// mergeBundleDockings makes each rejoin bundle enter its target as ONE
// arrow (rule M2). The members already share a lane (L6a); now they share
// the riser too: the member reaching furthest left owns it, in the target's
// own column, and every other member runs along the shared lane into that
// same column and follows the same riser to the same terminal point. Their
// final runs are collinear, so what the reader sees is one line growing as
// each arc joins it, ending in a single arrowhead.
//
// Only members whose riser IS the target's column can merge — the kinds
// that dock straight off their lane. A member routed through a corridor
// reserved elsewhere (pkUpBottom, pkDownTop) has to come in on its own
// column, so it keeps its own riser and its own arrowhead; it may still
// share the lane.
//
// The merge is DECLARED here, never inferred from geometry downstream: two
// unrelated inflows that happen to end up collinear and sharing a terminal
// point are the pileup bug, not a bundle, and must stay reportable.
func (cl *compLayout) mergeBundleDockings() {
	mergeable := func(k planKind) bool {
		return k == pkUnderRow || k == pkBackBottom ||
			k == pkOverRow || k == pkBackTop || k == pkLeftDown ||
			k == pkRightUp
	}
	isBack := func(k planKind) bool {
		return k == pkBackBottom || k == pkBackTop || k == pkLeftDown
	}
	// These two run along their source's own row and reserve no lane.
	laneless := func(k planKind) bool {
		return k == pkLeftDown || k == pkRightUp
	}
	byKey := map[string][]*edgePlan{}
	var order []string
	for _, pl := range cl.plans {
		if !mergeable(pl.kind) || pl.entry == nil {
			continue
		}
		if pl.seg1 == nil && !laneless(pl.kind) {
			continue
		}
		if pl.entry.side != sTop && pl.entry.side != sBottom {
			continue
		}
		// The gap is part of the key for FORWARD kinds: members sharing a
		// target but lying in different gaps sit at different lane depths,
		// so their risers have different lengths and one cannot follow the
		// other's line. Way-back returns are different (rule M2b): every
		// return rises in the target's own column, so a shallower member's
		// riser IS contained in a deeper one's — returns into one face
		// merge ACROSS gaps into the single riser the deepest one owns.
		// Forward REJOINS turning up in the target's column (rule N1b) are
		// in the same position and merge across gaps too: planRiserMerges
		// already put the whole bundle in one column, so the shallower
		// member's riser is contained in the deeper one's.
		//
		// Either way, a member whose target-column riser is shape-blocked —
		// its offset search settled beside the column, fixed != 0 — cannot
		// lie on that riser and keeps its own riser and arrowhead. A member
		// routed through a corridor reserved elsewhere (pkUpBottom,
		// pkDownTop) fails the same gate by construction: it only reached
		// that plan because the target's column was unavailable.
		//
		// Returns and rejoins keep separate keys. A way-back line and a
		// forward flow into one face are not one bundle, and rule 6's
		// crossing exemption applies to only one of them.
		var k string
		switch {
		case isBack(pl.kind):
			if pl.entry.fixed != 0 {
				continue
			}
			k = fmt.Sprintf("back/%s/%d", pl.dst, pl.entry.side)
		case pl.kind == pkRightUp:
			if pl.entry.fixed != 0 {
				continue
			}
			k = fmt.Sprintf("rejoin/%s/%d", pl.dst, pl.entry.side)
		default:
			k = fmt.Sprintf("%d/%s/%d", pl.g1, pl.dst, pl.entry.side)
		}
		if _, seen := byKey[k]; !seen {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], pl)
	}
	// riserDepth ranks how far a member's final riser reaches from the
	// target's face, in row/gap steps: a bottom return starting in a
	// deeper gap reaches further down; a laneless member (pkLeftDown,
	// pkRightUp) starts at its source's own row, further out than any lane
	// in the neighbouring gap. The deepest member owns the riser (rules
	// M2b and N1b).
	riserDepth := func(pl *edgePlan) int {
		dr := cl.rowOf[pl.dst]
		if laneless(pl.kind) {
			d := dr - cl.rowOf[pl.src]
			if d < 0 {
				return -d
			}
			return d
		}
		if pl.g1 > dr {
			return pl.g1 - dr
		}
		return dr - pl.g1
	}
	segLo := func(pl *edgePlan) float64 {
		if pl.seg1 != nil {
			return pl.seg1.lo()
		}
		return math.Min(cl.x[pl.src], cl.x[pl.dst])
	}
	for _, k := range order {
		members := byKey[k]
		if len(members) < 2 {
			continue
		}
		sort.SliceStable(members, func(i, j int) bool {
			// The deepest member has the longest riser and owns the
			// trunk; every shallower member's final run is contained in
			// it. Forward bundles share one gap, so this never reorders
			// them.
			if di, dj := riserDepth(members[i]), riserDepth(members[j]); di != dj {
				return di > dj
			}
			li, lj := segLo(members[i]), segLo(members[j])
			if li != lj {
				return li < lj
			}
			return members[i].id < members[j].id
		})
		owner := members[0]
		for _, pl := range members {
			pl.entry.merged = true
			pl.entry.pinned = true
			pl.entry.fixed = 0
			if pl != owner {
				cl.res.Merged[pl.id] = owner.id
			}
		}
	}
}

// laneGroup is one bundle (or one lone segment) competing for a lane.
type laneGroup struct {
	segs   []*laneSeg
	lo, hi float64
}

// assignLanes packs each gap's segments into lanes. Segments of one rejoin
// bundle share a lane (L6a) and are packed as a single run spanning all of
// them. Groups are ordered by lo ascending then hi descending (L6b), so a
// nested arc lands on a later lane than the arc containing it — and laneY
// puts later lanes nearer the row, which is what makes them nest.
func (cl *compLayout) assignLanes() {
	bundle := cl.laneBundles()
	for _, segs := range cl.gapLanes {
		if len(segs) == 0 {
			continue
		}
		var groups []*laneGroup
		byKey := map[string]*laneGroup{}
		for _, s := range segs {
			if k := bundle[s]; k != "" {
				if g := byKey[k]; g != nil {
					g.segs = append(g.segs, s)
					g.lo, g.hi = math.Min(g.lo, s.lo()), math.Max(g.hi, s.hi())
					continue
				}
				g := &laneGroup{segs: []*laneSeg{s}, lo: s.lo(), hi: s.hi()}
				byKey[k] = g
				groups = append(groups, g)
				continue
			}
			groups = append(groups, &laneGroup{segs: []*laneSeg{s}, lo: s.lo(), hi: s.hi()})
		}
		for _, g := range groups {
			sort.SliceStable(g.segs, func(i, j int) bool { return g.segs[i].edge < g.segs[j].edge })
		}
		// Dips and arches share a gap but are packed into separate lane
		// ranges: dips first, then arches. laneY maps a higher lane index
		// to a smaller y, so mirroring the arch comparator (lo desc, hi
		// asc) is what makes a CONTAINING arch land further from the row —
		// arches read as concentric arches, dips as concentric dips.
		var dips, skies []*laneGroup
		for _, g := range groups {
			if g.segs[0].sky {
				skies = append(skies, g)
				continue
			}
			dips = append(dips, g)
		}
		sort.SliceStable(dips, func(i, j int) bool {
			if dips[i].lo != dips[j].lo {
				return dips[i].lo < dips[j].lo
			}
			if dips[i].hi != dips[j].hi {
				return dips[i].hi > dips[j].hi
			}
			return dips[i].segs[0].edge < dips[j].segs[0].edge
		})
		sort.SliceStable(skies, func(i, j int) bool {
			if skies[i].lo != skies[j].lo {
				return skies[i].lo > skies[j].lo
			}
			if skies[i].hi != skies[j].hi {
				return skies[i].hi < skies[j].hi
			}
			return skies[i].segs[0].edge < skies[j].segs[0].edge
		})

		base := 0
		for _, batch := range [][]*laneGroup{dips, skies} {
			var lanes [][]*laneGroup
		next:
			for _, g := range batch {
				for li, lane := range lanes {
					ok := true
					for _, o := range lane {
						if g.lo < o.hi+20 && o.lo < g.hi+20 {
							ok = false
							break
						}
					}
					if ok {
						for _, s := range g.segs {
							s.lane = base + li
						}
						lanes[li] = append(lanes[li], g)
						continue next
					}
				}
				for _, s := range g.segs {
					s.lane = base + len(lanes)
				}
				lanes = append(lanes, []*laneGroup{g})
			}
			base += len(lanes)
		}
	}
}

// laneCount returns the number of lanes used in a gap.
func (cl *compLayout) laneCount(g int) int {
	max := 0
	for _, s := range cl.gapLanes[g] {
		if s.lane+1 > max {
			max = s.lane + 1
		}
	}
	return max
}

// dockingSpan returns the center an offset is measured from and the largest
// offset the shape can carry, along the side's own axis. A left or right
// docking runs down the shape's height and is ordered by the row the edge
// comes from; only a rectangle has a straight border to spread along, so a
// circle or a diamond keeps its single side point.
func (cl *compLayout) dockingSpan(key sideKey) (center, max float64) {
	if key.side != sLeft && key.side != sRight {
		return cl.x[key.node], cl.width(key.node)/2 - 4
	}
	center = float64(cl.rowOf[key.node])
	if n := cl.node(key.node); n.Kind.IsEvent() || n.Kind.IsGateway() {
		return center, 0
	}
	return center, cl.height(key.node)/2 - 4
}

// resolveDockings spreads the dockings of each node side around its center.
// The offset is where the edge's own run sits; on a diamond corner that is a
// lane beside the shape rather than a point on it (see stub).
func (cl *compLayout) resolveDockings() {
	for key, docks := range cl.sides {
		center, maxOff := cl.dockingSpan(key)
		var pinned, free []*docking
		var shared bool
		for _, d := range docks {
			switch {
			case d.shared: // the split's own trunk: every alternate at the corner
				d.off = 0
				shared = true
			case d.merged: // one rejoin bundle: every member on one riser
				d.off = 0
				shared = true
			case d.pinned:
				pinned = append(pinned, d)
			default:
				free = append(free, d)
			}
		}
		// placed records resolved offsets; pinned ones need not be whole
		// multiples of SlotStep, so proximity rather than equality decides.
		var placed []float64
		if shared {
			placed = append(placed, 0)
		}
		occupied := func(off float64) bool {
			if math.Abs(off) > maxOff {
				return true
			}
			for _, p := range placed {
				if math.Abs(p-off) < SlotStep-1 {
					return true
				}
			}
			return false
		}
		// Candidate offsets: whole multiples of SlotStep around the center,
		// nearest first, preferring the side the edge approaches from.
		assign := func(d *docking, from float64) {
			side := 1.0
			if d.approach < center {
				side = -1
			}
			d.off = from
			found := false
			for k := 0; k <= 6 && !found; k++ {
				for _, s := range []float64{side, -side} {
					if k == 0 && s != side {
						continue
					}
					off := from + s*float64(k)*SlotStep
					if occupied(off) {
						continue
					}
					d.off = off
					found = true
					break
				}
			}
			placed = append(placed, d.off)
		}
		// Pinned lanes first, the longest reach first: the run that comes
		// from furthest away keeps the column it validated and the shorter
		// ones step outwards around it, so a short run stops before the
		// long one's column instead of cutting across it.
		sort.SliceStable(pinned, func(i, j int) bool {
			ri, rj := math.Abs(pinned[i].approach-center), math.Abs(pinned[j].approach-center)
			if ri != rj {
				return ri > rj
			}
			return pinned[i].edge < pinned[j].edge
		})
		for _, d := range pinned {
			assign(d, d.fixed)
		}
		sort.SliceStable(free, func(i, j int) bool {
			if free[i].approach != free[j].approach {
				return free[i].approach < free[j].approach
			}
			return free[i].edge < free[j].edge
		})
		for _, d := range free {
			assign(d, 0)
		}
		// A diamond has no flat top or bottom: every edge touches the exact
		// corner point and jogs to its own lane over a short shared stub.
		if key.side != sLeft && key.side != sRight && cl.node(key.node).Kind.IsGateway() {
			for _, d := range docks {
				d.stub = math.Abs(d.off) > 0.5
			}
		}
	}
}
