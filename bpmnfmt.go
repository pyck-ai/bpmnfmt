// Package bpmnfmt lints BPMN process models and rewrites their diagram
// section (BPMN DI) with a canonical, readable layout — gofmt for BPMN.
//
// This package is the stable embedding API; the implementation lives in
// internal packages and may change freely. Typical use in a CLI:
//
//	src, _ := os.ReadFile("flow.bpmn")
//	res, err := bpmnfmt.Format(src, "flow.bpmn", bpmnfmt.Options{})
//	if err != nil { ... }
//	for _, f := range res.Findings {
//		fmt.Fprintf(os.Stderr, "%s: %s %s: %s\n", "flow.bpmn", f.Severity, f.Rule, f.Message)
//	}
//	if res.Formatted {
//		os.WriteFile("flow.bpmn", res.Output, 0o644)
//	}
//
// Everything outside the <bpmndi:BPMNDiagram> element is preserved byte for
// byte; the output is deterministic and idempotent.
package bpmnfmt

import (
	"github.com/pyck-ai/bpmnfmt/internal/format"
	"github.com/pyck-ai/bpmnfmt/internal/lint"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// Severity of a lint finding.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	default:
		return "info"
	}
}

// ParseSeverity converts "error", "warning" or "info".
func ParseSeverity(s string) (Severity, error) {
	v, err := lint.ParseSeverity(s)
	return Severity(v), err
}

// Finding is one lint result.
type Finding struct {
	Rule     string   `json:"rule"`
	Severity Severity `json:"-"`
	Sev      string   `json:"severity"`
	Element  string   `json:"element,omitempty"`
	Message  string   `json:"message"`
}

// HasErrors reports whether any finding is an error.
func HasErrors(fs []Finding) bool {
	for _, f := range fs {
		if f.Severity == SeverityError {
			return true
		}
	}
	return false
}

// MaxSeverity returns the highest severity present; ok is false when fs is
// empty.
func MaxSeverity(fs []Finding) (max Severity, ok bool) {
	for _, f := range fs {
		ok = true
		if f.Severity > max {
			max = f.Severity
		}
	}
	return max, ok
}

// Options controls formatting.
type Options struct {
	// Force lays out even when lint reports errors.
	Force bool
	// HappyEnd names the end event the happy path must reach.
	HappyEnd string
	// HappyFlows are preferred at gateway splits during spine selection.
	HappyFlows []string
}

// Result of formatting one document.
type Result struct {
	// Findings from the lint pass (always populated).
	Findings []Finding
	// Output is the complete reformatted document; nil when not formatted.
	Output []byte
	// Formatted is false when lint errors blocked the layout (see Force).
	Formatted bool
}

// Check lints a BPMN document. filename is used in messages only.
func Check(src []byte, filename string) ([]Finding, error) {
	f, err := model.Parse(src, filename)
	if err != nil {
		return nil, err
	}
	return convert(lint.Check(f)), nil
}

// Format lints and reformats a BPMN document. filename is used in messages
// only. A nil error with Formatted == false means lint errors blocked the
// layout; inspect Findings.
func Format(src []byte, filename string, opts Options) (*Result, error) {
	f, err := model.Parse(src, filename)
	if err != nil {
		return nil, err
	}
	fopts := format.Options{Force: opts.Force, HappyEnd: opts.HappyEnd, HappyFlows: map[string]bool{}}
	for _, id := range opts.HappyFlows {
		fopts.HappyFlows[id] = true
	}
	res, err := format.File(f, fopts)
	if err != nil {
		return nil, err
	}
	return &Result{
		Findings:  convert(res.Findings),
		Output:    res.Output,
		Formatted: res.Formatted,
	}, nil
}

func convert(fs []lint.Finding) []Finding {
	out := make([]Finding, len(fs))
	for i, f := range fs {
		out[i] = Finding{
			Rule:     f.Rule,
			Severity: Severity(f.Severity),
			Sev:      f.Sev,
			Element:  f.Element,
			Message:  f.Message,
		}
	}
	return out
}
