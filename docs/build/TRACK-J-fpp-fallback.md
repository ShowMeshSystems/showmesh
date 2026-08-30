# Track J: FPP Fallback and Degraded Operation

Status: specified, not started (2026-08-30)

## Goal

Keep a scheduled show running through a coordinator outage by having the FPP
plugin execute only a signed, pre-resolved map from its observed playlist-entry
key to the already authorized Cue outputs on named nodes.  Return to ordinary
coordinator control at the next normal scheduled-show boundary, never by a
mid-show takeover.

This is not a second scheduler, a ShowMesh playlist runner, or a generic FPP
remote-control channel.  FPP still owns its schedule, playlist selection, and
progression.  ShowMesh still owns Cue meaning, active-show authorization, and
the normal control path.

## Bound by

- [ADR-004](../decisions/ADR-004-layered-commands-and-fallback.md)
- [ADR-024](../decisions/ADR-024-identity-authorization-and-audit.md)
- [ADR-025](../decisions/ADR-025-agent-fallback-cache-is-signed.md)
- [ADR-038](../decisions/ADR-038-fpp-authorizes-night-sessions.md)
- [ADR-043](../decisions/ADR-043-show-scoped-cues-and-playlist-authority.md)
- [ADR-044](../decisions/ADR-044-agent-inbound-http-listener.md)
- [ADR-048](../decisions/ADR-048-signed-fpp-fallback-program.md)

Track H supplies the active-show generation, resolved Cue catalog, deterministic
FPP entry key, and normal Cue activation path.  This track consumes those
properties; it does not redefine them.

## Build order

### J1. Specify, build, sign, and publish the fallback program

In the main repository, define the versioned package, signature, per-FPP-host
target map, acknowledgement, expiry, and reconciliation behavior in
ADR-048.  Build the compiler from the active show and its resolved Cue catalog.

The compiler must refuse an ambiguous entry key, cross-show reference, missing
node catalog acknowledgement, unresolvable target, unsupported output, or
unsigned result.  It updates on every relevant configuration change and
reconciles periodically while the coordinator is healthy.

**Acceptance:** unit and integration tests demonstrate that a changed Cue,
playlist binding, target assignment, active-show generation, or catalog revision
produces a new signed package; a stale package cannot be treated as current.

### J2. Persist and verify the package in the FPP plugin

In the plugin repository, add last-known-good package persistence, coordinator
signature verification, atomic replacement, acknowledgement, age reporting,
and the normal, fallback, and resting state machine.  The plugin continues to
produce the existing atomic FPP entry observation.

The plugin's entry callback resolves only its deterministic entry key against
the installed package.  It never derives a Cue from a filename or creates a
Cue identifier.

**Acceptance:** plugin tests cover restart between download and replacement,
bad signatures, stale or wrong-FPP packages, duplicate filenames at different
positions, unknown entries, and an outage that reaches fallback only after the
configured threshold.

### J3. Add the agent's narrow fallback-activation ingress

In the main repository, add the authenticated, target-restricted fallback
activation route and its enrollment material.  Reuse the normal Cue activation
validation and output application after validating the fallback program,
origin, active-show generation, catalog revision, target, and replay fence.

The route accepts no arbitrary action, macro, raw payload, or Cue selection.
It is separately rate-limited and observable.  ADR-044's xLights endpoint is
not widened or reused for this purpose.

**Acceptance:** integration tests prove rejection for an invalid signature,
wrong FPP host, wrong target, stale generation, stale catalog, unknown entry,
expired package, replayed execution id, and arbitrary Cue request.  One valid
request must take the same resolved Cue path as a normal dispatch.

### J4. Connect the plugin executor to the agent ingress

Give the plugin its per-host executor credential and connect a matching entry
to the direct node activation.  Bound retries must preserve the execution id.
The plugin records delivery and refusal evidence without turning a refusal into
a different Cue or command.

**Acceptance:** a simulated coordinator outage during a playlist transition
causes one mapped Cue activation on the intended node; unknown or refused work
leaves the show in the declared hold/rest state and records why.

### J5. Enforce cutoff and recovery handoff

Implement the package's rest/hold and local-shutdown rules at cutoff.  After
the coordinator recovers, it may validate and distribute again but cannot take
over fallback progression until the next normal scheduled-show boundary.

**Acceptance:** tests cover outage, coordinator restart, recovery before the
boundary, cutoff, next scheduled boundary, and a second outage.  No scenario
may produce duplicate output activation or two competing progression owners.

### J6. Run the real-system evidence pass

On real FPP and nodes, exercise normal delivery, package refresh, coordinator
loss, fallback entry matching, node restart, network recovery, cutoff, and the
next-boundary handoff.  Capture the observed package revision, node result, and
safe recovery outcome for every run.

**Acceptance:** the assembled path is observed on the intended real devices.
Automated tests and a containerized FPP are necessary build evidence, not a
substitute for this pass.

## Explicitly out of scope

- calendar scheduling or ShowMesh control of FPP playlist progression;
- a generic FPP-to-agent command listener;
- dynamic fallback-program creation during an outage;
- a mid-show coordinator takeover; and
- multi-FPP witness quorum.  That is a separately decided future capability
  after J1 through J6 have real-system evidence.

## Handoff rules

Build J1 before beginning J2 through J5.  J2 and J3 may proceed in parallel
once J1 freezes the package and request schemas.  J4 depends on both.  J5
depends on J4.  J6 is the only completion gate and requires real hardware.

Each implementation pull request must name the relevant J section, preserve
the existing FPP entry-identity behavior, and state whether its evidence is
automated, containerized, or real-device evidence.
