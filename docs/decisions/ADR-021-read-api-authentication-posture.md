# ADR-021: The Read API Ships With Optional Shared-Secret Authentication and No Authorization

Status: Superseded by [ADR-024](ADR-024-identity-authorization-and-audit.md), 2026-08-11  
Date: 2026-08-10

**Superseded.** ADR-024 decides the identity, authorization, audit, MQTT
authorization, and browser session model this record's supersession section
demanded, and lifts rule 5's bar on the first write endpoint. `SHOWMESH_API_TOKEN`
is retired; a coordinator that still sees it refuses to start.

Three things here were carried forward rather than replaced, and ADR-024
decision 3 restates them so they are not orphaned by this record's supersession:
rule 3 (the health and version endpoints are never authenticated), rule 4
(credentials are redacted by code, never appearing in a log line, error body, or
problem detail), and the CORS allow-list posture in the consequences below,
which ADR-024 additionally binds to its same-origin write requirement.

## Context

ShowMesh has no authorization anywhere. The bundled Mosquitto allows anonymous
access, and any client with publish rights can create unbounded node rows by
publishing on arbitrary syntactically valid node IDs, recorded as a deferred
item when the control plane skeleton landed. Build Step 3 adds an HTTP read API
on top of that, which widens the surface from "anyone on the show VLAN can talk
to the broker" to "anyone on the show VLAN can also read the operational model".

Three accepted documents point in different directions here, and the tension is
real rather than apparent:

- ARCHITECTURE §10.4 requires authenticated identities and authorization by
  target and action.
- [OBSERVABILITY §13](../architecture/OBSERVABILITY.md#13-security-and-privacy)
  states that preview streams and telemetry require authenticated access.
- [OPERATOR-UI §14](../architecture/OPERATOR-UI.md#14-authentication-and-authorization)
  says the initial deployment may use a simple model appropriate to an isolated
  show VLAN, but that the mechanism must be **an explicit decision, not a
  default that arrives by omission**, and that a UI which can stop a show with
  no authentication is defensible on a private VLAN and indefensible otherwise.

Step 3 is read-only, so nothing the API exposes can stop a show. That lowers the
stakes but does not remove them: the API discloses the operational model of a
system, and disclosure is the thing OBSERVABILITY §13 is about.

Designing real identity and role-based authorization now was considered and
rejected as premature: there is no consumer to design it against, no write
endpoint to protect, and no operator requirement to shape it. It would be
guessed, and a guessed authorization model is expensive to retract.

## Decision

**The read API ships with optional shared-secret authentication, disabled by
default, and no authorization at all.**

1. **`SHOWMESH_API_TOKEN`.** When set, every `/api/v1/*` request, including the
   change stream, requires `Authorization: Bearer <token>`. The comparison is
   constant-time. Rejection is `401` with an RFC 9457 problem document and a
   `WWW-Authenticate: Bearer` header.
2. **When unset, the API is open**, and the coordinator logs a **warning at
   startup** naming the exposure in plain words. Not debug, not info. An
   operator who never reads a config file still sees it in the first ten lines
   of the log.
3. **`/healthz`, `/readyz`, and `/version` are never authenticated.** The
   container healthcheck uses them and they disclose nothing operational.
4. **The token is a secret.** It is redacted by the configuration type's
   `LogValue` exactly as the broker password already is, enforced by code rather
   than by a doc comment, and never appears in an error body, a log line, or a
   problem detail.
5. **No write endpoint may be added under this ADR.** A superseding ADR
   deciding a real authentication and authorization mechanism is a precondition
   for the first write operation, per OPERATOR-UI §14.

**What this explicitly is not.** One shared secret is not an identity. There are
no roles, no per-target or per-action authorization, and no audit attribution:
every authenticated request is indistinguishable from every other. This
**does not satisfy ARCHITECTURE §10.4**, and it is recorded that way here rather
than being allowed to look like compliance. It partially addresses
OBSERVABILITY §13, in that telemetry *can* be closed to unauthenticated access, and
only for operators who set the variable.

## Consequences

- An operator who wants the API closed has a switch. One that does nothing gets
  a warning explaining what is open. Neither existed before.
- Because a browser's `EventSource` cannot set request headers, Step 4's SPA
  must read the change stream with `fetch` and a streaming reader. This is
  already required by [ADR-020](ADR-020-control-api-shape-and-change-stream.md)
  for other reasons, so the cost is zero here. It does mean that enabling the token
  and then building a UI with `EventSource` would fail, and that is worth
  knowing before it is discovered.
- Where the browser stores a shared token is an unanswered question, and it is
  the reason the default is off rather than on. Turning it on before Step 4 has
  a session model would force a bad answer.
- The MQTT control plane remains anonymous. This ADR does not touch it, and the
  broker is still the larger exposure of the two: publish rights there affect
  coordinator state, while the read API only discloses it. That asymmetry should
  be resolved by the same superseding ADR, not by a second partial measure.
- Nothing here is a substitute for network isolation. The deployment
  documentation must keep saying that the show VLAN is the actual security
  boundary today.
- CORS is configured by explicit allow-list, defaulting to emitting no CORS
  headers at all. An unauthenticated API that also reflects arbitrary origins
  would let any page a browser visits read the show's operational model over the
  operator's own network position.

## Alternatives considered

**No authentication at all, recorded as a known exposure.** Simpler, and it has
the honest virtue of leaving nothing to mistake for security. Rejected because
it leaves OBSERVABILITY §13 unmet with no mitigation available even to an
operator who wants one, and because "we will add it later" has no forcing
function attached to it.

**Designing identity and role-based access now** (viewer, operator,
administrator). Rejected as premature for the reasons in the context: no
consumer, no write endpoint, no requirement. It would be a guess, and
OPERATOR-UI §14 already requires the API and UI architecture merely to avoid
*blocking* roles, which expressing authorization server-side achieves.

**mTLS on the show VLAN.** Genuinely stronger and rejected on operational cost:
certificate distribution and rotation for a seasonal display run by one operator
is a maintenance burden out of proportion to the threat, and it would make
`curl` an awkward client, which ADR-020's first design reason argues against.

**Making the token mandatory.** Rejected because it would force an answer to the
browser-token-storage question before Step 4 has a session model, and because a
mandatory secret with no distribution mechanism gets set to the same value in
every deployment.

## Supersession

This ADR is expected to be superseded, and it should be. The superseding record
must cover authenticated identities, authorization by target and action per
ARCHITECTURE §10.4, audit attribution, the MQTT control plane's own
authorization, and a session model for the browser. It must land before the
first write endpoint.

## Related research

[Failure testing](../research/RES-009-failure-mode-testing.md) ·
[Configuration model](../research/RES-008-configuration-model.md)
