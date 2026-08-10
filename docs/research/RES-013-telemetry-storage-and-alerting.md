# RES-013: Telemetry Storage and Alerting

[Observability](../architecture/OBSERVABILITY.md#3-observability-architecture) · [Tracker](README.md) · [ADR-011](../decisions/ADR-011-context-aware-observability.md) · [ADR-009](../decisions/ADR-009-sqlite-configuration-storage.md)

Status: planned · Risk: high · Verification: L0 (design constraint from ADR-009 recorded 2026-08-10)

## Decision to make

Choose the storage and alert-evaluation design that supports current state, event correlation, diagnostic evidence, metric history, contextual policies, and reliable notifications without unnecessary operational complexity.

## Questions

- Can SQLite (the accepted coordinator store per ADR-009) cover the initial metric volume, retention, and query needs with a dedicated telemetry database file and aggressive downsampling?
- Would PostgreSQL/TimescaleDB or Prometheus materially improve collection, retention, or alerting enough to justify superseding or extending ADR-009 for telemetry? (Adopting either requires a new ADR.)
- Which data belongs in current-state tables, immutable events, metric samples, diagnostic results, and external object storage?
- What raw and downsampled retention is useful for a seasonal show?
- Where are alert rules evaluated and persisted across coordinator restart?
- How are debounce, hysteresis, dependencies, grouping, lifecycle context, maintenance windows, acknowledgement, and resolution represented?
- How are Discord, Home Assistant, Hermes, email, webhooks, and push delivery retried, rate-limited, audited, and secured?
- What is the expected cardinality and write rate for the reference and community-scale deployments?

## Acceptance criteria

- The reference workload runs for a full-season simulation within documented storage and resource budgets.
- Coordinator restart preserves active alerts, acknowledgements, suppression, and delivery history.
- Operators can reconstruct an incident by sequence, position, resource, configuration revision, and correlated evidence.
- Notification retries do not produce storms or block local alert visibility.
- Retention and downsampling are configurable and do not delete incident evidence unexpectedly.
- Backup and restore recover configuration, baselines, events, alerts, and required history.

## Test matrix and method

Model normal and worst-case collection rates, alert storms, device flapping, long offline periods, season rollover, clock skew, database restart, disk pressure, notification outage, credential rotation, backup, and restore. Compare a single-store design with any proposed metrics-specific component using observed operational cost and query behavior.

## Evidence and findings

No external evidence collected yet. Design constraint recorded 2026-08-10: ADR-009 establishes SQLite (WAL) as the coordinator's store; the default telemetry design is therefore a separate SQLite database file (own write path, own retention/pruning) beside the configuration store. Prometheus, PostgreSQL, and TimescaleDB remain candidates only, and adopting any of them is an ADR-level change, not a research-record conclusion. Reference-scale write load (tens of devices at 1–10 s cadence) is well within documented SQLite capability, but this must be modeled and bench-verified, not assumed.

## Decision, fallback, and revalidation

Decision pending. Default direction per ADR-009: one durable SQLite store with conservative sampling, downsampling, and dashboard/Discord notifications; escalate to a dedicated TSDB only with load evidence and a superseding ADR. Revalidate after scale, retention, alert-engine, or datastore changes.
