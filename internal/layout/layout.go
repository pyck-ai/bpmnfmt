// Package layout computes a canonical BPMN diagram layout:
//
//   - the happy path (spine) runs left to right on one row,
//   - a spine gateway with exactly three outgoing flows uses all three
//     diamond corners (shorter alternate up, longer down); otherwise
//     alternates hang below in tiers, longest first,
//   - back edges run on dedicated way-back lines below their rows and
//     rise into the target's bottom; crossings are allowed only there,
//   - everything is deterministic and derived from the model only.
package layout

import (
	"fmt"
	"math"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// Tunables (px). Sizes follow bpmn-js defaults.
const (
	TaskW, TaskH     = 100.0, 80.0
	EventS           = 36.0
	GatewayS         = 50.0
	GapX             = 60.0  // minimum border-to-border gap along a row
	ColPitch         = 160.0 // column grid: node centers snap to columns
	RowH             = 80.0  // height of a node row band
	LaneStep         = 24.0  // distance between parallel channel lanes
	LanePad          = 14.0  // channel padding above/below the lane block
	MinGapH          = 50.0  // minimum vertical gap between rows
	Margin           = 50.0  // outer margin
	SlotStep         = 14.0  // spacing between dockings on one node side
	StubLen          = 14.0  // shared stub between a diamond corner and its lanes
	Clearance        = 15.0  // corridor distance to any shape
	AnnGap           = 14.0  // gap between annotation band and node row
	ProseGap         = 24.0  // gap below the prose-annotation zone
	ComponentGap     = 110.0
	ChainPad         = 20.0 // routing margin added to chain extents
	SubProcPadX      = 35.0 // horizontal padding between a sub-process border and its interior
	SubProcHeaderH   = 40.0 // room above the interior for the sub-process title
	SubProcPadBottom = 35.0 // padding below the interior (also leaves room for MI markers)
	LabelGap         = 7.0  // node label offset
	ShortAnnWrap     = 150.0
	ProseAnnWrap     = 240.0
	ExtLabelWrap     = 90.0 // external label wrap width (events/gateways)
	FlowLabelWrap    = 110.0
	BendBeforeTarget = 25.0 // bend distance for left-side fan-ins
)

// Point is a waypoint.
type Point struct{ X, Y float64 }

// Size is a shape's width and height.
type Size struct{ W, H float64 }

// Rect is an axis-aligned box.
type Rect struct{ X, Y, W, H float64 }

func (r Rect) CX() float64     { return r.X + r.W/2 }
func (r Rect) CY() float64     { return r.Y + r.H/2 }
func (r Rect) Right() float64  { return r.X + r.W }
func (r Rect) Bottom() float64 { return r.Y + r.H }

// Overlaps reports proper area overlap (touching edges is fine).
func (r Rect) Overlaps(o Rect) bool {
	return r.X < o.Right() && o.X < r.Right() && r.Y < o.Bottom() && o.Y < r.Bottom()
}

// Grow returns the rect expanded by m on every side.
func (r Rect) Grow(m float64) Rect {
	return Rect{r.X - m, r.Y - m, r.W + 2*m, r.H + 2*m}
}

// Result is the computed layout in absolute coordinates.
type Result struct {
	Shapes     map[string]Rect    // flow nodes and text annotations
	Labels     map[string]Rect    // external node labels (events, gateways)
	EdgeLabels map[string]Rect    // labels of named sequence flows
	Edges      map[string][]Point // sequence flows and associations
	// Retrograde holds the sequence flows of chains laid out right to left
	// (rule L3). They legitimately run leftwards, so the forward-direction
	// invariant exempts exactly these and no others.
	Retrograde map[string]bool
	// Merged maps a rejoin bundle's follower to the flow that owns the
	// shared riser (rule M2): both end at the same point on purpose, with
	// one arrowhead between them. Declared, never inferred — an accidental
	// pileup of two unrelated inflows must stay reportable.
	Merged map[string]string
}

// Compute lays out one process.
func Compute(p *model.Process, g *graph.Graph) (*Result, error) {
	res := &Result{
		Shapes:     map[string]Rect{},
		Labels:     map[string]Rect{},
		EdgeLabels: map[string]Rect{},
		Edges:      map[string][]Point{},
		Retrograde: map[string]bool{},
		Merged:     map[string]string{},
	}

	// Expanded sub-processes are laid out first (in their own coordinate
	// space); their bounding box drives the container node's size in this
	// scope. The interior is shifted into the container after the parent
	// layout has positioned it.
	subRes := map[string]*Result{}
	subSize := map[string]Size{}
	subMin := map[string]Point{}
	for _, n := range p.Nodes {
		if n.Sub == nil {
			continue
		}
		sg := graph.Build(n.Sub)
		r, err := Compute(n.Sub, sg)
		if err != nil {
			return nil, fmt.Errorf("sub-process %s: %w", n.ID, err)
		}
		minX, minY, maxX, maxY := bounds(r)
		subRes[n.ID] = r
		subMin[n.ID] = Point{minX, minY}
		subSize[n.ID] = Size{
			W: (maxX - minX) + 2*SubProcPadX,
			H: (maxY - minY) + SubProcHeaderH + SubProcPadBottom,
		}
	}

	yOff := Margin
	for ci, comp := range g.Components {
		cl, err := layoutComponent(p, g, comp, subSize)
		if err != nil {
			return nil, fmt.Errorf("component %d: %w", ci, err)
		}
		// Shift the component into place and merge.
		minX, minY, maxY := math.Inf(1), math.Inf(1), math.Inf(-1)
		each(cl, func(r Rect) {
			minX = math.Min(minX, r.X)
			minY = math.Min(minY, r.Y)
			maxY = math.Max(maxY, r.Bottom())
		}, func(pt Point) {
			minX = math.Min(minX, pt.X)
			minY = math.Min(minY, pt.Y)
			maxY = math.Max(maxY, pt.Y)
		})
		dx, dy := Margin-minX, yOff-minY
		merge(res, cl, dx, dy)
		yOff += maxY - minY + ComponentGap
	}

	// Drop each expanded sub-process interior into its container rectangle.
	for id, r := range subRes {
		cont, ok := res.Shapes[id]
		if !ok {
			continue
		}
		dx := cont.X + SubProcPadX - subMin[id].X
		dy := cont.Y + SubProcHeaderH - subMin[id].Y
		merge(res, r, dx, dy)
	}
	return res, nil
}

// bounds returns the axis-aligned bounding box of everything in a result.
func bounds(r *Result) (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	each(r, func(rc Rect) {
		minX = math.Min(minX, rc.X)
		minY = math.Min(minY, rc.Y)
		maxX = math.Max(maxX, rc.Right())
		maxY = math.Max(maxY, rc.Bottom())
	}, func(pt Point) {
		minX = math.Min(minX, pt.X)
		minY = math.Min(minY, pt.Y)
		maxX = math.Max(maxX, pt.X)
		maxY = math.Max(maxY, pt.Y)
	})
	if math.IsInf(minX, 1) {
		return 0, 0, 0, 0
	}
	return minX, minY, maxX, maxY
}

func each(r *Result, fr func(Rect), fp func(Point)) {
	for _, x := range r.Shapes {
		fr(x)
	}
	for _, x := range r.Labels {
		fr(x)
	}
	for _, x := range r.EdgeLabels {
		fr(x)
	}
	for _, pts := range r.Edges {
		for _, pt := range pts {
			fp(pt)
		}
	}
}

func merge(dst, src *Result, dx, dy float64) {
	for k, v := range src.Shapes {
		dst.Shapes[k] = Rect{v.X + dx, v.Y + dy, v.W, v.H}
	}
	for k, v := range src.Labels {
		dst.Labels[k] = Rect{v.X + dx, v.Y + dy, v.W, v.H}
	}
	for k, v := range src.EdgeLabels {
		dst.EdgeLabels[k] = Rect{v.X + dx, v.Y + dy, v.W, v.H}
	}
	for k, pts := range src.Edges {
		moved := make([]Point, len(pts))
		for i, pt := range pts {
			moved[i] = Point{pt.X + dx, pt.Y + dy}
		}
		dst.Edges[k] = moved
	}
	for k := range src.Retrograde {
		dst.Retrograde[k] = true
	}
	for k, v := range src.Merged {
		dst.Merged[k] = v
	}
}

// nodeSize returns the shape size for a flow node.
func nodeSize(n *model.FlowNode) (w, h float64) {
	switch {
	case n.Kind.IsEvent():
		return EventS, EventS
	case n.Kind.IsGateway():
		return GatewayS, GatewayS
	default:
		return TaskW, TaskH
	}
}
