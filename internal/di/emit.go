// Package di renders a layout.Result as a BPMN DI (diagram interchange)
// XML block ready to be spliced into the source file.
package di

import (
	"fmt"
	"math"
	"strings"

	"github.com/pyck-ai/bpmnfmt/internal/layout"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// Emit produces the <bpmndi:BPMNDiagram> element (no surrounding whitespace).
// Shape colors of the previous diagram are carried over.
func Emit(f *model.File, p *model.Process, res *layout.Result) ([]byte, error) {
	pre, err := prefixes(f)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("<%s id=\"BPMNDiagram_1\">\n", pre.di("BPMNDiagram"))
	w("    <%s id=\"BPMNPlane_1\" bpmnElement=\"%s\">\n", pre.di("BPMNPlane"), esc(p.ID))

	shape := func(id string, extra string, r layout.Rect, label *layout.Rect) {
		w("      <%s id=\"%s_di\" bpmnElement=\"%s\"%s>\n", pre.di("BPMNShape"), esc(id), esc(id), extra)
		w("        <%s x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\" />\n", pre.dc("Bounds"), ri(r.X), ri(r.Y), ri(r.W), ri(r.H))
		if label != nil {
			w("        <%s>\n", pre.di("BPMNLabel"))
			w("          <%s x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\" />\n", pre.dc("Bounds"), ri(label.X), ri(label.Y), ri(label.W), ri(label.H))
			w("        </%s>\n", pre.di("BPMNLabel"))
		}
		w("      </%s>\n", pre.di("BPMNShape"))
	}

	for _, n := range p.Nodes {
		r, ok := res.Shapes[n.ID]
		if !ok {
			continue
		}
		extra := colorAttrs(f, pre, n.ID)
		if n.Kind.IsGateway() {
			extra += ` isMarkerVisible="true"`
		}
		var label *layout.Rect
		if l, ok := res.Labels[n.ID]; ok {
			label = &l
		}
		shape(n.ID, extra, r, label)
	}
	for _, a := range p.Annotations {
		r, ok := res.Shapes[a.ID]
		if !ok {
			continue
		}
		shape(a.ID, colorAttrs(f, pre, a.ID), r, nil)
	}

	edge := func(id string, pts []layout.Point, label *layout.Rect) {
		w("      <%s id=\"%s_di\" bpmnElement=\"%s\">\n", pre.di("BPMNEdge"), esc(id), esc(id))
		for _, pt := range pts {
			w("        <%s x=\"%d\" y=\"%d\" />\n", pre.dd("waypoint"), ri(pt.X), ri(pt.Y))
		}
		if label != nil {
			w("        <%s>\n", pre.di("BPMNLabel"))
			w("          <%s x=\"%d\" y=\"%d\" width=\"%d\" height=\"%d\" />\n", pre.dc("Bounds"), ri(label.X), ri(label.Y), ri(label.W), ri(label.H))
			w("        </%s>\n", pre.di("BPMNLabel"))
		}
		w("      </%s>\n", pre.di("BPMNEdge"))
	}

	for _, fl := range p.Flows {
		pts, ok := res.Edges[fl.ID]
		if !ok {
			continue
		}
		var label *layout.Rect
		if l, ok := res.EdgeLabels[fl.ID]; ok {
			label = &l
		}
		edge(fl.ID, pts, label)
	}
	for _, a := range p.Associations {
		if pts, ok := res.Edges[a.ID]; ok {
			edge(a.ID, pts, nil)
		}
	}

	w("    </%s>\n", pre.di("BPMNPlane"))
	w("  </%s>", pre.di("BPMNDiagram"))
	return []byte(b.String()), nil
}

type prefixSet struct {
	pDI, pDC, pDD string
	byNS          map[string]string
}

func (p prefixSet) di(local string) string { return qual(p.pDI, local) }
func (p prefixSet) dc(local string) string { return qual(p.pDC, local) }
func (p prefixSet) dd(local string) string { return qual(p.pDD, local) }

func qual(prefix, local string) string {
	if prefix == "" {
		return local
	}
	return prefix + ":" + local
}

func prefixes(f *model.File) (prefixSet, error) {
	out := prefixSet{byNS: f.Prefixes}
	var ok bool
	if out.pDI, ok = f.Prefix(model.NSBPMNDI); !ok {
		return out, fmt.Errorf("%s: missing xmlns declaration for BPMN DI namespace", f.Path)
	}
	if out.pDC, ok = f.Prefix(model.NSDC); !ok {
		return out, fmt.Errorf("%s: missing xmlns declaration for DC namespace", f.Path)
	}
	if out.pDD, ok = f.Prefix(model.NSDI); !ok {
		return out, fmt.Errorf("%s: missing xmlns declaration for DI namespace", f.Path)
	}
	return out, nil
}

// colorAttrs re-emits bioc/color attributes recorded from the old diagram.
func colorAttrs(f *model.File, pre prefixSet, id string) string {
	if f.DI == nil {
		return ""
	}
	var b strings.Builder
	for _, a := range f.DI.ShapeColors[id] {
		prefix, ok := pre.byNS[a.Space]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, " %s=\"%s\"", qual(prefix, a.Local), esc(a.Value))
	}
	return b.String()
}

func ri(v float64) int { return int(math.Round(v)) }

func esc(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}
