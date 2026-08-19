package layout

import (
	"math"
	"sort"
)

// Side of a node shape.
type nodeSide int

const (
	sTop nodeSide = iota
	sBottom
	sLeft
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
}

// laneSeg is a horizontal run inside a gap channel. marginSentinel stands in
// for the (not yet known) margin corridor x during lane packing.
type laneSeg struct {
	edge   string
	x1, x2 float64
	lane   int
}

const marginSentinel = -100.0

func (s *laneSeg) lo() float64 { return math.Min(s.x1, s.x2) }
func (s *laneSeg) hi() float64 { return math.Max(s.x1, s.x2) }

// corridor registers a vertical line crossing row bands. Every vertical
// needs a column of its own: two lines sharing one column merge into a
// single stroke, and whatever arrowheads they carry stack on top of it.
type corridor struct {
	x float64
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

// planRoutes builds a symbolic routing plan per sequence flow.
func (cl *compLayout) planRoutes() error {
	cl.gapLanes = make([][]*laneSeg, len(cl.rows)+1)
	for _, fl := range cl.componentFlows() {
		pl := &edgePlan{id: fl.ID, src: fl.SourceRef, dst: fl.TargetRef}
		cl.plans = append(cl.plans, pl)
		cl.planByID[fl.ID] = pl

		sr, dr := cl.rowOf[pl.src], cl.rowOf[pl.dst]
		sx, dx := cl.x[pl.src], cl.x[pl.dst]
		aligned := math.Abs(sx-dx) < 0.5

		switch classify(cl.g, cl.chains, cl.chainOf, fl) {
		case fcChainInternal:
			pl.kind = pkH
			pl.entry = cl.dockLeft(pl, true)

		case fcEntry:
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
	cl.resolveDockings()
	return nil
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
	cl.useCorridor(gx)
}

func (cl *compLayout) planDownward(pl *edgePlan, sr, dr int, sx, dx float64, aligned bool) {
	if aligned && cl.corridorClear(sx, sr+1, dr-1) && cl.corridorFree(sx) {
		pl.kind = pkVDown
		pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, true, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sTop, sx, true, 0)
		cl.useCorridor(sx)
		return
	}
	// Drop straight down at the source, enter the target's left side.
	if cl.corridorClear(sx, sr+1, dr-1) && cl.corridorFree(sx) &&
		sx < cl.left(pl.dst) && !cl.nodesBetween(dr, sx, cl.left(pl.dst)) {
		pl.kind = pkDownLeftIn
		pl.corrX = sx
		pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, false, 0)
		pl.entry = cl.dockLeft(pl, false)
		cl.useCorridor(sx)
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
		pl.entry = cl.dock(pl.id, pl.dst, sTop, corr, false, 0)
		return
	}
	cl.planMargin(pl, sr, dr, false)
}

// planUpward: source row below target row, forward direction (rejoin).
func (cl *compLayout) planUpward(pl *edgePlan, sr, dr int, sx, dx float64, aligned bool) {
	if aligned && cl.corridorClear(sx, dr+1, sr-1) && cl.corridorFree(sx) {
		pl.kind = pkVUp
		pl.exit = cl.dock(pl.id, pl.src, sTop, dx, true, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sBottom, sx, true, 0)
		cl.useCorridor(sx)
		return
	}
	// Preferred rejoin shape: leave the source to the right, then rise
	// straight up into the target's bottom — one bend, with the vertical
	// merging into the target's center trunk when siblings share the entry.
	if dx > sx {
		side := -1.0
		if sx > dx {
			side = 1
		}
		halfW := cl.width(pl.dst)/2 - 4
		for _, off := range []float64{0, side * SlotStep, side * 2 * SlotStep} {
			if math.Abs(off) > halfW {
				continue
			}
			x := dx + off
			if x > cl.right(pl.src)+10 &&
				!cl.nodesBetween(sr, cl.right(pl.src), x) &&
				cl.corridorClear(x, dr+1, sr) && cl.corridorFree(x) {
				cl.useCorridor(x)
				pl.kind = pkRightUp
				pl.corrX = x
				pl.entry = cl.dock(pl.id, pl.dst, sBottom, sx, true, x-dx)
				return
			}
		}
	}
	if corr, ok := cl.findCorridor([]float64{sx, dx}, dr+1, sr-1, nil); ok {
		pl.kind = pkUpBottom
		pl.corrX = corr
		if corr != sx {
			pl.g1 = sr
			pl.seg1 = cl.lane(sr, pl.id, sx, corr)
		}
		pl.g2 = dr + 1
		pl.seg2 = cl.lane(dr+1, pl.id, corr, dx)
		pl.exit = cl.dock(pl.id, pl.src, sTop, corr, false, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sBottom, corr, false, 0)
		return
	}
	cl.planMargin(pl, sr, dr, false)
}

// planRootMerge: secondary-start chains fan into the merge target's left side.
func (cl *compLayout) planRootMerge(pl *edgePlan, sr, dr int) {
	bend := cl.left(pl.dst) - BendBeforeTarget
	if cl.corridorClear(bend, dr+1, sr-1) && cl.corridorFree(bend) &&
		!cl.nodesBetween(sr, cl.right(pl.src), bend) && !cl.nodesBetween(dr, bend, cl.left(pl.dst)) {
		pl.kind = pkRootMerge
		pl.corrX = bend
		pl.entry = cl.dockLeft(pl, false)
		cl.useCorridor(bend)
		return
	}
	cl.planUpward(pl, sr, dr, cl.x[pl.src], cl.x[pl.dst], false)
}

// planUnderRow: forward hop past intervening nodes on the shared row,
// dipping through the gap below the row (the space above rows is reserved
// for lifted branches).
func (cl *compLayout) planUnderRow(pl *edgePlan, row int) {
	pl.kind = pkUnderRow
	pl.g1 = row + 1
	pl.seg1 = cl.lane(row+1, pl.id, cl.x[pl.src], cl.x[pl.dst])
	pl.exit = cl.dock(pl.id, pl.src, sBottom, cl.x[pl.dst], false, 0)
	pl.entry = cl.dock(pl.id, pl.dst, sBottom, cl.x[pl.src], false, 0)
}

// planBack: every way-back edge drops from the source's bottom to a
// dedicated line in the gap below the lower of the two rows, runs backward,
// and rises into the target's bottom.
func (cl *compLayout) planBack(pl *edgePlan, sr, dr int, sx, dx float64) {
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
			cl.useCorridor(x)
			pl.kind = pkBackBottom
			pl.corrX = x
			pl.g1 = band
			pl.seg1 = cl.lane(band, pl.id, sx, x)
			pl.exit = cl.dock(pl.id, pl.src, sBottom, x, false, 0)
			pl.entry = cl.dock(pl.id, pl.dst, sBottom, x, true, x-dx)
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
	d := &docking{edge: edge, approach: approach, pinned: pinned, fixed: fixed}
	k := sideKey{node, side}
	cl.sides[k] = append(cl.sides[k], d)
	return d
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

func (cl *compLayout) corridorFree(x float64) bool {
	for _, c := range cl.corridors {
		if math.Abs(c.x-x) < 12 {
			return false
		}
	}
	return true
}

func (cl *compLayout) useCorridor(x float64) {
	cl.corridors = append(cl.corridors, corridor{x})
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
				if cl.corridorClear(x, lo, hi) && cl.corridorFree(x) && (extra == nil || extra(x)) {
					cl.useCorridor(x)
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

// assignLanes packs each gap's segments into lanes (first-fit, widest first).
func (cl *compLayout) assignLanes() {
	for _, segs := range cl.gapLanes {
		if len(segs) == 0 {
			continue
		}
		ordered := append([]*laneSeg(nil), segs...)
		sort.SliceStable(ordered, func(i, j int) bool {
			wi, wj := ordered[i].hi()-ordered[i].lo(), ordered[j].hi()-ordered[j].lo()
			if wi != wj {
				return wi > wj
			}
			return ordered[i].edge < ordered[j].edge
		})
		var lanes [][]*laneSeg
	next:
		for _, s := range ordered {
			for li, lane := range lanes {
				ok := true
				for _, o := range lane {
					if s.lo() < o.hi()+20 && o.lo() < s.hi()+20 {
						ok = false
						break
					}
				}
				if ok {
					s.lane = li
					lanes[li] = append(lanes[li], s)
					continue next
				}
			}
			s.lane = len(lanes)
			lanes = append(lanes, []*laneSeg{s})
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
// offset the shape can carry, along the side's own axis. A left docking runs
// down the shape's height and is ordered by the row the edge comes from;
// only a rectangle has a straight left border to spread along, so a circle
// or a diamond keeps its single left point.
func (cl *compLayout) dockingSpan(key sideKey) (center, max float64) {
	if key.side != sLeft {
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
		if key.side != sLeft && cl.node(key.node).Kind.IsGateway() {
			for _, d := range docks {
				d.stub = math.Abs(d.off) > 0.5
			}
		}
	}
}
