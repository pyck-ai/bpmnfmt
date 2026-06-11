// Package format ties parsing, linting, layout and DI emission together.
package format

import (
	"fmt"

	"github.com/pyck-ai/bpmnfmt/internal/di"
	"github.com/pyck-ai/bpmnfmt/internal/graph"
	"github.com/pyck-ai/bpmnfmt/internal/layout"
	"github.com/pyck-ai/bpmnfmt/internal/lint"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// Options controls formatting.
type Options struct {
	Force      bool   // lay out even when lint reports errors
	HappyEnd   string // end event the spine must reach
	HappyFlows map[string]bool
}

// Result of one file.
type Result struct {
	Findings  []lint.Finding
	Output    []byte // nil when not formatted
	Formatted bool
}

// File lints and reformats one parsed BPMN file.
func File(f *model.File, opts Options) (*Result, error) {
	res := &Result{Findings: lint.Check(f)}
	if lint.HasErrors(res.Findings) && !opts.Force {
		return res, nil
	}
	if len(f.Processes) != 1 || len(f.DiagramSpans) > 1 {
		return res, nil // unsupported shape; reported as E7 findings
	}
	p := f.Processes[0]
	g := graph.BuildOpts(p, graph.Options{HappyEnd: opts.HappyEnd, HappyFlows: opts.HappyFlows})
	lay, err := layout.Compute(p, g)
	if err != nil {
		return res, fmt.Errorf("%s: layout: %w", f.Path, err)
	}
	block, err := di.Emit(f, p, lay)
	if err != nil {
		return res, err
	}
	out, err := f.SpliceDI(block)
	if err != nil {
		return res, err
	}
	res.Output = out
	res.Formatted = true
	return res, nil
}
