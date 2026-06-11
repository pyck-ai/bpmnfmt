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
	row        int    // assigned tier (row index), spine = 0
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
	return chains, chainOf, nil
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
