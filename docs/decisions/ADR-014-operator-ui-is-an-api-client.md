# ADR-014: The Operator UI Is an Optional Client of a Versioned Public Control API

Status: Accepted  
Date: 2026-08-10

## Context

ARCHITECTURE §4.8 requires an operator surface and OBSERVABILITY §6 specifies in detail what that surface must display, but neither says what the surface *is* relative to the rest of the system. Until now the frontend was deliberately unsequenced (BUILD-PLAN, "Not yet sequenced").

UI work is about to begin, and the order of decisions matters more than usual here. The read-only status API arrives in Step 3 of the build plan. If that API is designed as an internal convenience for a UI that is being written alongside it, the two will fuse: behavior will settle in whichever layer is easier to change, and the API will stop being independently usable without anyone deciding that it should. Recovering from that later means re-deriving a contract from a working UI, which in practice is a rewrite.

The project also has a standing constraint that a running show survives coordinator loss and broker loss ([ADR-008](ADR-008-mqtt-control-plane.md)). Anything the UI adds must not weaken that.

## Decision

The Operator UI is one client of the ShowMesh control plane, not its owner.

1. **The control API is a public, versioned contract.** It is designed and documented to be used without the UI, by a CLI, an automation system, an alternate or mobile client, or a future additional coordinator. The UI may contain no orchestration behavior that cannot be performed through the API.
2. **The UI accesses ShowMesh only through that API and its real-time state stream.** It must not read or write the coordinator's SQLite store, node-local state, configuration files, or the MQTT broker. Browsers are not control-plane participants; MQTT over WebSocket direct to the browser is rejected (OPERATOR-UI §6.1).
3. **The UI is optional and separately deployed**, as its own container in the Compose bundle, independently upgradeable without restarting show execution, nodes, audio, or projection.
4. **The show must survive the UI's complete absence.** The governing test is that if every browser disappeared at this instant, the show continues correctly.
5. **Authorization is enforced by the API, not by the UI.** The UI is not a security boundary.

The detailed client contract is [OPERATOR-UI.md](../architecture/OPERATOR-UI.md).

This ADR deliberately says what the API *is* and not what it looks like, because
there was nothing to shape that against when it was written. The shape was
decided when the first read-only surface was built, ahead of any UI, in
[ADR-020](ADR-020-control-api-shape-and-change-stream.md) (versioned REST plus a
Server-Sent Events change stream) and
[ADR-021](ADR-021-read-api-authentication-posture.md) (the authentication
posture the read API ships with, and the bar it places on write endpoints).

## Consequences

- The read-only status API in build Step 3 becomes a deliberate public contract with versioning and a documented shape, not a debug endpoint. This is additional work in Step 3 and is the point of the decision.
- API version skew between coordinator and UI becomes a normal, permanent condition rather than an exceptional one, because the two are separately deployable. The UI must detect an incompatible coordinator and say so explicitly instead of rendering partial state that looks authoritative.
- A second image must be built, published, and version-tracked, and the supply-chain obligations ADR-012 accepted for the coordinator image now apply twice.
- Cross-origin configuration and TLS termination become real deployment questions that a single embedded surface would not have raised. They are deferred to when the UI is built, not resolved here.
- Deployment friction rises slightly against ADR-012's stated concern that friction is a first-order adoption issue. The Compose bundle absorbs most of it; an operator still runs one `docker compose up`.
- Every operator-facing capability must exist in the API before it can exist in the UI. This is intended, and it will occasionally feel like an obstruction when a small UI-only affordance would be quicker.

## Alternatives considered

**Embedding the built UI in the coordinator binary** (Go `embed`, served from the existing distroless image) was the serious competitor and remains technically attractive: no second image, no CORS, no version skew, lowest deployment friction. It was rejected because the API-only discipline in decision 2 would then rest entirely on convention and review. With a process boundary, "the UI cannot reach the database" is a fact about the deployment; without one it is a promise that erodes the first time a template renders something directly from the store. The decision is that the wall is worth the packaging cost, and it can be revisited by a superseding ADR if the operational cost proves higher than estimated.

Worth recording so it is not re-argued from the wrong premise: the isolation argument for a separate container is weaker in ShowMesh than it looks. A coordinator restart already does not interrupt a show, so separating the UI from the coordinator buys little additional show safety. The reason to separate is contract discipline and independent release cadence, not show survival.

**Serving the UI from a generic static web server with no ShowMesh code** was rejected as insufficient, since version negotiation and configuration injection need somewhere to live.

**Leaving the API internal and treating the UI as the product** was rejected because it forecloses the CLI, automation, and alternate-client paths that ARCHITECTURE §2.5 (open interfaces) commits to, and because it would make every future integration a scraping exercise.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) · [Telemetry storage and alerting](../research/RES-013-telemetry-storage-and-alerting.md) · [Control-provider model](../research/RES-014-control-provider-model.md)

Failure testing ([RES-009](../research/RES-009-failure-mode-testing.md)) must include UI container removal during a live show, coordinator restart underneath a connected browser, and operation of the full show path with no UI deployed at all.
