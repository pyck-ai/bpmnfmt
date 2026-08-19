package layout

import (
	"testing"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// --- synthetic chain trees ---------------------------------------------------

// tree builds a chain slice from compact specs. Chains are appended in the
// given order, so index == declaration order, which is what the lift
// tie-break relies on.
type spec struct {
	parent     int // -1 for the spine
	parentNode string
	entryFlow  string
	nodes      []string
}

func tree(specs ...spec) []*chain {
	chains := make([]*chain, len(specs))
	for i, s := range specs {
		chains[i] = &chain{
			idx:        i,
			nodes:      s.nodes,
			parent:     s.parent,
			parentNode: s.parentNode,
			entryFlow:  s.entryFlow,
		}
	}
	return chains
}

func TestComputeWeights(t *testing.T) {
	// spine(3) -> A(2) -> C(4)
	//          -> B(1)
	chains := tree(
		spec{parent: -1, nodes: []string{"s1", "s2", "s3"}},
		spec{parent: 0, parentNode: "s2", entryFlow: "fA", nodes: []string{"a1", "a2"}},
		spec{parent: 0, parentNode: "s2", entryFlow: "fB", nodes: []string{"b1"}},
		spec{parent: 1, parentNode: "a1", entryFlow: "fC", nodes: []string{"c1", "c2", "c3", "c4"}},
	)
	computeWeights(chains)

	for i, want := range []int{10, 6, 1, 4} {
		if got := chains[i].weight; got != want {
			t.Errorf("chain %d weight = %d, want %d", i, got, want)
		}
	}
}

func TestComputeWeightsDeepNesting(t *testing.T) {
	// A straight spine of nested single-node chains: every weight is the
	// number of chains at or below it.
	specs := []spec{{parent: -1, nodes: []string{"n0"}}}
	for i := 1; i < 5; i++ {
		specs = append(specs, spec{parent: i - 1, parentNode: "n0", entryFlow: "f", nodes: []string{"n"}})
	}
	chains := tree(specs...)
	computeWeights(chains)
	for i, want := range []int{5, 4, 3, 2, 1} {
		if got := chains[i].weight; got != want {
			t.Errorf("chain %d weight = %d, want %d", i, got, want)
		}
	}
}

// --- markLifted --------------------------------------------------------------

// fixture describes a synthetic split for the lift rules.
type liftCase struct {
	name string
	// forward flows leaving the split node
	fwd int
	// extra back edges leaving the split node (counted in Out, not ForwardOut)
	backFromSplit int
	onSpine       bool
	chains        []*chain
	// nodes that are sources of a back edge somewhere in the graph
	backSources []string
	wantLifted  int // chain index, or -1 for none
}

func runLift(t *testing.T, tc liftCase) {
	t.Helper()
	const split = "GW"

	out := map[string][]*model.SequenceFlow{}
	back := map[string]bool{}
	for i := 0; i < tc.fwd; i++ {
		id := "fwd" + string(rune('A'+i))
		out[split] = append(out[split], &model.SequenceFlow{ID: id, SourceRef: split, TargetRef: "t" + id})
	}
	for i := 0; i < tc.backFromSplit; i++ {
		id := "bk" + string(rune('A'+i))
		out[split] = append(out[split], &model.SequenceFlow{ID: id, SourceRef: split, TargetRef: "loop"})
		back[id] = true
	}
	for _, src := range tc.backSources {
		id := "back_" + src
		out[src] = append(out[src], &model.SequenceFlow{ID: id, SourceRef: src, TargetRef: "earlier"})
		back[id] = true
	}

	g := &graph.Graph{Out: out, Back: back}
	c := &graph.Component{SpineSet: map[string]bool{}}
	if tc.onSpine {
		c.SpineSet[split] = true
	}

	computeWeights(tc.chains)
	markLifted(g, c, tc.chains)

	got := -1
	for _, ch := range tc.chains {
		if ch.lifted {
			if got != -1 {
				t.Fatalf("%s: more than one chain lifted (%d and %d)", tc.name, got, ch.idx)
			}
			got = ch.idx
		}
	}
	if got != tc.wantLifted {
		t.Errorf("%s: lifted chain = %d, want %d", tc.name, got, tc.wantLifted)
	}
}

// twoAlts builds the standard shape: a spine plus two alternates off "GW".
func twoAlts(a, b []string) []*chain {
	return tree(
		spec{parent: -1, nodes: []string{"s1", "GW", "s3"}},
		spec{parent: 0, parentNode: "GW", entryFlow: "fA", nodes: a},
		spec{parent: 0, parentNode: "GW", entryFlow: "fB", nodes: b},
	)
}

func TestMarkLiftedRules(t *testing.T) {
	cases := []liftCase{
		{
			name: "eligible: shorter alternate lifts",
			fwd:  3, onSpine: true,
			chains:     twoAlts([]string{"a1", "a2", "a3"}, []string{"b1"}),
			wantLifted: 2,
		},
		{
			name: "rule 1: split not on the spine",
			fwd:  3, onSpine: false,
			chains:     twoAlts([]string{"a1", "a2", "a3"}, []string{"b1"}),
			wantLifted: -1,
		},
		{
			name: "rule 2: two-way split",
			fwd:  2, onSpine: true,
			chains:     twoAlts([]string{"a1", "a2", "a3"}, []string{"b1"}),
			wantLifted: -1,
		},
		{
			name: "rule 2: four-way split",
			fwd:  4, onSpine: true,
			chains:     twoAlts([]string{"a1", "a2", "a3"}, []string{"b1"}),
			wantLifted: -1,
		},
		{
			name: "rule 2: back edges do not count towards the three",
			fwd:  2, backFromSplit: 1, onSpine: true,
			chains:     twoAlts([]string{"a1", "a2", "a3"}, []string{"b1"}),
			wantLifted: -1,
		},
		{
			name: "rule 3: only one alternate chain",
			fwd:  3, onSpine: true,
			chains: tree(
				spec{parent: -1, nodes: []string{"s1", "GW", "s3"}},
				spec{parent: 0, parentNode: "GW", entryFlow: "fA", nodes: []string{"a1"}},
			),
			wantLifted: -1,
		},
		{
			name: "rule 3: three alternate chains",
			fwd:  3, onSpine: true,
			chains: tree(
				spec{parent: -1, nodes: []string{"s1", "GW", "s3"}},
				spec{parent: 0, parentNode: "GW", entryFlow: "fA", nodes: []string{"a1"}},
				spec{parent: 0, parentNode: "GW", entryFlow: "fB", nodes: []string{"b1"}},
				spec{parent: 0, parentNode: "GW", entryFlow: "fC", nodes: []string{"c1"}},
			),
			wantLifted: -1,
		},
		{
			name: "rule 4: shorter alternate is a back-edge source",
			fwd:  3, onSpine: true,
			chains:      twoAlts([]string{"a1", "a2", "a3"}, []string{"b1"}),
			backSources: []string{"b1"},
			wantLifted:  -1, // the longer alternate is NOT promoted
		},
		{
			name: "rule 4: back-edge source in the shorter alternate's descendant",
			fwd:  3, onSpine: true,
			chains: tree(
				spec{parent: -1, nodes: []string{"s1", "GW", "s3"}},
				spec{parent: 0, parentNode: "GW", entryFlow: "fA", nodes: []string{"a1", "a2", "a3", "a4"}},
				spec{parent: 0, parentNode: "GW", entryFlow: "fB", nodes: []string{"b1"}},
				spec{parent: 2, parentNode: "b1", entryFlow: "fC", nodes: []string{"d1"}},
			),
			backSources: []string{"d1"},
			wantLifted:  -1,
		},
		{
			name: "rule 4: a back-edge source in the OTHER alternate is irrelevant",
			fwd:  3, onSpine: true,
			chains:      twoAlts([]string{"a1", "a2", "a3"}, []string{"b1"}),
			backSources: []string{"a2"},
			wantLifted:  2,
		},
		{
			name: "tie-break 1: subtree weight beats own length",
			fwd:  3, onSpine: true,
			chains: tree(
				spec{parent: -1, nodes: []string{"s1", "GW", "s3"}},
				// A: 1 own node but a heavy subtree (weight 4)
				spec{parent: 0, parentNode: "GW", entryFlow: "fA", nodes: []string{"a1"}},
				// B: 2 own nodes, no children (weight 2)
				spec{parent: 0, parentNode: "GW", entryFlow: "fB", nodes: []string{"b1", "b2"}},
				spec{parent: 1, parentNode: "a1", entryFlow: "fD", nodes: []string{"d1", "d2", "d3"}},
			),
			wantLifted: 2,
		},
		{
			name: "tie-break 2: equal weight, fewer own nodes wins",
			fwd:  3, onSpine: true,
			chains: tree(
				spec{parent: -1, nodes: []string{"s1", "GW", "s3"}},
				// A: 2 own nodes, weight 2
				spec{parent: 0, parentNode: "GW", entryFlow: "fA", nodes: []string{"a1", "a2"}},
				// B: 1 own node + child of 1 => weight 2, but only 1 own node
				spec{parent: 0, parentNode: "GW", entryFlow: "fB", nodes: []string{"b1"}},
				spec{parent: 2, parentNode: "b1", entryFlow: "fD", nodes: []string{"d1"}},
			),
			wantLifted: 2,
		},
		{
			name: "tie-break 3: fully tied, later-declared goes up",
			fwd:  3, onSpine: true,
			chains:     twoAlts([]string{"a1", "a2"}, []string{"b1", "b2"}),
			wantLifted: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { runLift(t, tc) })
	}
}

// TestMarkLiftedPerSplit pins rule C's scope: alternates are ordered per
// split node, so two independent spine gateways each lift their own shorter
// branch.
func TestMarkLiftedPerSplit(t *testing.T) {
	out := map[string][]*model.SequenceFlow{}
	for _, gw := range []string{"GW1", "GW2"} {
		for i := 0; i < 3; i++ {
			id := gw + string(rune('a'+i))
			out[gw] = append(out[gw], &model.SequenceFlow{ID: id, SourceRef: gw, TargetRef: "t" + id})
		}
	}
	g := &graph.Graph{Out: out, Back: map[string]bool{}}
	c := &graph.Component{SpineSet: map[string]bool{"GW1": true, "GW2": true}}

	chains := tree(
		spec{parent: -1, nodes: []string{"s1", "GW1", "GW2"}},
		spec{parent: 0, parentNode: "GW1", entryFlow: "f1", nodes: []string{"p1", "p2", "p3"}},
		spec{parent: 0, parentNode: "GW1", entryFlow: "f2", nodes: []string{"q1"}},
		spec{parent: 0, parentNode: "GW2", entryFlow: "f3", nodes: []string{"r1"}},
		spec{parent: 0, parentNode: "GW2", entryFlow: "f4", nodes: []string{"u1", "u2"}},
	)
	computeWeights(chains)
	markLifted(g, c, chains)

	want := map[int]bool{2: true, 3: true} // q1 under GW1, r1 under GW2
	for _, ch := range chains {
		if ch.lifted != want[ch.idx] {
			t.Errorf("chain %d lifted = %v, want %v", ch.idx, ch.lifted, want[ch.idx])
		}
	}
}
