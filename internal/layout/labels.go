package layout

import (
	"math"
	"sort"
	"strings"

	"github.com/pyck-ai/bpmnfmt/internal/textmetrics"
)

// placeLabels positions external node labels and sequence flow labels.
func (cl *compLayout) placeLabels() {
	segs := cl.allSegments()

	// Node labels: events and gateways with names.
	for _, n := range cl.c.Nodes {
		if (!n.Kind.IsEvent() && !n.Kind.IsGateway()) || strings.TrimSpace(n.Name) == "" {
			continue
		}
		w, h := textmetrics.Box(n.Name, ExtLabelWrap)
		sh := cl.shape(n.ID)
		bottomBusy := len(cl.sides[sideKey{n.ID, sBottom}]) > 0
		topBusy := len(cl.sides[sideKey{n.ID, sTop}]) > 0

		below := Rect{sh.CX() - w/2, sh.Bottom() + LabelGap, w, h}
		above := Rect{sh.CX() - w/2, sh.Y - LabelGap - h, w, h}
		aboveLeft := Rect{sh.X - w - 8, sh.Y - h + 6, w, h}
		belowLeft := Rect{sh.X - w - 8, sh.Bottom() - 6, w, h}
		aboveRight := Rect{sh.Right() + 8, sh.Y - h + 6, w, h}
		belowRight := Rect{sh.Right() + 8, sh.Bottom() - 6, w, h}

		var order []Rect
		switch {
		case bottomBusy && topBusy:
			order = []Rect{aboveLeft, aboveRight, belowLeft, belowRight, above, below}
		case bottomBusy:
			order = []Rect{above, aboveLeft, aboveRight, below, belowRight}
		case topBusy:
			order = []Rect{below, belowLeft, belowRight, above, aboveRight}
		default:
			order = []Rect{below, above, belowRight, aboveLeft}
		}
		cl.res.Labels[n.ID] = cl.bestRect(order, segs, n.ID)
	}

	// Flow labels.
	for _, fl := range cl.componentFlows() {
		if strings.TrimSpace(fl.Name) == "" {
			continue
		}
		pts := cl.res.Edges[fl.ID]
		if len(pts) < 2 {
			continue
		}
		w, h := textmetrics.Box(fl.Name, FlowLabelWrap)
		p0, p1 := pts[0], pts[1]
		var r Rect
		switch {
		case sameY(p0, p1) && p1.X > p0.X: // rightwards
			r = Rect{p0.X + 8, p0.Y - h - 5, w, h}
		case sameY(p0, p1): // leftwards
			r = Rect{p0.X - 8 - w, p0.Y - h - 5, w, h}
		case p1.Y > p0.Y: // downwards
			r = Rect{p0.X + 8, p0.Y + 5, w, h}
		default: // upwards
			r = Rect{p0.X + 8, p0.Y - h - 5, w, h}
		}
		// Nudge along the segment until free.
		dx, dy := 0.0, 0.0
		if sameY(p0, p1) {
			dx = 14
			if p1.X < p0.X {
				dx = -14
			}
		} else {
			dy = 14
			if p1.Y < p0.Y {
				dy = -14
			}
		}
		for try := 0; try < 6 && cl.collisionCount(r, nil, "") > 0; try++ {
			r.X += dx
			r.Y += dy
		}
		cl.res.EdgeLabels[fl.ID] = r
	}
}

// placeAnnotations positions annotation boxes and draws associations.
// Short annotations go into the band directly above their anchor's row;
// prose annotations are parked in the prose zone at the top, near their
// anchor's x so the association line stays short and steep.
func (cl *compLayout) placeAnnotations() {
	segs := cl.allSegments()
	anns := cl.componentAnnotations()

	// Pass 1: short annotations into their row bands, centered above their
	// anchor whenever possible (grid look), dodging in 40px grid steps and
	// only then in finer steps.
	for _, ann := range anns {
		if ann.prose || ann.anchor == "" {
			continue
		}
		r := cl.rowOf[ann.anchor]
		anchorSh := cl.shape(ann.anchor)
		y := cl.rowCY[r] - RowH/2 - AnnGap - ann.h
		var offs []float64
		for k := 0.0; k <= 240; k += 40 {
			offs = append(offs, k)
			if k > 0 {
				offs = append(offs, -k)
			}
		}
		for k := 20.0; k <= 480; k += 20 {
			offs = append(offs, k, -k)
		}
		var cands []Rect
		for _, off := range offs {
			cands = append(cands, Rect{anchorSh.CX() + off - ann.w/2, y, ann.w, ann.h})
		}
		cl.res.Shapes[ann.id] = cl.bestRect(cands, segs, "")
	}

	// Pass 2: prose annotations are parked outside the flow, above or below
	// the diagram — whichever zone gives the shortest association line that
	// stays clear of other shapes (slight preference for the top zone).
	bottomY := cl.maxContentBottom() + 40
	// Place prose notes in anchor order (left to right) so their boxes pack
	// in the same order as their anchors — inversions force line crossings.
	var prose []*annInfo
	for _, ann := range anns {
		if ann.prose || ann.anchor == "" {
			prose = append(prose, ann)
		}
	}
	sort.SliceStable(prose, func(i, j int) bool {
		xi, xj := 0.0, 0.0
		if prose[i].anchor != "" {
			xi = cl.x[prose[i].anchor]
		}
		if prose[j].anchor != "" {
			xj = cl.x[prose[j].anchor]
		}
		return xi < xj
	})
	var prosePlaced []Rect
	var proseLines [][2]Point
	for _, ann := range prose {
		baseX := 0.0
		var anchorRect Rect
		hasAnchor := false
		if ann.anchor != "" {
			anchorRect = cl.shape(ann.anchor)
			if ann.flowID != "" {
				if pts := cl.res.Edges[ann.flowID]; len(pts) >= 2 {
					mid := Point{(pts[0].X + pts[1].X) / 2, (pts[0].Y + pts[1].Y) / 2}
					anchorRect = Rect{mid.X, mid.Y, 0, 0}
				}
			}
			baseX = anchorRect.CX() - ann.w/2
			hasAnchor = true
		}
		// Zone choice is restricted to the anchor's side of the diagram so
		// the comment line never traverses the full diagram height.
		type zone struct {
			y    float64
			bias float64
		}
		var zones []zone
		if hasAnchor {
			mid := float64(len(cl.rows)-1) / 2
			r := float64(cl.rowOf[ann.anchor])
			if r <= mid {
				zones = append(zones, zone{0, 0})
			}
			if r >= mid {
				zones = append(zones, zone{bottomY, 0.5})
			}
		} else {
			zones = []zone{{0, 0}}
		}
		var offsets []float64
		for k := 0.0; k <= 2400; k += 20 {
			offsets = append(offsets, k)
			if k > 0 {
				offsets = append(offsets, -k)
			}
		}
		best := Rect{baseX, 0, ann.w, ann.h}
		bestScore := math.Inf(1)
		for _, zone := range zones {
			for _, off := range offsets {
				cand := Rect{baseX + off, zone.y, ann.w, ann.h}
				score := zone.bias
				for _, pr := range prosePlaced {
					if cand.Overlaps(pr.Grow(20)) {
						score += 100
					}
				}
				if hasAnchor {
					from := borderPoint(anchorRect, Point{cand.CX(), cand.CY()})
					to := borderPoint(cand, from)
					score += 3 * float64(cl.lineCollisions(from, to, ann.anchor, ann.id))
					for _, l := range proseLines {
						if segsCross(from, to, l[0], l[1]) {
							score += 10 // comment lines must not cross each other
						}
					}
				}
				score += math.Abs(off) / 1000 // prefer staying near the anchor
				if score < bestScore {
					bestScore = score
					best = cand
				}
			}
		}
		prosePlaced = append(prosePlaced, best)
		cl.res.Shapes[ann.id] = best
		if hasAnchor {
			from := borderPoint(anchorRect, Point{best.CX(), best.CY()})
			proseLines = append(proseLines, [2]Point{from, borderPoint(best, from)})
		}
	}

	// Pass 3: associations.
	for _, as := range cl.p.Associations {
		annID, other := "", ""
		switch {
		case cl.p.AnnByID[as.SourceRef] != nil:
			annID, other = as.SourceRef, as.TargetRef
		case cl.p.AnnByID[as.TargetRef] != nil:
			annID, other = as.TargetRef, as.SourceRef
		default:
			continue
		}
		annRect, ok := cl.res.Shapes[annID]
		if !ok {
			continue // annotation belongs to another component
		}
		var from Point
		if fl := cl.p.FlowByID[other]; fl != nil {
			pts := cl.res.Edges[fl.ID]
			if len(pts) < 2 {
				continue
			}
			from = Point{(pts[0].X + pts[1].X) / 2, (pts[0].Y + pts[1].Y) / 2}
		} else if sh, ok := cl.res.Shapes[other]; ok {
			from = borderPoint(sh, Point{annRect.CX(), annRect.CY()})
		} else {
			continue
		}
		to := borderPoint(annRect, from)
		cl.res.Edges[as.ID] = []Point{from, to}
	}
}

// maxContentBottom returns the lowest extent of shapes and edges.
func (cl *compLayout) maxContentBottom() float64 {
	max := 0.0
	for _, sh := range cl.res.Shapes {
		if sh.Bottom() > max {
			max = sh.Bottom()
		}
	}
	for _, pts := range cl.res.Edges {
		for _, pt := range pts {
			if pt.Y > max {
				max = pt.Y
			}
		}
	}
	for _, l := range cl.res.Labels {
		if l.Bottom() > max {
			max = l.Bottom()
		}
	}
	return max
}

// lineCollisions counts shapes and label texts (other than the given
// elements) hit by a line. Comment lines must not run through text.
func (cl *compLayout) lineCollisions(a, b Point, skip1, skip2 string) int {
	n := 0
	for id, sh := range cl.res.Shapes {
		if id == skip1 || id == skip2 {
			continue
		}
		if segIntersectsRect(a, b, sh) {
			n++
		}
	}
	for _, l := range cl.res.Labels {
		if segIntersectsRect(a, b, l) {
			n++
		}
	}
	for _, l := range cl.res.EdgeLabels {
		if segIntersectsRect(a, b, l) {
			n++
		}
	}
	return n
}

// bestRect returns the first collision-free candidate, falling back to the
// one with the fewest collisions.
func (cl *compLayout) bestRect(cands []Rect, segs [][2]Point, selfID string) Rect {
	best := cands[0]
	bestN := math.MaxInt32
	for _, c := range cands {
		n := cl.collisionCount(c, segs, selfID)
		if n == 0 {
			return c
		}
		if n < bestN {
			bestN = n
			best = c
		}
		// A side candidate sits at a fixed offset from the node, so it can
		// overlap a neighbour that a small shift would clear. Nudge it
		// along its own axis before giving up on it.
		for _, step := range []float64{7, -7, 14, -14, 21, -21} {
			s := Rect{c.X + step, c.Y, c.W, c.H}
			n := cl.collisionCount(s, segs, selfID)
			if n == 0 {
				return s
			}
			if n < bestN {
				bestN = n
				best = s
			}
		}
	}
	return best
}

// allSegments collects every routed polyline segment for collision tests.
func (cl *compLayout) allSegments() [][2]Point {
	var segs [][2]Point
	for _, pts := range cl.res.Edges {
		for i := 0; i+1 < len(pts); i++ {
			segs = append(segs, [2]Point{pts[i], pts[i+1]})
		}
	}
	return segs
}

// collisionCount tests a rect against shapes, placed labels and (optionally)
// edge segments. selfID skips the rect owner's own shape.
func (cl *compLayout) collisionCount(r Rect, segs [][2]Point, selfID string) int {
	n := 0
	for id, sh := range cl.res.Shapes {
		if id != selfID && r.Overlaps(sh.Grow(2)) {
			n++
		}
	}
	for _, l := range cl.res.Labels {
		if r.Overlaps(l.Grow(2)) {
			n++
		}
	}
	for _, l := range cl.res.EdgeLabels {
		if r.Overlaps(l.Grow(2)) {
			n++
		}
	}
	for _, s := range segs {
		if segIntersectsRect(s[0], s[1], r.Grow(4)) {
			n++
		}
	}
	return n
}

// segIntersectsRect tests a segment (any slope) against a rect using
// Liang-Barsky clipping.
func segIntersectsRect(a, b Point, r Rect) bool {
	dx, dy := b.X-a.X, b.Y-a.Y
	t0, t1 := 0.0, 1.0
	clip := func(p, q float64) bool {
		if p == 0 {
			return q >= 0 // parallel: inside iff q >= 0
		}
		t := q / p
		if p < 0 {
			if t > t1 {
				return false
			}
			if t > t0 {
				t0 = t
			}
		} else {
			if t < t0 {
				return false
			}
			if t < t1 {
				t1 = t
			}
		}
		return true
	}
	if !clip(-dx, a.X-r.X) || !clip(dx, r.Right()-a.X) ||
		!clip(-dy, a.Y-r.Y) || !clip(dy, r.Bottom()-a.Y) {
		return false
	}
	return t1 > t0
}

// segsCross reports whether two segments (any slope) cross strictly.
func segsCross(a1, a2, b1, b2 Point) bool {
	d := func(p, q, r Point) float64 {
		return (q.X-p.X)*(r.Y-p.Y) - (q.Y-p.Y)*(r.X-p.X)
	}
	d1, d2 := d(a1, a2, b1), d(a1, a2, b2)
	d3, d4 := d(b1, b2, a1), d(b1, b2, a2)
	return d1*d2 < 0 && d3*d4 < 0
}

// borderPoint returns the point where the line from the rect center towards
// target crosses the rect border.
func borderPoint(r Rect, target Point) Point {
	cx, cy := r.CX(), r.CY()
	dx, dy := target.X-cx, target.Y-cy
	if dx == 0 && dy == 0 {
		return Point{cx, cy}
	}
	t := math.Inf(1)
	if dx != 0 {
		t = math.Min(t, (r.W/2)/math.Abs(dx))
	}
	if dy != 0 {
		t = math.Min(t, (r.H/2)/math.Abs(dy))
	}
	return Point{cx + dx*t, cy + dy*t}
}
