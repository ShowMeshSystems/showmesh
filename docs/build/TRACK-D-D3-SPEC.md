# Track D seam D-3: the Resolume action vocabulary

Status: specified 2026-08-14. **Built 2026-08-15, reviewed the same day, and amended by
what the review found.** Four sections below carry a dated amendment: §3.4's deck term now
costs one by-id read rather than zero requests, §3.6's identity gate does not refuse an
exempt action for a stale reading, §4.2's baseline failure does not refuse an exempt
action, and §4.3's confirmation is a backed-off poll rather than a single read. Unit-test
evidence only; nothing here has run against the real Arena. See
[the live-verification checklist](../bench/TRACK-D-LIVE-VERIFICATION.md).

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

> **Amended 2026-08-15 by the review, and it changes acceptance criterion 3.** As first
> built, the deck term read `selected` off the cached survey snapshot with no age check
> at all. Surveys are event-driven only, and under [ADR-033](../decisions/ADR-033-show-mode.md)
> Show Mode the WebSocket is closed, so on a stable Arena that snapshot freezes
> indefinitely: a 40-minute-old reading would refuse a clip on a deck the operator
> selected in Arena's own UI half an hour ago, which is verbatim the disguise this
> section names. An age fence was the obvious fix and it is the wrong one, because any
> threshold either refuses legitimate actions or trusts stale ones.
>
> **The deck term now rests on a targeted `by-id` read of the clip's own deck, taken at
> decision time**, and the refusal states that read's timestamp. One small read answers
> the question directly and removes the tuning problem. So criterion 3's "issuing no HTTP
> request to Resolume at all" becomes **exactly one `by-id` deck read, no clip read, and
> no write.** Nothing is dispatched and no write reaches Resolume, which is what that
> criterion was protecting.

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

> **Amended 2026-08-15.** The identity reading needs the same freshness fence the deck
> term needed, and unlike the deck term it cannot be re-read cheaply: the identity check
> runs inside the collector's own survey, roughly 24 to 36 requests, and this section is
> explicit that D-3 consumes identity rather than recomputing it. So identity carries an
> **age fence set to `DefaultSurveyValidFor`**, deliberately the same 15 minutes at which
> the dashboard renders that reading stale, so an action refuses exactly when the operator
> can see why. A stale reading also asks for a fresh survey, so the refusal is one attempt
> rather than a permanent state.
>
> **An exempt action is not refused for a stale reading.** Staleness is a fact about
> ShowMesh's own evidence pipeline, and refusing a stop for want of ShowMesh's own
> evidence is the fail-closed inversion [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md)
> decision 11 already settled for the audit write. Without this carve-out, a coordinator
> whose WebSocket has been closed for fifteen minutes refuses `blackout`, and a refusal
> fires no fallback (decision 7), so the operator is worse off than during a coordinator
> outage. An identity of `unknown` or `false` is a fact about the composition rather than
> about our pipeline, so it still refuses every action including the exempt ones.

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

> **Amended 2026-08-15, and this is an owner-reversible decision rather than a defect
> fix.** "Reports why" was implemented as a refusal for every action, which is right for a
> start and wrong for a stop. Refusing `blackout` because a *read* failed is the same
> fail-closed inversion ADR-024 decision 11 settled for the audit write, and CLAUDE.md's
> constraint 24 says blackout is never refused for want of bookkeeping.
>
> **A not-exempt action still refuses**, because refusing a start costs only that the
> start does not happen. **An exempt action (`blackout`, `clearLayer`) dispatches anyway
> and reports `unconfirmable` with the reason**, and runs no confirmation poll, since
> without a baseline the poll could not mean anything and would cost requests. Blackout's
> confirming predicate is absolute (every tracked layer reports no active clip) rather
> than a delta, so the baseline feeds only the already-satisfied test.
>
> The whole baseline phase is also now bounded by one budget rather than only a
> per-request timeout: see §4.5.

### 4.3 Confirmation is a targeted read, not a poll cycle

D-2's rule holds: continuous traffic is one `GET /product` per interval. Confirmation
adds **1 to 3 by-id reads of the objects this action touched**, at the moment of the
action. `blackout` is the exception and reads every tracked layer; that is bounded
by layer count and happens once per blackout.

Do not add a signal to the poll loop to make confirmation easier. There is a test
that fails the build if steady-state traffic grows.

> **Amended 2026-08-15: "1 to 3 by-id reads at the moment of the action" was never
> achievable, and the first build was 100 times over its own comment.** Confirmation
> cannot be a single read, because the evidence arrives when the transition finishes and
> that is up to seconds later; it has to be a poll. As first built the poll ran at a flat
> 50 ms, so `launchClip` issued about 41 reads across its 2 s deadline and `blackout` at
> 18 layers could reach the low thousands, each layer read being roughly 62 KB on the
> operator's composition. The constant's own doc comment claimed "tens, not thousands".
> Arena's crash is a use-after-free in its own HTTP response serialiser sensitive to
> connection churn, so understating this seam's footprint is the wrong direction to be
> wrong in.
>
> **The poll now starts at 25 ms and doubles to a 500 ms cap.** A single-object action
> went from 41 reads to 9 across the same deadline, and a test fails the build if it
> exceeds 12. Blackout at 18 layers is 7 attempts at a 1.5 s derived deadline and 26 at
> the 11 s unknown-transition fallback. The read set per attempt is unchanged; what
> changed is how often it repeats.

### 4.5 One budget bounds the whole dispatch, not just the confirmation

**Added 2026-08-15.** As first built, `MaxActionConfirmDeadline` was documented as the
upper bound on how long `Dispatch` could take, and the API sized its HTTP write deadline
from it. It was not that bound. The pre-dispatch baseline phase ran outside it, bounded
only per request, so `blackout` at 18 layers could spend 90 seconds before dispatch was
even attempted; the confirmation loop tested its deadline only *between* attempts, so one
in-flight blackout check could add another 90; and the two post-dispatch bookkeeping
writes spent up to 20 more. Measured on a real clock at 3 layers, a 1.1 s derived deadline
returned at 2.256 s, an overrun that scales linearly in layer count.

That is Step 7's two-timeouts defect, in the file that cites Step 7's two-timeouts defect,
and it lands hardest on `blackout`, the one action ADR-024 decision 11 insists must get
through.

So the bound is now composed and every phase enforces its own share:

| Term | Value | Bounds |
|---|---|---|
| `MaxBaselinePhaseBudget` | 5 s | the whole baseline phase, checked before each read, not one read within it |
| `MaxWritePhaseBudget` | 5 s | the dispatch write |
| `MaxActionConfirmDeadline` | 30 s | the confirmation poll, clamped to the window |
| **`MaxDispatchDuration`** | **40 s, the sum** | `Dispatch` end to end, held on the dispatcher's own clock so a fake clock can drive it |

The server's HTTP write deadline is sized from `MaxDispatchDuration` plus its own
bookkeeping budget plus a margin, and the CLI's floor from that. **Two timeouts on
opposite sides of one contract are a single decision**, and there are now three sides:
the dispatcher, the handler, and the client.

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
   both decks, leaving `resolume.composition.identified` untouched, and issuing
   ~~no HTTP request to Resolume at all~~ **exactly one `by-id` deck read and no
   write** (amended 2026-08-15, see §3.4).
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
13. **Added 2026-08-15.** No positional addressing and no OSC, both enforced by AST and
    import guards rather than by the doc comment that previously stated them. The D-1
    review flagged the gap; writes are what made it worth closing.
14. **Added 2026-08-15.** `Dispatch` returns within `MaxDispatchDuration` for every
    action including `blackout` against every tracked layer, with a test that fails if
    any phase's budget check is removed.

## 8. What D-3 does not do

- **No Operator UI.** That is D-4.
- **No macros.** Track A owns those. D-3 supplies actions a macro will later call.
- **No crash recovery.** That is the seam after D-3, and it needs `launchClip`.
- **No reconciler.** Nothing closes desired and observed state on a loop. ShowMesh
  does not become a second scheduler, and it does not become one for Resolume
  either.
- **Nothing verified against real hardware.** Unit-test evidence only, as with D-2.
  [The live-verification checklist](../bench/TRACK-D-LIVE-VERIFICATION.md) is what would
  change that, and it has not been run.

## 9. One open question, for the owner

**Added 2026-08-15. Both reviewers reached it independently and it is not settled.**

§2 says every action takes a ShowMesh reference resolving through the stored id map, and
that no operator-facing surface ever contains a raw object id. As built, `ActionParams`
holds `ObjectID`, which is the numeric Resolume id parsed out of the `.avc`, and the wire
contract passes that number as a string while describing it as "the ShowMesh reference
this action resolves through the stored composition id map". Resolving a raw Resolume id
against a map keyed on raw Resolume ids is not an indirection.

The consequence is the one [ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md)
exists to prevent: re-authoring the composition changes ids, and every stored binding
starts silently refusing.

Two honest readings, and the difference is real work:

- **D-3 scoped "reference" to mean "the id out of the uploaded file."** Then nobody
  recorded that scoping, and the OpenAPI text should stop describing an indirection it
  does not have.
- **The reference layer is missing** and belongs with the Show object in D-4 or
  [Track E](TRACK-E-show-authoring-and-assets.md), with D-3's contract written so adding
  it later is additive.

Nothing was changed in the code pending that decision.
