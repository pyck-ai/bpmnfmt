# bpmnfmt

gofmt for BPMN. Lints process models for logical errors and rewrites the
diagram section (BPMN DI) with a canonical, human-readable layout. The
process logic is never touched — only how it looks.

**Before** — as modeled by hand:

![tour-creation before formatting](docs/img/tour-creation-before.png)

**After** — `bpmnfmt -w tour-creation.bpmn`:

![tour-creation after formatting](docs/img/tour-creation-after.png)

One straight line of work, left to right. The batch loop reads forward on
the spine, its way-back line runs below, the exit rises to the end event.
Deterministic and idempotent: the same model always renders the same
diagram, and formatting a formatted file changes nothing.

## Install

```
go install github.com/pyck-ai/bpmnfmt/cmd/bpmnfmt@latest
```

## Usage

```
bpmnfmt file.bpmn            # formatted file to stdout (or stdin -> stdout)
bpmnfmt -w *.bpmn            # rewrite files in place
bpmnfmt -l *.bpmn            # list files whose layout differs (CI)
bpmnfmt -d file.bpmn         # unified diff
bpmnfmt -check *.bpmn        # lint only, write nothing
```

Flags:

| Flag | Effect |
|------|--------|
| `-json` | print findings as JSON |
| `-fail-on error\|warning\|info` | severity threshold for exit code 1 (default `error`) |
| `-force` | format even when lint reports errors |
| `-happy-end ID` | end event the happy path must reach |
| `-happy-flow ID,ID` | sequence flows preferred at gateway splits |
| `-version` | print version and exit |

Exit codes: `0` clean · `1` findings above threshold / differences found ·
`2` usage or parse error.

## What it guarantees

Only the `<bpmndi:BPMNDiagram>` element is replaced. Every other byte —
XML comments, attribute order, documentation, whitespace — survives
untouched. Shape colors (`bioc:`/`color:`) are carried over. Output is
deterministic and idempotent.

The layouter validates its own output on every run: no overlapping
shapes, no edge through a shape, no leftward forward flows, a straight
spine, and zero forbidden edge crossings.

## Layout rules

1. **Happy path on one row, left to right.** The spine is chosen per
   split, in order of precedence: flows named by `-happy-flow` /
   `-happy-end`; the branch that feeds a loop back to this gateway (loops
   read forward, see rule 3); the branch that reaches an end event; the
   first-declared flow. Overrides apply to the top-level process only —
   they are silently ignored inside expanded sub-processes. Off the spine,
   a chain continues into the successor that links back into already-placed
   work, so cross-linked nodes stay on an adjacent row instead of being
   buried and routed around the diagram.
2. **Grid.** Node centers snap to a 160px column grid and shared row
   centerlines — elements line up horizontally and vertically. Labels
   reserve their own room in that spacing: a named event or gateway
   reserves its label width, and a named sequence flow reserves its label
   width in the gap between the two nodes it connects, so a flow label
   never laps into the shape it points at. A long label can push the next
   node a column further right.
3. **Loops read forward.** At a gateway that a loop returns to, the loop
   body *is* the main line: it continues straight through the diamond, the
   way-back line returns below, and the loop's exit rises out of the top
   corner. The work stays on one row; leaving the loop is the detour.
   A branch whose only exit is a back edge upstream on its own chain, with
   no sub-branches, is laid out right to left instead — head under the
   split, tail under the loop target — provided marching left at natural
   spacing fills that span exactly. Such a branch is its own way-back line.
4. **Branches use the gateway's corners.** A spine gateway with exactly
   three outgoing flows uses all three corners of the diamond: the happy
   path runs straight through, the shorter alternate leaves the top
   corner, the longer the bottom (a branch that loops back never goes up).
   With four or more outgoing flows the top corner carries no alternate and
   every alternate hangs below, stacked longest-first (a skip arc or a
   way-back may still use that corner, see rule 6). Entry edges drop in the
   gateway's own column and turn once into the branch head's left side;
   branch heads align with the column of the first non-gateway node after
   the split, so a run of consecutive gateways shares one branch-head
   column. Gateways inside a branch always stack their alternates below.
   A branch is routed above the spine only when its whole subtree is
   terminal — one that re-merges downstream hangs below, however short.
5. **Rejoins turn in the target's column.** Branch tails whose last node is
   a gateway or event align under their merge target and rise vertically
   into its bottom edge. An activity tail stops one column short, leaves to
   the right, and turns up in the target's column — a rectangle is left
   through its right border, never its top. Secondary-start inflows rejoin
   the same way.
6. **Detour lines.** A back edge leaves its source, runs on a dedicated
   line clear of the rows, and enters its target. When source and target
   sit on DIFFERENT rows that line runs below the lower of the two — the
   edge has to travel between the rows anyway. When they sit on the SAME
   row the line arches ABOVE it if the sky over the spanned columns is
   free, and runs below otherwise; a forward skip arc over intervening
   nodes takes the same shape. Crossings are allowed on these lines (and
   only there); multiple lines stack, wider ones nesting outside narrower
   ones — below the row that means further down, above it further up. A
   way-back whose source sits directly below its target in the same column
   degenerates to a straight rise (rule 3). Detours that share a target
   share one lane and enter as a single arrow, so several arcs read as one
   line growing as each joins it.
7. **Orthogonal edges, few bends.** Dockings spread out per node side, and
   edges arriving at a gateway land on the diamond's slanted face at their
   own offset — arrowheads never pile onto one point. A vertical corridor
   is reserved per column *and* row band, so two verticals may share a
   column when the rows they cross do not overlap.
8. **Annotations.** Short notes sit in a band directly above their anchor;
   prose notes are parked above or below the diagram with a short, clear
   association line.
9. **Bundles arrive as one riser.** Lines entering the same node face merge
   into a single riser in the target's column even when they run at
   different depths: each shallower one joins the deepest one's riser, so
   several arcs read as one line growing as each joins it, ending in one
   arrowhead. This covers way-back returns whose lines lie in different
   gaps and forward rejoins whose runs lie on different rows — the latter
   share one riser instead of stepping apart by rule 5's depth order,
   which only matters while they are separate lines. Returns and rejoins
   bundle separately. A line whose target-column riser is blocked by a
   shape keeps its own riser and its own arrowhead, and on a diamond the
   merged entry lands on the bottom vertex rather than the slanted face.
10. **The sky clears the notes.** A short-annotation band never blocks a
    same-row detour: sky lanes stack above the band, and a note sitting in
    a riser's column dodges sideways. Only chains on higher rows and
    occupied riser columns keep a detour below its row (rule 6).
11. **Loop-return detours lift.** A branch with no forward exit — every
    path through it ends in a way-back edge to a spine node earlier than
    its split — exists only to return. When rule 3's backwards walk cannot
    fill the span exactly, the branch is lifted above the spine and returns
    through the sky into its target's top: below-spine gaps belong to
    forward branches and their rejoins; the sky belongs to returns. The
    lifted body reads RIGHT TO LEFT, mirroring rule 3 without its fill
    condition: the head sits one column left of the split (the entry rises
    from the gateway's top into the head's right face), the body marches
    leftward, and the return leaves the tail's left face and drops once
    into the target's top.

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

`Options` mirrors the CLI: `Force`, `HappyEnd`, `HappyFlows`. `Result`
carries `Findings` (always populated), `Output`, and `Formatted`.

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
go test ./...                                       # layout invariants + goldens
go test ./internal/format -run TestGolden -update   # refresh golden files
python3 -m http.server 8077                         # then open hack/viewer/?f=testdata/x.bpmn
```

The files under `testdata/` are anonymized real-world fixtures;
`testdata/golden/` pins their formatted output byte for byte. Every layout
rule above has a discriminating fixture that fails if the rule regresses.
