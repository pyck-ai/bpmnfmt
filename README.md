# bpmnfmt

gofmt for BPMN: lints process models for logical errors and rewrites the
diagram section (BPMN DI) with a canonical, human-readable layout.

```
go install github.com/pyck-ai/bpmnfmt/cmd/bpmnfmt@latest
```

## Usage

```
bpmnfmt file.bpmn            # formatted file to stdout (or stdin -> stdout)
bpmnfmt -w *.bpmn            # rewrite files in place
bpmnfmt -l *.bpmn            # list files whose layout differs (CI)
bpmnfmt -d file.bpmn         # unified diff
bpmnfmt -check *.bpmn        # lint only
   -json                     # findings as JSON
   -fail-on error|warning|info
   -force                    # format despite lint errors
   -happy-end ID             # end event the happy path must reach
   -happy-flow ID,ID         # flows preferred at gateway splits
```

Exit codes: `0` clean · `1` findings above threshold / differences found ·
`2` usage or parse error.

## Use as a library

The module root exposes a stable embedding API (e.g. for a `pyck bpmn fmt`
subcommand); the `internal/` packages are implementation detail.

```go
import "github.com/pyck-ai/bpmnfmt"

src, _ := os.ReadFile("flow.bpmn")

findings, err := bpmnfmt.Check(src, "flow.bpmn")          // lint only

res, err := bpmnfmt.Format(src, "flow.bpmn", bpmnfmt.Options{})
if res.Formatted {                                        // false on lint errors
    _ = os.WriteFile("flow.bpmn", res.Output, 0o644)
}
```

## What it guarantees

Only the `<bpmndi:BPMNDiagram>` element is replaced. Every other byte —
XML comments, attribute order, documentation, whitespace — survives
untouched. Shape colors (`bioc:`/`color:`) are carried over. Output is
deterministic and idempotent.

## Layout rules

1. **Happy path on one row, left to right.** Selected by following the
   first-declared forward flow at each split (backtracking to reach an end
   event); override with `-happy-end` / `-happy-flow`.
2. **Grid.** Node centers snap to a 160px column grid and shared row
   centerlines — elements line up horizontally and vertically.
3. **Branches hang below** the gateway in tiers; disjoint branches may
   share a tier; nested branches stack deeper.
4. **Straight rejoins.** Branch tails align under their merge target and
   rise vertically into its bottom edge.
5. **Loops in channels.** Back edges travel above the spine or under their
   own tier through node-free corridors — never through shapes. Chains that
   loop far back are placed below the rows their lane would sweep across.
6. **Orthogonal edges**, few bends, dockings spread per node side.
7. **Annotations**: short notes sit in a band directly above their anchor;
   prose notes are parked above or below the diagram with a short, clear
   association line.

The layouter validates its own output: no overlaps, no edge through a
shape, no leftward forward flows, straight spine, zero edge crossings on
the test corpus.

## Lint rules

| Rule | Severity | Meaning |
|------|----------|---------|
| E1 | error | dangling refs / inconsistent incoming-outgoing declarations |
| E2 | error | duplicate IDs |
| E3 | error | start event with incoming / end event with outgoing flow |
| E4 | error | node not wired in (missing incoming or outgoing flow) |
| E5 | error | unreachable from start / no path to an end event |
| E7 | error | unsupported construct (pools, lanes, collapsed/nested subprocesses, boundary events, parallel gateways, multiple processes) |
| W1 | warning | implicit split: activity with >1 outgoing flow |
| W2 | warning | unlabeled branch out of a decision gateway |
| W3 | warning | unnamed decision gateway |
| W4 | warning | gateway that both merges and splits |
| W5 | warning | disconnected flows in one process |
| W6/W7 | warning | missing / orphaned diagram elements |
| I1–I3 | info | implicit merge, no-op gateway, multiple start events |

Lint errors block formatting (`-force` overrides).

## Scope

Targets straight-through process models: events (plain, timer, signal,
message), tasks of all types, exclusive gateways, text annotations, and
**expanded embedded sub-processes** — the interior is laid out recursively
inside the container rectangle (one level deep; multi-instance markers are
preserved). Collapsed sub-processes, sub-processes nested more than one
level, collaborations (pools/lanes), boundary events and parallel/inclusive
gateways are detected and rejected with E7.

## Development

```
go test ./...                                  # includes layout invariants + goldens
go test ./internal/format -run TestGolden -update   # refresh golden files
python3 -m http.server 8077                    # then open hack/viewer/?f=testdata/x.bpmn
```

The files under `testdata/` are anonymized real-world fixtures;
`testdata/golden/` pins their formatted output byte for byte.
