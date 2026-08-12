# ADR-023: The Change Stream Carries Observation Deltas, Opt-In Per Connection

Status: Accepted  
Date: 2026-08-11

## Context

[ADR-020](ADR-020-control-api-shape-and-change-stream.md) established the
control API as versioned REST plus a Server-Sent Events change stream, made the
stream deliberately non-resumable, and required that within a major version the
contract is additive-only and clients ignore unknown fields.

Step 5 was the first work in this project run against real show hardware, and
it measured what that stream actually costs on a real fleet. The numbers are
not projections.

**Frame sizes**, from the live fleet: 11.4 KB for the player (49 signals),
63.7 KB for a K16A-B remote (251 signals), 90.2 KB for a K16-Max remote (349
signals). One full round is 161.5 KB.

**Sustained rate**, measured over 20 seconds with the display de-energized and
nothing happening: roughly 57 KB/s per connected browser for three hosts.

**Why it does not settle down.** A first hypothesis, that timestamp refreshes
were firing the hub, was refuted by measurement: a poll with no new message
produces byte-identical JSON. A genuine defect was found and fixed on the way
(the stale-evidence reason string embedded a computed age, which ADR-020
forbids precisely because it re-renders differently on every tick), and the
diff key was made state-aware. A second live run then confirmed the fix works
exactly as designed, **zero no-op frames across 17 frame-pairs**, and that
the volume did not improve.

Every frame was triggered by a genuine value change, always from the same
handful of signals: `fpp.uptime.seconds` increments every second by
definition, and the K16 voltage and temperature sensors jitter continuously.
So four to nine observations genuinely change, and the coordinator re-sends
all 349 to report it.

**The consequence is not theoretical.** Step 4 measured the hub's back-pressure
behaviour: a 64-frame buffer, and a client that merely reads slowly rather than
stopping. At ~55 KB mean frame size that is ~3.4 MB of pending frames held per
browser. A phone with roughly 20 KB/s of usable wifi against a 57 KB/s stream
falls the full buffer behind in about five minutes, is reset, re-fetches a
~170 KB snapshot, and repeats. That is a self-inflicted reset loop on exactly
the device OPERATOR-UI's responsiveness requirement exists for, and it gets
worse with every signal added, not better.

The problem is therefore not churn, and not collector cadence, both of which
were investigated and exonerated by measurement. **The change stream has no
delta granularity: any change to any observation re-sends every observation.**

## Decision

**1. The change stream gains observation delta frames, and they are opt-in per
connection.**

A client requests them with a query parameter on the stream endpoint:

```
GET /api/v1/stream?deltas=1
```

A connection that does not ask receives exactly what it receives today, byte
for byte. This is what makes the change additive under ADR-020 rather than a
silent break: no existing client's behaviour changes, and a client that has
never heard of deltas cannot be affected by them.

**2. A delta frame carries only what changed, and what was removed.**

```
event: fpp.observations.changed
data: {"seq":42,"serverTime":"...","instanceId":"fpp-remote-04",
       "changed":[ {"signal":"fpp.uptime.seconds", ...} ],
       "removed":["fpp.port.port_33.current_ma"]}
```

`removed` is required, not an optimisation. Observations can genuinely cease to
exist: a cape swapped for a smaller one, a renamed port, a sensor that
disappears. Without it a client's baseline accumulates rows the coordinator no
longer has, which is the ghost-port problem moved from the store into the
browser.

**3. Structural changes to an instance keep the existing frame.** Health,
endpoint, `lastPollAt` and `lastPollError` are instance-level, not
observation-level, and continue to arrive as `fpp.changed`. A delta-subscribed
client receives both kinds and must handle both. Splitting on this line keeps
each frame's meaning single.

**3a. The two frames have different merge semantics, and a client that
confuses them will diverge silently.** `fpp.changed` carries the instance's
**complete** observation set and is therefore a replacement. `fpp.observations.changed`
carries only what moved and is therefore a merge, with `removed` applied as a
deletion. Stated here rather than left to be inferred, because a client that
merges a full frame keeps observations the coordinator has dropped, and a
client that replaces on a delta frame renders four signals out of 349 while
looking perfectly healthy. Both failures are invisible from the server side.

When structural and observation changes land in the same render pass, a
delta-subscribed client receives **only** `fpp.changed`, never both. The full
frame already carries the current observation set, so a delta alongside it
would be redundant bytes in a feature whose entire purpose is to remove them.
This is a consequence of 3a rather than a separate rule: whichever frame
arrives, the client's state afterwards is the same.

**4. Deltas apply to a baseline the client already has, because ADR-020
already guarantees one.** The stream is non-resumable, `stream.start` sets
`snapshotRequired`, and a client must fetch an authoritative snapshot on every
connection and after any interruption. Delta frames change none of that. A
client that misses frames does not attempt to reconcile: it reconnects and
re-snapshots, exactly as before.

**5. Deltas never carry a precomputed age**, and every rule ADR-020 sets about
absent evidence, absolute timestamps and `serverTime` applies unchanged. A
delta is a smaller frame, not a different vocabulary.

**6. The default remains full frames, and that is a cost this record accepts
rather than hides.** The wasteful path stays reachable, and `curl -N` without
the parameter still pulls the heavy stream. That is the price of not breaking a
contract that ADR-014 insists is public, and it is the right price to pay while
the identity ADR is still ahead of us and clients other than the UI are
expected.

## Consequences

- The Operator UI opts in, and must merge rather than replace. Its store
  becomes responsible for applying `changed` over its baseline and honouring
  `removed`. A merge bug is a new class of defect that a full-frame client
  could not have: the UI can now hold state the coordinator does not.
- Two rendering paths exist in the hub, and both must stay correct. A test must
  assert that a delta-subscribed client and a full-frame client converge on the
  same state from the same sequence of events, because that equivalence is the
  whole safety argument.
- `showmeshctl` gains a way to exercise deltas, because a contract only the UI
  uses has stopped being public (ADR-014).
- `api/openapi.yaml` documents the parameter and the new frame; the conformance
  test covers both paths.
- The measured win is large: reporting four changed signals costs roughly 1 KB
  instead of 90 KB.
- This does not remove the need to think about payload size again. It removes
  the multiplier, not the underlying growth: a resource with thousands of
  observations still produces a large snapshot, and RES-013 still owns metric
  history and retention.

## Alternatives considered

**Make deltas the default.** Smallest implementation, and it deletes the
wasteful path outright. Rejected because both failure modes are silent. A
client that replaces its observation list rather than merging renders only the
four changed signals and looks like a working system reporting almost nothing.
A client that ignores an unrecognised event kind silently stops updating while
its connection stays healthy. ADR-020's non-resumability rule exists because "a
gap in the stream is indistinguishable from a quiet system"; making deltas the
default creates exactly that ambiguity at the client instead of the transport.

**Bump the API to v2.** Honest about the breakage and clean to reason about.
Rejected as disproportionate: a major version in the path applies to the whole
API, v1 is two steps old, every write endpoint is still ahead of it, and the
identity ADR that supersedes ADR-021 is more likely to warrant a major version
than one frame shape is.

**Leave the contract alone and shrink the payload.** For example, exclude
continuously-varying signals such as `fpp.uptime.seconds` from the diff
trigger, or coalesce frames over a window. Rejected as treating the symptom:
coalescing was measured not to help much, because frames already fire at
roughly collector cadence rather than faster, and excluding signals from the
trigger means deliberately not telling an operator about a value that changed.
Both leave the multiplier in place for the next high-cardinality resource,
which the preview wall, controlled devices, and V5 smart receivers all promise
to be.

**Per-signal subscription (the client names what it wants).** Rejected as
premature: it needs a filter vocabulary in the contract, it makes the
snapshot-plus-stream invariant much harder to state, and nothing has yet asked
for it. Deltas solve the measured problem without inventing a query language.

## Supersession

This record extends ADR-020 and supersedes nothing. ADR-020's transport,
non-resumability, evidence-absence and additive-only rules all stand unchanged;
this record adds one opt-in capability inside them.

If a future record makes deltas the default, it must supersede this one and
must say how it avoids the two silent-breakage modes recorded above, because
they are the entire reason the default is what it is.

## Related research

- [RES-013](../research/RES-013-telemetry-storage-and-alerting.md) owns metric
  history, retention tiers and downsampling, and is where the snapshot's own
  size belongs when it becomes the binding constraint.
- [RES-009](../research/RES-009-failure-mode-testing.md) owns the poll
  cadences, staleness windows and buffer bounds quoted here, all of which
  remain unmeasured ShowMesh hypotheses.
