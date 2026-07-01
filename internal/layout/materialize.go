package layout

import (
	"math"
	"strings"

	"github.com/pyck-ai/bpmnfmt/internal/textmetrics"
)

// annInfo is a text annotation prepared for placement.
type annInfo struct {
	id     string
	prose  bool
	w, h   float64
	anchor string // anchoring node ID ("" = unanchored, parked in prose zone)
	flowID string // set when anchored to a sequence flow
}

// prepareBands computes label-zone, annotation-band and prose-zone heights;
// they feed the vertical gap sizing.
func (cl *compLayout) prepareBands() {
	cl.labelH = make([]float64, len(cl.rows))
	for r, row := range cl.rows {
		max := 0.0
		for _, id := range row {
			n := cl.node(id)
			if (n.Kind.IsEvent() || n.Kind.IsGateway()) && strings.TrimSpace(n.Name) != "" {
				_, h := textmetrics.Box(n.Name, ExtLabelWrap)
				if h+LabelGap > max {
					max = h + LabelGap
				}
			}
		}
		cl.labelH[r] = max
	}

	cl.annBandH = make([]float64, len(cl.rows))
	cl.proseH = 0
	for _, ann := range cl.componentAnnotations() {
		if ann.prose || ann.anchor == "" {
			if ann.h+ProseGap > cl.proseH {
				cl.proseH = ann.h + ProseGap
			}
			continue
		}
		r := cl.rowOf[ann.anchor]
		if ann.h+AnnGap > cl.annBandH[r] {
			cl.annBandH[r] = ann.h + AnnGap
		}
	}
}

// componentAnnotations resolves annotations anchored to nodes (directly or
// via flows) of this component and computes their box sizes.
func (cl *compLayout) componentAnnotations() []*annInfo {
	if cl.anns != nil {
		return cl.anns
	}
	for _, ann := range cl.p.Annotations {
		anchor, flowID := "", ""
		for _, as := range cl.p.Associations {
			other := ""
			switch {
			case as.SourceRef == ann.ID:
				other = as.TargetRef
			case as.TargetRef == ann.ID:
				other = as.SourceRef
			default:
				continue
			}
			if fl := cl.p.FlowByID[other]; fl != nil {
				anchor, flowID = fl.SourceRef, fl.ID
			} else if cl.p.NodeByID[other] != nil {
				anchor = other
			}
			break
		}
		if anchor == "" {
			// Unanchored annotations belong to the first component only.
			if cl.c != cl.g.Components[0] {
				continue
			}
		} else if !cl.c.NodeSet[anchor] {
			continue
		}

		text := strings.TrimSpace(ann.Text)
		lines := textmetrics.Wrap(text, ShortAnnWrap)
		prose := len(lines) > 2 || strings.Contains(text, "\n")
		var w, h float64
		if prose {
			w, h = textmetrics.Box(text, ProseAnnWrap)
			w, h = w+18, h+12
		} else {
			w, h = textmetrics.Box(text, ShortAnnWrap)
			w, h = w+18, h+10
		}
		cl.anns = append(cl.anns, &annInfo{
			id: ann.ID, prose: prose, w: w, h: h, anchor: anchor, flowID: flowID,
		})
	}
	return cl.anns
}

func (cl *compLayout) laneZoneH(g int) float64 {
	n := cl.laneCount(g)
	if n == 0 {
		return 0
	}
	return 2*LanePad + float64(n-1)*LaneStep
}

// materializeY assigns row center lines, gap lane positions and node shapes.
func (cl *compLayout) materializeY() {
	nRows := len(cl.rows)
	cl.rowCY = make([]float64, nRows)
	cl.gapTop = make([]float64, nRows+1)

	// Row height is the tallest shape on the row (>= RowH). Rows made of
	// ordinary events/gateways/tasks are exactly RowH; an expanded
	// sub-process container makes its row taller and pushes lower rows down.
	rowH := make([]float64, nRows)
	for r := 0; r < nRows; r++ {
		h := RowH
		for _, id := range cl.rows[r] {
			if hh := cl.height(id); hh > h {
				h = hh
			}
		}
		rowH[r] = h
	}

	y := cl.proseH
	for r := 0; r < nRows; r++ {
		labelAbove := 0.0
		if r > 0 {
			labelAbove = cl.labelH[r-1]
		}
		zone := cl.laneZoneH(r)
		top := y + labelAbove + 4 + zone + cl.annBandH[r] + 4
		if top-y < MinGapH {
			top = y + MinGapH
		}
		cl.gapTop[r] = top - 4 - cl.annBandH[r] - zone
		cl.rowCY[r] = top + rowH[r]/2
		y = top + rowH[r]
	}
	last := 0.0
	if nRows > 0 {
		last = cl.labelH[nRows-1]
	}
	cl.gapTop[nRows] = y + last + 4

	for _, n := range cl.c.Nodes {
		w, h := cl.sizeOf(n.ID)
		cl.res.Shapes[n.ID] = Rect{
			X: cl.x[n.ID] - w/2,
			Y: cl.rowCY[cl.rowOf[n.ID]] - h/2,
			W: w,
			H: h,
		}
	}
}

// laneY returns the y of a lane in a gap. Gap 0 stacks long spans on top
// (loops over the spine); deeper gaps stack long spans toward the bottom
// (loops under a row) — both orders avoid crossings between nested loops.
func (cl *compLayout) laneY(g, lane int) float64 {
	n := cl.laneCount(g)
	if g == 0 {
		return cl.gapTop[g] + LanePad + float64(lane)*LaneStep
	}
	return cl.gapTop[g] + LanePad + float64(n-1-lane)*LaneStep
}

// minShapeX is the leftmost shape edge (for the margin corridor).
func (cl *compLayout) minShapeX() float64 {
	min := math.Inf(1)
	for _, r := range cl.res.Shapes {
		min = math.Min(min, r.X)
	}
	if math.IsInf(min, 1) {
		return 0
	}
	return min
}

// materializeEdges turns plans into waypoints.
func (cl *compLayout) materializeEdges() {
	marginBase := cl.minShapeX() - 45
	for _, pl := range cl.plans {
		src, dst := cl.shape(pl.src), cl.shape(pl.dst)
		var pts []Point
		exitX := func() float64 { return src.CX() + pl.exit.off }
		entryX := func() float64 { return dst.CX() + pl.entry.off }

		switch pl.kind {
		case pkH:
			pts = []Point{{src.Right(), src.CY()}, {dst.X, dst.CY()}}

		case pkVDown:
			pts = []Point{{exitX(), src.Bottom()}, {exitX(), dst.Y}}

		case pkVUp:
			pts = []Point{{exitX(), src.Y}, {exitX(), dst.Bottom()}}

		case pkDownLeftIn:
			x := src.CX() + pl.exit.off
			pts = []Point{{x, src.Bottom()}, {x, dst.CY()}, {dst.X, dst.CY()}}

		case pkDownJog:
			ly := cl.laneY(pl.g1, pl.seg1.lane)
			pts = []Point{
				{exitX(), src.Bottom()}, {exitX(), ly},
				{pl.corrX, ly}, {pl.corrX, dst.CY()}, {dst.X, dst.CY()},
			}

		case pkDownTop:
			ly1 := cl.laneY(pl.g1, pl.seg1.lane)
			ly2 := cl.laneY(pl.g2, pl.seg2.lane)
			pts = []Point{
				{exitX(), src.Bottom()}, {exitX(), ly1}, {pl.corrX, ly1},
				{pl.corrX, ly2}, {entryX(), ly2}, {entryX(), dst.Y},
			}

		case pkRightUp:
			pts = []Point{
				{src.Right(), src.CY()}, {pl.corrX, src.CY()}, {pl.corrX, dst.Bottom()},
			}

		case pkUpBottom:
			ly2 := cl.laneY(pl.g2, pl.seg2.lane)
			if pl.seg1 != nil {
				ly1 := cl.laneY(pl.g1, pl.seg1.lane)
				pts = []Point{
					{exitX(), src.Y}, {exitX(), ly1}, {pl.corrX, ly1},
					{pl.corrX, ly2}, {entryX(), ly2}, {entryX(), dst.Bottom()},
				}
			} else {
				pts = []Point{
					{exitX(), src.Y}, {exitX(), ly2}, {entryX(), ly2}, {entryX(), dst.Bottom()},
				}
			}

		case pkLoopTop:
			ly2 := cl.laneY(pl.g2, pl.seg2.lane)
			if pl.seg1 != nil {
				ly1 := cl.laneY(pl.g1, pl.seg1.lane)
				pts = []Point{
					{exitX(), src.Y}, {exitX(), ly1}, {pl.corrX, ly1},
					{pl.corrX, ly2}, {entryX(), ly2}, {entryX(), dst.Y},
				}
			} else {
				pts = []Point{
					{exitX(), src.Y}, {exitX(), ly2}, {entryX(), ly2}, {entryX(), dst.Y},
				}
			}

		case pkRootMerge:
			pts = []Point{
				{src.Right(), src.CY()}, {pl.corrX, src.CY()},
				{pl.corrX, dst.CY()}, {dst.X, dst.CY()},
			}

		case pkOverRow:
			ly := cl.laneY(pl.g1, pl.seg1.lane)
			pts = []Point{
				{exitX(), src.Y}, {exitX(), ly}, {entryX(), ly}, {entryX(), dst.Y},
			}

		case pkBackBottom:
			ly := cl.laneY(pl.g1, pl.seg1.lane)
			pts = []Point{
				{exitX(), src.Bottom()}, {exitX(), ly},
				{pl.corrX, ly}, {pl.corrX, dst.Bottom()},
			}

		case pkBackMargin:
			mx := marginBase - float64(pl.marginIdx)*15
			ly1 := cl.laneY(pl.g1, pl.seg1.lane)
			ly2 := cl.laneY(pl.g2, pl.seg2.lane)
			start := Point{exitX(), src.Bottom()}
			if cl.rowOf[pl.src] < cl.rowOf[pl.dst] {
				start = Point{exitX(), src.Y}
			}
			pts = []Point{
				start, {exitX(), ly1}, {mx, ly1},
				{mx, ly2}, {entryX(), ly2}, {entryX(), dst.Y},
			}
		}
		cl.res.Edges[pl.id] = cleanPath(pts)
	}
}

// cleanPath removes zero-length segments and collinear midpoints.
func cleanPath(pts []Point) []Point {
	if len(pts) < 2 {
		return pts
	}
	out := []Point{pts[0]}
	for _, p := range pts[1:] {
		last := out[len(out)-1]
		if math.Abs(p.X-last.X) < 0.5 && math.Abs(p.Y-last.Y) < 0.5 {
			continue
		}
		out = append(out, p)
	}
	// Merge collinear runs.
	merged := []Point{out[0]}
	for i := 1; i < len(out); i++ {
		if i+1 < len(out) {
			a, b, c := merged[len(merged)-1], out[i], out[i+1]
			if (sameX(a, b) && sameX(b, c)) || (sameY(a, b) && sameY(b, c)) {
				continue
			}
		}
		merged = append(merged, out[i])
	}
	return merged
}

func sameX(a, b Point) bool { return math.Abs(a.X-b.X) < 0.5 }
func sameY(a, b Point) bool { return math.Abs(a.Y-b.Y) < 0.5 }
