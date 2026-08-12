# ADR-022: The Operator UI Container Serves the API Same-Origin and Never Holds a Credential

Status: Accepted  
Date: 2026-08-11

## Context

Two questions were deliberately left open by the documents that preceded the
first UI step, and Step 4 reaches both of them on its first day.

[OPERATOR-UI §4](../architecture/OPERATOR-UI.md#4-deployment-shape) records the
first: "whether the UI container serves the API through a reverse proxy or the
browser addresses the coordinator directly with explicit cross-origin
configuration, and where TLS terminates" is "open at this stage and to be
settled when the UI is built."

[ADR-021](ADR-021-read-api-authentication-posture.md) records the second in its
consequences: "where the browser stores a shared token is an unanswered
question, and it is the reason the default is off rather than on. Turning it on
before Step 4 has a session model would force a bad answer."

They are separate questions that interact, which is why they are settled
together here rather than in two records that would each have to assume the
other's outcome.

Neither question is an authentication *mechanism* choice. Step 4 is read-only,
BUILD-PLAN puts "authentication mechanism selection" explicitly out of its
scope, and ADR-021 bars a write endpoint until a superseding ADR decides
authenticated identities, authorization by target and action, audit
attribution, MQTT authorization, and a browser session model. This record does
not attempt any of that. It decides how the browser reaches an API whose
authentication posture is already settled, and it is written to constrain the
superseding identity ADR as little as possible.

The relevant standing constraints are that the UI reaches ShowMesh only through
the public API and holds no orchestration behavior
([ADR-014](ADR-014-operator-ui-is-an-api-client.md), OPERATOR-UI §2.3), that
the API must remain usable with no UI in the deployment at all, that
authorization is enforced by the coordinator API and never by hiding a control
(OPERATOR-UI §11), and that the UI must load and function with no internet
access (OPERATOR-UI §4).

## Decision

### 1. The UI container serves the API same-origin

The Operator UI container serves the built static assets and forwards
`/api/*` to the coordinator. The browser sees exactly one origin.

The alternative, a browser addressing the coordinator directly, would require
the SPA to learn the coordinator's base URL at runtime, since static assets
cannot be built per-deployment. That means an environment-substituted
configuration document written at container start, which is a second
configuration surface for an operator to get wrong. It would also require the
operator to configure the coordinator's CORS allow-list correctly or the UI
fails in a way that looks like the coordinator being down. ADR-021's final
consequence already names a reflected-origin misconfiguration on an
unauthenticated API as a real exposure; requiring every UI deployment to
configure that allow-list makes the exposure routine rather than exceptional.

Same-origin also keeps the likely superseding identity mechanisms cheap. A
session cookie, forward-authentication from a reverse proxy, and an OIDC
redirect flow all want one origin. Cross-origin cookie sessions need
`SameSite=None`, `Secure`, mandatory TLS, and credentialed CORS, which is a
larger and more error-prone surface to inherit before anyone has decided the
mechanism.

### 2. The proxy forwards credentials. It never holds or mints them

This is the load-bearing rule of this record, and it outlives the shared-secret
posture that motivated it.

The UI container must never be configured with `SHOWMESH_API_TOKEN`, must never
inject an `Authorization` header the browser did not send, must never
terminate, issue, validate, or refresh a session of any kind, and must never
serve API content from its own cache. It passes the request through and passes
the response back.

A proxy that holds the credential would make reaching the UI equivalent to
reaching the API, which silently converts the coordinator's authentication into
"anyone who can load a web page". It would also make the proxy a security
boundary, which OPERATOR-UI §11 forbids by requiring the coordinator to enforce
authorization itself, and which ADR-014 forbids in effect by requiring the API
to be equally usable with no UI present.

### 3. The proxy is a dumb pass-through

Beyond credentials, the proxy performs no path rewriting beyond stripping
nothing and adding nothing, no request or response body transformation, no
aggregation or fan-out of API calls, no response caching of any `/api/*`
response, and no retry logic. Static assets may be cached; API responses may
not.

This is stated as a constraint rather than an implementation note because the
proxy is the one place in the architecture where UI-side behavior could
accumulate without obviously violating ADR-014: each individual convenience
would look like proxy configuration rather than like orchestration. The test is
that removing the proxy and pointing a client directly at the coordinator must
change nothing except the origin.

The proxy must also not buffer `text/event-stream` responses, which would
convert the change stream into a stream that arrives all at once when the
connection closes. This is a correctness requirement, not a performance one:
[ADR-020](ADR-020-control-api-shape-and-change-stream.md) makes the stream the
UI's real-time transport, and a buffered stream presents as a UI that shows
stale state while reporting itself connected, which is the precise failure
OPERATOR-UI §2.4 exists to prevent.

### 4. The browser holds the shared secret, in `sessionStorage`

**Superseded by [ADR-024](ADR-024-identity-authorization-and-audit.md)
decision 5, 2026-08-11.** The browser now holds a coordinator-minted HttpOnly
session cookie, and the shared secret this decision stored is retired. As
predicted below, the answer was small enough to delete. One part survives
deliberately: the bearer-paste input affordance is kept as a break-glass path,
so a machine token can act from a phone when the session path is broken. What
was deleted is the shared-secret semantics, not the input box. Decisions 1, 2,
3, and 5 of this record stand unchanged.

When the coordinator has `SHOWMESH_API_TOKEN` set, the UI discovers this by
receiving `401` with an RFC 9457 problem document. It then prompts the operator
for the secret, keeps it in `sessionStorage`, and sends it as
`Authorization: Bearer <token>` on every API request including the change
stream, which the proxy forwards verbatim per rule 2.

`sessionStorage`, not `localStorage`: the secret does not outlive the tab, so a
shared or borrowed device does not retain it indefinitely, and there is no
persistence for a future identity model to have to migrate away from. Not a
cookie: cookies would be sent automatically, which is the property that creates
a cross-site request forgery surface the moment the API gains its first write
endpoint, and nothing here needs automatic attachment.

This deliberately introduces no login, no identity, no expiry, no refresh, and
no logout beyond discarding the secret. It is a password box for one shared
secret, which is what ADR-021 actually shipped. ADR-021 feared that answering
this question early would force a bad session model; the answer that avoids
that is one small enough to delete outright when the superseding ADR lands.

### 5. TLS is not terminated by ShowMesh

Neither the UI container nor the coordinator terminates TLS in this bundle.
Deployments requiring it place a reverse proxy in front of the UI container.
This is recorded so that the OPERATOR-UI §4 open question is fully closed rather
than partially closed, and because the show VLAN remains the actual security
boundary today (ADR-021).

## Consequences

- The UI container gains a proxy component, which is a moving part the direct
  alternative would not have had. Its correctness is now load-bearing for the
  change stream, and rule 3's no-buffering requirement must be verified against
  the running container rather than assumed from configuration.
- An operator running the API open, which is the default, sees no
  authentication interaction at all. The prompt exists only for deployments
  that turned the token on.
- Because the browser holds the secret, an operator with several devices enters
  it on each, and per-tab. That is the honest cost of a shared secret with no
  distribution mechanism, and it is a reason to expect supersession rather than
  a problem to engineer around now.
- `showmeshctl` and any other non-browser client are unaffected. They continue
  to address the coordinator directly, which is what keeps ADR-014's "usable
  with no UI" property observable rather than theoretical.
- The acceptance criterion that the stack runs correctly with the UI container
  stopped and with it removed entirely is unchanged by the proxy, because
  nothing but the browser routes through it.
- A future identity ADR inherits one origin and no stored credential state,
  which is close to the cheapest starting position available. It does not
  inherit a session model it has to unwind.

## Alternatives considered

**Browser addresses the coordinator directly with a CORS allow-list.** The
strongest alternative, and genuinely simpler in component count. Rejected on
the two operator-facing costs in decision 1: a runtime base-URL configuration
document, and a CORS allow-list whose misconfiguration is indistinguishable
from an outage. The architectural purity it buys is small, because rule 2 and
rule 3 already reduce the proxy to something whose removal changes nothing.

**The UI container injects the token from its own environment.** Rejected in
decision 2. It is the most convenient option and the one that quietly converts
an access control into a formality.

**Step 4 does not support the token at all**, rendering an explicit "this
coordinator requires a token and this UI cannot supply one" state on `401`.
Honest, cheap, and rejected because it would make ADR-021's switch decorative
for any deployment that runs the UI, which is every deployment the switch was
written for. The explicit-error behavior is still required as the fallback when
the operator supplies a wrong secret.

**A cookie set by the UI container after a form post.** Rejected because it is
the session model ADR-021 specifically did not want forced, because it makes
the UI container a credential issuer in violation of rule 2, and because it
creates a CSRF surface for a write API that does not exist yet.

## Supersession

The identity and authorization ADR that ADR-021 requires before the first write
endpoint also supersedes decision 4 of this record. Decisions 1, 2, 3, and 5
are expected to survive it, and rule 2 in particular is written to constrain
it: whatever mechanism lands, the coordinator enforces it and the UI container
forwards it.

## Related research

[Configuration model](../research/RES-008-configuration-model.md) ·
[Failure testing](../research/RES-009-failure-mode-testing.md)
