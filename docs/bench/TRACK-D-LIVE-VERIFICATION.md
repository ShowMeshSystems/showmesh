# Track D live verification: what to run against the real Arena

[Track D](../build/TRACK-D-resolume.md) · [D-2 spec](../build/TRACK-D-D2-SPEC.md) · [D-3 spec](../build/TRACK-D-D3-SPEC.md) · [the control-surface capture](resolume-control-surface.md)

Status: **written 2026-08-15, and partly RUN the same day** against Arena 7.23.2 (r51094)
on the development laptop, with the owner's explicit authorisation to drive and crash it.
Results are in §6. **13 of the checks passed, none failed, and the rest are marked
not-run** with the reason.

**The single most important qualifier, carried from ADR-032's own correction: this ran on
the development laptop, which is not the playout machine.** A pass here is real evidence
about ShowMesh's behaviour and is not evidence about how Arena behaves on the show host.
Part 1 check 1.4 and every action check should be repeated on the playout machine before
the show.

## Why this exists

D-1 was exercised against the operator's running Arena and that is why its findings are
trusted. **D-2 and D-3 were unit-test evidence only until this file's §6 was run.** Every
claim about layer readiness, composition identity, the deck term, the seven actions and
their confirmation rested on a fake Arena written from the capture. The capture is good,
and a fake written from a capture reproduces the capture's own blind spots perfectly.

This is Step 4's lesson in a third subsystem: a test environment that differs from the
deployment environment reports success on exactly that difference, and the closer the
harness gets to real, the more convincing its false success looks.

**Nothing in this file needs to pass before more code is written.** It is what moves D-2
and D-3 from "safe to keep building against" to evidence. §6 did that for thirteen checks
on the development laptop; the remainder, and a repeat on the playout machine, should
happen before the Halloween show rather than before D-4.

## Ground rules

- **Run it on the playout host, not the development laptop.** Every Arena crash this
  project measured happened on the laptop, which is not properly supported hardware for
  Arena; the playout machine ran the same composition for over a month untouched. A crash
  here means something different from a crash there.
- **These are writes.** Unlike the FPP fleet, where the standing rule is read-only, D-3
  exists to change what is on the wall. Run it when the wall is not needed.
- **ShowMesh never writes the `.avc` and never reads one off the Resolume host.** Upload
  is the only ingestion path. If a step below seems to want ShowMesh to touch the file in
  place, the step is wrong.
- **Restore the composition to its baseline afterwards** and note anything that did not
  come back.
- Record Arena's exact build, the host OS, the layer count and the deck count with the
  results. A number without a topology is not a measurement.

## Setup

1. Coordinator running with `SHOWMESH_RESOLUME_URL` pointing at the playout host's REST
   port. The installation runs 9080, not the documented default; the port is configuration.
2. A principal holding `resolume:action`, and its token in `SHOWMESH_API_TOKEN_FILE` or
   passed with `--token`. The `operator` role carries the scope.
3. The show's `.avc` uploaded:
   `showmeshctl resolume composition upload <path to .avc>`
   then `showmeshctl resolume composition show` to confirm what was stored.

## Part 1: D-2, the observations

| # | What to do | What should happen | What would falsify it |
|---|---|---|---|
| 1.1 | `showmeshctl snapshot` with Arena running | `resolume.reachable` and `resolume.product` current, product reads the real build string | either signal blank, or a `false` that looks measured |
| 1.2 | Quit Arena, wait past one poll interval, `snapshot` again | `resolume.reachable` renders `collection_failed` with a reason | a stale `true`, or a bare `false` with no provenance |
| 1.3 | Relaunch Arena, load the show, wait for the survey | layer readiness, active clip, per-clip connected and transport type all populate | any signal blank, or `ready` on a layer that is plainly not |
| 1.4 | Watch the traffic to Arena over five minutes idle (tcpdump, a proxy, or Arena's own log) | **exactly one `GET /product` per interval and nothing else** | any survey on the timer; any `GET /composition` at all, which is the one that must never appear |
| 1.5 | Bypass a layer in Arena's UI, then force a survey | that layer's readiness reports not ready and **names the failing term** | `ready`, or not ready with no term named |
| 1.6 | Set a layer's master to zero in Arena's UI, force a survey | same as 1.5, naming master | `ready`, which is the silent-failure case this seam exists for |
| 1.7 | Restart Arena and watch the first ~2 seconds | identity does not report `identified` off the ~1.2 s window where REST answers about a composition that is not the show | `identified` inside that window |
| 1.8 | Select a different deck in Arena's UI, force a survey | selected deck follows | it does not, or clip signals silently go absent |

**1.4 is the one that matters most**, because it is the property the whole D-2 design was
reshaped around and the only one a fake Arena cannot really test: the fake counts requests
ShowMesh chose to make, not requests that actually crossed a wire.

## Part 2: D-3, the actions

Every command below is `showmeshctl`, deliberately. The CLI is the "the show is broken and
the UI is down" path and this is the run that proves it works.

| # | What to do | What should happen | What would falsify it |
|---|---|---|---|
| 2.1 | `resolume action list` | the seven actions, each with its safety class and `coordinator-required` | a short list, or a missing safety class |
| 2.2 | `resolume action launch-clip <id>` on a clip that is not playing | `confirmed`, and the clip is visibly playing | `confirmed` with nothing on the wall, which is the failure this whole track is about |
| 2.3 | The same command again, immediately | **`unconfirmable` with a reason**, not `confirmed` | `confirmed`. This is Step 7's 179-microsecond defect and it is the single most important line in this table |
| 2.4 | `resolume action clear-layer <id>` on a layer with a long transition (set it to 2.5 s first) | `confirmed`, and the elapsed time tracks the transition rather than a fixed budget | confirmation well before the transition finishes, which means it is reading something other than the real state |
| 2.5 | Set the same layer's transition to 0.1 s and repeat | confirms much faster | the same elapsed time, which means the deadline is a constant |
| 2.6 | `resolume action launch-clip <id>` for a clip on a deck that is **not** selected | **refused**, naming both decks, and no write reaches Arena | it launches, it silently selects the deck, or it reports a stale reference |
| 2.7 | Select that deck, repeat 2.6 | it launches and confirms | still refused, which means the deck read is not fresh |
| 2.8 | `resolume action select-deck <id>` | confirmed, and Arena shows that deck | |
| 2.9 | `resolume action launch-column <id>` | confirmed | |
| 2.10 | `resolume action set-layer-bypass <id> true` then `false` | each confirms, and the wall follows | |
| 2.11 | `resolume action set-layer-master <id> 0.5`, then a value outside the layer's declared range | the first confirms; the second is **refused naming the declared bound**, never silently clamped | a silent clamp |
| 2.12 | `resolume action blackout` with several layers playing | confirmed, wall dark | `confirmed` with anything still on the wall |
| 2.13 | `resolume action blackout` again, already dark | `unconfirmable`, not `confirmed` | `confirmed` |
| 2.14 | Any action with a token lacking `resolume:action` | `403`, and **no request reaches Arena** | anything reaching Arena |
| 2.15 | Any action with Arena quit | refused or failed with a stated reason, and **the reason contains no URL and no host** | a raw transport error with `http://<host>:<port>/...` in it |
| 2.16 | `resolume action clear-layer <id>` while Arena is deliberately hung (suspend the process) | returns within about a minute with `unconfirmed` and a reason naming the phase, **and the CLI does not time out first** | the CLI reporting a transport failure, which means the two timeouts disagree again |

**2.3, 2.6, 2.13 and 2.16 are the four to run if there is only time for four.** Each one
is a defect this project has already shipped once, in a different subsystem.

## Part 3: the crash-recovery path

Only once that seam is built. Recorded here so the checklist grows with the track rather
than being rewritten.

| # | What to do | What should happen |
|---|---|---|
| 3.1 | Kill Arena mid-show | ShowMesh says Arena is gone, promptly and visibly |
| 3.2 | Relaunch Arena by hand and load the show | ShowMesh notices, and restores the layers that were playing |
| 3.3 | Kill Arena and leave it down | ShowMesh keeps saying so, and **never relaunches the process**. The operator owns the process; ShowMesh owns the show state |

## What this checklist deliberately does not include

- **A show-length soak counting crashes.** Struck by the owner: no build decision hangs
  on the count, since ShowMesh cannot patch Resolume.
- **A vendor report.** Struck: 7.23.2 is a subscription-expired build that no fix reaches
  before the show.
- **Anything touching the timecode path.** That is D0 and it needs an LTC source, which is
  a separate bench.
- **Any `GET /composition`.** Not as a control, not as a comparison, not once. Two reads
  have crashed Arena.

## 6. Results, run 2026-08-15

Environment: Arena **7.23.2 (r51094)**, macOS, development laptop. REST enabled in
`Preferences/server.xml` on port **9080** bound to **0.0.0.0**. Composition
**`Christmas 25.avc`**, 397.8 KiB, canvas 3000x1440, 18 layers in 3 layer groups, 32
columns, 36 clips of which 4 persistent, 3 decks (Main 26 clips, Rest Staging 8,
Downloads 2). Coordinator built from `3daa80b`, run on port 8099 against a scratch data
directory. The composition file was **never written**: verified byte-identical, 407,344
bytes, mtime unchanged, before and after.

### Part 1, the observations

| # | Result | Evidence |
|---|---|---|
| 1.1 | **PASS** | `resolume.product` read `Arena 7.23.2 (r51094)`, `resolume.reachable` current |
| 1.2 | **PASS** | with Arena quit, both signals rendered `state=collection_failed`, `value=None`, `reason="connection refused"`. No fabricated `false`, no stale `true` |
| 1.3 | **PASS** | after upload, per-clip and per-layer signals populated, e.g. `resolume.clip.1733100601401.connected = "Disconnected"`, `source=resolume-survey`, `quality=direct` |
| **1.4** | **PASS, and this is the one a fake could not test** | see below |
| 1.5, 1.6 | not run | needs hand edits in Arena's UI |
| 1.7 | not run | needs sub-second sampling across a restart |
| 1.8 | **PASS**, indirectly | the deck refusal in 2.6 named the correct selected deck read live |

**1.4, measured through a counting proxy in front of Arena for 180 seconds of idle.**
ShowMesh's own steady-state traffic was **exactly one `GET /product` per 10-second
interval and nothing else**. The composition survey ran **once**, at startup, and never
repeated: the per-object counters for all three decks and every surveyed layer stayed at
**1** across every 30-second sample from 30 s to 180 s while `/product` climbed 3, 6, 10,
13, 16. Acceptance criterion 10 holds against a real Arena, not just against the fake.

One artefact worth recording so nobody re-derives it as a defect: the proxy also logged
about one `GET /api/v1` every 13 seconds. That is the **WebSocket upgrade failing against
the proxy**, which does not implement upgrade, and the coordinator logged
`resolume watcher dial failed: websocket: bad handshake` with a rate-limited counter,
which is D-1's own dead-change-signal fix working. It is not ShowMesh traffic to Arena in
normal operation.

### Part 2, the actions

Every one of these is a real dispatch to a real Arena that really changed the wall.

| # | Result | What Arena and ShowMesh actually did |
|---|---|---|
| 2.1 | **PASS** | all seven actions listed with safety class and `coordinator-required`; `blackout` and `clearLayer` exempt, five not |
| 2.2 | **PASS** | `confirmed: the clip is connected and its owning layer reports it as the active clip` |
| **2.3** | **PASS** | the same clip again: `unconfirmable: this clip was already playing before this command was dispatched, so evidence collected afterward cannot be attributed to it`. **Step 7's 179-microsecond defect does not recur, proven live** |
| **2.6** | **PASS** | `refused: this clip belongs to Rest Staging (id 1733100600921), and that deck is not selected (read at 04:41:08); the most recently observed selected deck is Main (id 1733100600915) (as of 04:39:08)`. Both decks named, and **the two timestamps are 2 minutes apart**, which is the fresh deck read doing exactly what it was added for: the decision rested on a read taken at decision time, not on the cached survey |
| 2.12 | **PASS** | `confirmed: every tracked layer reports no active clip` |
| **2.13** | **PASS** | blackout again while already dark: `unconfirmable: every tracked layer already reported no active clip before this command was dispatched` |
| 2.14 | not run | covered by a mutation-verified unit test |
| **2.15** | **PASS** | with Arena quit: `refused: could not read whether this clip's deck is selected: connection refused`. **No URL, no host, no port in the operator-facing text** |
| **2.16** | **PASS** | Arena suspended with `SIGSTOP`: `unconfirmed: dispatching clearLayer ran out of its 5s write budget, so whether Resolume received it is not known: timeout`, returned in **10 seconds**, and the CLI did not time out. The client floor raised itself from the 10 s default to 1 m 20 s and said why |
| exempt class | **PASS** | `blackout` with Arena gone reported `failed: dispatching blackout failed: connection refused`, which is a definite negative rather than a refusal. It **attempted the dispatch** instead of refusing on an unreadable baseline, which is the 2026-08-15 amendment behaving correctly |

### What this changes

D-2 and D-3 are no longer unit-test-only. The four checks most worth having are the four
that reproduce defects this project has already shipped once in another subsystem, and all
four passed against real hardware. **The steady-state measurement is the most valuable
single result**, because the request-count test against the fake counts requests ShowMesh
chose to make, and this counted requests that actually crossed a socket.

### Two findings that came out of running it

**Layer names exist in the composition file and ShowMesh drops them.** Each `<Layer>`
carries a generic `name="Layer"` attribute, but the user-assigned name lives in a nested
`<Param name="Name" T="STRING" value="...">`, the same shape the deck names are already
read from. 13 of this composition's 18 layers have one: `Peak Only`, `All Front Windows`,
`Whole House 1`, `Whole House 2`, `Bedroom Window`, `Center Window`, `Small Front Windows`,
`Side`, `Front`, and others. The parser reads the deck's copy and not the layer's, so
`showmeshctl resolume composition show` prints 18 layers as bare ids and indices, and the
operator has no way to tell which id is the peak of the roof. See ADR-037.

**Clip names are not unique**: 36 clips carry 18 distinct names. Columns carry no name
attribute at all. Both facts constrain what a friendly reference can be, and both are
measured rather than assumed.
