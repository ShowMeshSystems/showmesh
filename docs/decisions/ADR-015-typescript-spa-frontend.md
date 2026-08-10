# ADR-015: The Operator UI Is a TypeScript Single-Page Application

Status: Accepted  
Date: 2026-08-10

## Context

[ADR-006](ADR-006-go-implementation-language.md) chose Go for the coordinator and node agent and explicitly deferred the frontend stack: "the coordinator's web UI framework is deliberately not decided here". BUILD-PLAN carried the same deferral. [ADR-014](ADR-014-operator-ui-is-an-api-client.md) now makes the UI a separately deployed client of a versioned public API, which removes the assumption in ADR-006 that the coordinator serves a bundled frontend and makes the stack choice independent of the coordinator's language.

The requirements that actually discriminate between candidates come from [OPERATOR-UI.md](../architecture/OPERATOR-UI.md): a long-lived push connection feeding many independently updating panels (§6), surfaces composed dynamically from advertised capabilities and provider metadata rather than from fixed templates (§9), a phone treated as a primary operational surface rather than a fallback (§13), and connection-state handling that must remain correct while the client is disconnected from the server (§7). The last one matters most: a client that must render meaningfully while it cannot reach the server needs its own state model regardless of stack.

## Decision

The Operator UI is a TypeScript single-page application, built to static assets and served from the UI container.

The specific framework, build tool, and component library are implementation choices for the first UI step and are not fixed by this ADR. TypeScript, static output with no server-side rendering requirement, and no runtime dependency on external CDNs or hosted services are fixed.

This does not extend to the coordinator or the node agent, which remain Go per ADR-006. ShowMesh is a Go project with a TypeScript frontend; it is not a polyglot backend.

## Consequences

- A JavaScript toolchain enters the repository and CI: a second dependency ecosystem, a second lockfile, a second set of vulnerability advisories, and a build step whose output must be reproducible offline.
- Contributors to the UI need TypeScript, and contributors to the backend need Go. ADR-006's single-language contributor argument no longer holds across the whole project. It still holds where it was aimed, at coordinator and agent code.
- The API contract in ADR-014 becomes the enforced boundary between the two ecosystems, which is a benefit: it is much harder to accidentally reach past the API from a separate TypeScript build than from a Go template in the same binary.
- Type definitions for API payloads must be generated from the same source as the Go types or verified against them. Hand-maintaining a second copy of the state model will drift, and the drift will present as UI bugs that look like backend bugs.
- Offline operation constrains the dependency set: everything ships in the image, nothing is fetched at runtime.

## Alternatives considered

**Go server-rendered with templ and HTMX** was the strongest alternative and would have kept one language, one toolchain, and one contributor skill set. It was rejected on fit rather than principle: fanning a single push stream into many independently updating panels, composing surfaces from capability and provider metadata discovered at runtime, and maintaining a correct client-side model across disconnection all cut against a server-rendered fragment model. Under ADR-014 the server-rendered option is also less natural, since the rendering server would be a second service that still has to fetch everything over the API.

**Go compiled to WebAssembly** was rejected for payload size on a phone over show-network Wi-Fi, immature UI ecosystem, and debugging difficulty, for no benefit that the API boundary does not already provide.

**No JavaScript at all — server-rendered pages with periodic refresh** was rejected because OPERATOR-UI §6 forbids depending solely on aggressive polling and §7 requires the client to behave correctly while disconnected, which a page-refresh model cannot express.

## Related research

None directly. The provider-metadata-driven form generation this stack is expected to support is an open hypothesis tracked in [RES-014](../research/RES-014-control-provider-model.md), and a negative result there would change how much dynamic composition the frontend must do, not which language it is written in.
