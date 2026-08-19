package layout

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"testing"

	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

var probeFixtures = []string{
	"item-stock-placement.bpmn", "order-created.bpmn", "tour-creation.bpmn",
	"001_mc_creation.bpmn", "001_mc_workflow_assigned.bpmn", "tour-execution.bpmn",
	"picking-subprocess.bpmn", "split-three-corners.bpmn", "split-four-below.bpmn",
	"below-stack-order.bpmn", "branch-entry-elbow.bpmn", "back-edge-below.bpmn",
	"lifted-subtree.bpmn", "split-last-in-chain.bpmn", "corridor-row-ranges.bpmn",
	"lift-only-terminal.bpmn", "cross-link-adjacent.bpmn",
	"gateway-cluster-columns.bpmn", "rejoin-bundle-lane.bpmn",
	"rejoin-right-then-up.bpmn", "loop-branch-backwards.bpmn",
}

func probeLayout(t *testing.T, name string) (*model.Process, *Result) {
	t.Helper()
	f, err := model.ParseFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Processes[0]
	res, err := Compute(p, graph.Build(p))
	if err != nil {
		t.Fatal(err)
	}
	return p, res
}

// TestProbeSites lists (H) shared-terminal and (T) arrowhead-mid-line sites.
func TestProbeSites(t *testing.T) {
	total := 0
	for _, name := range probeFixtures {
		p, res := probeLayout(t, name)
		var ids []string
		for _, sc := range scopeList(p) {
			for _, fl := range sc.Flows {
				if len(res.Edges[fl.ID]) > 0 {
					ids = append(ids, fl.ID)
				}
			}
		}
		sort.Strings(ids)
		for i, a := range ids {
			pa := res.Edges[a]
			enda := pa[len(pa)-1]
			for _, b := range ids[i+1:] {
				pb := res.Edges[b]
				endb := pb[len(pb)-1]
				if math.Abs(enda.X-endb.X) < 0.5 && math.Abs(enda.Y-endb.Y) < 0.5 {
					fmt.Printf("H %-30s (%.0f,%.0f) %s + %s\n", name, enda.X, enda.Y, a, b)
					total++
				}
			}
		}
		for _, a := range ids {
			pa := res.Edges[a]
			enda := pa[len(pa)-1]
			for _, b := range ids {
				if a == b {
					continue
				}
				pb := res.Edges[b]
				for k := 0; k+1 < len(pb); k++ {
					if onSegInterior(enda, pb[k], pb[k+1]) {
						fmt.Printf("T %-30s (%.0f,%.0f) %s ends inside %s\n", name, enda.X, enda.Y, a, b)
						total++
					}
				}
			}
		}
		// Shared collinear run: legal when it is a prefix of BOTH (one
		// trunk), and legal for two flows of one rejoin bundle — rule L6
		// puts them on a single lane on purpose, so the shared run IS the
		// single line the reader is meant to see. Their risers still have
		// to differ, which the H check above enforces.
		target := map[string]string{}
		for _, sc := range scopeList(p) {
			for _, fl := range sc.Flows {
				target[fl.ID] = fl.TargetRef
			}
		}
		for i, a := range ids {
			pa := res.Edges[a]
			for _, b := range ids[i+1:] {
				if target[a] != "" && target[a] == target[b] {
					continue // one rejoin bundle, one lane
				}
				pb := res.Edges[b]
				for x := 0; x+1 < len(pa); x++ {
					for y := 0; y+1 < len(pb); y++ {
						ov, lo, hi := overlapRun(pa[x], pa[x+1], pb[y], pb[y+1])
						if ov <= 0.5 {
							continue
						}
						if samePt(pa[0], pb[0]) && (samePt(pa[0], lo) || samePt(pa[0], hi)) {
							continue // one trunk, both edges peel off it
						}
						fmt.Printf("S %-30s %.0fpx %s(first %v) + %s(first %v) run %v-%v\n",
							name, ov, a, pa[0], b, pb[0], lo, hi)
						total++
					}
				}
			}
		}
	}
	fmt.Println("total defect sites:", total)
}

func samePt(a, b Point) bool { return math.Abs(a.X-b.X) < 0.5 && math.Abs(a.Y-b.Y) < 0.5 }

// overlapRun returns the collinear overlap length of two axis-parallel
// segments and its two ends.
func overlapRun(a1, a2, b1, b2 Point) (float64, Point, Point) {
	if sameX(a1, a2) && sameX(b1, b2) && math.Abs(a1.X-b1.X) < 0.5 {
		lo := math.Max(math.Min(a1.Y, a2.Y), math.Min(b1.Y, b2.Y))
		hi := math.Min(math.Max(a1.Y, a2.Y), math.Max(b1.Y, b2.Y))
		return hi - lo, Point{a1.X, lo}, Point{a1.X, hi}
	}
	if sameY(a1, a2) && sameY(b1, b2) && math.Abs(a1.Y-b1.Y) < 0.5 {
		lo := math.Max(math.Min(a1.X, a2.X), math.Min(b1.X, b2.X))
		hi := math.Min(math.Max(a1.X, a2.X), math.Max(b1.X, b2.X))
		return hi - lo, Point{lo, a1.Y}, Point{hi, a1.Y}
	}
	return 0, Point{}, Point{}
}

func onSegInterior(p, a, b Point) bool {
	const e = 0.5
	if math.Abs(a.X-b.X) < e && math.Abs(p.X-a.X) < e {
		lo, hi := math.Min(a.Y, b.Y), math.Max(a.Y, b.Y)
		return p.Y > lo+e && p.Y < hi-e
	}
	if math.Abs(a.Y-b.Y) < e && math.Abs(p.Y-a.Y) < e {
		lo, hi := math.Min(a.X, b.X), math.Max(a.X, b.X)
		return p.X > lo+e && p.X < hi-e
	}
	return false
}

// TestProbeCrossings prints crossings per fixture, with and without the
// way-back exemption.
func TestProbeCrossings(t *testing.T) {
	sumAll, sumBack := 0, 0
	for _, name := range probeFixtures {
		p, res := probeLayout(t, name)
		type seg struct {
			a, b Point
			edge string
		}
		back := map[string]bool{}
		var segs []seg
		for _, sc := range scopeList(p) {
			for id := range graph.Build(sc).Back {
				back[id] = true
			}
			for _, fl := range sc.Flows {
				pts := res.Edges[fl.ID]
				for i := 0; i+1 < len(pts); i++ {
					segs = append(segs, seg{pts[i], pts[i+1], fl.ID})
				}
			}
		}
		all, exempt := 0, 0
		for i := 0; i < len(segs); i++ {
			for j := i + 1; j < len(segs); j++ {
				if segs[i].edge == segs[j].edge {
					continue
				}
				if !properCrossing(segs[i].a, segs[i].b, segs[j].a, segs[j].b) {
					continue
				}
				all++
				if back[segs[i].edge] && back[segs[j].edge] {
					continue
				}
				exempt++
				fmt.Printf("  X %-30s %s %v-%v  %s %v-%v\n", name,
					segs[i].edge, segs[i].a, segs[i].b, segs[j].edge, segs[j].a, segs[j].b)
			}
		}
		if all > 0 {
			fmt.Printf("%-32s all=%d  nonBackPair=%d\n", name, all, exempt)
		}
		sumAll += all
		sumBack += exempt
	}
	fmt.Printf("TOTAL all=%d  nonBackPair=%d\n", sumAll, sumBack)
}

// TestProbeShapes dumps every shape rect so node movement can be diffed.
func TestProbeShapes(t *testing.T) {
	for _, name := range probeFixtures {
		_, res := probeLayout(t, name)
		for _, id := range sortedKeys(res.Shapes) {
			r := res.Shapes[id]
			fmt.Printf("%s %s %.0f %.0f %.0f %.0f\n", name, id, r.X, r.Y, r.W, r.H)
		}
	}
}
