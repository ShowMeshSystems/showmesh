# Track D seam D-3: the Resolume action vocabulary

Status: specified 2026-08-14. Not built.

Bound by: [ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md),
[ADR-003](../decisions/ADR-003-desired-and-observed-state.md),
[ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md),
[ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md),
[ADR-030](../decisions/ADR-030-operator-ui-is-the-authoring-surface.md),
[ADR-032](../decisions/ADR-032-resolume-composition-configuration-from-file.md),
[ADR-033](../decisions/ADR-033-show-mode.md).

Parent: [TRACK-D-ADAPTER-SPEC.md](TRACK-D-ADAPTER-SPEC.md) §3.4, §3.5, §3.9, §6, §7.
Prior seam: [TRACK-D-D2-SPEC.md](TRACK-D-D2-SPEC.md). Bench: [resolume-control-surface.md](../bench/resolume-control-surface.md).

## 1. What D-3 ships

**The first thing ShowMesh can do to the wall.** Seven logical actions, each
dispatched over REST by object id, each confirmed on evidence that post-dates its
own dispatch, each requiring an authenticated principal holding a named scope.

This is Step 8's shape applied to a second vendor, and the precedent matters: Step 8
shipped eight FPP primitives and its two most expensive defects were both in
*confirmation*, not dispatch. Read §4 before writing any of this.

## 2. The vocabulary

| Action | Call | Confirming evidence |
|---|---|---|
| `launchClip` | `POST /composition/clips/by-id/{id}/connect` | clip `connected` ∈ {`Connected`, `Connected & previewing`} **and** the owning layer's `active_clip.id == id` |
| `clearLayer` | `POST /composition/layers/by-id/{id}/clear` | that layer's `active_clip` reported absent |
| `blackout` | `POST /composition/disconnect-all` | every tracked layer's `active_clip` reported absent |
| `launchColumn` | `POST /composition/columns/by-id/{id}/connect` | column `connected == Connected` |
| `selectDeck` | `POST /composition/decks/by-id/{id}/select` | that deck's `selected == true` |
| `setLayerBypass` | `PUT /parameter/by-id/{bypassed}` | layer `bypassed` equals the requested value |
| `setLayerMaster` | `PUT /parameter/by-id/{master}` | layer `master` equals the requested value |

**Every action takes a ShowMesh reference resolving through the stored id map. No
macro, no API payload and no operator-facing surface ever contains a Resolume path
or a raw object id** (ADR-029).

**No action not in this table.** `POST /composition/action` is undo/redo and is
excluded outright. `DELETE` on any object is excluded. Resolume's API is
unauthenticated and wide open (capture §2.4), so ShowMesh's restraint is the only
restraint that exists on ShowMesh's own traffic.

## 3. Six rules taken from the capture, each of which would otherwise be discovered

### 3.1 Never issue `connect` with body `false`

It returns **204 and does nothing.** It is mouse-up, not disconnect. Measured. The
disconnect operations are `clear` and `disconnect-all` and they are the only ones in
§2. A builder who "adds the off switch" to `launchClip` has added nothing and
reported success.

### 3.2 A `204` is an acknowledgement, never evidence

Resolume is better than FPP here: a command against a target that does not exist
genuinely `404`s. It is not perfect. One `POST …/connect` was observed returning
204 while leaving the clip disconnected, not reproduced in five subsequent attempts,
cause unknown. **Confirmation never reads the dispatch response's status.**

### 3.3 The deadline is derived per action, never a constant

| Action class | Deadline |
|---|---|
| makes a clip start, or sets a parameter | short fixed budget, default **2 s** |
| makes a clip stop (`clearLayer`) | that layer's `transition.duration` + margin, default margin **1 s** |
| `blackout` | **max** `transition.duration` over the affected layers, + the same margin |

A fixed deadline is wrong by 35×: connect confirms in 4 to 64 ms, disconnect
confirms one layer transition later, proven causal by driving the parameter
(0.0 s → 75 ms, 0.5 s → 531 ms, 2.5 s → 2,527 ms, 5.0 s → 4,068 ms). Overshoot past
the transition was 75 to 113 ms in every run, so a 1 s margin is an order of
magnitude of headroom. Both numbers are named constants, never literals.

**A deadline expiring produces `unconfirmed` with a stated reason, never `failed`.**
`failed` is a claim about the show; `unconfirmed` is a claim about ShowMesh's own
evidence pipeline, and this architecture degrades toward the show continuing.

### 3.4 A clip action whose deck is not selected is REFUSED, and never silently fixed

ADR-032 decision 6. A clip id resolves only while its own deck is selected, 30/30
against 0/10. Every stored clip reference carries its deck.

- clip's deck **is** selected → dispatch normally.
- clip's deck is **not** selected → **refuse before dispatching**, with an outcome
  naming the expected deck and the selected one. Not a stale reference, not an
  identity failure, and `resolume.composition.identified` is not touched.
- clip is a `PersistentClip` → no deck term applies; it resolves regardless.

**The adapter must never select the deck for itself.** `selectDeck` exists and is
explicit. An action that silently changes which deck is showing has changed
everything else on the wall as a side effect of one clip launch, and a macro author
who wanted that can write two steps.

**The `selected` reading is fenced like any other evidence.** State which reading
the refusal rested on and when. Deciding "deck mismatch" from a reading the operator
has since changed is Step 7's confirmation defect wearing a third disguise.

### 3.5 `launchClip` on an already-playing clip cannot be confirmed, and says so

The observable is already at the desired value before dispatch, so post-dispatch
evidence proves nothing. That is Step 7's 179-microsecond defect, and no signal in
D-2 can predicate it away.

The action reports **`unconfirmable` with the reason stated**, exactly as
`nextPlaylistItem` and `prevPlaylistItem` do for FPP. Per ADR-029: an action whose
effect cannot be observed reports as unconfirmable, **never as success**, because a
step that always reports success is worse than no step, the operator stops reading
it.

**Do not implement the `transport.position` idea.** The parent specification offers
it as a candidate and explicitly forbids building on that paragraph: it was never
measured and depends on `playmode` and in-point.

### 3.6 Reachable is not ready, and no action dispatches on reachability alone

For ~1.2 s after a restart the REST API answers `200 OK` describing a composition
that is not the show, carrying the **correct composition name** for the last 0.7 s
of it. There is no `loading` field.

**No action dispatches while composition identity is `unknown` or `false`.** It is
refused with the reason stated. D-2 already computes identity; D-3 consumes it and
does not recompute it.

## 4. Confirmation, which is where Step 8's defects lived

### 4.1 Evidence must post-date dispatch, and the fence is on collection time

Step 7 measured a command reporting `confirmed` **179 microseconds** after its own
dispatch, because it compared the current observation to the desired value and never
checked the evidence was newer than the command. Observations stay current for
tens of seconds.

Every confirmation here compares against an observation whose **collection time is
strictly after the dispatch time.** Not observation age, not "the value equals what
I asked for."

### 4.2 The pre-dispatch baseline is mandatory, and a missing one is not a pass

Step 8's `nextPlaylistItem` accepted `idle` as confirmation, correctly, and never
checked the host was not *already* idle. The exact input the capture had already
measured reported `confirmed` while nothing happened.

So: **capture a pre-dispatch baseline for every action**, and where the confirming
predicate could already be true beforehand, the action is `unconfirmable` (§3.5) and
not `confirmed`. If the baseline cannot be read, the action does not silently
proceed to a confirmation that cannot mean anything; it reports why.

### 4.3 Confirmation is a targeted read, not a poll cycle

D-2's rule holds: continuous traffic is one `GET /product` per interval. Confirmation
adds **1 to 3 by-id reads of the objects this action touched**, at the moment of the
action. `blackout` is the exception and reads every tracked layer; that is bounded
by layer count and happens once per blackout.

Do not add a signal to the poll loop to make confirmation easier. There is a test
that fails the build if steady-state traffic grows.

### 4.4 One resolver, one authority

The WebSocket is a wake-up and never an authority; **no observed value is ever taken
from a WebSocket message.** Confirmation reads through the same by-id path D-2 uses
and through D-2's own resolution, never a second implementation.

Step 7's `precedence.go` defect is the reason this is stated: a shared resolver
existed, its own comment claimed inconsistent application was impossible, and the
confirmation path one file over took the first matching row instead.

## 5. Authorization, audit, and the safety class

### 5.1 A new scope

`resolume:action`, added to `internal/coordinator/identity` alongside
`ScopeFPPCommand`, and a member of `operatorActionScopes` so the operator role
carries it. Reads stay open (ADR-024). Every action requires the scope.

### 5.2 The safety class is a three-valued enum whose zero value fails the build

Mirror `fppSafetyClass` exactly, including the test that fails the moment a
registry entry carries the undeclared zero value. "Nobody decided" must not be able
to masquerade as "decided no."

| Action | Class | Why |
|---|---|---|
| `blackout` | **exempt** | ADR-024 decision 11's own named case. Never refused for want of an audit write |
| `clearLayer` | **exempt** | a stop, scoped to one layer |
| `launchClip` | not exempt | refusing a start costs only that the clip does not start, which is the safe direction |
| `launchColumn` | not exempt | as above, and it starts more at once |
| `selectDeck` | not exempt | changes everything on the wall; refusing it changes nothing |
| `setLayerBypass` | not exempt | **see below** |
| `setLayerMaster` | not exempt | **see below** |

**`setLayerBypass` and `setLayerMaster` are the interesting entries and the reason
this table is written out.** Each can silence a layer *or* light one up, and the
class is per action, not per invocation. Exempting them to protect the silencing
direction would exempt the lighting direction with it, which is Step 8's exact
defect: a doc comment claimed `stopPlaylist` was the only exempt member while the
code exempted all eight. **The blackout path is `blackout`, and it is exempt.**

### 5.3 Every action declares `coordinator-required`

Per ADR-016 and the parent specification's §3.9. The Resolume adapter is
coordinator-hosted, Resolume holds no fallback, and the composition is reachable
only through the coordinator's network path. **The vocabulary supplies the label; a
macro author never has to know.**

The paired consequence must be stated rather than discovered: with the coordinator
stopped, `blackout` does not run and the wall keeps showing what it was showing.
That is correct behaviour under standing constraint 6, and an operator procedure has
to exist for it, which is Resolume's own interface.

### 5.4 Program Mode and Show Mode do not gate actions

ADR-033 decision 4. No mode may refuse, delay, or degrade any action in §2. D-3
reads no mode value at all. This is stated so that a later seam adding mode-aware
behaviour has to argue against a written decision rather than fill a silence.

## 6. Build seams

Two, parallel, against the contract in §2 and §7.

| Seam | Files | Contents |
|---|---|---|
| **D-3/A** | `internal/coordinator/collector/resolume/action*.go` (new) + tests | The action registry, dispatch, the derived deadline, the pre-dispatch baseline, the deck refusal, and confirmation. Declares the interface the API consumes |
| **D-3/B** | `internal/coordinator/api/resolumeaction*.go` (new), `internal/coordinator/identity/types.go`, `api/openapi.yaml`, `cmd/showmeshctl/` + tests | The scope, the HTTP surface, audit, the OpenAPI contract and its conformance test, and `showmeshctl` coverage. Consumes A through a consumer-side interface it declares itself |

`showmeshctl` coverage is **not deferred to D-4** (ADR-030): the CLI is the "the show
is broken and the UI is down" path, which makes it a tested emergency path rather
than contract hygiene. **D-4 is the Operator UI only.**

Client timeouts are derived from the server's deadline, with a test that fails if
one is ever set below it. Step 7 shipped a server holding an unconfirmed response
for 20 s while the CLI gave up at 10 and the browser at 15, making `unconfirmed`
unreachable by both shipped clients.

## 7. Acceptance criteria

1. A clip launch, a layer clear and a column trigger are each **confirmed on
   evidence collected after dispatch**, with a test that fails if the fence is
   removed.
2. **A clip launch against an already-playing clip reports `unconfirmable` with a
   reason, not `confirmed`.**
3. **A clip action whose deck is not selected is refused before dispatch**, naming
   both decks, leaving `resolume.composition.identified` untouched, and issuing no
   HTTP request to Resolume at all.
4. A `clearLayer` on a layer with a 2.5 s transition confirms, and the same action
   with the transition at 0.1 s confirms faster, **demonstrating the deadline is
   derived rather than constant**.
5. A deadline expiry produces `unconfirmed` with a reason, never `failed`.
6. **No action dispatches while composition identity is unknown or false.**
7. Every action requires `resolume:action`; a principal without it gets `403` and no
   HTTP request reaches Resolume.
8. **Every registry entry declares a safety class**, and the build fails if one does
   not.
9. `blackout` still dispatches when the audit store is failing; `launchClip` does
   not.
10. Steady-state traffic is still exactly one `GET /product` per interval. D-2's
    test still passes unchanged.
11. `showmeshctl` can drive every action in §2, and its timeout is derived from the
    server's deadline.
12. **No `GET /composition` on any path.** The AST guard still passes.

## 8. What D-3 does not do

- **No Operator UI.** That is D-4.
- **No macros.** Track A owns those. D-3 supplies actions a macro will later call.
- **No crash recovery.** That is the seam after D-3, and it needs `launchClip`.
- **No reconciler.** Nothing closes desired and observed state on a loop. ShowMesh
  does not become a second scheduler, and it does not become one for Resolume
  either.
- **Nothing verified against real hardware.** Unit-test evidence only, as with D-2.
