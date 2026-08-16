# Track D seam D-3a: Arena crash recovery

Status: **specified 2026-08-15. Not built.** §7's three decisions were **answered by the
owner the same day** and are folded in below. A pre-build review on **2026-08-16** raised
six more, all **answered by the owner the same day** and folded in as §10; two of them
changed this seam's scope (the restore's coverage, and the toggle's UI control) and one
reversed part of §1. **Sequencing, owner 2026-08-16: [ADR-037](../decisions/ADR-037-resolume-references-are-names-not-ids.md)
seam B lands before this seam**, because this seam's per-layer report is operator-facing
and would otherwise print raw object ids.

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

**Amended 2026-08-16 by the owner, and the amendment is about where a watchdog may live,
not about building one now.** The rejection above stands for this seam and for this
repository. It is explicitly open to revisit **after the Resolume upgrade planned for
November**, because that upgrade may or may not fix the crash, and if it does not, a
watchdog becomes worth its cost. **If one is ever built it is its own small repository,
never part of core.** What this seam owes that future is that it must not be the thing
standing in the way: see §10.6 for the three properties that keep an external watchdog a
~100-line program rather than a core change.

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

**Corrected 2026-08-16, and the correction is sharper than the row above says.** The survey
source is not "possibly hours old". Read against the code: a survey runs on a WebSocket
connect, on an explicit `RequestSurvey`, on an unreachable-to-reachable liveness
transition, and on a composition upload. **It never runs on a timer, and a WebSocket
change message does not trigger one** (it triggers a `/product` nudge only, `resolumewiring.go`
`OnChange`). The live verification measured exactly this: the survey ran once at startup
and never repeated across 180 seconds.

So in an ordinary evening with the WebSocket held open, the survey source contributes
**one reading taken at coordinator startup, before the show ran.**

**And the consequence that decides this seam's scope:** [Track D](TRACK-D-resolume.md)'s
path one is that **LTC launches the timeline clips, not ShowMesh.** Those launches appear
in neither source. So a crash mid-timeline finds the record holding only the layers
ShowMesh drove explicitly, which is countdowns, resting visuals, pre-show text and
blackout. See §10.1 for the owner's decision on that, which is to accept it.

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

**Back, and this is where the seam earns its keep.** The return is a four-term gate, in
this order, all of which must hold before a single restore write is issued:

1. REST answers.
2. A **settle delay** before ShowMesh issues anything beyond the liveness probe that
   noticed the return. **Default 8 seconds, configurable** (owner, 2026-08-16: "give it
   like 5-10 seconds before we start hammering requests at it"). **SHOWMESH GUESS, NOT
   MEASURED**, in the same sense as `DefaultTransitionSurveyMinInterval`: it is chosen to
   sit comfortably past §2's measured 1.2-second wrong-composition window with room for a
   slower host, and it has not been timed against a real Arena restart. The playout-host
   verification is where it gets a measured value.
3. **Composition identity is confirmed**, by D-2's own check, not by a name comparison.
   §2's 1.2-second window is exactly what this defends against, and the composition name
   is present and correct for the last 0.7 s of it, so a name check passes at the worst
   possible moment. Confirming identity requires a survey, which is what term 2 is
   waiting to be safe to run.
4. The identity confirmation is **from a survey that ran after this return**, never from a
   cached snapshot predating the crash.

**No restore fires on reachability alone.** That is D-3 §3.6's rule, and this seam is the
case that rule was written for.

**The restore's survey bypasses the transition-survey throttle** (owner, 2026-08-16).
`DefaultTransitionSurveyMinInterval` is 1 minute and exists to stop a flapping Arena
drawing a fresh ~30-request survey on every bounce. A recovery gate that respects it
cannot fire on a second crash inside that minute, and the measured crash intervals go down
to 36 seconds, so respecting it means the restore silently does not happen in exactly the
case it was built for. Recovery therefore requests its survey through the same explicit
path the limiter already exempts. The settle delay above is what keeps that from being a
licence to hammer a just-restarted Arena: **the bypass changes when the survey may run,
never how many requests it makes.**

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

## 7. The three decisions, answered by the owner 2026-08-15

### 7.1 Automatic, with a toggle

**The restore runs automatically, and it has an operator toggle.** Not gated on anything
else. §6's five skip rules are what stop it fighting a human who is already fixing things.

The toggle is ordinary configuration: revisioned, audited, readable and writable through
the API and `showmeshctl`, and its current value is visible on the dashboard. **Its state
must be visible without being hunted for**, because an operator who thinks recovery is
armed and finds out otherwise mid-show is worse off than one who knows it is off.

**The working switch ships in this seam, not in D-4** (owner, 2026-08-16: "rather it get
done when we're working on that system than forgetting about it"). This resolves a
contradiction the pre-build review found between criterion 9, which required dashboard
visibility, and §9, which excluded all Operator UI work. **The exception is narrow: this
seam ships the toggle's own read and write control and nothing else of Track D's UI.** The
clip and layer dropdowns, the Go button, and every other Resolume surface remain D-4's.
The API and CLI still come first per ADR-030; the control mirrors them and holds no logic.

The manual path exists either way: a `showmeshctl` command that runs the same restore on
demand, for the case where the toggle is off, or where the automatic attempt skipped
layers and the operator wants a second pass after fixing the cause.

### 7.2 Show Mode does not gate it

**Answered: no.** The earlier recommendation here was that the restore should be armed in
`show` and inert in `program`, which would have made this seam the one that builds
[ADR-033](../decisions/ADR-033-show-mode.md)'s mode. The owner's answer is simpler and it
removes that scope: **auto restore may happen at any time, and the toggle is the control.**

So this seam does **not** build Show Mode, does not read a mode value, and does not
create a private boolean that means "we are running a show now" (ADR-033 exists precisely
to stop four subsystems inventing that separately). The mode remains unbuilt and unblocked.

### 7.3 The automatic path acts as a named system principal

The manual command is an ordinary authenticated write needing `resolume:action`. The
automatic restore has no human behind it, which is the Step 7 startup-migration situation
exactly: fail-closed protects the operator from an unaccountable actor, and where there is
no actor it protects nobody.

So the automatic path acts as a **named system principal** so the audit trail says who,
and it is **never refused for want of an audit write**, because refusing it leaves the
wall dark, which is the same failure direction as refusing blackout.

**The principal is built in and cannot be deleted, disabled, or stripped of its scope**
(owner, 2026-08-16: "not deletable, baked in, only off when recovery is off"). It is
visible wherever principals are listed and its actions appear in the audit trail like any
other principal's, so the operator can see what it did. What is not offered is a second
way to turn recovery off. **A deletable recovery principal is a silent disarm**: an
operator tidying the principal list removes it, the toggle still reads armed, and the
discovery happens during a show. The toggle is the one off switch, and §7.1 requires its
state to be visible without being hunted for precisely so that switch is never a surprise.
Its scope grant is exactly `resolume:action` and nothing wider.

## 7a. Controlling a Resolume machine that is not the coordinator, answered by measurement

The owner asked what the plan is for driving the Resolume host when it is a different
machine, and whether that needs a helper app. **It does not, and this is measured rather
than assumed.**

Arena's own REST server is a network server. On the operator's installation
`Preferences/server.xml` reads `enabled="1" port="9080" address="0.0.0.0"`, so it listens
on every interface, and the coordinator reaches it over the network exactly as it reaches
an FPP host. Everything D-1, D-2 and D-3 do, and everything this seam does, works from
another machine with no ShowMesh code on the Resolume host at all. **`SHOWMESH_RESOLUME_URL`
is the whole integration.**

What a helper on the Resolume host would add, and why none of it is needed now:

| Would add | Verdict |
|---|---|
| Telling apart "Arena crashed" from "the machine is off" from "the network is down" | Genuinely useful and **not** available remotely: all three look identical to the coordinator. Worth revisiting **only if** the operator finds that distinction matters in practice |
| Faster crash detection | The poll interval already bounds this, and a few seconds does not change the recovery |
| Relaunching Arena | **Explicitly rejected** (§1). Not a reason to build a helper |
| Reading the `.avc` off the host | **Forbidden** by ADR-032. Upload is the only ingestion path |

**Recommendation: no helper app.** The one real gap is the failure-cause distinction, and
the honest response to it is that ShowMesh reports "Resolume is unreachable" with the
evidence it actually has, rather than guessing which of three causes it is. That is
ADR-011's rule and it is the same thing the FPP collector already does for an unreachable
host. A helper is a new deployment target on a Windows or macOS box, which is a real cost,
and it should be paid only against a named problem.

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
7. **No code in this repository starts, restarts or signals the Arena process**, enforced
   by the same kind of structural guard as the positional and OSC guards, because "just
   relaunch it" is the obvious thing a future builder adds. **Scoped to this repository
   deliberately** (§1's 2026-08-16 amendment): the guard exists to keep process
   supervision out of core, not to declare the idea forbidden everywhere.
8. Steady-state traffic is unchanged: still exactly one `GET /product` per interval,
   which is now a measured property of the real adapter and not only a unit test (see
   [the live verification](../bench/TRACK-D-LIVE-VERIFICATION.md) §6).
9. **The toggle is readable and writable through the API and `showmeshctl`, is audited,
   and is readable and settable from the dashboard**, per §7.1. The UI control ships in
   this seam.
10. **With the toggle off, a crash and return produce the report and no write**, with a
   test that fails if any restore write escapes while it is off.
11. **No request beyond the liveness probe is issued inside the settle delay** (§5 term 2),
   with a test that fails if the survey is issued on the same cycle that observes the
   return.
12. **A second crash and return inside `DefaultTransitionSurveyMinInterval` still
   restores**, with a test that fails if recovery's survey is dropped by the throttle.
13. **The recovery principal cannot be deleted, disabled, or have its scope removed**
   through any API or CLI path, with a test per path that asserts the refusal.
14. **A layer whose only evidence predates the crash by a survey ShowMesh never repeated
   is reported as never observed, with the age and the source stated** (§10.1). A layer
   the timeline was driving must never report as dark.

## 9. What this seam does not do

- **No process supervision of any kind.** See §1, including its 2026-08-16 amendment about
  where a watchdog may live if the November upgrade does not fix the crash.
- **No persistence of the recovery record.** See §4.
- **No reconciler.** Nothing closes desired and observed state on a loop; this fires on a
  transition and stops. ShowMesh does not become a second scheduler for Resolume any more
  than it does for FPP.
- **No periodic composition survey.** §10.1 rejected adding one, and D-2's steady-state
  property is criterion 8.
- **No Operator UI beyond the recovery toggle's own control.** Everything else Resolume
  needs in the browser is D-4. See §7.1's 2026-08-16 amendment for why the toggle is the
  one exception.
- **No Show Mode.** §7.2 removed it from this seam's scope.
- **No helper app on the Resolume host.** §7a.

## 10. Six more decisions, answered by the owner 2026-08-16

A pre-build review read this specification against the adapter as built and found six
things a builder would have had to guess at. All six are answered. Each is folded into the
section it governs; this section records the decision and the reasoning so the reason does
not have to be reconstructed from the change.

### 10.1 The restore covers what ShowMesh drove, and says so about everything else

**Answered: accept the coverage this seam can honestly claim.** No periodic survey, no
new polling, no widening of D-2's steady-state footprint.

The reasoning that made this a real question is §3's 2026-08-16 correction: the survey
source is one reading taken at coordinator startup, and the timeline's clips are launched
by LTC rather than by ShowMesh, so they appear in neither source. **What the restore
actually restores is the explicit-control layers**: countdowns, resting visuals, pre-show
text, blackout. That is not a small thing, and it is the half of the wall that will not
come back on its own.

The owner's reason for accepting it: *"I think it will just pop right back to where we
were if it's running timecode when it crashes."* **That is an assumption, not a
measurement, and it must travel as one.** Nothing has tested what Resolume does with a
timeline clip when LTC keeps rolling through a restart. It belongs in
[RES-001](../research/RES-001-resolume-smpte-behavior.md)'s test matrix and therefore in
D0, where "a Resolume restart mid-show" is already a listed case. **If D0 finds the
timeline does not resume, this decision is the one that gets revisited**, and the options
are the ones the review put up: a low-rate layer read while a show runs, or restoring
layer state (bypass, master) rather than clip state.

What this obliges the build to do is criterion 14. A layer whose entry comes from a
startup survey the show has since moved past must report **never observed, with the age
and the source stated**, never dark and never restored on that evidence. This is the fifth
time this project has had to write down that absence of evidence is not evidence of
absence, and it is the first time the absence is one the design chose knowingly.

### 10.2 ADR-037 seam B lands first

**Answered: yes, names before crash recovery.** This seam's per-layer report is
operator-facing text, and built before ADR-037 it prints eighteen-digit Arena object ids,
which is exactly what the owner reacted to with *"I don't even know where I'd find those
clip id numbers."* Seam B is the smaller job and it is what makes this seam's output
readable. Recorded in the status line at the top of this file.

### 10.3 The toggle's switch ships here

**Answered: build the switch now.** Owner: *"rather it get done when we're working on
that system than forgetting about it."* Folded into §7.1 and criterion 9, and §9's
UI exclusion is narrowed to match. This resolves a contradiction between criterion 9 and
§9 that existed in the specification as written.

### 10.4 The recovery principal is baked in and not deletable

**Answered: not deletable, not disableable, off only when recovery is off.** Folded into
§7.3 and criterion 13. The failure this closes is a silent disarm, which is a shape this
project has now caught in several disguises: a control that reads armed while the thing it
arms cannot run.

### 10.5 Recovery bypasses the survey throttle, behind a settle delay

**Answered: bypass it, but wait first.** Owner: *"let recovery skip the throttle, but
let's put a wait to let Resolume fully come up, like 5-10 seconds before we start
hammering requests at it."* Folded into §5 as the four-term gate, and into criteria 11
and 12.

The two halves are doing different jobs and both are needed. The bypass is what makes
recovery fire at all on a second crash inside a minute, which the measured 36-second
crash interval makes a real case rather than a hypothetical. The settle delay is what
stops the bypass turning into load on an application that has just restarted after
crashing. **The bypass changes when the survey may run, never how many requests it makes.**

### 10.6 The watchdog stays out, and stays buildable

**Answered: keep it out of core, keep the door open, revisit after the November Resolume
upgrade.** §1 carries the amendment. Owner's framing: the no-relaunch decision was made in
frustration and may be worth revisiting, and if it is ever built it is **its own small
repository, never part of core**.

**Three properties keep an external watchdog a ~100-line program.** The build must not
break them, and none of them costs this seam anything, because all three already exist or
fall out of what §5 already requires:

1. **`resolume.reachable` is published on the change stream with its reason**, so an
   external process subscribes and learns Arena is gone without polling Arena itself or
   reading ShowMesh's database. This is ADR-014 and ADR-020 doing exactly what they were
   written for, and it needs no new surface.
2. **The restore fires on the observed return, however Arena got back.** §1 already says
   this and §5's gate already implements it: nothing in the gate asks who or what
   restarted the process. A watchdog relaunching Arena is indistinguishable, to this seam,
   from the operator double-clicking the icon.
3. **Criterion 7's guard is scoped to this repository**, not written as a claim that
   process supervision is wrong everywhere. A future reader of the guard must be able to
   tell that it is a boundary, not a verdict.

**Nothing else is owed.** No hook, no plugin point, no configuration field, and no code
written now against a program that may never exist. If the November upgrade fixes the
crash, all three properties are still things this system wanted anyway.
