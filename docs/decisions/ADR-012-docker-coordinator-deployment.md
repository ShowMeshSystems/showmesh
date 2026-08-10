# ADR-012: Docker Is the Primary Coordinator Deployment Method

Status: Accepted  
Date: 2026-08-10

## Context

ARCHITECTURE §10.1 called for "a documented container bundle" without committing to a mechanism. Code is starting, so the packaging decision must be settled now rather than deferred into implementation.

The coordinator is a single-writer management-plane service on one host ([ADR-009](ADR-009-sqlite-configuration-storage.md)). For many deployments the coordination and observability layer is the whole product: an operator may run ShowMesh purely to watch FPP, Resolume, and infrastructure health, and may never run a node agent at all. Deployment friction is therefore a first-order adoption concern in a hobbyist community, not an operational afterthought.

## Decision

Docker is the primary and supported deployment method for the coordinator, on `linux/amd64` and `linux/arm64`. The reference bundle is a Compose stack containing the coordinator and an Eclipse Mosquitto broker with persistent named volumes; it must also support pointing at an external broker instead of the bundled one.

This decision applies to the coordinator only. Node agents continue to run natively under the platform service manager per ARCHITECTURE §10.2, because they need direct GPU, HDMI, audio, EDID, and NDI access ([ADR-007](ADR-007-gstreamer-media-engine.md)). Containerization is not a project-wide mandate; each component is packaged for what it must reach.

## Consequences

- The coordinator must build CGo-free so the image can be a static binary on a distroless base and cross-compile cleanly for arm64. This selects pure-Go dependencies, notably `modernc.org/sqlite` rather than `mattn/go-sqlite3`, for the ADR-009 store. It is consistent with [ADR-006](ADR-006-go-implementation-language.md), which already confines CGo to the GStreamer boundary, and GStreamer never runs in the coordinator.
- Backup and restore become a volume copy plus the ADR-009 YAML export; upgrade and rollback become an image tag change with the data volume preserved. Schema migrations still apply forward-only at startup per ADR-009. That tag-change workflow requires published images, which do not exist yet, so the bundle builds from source today and versioning means checking out and rebuilding the corresponding git ref.
- Secrets reach the container as environment variables and must stay out of exported configuration bundles (ARCHITECTURE §10.4).
- Offline operation must not regress: the stack must start and run with no internet access once images are present.
- A containerized coordinator cannot use host-level discovery that depends on the host network namespace by default. Anything requiring multicast reception (for example, observing FPP MultiSync traffic from the coordinator itself) needs explicit host networking or a documented network mode. This is a known constraint to resolve when coordinator-side collectors are built; per ADR-001 and ADR-008 the coordinator is never in the timing path, so this affects observation only, not show execution.
- Publishing images adds a supply-chain surface: image provenance, base image updates, and CVE response become ongoing maintenance obligations.

## Alternatives considered

Native packages and systemd units only were rejected as higher friction for the primary audience and more support burden across distributions, though nothing prevents adding them later alongside the container path. Kubernetes or a Helm chart as the primary target was rejected as disproportionate for a single-host, single-writer appliance. Snap or Flatpak was rejected as narrower reach and awkward for a long-running networked service. No packaging opinion at all, build from source, was rejected because it makes the easiest deployment the least supported.

## Later decisions narrowing this one

[ADR-014](ADR-014-operator-ui-is-an-api-client.md) (2026-08-10) adds the Operator UI to the reference bundle as a third container, independently upgradeable. The bundle described above is therefore coordinator, broker, and UI; the coordinator-only scope of the *CGo-free* and *native agents* consequences is unchanged.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [Failure testing](../research/RES-009-failure-mode-testing.md)

Failure testing (RES-009) must include coordinator container restart and volume persistence.
