package layout

import (
	"fmt"
	"math"
	"sort"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// Validate checks the layout post-conditions and returns human-readable
// violations (empty = clean):
//
//   - no two shapes overlap (nodes, annotations),
//   - external labels do not overlap shapes,
//   - no edge segment passes through a foreign shape,
//   - forward sequence flows never move leftwards (center to center),
//   - spine nodes share one centerline with ascending x.
func Validate(p *model.Process, g *graph.Graph, res *Result) []string {
	var out []string

	// Shape-shape overlaps.
	ids := sortedKeys(res.Shapes)
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			if res.Shapes[a].Overlaps(res.Shapes[b]) {
				out = append(out, fmt.Sprintf("shapes overlap: %s and %s", a, b))
			}
		}
	}

	// Label-shape overlaps.
	for _, lid := range sortedKeys(res.Labels) {
		l := res.Labels[lid]
		for _, sid := range ids {
			if sid != lid && l.Overlaps(res.Shapes[sid].Grow(-1)) {
				out = append(out, fmt.Sprintf("label of %s overlaps shape %s", lid, sid))
			}
		}
	}

	// Edge segments through foreign shapes.
	edgeIDs := make([]string, 0, len(res.Edges))
	for k := range res.Edges {
		edgeIDs = append(edgeIDs, k)
	}
	sort.Strings(edgeIDs)
	for _, eid := range edgeIDs {
		pts := res.Edges[eid]
		skip := edgeEndpoints(p, eid)
		for i := 0; i+1 < len(pts); i++ {
			for _, sid := range ids {
				if skip[sid] {
					continue
				}
				if segIntersectsRect(pts[i], pts[i+1], res.Shapes[sid].Grow(-2)) {
					out = append(out, fmt.Sprintf("edge %s passes through shape %s", eid, sid))
				}
			}
		}
	}

	// Forward flows move rightwards.
	for _, fl := range p.Flows {
		if g.Back[fl.ID] {
			continue
		}
		src, okS := res.Shapes[fl.SourceRef]
		dst, okD := res.Shapes[fl.TargetRef]
		if okS && okD && dst.CX() < src.CX()-0.5 {
			out = append(out, fmt.Sprintf("forward flow %s moves leftwards (%.0f -> %.0f)", fl.ID, src.CX(), dst.CX()))
		}
	}

	// Spine straightness.
	for ci, comp := range g.Components {
		lastX := math.Inf(-1)
		cy := math.NaN()
		for _, id := range comp.Spine {
			sh, ok := res.Shapes[id]
			if !ok {
				continue
			}
			if math.IsNaN(cy) {
				cy = sh.CY()
			} else if math.Abs(sh.CY()-cy) > 0.5 {
				out = append(out, fmt.Sprintf("component %d: spine node %s off the spine row", ci, id))
			}
			if sh.CX() <= lastX {
				out = append(out, fmt.Sprintf("component %d: spine node %s not right of its predecessor", ci, id))
			}
			lastX = sh.CX()
		}
	}

	sort.Strings(out)
	return out
}

// CountCrossings counts transversal crossings between sequence flow segments
// (associations excluded; touching endpoints and collinear overlaps do not
// count).
func CountCrossings(p *model.Process, res *Result) int {
	type seg struct {
		a, b Point
		edge string
	}
	var segs []seg
	for _, fl := range p.Flows {
		pts := res.Edges[fl.ID]
		for i := 0; i+1 < len(pts); i++ {
			segs = append(segs, seg{pts[i], pts[i+1], fl.ID})
		}
	}
	count := 0
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			if segs[i].edge == segs[j].edge {
				continue
			}
			if properCrossing(segs[i].a, segs[i].b, segs[j].a, segs[j].b) {
				count++
			}
		}
	}
	return count
}

// properCrossing for axis-parallel segments: one horizontal, one vertical,
// intersecting strictly inside both.
func properCrossing(a1, a2, b1, b2 Point) bool {
	aH, bH := sameY(a1, a2), sameY(b1, b2)
	if aH == bH {
		return false // parallel (or degenerate): collinear overlap not counted
	}
	h1, h2, v1, v2 := a1, a2, b1, b2
	if !aH {
		h1, h2, v1, v2 = b1, b2, a1, a2
	}
	xlo, xhi := math.Min(h1.X, h2.X), math.Max(h1.X, h2.X)
	ylo, yhi := math.Min(v1.Y, v2.Y), math.Max(v1.Y, v2.Y)
	const e = 0.5
	return v1.X > xlo+e && v1.X < xhi-e && h1.Y > ylo+e && h1.Y < yhi-e
}

func edgeEndpoints(p *model.Process, edgeID string) map[string]bool {
	skip := map[string]bool{}
	if fl := p.FlowByID[edgeID]; fl != nil {
		skip[fl.SourceRef] = true
		skip[fl.TargetRef] = true
		return skip
	}
	for _, as := range p.Associations {
		if as.ID == edgeID {
			skip[as.SourceRef] = true
			skip[as.TargetRef] = true
			// Flow-anchored associations start at the flow midpoint.
			if fl := p.FlowByID[as.SourceRef]; fl != nil {
				skip[fl.SourceRef] = true
				skip[fl.TargetRef] = true
			}
			if fl := p.FlowByID[as.TargetRef]; fl != nil {
				skip[fl.SourceRef] = true
				skip[fl.TargetRef] = true
			}
		}
	}
	return skip
}

func sortedKeys(m map[string]Rect) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
