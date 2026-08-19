// Package graph builds the directed flow graph of a BPMN process and derives
// the structures the linter and layouter need: weakly connected components,
// back edges (loops) and the happy-path spine.
package graph

import (
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// Graph is the sequence-flow graph of one process.
type Graph struct {
	Proc *model.Process
	opts Options
	// Out and In hold flows per node in declared order: the order of the
	// node's <outgoing>/<incoming> children, with undeclared flows appended
	// in document order. Declared order encodes the modeler's intent and
	// drives spine selection ("first-declared flow wins").
	Out map[string][]*model.SequenceFlow
	In  map[string][]*model.SequenceFlow
	// Back marks sequence flow IDs that close a loop (DFS back edges).
	Back map[string]bool
	// Components in document order (by first contained node).
	Components []*Component
}

// Component is one weakly connected subgraph.
type Component struct {
	Nodes   []*model.FlowNode // document order
	NodeSet map[string]bool
	Starts  []*model.FlowNode // start events in document order
	// Spine is the happy path: node IDs from the primary start to an end
	// event, following first-declared forward flows.
	Spine      []string
	SpineSet   map[string]bool
	SpineFlows map[string]bool // flow IDs connecting consecutive spine nodes
}

// Options tune spine selection.
type Options struct {
	// HappyEnd names the end event the spine must reach (when reachable).
	HappyEnd string
	// HappyFlows are preferred at splits during the spine walk.
	HappyFlows map[string]bool
}

// Build constructs the graph for a process.
func Build(p *model.Process) *Graph { return BuildOpts(p, Options{}) }

// BuildOpts constructs the graph with spine overrides.
func BuildOpts(p *model.Process, opts Options) *Graph {
	g := &Graph{
		Proc: p,
		opts: opts,
		Out:  map[string][]*model.SequenceFlow{},
		In:   map[string][]*model.SequenceFlow{},
		Back: map[string]bool{},
	}
	g.buildAdjacency()
	g.buildComponents()
	g.findBackEdges()
	for _, c := range g.Components {
		g.selectSpine(c)
	}
	return g
}

func (g *Graph) buildAdjacency() {
	p := g.Proc
	// known reports whether both endpoints of fl point at nodes the model
	// registered. Flows that reference unsupported elements (e.g. a
	// parallelGateway, which the parser puts in Process.Unsupported but
	// not in NodeByID) are skipped: keeping them would let the spine walk
	// step to a node that does not exist and panic. Lint reports the
	// unsupported element separately (E7).
	known := func(fl *model.SequenceFlow) bool {
		return p.NodeByID[fl.SourceRef] != nil && p.NodeByID[fl.TargetRef] != nil
	}
	for _, n := range p.Nodes {
		seenOut := map[string]bool{}
		for _, fid := range n.Outgoing {
			fl := p.FlowByID[fid]
			if fl == nil || fl.SourceRef != n.ID || seenOut[fid] || !known(fl) {
				continue
			}
			g.Out[n.ID] = append(g.Out[n.ID], fl)
			seenOut[fid] = true
		}
		seenIn := map[string]bool{}
		for _, fid := range n.Incoming {
			fl := p.FlowByID[fid]
			if fl == nil || fl.TargetRef != n.ID || seenIn[fid] || !known(fl) {
				continue
			}
			g.In[n.ID] = append(g.In[n.ID], fl)
			seenIn[fid] = true
		}
		// Append flows that exist in the model but are missing from the
		// declaration lists (tolerates slightly inconsistent files).
		for _, fl := range p.Flows {
			if !known(fl) {
				continue
			}
			if fl.SourceRef == n.ID && !seenOut[fl.ID] {
				g.Out[n.ID] = append(g.Out[n.ID], fl)
				seenOut[fl.ID] = true
			}
			if fl.TargetRef == n.ID && !seenIn[fl.ID] {
				g.In[n.ID] = append(g.In[n.ID], fl)
				seenIn[fl.ID] = true
			}
		}
	}
}

func (g *Graph) buildComponents() {
	p := g.Proc
	compOf := map[string]int{}
	next := 0
	for _, seed := range p.Nodes {
		if _, ok := compOf[seed.ID]; ok {
			continue
		}
		id := next
		next++
		queue := []string{seed.ID}
		compOf[seed.ID] = id
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			var neighbors []string
			for _, fl := range g.Out[cur] {
				neighbors = append(neighbors, fl.TargetRef)
			}
			for _, fl := range g.In[cur] {
				neighbors = append(neighbors, fl.SourceRef)
			}
			for _, nb := range neighbors {
				if _, ok := compOf[nb]; !ok && p.NodeByID[nb] != nil {
					compOf[nb] = id
					queue = append(queue, nb)
				}
			}
		}
	}
	comps := make([]*Component, next)
	for i := range comps {
		comps[i] = &Component{NodeSet: map[string]bool{}}
	}
	for _, n := range p.Nodes { // document order
		c := comps[compOf[n.ID]]
		c.Nodes = append(c.Nodes, n)
		c.NodeSet[n.ID] = true
		if n.Kind == model.KindStartEvent {
			c.Starts = append(c.Starts, n)
		}
	}
	g.Components = comps
}

// findBackEdges runs an iterative DFS per component, visiting start events
// first (document order) and following outgoing flows in declared order, so
// the back-edge set is deterministic and matches the modeler's reading.
func (g *Graph) findBackEdges() {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}

	type item struct {
		id   string
		next int // index of the next outgoing flow to process
	}

	dfs := func(root string) {
		if color[root] != white {
			return
		}
		stack := []item{{id: root}}
		color[root] = gray
		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			out := g.Out[top.id]
			if top.next >= len(out) {
				color[top.id] = black
				stack = stack[:len(stack)-1]
				continue
			}
			fl := out[top.next]
			top.next++
			switch color[fl.TargetRef] {
			case white:
				color[fl.TargetRef] = gray
				stack = append(stack, item{id: fl.TargetRef})
			case gray:
				g.Back[fl.ID] = true
			}
		}
	}

	for _, c := range g.Components {
		for _, s := range c.Starts {
			dfs(s.ID)
		}
		for _, n := range c.Nodes {
			dfs(n.ID)
		}
	}
}

// ForwardOut returns the outgoing flows of a node excluding back edges.
func (g *Graph) ForwardOut(id string) []*model.SequenceFlow {
	var out []*model.SequenceFlow
	for _, fl := range g.Out[id] {
		if !g.Back[fl.ID] {
			out = append(out, fl)
		}
	}
	return out
}

// ForwardIn returns the incoming flows of a node excluding back edges.
func (g *Graph) ForwardIn(id string) []*model.SequenceFlow {
	var in []*model.SequenceFlow
	for _, fl := range g.In[id] {
		if !g.Back[fl.ID] {
			in = append(in, fl)
		}
	}
	return in
}

// selectSpine picks the happy path of a component: starting at the primary
// start event, greedily follow the first-declared forward flow whose target
// can still reach an end event; stop at the first end event reached.
//
// At a loop-header gateway (a gateway a back edge returns to) the loop body
// wins instead: the branch whose subtree feeds that back edge stays on the
// spine and the loop exit becomes an alternate, so the loop reads as a
// straight run with the back edge under it. An explicit HappyFlows override
// still takes precedence.
func (g *Graph) selectSpine(c *Component) {
	if len(c.Nodes) == 0 {
		return
	}
	root := g.primaryStart(c)
	canEnd := g.canReachEnd(c)

	spine := []string{root}
	spineSet := map[string]bool{root: true}
	spineFlows := map[string]bool{}
	cur := root
	for {
		n := g.Proc.NodeByID[cur]
		if n == nil || n.Kind == model.KindEndEvent {
			break
		}
		loopSrcs := g.loopBackSources(n)
		var chosen, preferred, loopBody, fallback *model.SequenceFlow
		for _, fl := range g.ForwardOut(cur) {
			if spineSet[fl.TargetRef] {
				continue // never revisit (paranoia; forward edges form a DAG)
			}
			if fallback == nil {
				fallback = fl
			}
			if loopBody == nil && len(loopSrcs) > 0 && g.reachesForward(fl.TargetRef, loopSrcs) {
				loopBody = fl
			}
			if canEnd[fl.TargetRef] {
				if g.opts.HappyFlows[fl.ID] {
					preferred = fl
					break
				}
				if chosen == nil {
					chosen = fl
				}
			}
		}
		switch {
		case preferred != nil:
			chosen = preferred
		case loopBody != nil:
			chosen = loopBody
		case chosen == nil:
			chosen = fallback // component without reachable end event
		}
		if chosen == nil {
			break // dead end
		}
		spineFlows[chosen.ID] = true
		cur = chosen.TargetRef
		spine = append(spine, cur)
		spineSet[cur] = true
	}
	c.Spine = spine
	c.SpineSet = spineSet
	c.SpineFlows = spineFlows
}

// loopBackSources returns the sources of the back edges that close a loop at
// this node, but only for gateways: a node without a split has no branch to
// choose between, so the loop-header rule cannot apply to it.
func (g *Graph) loopBackSources(n *model.FlowNode) map[string]bool {
	if !n.Kind.IsGateway() {
		return nil
	}
	var srcs map[string]bool
	for _, fl := range g.In[n.ID] {
		if !g.Back[fl.ID] {
			continue
		}
		if srcs == nil {
			srcs = map[string]bool{}
		}
		srcs[fl.SourceRef] = true
	}
	return srcs
}

// reachesForward reports whether any node in targets is reachable from `from`
// over forward flows (from itself counts).
func (g *Graph) reachesForward(from string, targets map[string]bool) bool {
	seen := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if targets[cur] {
			return true
		}
		for _, fl := range g.ForwardOut(cur) {
			if !seen[fl.TargetRef] {
				seen[fl.TargetRef] = true
				queue = append(queue, fl.TargetRef)
			}
		}
	}
	return false
}

// primaryStart returns the first start event of the component in document
// order, falling back to the first source-only node, then the first node.
func (g *Graph) primaryStart(c *Component) string {
	if len(c.Starts) > 0 {
		return c.Starts[0].ID
	}
	for _, n := range c.Nodes {
		if len(g.In[n.ID]) == 0 {
			return n.ID
		}
	}
	// All nodes have incoming (pure cycle): pick the first whose incoming
	// flows are all back edges, else the first node.
	for _, n := range c.Nodes {
		if len(g.ForwardIn(n.ID)) == 0 {
			return n.ID
		}
	}
	return c.Nodes[0].ID
}

// canReachEnd computes, per node of the component, whether an end event is
// reachable via forward edges. With Options.HappyEnd set (and present in the
// component), only that end event counts.
func (g *Graph) canReachEnd(c *Component) map[string]bool {
	can := map[string]bool{}
	var queue []string
	if g.opts.HappyEnd != "" && c.NodeSet[g.opts.HappyEnd] {
		can[g.opts.HappyEnd] = true
		queue = append(queue, g.opts.HappyEnd)
	} else {
		for _, n := range c.Nodes {
			if n.Kind == model.KindEndEvent {
				can[n.ID] = true
				queue = append(queue, n.ID)
			}
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, fl := range g.ForwardIn(cur) {
			if !can[fl.SourceRef] {
				can[fl.SourceRef] = true
				queue = append(queue, fl.SourceRef)
			}
		}
	}
	return can
}
