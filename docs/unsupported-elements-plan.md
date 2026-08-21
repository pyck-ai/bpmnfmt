# Plan: supporting more BPMN elements in bpmnfmt

Status: **draft for review**
Author: agent (Opus) for Max
Scope: turn the 15 currently-rejected BPMN element classes into either
fully-supported, partially-supported, or explicitly-rejected-with-clean-lint
elements, in priority order.

---

## 1. Where we are today

### 1.1 The current model

`internal/model/parse.go` recognises seven `NodeKind` values:

```
KindStartEvent KindEndEvent
KindIntermediateCatchEvent KindIntermediateThrowEvent
KindTask           (every *task variant collapses to this)
KindExclusiveGateway
KindUnknown        (reserved)
```

Anything else inside `<bpmn:process>` either becomes a `*model.Unsupported`
record (`unsupportedTags`, 14 entries) or is silently dropped (`default`
branch in `parse.go:218`). At the definitions level only `<collaboration>`
is recognised as "unsupported but seen".

### 1.2 The layouter's assumptions

`internal/layout` is built around three shape primitives only
(`layout.go:140-150`):

```go
case n.Kind.IsEvent():    return EventS, EventS    // 36×36 circle
case n.Kind.IsGateway():  return GatewayS, GatewayS // 50×50 diamond
default:                  return TaskW, TaskH      // 100×80 rectangle
```

Everything downstream — chain decomposition, X assignment, row assignment,
edge routing, label placement — assumes those three sizes and that every
flow node sits on exactly one row in a single coordinate system.

### 1.3 The DI emitter's assumptions

`internal/di/emit.go` emits:

* one `BPMNShape` per `p.Nodes[i]`, one per `p.Annotations[i]`,
* one `BPMNEdge` per `p.Flows[i]`, one per `p.Associations[i]`,
* `isMarkerVisible="true"` only on gateways.

It knows nothing about pools, lanes, message flows, data inputs/outputs,
groups, or nested planes.

### 1.4 Lint coverage

`internal/lint/lint.go` rule `E7` (`fileRules`) currently maps every
`model.Unsupported` to the message *"unsupported construct …: bpmnfmt
cannot lay out collaborations/pools"*. This is the only piece of code
gating the 28 crashing files in our test corpus.

---

## 2. Real-world frequency (134 files in `test-local/`)

| Bucket          | Tag                    | Files | Elements |
| --------------- | ---------------------- | ----: | -------: |
| **EASY**        | parallelGateway        |    15 |       26 |
|                 | inclusiveGateway       |     5 |        5 |
|                 | complexGateway         |     3 |        3 |
|                 | eventBasedGateway      |     3 |        3 |
| **MEDIUM**      | dataObject             |     4 |        6 |
|                 | dataObjectReference    |     7 |       12 |
|                 | dataStoreReference     |     9 |        9 |
|                 | group                  |    18 |       30 |
| **HARD**        | subProcess             |    22 |      106 |
|                 | adHocSubProcess        |     3 |        3 |
|                 | callActivity           |     4 |        8 |
|                 | boundaryEvent          |    23 |       73 |
|                 | transaction            |     0 |        0 |
| **VERY HARD**   | laneSet                |     6 |       19 |
|                 | collaboration          |    25 |       25 |

(`collaboration` 25 / `laneSet` 6 are mostly Camunda Modeler exports that
wrap a single process in a single-participant pool with no real
multi-pool semantics; see §6.)

---

## 3. Strategy summary

I propose a layered rollout with a clean stopping point after every layer
so we can pause and ship at any time:

```
Phase 1  Gateway variants            ← graph-identical, marker-only          ~ 1h
Phase 2  Visual-only artifacts       ← data*, group                          ~ 4h
Phase 3  callActivity                ← marker-only on tasks                  ~ 30m
Phase 4  boundaryEvent               ← interrupting/non-interrupting docking ~ 6h
Phase 5  subProcess (collapsed only) ← treat like a fancy task               ~ 4h
Phase 6  subProcess (expanded)       ← nested plane layouter                 ~ 1-2 days
Phase 7  laneSet                     ← horizontal bands inside a process     ~ 1 day
Phase 8  collaboration / pools       ← multi-process canvas + message flows  ~ 2-3 days
```

Each phase ends with: `go test ./...` green + the visual-comparison HTML
showing N new files cleanly formatted with no regressions on the
currently-supported set.

The cumulative-coverage hit-rate per phase against our 134-file corpus:

| After phase | Crashes left | Files newly formattable* |
| ----------- | -----------: | -----------------------: |
| (today)     | 28           | —                        |
| 1           | ~16          | ~12                      |
| 2           | ~16          | +5 (visual only)         |
| 3           | ~16          | +2                       |
| 4           | ~16          | +20 (boundary often co-occurs with sub-process) |
| 5           | ~10          | +6                       |
| 6           | 0            | +16                      |
| 7           | 0            | +5                       |
| 8           | 0            | +22                      |

`*` numbers are estimates from element counts; some files contain multiple
unsupported elements so the diagonals are fuzzy.

---

## 4. Phase 1 — Gateway variants (EASY)

### 4.1 Why easy

From a flow-graph perspective an inclusive, parallel, complex, or
event-based gateway is **identical** to an exclusive one: a node with
incoming and outgoing sequence flows. The only difference is the marker
inside the 50×50 diamond:

| BPMN element        | Marker (bpmn-js) |
| ------------------- | ---------------- |
| `exclusiveGateway`  | nothing / × (only when `isMarkerVisible="true"`) |
| `parallelGateway`   | `+` (cross)      |
| `inclusiveGateway`  | `○` (circle)     |
| `complexGateway`    | `*` (asterisk)   |
| `eventBasedGateway` | pentagon         |

That marker is set by the modeler in two places:

1. The BPMN element name itself (`<bpmn:parallelGateway>`).
2. `isMarkerVisible="true"` on the `BPMNShape` — only meaningful for
   exclusive (and historically also for inclusive).

The current code unconditionally adds `isMarkerVisible="true"` for
gateways (`di/emit.go:45`). For exclusive that produces the ×, which is
the Camunda Modeler default. For the other variants this attribute is
either ignored or visually neutral.

### 4.2 Design

#### Model layer

Add four new kinds (next to `KindExclusiveGateway`):

```go
KindParallelGateway
KindInclusiveGateway
KindComplexGateway
KindEventBasedGateway
```

Extend `NodeKind.IsGateway()` to cover all five. Extend `kindByTag` in
`parse.go` accordingly. Remove the four tags from `unsupportedTags`.

#### Lint layer

`gatewayRules` currently fires W2/W3/W4/I2 only for exclusive gateways.
We have a choice:

* **Option A (recommended):** apply the same rules to every gateway —
  unlabeled-branch warning, mixed-role warning, useless-gateway info.
  The rules read just as well for inclusive/parallel.
* **Option B:** keep them exclusive-only and add new specialised rules
  later if we want them.

I'll do A. The `n.Tag` in the messages already gives the modeler the
correct element name.

For event-based gateways there is an extra invariant — every outgoing
flow must target an intermediate catch event. **Out of scope for phase
1; add a TODO with a separate rule placeholder.**

#### Layout layer

No change. `nodeSize` already returns 50×50 for "any gateway" via
`IsGateway()`.

#### DI emitter

* Keep emitting `isMarkerVisible="true"` for `exclusive` and
  `inclusive` (matches Camunda Modeler behaviour).
* Do NOT emit it for parallel/complex/event-based (matches modeler
  defaults; older bpmn-js versions misrendered the marker when the
  attribute was present).

### 4.3 Tests

* Unit: parse a one-of-each gateways fixture; assert kinds.
* Unit: golden snapshot for a process with each gateway variant.
* Integration: re-run `pyck bpmn fmt` against the 15+5+3+3 affected
  files; expect status to flip from `CRASH` to either `OK` or `LINT`
  (some have other unsupported elements too).

### 4.4 Risk

Negligible. The graph code already handles arbitrary gateway-shaped
nodes correctly via the kind-agnostic `g.Out` / `g.In`.

---

## 5. Phase 2 — Visual-only artifacts

This phase adds the four artifacts that **don't participate in the
sequence-flow graph** but need to be drawn and positioned:

* `dataObject` — semantic data definition, **no shape** in BPMN 2 DI
  (it's the `dataObjectReference` that gets a shape). Treat as parse-only.
* `dataObjectReference` — small page shape (`36×50`), connected to
  tasks/events via `dataInputAssociation` / `dataOutputAssociation`.
* `dataStoreReference` — cylinder shape (`50×50`), same association
  semantics.
* `group` — a styled rectangle that **encloses** other shapes; has
  bounds and an optional `categoryValueRef`. No edges.

### 5.1 New model concept: `Artifact`

Today we have `FlowNode`, `SequenceFlow`, `TextAnnotation`,
`Association`. Add a fifth bucket:

```go
type Artifact struct {
    Kind     ArtifactKind  // data, datastore, group
    Tag      string
    ID       string
    Name     string
    DocIndex int

    // For data/dataStore: connection points populated from associations.
    // For group: nodes contained (computed from final layout, not parsed).
    Bounds *Rect           // explicit modeler-provided bounds, nil if none
}
```

Plus extend `Process.AnnByID` style indices, and a new association kind
`dataInputAssociation` / `dataOutputAssociation` — these live on **flow
nodes**, not at the process level. Two design options:

* **(a)** treat data-input/output-assoc as ordinary `Association` with
  a kind discriminator. Simpler. Recommended.
* **(b)** model them strictly per BPMN spec (children of activities,
  referencing `dataObjectReference` IDs). More faithful but no
  layout benefit.

I pick (a).

### 5.2 Layout impact

* Data refs and data store refs need a horizontal lane *above* the row
  band of the activity they connect to (mirror of the annotation band
  that already exists). Smallest disruption: add `dataBandH[]` next to
  `annBandH[]` in `compLayout`.
* Groups are computed **last**: after every shape has its final bounds,
  for each group, take the union of bounds of its `categoryValueRef` or
  `bpmnElement` targets and inflate by a margin. Groups should respect
  the modeler's *intent* about which shapes they contain — we'll need
  some way to detect that from the original DI.

  Simplest workable heuristic for the first cut: keep the original
  group bounds **untouched** in the DI emitter. The layouter does
  nothing with them. This is wrong in spirit (groups won't follow nodes
  that moved) but produces a non-broken diagram.

  Better: phase 2.1 — recompute group bounds by intersecting which
  nodes the **original** group overlapped, then taking the union of
  those nodes' **new** bounds + padding.

### 5.3 Tests

* Parse fixtures with each artifact.
* Golden: a small process with one data object reference connected to a
  task; another with a group around two tasks.
* Visual: open in the comparison HTML, eyeball.

### 5.4 Risk

Medium for groups (band & layering), low for data refs (just one extra
shape per band).

---

## 6. Phase 3 — `callActivity`

A call activity is a task that delegates to another process. Visually
in bpmn-js it's a **thick-bordered rectangle** (same 100×80) with no
other markup difference from a regular task.

### 6.1 Design

* Add `KindCallActivity` (or treat as `KindTask` with a sub-flag —
  recommended for minimal disruption: introduce `FlowNode.SubKind`
  string that mirrors `Tag` for the cases where the marker matters).
* `nodeSize` returns `TaskW, TaskH` either way.
* DI emit needs no change; the thick border comes from the BPMN element
  type, not from a DI attribute.

### 6.2 Risk

Trivial.

---

## 7. Phase 4 — `boundaryEvent`

**This is where the project's data model first really bends.**

### 7.1 What a boundary event is

```xml
<bpmn:serviceTask id="T_pick">
  <bpmn:incoming>F1</bpmn:incoming>
  <bpmn:outgoing>F2</bpmn:outgoing>
</bpmn:serviceTask>

<bpmn:boundaryEvent id="E_timeout"
                    attachedToRef="T_pick"
                    cancelActivity="true">
  <bpmn:outgoing>F3</bpmn:outgoing>
  <bpmn:timerEventDefinition>…</bpmn:timerEventDefinition>
</bpmn:boundaryEvent>
```

A boundary event:

* is a node in the flow graph (it has outgoing sequence flows that
  drive an exception path);
* is **glued to** its parent activity, positioned by the modeler on
  one of its four edges with a docking point inside the rectangle;
* contributes to outgoing degree but **never has incoming** sequence
  flows (it's "incoming" is the activity's interrupt, not modelled).

### 7.2 What changes in our code

#### Model

* Add `KindBoundaryEvent`.
* Add `FlowNode.AttachedToRef string` (empty for non-boundary events).
* Add `FlowNode.CancelActivity bool` (interrupting vs non-interrupting,
  affects the rendered double border).
* Lint must skip the "no incoming" check (`E4`) for boundary events.
* Lint must verify `AttachedToRef` resolves to an activity in the same
  process (new rule, e.g. `E8`).

#### Graph

* Boundary events become roots in the spine algorithm sort of like start
  events but for the **exception path**: their out-edges seed a
  secondary spine that lives below the main spine, parallel to the
  parent activity's row.
* `buildComponents` treats the boundary event and the activity as part
  of the same weakly-connected component (already does via the
  outgoing flow, but we should add an explicit "boundary attaches to"
  edge so an event whose only outgoing reaches a different component
  doesn't split the diagram).

#### Layout

This is the meat of phase 4.

* When placing the row that contains an activity with N boundary
  events, reserve `N × LaneStep` of extra height **below** the row
  band (or to the side, depending on docking).
* Anchor each boundary event's center on a docking point computed from
  its bounds in the original DI (preserve the modeler's choice when
  possible), or default to bottom-center / bottom-right slots.
* The outgoing flow of a boundary event starts at the docking point,
  goes down `Clearance`, then routes through a fresh "exception
  channel" below the main row.

#### DI

* Emit `BPMNShape` with the computed bounds.
* Emit no extra attributes (cancelActivity is on the BPMN element).

### 7.3 Risk

High. The layouter's row model is currently flat. Boundary events
introduce *sub-row* positioning. Mitigation: in phase 4 we preserve the
modeler's boundary offsets verbatim from the original DI when they exist
and only synthesise sensible defaults when they don't. That makes the
phase ship without a full layout-engine rewrite.

---

## 8. Phase 5 — `subProcess` (collapsed)

A **collapsed** sub-process is rendered as a 100×80 rectangle with a
`[+]` marker. Visually it's a task; semantically it can contain nested
elements. As long as the modeler set `isExpanded="false"` on the
`BPMNShape`, none of the nested content is drawn.

### 8.1 Design

* Add `KindSubProcess`, `KindAdHocSubProcess`, `KindTransaction` (alias
  of SubProcess for now).
* In parse, when we hit `<bpmn:subProcess>`:
  * register the node;
  * **descend** into its children but tag them with `EnclosingID`. For
    phase 5 we record but **don't lay out** them; they appear in
    `Unsupported` with a more useful message ("sub-process content not
    yet laid out").
  * Look up the corresponding `BPMNShape` in the DI: if
    `isExpanded="false"` (or attribute absent), draw as a marker task
    and we're done. If `isExpanded="true"`, emit a lint warning that
    bpmnfmt will collapse it (or fail with a clear error, configurable).
* Emit `isExpanded="false"` on the DI shape.

### 8.2 Risk

Medium. Collapse changes how the file looks. Should be opt-in or at
least loudly logged.

---

## 9. Phase 6 — `subProcess` (expanded)

Now we lay out the nested process **inside** the parent rectangle.

### 9.1 The architectural change

Today `layoutComponent` lays out one flat list of nodes. For expanded
sub-processes we need:

```
type Plane struct {
    Owner   string      // process ID or sub-process ID
    Nodes   []*FlowNode
    Flows   []*SequenceFlow
    SubPlanes []*Plane  // child sub-processes
}
```

`layout.Compute` becomes recursive:

1. Lay out each sub-plane in isolation (in component-local coords).
2. The parent treats each sub-process as a node whose size is the
   bounding box of the laid-out child plus padding (`SubProcPad ≈ 30`).
3. After parent layout, shift child planes into place.

`graph.Build` must operate per plane: each sub-process is its own
graph; sequence flows must not cross plane boundaries (BPMN rule). The
`compLayout` struct gets a `parentOffset` so child edges can be merged
into the global coordinate space.

DI emits one `BPMNShape` per sub-process (with `isExpanded="true"`) and
emits its children's shapes inside the same `BPMNPlane`. In strict
BPMN this is the same plane — the spec allows separate planes per
sub-process but most exporters use a single plane. We'll match the
Camunda Modeler convention: single plane, all shapes flat, the visual
nesting comes from coordinates only.

### 9.2 Risk

High. This is a layout engine extension, not a tweak. Realistic
estimate: 1-2 days, with a real risk of subtle regressions on the
existing flat layouts. Must land behind a feature flag at first.

---

## 10. Phase 7 — `laneSet`

### 10.1 What lanes are

A lane set is a list of horizontal bands inside a single process. Each
`<bpmn:lane>` lists the IDs of `flowNodeRef`s belonging to it. The DI
emits one `BPMNShape` per lane plus a containing `participant`/`process`
shape that gives the surrounding box.

Layout-wise, lanes constrain the row assignment: every node must be in
the band of its declaring lane. The lane order in the source file is the
visual order top-to-bottom.

### 10.2 Design

* New `Lane` struct in the model (ID, Name, NodeIDs).
* `Process.Lanes []*Lane` populated by parser.
* In `layoutComponent`:
  * Group nodes by lane; assign every lane a vertical band.
  * Run the existing row assignment **inside** each band (rows clamp to
    the band's y-range).
  * Each lane gets a fixed minimum height; tall lanes expand to fit
    their tallest row.
  * The lane title sits on the left, rotated 90°. We don't need to
    render text rotation in our DI; bpmn-js does it from the lane
    bounds.
* DI emits lane shapes as bands.

### 10.3 Risk

High. Row assignment becomes constrained instead of free. The chain
decomposer (`internal/layout/chains.go`) may not gracefully accept lane
constraints — likely needs a new pass that swaps "row" for "lane row".

---

## 11. Phase 8 — `collaboration` / pools

### 11.1 What collaborations are

A `<bpmn:collaboration>` sits at the definitions level and lists
`<bpmn:participant>` elements. Each participant either references a
`processRef="…"` (a real process) or stands alone as a black-box pool.
Between participants, `<bpmn:messageFlow>` elements draw dashed edges.

This breaks several core assumptions:

* **Multiple processes per file.** Today lint rejects this hard
  (`E7: file contains N processes`).
* **Cross-pool edges.** Message flows connect nodes that live in
  different `Plane`s and have completely independent layouters.
* **Pools are top-level shapes** with the same title-bar convention
  as lanes.

### 11.2 Design

A collaboration becomes the **outer** plane:

```
collaboration
 ├─ pool A (participant) → owns process A's plane
 ├─ pool B (participant) → owns process B's plane
 ├─ message flow A1 → B1
 └─ message flow B2 → A2
```

* New `model.Collaboration` and `model.Participant` types.
* `model.File.Processes` stays but each `*Process` gets `Participant *Participant`.
* `layout.Compute` switches on `f.Collaboration != nil`:
  * Lay out each pool's process plane.
  * Stack pools vertically with `ComponentGap × 2`.
  * Width = max(pool widths) so the title bars align.
  * Route message flows after both planes have positions; use a
    relaxed orthogonal router with dashed style hint.
* DI emits one `BPMNShape` per pool, one `BPMNShape` per pool title
  band, all child shapes inside the same `BPMNPlane`, and one
  `BPMNEdge` per message flow.

### 11.3 What about the 25 collaboration files in our corpus

Looking at the actual files: most of them appear to be Camunda Modeler
single-pool exports — i.e. one `<participant>` referencing one
`<process>`. These can be supported by treating the pool as a thin
wrapper (just an outer rectangle with a title band) without any
message-flow logic.

Recommendation: split phase 8 into:

* **8a:** single-pool collaborations (treat as one process inside a
  decorative outer rectangle). Unblocks ~22 of 25 files. Small effort.
* **8b:** real multi-pool collaborations with message flows. Big
  effort; defer behind 8a.

### 11.4 Risk

8a: medium. 8b: very high. Both touch the lint, layout, and DI layers.

---

## 12. Cross-cutting concerns

### 12.1 Backward compatibility

Every phase changes the **output** DI for files that currently format
as OK. We should:

* Snapshot the current golden output (`internal/format/golden_test.go`)
  and treat any diff in those files as a regression.
* Run the full visual-comparison HTML before merging each phase and
  visually diff a sampled subset.

### 12.2 Lint rule numbering

I propose reserving:

| Rule | Meaning                                         | Phase |
| ---- | ----------------------------------------------- | ----- |
| E8   | boundary event references missing activity     | 4     |
| E9   | sub-process contains element bpmnfmt can't lay | 5/6   |
| E10  | event-based gateway target is not a catch event | (later) |
| W8   | sub-process is expanded; bpmnfmt collapsed it   | 5     |
| W9   | message flow crosses pools but target is unknown | 8b    |

### 12.3 Feature flags

Phases 6, 7, 8 should ship behind explicit CLI flags (`--lay-out-subprocesses`,
`--lay-out-lanes`, `--lay-out-pools`) so we can land them dark and
flip them on when stable. Phase 1-5 can be unconditional because they
already produce sane output even if disabled.

### 12.4 Testing infrastructure

We already have `internal/format/golden_test.go`. Extend it with:

* `testdata/golden/phase1-gateways/` etc. — one canonical
  fixture per phase.
* A "regression sweep" that runs every file in `test-local/original/`
  through `bpmnfmt -check` and asserts the exit code matches a recorded
  table (so adding a new feature doesn't silently change behaviour for
  files that were `LINT` before).

### 12.5 Backporting the crash fix

The crash fix in `internal/graph/graph.go` (already merged in this
branch) stays valid for every phase: even after we add support for
parallel gateways, there will still be files with `<inclusiveGateway>`
inside a `<subProcess>` until phase 6 ships, etc. The defensive filter
is a permanent invariant, not a stopgap.

---

## 13. Open questions for Max

1. **Group recomputation.** When a `<bpmn:group>` enclosed three tasks
   and bpmnfmt moves those tasks, do you want:
   (a) the group to follow them (recompute bounds from members),
   (b) the group to stay where the modeler put it (keep original bounds),
   (c) bpmnfmt to ignore groups entirely (preserve them as a passthrough)?
   Recommendation: **(a)** with a flag to disable.

2. **Sub-process default behaviour.** When the modeler left a
   sub-process **expanded**, should phase 5 collapse it (loud warning)
   or hard-fail until phase 6 lands?
   Recommendation: **hard-fail until phase 6**, the collapse loses
   information.

3. **Lane assignment when a node has no lane.** A flow node in a process
   with lanes but not listed under any `<bpmn:lane>` is technically a
   modeling bug. Lint as `E?` error, or silently put it in a synthetic
   "(unassigned)" lane?
   Recommendation: lint as error.

4. **Multi-pool layout.** Are real multi-pool diagrams in scope for
   pyck workflows, or is it only the Camunda single-pool export
   convention you care about? If it's the latter, phase 8b can be
   deferred indefinitely.

5. **`callActivity` semantics.** Currently we'd render it as a
   thick-bordered task. Do you want bpmnfmt to also follow the
   `calledElement` link and lint that the referenced process exists
   somewhere (across files)? That's a much bigger feature (file-system
   awareness). Recommendation: out of scope.

6. **Failure mode preference.** Today `pyck bpmn fmt` reports lint
   errors and refuses to format. For files that contain `parallelGateway`
   etc. **after** phase 1, the file becomes formattable. But before
   each phase ships, what's the preferred behaviour: a clean lint
   error ("element X not yet supported, file untouched") or a hard
   crash ("file blew up the tool")? We already fixed the crashes; this
   question is about future unsupported additions.
   Recommendation: clean lint error, always.

---

## 14. Suggested order of operations after Max signs off

1. Approve / reject phases and answer the open questions in §13.
2. Land phase 1 in a single PR, including the regression sweep test.
3. Re-run the visual-comparison HTML; eyeball 5-10 sampled files;
   sign off.
4. Repeat for phases 2 → 8a.
5. Phase 6/7/8b are separate efforts each.

Stopping after phase 5 gives 22 of 28 crashes fixed plus visual support
for half the corpus, with no architectural rewrite. That's a defensible
"good for now" checkpoint.
