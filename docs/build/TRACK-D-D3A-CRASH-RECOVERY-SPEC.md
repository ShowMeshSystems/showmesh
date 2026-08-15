# Track D seam D-3a: Arena crash recovery

Status: **specified 2026-08-15. Not built.** Three decisions in §7 need the owner
before this is buildable.

Bound by: [ADR-003](../decisions/ADR-003-desired-and-observed-state.md),
[ADR-001](../decisions/ADR-001-fpp-is-authoritative.md),
[ADR-011](../decisions/ADR-011-context-aware-observability.md),
[ADR-016](../decisions/ADR-016-controlled-devices-and-control-providers.md),
[ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md),
[ADR-029](../decisions/ADR-029-logical-actions-and-integration-bindings.md),
[ADR-032](../decisions/ADR-032-resolume-composition-configuration-from-file.md),
[ADR-033](../decisions/ADR-033-show-mode.md).

Prior seams: [D-2](TRACK-D-D2-SPEC.md), [D-3](TRACK-D-D3-SPEC.md). Bench:
[resolume-control-surface.md](../bench/resolume-control-surface.md).

## 1. Why this seam exists, and what replaced it

This is what the owner put in place of the vendor report and the show-length soak, both
struck on 2026-08-14. Neither would have changed a build decision: ShowMesh cannot patch
Resolume, so the response to two crashes an evening and to ten is the same response.

**What ShowMesh can do is survive one.** Arena goes away, ShowMesh says so promptly, and
when Arena comes back, however it got back, the layers that were playing are playing
again.

**Nothing here relaunches the Arena process.** A host-level watchdog was proposed and
rejected the same day: its failure mode is relaunching Arena at a moment a human
deliberately had it stopped, which trades a way to break a working Arena for a few seconds
of recovery. **The operator owns the process; ShowMesh owns the show state.**

## 2. The three facts from the capture this seam is built on

**Arena comes back with nothing playing.** Measured in D-1. Resolume will not resume the
show by itself, which is the entire reason this seam is not a no-op.

**Reachable is not ready.** For about 1.2 seconds after a restart the REST API answers
`200 OK` describing a composition that is not the show, and it carries the **correct
composition name** for the last 0.7 s of that. There is no `loading` field. A restore that
fires on reachability restores into the wrong composition.

**Parameter ids die on every restart and object ids survive.** 0 of 14 parameter ids and
14 of 14 clip ids. So a restore may address clips and layers by their stored ids and must
re-resolve anything parameter-shaped.

## 3. What ShowMesh knows about what was playing, and what it does not

This is the hard part of the seam and it must not be papered over.

D-2's `SurveySnapshot` carries identity and the selected deck. **It does not carry the
per-layer active clip.** That lives in the observations the collector publishes
(`resolume.layer.<id>.active_clip`) and in nothing this seam can currently read directly.

So the recovery record has to be built, and it has exactly two honest sources:

| Source | What it tells you | When it is wrong |
|---|---|---|
| The most recent survey's per-layer active clip | what Arena reported | surveys are event-driven, so under Show Mode with the WebSocket closed this can be hours old |
| Every `launchClip` and `clearLayer` ShowMesh itself confirmed | what ShowMesh asked for and saw happen | says nothing about anything the operator launched in Arena's own UI |

**Neither source covers a clip the operator launched by hand since the last survey**, and
there is no third source, because enumerating the composition is what ADR-032 forbids.

That is not a reason to skip the seam. It is a reason the record must state its own
provenance per layer, and the restore must say what it restored and on what evidence,
rather than presenting a partial restore as a complete one. **This project has decided
four times that absence of evidence is not evidence of absence**, and "ShowMesh has no
record for layer 7" must never render as "layer 7 was dark".

## 4. The recovery record

A per-layer entry, held by the adapter, updated from both sources above:

- layer id
- clip id, or a stated "known dark", or a stated "never observed"
- the time that entry was established, and which source established it

Rules:

1. **A confirmed D-3 action updates the record at confirmation, never at dispatch.** ADR-003.
2. **A survey updates every layer it read**, and only those.
3. **An unreadable layer leaves its entry alone and marks it unknown**, never dark.
4. The record is **in memory and not persisted**. A coordinator restart loses it, which is
   correct: a restore is a claim about what was on the wall a moment ago, and a record
   that outlived the coordinator is a claim nobody can date. Say this in the code, because
   persisting it is the obvious "improvement".

## 5. Detecting the crash, and detecting the return

**Gone:** `resolume.reachable` transitions to a collection failure. D-2 already produces
this and D-1's own review already fixed the case where it was silent. This seam adds the
operator-visible statement, not the detection.

**Back, and this is where the seam earns its keep.** The return is a three-term gate, all
of which must hold before a single restore write is issued:

1. REST answers.
2. **Composition identity is confirmed**, by D-2's own check, not by a name comparison.
   §2's 1.2-second window is exactly what this defends against, and the composition name
   is present and correct for the last 0.7 s of it, so a name check passes at the worst
   possible moment.
3. A **debounce** past the identity confirmation, so a restore is not issued into a
   composition still loading. D-1's convergence window is the existing shape.

**No restore fires on reachability alone.** That is D-3 §3.6's rule, and this seam is the
case that rule was written for.

## 6. The restore itself

Per layer with a usable record entry, in layer order, using D-3's `launchClip`:

- **A layer whose entry is "known dark" gets nothing.** Not a `clearLayer`. Arena came
  back dark; issuing a clear to reach a state it is already in is a write with no purpose
  and a confirmation that cannot mean anything.
- **A layer whose entry is unknown gets nothing, and that is reported.**
- **A layer already playing the recorded clip gets nothing.** The operator may have got
  there first, or Arena may have restored more than expected.
- **A layer playing something else gets nothing, and that is reported.** ShowMesh does not
  overwrite a clip somebody else launched during the recovery window. This is the rule
  that stops a restore fighting an operator who is already fixing it by hand.
- The deck term applies exactly as in D-3 §3.4: a clip whose deck is not selected is not
  launched and is reported. **The restore does not select a deck**, for the same reason a
  single clip launch does not.

Each restore is an ordinary D-3 dispatch and inherits its confirmation, its deadline and
its budget. **The whole restore is bounded** and reports per layer: restored, skipped with
a reason, or failed with a reason. A partial restore is reported as partial.

## 7. Three decisions the owner has to make, because guessing them wrong is expensive

### 7.1 Is the restore automatic, or does it wait for the operator?

Automatic is what "restores the layers that were playing" says, and requiring the operator
to notice defeats the point during a show. But it is a write, issued from a record that
§3 says can be incomplete, at a moment when a human may already be fixing things.

**Recommendation: automatic, with §6's five skip rules doing the protecting**, and a
`showmeshctl` command to run it manually for the case where it was skipped or declined.

### 7.2 Does Show Mode gate it?

This is the first genuinely good use of [ADR-033](../decisions/ADR-033-show-mode.md)'s mode
that has come up, and the mode **is not built** (D-2 built the seams that read it, not the
mode itself). During a show, automatic recovery is the whole point. While programming, a
coordinator relaunching clips underneath the operator is exactly what nobody wants.

ADR-033's clause that mode never gates blackout, stop or power-off does not apply: a
restore is not in that class.

**Recommendation: armed in `show`, and in `program` it detects and reports but does not
write.** That makes this seam the one that builds the mode, which is a larger scope than
"crash recovery" reads like, and the owner should know that before it starts.

### 7.3 Does it need a new scope, or does it use `resolume:action`?

The restore is a sequence of `launchClip` calls, so `resolume:action` covers the manual
command. But an **automatic** restore has no principal, which is the exact situation the
Step 7 startup-migration lesson is about: fail-closed protects the operator from an
unaccountable actor, and where there is no actor it protects nobody.

**Recommendation: the automatic path acts as a named system principal** so the audit trail
says who, and it is never refused for want of an audit write, because refusing it leaves
the wall dark, which is the same failure direction as refusing blackout.

## 8. Acceptance criteria

1. Arena is killed mid-show and ShowMesh reports it as gone, promptly and visibly, with a
   reason and provenance rather than a bare `false`.
2. Arena returns, the show is loaded, and the layers that were playing are playing again.
3. **No restore write is issued inside the post-restart window where REST answers about a
   composition that is not the show**, with a test that fails if the gate drops to
   reachability or to a name comparison.
4. A layer the operator has already relaunched by hand is left alone, and reported.
5. A layer with no record entry is reported as unknown, never restored and never treated
   as dark.
6. A partial restore reports as partial, per layer, with a reason on every skip.
7. **Nothing in the seam starts, restarts or signals the Arena process**, enforced by the
   same kind of structural guard as the positional and OSC guards, because "just relaunch
   it" is the obvious thing a future builder adds.
8. Steady-state traffic is unchanged: still exactly one `GET /product` per interval.

## 9. What this seam does not do

- **No process supervision of any kind.** See §1.
- **No persistence of the recovery record.** See §4.
- **No reconciler.** Nothing closes desired and observed state on a loop; this fires on a
  transition and stops. ShowMesh does not become a second scheduler for Resolume any more
  than it does for FPP.
- **No Operator UI.** That is D-4.
