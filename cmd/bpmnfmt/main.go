// Command bpmnfmt lints BPMN files and rewrites their diagram section with a
// canonical, readable layout — gofmt for BPMN.
//
// Usage:
//
//	bpmnfmt [flags] [files...]
//
// Without flags the formatted file is printed to stdout (single file or
// stdin). Everything outside the <bpmndi:BPMNDiagram> block is preserved
// byte for byte.
//
//	-w            write result back to the source files
//	-l            list files whose formatting differs
//	-d            print unified diffs instead of full output
//	-check        lint only, print findings, write nothing
//	-json         print findings as JSON
//	-fail-on S    severity threshold for exit code 1 (error|warning|info)
//	-force        format even when lint reports errors
//	-happy-end ID     end event the happy path must reach
//	-happy-flow IDs   comma-separated sequence flows preferred at splits
//
// Exit codes: 0 clean, 1 findings above threshold or differences found,
// 2 usage or parse errors.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/pyck-ai/bpmnfmt/internal/format"
	"github.com/pyck-ai/bpmnfmt/internal/lint"
	"github.com/pyck-ai/bpmnfmt/internal/model"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

type jsonFinding struct {
	File string `json:"file"`
	lint.Finding
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bpmnfmt", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		write     = fs.Bool("w", false, "write result back to the source files")
		list      = fs.Bool("l", false, "list files whose formatting differs")
		diff      = fs.Bool("d", false, "print unified diffs instead of full output")
		check     = fs.Bool("check", false, "lint only, write nothing")
		asJSON    = fs.Bool("json", false, "print findings as JSON")
		failOn    = fs.String("fail-on", "error", "severity threshold for exit code 1 (error|warning|info)")
		force     = fs.Bool("force", false, "format even when lint reports errors")
		happyEnd  = fs.String("happy-end", "", "end event ID the happy path must reach")
		happyFlow = fs.String("happy-flow", "", "comma-separated sequence flow IDs preferred at splits")
		showVer   = fs.Bool("version", false, "print version and exit")
	)
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: bpmnfmt [flags] [files...]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVer {
		_, _ = fmt.Fprintln(stdout, "bpmnfmt", version)
		return 0
	}
	threshold, err := lint.ParseSeverity(*failOn)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "bpmnfmt:", err)
		return 2
	}
	opts := format.Options{Force: *force, HappyEnd: *happyEnd, HappyFlows: map[string]bool{}}
	for _, id := range strings.Split(*happyFlow, ",") {
		if id = strings.TrimSpace(id); id != "" {
			opts.HappyFlows[id] = true
		}
	}

	files := fs.Args()
	useStdin := len(files) == 0
	if useStdin && *write {
		_, _ = fmt.Fprintln(stderr, "bpmnfmt: -w requires file arguments")
		return 2
	}

	exit := 0
	raise := func(code int) {
		if code > exit {
			exit = code
		}
	}
	var allFindings []jsonFinding

	process := func(path string, f *model.File) {
		var findings []lint.Finding
		var res *format.Result
		if *check {
			findings = lint.Check(f)
		} else {
			var err error
			res, err = format.File(f, opts)
			if err != nil {
				_, _ = fmt.Fprintf(stderr, "bpmnfmt: %v\n", err)
				raise(2)
				return
			}
			findings = res.Findings
		}

		for _, fd := range findings {
			allFindings = append(allFindings, jsonFinding{File: path, Finding: fd})
			if !*asJSON {
				elem := fd.Element
				if elem != "" {
					elem = " " + elem
				}
				_, _ = fmt.Fprintf(stderr, "%s: %s %s%s: %s\n", path, fd.Sev, fd.Rule, elem, fd.Message)
			}
		}
		if max, any := lint.MaxSeverity(findings); any && max >= threshold {
			raise(1)
		}
		if *check {
			return
		}
		if !res.Formatted {
			_, _ = fmt.Fprintf(stderr, "%s: not formatted (lint errors; use -force to override)\n", path)
			raise(1)
			return
		}

		changed := !bytes.Equal(f.Raw, res.Output)
		switch {
		case *list:
			if changed {
				_, _ = fmt.Fprintln(stdout, path)
				raise(1)
			}
		case *diff:
			if changed {
				_, _ = fmt.Fprint(stdout, unifiedDiff(path, f.Raw, res.Output))
				raise(1)
			}
		case *write:
			if changed {
				if err := os.WriteFile(path, res.Output, 0o644); err != nil {
					_, _ = fmt.Fprintf(stderr, "bpmnfmt: %v\n", err)
					raise(2)
				}
			}
		default:
			if _, err := stdout.Write(res.Output); err != nil {
				raise(2)
			}
		}
	}

	if useStdin {
		raw, err := io.ReadAll(stdin)
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "bpmnfmt:", err)
			return 2
		}
		f, err := model.Parse(raw, "<stdin>")
		if err != nil {
			_, _ = fmt.Fprintln(stderr, "bpmnfmt:", err)
			return 2
		}
		process("<stdin>", f)
	} else {
		for _, path := range files {
			f, err := model.ParseFile(path)
			if err != nil {
				_, _ = fmt.Fprintln(stderr, "bpmnfmt:", err)
				raise(2)
				continue
			}
			process(path, f)
		}
	}

	if *asJSON {
		if allFindings == nil {
			allFindings = []jsonFinding{}
		}
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(allFindings); err != nil {
			return 2
		}
	}
	return exit
}
