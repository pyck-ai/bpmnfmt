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
)

type sideKey struct {
	node string
	side nodeSide
}

// docking is one edge endpoint on a node's top or bottom side. Offsets are
// relative to the node center and resolved after all plans exist. Left/right
// side dockings always sit on the row centerline and need no bookkeeping.
type docking struct {
	edge     string
	approach float64 // x of the far end; orders dockings along the side
	pinned   bool    // must sit exactly at fixed (alignment / corridor)
	fixed    float64
	off      float64 // resolved
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

// corridor registers a vertical line crossing row bands. owner names the
// docking it ends at ("node/side") — verticals with the same owner may share
// the exact same x, merging visually into one trunk (classic fan-in look).
type corridor struct {
	x     float64
	owner string
}

type planKind int

const (
	pkH          planKind = iota // straight horizontal along a row
	pkVDown                      // straight vertical, source above target
	pkVUp                        // straight vertical, source below target
	pkDownLeftIn                 // exit bottom, drop beside the source, enter left side
	pkDownJog                    // exit bottom, lane below source row, corridor down, enter left side
	pkDownTop                    // exit bottom, lane below source row, corridor down, lane above target row, enter top
	pkRightUp                    // exit right, horizontal to the target column, straight up into the bottom
	pkUpBottom                   // exit top, corridor up (optional jog), lane below target row, enter bottom
	pkLoopTop                    // exit top, corridor up (optional jog), lane above target row, enter top
	pkRootMerge                  // exit right, bend before the target, enter left side
	pkOverRow                    // exit top, lane above own row, enter target top
	pkBackBottom                 // exit bottom, lane below source row, corridor up, enter bottom
	pkBackMargin                 // around the outside via the margin corridor, enter top
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

		case fcEntry:
			cl.planDownward(pl, sr, dr, sx, dx, aligned)

		case fcExit, fcCross:
			switch {
			case sr == dr:
				if cl.nodesBetween(sr, cl.right(pl.src), cl.left(pl.dst)) {
					cl.planOverRow(pl, sr)
				} else {
					pl.kind = pkH
				}
			case sr > dr: // target above
				cl.planUpward(pl, sr, dr, sx, dx, aligned)
			default: // target below
				cl.planDownward(pl, sr, dr, sx, dx, aligned)
			}

		case fcRootExit:
			cl.planRootMerge(pl, sr, dr)

		case fcBack:
			switch {
			case sr == dr:
				cl.planOverRow(pl, sr)
			case sr > dr:
				cl.planLoopUp(pl, sr, dr, sx, dx)
			default:
				cl.planMargin(pl, sr, dr)
			}
		}
	}
	cl.assignLanes()
	cl.resolveDockings()
	return nil
}

func (cl *compLayout) left(id string) float64  { return cl.x[id] - cl.width(id)/2 }
func (cl *compLayout) right(id string) float64 { return cl.x[id] + cl.width(id)/2 }

// planDownward: source row above target row, forward direction.
func (cl *compLayout) planDownward(pl *edgePlan, sr, dr int, sx, dx float64, aligned bool) {
	if aligned && cl.corridorClear(sx, sr+1, dr-1) && cl.corridorFree(sx, trunk(pl.dst, sTop)) {
		pl.kind = pkVDown
		pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, true, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sTop, sx, true, 0)
		cl.useCorridor(sx, trunk(pl.dst, sTop))
		return
	}
	// Drop straight down at the source, enter the target's left side.
	if cl.corridorClear(sx, sr+1, dr-1) && cl.corridorFree(sx, "") &&
		sx < cl.left(pl.dst) && !cl.nodesBetween(dr, sx, cl.left(pl.dst)) {
		pl.kind = pkDownLeftIn
		pl.corrX = sx
		pl.exit = cl.dock(pl.id, pl.src, sBottom, dx, false, 0)
		cl.useCorridor(sx, "")
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
	cl.planMargin(pl, sr, dr)
}

// planUpward: source row below target row, forward direction (rejoin).
func (cl *compLayout) planUpward(pl *edgePlan, sr, dr int, sx, dx float64, aligned bool) {
	if aligned && cl.corridorClear(sx, dr+1, sr-1) && cl.corridorFree(sx, trunk(pl.dst, sBottom)) {
		pl.kind = pkVUp
		pl.exit = cl.dock(pl.id, pl.src, sTop, dx, true, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sBottom, sx, true, 0)
		cl.useCorridor(sx, trunk(pl.dst, sBottom))
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
				cl.corridorClear(x, dr+1, sr) && cl.corridorFree(x, trunk(pl.dst, sBottom)) {
				cl.useCorridor(x, trunk(pl.dst, sBottom))
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
	cl.planMargin(pl, sr, dr)
}

// planRootMerge: secondary-start chains fan into the merge target's left side.
func (cl *compLayout) planRootMerge(pl *edgePlan, sr, dr int) {
	bend := cl.left(pl.dst) - BendBeforeTarget
	if cl.corridorClear(bend, dr+1, sr-1) && cl.corridorFree(bend, "") &&
		!cl.nodesBetween(sr, cl.right(pl.src), bend) && !cl.nodesBetween(dr, bend, cl.left(pl.dst)) {
		pl.kind = pkRootMerge
		pl.corrX = bend
		cl.useCorridor(bend, "")
		return
	}
	cl.planUpward(pl, sr, dr, cl.x[pl.src], cl.x[pl.dst], false)
}

// planOverRow: hop or loop through the gap above the shared row.
func (cl *compLayout) planOverRow(pl *edgePlan, row int) {
	pl.kind = pkOverRow
	pl.g1 = row
	pl.seg1 = cl.lane(row, pl.id, cl.x[pl.src], cl.x[pl.dst])
	pl.exit = cl.dock(pl.id, pl.src, sTop, cl.x[pl.dst], false, 0)
	pl.entry = cl.dock(pl.id, pl.dst, sTop, cl.x[pl.src], false, 0)
}

// planLoopUp: back edge whose target row lies above the source row.
func (cl *compLayout) planLoopUp(pl *edgePlan, sr, dr int, sx, dx float64) {
	// Preferred: under the source row, then straight up into the target's
	// bottom ("loops back under").
	halfW := cl.width(pl.dst)/2 - 4
	for _, off := range []float64{0, -SlotStep, SlotStep, -2 * SlotStep, 2 * SlotStep} {
		if math.Abs(off) > halfW {
			continue
		}
		x := dx + off
		if cl.corridorClear(x, dr+1, sr) && cl.corridorFree(x, trunk(pl.dst, sBottom)) {
			cl.useCorridor(x, trunk(pl.dst, sBottom))
			pl.kind = pkBackBottom
			pl.corrX = x
			pl.g1 = sr + 1
			pl.seg1 = cl.lane(sr+1, pl.id, sx, x)
			pl.exit = cl.dock(pl.id, pl.src, sBottom, x, false, 0)
			pl.entry = cl.dock(pl.id, pl.dst, sBottom, x, true, x-dx)
			return
		}
	}
	// Second choice: up beside the source, over the target row, into its top.
	if corr, ok := cl.findCorridor([]float64{sx}, dr, sr-1, nil); ok {
		pl.kind = pkLoopTop
		pl.corrX = corr
		if corr != sx {
			pl.g1 = sr
			pl.seg1 = cl.lane(sr, pl.id, sx, corr)
		}
		pl.g2 = dr
		pl.seg2 = cl.lane(dr, pl.id, corr, dx)
		pl.exit = cl.dock(pl.id, pl.src, sTop, corr, false, 0)
		pl.entry = cl.dock(pl.id, pl.dst, sTop, corr, false, 0)
		return
	}
	cl.planMargin(pl, sr, dr)
}

// planMargin routes around the far left of the component.
func (cl *compLayout) planMargin(pl *edgePlan, sr, dr int) {
	pl.kind = pkBackMargin
	pl.marginIdx = cl.marginUse
	cl.marginUse++
	if sr >= dr { // exit below the source row, travel up at the margin
		pl.g1 = sr + 1
		pl.exit = cl.dock(pl.id, pl.src, sBottom, marginSentinel, false, 0)
	} else { // exit above the source row, travel down at the margin
		pl.g1 = sr
		pl.exit = cl.dock(pl.id, pl.src, sTop, marginSentinel, false, 0)
	}
	pl.g2 = dr
	pl.seg1 = cl.lane(pl.g1, pl.id, cl.x[pl.src], marginSentinel)
	pl.seg2 = cl.lane(pl.g2, pl.id, marginSentinel, cl.x[pl.dst])
	pl.entry = cl.dock(pl.id, pl.dst, sTop, marginSentinel, false, 0)
}

// --- helpers -----------------------------------------------------------------

func (cl *compLayout) dock(edge, node string, side nodeSide, approach float64, pinned bool, fixed float64) *docking {
	d := &docking{edge: edge, approach: approach, pinned: pinned, fixed: fixed}
	k := sideKey{node, side}
	cl.sides[k] = append(cl.sides[k], d)
	return d
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

func (cl *compLayout) corridorFree(x float64, owner string) bool {
	for _, c := range cl.corridors {
		if math.Abs(c.x-x) < 12 {
			if owner != "" && c.owner == owner && math.Abs(c.x-x) < 0.5 {
				continue // shared trunk into the same docking
			}
			return false
		}
	}
	return true
}

func (cl *compLayout) useCorridor(x float64, owner string) {
	cl.corridors = append(cl.corridors, corridor{x, owner})
}

func trunk(node string, side nodeSide) string {
	if side == sTop {
		return node + "/top"
	}
	return node + "/bottom"
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
				if cl.corridorClear(x, lo, hi) && cl.corridorFree(x, "") && (extra == nil || extra(x)) {
					cl.useCorridor(x, "")
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

// resolveDockings spreads the dockings of each node side around its center.
func (cl *compLayout) resolveDockings() {
	for key, docks := range cl.sides {
		w := cl.width(key.node)
		var pinned, free []*docking
		for _, d := range docks {
			if d.pinned {
				pinned = append(pinned, d)
				d.off = d.fixed
			} else {
				free = append(free, d)
			}
		}
		n := len(free)
		if n == 0 {
			continue
		}
		sort.SliceStable(free, func(i, j int) bool {
			if free[i].approach != free[j].approach {
				return free[i].approach < free[j].approach
			}
			return free[i].edge < free[j].edge
		})
		// Candidate offsets: whole multiples of SlotStep around the center,
		// nearest first, preferring the side the edge approaches from, and
		// skipping pinned positions.
		taken := func(off float64) bool {
			for _, p := range pinned {
				if math.Abs(p.off-off) < SlotStep-1 {
					return true
				}
			}
			return false
		}
		if len(pinned) == 0 && n == 1 {
			free[0].off = 0
			continue
		}
		center := cl.x[key.node]
		used := map[float64]bool{}
		for _, d := range free {
			side := 1.0
			if d.approach < center {
				side = -1
			}
			d.off = 0
			found := false
			for k := 0; k <= 6 && !found; k++ {
				for _, s := range []float64{side, -side} {
					off := s * float64(k) * SlotStep
					if k == 0 && s != side {
						continue
					}
					if math.Abs(off) > w/2-4 {
						continue
					}
					if taken(off) || used[off] {
						continue
					}
					d.off = off
					used[off] = true
					found = true
					break
				}
			}
		}
	}
}
