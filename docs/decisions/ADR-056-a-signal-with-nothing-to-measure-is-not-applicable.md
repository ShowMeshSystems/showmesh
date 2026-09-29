# ADR-056: A signal with nothing to measure reports `not_applicable`

Status: Proposed
Date: 2026-09-29

## Context

[ADR-020](ADR-020-control-api-shape-and-change-stream.md) decision 5 gives
every observation-bearing field a `state` from a fixed six-value list:
`current`, `stale`, `unknown_age`, `not_collected`, `collection_failed`,
`unsupported`. The list has no value for "the thing this signal describes does
not exist right now". A session with no restore queued, a PTP clock that has
not stepped since it started, and a stopped session's timeline all report
`not_collected`, the same state as a sensor that never answered.

On the rig on 2026-09-18, `showmesh-node-01` reported 320 audio signals and 163
were `not_collected`. After the fixes on 2026-09-24, a healthy node with no
night session still showed 61, nearly all describing a healthy condition in
their own reason text. The Monitor, Node Detail and the Dashboard render
`not_collected` as unobserved, so a healthy node looks grey and an operator
cannot tell it apart from a node whose reports stopped arriving.

## Decision

1. **A seventh evidence state, `not_applicable`.** It means the signal's
   subject does not exist at the moment of the observation: nothing is queued,
   nothing is running, nothing has happened yet. It is a reading, not an
   absence. It is reported only when the source's own report establishes that
   the subject does not exist.

2. **Its envelope.** `value` is null. `reason` is non-null and states what is
   absent, in operator wording, for example "No restore is queued."
   `observedAt` is the time of the report that established it, and
   `collectedAt` is set as for any other state.

3. **It ages like any other reading.** A `not_applicable` row whose source stops
   reporting goes `stale` on the same rule as a `current` row. It is never a
   permanent answer.

4. **A signal with a meaningful default reports the default.** When the system
   is running on an effective value the operator did not set, such as the gain
   ceiling of a session with no ceiling configured, the signal is `current`
   with that value, and `reason` names it as the default. `not_applicable` is
   only for signals where no value exists.

5. **`not_collected` keeps its meaning and nothing else.** It stays the state
   for a signal that should have a reading and does not. A signal that is
   `not_collected` because its subject does not exist is a defect after this
   ADR.

6. **Clients show it as not a problem.** The UI labels it "N/A", renders it
   without the unobserved treatment, and leaves it out of health counts and
   warning tallies. `showmeshctl` prints `n/a` in table output and the wire
   value in JSON output.

## Consequences

- ADR-020 decision 5 now reads as a seven-value list. Its rule that absence is
  stated and never omitted is unchanged: a `not_applicable` row is still
  present on the resource.
- `api/openapi.yaml` adds `not_applicable` to the `Evidence.state` enum and
  describes the envelope in decision 2. This is an additive change to a public
  enum, and clients that switch over the state must handle the new value.
- The coordinator's collectors, chiefly
  `internal/coordinator/collector/nodeaudio`, move the healthy-condition rows
  from `not_collected` to `not_applicable` or to a `current` default.
- The generated UI types gain the value, and every `Record<EvidenceState, ...>`
  map in `ui/src/domain/evidence.ts` must give it a label, tone and absence
  style. The signal counts used by the Dashboard and Monitor count it
  separately from unobserved.
- A node whose reports stop arriving still turns its rows `stale`, so this
  state cannot hide a node that has gone quiet.
