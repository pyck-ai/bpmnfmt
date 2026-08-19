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

	// walk extends a chain from a start node, following first-declared
	// forward flows into unassigned territory until it merges or dead-ends.
	// Forward flows form a DAG, so the walk terminates.
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
	return chains, chainOf, nil
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
// branch's subtree contains no back-edge source, and it is the shorter of
// the two alternates.
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
		if subtreeHasBackSource(g, chains, kids, best.idx) { // (4) no loop sources
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
// the exit leaves through the top corner instead. A branch that itself
// sources a back edge keeps rule e's lane below and is left alone.
func markLoopExits(g *graph.Graph, c *graph.Component, chains []*chain) {
	kids := childrenOf(chains)
	for _, ch := range chains {
		if ch.lifted || ch.isRoot || ch.parentNode == "" {
			continue
		}
		if !c.SpineSet[ch.parentNode] || !isLoopHeader(g, ch.parentNode) {
			continue
		}
		if subtreeHasBackSource(g, chains, kids, ch.idx) {
			continue
		}
		ch.lifted = true
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
