# ADR-009: SQLite Is the Coordinator's Authoritative Store; YAML Bundles Are Portable

Status: Accepted  
Date: 2026-08-10

## Context

The configuration model (ARCHITECTURE §9) requires revisions, transactions, validation, rollback, and history on a single-host coordinator appliance that must work offline. Operators need reviewable backup/restore and migration artifacts.

## Decision

The coordinator's authoritative store is embedded SQLite (WAL mode): configuration revisions as immutable rows, desired state, observed-state history, command journal, and event log. Schema migrations are versioned and applied transactionally at startup; downgrade is refused.

Portable representation is versioned YAML export/import bundles for backup, review, diff, and migration — never the runtime source of truth. Secrets are excluded from bundles by default per ARCHITECTURE §10.4.

Node agents cache a verified JSON subset (current assignment + fallback definitions) on local disk, checksummed, sufficient to execute reduced local fallback with the coordinator unreachable. A stale returning node must not overwrite coordinator state (RES-008 conflict rules).

## Consequences

- Zero-dependency persistence: backup is a file copy plus YAML export; fits the appliance model.
- Single-writer coordinator is an accepted constraint; a future HA coordinator would require a superseding ADR.
- Observed-state history needs retention/pruning policy to bound database growth.
- RES-008 remains open for schema shape, merge semantics, and stale-node reconciliation — this ADR settles only the storage engines and the authority boundary.

## Alternatives considered

PostgreSQL was rejected as operational weight without a concurrency need on a single-host appliance. Plain YAML/JSON files as the runtime store were rejected: no transactions, no safe concurrent updates, no history. Embedded KV stores (bbolt/badger) were rejected because relational queries over history and revisions are wanted.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [Failure testing](../research/RES-009-failure-mode-testing.md)
