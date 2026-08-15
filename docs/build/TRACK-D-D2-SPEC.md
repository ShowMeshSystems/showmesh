# Track D seam D-2: Resolume observations

Status: specified 2026-08-14. Not built.

Bound by: [ADR-032](../decisions/ADR-032-resolume-composition-configuration-from-file.md),
[ADR-003](../decisions/ADR-003-desired-and-observed-state.md),
[ADR-011](../decisions/ADR-011-context-aware-observability.md),
[ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md),
[ADR-020](../decisions/ADR-020-control-api-shape-and-change-stream.md).

Parent specification: [TRACK-D-ADAPTER-SPEC.md](TRACK-D-ADAPTER-SPEC.md) §3.7, §3.8, §5,
§6.4. Bench evidence: [resolume-control-surface.md](../bench/resolume-control-surface.md).

## 1. What D-2 ships

The Resolume collector stops being a reachability probe and starts describing the
composition — **read entirely `by-id` off the stored id map, with no
`GET /composition` on any path.**

D-2 ships **no action, no write, and no `POST`/`PUT` to Resolume of any kind.**
This is ADR-011's monitor-before-control rule, and it is the same ordering that put
Step 3 before Step 7.

## 2. The prerequisite that is not optional

`internal/coordinator/collector/resolume/composition.go` models null-vs-absent
([`Presence`](../../internal/coordinator/collector/resolume/composition.go)) for
exactly two leaves: `active_clip` and `transport.controls`. Every other envelope
leaf collapses an explicit JSON `null` to a Go zero value.

**That is latent in D-1 and live in D-2.** `bypassed` is a §3.7 conjunction term.
`"bypassed": null` decoding to `false` reads as *not bypassed*, which makes a dark
layer report ready. That is CLAUDE.md's `"ma": null` defect reproduced in a fourth
subsystem, and it would arrive attached to the one signal an operator checks before
a show.

**Every leaf this seam decodes carries `Presence`.** A field that is present-with-null
is `StateUnknown` with a reason, never a zero value, and never a term that silently
satisfies a conjunction.

## 3. What the collector reads: on demand, not on a cadence

**Revised 2026-08-14 on the owner's challenge, and the challenge was correct.** The
first version of this section specified a continuous ~27-request cycle every 10
seconds: all layers, all groups, all decks, every cycle, forever. That was the FPP
collector's shape applied to a system that does not deserve it. **FPP is a healthy
daemon that ShowMesh polls happily. Arena 7.23.2 is an application that segfaults
on its own and cannot be patched before the show.** Reaching for the same pattern
because it is the pattern already in the codebase is how a stability project
becomes a stability problem.

The rule for this seam, and for every later Resolume seam:

> **Continuous traffic to Resolume during a show is `GET /product` and nothing
> else. Everything else is read on demand, and reads only the objects the demand
> names.**

### 3.1 The three read modes

| Mode | Trigger | Reads | Budget |
|---|---|---|---|
| **Liveness** | the 10 s poll timer | `GET /product` | **1 request, 64 bytes.** Unchanged from D-1 and already running against the operator's Arena |
| **Survey** | an explicit operator request, and on a confirmed reconnect | all layers, groups and decks by id; identity sample (§6) | ~24+12 requests, **once**, not on a timer |
| **Targeted** | an action needs confirmation (D-3), or a signal is explicitly refreshed | only the objects that action touched — typically one layer, sometimes its group and one clip | **1–3 requests per action** |

Steady state during a show is therefore **1 request per 10 seconds**, which is the
cheapest probe this API offers and less than any single FPP endpoint this project
already polls.

`by-id` reads survived 209,916 requests and 6.5 GB in five minutes (capture §14.3),
so the survey and targeted modes are not near anything measured. The reason to
avoid a continuous cycle is not that by-id is dangerous — it is that **continuous
traffic to a crashing application buys dashboard freshness nobody asked for**, and
the operator's stated priority is that the show is stable.

### 3.2 Why this does not weaken confirmation

[ADR-003](../decisions/ADR-003-desired-and-observed-state.md) is not negotiable: a
command is not successful because it was sent. Nothing here touches that.

What changes is that confirmation costs **1–3 reads at the moment an operator takes
an action**, instead of falling out of a poll cycle that runs whether anyone is
acting or not. Step 8 already established the pattern for FPP: a post-dispatch
nudge reads the specific thing that must have moved. Resolume's version is cheaper,
because `by-id` addresses exactly the object in question.

### 3.3 Both footprint controls are runtime configuration, and the WebSocket's is mode-shaped

**Owner decision 2026-08-14, recorded as
[ADR-033](../decisions/ADR-033-show-mode.md).** The WebSocket's real answer is not a
static preference but an installation-wide **show mode**: held open in Program Mode,
closed during a show. That is how show control systems in this category are built,
and the same condition is waiting to be invented separately by the FPP collector's
cadence, the audio engine's device-loss policy and the asset sync timer.

**D-2 does not build show mode.** It builds the switch, shaped so a mode can drive
it later: a value the adapter reads rather than a constant it was constructed with,
changeable at runtime without reconstructing the collector or the coordinator. A
static boolean baked in at construction would have to be torn out; a readable value
would not.

**The WebSocket is a switch, and the liveness interval is a number, and neither
requires a rebuild to change.**

Capture §14.4 measured Arena crashing while ShowMesh's only traffic was `/product`
polling and one held WebSocket, and §14's no-client control ran only 7 minutes —
too short to establish that Arena is fine alone. **So it is not established that
ShowMesh's remaining footprint is harmless, and it is not established that it is
harmful either.** Rather than settle that with a bench whose result changes nothing
(§14.5), the seam ships the knobs: if Arena starts dying during show week, the
operator turns ShowMesh's presence down to one 64-byte request per interval, or
off, without waiting for a code change.

Disabling the WebSocket costs the change-signal nudge and nothing else. It must
degrade to "signals are as fresh as the last survey" with that stated on the
observation, never to a stale value presented as current.

### 3.4 What is deliberately never read

**Every clip in the composition.** 252 slots is 252 requests to produce 252
dashboard rows nobody reads. Per-clip signals exist only for clips that are
**currently a tracked layer's `active_clip`**, plus the four `PersistentClips`.
This answers the parent specification's §11 open question "which clips does
ShowMesh track?" — bounded by what is playing, not by what exists.

**Clips on a non-selected deck.** They 404 by construction (capture §16.1, 0/10).
Reading them to discover that is manufacturing absence.

**Counts come from the stored map, not from a constant.** 18 layers, 3 groups and 3
decks are this operator's `Christmas 25`. Nothing in the code may assume them.

## 4. The composition-level terms, and the ladder for reading them

§3.7's conjunction needs `composition.bypassed` and `composition.master`. Both were
only ever read out of `GET /composition`, which ADR-032 decision 2 forbids
outright. There is no confirmed replacement path.

**Implement the ladder, in order, and record which rung answered:**

1. `GET /composition/bypassed` and `GET /composition/master` as single-parameter
   paths. The path inventory (capture §2.3) lists `/composition/{parameter}/reset`,
   so parameter-name addressing under `/composition/` probably exists. If either
   returns a 200 parameter envelope, use it.
2. If step 1 404s, the two terms are **unreadable in 7.23.2 without the forbidden
   call.** Readiness then reports the five readable terms and names the two
   unreadable ones explicitly.

**Rung 2 must not report `ready`.** A conjunction with unread terms is `unknown`
with those terms named, per ADR-011 — stale is never healthy, and neither is
unmeasured. The observation states *which* terms are unknown, because "layer 7
readiness unknown" and "layer 7 readiness unknown because composition master is
unreadable on this Arena version" are different amounts of help at 17:00.

**Which rung answered is logged once at startup and carried on the observation's
reason**, not left to inference. It is the first thing to check if readiness looks
wrong, and it is a fact about the Arena version rather than about the show.

### 4.0 SUPERSEDED 2026-08-14: the ladder is deleted, because the paths it climbs do not exist

**Everything below in §4 is retained for the reasoning and is no longer the
instruction.** Arena ships its own OpenAPI specification on disk (capture §17), and
checking against it answered the question the ladder existed to guess at:

**There is no `GET /composition/{parameter}` path.** No `/composition/bypassed`, no
`/composition/master`, no `/composition/name`. The only `{parameter}`-addressed
composition path in the entire specification is `POST /composition/{parameter}/reset`,
a write. So rung 1 climbs paths the vendor does not document and the predicted answer
is always rung 2.

**The ladder is therefore removed rather than left switched off**, which is
[ADR-032](../decisions/ADR-032-resolume-composition-configuration-from-file.md)
decision 2's own reasoning applied a second time: a bound that still permits a
dangerous call leaves it on the critical path. A configuration flag defaulting to
off is still a flag someone can turn on, and what it turns on is an undocumented
request into the `/composition/...` URL space, next door to the one call measured to
kill Arena. It can only ever 404, so nothing is lost by deleting it and one hazard is.

**The two composition-level readiness terms are therefore permanently unavailable**
by any path this seam may use. Readiness reports the five readable terms and names
the two as unavailable, with the reason, and can never report `ready`. That is
§4's rung-2 behaviour, now unconditional.

**One route remains and is deliberately not taken.** The specification documents
`GET /parameter/by-id/{parameter-id}` for any parameter, composition-level ones
included. The obstacle is unchanged: acquiring a session-scoped parameter id without
the forbidden full read. The only available source is the WebSocket's connect-time
dump, and harvesting an id from it would need the adapter specification's §3.4 rule
("no observed value is ever read out of a WebSocket message") narrowed rather than
broken, since an id is not an observed value and the follow-up read still produces
the evidence. **That is an owner decision, nothing depends on it, and it is not part
of this seam.**

### 4.1 The ladder is attempted at most once per Arena, and is guarded against reaching the full read

**Added 2026-08-14, after seam D-2/A built the ladder.** The ladder assumes Arena
routes `/composition/bypassed` as a parameter path. **That assumption is untested
against a real Arena**, and the failure mode if it is wrong is the worst one
available: a router that prefix-matches `/composition/bypassed` onto `/composition`
serves the 2.26 MB full document, which is the call measured to crash Arena in two
requests. `guardfullcomposition_test.go` cannot catch that, because it checks the
literal ShowMesh sends, not how Resolume routes it.

Three requirements, and none is optional:

1. **At most one attempt per term per coordinator run.** The answering rung is
   cached for the process lifetime. It is not retried on a reconnect, on a survey,
   on a composition upload, or on a change signal. A rung-2 answer is a fact about
   the Arena build, and Arena's build does not change while it is running. If it
   restarts into a different build, `resolume.product` changes and that is the
   revalidation trigger the parent specification's §5 already names.
2. **A hard, small response cap.** A single parameter envelope is a few hundred
   bytes. Cap the ladder's read far below any plausible envelope size, check
   `Content-Length` before reading a byte where the server supplies one, and abandon
   the response without draining it if either exceeds the cap.
3. **An oversize response is rung 2 and a loud warning, never a parse attempt and
   never a retry.** An oversize body means the routing assumption was wrong and
   ShowMesh has just made the dangerous call once. It must never make it twice.

**The ladder ships behind configuration and defaults to OFF.** Its entire value is
upgrading two of seven readiness terms from `unknown` to measured. Weighed against
any chance of issuing the call that crashes Arena, that is not a trade worth making
before the Halloween show, and the owner's stated priority on 2026-08-14 is that the
show is stable rather than that it is fully instrumented.

With the ladder off, readiness reports five of seven terms and names
`composition.bypassed` and `composition.master` as not attempted, with the reason.
That is honest, it is still useful, and it costs zero requests. Turning the ladder
on is a single configuration change for whoever wants the other two terms, on an
Arena they are willing to risk.

**Note what the two missing terms actually are**: a composition-level bypass or a
composition master at zero silences *every* layer at once. That is a failure the
operator sees instantly on the wall, unlike a single bypassed layer among eighteen,
which is the case the conjunction exists to catch and which the five readable terms
do catch.

The same ladder applies to `resolume.composition.name` (§5). If unreadable, the
signal is `StateUnknown` with a reason — **never** backfilled from the uploaded
file, because the file says what ShowMesh *expects*, and reporting an expectation
as an observation is the defect this project has now caught four times.

## 5. Signals

Per the parent specification's §5, unchanged except where noted.

| Signal | Type | Notes |
|---|---|---|
| `resolume.reachable` | health | unchanged from D-1 |
| `resolume.product` | string | unchanged from D-1 |
| `resolume.composition.name` | string | display only, explicitly not identity (§3.8). Subject to §4's ladder |
| `resolume.composition.identified` | evidence | §6. `unknown` while loading, never healthy |
| `resolume.composition.decks` | int | deck count, plus the selected deck's name and id |
| `resolume.layer.<id>.ready` | evidence | §3.7's conjunction, failing term named, unknown terms named separately |
| `resolume.layer.<id>.active_clip` | string / absent | clip id and name, or explicit absence with a reason. **Never omitted** |
| `resolume.clip.<id>.connected` | state | the five-state `ParamState`. **Not a boolean** — a predicate written `== "Connected"` misses `Connected & previewing` |
| `resolume.clip.<id>.transporttype` | string | feeds the parent specification's §8 drift check |

`ObservedAt` is a pointer and `nil` means genuinely unknown. It is never defaulted
to collection time.

## 6. Composition identity (§3.8), made cheap

Assert identity by **resolving a sample of stored clip ids**, never by name.

- Sample: up to 8 non-empty clips of the **currently selected deck**, plus all
  `PersistentClips`. Drawn from one deck for ADR-032 decision 6's reason.
- All resolve → `identified` true.
- Some 404 **and their deck is selected** → `identified` false, with the ids named.
- Some 404 **and their deck is not selected** → this is not an identity result at
  all. Report a deck mismatch naming both decks; leave `identified` untouched.
- Nothing resolves and Resolume is reachable → `unknown`, not false. This is the
  restart load window (capture §10.1) and it resolves itself.

**Cadence: part of a survey, never on a timer.** Identity is checked on a confirmed
reconnect and when the operator asks for a survey. It is not polled: identity does
not change on a 10-second timescale, and §3's rule is that continuous traffic to
Resolume is `/product` alone.

## 7. The load window is a first-class state, not an edge case

Capture §10.1: for ~1.2 s after an Arena restart the REST API answers `200 OK`
describing a composition that is not the show, **carrying the correct composition
name for the last 0.7 s of it.** There is no `loading` field.

D-1 already shipped the lesson that produced this: the resolver keyed off a
transport event and held Arena's empty default for 90 seconds while the real show
was loaded.

So: **reachability is never sufficient.** Between a connect and a successful
identity check, `resolume.composition.identified` is `unknown` and every layer
readiness is `unknown`, both with the load window named as the reason. They are not
`false`, and they are not omitted.

## 8. The WebSocket stays a wake-up

Unchanged from §3.4 and from D-1. **No observed value is ever read out of a
WebSocket message.** A message means "read again now" and nothing else. D-1's
nudged-poll path already exists and is reused; it was measured at 126 ms after
dispatch.

Parameter ids remain unpersistable — `ParameterID.MarshalJSON` returns an error and
that stays. Per §3.6 as corrected, re-resolution after a reconnect is by `by-id`
reads off the stored map, never a composition read.

## 9. Build seams

Two parallel, then one.

| Seam | Files | Contents |
|---|---|---|
| **D-2/A** | `client.go`, `composition.go`, their tests | The by-id GET methods, and the targeted decode types with `Presence` on **every** leaf (§2). Includes §4's ladder as a client method that reports which rung answered |
| **D-2/B** | `resolumewiring.go`, new `idmap.go`, tests | Reading the stored composition config revision into an in-memory tracked-object set: layers, groups, decks, per-deck clips with their deck, persistent clips. Handles "no composition uploaded yet" as a stated state, never an empty map that reads as an empty composition |
| **D-2/C** | new `readiness.go`, `identity.go`, `signals.go`, `collector.go` | §3.7's conjunction **in exactly one function**, §6's identity check, the signal vocabulary, and the poll cycle. Depends on A and B |

## 10. Acceptance criteria

1. **No `GET /composition` on any path.** `guardfullcomposition_test.go` still
   passes and is extended to cover the new by-id call sites.
2. **A layer that is bypassed, and a layer at master 0, both report not-ready while
   `connected` reads `Connected`** — the conjunction demonstrated against the state
   that fools the naive check.
3. **A leaf arriving as explicit `null` produces `unknown` with a reason, not a zero
   value.** Tested per conjunction term, against fixtures, not only against the two
   leaves D-1 already handled.
4. **§4's ladder records which rung answered**, and rung 2 produces `unknown`
   readiness naming the unreadable terms rather than `ready`.
5. **A stored clip id whose deck is not selected produces a deck mismatch naming
   both decks**, and does not move `resolume.composition.identified`.
6. **Between connect and a successful identity check, readiness and identity are
   `unknown` with the load window named**, never `false` and never omitted.
7. **A layer with no active clip publishes an explicit absence with a reason.**
8. Resolume state appears in the Operator UI with provenance and freshness.
9. **Steady-state traffic to Resolume is exactly one `GET /product` per interval**,
   asserted by a test that counts requests against a fake Arena over several
   intervals with no operator action. A survey or a targeted read appearing on the
   timer fails this criterion. This is the criterion the owner cares most about and
   it is the one most likely to regress silently, because the natural way to add a
   signal is to add it to the poll.
10. **The WebSocket can be disabled and the interval raised from configuration**,
    and with the WebSocket off, signals report their real age rather than a stale
    value presented as current.
11. Signals never read continuously are stated with their freshness and the survey
    they came from — never rendered as though they were just measured.

## 11. What D-2 does not do

- **No actions.** That is D-3.
- **No crash recovery.** That is the seam after D-3, because restoring a wall
  requires `launchClip`.
- **No `showmeshctl` action coverage or UI controls.** That is D-4.
- **Nothing about timecode.** D0 is untouched and RES-001's fault behaviour stays
  at L0.
