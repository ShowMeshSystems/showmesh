# ADR-041: Operator Recovery Is Not a Calendar Intent

Status: **Drafted, awaiting owner acceptance.** The substance was approved by the owner on
2026-08-19 (Linear SM-82); this record exists because the approval sits against a sentence in
an accepted ADR that says the opposite, and that tension belongs in a record rather than in a
reader's interpretation.
Date: 2026-08-19

Narrows [ADR-038](ADR-038-fpp-authorizes-night-sessions.md) decision 2.

## Context

ADR-038 decision 2 says the night-session controller "is a dedicated persisted lifecycle
controller with a closed state machine and a closed command vocabulary", and decision 1 lists
the seven commands FPP invokes. Track F implemented that controller.

Its review then proved, by execution, that a degraded session refused every one of those seven
commands with a `409`, including `fade-out-night` and `power-down-presentation`, and that
**nothing anywhere could clear the degraded flag.** A session degrades when the coordinator
restarts mid-transition and cannot confirm what is safe to resume, which is an ordinary event.
So one restart during a show disabled the controller for every subsequent night, recoverable
only by editing SQLite by hand, while the problem detail promised an "operator recovery" that
existed on no surface.

Two separate defects sat inside that. The refusal itself is a straight
[ADR-024](ADR-024-identity-authorization-and-audit.md) decision 7 violation, the same shape
Track D paid for in D-3: a refusal for want of ShowMesh's own evidence is strictly worse than
the coordinator being switched off, because a `409` is a successful conversation with a healthy
coordinator and fires no fallback. That half needed no decision and was fixed by exempting the
three admission-closing commands from the gate.

The permanence half needed a way out, and a way out is a command. ADR-038 said there were seven.

## Decision

### 1. ADR-038's closed vocabulary is the set of lifecycle intents FPP's calendar invokes

That is what "closed" was protecting: a controller that cannot grow a scheduler, a workflow
graph, or a second calendar authority. It was never a claim that no operator-facing action may
exist alongside it.

The seven remain closed as a set. Nothing may be added to them, and in particular nothing that
FPP could invoke on a schedule.

### 2. `end-session` exists as an operator recovery action, outside that set

It abandons the current session, reaches `stopped`, and launches nothing. It requires
`night:command` and has API and `showmeshctl` coverage.

**It deliberately does not clear the degraded flag and continue.** Resuming a session whose real
state ShowMesh cannot confirm is precisely the guess ADR-038 decision 3 forbids when it says
ShowMesh does not guess. After `end-session` the operator runs `prepare-site` and the ordinary
sequence, with their own judgement in the loop and readiness evaluated fresh.

### 3. A recovery action may never do a lifecycle action's work

`end-session` cannot start a show, cannot resume an unconfirmed session, and cannot skip
readiness. Any future recovery action carries the same three prohibitions. The test is whether
the action could be used to obtain a lifecycle outcome that the lifecycle commands would have
refused; if it could, it is a lifecycle command wearing a recovery label, and it belongs in
front of the owner rather than in the code.

## Consequences

- The night-session controller has eight verbs, seven of which FPP invokes and one of which it
  must not.
- A degraded session is now recoverable without database surgery, and the reason text names the
  recovery that exists rather than one that does not.
- The distinction is load-bearing for anything added later. "Is this a calendar intent or an
  operator action" is now a question with an answer, and the answer decides whether ADR-038's
  closed set is being widened.
- **This does not make recovery comprehensive.** A cue left ambiguous after a crash still has no
  resolution short of ending the session, because resolving one safely is a question about
  duplicate outward effects that nobody has answered yet. Linear SM-98 covers making that state
  visible; resolving it is not designed.

## Alternatives considered

**Clear the degraded flag and continue.** Rejected. It is the convenient answer and it is a guess
about a running show, which is the one thing this controller is built not to do.

**Amend ADR-038 in place.** Rejected because an accepted record is superseded or narrowed by a new
one, never edited, so that a reader who has already read it learns that it changed.

**Leave the vocabulary literally closed and accept the permanence.** Rejected on the measured
consequence: one ordinary coordinator restart disabling the controller for every subsequent night
is not a defensible reading of any rule in this project.

## Related

[ADR-038](ADR-038-fpp-authorizes-night-sessions.md) · [ADR-024](ADR-024-identity-authorization-and-audit.md) decision 7 · [RESTING-MODE](../architecture/RESTING-MODE.md) §4 · Linear SM-82
