package layout

import (
	"fmt"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// chain is a maximal horizontal run of nodes: the spine, a gateway branch, or
// a secondary-start inflow. Chains are the unit of tier assignment.
type chain struct {
	idx        int
	nodes      []string
	parent     int    // chain index this chain branches from; -1 for the spine
	parentNode string // node the entry flow leaves ("" for spine and root chains)
	entryFlow  string // flow parentNode -> nodes[0] ("" for spine and root chains)
	mergeNode  string // forward merge target ("" when the chain dead-ends)
	exitFlow   string // flow nodes[last] -> mergeNode
	depth      int    // 0 = spine
	isRoot     bool   // secondary start inflow (fans into mergeNode's left side)
	row        int    // assigned tier (row index); the spine's row is 0 unless above-branches push it down
	weight     int    // node count of this chain plus its whole subtree
	lifted     bool   // routed above its parent instead of below
	retro      bool   // laid out right to left; the chain IS the way-back line
	// loopReturn marks a branch that exists only to return: every path
	// through its subtree ends in a back edge to a spine node earlier than
	// the split (rule N3). It is lifted above the spine — immediately, or
	// on applyRetro's revert when it is also a retro candidate.
	loopReturn bool
	// skyRetro marks a lifted loop-return whose body was laid out right to
	// left (rule N3b): the head sits one column left of its split, the
	// body reads leftward, and the return leaves the tail's left face.
	skyRetro bool
}

// decompose splits a component into chains. Every node lands in exactly one
// chain; processing order is deterministic (spine first, then splits in
// walk/declaration order, then unassigned roots in document order).
func decompose(g *graph.Graph, c *graph.Component) ([]*chain, map[string]int, error) {
	chainOf := map[string]int{}
	var chains []*chain

	add := func(ch *chain) *chain {
		ch.idx = len(chains)
		chains = append(chains, ch)
		for _, id := range ch.nodes {
			chainOf[id] = ch.idx
		}
		return ch
	}

	// reachesClaimed reports whether the subtree hanging off an unclaimed
	// successor carries a forward flow back into already-chained territory.
	// The search stops at claimed nodes: it asks whether this branch links
	// up with what is already laid out, not how far it can travel.
	reachesClaimed := func(from string) bool {
		seen := map[string]bool{from: true}
		stack := []string{from}
		for len(stack) > 0 {
			cur := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			for _, fl := range g.ForwardOut(cur) {
				if _, taken := chainOf[fl.TargetRef]; taken {
					return true
				}
				if !seen[fl.TargetRef] {
					seen[fl.TargetRef] = true
					stack = append(stack, fl.TargetRef)
				}
			}
		}
		return false
	}

	// walk extends a chain from a start node until it merges or dead-ends.
	// At a split it continues into the successor whose subtree links back
	// into already-chained territory (rule L7), falling back to the
	// first-declared one; `out` is scanned in declared order, so ties
	// resolve first-declared. Forward flows form a DAG, so the walk
	// terminates.
	walk := func(from string) (nodes []string, mergeNode, exitFlow string) {
		cur := from
		for {
			nodes = append(nodes, cur)
			chainOf[cur] = -2 // claimed; replaced with the real index by add()
			out := g.ForwardOut(cur)
			if len(out) == 0 {
				return nodes, "", ""
			}
			next := out[0]
			if _, taken := chainOf[next.TargetRef]; taken {
				return nodes, next.TargetRef, next.ID
			}
			// Prefer the cross-linked successor: continuing into the branch
			// that rejoins keeps the chain on the row its rejoin needs,
			// instead of stranding it a tier away and routing the cross
			// link around the diagram.
			for _, fl := range out {
				if _, taken := chainOf[fl.TargetRef]; taken {
					continue
				}
				if reachesClaimed(fl.TargetRef) {
					next = fl
					break
				}
			}
			cur = next.TargetRef
		}
	}

	// Spine.
	spine := &chain{nodes: c.Spine, parent: -1, depth: 0}
	add(spine)

	// Breadth-first over chains: every node's non-chain forward flows spawn
	// sub-chains in declaration order.
	for qi := 0; qi < len(chains); qi++ {
		ch := chains[qi]
		for _, nid := range ch.nodes {
			for _, fl := range g.ForwardOut(nid) {
				if _, ok := chainOf[fl.TargetRef]; ok {
					continue // internal continuation, merge target, or already chained
				}
				nodes, mergeNode, exitFlow := walk(fl.TargetRef)
				add(&chain{
					nodes:      nodes,
					parent:     ch.idx,
					parentNode: nid,
					entryFlow:  fl.ID,
					mergeNode:  mergeNode,
					exitFlow:   exitFlow,
					depth:      ch.depth + 1,
				})
			}
		}
	}

	// Unassigned roots: secondary start events (and, with --force, floaters).
	for _, n := range c.Nodes {
		if _, ok := chainOf[n.ID]; ok {
			continue
		}
		if len(g.ForwardIn(n.ID)) > 0 {
			continue // reachable; will be picked up via its source's chain pass below
		}
		nodes, mergeNode, exitFlow := walk(n.ID)
		add(&chain{
			nodes:     nodes,
			parent:    0,
			mergeNode: mergeNode,
			exitFlow:  exitFlow,
			depth:     1,
			isRoot:    true,
		})
	}
	// Defensive sweep: anything still unassigned becomes its own chain.
	for _, n := range c.Nodes {
		if _, ok := chainOf[n.ID]; ok {
			continue
		}
		nodes, mergeNode, exitFlow := walk(n.ID)
		add(&chain{nodes: nodes, parent: 0, mergeNode: mergeNode, exitFlow: exitFlow, depth: 1})
	}

	for _, n := range c.Nodes {
		if i, ok := chainOf[n.ID]; !ok || i < 0 {
			return nil, nil, fmt.Errorf("node %s not assigned to a chain", n.ID)
		}
	}
	computeWeights(chains)
	markLifted(g, c, chains)
	markLoopExits(g, c, chains)
	markRetro(g, chains, chainOf)
	markLoopReturns(g, c, chains)
	return chains, chainOf, nil
}

// backEdgeOut returns the single back edge leaving a node, when the node has
// no forward continuation at all and exactly one loop edge.
func backEdgeOut(g *graph.Graph, id string) *model.SequenceFlow {
	if len(g.ForwardOut(id)) > 0 {
		return nil
	}
	var found *model.SequenceFlow
	for _, fl := range g.Out[id] {
		if !g.Back[fl.ID] {
			return nil
		}
		if found != nil {
			return nil // more than one loop out
		}
		found = fl
	}
	return found
}

// markRetro flags branches that are nothing but a loop back: a chain hanging
// off a split whose only exit is a back edge to a node upstream on the same
// chain, with no rejoin and no sub-branches. Such a branch reads best laid
// out RIGHT TO LEFT — the body itself becomes the way-back line, instead of
// running forward and then doubling back under the row.
//
// This marks CANDIDATES only. Whether the branch actually fits between the
// split's column and the loop target's column cannot be known until columns
// exist, so applyRetro makes the final call.
func markRetro(g *graph.Graph, chains []*chain, chainOf map[string]int) {
	for _, ch := range chains {
		if ch.parent < 0 || ch.isRoot || ch.parentNode == "" || ch.lifted {
			continue
		}
		if ch.mergeNode != "" || ch.weight != len(ch.nodes) {
			continue // rejoins, or has sub-branches of its own
		}
		fl := backEdgeOut(g, ch.nodes[len(ch.nodes)-1])
		if fl == nil {
			continue
		}
		// Nothing but the entry flow may reach into the branch. Marching it
		// left drags its nodes behind their own column, so any other inbound
		// forward flow would end up running backwards to reach them.
		shared := false
		for _, id := range ch.nodes {
			for _, in := range g.ForwardIn(id) {
				if in.ID != ch.entryFlow && chainOf[in.SourceRef] != ch.idx {
					shared = true
				}
			}
		}
		if shared {
			continue
		}
		// The loop target must sit on the parent chain, upstream of the
		// split: that is what makes the branch retrograde rather than a
		// jump into unrelated territory.
		parent := chains[ch.parent]
		if chainOf[fl.TargetRef] != ch.parent {
			continue
		}
		pos := func(id string) int {
			for i, n := range parent.nodes {
				if n == id {
					return i
				}
			}
			return -1
		}
		t, s := pos(fl.TargetRef), pos(ch.parentNode)
		if t < 0 || s < 0 || t >= s {
			continue
		}
		ch.retro = true
	}
}

// childrenOf groups chain indices by their parent, preserving chain order
// (which is creation order, i.e. declaration order per split).
func childrenOf(chains []*chain) map[int][]int {
	kids := map[int][]int{}
	for _, ch := range chains {
		if ch.parent >= 0 {
			kids[ch.parent] = append(kids[ch.parent], ch.idx)
		}
	}
	return kids
}

// computeWeights fills chain.weight: the chain's own node count plus the
// weight of every descendant. Children are always created after their
// parent, so a reverse pass over the slice is a valid post-order.
func computeWeights(chains []*chain) {
	for _, ch := range chains {
		ch.weight = len(ch.nodes)
	}
	for i := len(chains) - 1; i >= 1; i-- {
		ch := chains[i]
		if ch.parent >= 0 {
			chains[ch.parent].weight += ch.weight
		}
	}
}

// subtreeHasBackSource reports whether any node in the chain's subtree is
// the source of a back edge. Such branches are never lifted: rule e keeps
// loop lanes below the source's row, which a lifted branch would violate.
// The one exception is a pure loop-return branch (rule N3, markLoopReturns),
// whose back edges return through the sky instead.
func subtreeHasBackSource(g *graph.Graph, chains []*chain, kids map[int][]int, idx int) bool {
	stack := []int{idx}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, id := range chains[i].nodes {
			for _, fl := range g.Out[id] {
				if g.Back[fl.ID] {
					return true
				}
			}
		}
		stack = append(stack, kids[i]...)
	}
	return false
}

// subtreeNodes collects every node of a chain and its descendants.
func subtreeNodes(chains []*chain, kids map[int][]int, idx int) map[string]bool {
	out := map[string]bool{}
	stack := []int{idx}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, id := range chains[i].nodes {
			out[id] = true
		}
		stack = append(stack, kids[i]...)
	}
	return out
}

// isTerminalSubtree reports whether a branch ends where it is: no chain in
// it rejoins anything, no forward flow leaves it, and nothing in it sources
// a back edge. Only such a branch may be routed above its split — a branch
// that re-merges downstream would have to come back down across the spine,
// and a loop source needs the lane below its row (rule e; a pure
// loop-return branch is the exception and lifts via markLoopReturns).
func isTerminalSubtree(g *graph.Graph, chains []*chain, kids map[int][]int, idx int) bool {
	if subtreeHasBackSource(g, chains, kids, idx) {
		return false
	}
	inside := subtreeNodes(chains, kids, idx)
	stack := []int{idx}
	for len(stack) > 0 {
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if chains[i].mergeNode != "" {
			return false
		}
		for _, id := range chains[i].nodes {
			for _, fl := range g.ForwardOut(id) {
				if !inside[fl.TargetRef] {
					return false
				}
			}
		}
		stack = append(stack, kids[i]...)
	}
	return true
}

// shorter is the total order deciding which of two eligible alternates is
// lifted: smaller subtree weight, then fewer own nodes, then the
// later-declared entry flow. Chain order is declaration order, so a larger
// index means later-declared.
func shorter(a, b *chain) bool {
	if a.weight != b.weight {
		return a.weight < b.weight
	}
	if len(a.nodes) != len(b.nodes) {
		return len(a.nodes) < len(b.nodes)
	}
	return a.idx > b.idx
}

// markLifted decides, per split node, whether one alternate is routed above
// the spine. A branch is lifted only when its split sits on the spine, has
// exactly three forward outgoing flows and exactly two child chains, the
// branch's subtree is terminal (rule L5), and it is the shorter of the two
// alternates.
func markLifted(g *graph.Graph, c *graph.Component, chains []*chain) {
	kids := childrenOf(chains)

	// Group the child chains by the node they split from, in chain order.
	bySplit := map[string][]int{}
	var splitOrder []string
	for _, ch := range chains {
		if ch.parent < 0 || ch.isRoot || ch.parentNode == "" {
			continue
		}
		if _, seen := bySplit[ch.parentNode]; !seen {
			splitOrder = append(splitOrder, ch.parentNode)
		}
		bySplit[ch.parentNode] = append(bySplit[ch.parentNode], ch.idx)
	}

	for _, node := range splitOrder {
		alts := bySplit[node]
		if !c.SpineSet[node] { // (1) nested gateways never lift
			continue
		}
		if len(g.ForwardOut(node)) != 3 { // (2) exactly a three-way split
			continue
		}
		if len(alts) != 2 { // (3) exactly two alternates hang off it
			continue
		}
		// (5) pick the shorter alternate first, then test it: every rule
		// must hold for the branch that is actually lifted, so a
		// disqualifying back edge in the shorter one lifts nothing rather
		// than promoting the longer one.
		a, b := chains[alts[0]], chains[alts[1]]
		best := a
		if shorter(b, a) {
			best = b
		}
		if !isTerminalSubtree(g, chains, kids, best.idx) { // (4) terminal only
			continue
		}
		best.lifted = true
	}
}

// isLoopHeader reports whether a back edge closes a loop at this gateway.
// The spine walk keeps such a gateway's loop body straight (see
// graph.selectSpine), so everything else hanging off it is a loop exit.
func isLoopHeader(g *graph.Graph, id string) bool {
	n := g.Proc.NodeByID[id]
	if n == nil || !n.Kind.IsGateway() {
		return false
	}
	for _, fl := range g.In[id] {
		if g.Back[fl.ID] {
			return true
		}
	}
	return false
}

// markLoopExits lifts the alternates of a spine loop-header gateway. The
// loop body runs straight through the gateway and its back edge owns the
// lane below that row, so an exit hanging below would have to cross it:
// the exit leaves through the top corner instead. Only a terminal exit is
// lifted (rule L5): one that re-merges downstream hangs below and crosses
// the back edge's lane, which is legal — crossings on way-back lines are
// allowed by design.
func markLoopExits(g *graph.Graph, c *graph.Component, chains []*chain) {
	kids := childrenOf(chains)
	for _, ch := range chains {
		if ch.lifted || ch.isRoot || ch.parentNode == "" {
			continue
		}
		if !c.SpineSet[ch.parentNode] || !isLoopHeader(g, ch.parentNode) {
			continue
		}
		if !isTerminalSubtree(g, chains, kids, ch.idx) {
			continue
		}
		ch.lifted = true
	}
}

// markLoopReturns lifts branches that exist only to return (rule N3): the
// split sits on the spine and every path through the subtree ends in a back
// edge to a spine node EARLIER on the spine than the split ("earlier" by
// spine order, not by column). Below-spine gaps belong to forward branches
// and their rejoins; the sky belongs to returns, so such a branch reads
// best above the spine, its back edges dropping through the gap above the
// target's row.
//
// This deliberately relaxes rule e's "back-edge sources never lift" (see
// subtreeHasBackSource) for exactly this shape: these back edges do not
// need the lane below their row — they return through the sky.
//
// Rule L3 keeps precedence: a retro candidate stays marked and lifts only
// when applyRetro rejects the exact fill.
func markLoopReturns(g *graph.Graph, c *graph.Component, chains []*chain) {
	kids := childrenOf(chains)
	spineIdx := map[string]int{}
	for i, id := range c.Spine {
		spineIdx[id] = i
	}
	for _, ch := range chains {
		if ch.parent < 0 || ch.isRoot || ch.parentNode == "" || ch.lifted {
			continue
		}
		split, onSpine := spineIdx[ch.parentNode]
		if !onSpine {
			continue // nested splits stack below (rule B)
		}
		inside := subtreeNodes(chains, kids, ch.idx)
		subtree := []int{ch.idx}
		for qi := 0; qi < len(subtree); qi++ {
			subtree = append(subtree, kids[subtree[qi]]...)
		}
		ok := true
		for _, ci := range subtree {
			cc := chains[ci]
			// No chain of the subtree may rejoin, and no forward flow may
			// leave OR enter it (other than the entry flow): lifting drags
			// the whole subtree above the spine, which any other forward
			// connection would then have to climb over.
			if cc.mergeNode != "" {
				ok = false
				break
			}
			for _, id := range cc.nodes {
				for _, fl := range g.ForwardOut(id) {
					if !inside[fl.TargetRef] {
						ok = false
					}
				}
				for _, fl := range g.ForwardIn(id) {
					if fl.ID != ch.entryFlow && !inside[fl.SourceRef] {
						ok = false
					}
				}
				// Every back edge must return to a spine node earlier than
				// the split; a dead end without one is a forward stop, not
				// a return.
				backs := 0
				for _, fl := range g.Out[id] {
					if !g.Back[fl.ID] {
						continue
					}
					backs++
					if t, tOnSpine := spineIdx[fl.TargetRef]; !tOnSpine || t >= split {
						ok = false
					}
				}
				if backs == 0 && len(g.ForwardOut(id)) == 0 {
					ok = false
				}
			}
			if !ok {
				break
			}
		}
		if !ok {
			continue
		}
		ch.loopReturn = true
		if !ch.retro {
			ch.lifted = true
		}
	}
}

// flowEndpointsRow classifies every sequence flow of the component relative
// to the chain structure; used by the router.
type flowClass int

const (
	fcChainInternal flowClass = iota // consecutive nodes of one chain
	fcEntry                          // parentNode -> chain head (split)
	fcExit                           // chain tail -> mergeNode (rejoin)
	fcRootExit                       // root chain tail -> mergeNode (left fan-in)
	fcBack                           // loop
	fcCross                          // any other forward flow
)

func classify(g *graph.Graph, chains []*chain, chainOf map[string]int, fl *model.SequenceFlow) flowClass {
	if g.Back[fl.ID] {
		return fcBack
	}
	for _, ch := range chains {
		if ch.entryFlow == fl.ID {
			return fcEntry
		}
		if ch.exitFlow == fl.ID {
			if ch.isRoot {
				return fcRootExit
			}
			return fcExit
		}
	}
	si, ti := chainOf[fl.SourceRef], chainOf[fl.TargetRef]
	if si == ti {
		ch := chains[si]
		for i := 0; i+1 < len(ch.nodes); i++ {
			if ch.nodes[i] == fl.SourceRef && ch.nodes[i+1] == fl.TargetRef {
				return fcChainInternal
			}
		}
	}
	return fcCross
}
