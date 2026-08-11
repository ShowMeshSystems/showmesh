# Security Policy

## Current posture, stated plainly

ShowMesh is pre-alpha. Its security posture is deliberate and recorded, but it is **not** a posture suitable for exposure to an untrusted network.

**There are no write operations of any kind in this release.** Nothing reachable through the API can change a device, a playlist, or a show. The entire surface discloses state; it does not alter it.

**The read API is unauthenticated by default.** The coordinator logs a warning at startup saying so. Setting `SHOWMESH_API_TOKEN` closes it behind a shared bearer token, which [ADR-021](docs/decisions/ADR-021-read-api-authentication-posture.md) describes accurately as *one shared secret and not an identity*: no roles, no authorization by target or action, no audit attribution. It does not satisfy ARCHITECTURE §10.4, and its existence explicitly bars the first write endpoint until a superseding ADR lands.

**The show VLAN is the actual security boundary.** That is the design assumption, not a workaround.

**The bundled Mosquitto allows anonymous access, and that is the larger exposure of the two.** Publish rights on the broker affect coordinator state, while the API only discloses it. `deploy/mosquitto/mosquitto.conf` carries a comment block on enabling a password file; do that before the broker is reachable from anywhere you don't control.

**ShowMesh terminates no TLS**, in the coordinator container or the UI container. A deployment that needs TLS puts its own reverse proxy in front.

**The browser session model is intentionally minimal.** After a `401`, the SPA prompts for the shared secret and holds it in that tab's `sessionStorage`. There is no login, identity, expiry, or logout beyond closing the tab. It is deliberately small enough to delete outright when the identity ADR supersedes it ([ADR-022](docs/decisions/ADR-022-operator-ui-serves-the-api-same-origin.md)).

**The UI container never holds a credential.** It serves static assets and forwards `/api/*` to the coordinator unchanged. It forwards credentials; it never holds, injects, mints, validates, or refreshes one, and cannot be configured with one. If it could, reaching the UI would be equivalent to reaching the API.

## Deploying it safely

1. Put the coordinator, the broker, and the UI on an isolated show VLAN with no route in from a general-purpose network.
2. Set `SHOWMESH_API_TOKEN` anyway. It is not identity, but it is not nothing.
3. Disable anonymous access on the broker and configure a password file.
4. Never place secrets in exported configuration bundles. Configuration exports exclude secrets by default by design (ARCHITECTURE §10.4, [ADR-009](docs/decisions/ADR-009-sqlite-configuration-storage.md)); keep them in `deploy/.env`, which is gitignored.
5. If the UI must be reachable over TLS, terminate it in a reverse proxy you control.

## What is out of scope for a report

These are known and documented, not vulnerabilities to report:

- The API being open by default (ADR-021)
- The shared secret providing no identity, roles, or audit trail (ADR-021)
- Anonymous MQTT in the bundled broker (documented in `deploy/mosquitto/mosquitto.conf` and `deploy/README.md`)
- No TLS anywhere (ADR-022)
- No session expiry or logout in the SPA (ADR-022)

If you think one of those decisions is *wrong* rather than merely unfinished, that is a valuable conversation — open an issue arguing the case. Superseding a recorded decision is how this project is designed to change.

## Reporting a vulnerability

For anything not on the list above, please report privately rather than opening a public issue: use GitHub's **[private vulnerability reporting](https://github.com/ShowMeshSystems/showmesh/security/advisories/new)** on this repository.

Please include the affected component and version or commit, reproduction steps, and what an attacker gains. Expect an acknowledgement within a week; this is a personal project and not staffed for a faster commitment.

## Supported versions

None. There is no released version and no backport policy. The `main` branch is the only supported code.
