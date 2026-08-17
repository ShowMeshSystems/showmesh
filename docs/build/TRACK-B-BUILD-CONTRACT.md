# Track B build contract: the render node

[Track B](TRACK-B-nodes-and-projection.md) · [Session handoff](TRACK-B-SESSION-HANDOFF.md) · [Identifier register](IDENTIFIER-REGISTER.md) · [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) · [ADR-007](../decisions/ADR-007-gstreamer-media-engine.md)

Written 2026-08-17 by the Track B orchestration session. This is the
implementation contract the seam builders work from. It does not replace the
track document, which remains the specification; it says how the four
remaining seams are cut, what each may and may not touch, and which questions
were ruled on rather than left open.

**This file is committed.** Track D's `TRACK-D-D3A-BUILD-CONTRACT.md` was
cited 88 times across 16 files and existed only inside a worktree.

## Standing rules for every seam in this track

1. **The coordinator is never in the timing or media path.** The node renders;
   the coordinator watches. A render node whose coordinator has gone away
   keeps rendering. A render node whose broker has gone away keeps rendering.
   If any seam produces code where a frame's timing depends on a coordinator
   response, that seam is wrong.
2. **Timing arrives over MultiSync and only over MultiSync** (ADR-008). Nothing
   in this track may derive a frame position from an MQTT message, an API
   response, or a wall clock.
3. **Stale is `unknown`, never healthy** (ADR-011). `ObservedAt` is a pointer
   and `nil` means genuinely unknown. Never default it to collection time.
4. **Absent evidence is stated, never omitted** (ADR-020). A pipeline whose
   state cannot be determined reports a state and a reason.
5. **Every capability gets a `showmeshctl` verb in the seam that adds it**, and
   a UI control where a UI control is possible (ADR-030, ADR-039, owner
   2026-08-17). The CLI is the emergency surface and carries full control. UI
   is make-it-work, not make-it-pretty.
6. **Builders never edit `docs/build/IDENTIFIER-REGISTER.md`, `BUILD-LOG.md`,
   or `BUILD-PLAN.md`**, and never author or accept an ADR. Request
   identifiers from the orchestrator.
7. **The live fleet is untouchable.** No write, no command, no restart, no
   settings change, and no MQTT publish on any `falcon/` topic.
   `deploy/.env.live-fleet-run.bak` must never be combined with a
   write-capable stack.
8. **Gates are run, not claimed.** `make check` and `make test-integration`
   actually execute, and the output is captured. A test whose name is a claim
   gets its behaviour broken once to confirm the test fails.

## Rulings made by this session

The AFK instruction is to decide and log rather than block. Each of these is
recorded here and in `docs/private/DECISION-QUEUE.md` for the owner to
overturn. Nothing here accepts an ADR.

### Ruling 1: GStreamer runs as a supervised child process, not through CGo bindings

The agent builds a pipeline description and runs `gst-launch-1.0` as a child
process, supervising it by process lifecycle, its bus output, and its own
liveness. It does **not** link `go-gst` or the GStreamer C libraries.

Three reasons, in order of weight.

**It is what ADR-007 actually says.** "The node agent constructs, supervises,
and monitors GStreamer pipelines" and "ShowMesh must not implement codecs or
per-frame rendering in application code." Process supervision is the purest
available reading of that sentence, and the in-process binding is the reading
that puts ShowMesh code inside the media library's address space.

**It keeps every binary CGo-free.** `Makefile:21` builds the agent with
`CGO_ENABLED=0`. Linking GStreamer means relaxing that for the agent, which
means the agent stops cross-compiling cleanly and starts depending on a
GStreamer development environment at build time on every platform it targets.
ADR-012's CGo prohibition is written for the coordinator, so this is not a
constraint violation either way, but spending it needs a reason and there
isn't one.

**It is what B0 measured.** The owner's spike ran `gst-launch-1.0` pipelines
by hand for 6 h 49 min at 40.00 fps with zero dropped frames. That is direct
evidence this process model sustains the reference profile. An in-process
binding has no evidence at all in this project.

**The escape hatch stays open.** The pipeline builder is an interface with the
subprocess implementation behind it. If frame pacing proves inadequate,
ADR-007's own exit criterion applies and a superseding ADR is the route.

### Ruling 2: where the per-frame boundary sits, and it needs the owner's eye

ADR-007 forbids per-frame rendering in application code. There is no
GStreamer element that reads an FSEQ file, so *something* in ShowMesh has to
turn channel bytes into pixels. The boundary this track builds to:

- **ShowMesh does**: seek to a frame in the local FSEQ, decompress the block,
  extract the surface's channel range, and write one raw matrix-sized buffer
  into the pipeline. At a virtual-matrix geometry this is a small buffer, not
  a video frame. The **pixel format of that buffer is a spec field, decided by
  measurement** — see ruling 5, which supersedes an earlier draft of this line
  that named RGB.
- **GStreamer does**: everything after that byte lands. Scaling to canvas
  dimensions, colour conversion, frame pacing against the pipeline clock,
  SpeedHQ encode, and NDI transport.

The claim is that extracting channel data is a **data** transform on show
content, not video rendering, and that the frame the operator sees is
produced entirely inside GStreamer. **This is an interpretation of ADR-007,
not a quotation of it**, and it is the interpretation on which the whole
renderer rests, so it is drafted as ADR-040 for the owner rather than
asserted here. Build against it; expect it to be ruled on.

The measurable consequence, which is what makes it a real question rather
than a semantic one: at the reference geometry ShowMesh moves a few tens of
kilobytes per frame, and if a future surface is authored at canvas
resolution it moves megabytes per frame. **The ruling is safe at matrix
resolution and unsafe at canvas resolution**, so B3 must record the actual
buffer size it produces into RES-004 rather than assuming.

### Ruling 3: sync loss changes the picture, never the sender

When the timeline stops or goes unsynchronized, the renderer changes **what it
draws**. It does not stop the pipeline, tear down the NDI sender, or let the
process exit.

The reason is downstream and it is specific. Resolume losing an NDI source
entirely is a larger and slower-to-recover event than Resolume receiving black
frames, and Track D's D-3a crash recovery is built on the premise that Arena
disappears and returns, not on the premise that its inputs do. Removing a
sender to signal "no content" spends a reconnection to communicate something a
black frame communicates for free.

Behaviour by timeline state (`pkg/multisync` vocabulary):

| Timeline state | Output |
|---|---|
| `playing` | free-run content at the reported position |
| `unsynchronized` | free-run content, unchanged. This state is a confidence statement, not a playback stop, and `pkg/multisync` keeps free-running through it deliberately |
| `opened` | the idle output |
| `stopping` | last rendered frame held for `BlankDelayFrames`, mirroring FPP remote semantics |
| `stopped` | the idle output |
| `unknown` | the idle output |

The **idle output** is operator-settable in the `render.settings`
configuration object, one of `black` (default), `hold`, or `diagnostic`.
Default `black`: a frozen frame on a house is indistinguishable from a
crash and stays that way all night, which is the failure the operator cannot
diagnose from the driveway. `diagnostic` renders a legible identification
card (surface name, node, timeline state, reason) and exists because "the
projector shows something wrong" is the report this track will actually
receive.

**This is a product decision and the track document already flagged it.** It
is ruled here so B2 and B3 can build; it is queued for the owner.

### Ruling 4: the node is told its surface; it does not discover it

There is no configuration-distribution path to a node today (ADR-025's cache
is decided and unbuilt, and ADR-008's v1 topic set has no config topic). The
node also cannot resolve "the FSEQ for my surface's sequence" from local
state: it holds filenames and content hashes, and the show/sequence/target
mapping lives only in the coordinator.

So the coordinator pushes a complete, self-contained assignment as the
`render.surface.apply` operation's params: the full decoded surface payload
plus the **resolved** runtime filename and content hash of the FSEQ. The node
stores that assignment on disk and re-reads it at boot, so a node that
restarts with no coordinator reachable resumes rendering.

This mirrors `asset.fetch` exactly and adds no new distribution mechanism. It
also keeps ADR-028's rule intact: the node never resolves an asset by
filename, because the coordinator resolved it by identity before dispatch.

### Ruling 5 amended by measurement: the pipeline is threaded deliberately

**Added 2026-08-17 from the owner's B0 measurements**, which arrived after the
rulings above were written and which change the shape of the pipeline rather
than a detail of it.

**The B0 spike encoded at 86% of one core with no queue in the pipeline.**
Frame generation, SpeedHQ encode and NDI send therefore shared a single
GStreamer streaming thread while the rest of the machine sat near idle. **The
ceiling this track is building against is per-core, not per-machine**, and the
14% of one core left over is the entire budget the renderer would inherit if
it were simply hung off the front of the spike's pipeline shape.

So:

- **A `queue` is a first-class element of the pipeline spec, not a tuning
  detail.** Thread boundaries are chosen and expressed, and the default spec
  carries one before the sink from B2a onward. A queue added only when
  something turns out to be slow is a queue added after the measurement that
  would have justified it.
- **The pipeline is staged, not extended.** B3's source stage and B4's sink
  stage must each be able to sit on their own thread. "The spike's pipeline
  with FSEQ extraction bolted on the front" is the specific design this
  paragraph exists to forbid.

**Pixel format is an explicit field of the spec and is hardcoded nowhere.**
The 86% figure was measured on a zero-conversion path, so a `videoconvert`
added later spends budget never measured as spare, and the owner's guidance
is to emit UYVY from the extraction where possible.

**One tension is recorded rather than resolved, because it needs a
measurement this track will produce.** The zero-conversion result was measured
with **no scaling**: the spike's source emitted the sink's native format at
canvas dimensions. The real path extracts a small virtual matrix that must be
scaled up to canvas dimensions, so some scaling and probably some conversion
cost is unavoidable, and where it is cheapest to pay it is exactly the
question B3 must answer with numbers rather than argument. What B2a owes is
that nothing forecloses the answer.

**Recovery stays unverified and must not be assumed.** Spike run 5 never ran,
so nothing says the NDI sender and Resolume survive each other's restart. No
code, comment or test name may assert that a restart re-establishes the
downstream connection. A pipeline that restarts cleanly while its downstream
never returns must be visible as its own condition: **"the process is up" is
not "frames are arriving somewhere"**, and reporting the first as health is
how this track would ship a black wall that monitors green.

## Seam B2: pipeline supervision

**Goal.** The agent builds, starts, watches and restarts a GStreamer pipeline,
and reports its health as observations with provenance and freshness. Test
pattern content only; no FSEQ, no channel extraction.

**Deliver.**

- `internal/agent/pipeline/` — a `Supervisor` owning one pipeline per surface
  assignment. Build the `gst-launch-1.0` argv from a `Spec`, start it, read
  its stdout/stderr, detect exit, restart with bounded exponential backoff,
  and expose a snapshot of state. Restart policy must distinguish a crash from
  a clean stop, and must not restart a pipeline that fails identically and
  immediately forever — after a bounded number of consecutive fast failures it
  reports `failed` with the last stderr and stops trying until told otherwise.
  **A silent infinite restart loop is the failure mode here**, and it looks
  healthy from every angle except the wall.
- The `gst-launch-1.0` binary is located by `PATH` with a
  `SHOWMESH_GST_LAUNCH` override. Absent binary degrades the node with a
  stated reason; it never stops the agent (ADR-026 decision 6's rule,
  generalized).
- Three allowlisted operations in `newOperationRegistry`
  (`internal/agent/command.go:177`): `render.surface.apply`,
  `render.surface.clear`, `render.pipeline.restart`.
  **`OperationResult.ExecutedAt` and `ObservedAt` must not be collapsed.**
  Applying a surface starts a pipeline; confirmation requires polling until it
  is observed running, and the evidence must post-date the dispatch. The
  doc comment at `command.go:74-89` names this exact case in advance.
- A new retained observation on `showmesh/nodes/<id>/observed/render`, schema
  `showmesh.node.render/v1`, published on a ticker and on every state
  transition, following `runHeartbeat`/`runAssetInventory`'s exact shape. New
  payload type, `Validate`, `NewRenderEnvelope`, `DecodeRenderPayload` in
  `pkg/mqttproto/payload.go`.
- Coordinator ingest: a `case "render"` in
  `internal/coordinator/inventory/inventory.go:315`. **Without it the message
  is dropped at Debug and everything downstream reports nothing** — that
  default branch is currently swallowing `observed/agent/echo`.
- A `collector/noderender` collector converting ingested reports into real
  `observation.Observation` values under the new `surface` resource kind, so
  the render signals reach `GET /api/v1/observations`, the SSE stream, and
  health aggregation through the paths that already exist rather than a
  parallel one.
- `render.settings` configuration kind: singleton, revisioned, audited,
  applying without a restart (ADR-036), with `GET`/`PUT`
  `/api/v1/config/render.settings`, `showmeshctl render settings get|set`,
  and a UI control. Per ADR-039 this is not optional.
- `showmeshctl render` verb group: `status`, `apply`, `clear`, `restart`,
  `settings get|set`. Re-declare wire types locally; the import-graph guard
  forbids importing any coordinator package.
- UI: a render panel on the node detail view and pipeline state on the
  dashboard. `ScopedButton` for every control, `EvidenceValue` for every
  observation. **Do not build a `/config/show.surface` screen** — Track G
  seam G-8 owns it.

**Must not.** Touch `internal/coordinator/config/showsurface.go`. Add a nav
group. Put GStreamer on the coordinator. Let a pipeline failure stop the
agent.

**Verify against a running binary.** GStreamer 1.28.6 is installed on the
development laptop as of 2026-08-17, so B2's supervision is testable for real:
start a `videotestsrc ! fakesink` pipeline, `kill -9` it, and confirm the
restart is detected, reported, and visible as an event rather than silent.
That is an acceptance criterion, and a fake process will not prove it.

## Seam B3: FSEQ extraction and the timeline

**Goal.** The node renders its assigned surface's channels from the local
FSEQ at the frame the MultiSync timeline reports.

**Deliver.**

- `pkg/fseq/` — a reader for FSEQ v2 with sparse ranges and zstd
  compression, which is what xLights writes by default for a per-target
  render (RES-003 §9.7). Pure Go: `github.com/klauspost/compress/zstd`. The
  format work cites `docs/research/RES-017-fseq-format.md`; do not re-derive
  it from a hex dump.
  **The sparse-range mapping is the defect-prone part.** In a sparse file the
  frame data contains only the channels inside the sparse ranges, packed, so a
  surface's absolute channel range must be mapped through those ranges to a
  byte offset. Getting it wrong renders the wrong pixels and renders them
  confidently. Test it against a file whose sparse ranges do not start at
  channel 1.
  Refuse, with the numbers stated, when the surface's channel range is not
  fully covered by the file's sparse ranges. **A partially covered range must
  never render as black for the missing part**, because that is
  indistinguishable from content.
- A MultiSync listener in the agent on UDP 32320. **ADR-013 does not constrain
  a render node** — it runs no `fppd`, so it binds normally with no port
  sharing and `AllowPortSharing` false. Pass the **source IP only** to
  `Timeline.Observe`; the 30-line contract at `timeline.go:510` explains why
  an `ip:port` string is wrong and what it cost last time.
- `Timeline.SetStepTime` is called from the FSEQ's own step time on every file
  change. Step time is not carried on the wire and the 25 ms default is a
  guess about the file, not knowledge of it.
  **FSEQ step time is a single byte**, so 30 fps is stored as `33` and a
  sequence played at exactly 33 ms drifts about 1.2 seconds per hour
  (RES-017). Do **not** "correct" this to 33.333: FPP reads the same byte, so
  ShowMesh matching FPP is what keeps the surface in step with the lighting,
  which is the acceptance criterion. Matching the true frame rate instead
  would drift ShowMesh away from the show. Say so where the value is read,
  because it reads like a bug.
- **Do not assume channels come in RGB triplets.** Real per-target files
  measured in RES-017 carry channel counts of 45241, 22876 and 19884, none a
  multiple of three. An extractor that assumes triplets shears the image. The
  surface's own `geometry` and `pixelFormat` decide pixel layout, and
  `showsurface.go` already enforces
  `width * height * channelsPerPixel == channelCount` — trust that, not the
  file's total.
- **An absent channel must never decode to zero.** A channel outside the
  file's sparse ranges is not black; it is not present. This is the `"ma":
  null` defect from Step 5 in a new disguise and it is the third subsystem to
  meet it.
- The frame writer: a goroutine that, per frame period, reads the timeline
  position, extracts the channel range, writes the buffer to the pipeline's
  stdin, and counts what it did. Late and dropped frames are counted and
  reported, not logged and forgotten. A short write or a closed pipe is a
  pipeline failure, handed to B2's supervisor.
- Idle output per ruling 3.

**Must not.** Introduce a live matrix stream (ADR-026 decision 2). Reach the
asset store from any playback path (ADR-028). Assume one surface per node in
any type, schema, or signal name (ADR-026 decision 3) — `N=1` is enforced in
validation only.

**Record, do not assume.** The measured buffer size per frame, the achieved
frame rate, and the extraction cost go into RES-004 as measurements. ADR-026
decision 5's 40 fps target stays labelled as intent until they do.

## Seam B4: NDI output

**Goal.** The NDI transport adapter, with absence handled as degradation.

**Deliver.**

- The NDI sink stage of the pipeline spec, named from
  `show.surface.output.ndi.sourceName`.
- A transport probe, `render.transport.probe`, and this is the seam's
  sharpest requirement:

  **Element presence is not runtime presence, and this session reproduced the
  trap.** On the development laptop, `gst-inspect-1.0 ndisink` exits 0 and
  reports the element found, while an actual pipeline fails at the NULL to
  PAUSED transition with `Failed loading NDI SDK`
  (`net/ndi/src/ndisink/imp.rs:182`). The gst-plugins-rs sink dlopens the NDI
  runtime **at state change**, not at plugin load. So a probe that checks
  whether the element exists will advertise a usable NDI sender on a node that
  cannot send one frame, which is precisely the false claim ADR-026 decision 6
  and CLAUDE.md forbid. **The probe must attempt a real state transition on a
  throwaway pipeline and read the outcome.**

  Captured evidence is in the seam's own notes; reproduce it before building
  the probe, because this laptop is a genuine missing-runtime node and the
  degradation path is therefore testable for real rather than against a fake.
- Absence behaviour: the node starts, keeps every other capability, does not
  advertise a usable sender, and reports an actionable installation pointer.
  It never refuses to start. Note the field reality this will meet: the
  element is a source build from `gst-plugins-rs` on Debian 13, not a package.
- `showmeshctl render transport` reporting the probe result with its reason,
  exiting `22` (`exitRenderUnavailable`) when the runtime is absent.

**Must not.** Vendor, link, or redistribute the NDI runtime (ADR-010,
ADR-026 decision 6). Treat NDI support as evidence for HDMI support or the
reverse (ADR-026 decision 4).

## Seam B5: capability advertisement

**Goal.** The node advertises what it can actually do, from detection rather
than from an environment variable.

**Deliver.**

- Real capability detection at boot and on change, replacing
  `SHOWMESH_NODE_CAPABILITIES` as the only source. The env var stays as an
  operator override, but a node must be able to advertise `render.surface` and
  `transport.ndi.send` because it probed them, not because someone typed them.
  **Today the default is an empty set and there is no detection at all**, so
  nothing has ever advertised a real capability in this project.
- `render.surface` with attributes (max geometry, pixel formats, measured
  frame rate where one exists), and per-transport capabilities advertised
  **independently**. `transport.ndi.send` is advertised only when B4's probe
  actually transitioned a pipeline.
- Attributes carry evidence, not aspiration. A frame rate attribute that was
  never measured must not appear.
- A `CapabilityPanel` lookup-table entry keyed on capability id for
  `render.surface`, per that component's stated extension rule
  (`ui/src/components/CapabilityPanel.tsx:13-18`), leaving the generic
  renderer as the fallback.

**Must not.** Let the coordinator gate a `show.surface` write on advertised
capability. `showsurface.go:160-164` states that rule and its reason:
advertisement is observed state and is absent when the node is offline.

## Sequencing and parallelism

B2 and B3 touch overlapping files in `internal/agent/` and must be built in
sequence on one branch. B4 and B5 are largely disjoint from each other and
from B3's `pkg/fseq` work.

- `pkg/fseq` is fully independent and can be built in parallel with B2 from
  the start, since it is a pure parsing package with no agent dependency.
- B4 depends on B2's pipeline spec type.
- B5 depends on B4's probe for the transport capability, but its detection
  scaffolding does not.

Every seam ends with `make check` and `make test-integration` actually run.

## What this track cannot prove tonight, and must not claim

Straight to `docs/private/PUNCH-LIST.md`, not marked done:

- Anything requiring a real projector, a real Resolume on the playout host, or
  the physical MultiSync rig. RES-002's drift and switch behaviour stay open.
- The acceptance criterion "with FPP playing a sequence, the node follows the
  MultiSync timeline and Resolume displays the surface's content in step with
  the lighting." The bench `fppd` can drive MultiSync; a wall cannot be
  checked from here.
- Sender/receiver restart recovery, until spike run 5 lands.

**One item came off this list before it was written.** RES-017's research pass
found **198 real xLights-written `.fseq` files on the development machine**,
including an FPP host's own `sequences/` directory, and parsed all of them.
So `pkg/fseq` is testable against real per-target xLights output rather than
against synthetic bytes, which is a materially stronger position than Track E
had and closes the "a synthetic FSEQ proves the parser against this project's
own assumptions about the format" objection for the parser specifically. It
does **not** close Track E's punch-list item, which is about an FSEQ moving
end to end through the store to a node and being rendered; that still has not
happened.
- Sender/receiver restart recovery, until spike run 5 lands.
