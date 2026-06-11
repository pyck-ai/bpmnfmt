package bpmnfmt

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPublicAPI(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("testdata", "tour-execution.bpmn"))
	if err != nil {
		t.Fatal(err)
	}

	findings, err := Check(src, "tour-execution.bpmn")
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) == 0 {
		t.Error("expected lint findings on the fixture")
	}
	if HasErrors(findings) {
		t.Errorf("unexpected errors: %+v", findings)
	}
	if max, ok := MaxSeverity(findings); !ok || max != SeverityWarning {
		t.Errorf("max severity = %v, %v; want warning, true", max, ok)
	}

	res, err := Format(src, "tour-execution.bpmn", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Formatted || len(res.Output) == 0 {
		t.Fatalf("not formatted: %+v", res.Findings)
	}

	// Idempotency through the public API.
	res2, err := Format(res.Output, "tour-execution.bpmn", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(res2.Output, res.Output) {
		t.Error("public Format is not idempotent")
	}

	// Severity helpers.
	if s, err := ParseSeverity("warning"); err != nil || s != SeverityWarning {
		t.Errorf("ParseSeverity = %v, %v", s, err)
	}
	if SeverityError.String() != "error" {
		t.Errorf("String() = %s", SeverityError)
	}
}
