# RES-008: Configuration Model

[Architecture](../architecture/ARCHITECTURE.md#9-configuration-model) · [Tracker](README.md) · [Failure testing](RES-009-failure-mode-testing.md)

Status: unresearched · Risk: high · Verification: L0

## Decision to make

Define authoritative runtime configuration, portable representation, revisioning, validation, secrets, node-local fallback data, and conflict handling.

## Proposed boundary to validate

The coordinator owns authoritative runtime configuration. Versioned YAML or JSON bundles provide backup and review. Nodes cache only the verified subset needed for assigned work and reduced local fallback.

## Questions

- Which objects describe nodes, capabilities, assignments, surfaces, transports, audio routes, macros, and fallbacks?
- How are desired state and configuration revisions related?
- Which changes are safe live, staged for the next show, or restart-required?
- How are schema migrations, dry runs, rollback, and partial deployment handled?
- How are secrets referenced without entering portable exports or logs?
- What happens when a disconnected node returns with stale configuration?
- How are user edits reconciled with discovered hardware facts?

## Acceptance criteria

Invalid configuration is rejected before activation; imports show a change set; revisions are immutable and reversible; partial application is visible; stale nodes cannot silently overwrite current state; secrets remain separate; and a clean coordinator can be restored from a documented export plus secret recovery procedure.

## Test method

Model a small and advanced installation. Test create, edit, dry run, activation, rollback, concurrent edits, schema upgrade, downgrade refusal, stale-node return, coordinator restore, missing secret, partial node reachability, and corrupted local cache.

## Evidence and findings

No evidence collected.

## Decision, fallback, and revalidation

Decision pending. A minimal local configuration file may bootstrap nodes, but must not become a competing source of truth. Revalidate whenever the schema, persistence layer, migration engine, or trust model changes.
