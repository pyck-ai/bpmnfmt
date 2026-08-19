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

	// subSize holds the precomputed size of expanded sub-process container
	// nodes (id -> box). Other nodes fall back to nodeSize.
	subSize map[string]Size

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

func layoutComponent(p *model.Process, g *graph.Graph, c *graph.Component, subSize map[string]Size) (*Result, error) {
	cl := &compLayout{
		p: p, g: g, c: c,
		subSize:  subSize,
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

// sizeOf returns the shape size of a node: the precomputed box for an
// expanded sub-process container, otherwise the kind-based default.
func (cl *compLayout) sizeOf(id string) (w, h float64) {
	if s, ok := cl.subSize[id]; ok {
		return s.W, s.H
	}
	return nodeSize(cl.node(id))
}

func (cl *compLayout) width(id string) float64 {
	w, _ := cl.sizeOf(id)
	return w
}

func (cl *compLayout) height(id string) float64 {
	_, h := cl.sizeOf(id)
	return h
}

// spacingWidth is the width used for horizontal gap constraints: events and
// gateways with external labels reserve room for them so neighboring labels
// and shapes cannot collide.
func (cl *compLayout) spacingWidth(id string) float64 {
	n := cl.node(id)
	w, _ := cl.sizeOf(id)
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

// isActivity reports whether a node is drawn as a rectangle: a task or an
// expanded sub-process, i.e. anything with a flat right border to leave from.
func (cl *compLayout) isActivity(id string) bool {
	n := cl.node(id)
	return n != nil && !n.Kind.IsEvent() && !n.Kind.IsGateway()
}

// rejoinGap reports the horizontal clearance a chain needs between its tail
// and its merge target, and whether the tail is de-aligned at all.
//
// Rule L1: a tail that is an ACTIVITY with exactly one forward outgoing flow
// leaves through its right border and turns up in the target's column, so it
// must stop at least one column short of the target. A gateway or event tail
// keeps the aligned straight vertical out of its corner or point, and a tail
// with more than one forward flow keeps the vertical out of its top — its
// right side is taken by the row continuation.
func (cl *compLayout) rejoinGap(ch *chain) (float64, bool) {
	tail := ch.nodes[len(ch.nodes)-1]
	gap := (cl.spacingWidth(tail)+cl.spacingWidth(ch.mergeNode))/2 + GapX
	if ch.isRoot {
		return gap, true // secondary-start inflow: same shape
	}
	if !cl.isActivity(tail) || len(cl.g.ForwardOut(tail)) != 1 {
		return 0, false
	}
	return gap, true
}

// pastGatewayCluster advances an alignment successor along its chain past a
// run of consecutive gateways, stopping at the first non-gateway node (or at
// the chain's end). A gateway carries no branch of its own to align to, so a
// whole cluster shares the column of the node that follows it.
func (cl *compLayout) pastGatewayCluster(succ string, chainIdx int) string {
	if succ == "" || chainIdx < 0 || chainIdx >= len(cl.chains) {
		return succ
	}
	nodes := cl.chains[chainIdx].nodes
	at := -1
	for i, id := range nodes {
		if id == succ {
			at = i
			break
		}
	}
	if at < 0 {
		return succ // not on the parent chain; leave the guard below to reject it
	}
	for at+1 < len(nodes) && cl.node(nodes[at]).Kind.IsGateway() {
		at++
	}
	return nodes[at]
}

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

	// Room-rule dependencies (filled below) also carry the branch-head
	// alignment edges, which are not sequence flows and so need explicit
	// topological dependencies.
	extraOut := map[string][]string{}
	extraDeps := map[string]int{}

	for _, ch := range cl.chains {
		for i, id := range ch.nodes {
			if i > 0 {
				prev := ch.nodes[i-1]
				cons[id] = append(cons[id], constr{prev, (cl.spacingWidth(prev)+cl.spacingWidth(id))/2 + GapX})
			}
		}
		head := ch.nodes[0]
		if ch.parentNode != "" {
			// Branch heads align with the column of the first NON-GATEWAY
			// node following the split on its parent chain, so every row's
			// first element starts in the same column and the entry edge
			// turns a single corner into the head's left side. Consecutive
			// gateways form one cluster and share that column: aligning to
			// a neighbouring gateway instead would stack the branch heads
			// of a cluster in as many columns as the cluster has gateways
			// (rule L4).
			succ := ""
			for _, fl := range cl.g.ForwardOut(ch.parentNode) {
				if fl.ID != ch.entryFlow {
					succ = fl.TargetRef
					break
				}
			}
			succ = cl.pastGatewayCluster(succ, ch.parent)
			// Aligning to a successor that sits at or past this branch's
			// rejoin point would close a cycle: the merge node is already
			// constrained to at-or-right-of the branch tail, which is
			// right of the head. Fall back to one column step right of the
			// split instead.
			usable := succ != "" && cl.chainOf[succ] == ch.parent
			if usable && ch.mergeNode != "" && cl.chainOf[ch.mergeNode] == ch.parent {
				pos := func(id string) int {
					for i, n := range cl.chains[ch.parent].nodes {
						if n == id {
							return i
						}
					}
					return -1
				}
				usable = pos(succ) >= 0 && pos(succ) < pos(ch.mergeNode)
			}
			if usable {
				cons[head] = append(cons[head], constr{succ, 0})
				extraOut[succ] = append(extraOut[succ], head)
				extraDeps[head]++
			} else {
				cons[head] = append(cons[head], constr{ch.parentNode,
					(cl.spacingWidth(ch.parentNode)+cl.spacingWidth(head))/2 + GapX})
			}
		}
		if ch.mergeNode != "" {
			last := ch.nodes[len(ch.nodes)-1]
			if gap, deAligned := cl.rejoinGap(ch); deAligned {
				// Rule L1: an activity tail leaves to the RIGHT and turns up
				// in the target's column, so it needs a column of clearance
				// to turn in. (Root chains have always fanned in this way.)
				cons[ch.mergeNode] = append(cons[ch.mergeNode], constr{last, gap})
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
// merge target ended up further right than the tail. A tail that rule L1
// de-aligns is shifted to its required gap instead, never under the target.
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
		target := cl.x[ch.mergeNode]
		if gap, deAligned := cl.rejoinGap(ch); deAligned {
			target -= gap
		}
		delta := target - cl.x[tail]
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
// The branches of one split node are placed longest-first, so the longest
// alternate sits directly next to its parent's row and the shortest ends up
// furthest away.
//
// Placement runs in a spine-relative index space: the spine starts at 0, a
// lifted chain and its subtree take negative indices, and the whole
// component is shifted back into non-negative indices at the end, so the
// spine lands on row 1+ whenever a branch was lifted.
func (cl *compLayout) assignRows() {
	rowIntervals := map[int][]span{}

	overlapRow := func(row int, iv span) bool {
		for _, o := range rowIntervals[row] {
			if iv.lo < o.hi && o.lo < iv.hi {
				return true
			}
		}
		return false
	}

	// place puts a chain on the first overlap-free row starting at from and
	// stepping by step (+1 = downward, -1 = upward).
	place := func(ch *chain, from, step int) {
		head, tail := ch.nodes[0], ch.nodes[len(ch.nodes)-1]
		lo := cl.x[head] - cl.width(head)/2 - ChainPad
		if ch.parentNode != "" {
			// The entry edge turns a corner at the split's column, so the
			// row is occupied from there rather than from the head.
			lo = math.Min(lo, cl.x[ch.parentNode]-ChainPad)
		}
		iv := span{lo, cl.x[tail] + cl.width(tail)/2 + ChainPad}
		row := from
		for overlapRow(row, iv) {
			row += step
		}
		rowIntervals[row] = append(rowIntervals[row], iv)
		ch.row = row
		for _, id := range ch.nodes {
			cl.rowOf[id] = row
		}
	}

	// dir records the direction each chain was placed in. A branch lifted
	// above the spine takes its whole subtree with it: descendants inherit
	// the direction, so they stack away from the spine instead of crossing
	// back over it.
	dir := map[int]int{}

	// Placement order: parents before children, the branches of one split
	// node longest-first, and — across split nodes on one row — the
	// rightmost split first. A split's entry trunk drops in its own column
	// and every branch head of a cluster shares one column (rule L4), so a
	// trunk starting further left has to cross every row between its split
	// and its own; taking the shallowest rows for the rightmost splits keeps
	// each trunk above the entries it would otherwise cut through.
	children := map[int][]int{}
	order := make([]int, 0, len(cl.chains))
	queue := make([]int, 0, len(cl.chains))
	for _, ch := range cl.chains {
		if ch.parent == -1 {
			queue = append(queue, ch.idx)
			continue
		}
		children[ch.parent] = append(children[ch.parent], ch.idx)
	}
	for len(queue) > 0 {
		idx := queue[0]
		queue = queue[1:]
		order = append(order, idx)

		kids := append([]int(nil), children[idx]...)
		sort.SliceStable(kids, func(i, j int) bool {
			a, b := cl.chains[kids[i]], cl.chains[kids[j]]
			if a.parentNode != b.parentNode {
				// Both split nodes sit on this same parent chain, so they
				// share a row; the rightmost one takes the shallower tier.
				if a.parentNode == "" || b.parentNode == "" {
					return false
				}
				return cl.x[a.parentNode] > cl.x[b.parentNode]
			}
			if a.weight != b.weight {
				return a.weight > b.weight
			}
			return len(a.nodes) > len(b.nodes)
		})
		queue = append(queue, kids...)
	}

	for _, idx := range order {
		ch := cl.chains[idx]
		if ch.parent == -1 {
			dir[ch.idx] = 1
			place(ch, 0, 1)
			continue
		}
		if ch.lifted || dir[ch.parent] == -1 {
			dir[ch.idx] = -1
			place(ch, cl.chains[ch.parent].row-1, -1)
			continue
		}
		dir[ch.idx] = 1
		place(ch, cl.chains[ch.parent].row+1, 1)
	}

	// Shift the spine-relative rows into non-negative indices.
	shift := 0
	for _, ch := range cl.chains {
		if ch.row < shift {
			shift = ch.row
		}
	}
	if shift < 0 {
		for _, ch := range cl.chains {
			ch.row -= shift
			for _, id := range ch.nodes {
				cl.rowOf[id] = ch.row
			}
		}
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
