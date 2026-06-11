package layout

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/model"
	"github.com/pyck-ai/bpmnfmt/internal/textmetrics"
)

// compLayout carries the state of one component's layout computation.
// Coordinates are component-local; Compute shifts the merged result.
type compLayout struct {
	p *model.Process
	g *graph.Graph
	c *graph.Component

	chains  []*chain
	chainOf map[string]int

	x        map[string]float64 // node center x
	rowOf    map[string]int
	rows     [][]string // per row: node IDs sorted by center x
	rowSpans [][]span   // per row: solid chain extents (nodes + their connecting edges)

	// Routing state (see route.go).
	plans     []*edgePlan
	planByID  map[string]*edgePlan
	gapLanes  [][]*laneSeg // per gap index (gap g sits above row g; last entry = below the last row)
	corridors []corridor
	sides     map[sideKey][]*docking
	marginUse int

	// Vertical geometry, filled by materializeY.
	rowCY    []float64
	gapTop   []float64 // top y of each gap's lane zone
	annBandH []float64 // height of the annotation band above each row
	labelH   []float64 // height of the label zone below each row
	proseH   float64   // height of the prose-annotation zone above everything

	anns []*annInfo // annotations of this component (cached)

	res *Result
}

func layoutComponent(p *model.Process, g *graph.Graph, c *graph.Component) (*Result, error) {
	cl := &compLayout{
		p: p, g: g, c: c,
		x:        map[string]float64{},
		rowOf:    map[string]int{},
		planByID: map[string]*edgePlan{},
		sides:    map[sideKey][]*docking{},
		res: &Result{
			Shapes:     map[string]Rect{},
			Labels:     map[string]Rect{},
			EdgeLabels: map[string]Rect{},
			Edges:      map[string][]Point{},
		},
	}
	var err error
	cl.chains, cl.chainOf, err = decompose(g, c)
	if err != nil {
		return nil, err
	}
	if err := cl.assignX(); err != nil {
		return nil, err
	}
	cl.alignChains()
	cl.assignRows()
	if err := cl.planRoutes(); err != nil {
		return nil, err
	}
	cl.prepareBands()
	cl.materializeY()
	cl.materializeEdges()
	cl.placeLabels()
	cl.placeAnnotations()
	return cl.res, nil
}

func (cl *compLayout) node(id string) *model.FlowNode { return cl.p.NodeByID[id] }

func (cl *compLayout) width(id string) float64 {
	w, _ := nodeSize(cl.node(id))
	return w
}

// spacingWidth is the width used for horizontal gap constraints: events and
// gateways with external labels reserve room for them so neighboring labels
// and shapes cannot collide.
func (cl *compLayout) spacingWidth(id string) float64 {
	n := cl.node(id)
	w, _ := nodeSize(n)
	if (n.Kind.IsEvent() || n.Kind.IsGateway()) && strings.TrimSpace(n.Name) != "" {
		lw, _ := textmetrics.Box(n.Name, ExtLabelWrap)
		if lw+10 > w {
			return lw + 10
		}
	}
	return w
}

// shape returns the node rect; only valid after materializeY.
func (cl *compLayout) shape(id string) Rect { return cl.res.Shapes[id] }

// assignX propagates x constraints in topological order over forward flows.
// All constraints have the form x(later) >= x(earlier) + delta, so a single
// pass suffices.
func (cl *compLayout) assignX() error {
	// Constraint lists per node.
	type constr struct {
		from  string
		delta float64
	}
	cons := map[string][]constr{}

	for _, ch := range cl.chains {
		for i, id := range ch.nodes {
			if i > 0 {
				prev := ch.nodes[i-1]
				cons[id] = append(cons[id], constr{prev, (cl.spacingWidth(prev)+cl.spacingWidth(id))/2 + GapX})
			}
		}
		head := ch.nodes[0]
		if ch.parentNode != "" {
			// Branch heads align directly below their split node.
			cons[head] = append(cons[head], constr{ch.parentNode, 0})
		}
		if ch.mergeNode != "" {
			last := ch.nodes[len(ch.nodes)-1]
			if ch.isRoot {
				// Fan-in from the left: stay left of the merge target.
				cons[ch.mergeNode] = append(cons[ch.mergeNode],
					constr{last, (cl.spacingWidth(last)+cl.spacingWidth(ch.mergeNode))/2 + GapX})
			} else {
				// Vertical rejoin: merge target at or right of the chain tail.
				cons[ch.mergeNode] = append(cons[ch.mergeNode], constr{last, 0})
			}
		}
	}
	// Cross flows keep left-to-right monotonicity.
	for _, fl := range cl.componentFlows() {
		if classify(cl.g, cl.chains, cl.chainOf, fl) == fcCross {
			cons[fl.TargetRef] = append(cons[fl.TargetRef], constr{fl.SourceRef, 0})
		}
	}

	// Room rule: a chain that merges past its parent chain (into a
	// grandparent or the spine) must extend beyond every ancestor chain it
	// skips over, so its rejoin can rise straight up instead of weaving
	// around the ancestor's nodes. These constraints are not sequence
	// flows, so they need explicit topological dependencies.
	extraOut := map[string][]string{}
	extraDeps := map[string]int{}
	for _, ch := range cl.chains {
		if ch.mergeNode == "" || ch.isRoot || ch.parent < 0 {
			continue
		}
		target := cl.chainOf[ch.mergeNode]
		var between []int
		onPath := false
		for a := ch.parent; a >= 0; a = cl.chains[a].parent {
			if a == target {
				onPath = true
				break
			}
			between = append(between, a)
		}
		if !onPath {
			continue
		}
		tail := ch.nodes[len(ch.nodes)-1]
		for _, a := range between {
			aTail := cl.chains[a].nodes[len(cl.chains[a].nodes)-1]
			cons[tail] = append(cons[tail], constr{aTail, (cl.spacingWidth(aTail)+cl.spacingWidth(tail))/2 + GapX})
			extraOut[aTail] = append(extraOut[aTail], tail)
			extraDeps[tail]++
		}
	}

	// Kahn topological order over forward flows, document order tie-break.
	// Each node's center snaps up onto the global column grid so elements
	// in different rows align vertically.
	indeg := map[string]int{}
	for _, n := range cl.c.Nodes {
		indeg[n.ID] = len(cl.g.ForwardIn(n.ID)) + extraDeps[n.ID]
	}
	done := map[string]bool{}
	for processed := 0; processed < len(cl.c.Nodes); {
		advanced := false
		for _, n := range cl.c.Nodes { // document order
			if done[n.ID] || indeg[n.ID] > 0 {
				continue
			}
			x := cl.width(n.ID) / 2
			for _, co := range cons[n.ID] {
				if v, ok := cl.x[co.from]; ok && v+co.delta > x {
					x = v + co.delta
				}
			}
			cl.x[n.ID] = snapToColumn(x)
			done[n.ID] = true
			processed++
			advanced = true
			for _, fl := range cl.g.ForwardOut(n.ID) {
				indeg[fl.TargetRef]--
			}
			for _, t := range extraOut[n.ID] {
				indeg[t]--
			}
		}
		if !advanced {
			return fmt.Errorf("forward flows contain a cycle (back-edge detection failed)")
		}
	}
	return nil
}

// snapToColumn rounds a center x up to the next column center. Column k has
// its center at ColPitch/2 + k*ColPitch.
func snapToColumn(x float64) float64 {
	first := ColPitch / 2
	k := math.Ceil((x - first - 1e-6) / ColPitch)
	if k < 0 {
		k = 0
	}
	return first + k*ColPitch
}

func (cl *compLayout) componentFlows() []*model.SequenceFlow {
	var out []*model.SequenceFlow
	for _, fl := range cl.p.Flows {
		if cl.c.NodeSet[fl.SourceRef] && cl.c.NodeSet[fl.TargetRef] {
			out = append(out, fl)
		}
	}
	return out
}

// alignChains shifts whole branch subtrees right so that chain tails sit
// exactly under their merge targets (straight vertical rejoins) whenever the
// merge target ended up further right than the tail.
func (cl *compLayout) alignChains() {
	children := map[int][]int{}
	for _, ch := range cl.chains {
		if ch.parent >= 0 {
			children[ch.parent] = append(children[ch.parent], ch.idx)
		}
	}
	var subtree func(int) []string
	subtree = func(idx int) []string {
		out := append([]string{}, cl.chains[idx].nodes...)
		for _, c := range children[idx] {
			out = append(out, subtree(c)...)
		}
		return out
	}

	// Children were created after their parents; reverse order shifts the
	// deepest chains first so parent shifts keep them aligned.
	for i := len(cl.chains) - 1; i >= 1; i-- {
		ch := cl.chains[i]
		if ch.isRoot || ch.mergeNode == "" {
			continue
		}
		tail := ch.nodes[len(ch.nodes)-1]
		delta := cl.x[ch.mergeNode] - cl.x[tail]
		if delta <= 0.5 {
			continue
		}
		nodes := subtree(i)
		inSubtree := map[string]bool{}
		for _, id := range nodes {
			inSubtree[id] = true
		}
		// The shift must not push any forward edge leaving the subtree past
		// its target.
		ok := true
		for _, id := range nodes {
			for _, fl := range cl.g.ForwardOut(id) {
				if !inSubtree[fl.TargetRef] && cl.x[fl.TargetRef] < cl.x[id]+delta-0.5 {
					ok = false
					break
				}
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		for _, id := range nodes {
			cl.x[id] += delta
		}
	}
}

// span is a solid horizontal extent within a row.
type span struct{ lo, hi float64 }

// assignRows places each chain in the highest tier that is strictly below
// its parent chain and free of horizontal overlap (rule R4: tier sharing).
// Chains that end in a loop back to an earlier row are placed below every
// row their loop lane would sweep across, keeping the lane crossing-free.
func (cl *compLayout) assignRows() {
	var rowIntervals [][]span

	overlapRow := func(row int, iv span) bool {
		for _, o := range rowIntervals[row] {
			if iv.lo < o.hi && o.lo < iv.hi {
				return true
			}
		}
		return false
	}

	place := func(ch *chain, minRow int) {
		head, tail := ch.nodes[0], ch.nodes[len(ch.nodes)-1]
		iv := span{
			cl.x[head] - cl.width(head)/2 - ChainPad,
			cl.x[tail] + cl.width(tail)/2 + ChainPad,
		}
		row := minRow
		for {
			for len(rowIntervals) <= row {
				rowIntervals = append(rowIntervals, nil)
			}
			if !overlapRow(row, iv) {
				break
			}
			row++
		}
		rowIntervals[row] = append(rowIntervals[row], iv)
		ch.row = row
		for _, id := range ch.nodes {
			cl.rowOf[id] = row
		}
	}

	for _, ch := range cl.chains {
		if ch.parent == -1 {
			place(ch, 0)
			continue
		}
		minRow := cl.chains[ch.parent].row + 1

		// Loop sweep: a back edge leaving this chain will run a lane under
		// the chain's row from the loop source to the loop target. Demote
		// the chain below every already-placed row that overlaps the sweep.
		for _, id := range ch.nodes {
			for _, fl := range cl.g.Out[id] {
				if !cl.g.Back[fl.ID] || cl.chainOf[fl.TargetRef] == ch.idx {
					continue
				}
				sweep := span{
					math.Min(cl.x[fl.TargetRef], cl.x[id]) - ChainPad,
					math.Max(cl.x[fl.TargetRef], cl.x[id]) + ChainPad,
				}
				for r := minRow; r < len(rowIntervals); r++ {
					if overlapRow(r, sweep) && r+1 > minRow {
						minRow = r + 1
					}
				}
			}
		}
		place(ch, minRow)
	}

	nRows := 0
	for _, ch := range cl.chains {
		if ch.row+1 > nRows {
			nRows = ch.row + 1
		}
	}
	cl.rows = make([][]string, nRows)
	cl.rowSpans = make([][]span, nRows)
	for _, n := range cl.c.Nodes {
		r := cl.rowOf[n.ID]
		cl.rows[r] = append(cl.rows[r], n.ID)
	}
	for _, row := range cl.rows {
		sort.SliceStable(row, func(i, j int) bool { return cl.x[row[i]] < cl.x[row[j]] })
	}
	// Solid extents: chain nodes plus the edges connecting them.
	for _, ch := range cl.chains {
		head, tail := ch.nodes[0], ch.nodes[len(ch.nodes)-1]
		cl.rowSpans[ch.row] = append(cl.rowSpans[ch.row], span{
			cl.x[head] - cl.width(head)/2,
			cl.x[tail] + cl.width(tail)/2,
		})
	}
}

// rowClear reports whether the vertical strip [x-Clearance, x+Clearance]
// crosses neither a node nor a chain extent (nodes + connecting edges) of
// the given rows.
func (cl *compLayout) rowClear(x float64, rows ...int) bool {
	for _, r := range rows {
		if r < 0 || r >= len(cl.rows) {
			continue
		}
		for _, sp := range cl.rowSpans[r] {
			if x > sp.lo-Clearance && x < sp.hi+Clearance {
				return false
			}
		}
	}
	return true
}
