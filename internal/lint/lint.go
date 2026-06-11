// Package lint statically checks BPMN process models for logical errors and
// style problems. Errors indicate a broken or ambiguous model (and block
// formatting), warnings indicate likely modeling mistakes, infos are style
// notes.
package lint

import (
	"fmt"
	"sort"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// Severity of a finding.
type Severity int

const (
	SevInfo Severity = iota
	SevWarning
	SevError
)

func (s Severity) String() string {
	switch s {
	case SevError:
		return "error"
	case SevWarning:
		return "warning"
	default:
		return "info"
	}
}

// ParseSeverity converts a -fail-on flag value.
func ParseSeverity(s string) (Severity, error) {
	switch s {
	case "error":
		return SevError, nil
	case "warning":
		return SevWarning, nil
	case "info":
		return SevInfo, nil
	}
	return 0, fmt.Errorf("invalid severity %q (want error, warning or info)", s)
}

// Finding is one lint result.
type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"-"`
	Sev      string   `json:"severity"`
	Element  string   `json:"element,omitempty"`
	Message  string   `json:"message"`

	docIdx int
}

// Check runs all rules against a parsed file.
func Check(f *model.File) []Finding {
	c := &checker{f: f}

	c.fileRules()
	for _, p := range f.Processes {
		g := graph.Build(p)
		c.refRules(p)
		c.duplicateIDs()
		c.nodeRules(p, g)
		c.gatewayRules(p, g)
		c.reachabilityRules(p, g)
		c.componentRules(p, g)
		c.diRules(p)
	}

	sort.SliceStable(c.out, func(i, j int) bool {
		if c.out[i].docIdx != c.out[j].docIdx {
			return c.out[i].docIdx < c.out[j].docIdx
		}
		return c.out[i].Rule < c.out[j].Rule
	})
	return c.out
}

// MaxSeverity returns the highest severity present (SevInfo when empty is
// fine because callers compare against a threshold with >=).
func MaxSeverity(fs []Finding) (Severity, bool) {
	if len(fs) == 0 {
		return SevInfo, false
	}
	max := SevInfo
	for _, f := range fs {
		if f.Severity > max {
			max = f.Severity
		}
	}
	return max, true
}

// HasErrors reports whether any finding is an error.
func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == SevError {
			return true
		}
	}
	return false
}

type checker struct {
	f   *model.File
	out []Finding
}

func (c *checker) add(rule string, sev Severity, element string, docIdx int, format string, args ...any) {
	c.out = append(c.out, Finding{
		Rule:     rule,
		Severity: sev,
		Sev:      sev.String(),
		Element:  element,
		Message:  fmt.Sprintf(format, args...),
		docIdx:   docIdx,
	})
}

// --- file-level rules ------------------------------------------------------

func (c *checker) fileRules() {
	for _, u := range c.f.Unsupported {
		c.add("E7", SevError, u.ID, -1, "unsupported construct <%s>: bpmnfmt cannot lay out collaborations/pools", u.Tag)
	}
	if len(c.f.Processes) > 1 {
		c.add("E7", SevError, "", -1, "file contains %d processes; bpmnfmt supports exactly one", len(c.f.Processes))
	}
	if len(c.f.DiagramSpans) > 1 {
		c.add("E7", SevError, "", -1, "file contains %d BPMNDiagram elements; bpmnfmt supports at most one", len(c.f.DiagramSpans))
	}
}

// --- E1: reference integrity ------------------------------------------------

func (c *checker) refRules(p *model.Process) {
	for _, fl := range p.Flows {
		if p.NodeByID[fl.SourceRef] == nil {
			c.add("E1", SevError, fl.ID, fl.DocIndex, "sequence flow sourceRef %q does not resolve to a flow node", fl.SourceRef)
		}
		if p.NodeByID[fl.TargetRef] == nil {
			c.add("E1", SevError, fl.ID, fl.DocIndex, "sequence flow targetRef %q does not resolve to a flow node", fl.TargetRef)
		}
	}
	resolves := func(id string) bool {
		return p.NodeByID[id] != nil || p.FlowByID[id] != nil || p.AnnByID[id] != nil
	}
	for _, a := range p.Associations {
		if !resolves(a.SourceRef) {
			c.add("E1", SevError, a.ID, a.DocIndex, "association sourceRef %q does not resolve", a.SourceRef)
		}
		if !resolves(a.TargetRef) {
			c.add("E1", SevError, a.ID, a.DocIndex, "association targetRef %q does not resolve", a.TargetRef)
		}
	}
	// Declaration consistency, only enforced when the process uses
	// incoming/outgoing declarations at all (exported files always do).
	declares := false
	for _, n := range p.Nodes {
		if len(n.Incoming) > 0 || len(n.Outgoing) > 0 {
			declares = true
			break
		}
	}
	if !declares {
		return
	}
	for _, n := range p.Nodes {
		declaredOut := map[string]bool{}
		for _, fid := range n.Outgoing {
			declaredOut[fid] = true
			fl := p.FlowByID[fid]
			if fl == nil {
				c.add("E1", SevError, n.ID, n.DocIndex, "declared outgoing %q does not exist", fid)
			} else if fl.SourceRef != n.ID {
				c.add("E1", SevError, n.ID, n.DocIndex, "declared outgoing %q has sourceRef %q", fid, fl.SourceRef)
			}
		}
		declaredIn := map[string]bool{}
		for _, fid := range n.Incoming {
			declaredIn[fid] = true
			fl := p.FlowByID[fid]
			if fl == nil {
				c.add("E1", SevError, n.ID, n.DocIndex, "declared incoming %q does not exist", fid)
			} else if fl.TargetRef != n.ID {
				c.add("E1", SevError, n.ID, n.DocIndex, "declared incoming %q has targetRef %q", fid, fl.TargetRef)
			}
		}
		for _, fl := range p.Flows {
			if fl.SourceRef == n.ID && !declaredOut[fl.ID] {
				c.add("E1", SevError, n.ID, n.DocIndex, "flow %s leaves this node but is not declared as outgoing", fl.ID)
			}
			if fl.TargetRef == n.ID && !declaredIn[fl.ID] {
				c.add("E1", SevError, n.ID, n.DocIndex, "flow %s enters this node but is not declared as incoming", fl.ID)
			}
		}
	}
}

// --- E2: duplicate IDs -------------------------------------------------------

func (c *checker) duplicateIDs() {
	seen := map[string]string{}
	reported := map[string]bool{}
	for _, d := range c.f.IDs {
		if prev, ok := seen[d.ID]; ok && !reported[d.ID] {
			c.add("E2", SevError, d.ID, -1, "duplicate id %q (used by <%s> and <%s>)", d.ID, prev, d.Tag)
			reported[d.ID] = true
			continue
		}
		seen[d.ID] = d.Tag
	}
}

// --- E3/E4, W1, I1: node degree rules ---------------------------------------

func (c *checker) nodeRules(p *model.Process, g *graph.Graph) {
	for _, n := range p.Nodes {
		in, out := len(g.In[n.ID]), len(g.Out[n.ID])
		switch n.Kind {
		case model.KindStartEvent:
			if in > 0 {
				c.add("E3", SevError, n.ID, n.DocIndex, "start event has %d incoming sequence flow(s)", in)
			}
			if out == 0 {
				c.add("E4", SevError, n.ID, n.DocIndex, "start event has no outgoing sequence flow")
			}
		case model.KindEndEvent:
			if out > 0 {
				c.add("E3", SevError, n.ID, n.DocIndex, "end event has %d outgoing sequence flow(s)", out)
			}
			if in == 0 {
				c.add("E4", SevError, n.ID, n.DocIndex, "end event has no incoming sequence flow")
			}
		default:
			if in == 0 {
				c.add("E4", SevError, n.ID, n.DocIndex, "%s %q is not wired in: no incoming sequence flow", n.Tag, name(n))
			}
			if out == 0 {
				c.add("E4", SevError, n.ID, n.DocIndex, "%s %q is not wired in: no outgoing sequence flow", n.Tag, name(n))
			}
		}
		if n.Kind != model.KindExclusiveGateway {
			if out > 1 {
				c.add("W1", SevWarning, n.ID, n.DocIndex, "implicit split: %s has %d outgoing flows (tokens fork in parallel; use a gateway)", n.Tag, out)
			}
			if in > 1 {
				c.add("I1", SevInfo, n.ID, n.DocIndex, "implicit merge: %s has %d incoming flows", n.Tag, in)
			}
		}
	}
}

// --- W2/W3/W4, I2: gateway rules ---------------------------------------------

func (c *checker) gatewayRules(p *model.Process, g *graph.Graph) {
	for _, n := range p.Nodes {
		if n.Kind != model.KindExclusiveGateway {
			continue
		}
		in, out := g.In[n.ID], g.Out[n.ID]
		if len(out) >= 2 {
			if n.Name == "" {
				c.add("W3", SevWarning, n.ID, n.DocIndex, "decision gateway has no name; name it with the question it decides")
			}
			for _, fl := range out {
				if fl.Name == "" {
					c.add("W2", SevWarning, fl.ID, fl.DocIndex, "unlabeled branch out of decision gateway %q", name(n))
				}
			}
		}
		if len(in) >= 2 && len(out) >= 2 {
			c.add("W4", SevWarning, n.ID, n.DocIndex, "gateway both merges (%d in) and splits (%d out); split it into two gateways", len(in), len(out))
		}
		if len(in) == 1 && len(out) == 1 {
			c.add("I2", SevInfo, n.ID, n.DocIndex, "gateway with one incoming and one outgoing flow has no effect")
		}
	}
}

// --- E5: reachability ----------------------------------------------------------

func (c *checker) reachabilityRules(p *model.Process, g *graph.Graph) {
	// Forward reachability from all start events (over all edges).
	reached := map[string]bool{}
	var queue []string
	for _, n := range p.Nodes {
		if n.Kind == model.KindStartEvent {
			reached[n.ID] = true
			queue = append(queue, n.ID)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, fl := range g.Out[cur] {
			if !reached[fl.TargetRef] && p.NodeByID[fl.TargetRef] != nil {
				reached[fl.TargetRef] = true
				queue = append(queue, fl.TargetRef)
			}
		}
	}
	// Backward reachability from all end events.
	reachesEnd := map[string]bool{}
	queue = queue[:0]
	for _, n := range p.Nodes {
		if n.Kind == model.KindEndEvent {
			reachesEnd[n.ID] = true
			queue = append(queue, n.ID)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, fl := range g.In[cur] {
			if !reachesEnd[fl.SourceRef] && p.NodeByID[fl.SourceRef] != nil {
				reachesEnd[fl.SourceRef] = true
				queue = append(queue, fl.SourceRef)
			}
		}
	}

	for _, comp := range g.Components {
		hasStart := len(comp.Starts) > 0
		hasEnd := false
		for _, n := range comp.Nodes {
			if n.Kind == model.KindEndEvent {
				hasEnd = true
				break
			}
		}
		first := comp.Nodes[0]
		if !hasStart {
			c.add("E5", SevError, first.ID, first.DocIndex, "flow section around %q has no start event", name(first))
		}
		if !hasEnd {
			c.add("E5", SevError, first.ID, first.DocIndex, "flow section around %q has no end event", name(first))
		}
		for _, n := range comp.Nodes {
			// Degree problems are already E4; don't double-report.
			if hasStart && !reached[n.ID] && len(g.In[n.ID]) > 0 {
				c.add("E5", SevError, n.ID, n.DocIndex, "%s %q is unreachable from any start event", n.Tag, name(n))
			}
			if hasEnd && !reachesEnd[n.ID] && len(g.Out[n.ID]) > 0 && n.Kind != model.KindEndEvent {
				c.add("E5", SevError, n.ID, n.DocIndex, "no path from %s %q to any end event (inescapable loop?)", n.Tag, name(n))
			}
		}
	}
}

// --- W5, I3: process shape ------------------------------------------------------

func (c *checker) componentRules(p *model.Process, g *graph.Graph) {
	if len(g.Components) > 1 {
		c.add("W5", SevWarning, p.ID, -1, "process contains %d disconnected flows; consider splitting them into separate processes", len(g.Components))
	}
	starts := 0
	for _, n := range p.Nodes {
		if n.Kind == model.KindStartEvent {
			starts++
		}
	}
	if starts > 1 {
		c.add("I3", SevInfo, p.ID, -1, "process has %d start events", starts)
	}
}

// --- W6/W7: diagram interchange ---------------------------------------------------

func (c *checker) diRules(p *model.Process) {
	di := c.f.DI
	if di == nil {
		c.add("W6", SevWarning, p.ID, -1, "file has no BPMNDiagram section (bpmnfmt will generate one)")
		return
	}
	check := func(id string, docIdx int, what string) {
		if id != "" && !di.RefSet[id] {
			c.add("W6", SevWarning, id, docIdx, "%s has no diagram shape/edge and would be invisible", what)
		}
	}
	for _, n := range p.Nodes {
		check(n.ID, n.DocIndex, n.Tag)
	}
	for _, fl := range p.Flows {
		check(fl.ID, fl.DocIndex, "sequence flow")
	}
	for _, a := range p.Annotations {
		check(a.ID, a.DocIndex, "text annotation")
	}
	for _, a := range p.Associations {
		check(a.ID, a.DocIndex, "association")
	}
	known := func(id string) bool {
		return p.NodeByID[id] != nil || p.FlowByID[id] != nil || p.AnnByID[id] != nil ||
			id == p.ID || c.assocByID(p, id)
	}
	for _, ref := range di.Refs {
		if !known(ref) {
			c.add("W7", SevWarning, ref, -1, "diagram references unknown element %q", ref)
		}
	}
}

func (c *checker) assocByID(p *model.Process, id string) bool {
	for _, a := range p.Associations {
		if a.ID == id {
			return true
		}
	}
	return false
}

func name(n *model.FlowNode) string {
	if n.Name != "" {
		return n.Name
	}
	return n.ID
}
