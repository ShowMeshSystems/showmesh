# Track B render node: bench procedure

[Build plan](../build/BUILD-PLAN.md) · [ADR-026](../decisions/ADR-026-renderer-surface-model-and-reference-transport.md) · [ADR-027](../decisions/ADR-027-show-and-surface-model.md) · [RES-004](../research/RES-004-virtual-matrix-renderer-performance.md) · [RES-005](../research/RES-005-ndi-vs-hdmi-transport.md) · [RES-006](../research/RES-006-linux-ndi-support.md) · [TRACK-B-NDI-SPIKE](TRACK-B-NDI-SPIKE.md) · [TRACK-B-nodes-and-projection](../build/TRACK-B-nodes-and-projection.md)

Written 2026-08-17. This is the operator procedure for commissioning and testing the actual Track B renderer — the node, the agent, the surface model, and Resolume — as opposed to [TRACK-B-NDI-SPIKE](TRACK-B-NDI-SPIKE.md), which proved only that a bare GStreamer pipeline could reach Resolume as NDI, ran once on 2026-08-16, and carries no FSEQ, no virtual-matrix extraction, no surface model, and no ShowMesh code at all. That spike answered "can frames leave this box." This procedure answers "does the renderer we built actually run a show."

This document is a **procedure to run, not a set of results**. Nothing in it should be read as a claim that any of these steps has been executed against real hardware. Where an expected outcome is a hypothesis rather than something measured, it is marked **SHOWMESH HYPOTHESIS**.

## WARNING: the live fleet is untouchable, and this bench needs its own FPP

**No write, no command, no restart, no settings change, and no MQTT publish on any `falcon/` topic reaches the operator's deployed fleet or its live broker, ever, from this procedure.** `deploy/.env.live-fleet-run.bak` exists locally and must never be combined with a write-capable stack — that file, if you have it, points at the real broker and the real hosts, and this procedure issues real playlist commands and drives a real MultiSync timeline. Confirm which coordinator, which broker, and which FPP your `showmeshctl` config and agent environment point at before step 1, not after something goes wrong.

The FPP this procedure drives must be the containerized bench `fppd` (`bench/fpp-multisync/`) or a dedicated bench FPP host that is not a MultiSync participant in the real show, never the reference installation. If at any point you are unsure which FPP a command is about to reach, stop and check `SHOWMESH_FPP_ENDPOINTS` / the coordinator's `fpp.endpoints` configuration before proceeding.

## Hardware and software to record

Record every one of these once, at the top of your results, per [TRACK-B-NDI-SPIKE](TRACK-B-NDI-SPIKE.md)'s own rule: a result without its parameters cannot be compared to the next one.

- Render node: model, CPU, RAM, GPU, OS and kernel version.
- NIC and link speed on the renderer, and the switch it lands on.
- Resolume: exact version, and the machine it runs on.
- NDI runtime version, and where it was installed from.
- GStreamer version, and the NDI plugin's name, version, and origin (source-built — see step 1).
- Coordinator: commit or version, and where it is running.
- Agent: commit or version.
- FPP: version, and confirmation it is the bench instance (containerized `fppd`, or a dedicated bench host — never the reference installation).
- `showmeshctl` version/commit.

## 1. Prerequisites and node commissioning

### 1a. Operating system and GStreamer NDI element — the unrecorded build

Debian 13, per the [B0 spike's decision](TRACK-B-NDI-SPIKE.md#operating-system) and confirmed again by [RES-006](../research/RES-006-linux-ndi-support.md)'s 2026-08-16 soak, which ran on a Debian 13 OptiPlex 7050.

**The GStreamer NDI element is NOT packaged on Debian 13.** It ships from `gst-plugins-rs`, source-built on the node. **This build recipe currently exists only on the owner's bench machine, in his shell history, and nowhere in this repository.** That is a day-0 dependency resting on one person's memory of what he ran, which is exactly the kind of unrecorded fact this project's own standing rules exist to catch before it costs a week. Capturing it is not optional bench hygiene here — it is the first numbered step, because every later step in this procedure depends on it existing at all.

**Step 1a, required, before anything else:**

1. On the render node (or reproduce from the owner's bench machine if that is where the working build currently lives), run the full build from a clean Debian 13 checkout: toolchain packages installed, `gst-plugins-rs` cloned at a specific commit or tag, the NDI feature/plugin built, and the resulting `.so` placed where `gst-inspect-1.0` finds it.
2. Record every command run, in order, with versions: Rust toolchain version, `cargo` version, the exact `gst-plugins-rs` git ref, the `cargo build` invocation and feature flags used, and the resulting plugin path.
3. Confirm the freshly built element loads on a clean node, not just on the machine that built it.

   The element name is **`ndisink`**, measured rather than assumed: on this project's macOS development machine, with `gst-plugin-ndi` 0.15.3 from Homebrew, `gst-inspect-1.0 ndisink` exits 0 and reports `NewTek NDI Sink` (checked 2026-08-17). **That evidence is from a different platform and a different build**, so confirm it on the node rather than carrying it across. A source build from `gst-plugins-rs` producing a different name is exactly what this step exists to catch.

   **Element presence is not runtime presence, and designing around that is why this track's probe works the way it does.** The same development machine that reports the element present fails a real pipeline at the NULL to PAUSED transition with `Failed loading NDI SDK` from `net/ndi/src/ndisink/imp.rs:182`, because the sink dlopens the NDI runtime at state change rather than at plugin load. `gst-inspect-1.0` succeeding proves the plugin is installed and proves nothing about whether a frame can leave the box. Run a real pipeline before believing the node can send:

   ```
   gst-launch-1.0 videotestsrc num-buffers=5 is-live=true \
     ! video/x-raw,format=UYVY,width=64,height=64,framerate=10/1 \
     ! ndisink ndi-name=commissioning-check sync=false
   ```

   Success looks like a `New clock:` line and a clean exit. A node that prints `Setting pipeline to PLAYING` and then fails has NOT succeeded; that string is printed before the transition is attempted, which is a distinction this track had to fix a real defect over.
4. Write the recorded recipe into this repository (a new file under `docs/bench/`, or an update to this procedure — orchestrator's call) before treating any later step in this procedure as repeatable by anyone other than the owner.

Until step 1a's recipe is captured in the repository, every other step below is a one-person bench, not a commissioning procedure.

### 1b. NDI runtime

Install NDI's own runtime on the render node, per [RES-006](../research/RES-006-linux-ndi-support.md) and the standing constraint (Apache-2.0, dlopen only, never vendored). Record the runtime version and install source.

### 1c. Agent install and MQTT credential

1. Install the ShowMesh agent binary on the render node.
2. From `deploy/`, issue the node its MQTT credential: `./mosquitto/add-agent-credential.sh <node-id>`. This validates `<node-id>` against the agent's node-ID pattern, generates a password, rebuilds the Mosquitto ACL, and reloads Mosquitto if the compose stack is running. It prints `SHOWMESH_MQTT_USERNAME` and `SHOWMESH_MQTT_PASSWORD` — set both in the agent's environment on the render node.
3. Confirm coordinator reachability from the render node: the agent needs the control-plane broker URL (`SHOWMESH_MQTT_URL` or equivalent) and, per ADR-039, nothing else — no coordinator HTTP endpoint is configured into the agent's environment.
4. Start the agent and confirm in its logs that it connects to the broker and publishes its retained hello.

## 2. Bring-up, before any measurement

### 2a. Declare the node

```
showmeshctl declare -label "<descriptive label>" <node-id>
```

This is `POST /api/v1/nodes/{nodeId}/declaration`, requires `config:write`.

### 2b. Confirm capability advertisement

```
showmeshctl node <node-id>
```

Confirm:
- `render.surface` is present. This is the agent's render capability, advertised unconditionally.
- `transport.ndi.send` is present **only if** the NDI probe genuinely transitioned a pipeline — this capability is advertised, per the agent's own code, only after a successful probe, never on the strength of `gst-inspect-1.0` reporting the element present. If the NDI runtime or plugin is missing or broken, `transport.ndi.send` must be **absent**, and the node must still advertise `render.surface` and start normally with its other capabilities.

This restates acceptance criterion 5, already closed once on the development laptop (no NDI runtime installed there, node started, capability correctly absent). **Run it again here, on real render-node hardware, for the first time**, because "closed on the development laptop" and "closed on the node that will actually run a show" are different claims, and this project's own recurring lesson is that a test environment differing from the deployment environment reports success on exactly that difference.

If your node genuinely has the NDI runtime and a working plugin (the expected case after step 1), confirm `transport.ndi.send` is present instead, and record that this is the "capability correctly advertised" half of the same criterion, not a separate one.

## 3. Show, surface, asset

### 3a. Create and activate a show

```
showmeshctl show set -name "<show name>" <show-id>
showmeshctl show activate <show-id>
showmeshctl show active
```

`show set` is a full replacement PUT to `/api/v1/config/show/{id}` (`config:write`, admin only). `show activate` is a full replacement PUT to `/api/v1/config/show.active`.

**Operator UI path:** create and activate the same show from the Operator UI's show configuration screen. Confirm the UI reflects the same active show `showmeshctl show active` reports. This closes the UI half of acceptance criterion 1 that the previous session left unverified — nobody has loaded this in a real browser yet.

### 3b. Create a surface with a manual channel range

Manual channel ranges are a **permanent first-class path** under ADR-027, not a placeholder for name-based mapping — use one here regardless of whether your render node's target model has xLights metadata available.

```
showmeshctl surface set \
  -show <show-id> \
  -name "<surface name>" \
  -node <node-id> \
  -start-channel <n> \
  -channel-count <n> \
  -width <px> \
  -height <px> \
  -pixel-format rgb \
  -frame-rate 40 \
  -transport ndi \
  -ndi-source-name "<the name this node's NDI sender will advertise>" \
  <surface-id>
```

`width * height * channelsPerPixel(pixel-format)` must equal `-channel-count` exactly or the coordinator refuses the write — if you get a rejection here, that is the arithmetic check working, not a bug. Use the canvas dimensions you actually intend to project; this is the number that step 5 measures against.

**Operator UI path:** create the equivalent surface from the Operator UI's surface configuration screen with the same manual channel range, and confirm `showmeshctl surface get <surface-id>` shows what the UI wrote.

### 3c. Upload the FSEQ asset

Use a real xLights-rendered FSEQ for this node as target — not a synthetic pattern. TRACK-B-NDI-SPIKE and RES-005's 2026-08-16 soak both used `videotestsrc`, and RES-005 itself flags that result as "a transport result and not a renderer result." This procedure is the first chance to move past that.

```
showmeshctl assets upload -show <show-id> -sequence <sequence-id> \
  -media-type fseq -target-kind node -target <node-id> -file <path-to-fseq>
```

### 3d. Wait for the node's asset manifest to report ready

```
showmeshctl assets manifest -node <node-id> -require-ready
```

This exits 0 only when the node's manifest is ready; 20 if `not_ready`, 21 if `unknown` (and none `not_ready`). Poll or wait until this returns 0 before applying.

### 3e. Apply the surface

```
showmeshctl render apply <node-id> <surface-id> <sequence-id>
```

This dispatches `render.surface.apply`. The coordinator resolves the surface's complete assignment — including its current FSEQ asset for `sequence-id`, by content identity per ADR-028, never by filename — and refuses outright, naming what could not be resolved, rather than ever sending a partial assignment. Confirm the command reports success and that it is evidenced, not just accepted:

```
showmeshctl render status <node-id>
```

Confirm `surface.pipeline.state` and `surface.transport.available` for this surface, not just a `200` from the apply call. A `200` is not evidence that the pipeline is actually running (ADR-003).

## 4. Acceptance criterion 2: does the node follow the timeline, and is it actually in step

With FPP (the bench instance — see the warning above) playing a sequence, the node should follow the MultiSync timeline and Resolume should display the surface's content in step with the lighting.

**What to look at.** Position the render node's output (via Resolume, on a monitor or the actual projection surface) next to the physical lights it corresponds to, in the same field of view if at all possible. Pick a moment in the sequence with a sharp, recognizable simultaneous event on both sides — a blackout, a strobe, a color snap, a chase starting — rather than a slow fade, because a slow fade makes small lag invisible to the eye and a sharp event does not.

**What "in step" looks like.** The projected content's sharp event and the physical lights' sharp event read as one motion, not two. If you have to ask "was that at the same time," it was not.

**What a lag would look like.** A consistent, repeatable offset where the projected content's event trails (or leads) the lights' event by a noticeable beat, especially if the offset is the same magnitude on every repeat of the sequence rather than jittering randomly. A jittery, inconsistent offset that varies run to run is a different problem (pacing/dropped frames — see step 5) from a consistent one (a real sync-model discrepancy).

**If the picture is right but late:** that is a lag, not a sync failure, and it matters differently. Check `surface.timeline.position_ms` via `showmeshctl render status <node-id>` against FPP's own reported position at the same wall-clock moment. A late-but-correct picture that stays a constant offset behind is worth recording as a measured lag (in milliseconds, from the position comparison, not from eyeballing) rather than either dismissing it or treating it as equivalent to the picture being wrong. Do not guess at the offset from the video; measure it from the two reported positions.

**SHOWMESH HYPOTHESIS:** the FPP remote-sync semantics this project follows for the lighting timeline (free-run through sync silence, slew ≤4 frames, jump when >0.5s behind, STOP then ~5-frame blank delay) apply to the render surface the same way they apply to a lighting remote. This has not been measured on a render node; this step is what tests it.

Only the MultiSync-following half of this criterion is testable at all without a physical install — actually watching Resolume's output next to real lights requires being at the bench with both visible at once. Do not attempt to substitute a screen recording reviewed later for watching both live; timing artifacts are the thing most likely to be smoothed over by asynchronous review.

## 5. Acceptance criterion 6: frame rate and pacing at the intended canvas dimensions

This is the measurement that moves a document. RES-004 is at L0 with no measurement at the reference profile's real hardware, canvas dimensions, or sustained duration; ADR-026 decision 5's 40 fps / OptiPlex 7040-class profile is stated as a target it explicitly expects to be revisited once a real bench exists. This step produces that bench.

Capture, at the surface's actual configured canvas dimensions from step 3b (not a smaller test resolution):

- **Canvas dimensions and matrix pixel count.** State both explicitly next to every number below. A frame rate without them cannot be compared to any other measurement in RES-004, including the 2026-08-17 extraction-cost figure (272µs warm / 1.49ms cold, one second, `fakesink`, development laptop) already on record there, which was explicit about binding only the algorithm and saying nothing about the OptiPlex.

- **Achieved fps, dropped, late**, from `showmeshctl render status <node-id>` (`surface.frames.rate`, `surface.frames.dropped`, `surface.frames.late`) read at the end of the run, and ideally sampled a few times during it. **`dropped: 0` across a long run on a QoS-active sink is a real measurement; an average frame rate sitting near 40 fps is close to a tautology on its own** — this is RES-005's own lesson from the B0 spike, where reporting jitter and drops as observed results rather than hiding them inside an average is what makes a number trustworthy. For each figure you record, ask: which of these numbers could plausibly have come out differently if the pipeline were actually struggling? An average near target that never varies is worth treating with more suspicion than one that dips and recovers.

- **CPU utilization per core, not per machine.** This is the single most important number in this document. The B0 spike measured the bare NDI transport at 86% of **one** core on a near-idle box (RES-005, 2026-08-16 soak, `videotestsrc` source). The virtual-matrix extraction step now sits in front of that transport cost, and the two have never been measured running together. A whole-machine CPU percentage on a multi-core box hides whether the renderer is one core away from falling over; record per-core utilization (e.g. `mpstat -P ALL` or equivalent) for the process(es) actually doing extraction and NDI send, not an aggregate.

- **Run length.** State it explicitly. A one-second run proves nothing about sustained behavior — B3's 272µs/1.49ms laptop numbers are one-second `fakesink` figures and are explicitly not a substitute for this criterion. Run long enough to see steady state: RES-005's own soak ran 6h49m; this step does not need to match that length to be useful, but a run under a few minutes should be labeled a smoke test, not a measurement, in whatever you write into RES-004.

- **ShowMesh's own reported evidence, alongside the external (wall/tool) observation.** Read `surface.frames.rate`, `surface.frames.late`, and `surface.frames.dropped` from `showmeshctl render status <node-id>` at the same time you are independently observing or measuring the output (a capture card, a frame counter overlay, or an external tool watching the NDI stream). Record whether the dashboard's numbers and the independently observed numbers agree. **The entire point of `surface.frames.rate` existing is that an operator can learn what is happening from the dashboard instead of the wall, and that claim itself has never been tested** — this is the step that tests it, not a formality on top of the frame-rate measurement itself.

## 6. Sync-loss behaviour on real hardware

Stop the FPP sequence (bench instance only) while the surface is applied and playing.

1. With `render.settings` at its default (`idleOutput: black`), confirm the surface's output goes to black and stays black — not a frozen last frame. Check `surface.output.idle_mode` via `render status`.
2. Set `render.settings` to `idleOutput: diagnostic` (`showmeshctl render settings set`, full replacement — every field including `restartPolicy` is required) and repeat. Confirm the output is generated content that **visibly changes over time**, never a static frame — a diagnostic mode that looks frozen is indistinguishable from a crash and defeats its own purpose.
3. Note for the record, but do not test as a positive case: `idleOutput: hold` is a selectable value and is **deliberately** indistinguishable from a crashed renderer by design — do not report "it looked frozen" under `hold` as a defect; that is the documented behavior of that setting, not evidence of anything.

## 7. Pipeline death on real hardware

This has passed in the integration suite against a bench agent subprocess on a laptop. It has never run against a real render node.

1. With a surface applied and its pipeline running, find and kill the `gst-launch-1.0` process the agent supervises (not the agent itself).
2. Confirm detection: `surface.pipeline.state` transitions, `surface.pipeline.restart_count` increments, in `showmeshctl render status <node-id>`.
3. Confirm restart: the pipeline resumes without operator action, subject to the configured `restartPolicy` backoff.
4. Confirm an event: `GET /api/v1/events` carries a `category: "render_pipeline"` entry, `severity: "warning"`, for the restart. If you kill it repeatedly fast enough to trip `maxConsecutiveFastFailures`, confirm a second, `severity: "critical"` event for entering the failed lockout, and confirm `showmeshctl render restart <node-id> <surface-id>` clears the lockout and restarts from the currently-applied spec.

## 8. Spike run 5, still unrun: sender and receiver restart recovery

Neither side of the sender/receiver relationship has been shown to survive the other's restart, and the agent's pipeline supervision (step 7) was deliberately built assuming neither does.

1. With the surface applied and streaming, restart Resolume while the sender keeps running. Confirm whether the sender detects the loss, what `surface.transport.available` reports while Resolume is down, and whether the surface resumes displaying once Resolume is back — with or without an operator action (`showmeshctl render probe` / `render apply` again). Record which.
2. With Resolume running and receiving, kill and restart the agent (or restart the render node) with Resolume left up. Confirm whether the surface resumes on its own once the agent/node comes back, or requires a fresh `render apply`.

**SHOWMESH HYPOTHESIS:** nothing in the current design claims either direction recovers unattended; this step is what finds out, not what confirms an assumption.

## 9. What each result changes

Per this project's standing rule, name what a result moves before spending bench time on it:

| Step | What a clean result changes | What a bad result changes |
|---|---|---|
| 1a (build recipe) | Removes a single-person day-0 dependency; recipe becomes reproducible by anyone | Nothing ships until it is captured — this blocks everything else regardless of outcome |
| 2b (capability advertisement, real hardware) | Re-confirms acceptance criterion 5 on real hardware, not just the development laptop | Reopens criterion 5 as hardware-dependent, not closed |
| 3a (Operator UI show create/activate) | Closes the UI half of acceptance criterion 1 | Criterion 1 stays partly open, logged to the punch list |
| 4 (MultiSync-following, in-step check) | First real evidence toward acceptance criterion 2's MultiSync half | Criterion 2 stays open; a consistent lag is new information for RES-004/RES-005, not yet explained by either |
| 5 (fps/pacing/CPU-per-core) | Moves RES-004 off L0 toward L1/L2 depending on run conditions; gives ADR-026 decision 5 a measured profile to replace "target" with "supported," or to name the dimensions that actually hold | If the reference dimensions cannot sustain 40 fps, that is exactly the "find the dimensions the hardware does sustain" case TRACK-B-NDI-SPIKE already anticipated for the transport layer — now re-run with the real renderer in front of it |
| 6 (idle output) | Confirms acceptance criterion 3 on real hardware | Surfaces a defect in idle-output handling under real GStreamer/NDI conditions the bench container never exercised |
| 7 (pipeline death) | Confirms acceptance criterion 4 on real hardware | Surfaces a defect the integration suite's simulated subprocess kill did not catch |
| 8 (restart recovery) | Closes RES-005's open "recovery" item and TRACK-B-NDI-SPIKE's unrun run 5, on the real renderer this time | Names a real gap in the agent's supervision design that step 7's assumption ("neither side survives the other's restart") needs to account for explicitly rather than by omission |

Whichever punch-list entries these steps were sitting on (`docs/private/PUNCH-LIST.md`) close only for the specific criterion actually measured here — do not close an entry on the strength of a step that was skipped or that stopped short of a full run.

## Results template

Paste this, filled in, into RES-004 (frame rate / pacing / CPU) and RES-005 (transport recovery, sync behavior) as appropriate. Every field left blank should say why, not be silently omitted.

```
## Track B render node bench — <date>

### Environment
- Render node: <model, CPU, RAM, GPU, OS+kernel>
- NIC/link speed: <>
- Switch/network path: <>
- Resolume: <version>, on <machine>
- NDI runtime: <version>, installed from <source>
- GStreamer: <version>; NDI plugin <name>/<version>/<origin> (gst-plugins-rs ref: <git ref>)
- Coordinator: <commit/version>, running on <where>
- Agent: <commit/version>
- FPP: <version>, bench instance confirmed: <yes/no, how>
- showmeshctl: <version/commit>

### Step 1a: build recipe captured
- Recorded where: <file/path>
- Reproduced on a clean node (not just the build machine): <yes/no>

### Step 2b: capability advertisement
- render.surface present: <yes/no>
- transport.ndi.send present: <yes/no> — matches NDI runtime state: <yes/no>

### Step 3: show/surface/asset
- Show id/name: <>
- Surface id, channel range, canvas dims, pixel format, frame rate: <>
- Manual channel range used: <yes> (ADR-027 permanent path)
- FSEQ source: <real xLights sequence name/show, not synthetic>
- Operator UI create/activate verified in a real browser: <yes/no, browser/OS>
- render apply result + evidence (pipeline state, transport available): <>

### Step 4: MultiSync-following / in-step
- Sharp event used for comparison: <>
- In step / consistent lag / jitter: <>
- If lagged: measured offset (position_ms comparison, not eyeballed): <>

### Step 5: frame rate, pacing, CPU (THE measurement)
- Canvas dimensions: <W x H>
- Matrix pixel count: <>
- Run length: <>
- surface.frames.rate (reported): <>
- surface.frames.dropped (reported): <>
- surface.frames.late (reported): <>
- Independently observed fps/drops (external tool/capture): <>
- Dashboard vs. independent observation agreement: <>
- CPU utilization, per core, of the extraction+send process(es): <core-by-core figures>
- Anything that looked wrong even if you cannot name it: <>

### Step 6: sync-loss / idle output
- black default: <confirmed / not>
- diagnostic: visibly changing, not frozen: <confirmed / not>

### Step 7: pipeline death
- Detection (state transition, restart_count): <>
- Restart succeeded without operator action: <yes/no>
- Event in GET /api/v1/events (category, severity): <>
- Lockout + showmeshctl render restart clears it: <tested / not>

### Step 8: sender/receiver restart recovery
- Resolume restarted, sender kept running: recovered <unattended/manual/not at all>, time to recover: <>
- Agent/node restarted, Resolume kept running: recovered <unattended/manual/not at all>, time to recover: <>

### Documents this run moves
- RES-004: <L0 -> ?, or stays L0 because <reason>>
- RES-005: <which open item this closes or advances>
- ADR-026 decision 5: <profile confirmed as-is / replaced with: <new numbers>>
- Punch-list entries closed: <list, or "none — see above">
```
