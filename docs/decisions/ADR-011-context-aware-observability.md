# ADR-011: Health and Alerts Are Context-Aware

Status: Accepted  
Date: 2026-08-10

## Context

The same observation can mean different things during resting, diagnostics, live playback, maintenance, warm-up, or shutdown. Raw reachability and threshold checks also cannot distinguish a failed presentation from an intentionally static or powered-off system.

## Decision

Derive health and alerts from timestamped evidence plus expected lifecycle, sequence, topology, maintenance, and diagnostic context. Every observation has freshness and provenance. Stale or insufficient evidence becomes `unknown`, not healthy. Suppression preserves the underlying observation and its reason.

Monitoring must use layered evidence where available: device reachability, process state, signal presence, transport health, preview content, and physical telemetry remain distinguishable.

## Consequences

- Alert rules require show and maintenance context rather than only metric thresholds.
- Sequence metadata may declare presentation expectations such as motion, blackout, or permitted static intervals.
- The system can localize faults more precisely and reduce false notifications.
- Missing context may limit severity or produce `unknown` rather than a confident failure classification.
- Event and metric records must retain enough context to reconstruct an incident.

## Alternatives considered

Static threshold alerts were rejected as too noisy for lifecycle-dependent equipment. Treating a reachable host or process as healthy was rejected because it does not prove physical output. Deleting suppressed conditions was rejected because it prevents incident reconstruction.

## Related research

[Projection preview monitoring](../research/RES-010-projection-preview-monitoring.md) · [Pixel-current diagnostics](../research/RES-011-pixel-current-diagnostics.md) · [Device telemetry adapters](../research/RES-012-device-telemetry-adapters.md) · [Telemetry storage and alerting](../research/RES-013-telemetry-storage-and-alerting.md)
