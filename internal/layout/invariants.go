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
//   - no two edge labels overlap,
//   - edge labels do not overlap shapes,
//   - no edge segment passes through a foreign shape,
//   - forward sequence flows never move leftwards (center to center),
//   - spine nodes share one centerline with ascending x.
func Validate(p *model.Process, g *graph.Graph, res *Result) []string {
	var out []string

	scopes := scopeList(p)
	// An expanded sub-process legitimately contains its interior shapes and
	// interior edges: record container ids and, per element, its container so
	// those overlaps/crossings are not flagged.
	containers := map[string]bool{}
	ownerOf := map[string]string{}
	for _, n := range p.Nodes {
		if n.Sub == nil {
			continue
		}
		containers[n.ID] = true
		for _, in := range n.Sub.Nodes {
			ownerOf[in.ID] = n.ID
		}
		for _, fl := range n.Sub.Flows {
			ownerOf[fl.ID] = n.ID
		}
		for _, a := range n.Sub.Annotations {
			ownerOf[a.ID] = n.ID
		}
		for _, as := range n.Sub.Associations {
			ownerOf[as.ID] = n.ID
		}
	}
	nested := func(a, b string) bool {
		return (containers[a] && ownerOf[b] == a) || (containers[b] && ownerOf[a] == b)
	}

	// Shape-shape overlaps (a container may contain its own children).
	ids := sortedKeys(res.Shapes)
	for i, a := range ids {
		for _, b := range ids[i+1:] {
			if nested(a, b) {
				continue
			}
			if res.Shapes[a].Overlaps(res.Shapes[b]) {
				out = append(out, fmt.Sprintf("shapes overlap: %s and %s", a, b))
			}
		}
	}

	// Label-shape overlaps (skip the label's own container).
	for _, lid := range sortedKeys(res.Labels) {
		l := res.Labels[lid]
		for _, sid := range ids {
			if sid == lid || ownerOf[lid] == sid {
				continue
			}
			if l.Overlaps(res.Shapes[sid].Grow(-1)) {
				out = append(out, fmt.Sprintf("label of %s overlaps shape %s", lid, sid))
			}
		}
	}

	// Edge-label overlaps.
	edgeLabelIDs := sortedKeys(res.EdgeLabels)
	for i, a := range edgeLabelIDs {
		for _, b := range edgeLabelIDs[i+1:] {
			if res.EdgeLabels[a].Overlaps(res.EdgeLabels[b]) {
				out = append(out, fmt.Sprintf("edge labels overlap: %s and %s", a, b))
			}
		}
	}

	// Edge-label/shape overlaps (skip the label's own container).
	for _, lid := range edgeLabelIDs {
		l := res.EdgeLabels[lid]
		for _, sid := range ids {
			if ownerOf[lid] == sid {
				continue
			}
			if l.Overlaps(res.Shapes[sid].Grow(-1)) {
				out = append(out, fmt.Sprintf("edge label of %s overlaps shape %s", lid, sid))
			}
		}
	}

	// Edge segments through foreign shapes (an interior edge may lie inside
	// its own container).
	edgeIDs := make([]string, 0, len(res.Edges))
	for k := range res.Edges {
		edgeIDs = append(edgeIDs, k)
	}
	sort.Strings(edgeIDs)
	for _, eid := range edgeIDs {
		pts := res.Edges[eid]
		skip := edgeEndpoints(scopes, eid)
		owner := ownerOf[eid]
		for i := 0; i+1 < len(pts); i++ {
			for _, sid := range ids {
				if skip[sid] || sid == owner {
					continue
				}
				if segIntersectsRect(pts[i], pts[i+1], res.Shapes[sid].Grow(-2)) {
					out = append(out, fmt.Sprintf("edge %s passes through shape %s", eid, sid))
				}
			}
		}
	}

	// Forward-flow direction and spine straightness, per scope.
	checkFlowGeometry(p, g, res, &out)
	for _, n := range p.Nodes {
		if n.Sub != nil {
			checkFlowGeometry(n.Sub, graph.Build(n.Sub), res, &out)
		}
	}

	sort.Strings(out)
	return out
}

// checkFlowGeometry validates that forward sequence flows never move leftwards
// and that the spine of every component sits on one ascending centerline,
// within a single scope (a process or a sub-process interior).
func checkFlowGeometry(p *model.Process, g *graph.Graph, res *Result, out *[]string) {
	for _, fl := range p.Flows {
		if g.Back[fl.ID] {
			continue
		}
		src, okS := res.Shapes[fl.SourceRef]
		dst, okD := res.Shapes[fl.TargetRef]
		if okS && okD && dst.CX() < src.CX()-0.5 {
			*out = append(*out, fmt.Sprintf("forward flow %s moves leftwards (%.0f -> %.0f)", fl.ID, src.CX(), dst.CX()))
		}
	}
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
				*out = append(*out, fmt.Sprintf("component %d: spine node %s off the spine row", ci, id))
			}
			if sh.CX() <= lastX {
				*out = append(*out, fmt.Sprintf("component %d: spine node %s not right of its predecessor", ci, id))
			}
			lastX = sh.CX()
		}
	}
}

// scopeList returns the process and every expanded sub-process interior.
func scopeList(p *model.Process) []*model.Process {
	scopes := []*model.Process{p}
	for _, n := range p.Nodes {
		if n.Sub != nil {
			scopes = append(scopes, n.Sub)
		}
	}
	return scopes
}

// CountCrossings counts transversal crossings between sequence flow segments
// (associations excluded; touching endpoints and collinear overlaps do not
// count).
// CountCrossings counts forbidden edge crossings. Way-back edges occupy
// dedicated lines below their rows on which crossings are explicitly
// allowed, so any pair involving a back edge is skipped; the nesting of
// way-back lines is guarded by geometry assertions instead.
func CountCrossings(p *model.Process, res *Result) int {
	type seg struct {
		a, b Point
		edge string
	}
	back := map[string]bool{}
	var segs []seg
	for _, sc := range scopeList(p) {
		for id := range graph.Build(sc).Back {
			back[id] = true
		}
		for _, fl := range sc.Flows {
			pts := res.Edges[fl.ID]
			for i := 0; i+1 < len(pts); i++ {
				segs = append(segs, seg{pts[i], pts[i+1], fl.ID})
			}
		}
	}
	count := 0
	for i := 0; i < len(segs); i++ {
		for j := i + 1; j < len(segs); j++ {
			if segs[i].edge == segs[j].edge {
				continue
			}
			if back[segs[i].edge] || back[segs[j].edge] {
				continue // crossings on way-back lines are allowed
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

func edgeEndpoints(scopes []*model.Process, edgeID string) map[string]bool {
	skip := map[string]bool{}
	flowByID := func(id string) *model.SequenceFlow {
		for _, sc := range scopes {
			if fl := sc.FlowByID[id]; fl != nil {
				return fl
			}
		}
		return nil
	}
	if fl := flowByID(edgeID); fl != nil {
		skip[fl.SourceRef] = true
		skip[fl.TargetRef] = true
		return skip
	}
	for _, sc := range scopes {
		for _, as := range sc.Associations {
			if as.ID != edgeID {
				continue
			}
			skip[as.SourceRef] = true
			skip[as.TargetRef] = true
			// Flow-anchored associations start at the flow midpoint.
			if fl := flowByID(as.SourceRef); fl != nil {
				skip[fl.SourceRef] = true
				skip[fl.TargetRef] = true
			}
			if fl := flowByID(as.TargetRef); fl != nil {
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
