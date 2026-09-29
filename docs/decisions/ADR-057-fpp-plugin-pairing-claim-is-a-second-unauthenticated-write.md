# ADR-057: The FPP plugin pairing claim is a second write that takes no principal

Status: Accepted (owner, 2026-09-22 pairing ruling, recorded 2026-09-29)
Date: 2026-09-29
Amends: [ADR-055](ADR-055-one-command-install-and-node-enrollment.md), the
consequence line "The redeem endpoint is the only write the API accepts
without a principal."

## Context

An FPP plugin that is being paired has no credential yet. The operator opens a
pairing from the UI or `showmeshctl` by entering the code the plugin shows on
its own page. The plugin derives that code from a secret it generated, and
then has to prove it holds the secret and receive its API token. It has
nothing else to present, so the route that hands over the token cannot require
a principal.

[ADR-055](ADR-055-one-command-install-and-node-enrollment.md) recorded node
enrollment redeem as the only write the API accepts without a principal. The
pairing claim route, `POST /api/v1/integrations/fpp/pairing/claim`, is a
second one, and a test, two OpenAPI descriptions and three code comments still
said redeem was the only one.

## Decision

1. The pairing claim route takes no principal and no same-origin check. The
   secret in the request body is the credential.
2. The API has exactly two writes that take no principal: node enrollment
   redeem and the pairing claim. A test reads the route table and fails on any
   other.
3. The claim route is bounded in these ways:
   - A pairing is open for ten minutes and is then dropped, and its unclaimed
     token is revoked. The token also carries its own expiry, two minutes past
     the pairing's, so a failed revoke cannot leave it alive.
   - The token is delivered once, in the claim response, to the caller that
     presents the matching secret. It is never in an operator-facing response.
   - Opening a new pairing for the same player replaces the earlier one and
     revokes its token. The coordinator shutting down revokes every open one.
   - A claim is limited per client address over a rolling minute.
   - Every refusal is the same `404` with the same text.
   - The claim limiter and the redeem limiter are separate and share no state.
   - The request body is limited to 4 KiB.
4. A successful claim revokes every other token the plugin's principal holds,
   so a re-pair leaves exactly one live token.
5. Opening a pairing is refused with `409` when the same code is already
   waiting on a different player.

## Consequences

- The sentence in ADR-055 is superseded by decision 2 above. Its decision text
  is unchanged. Its failure limit rationale applies to redeem only.
- The claim is matched against a 40-bit code that the plugin shows on FPP's
  own web page, which is usually reachable without a login on the show
  network. The claim limit does not bound a guess of a secret that hashes to a
  known code the way redeem's failure limit bounds a short enrollment code.
  Someone on that network who reads the code while it is displayed could
  produce a matching secret and race the real plugin for the token, which
  holds the `scheduler` role. The owner is ruling separately on the code
  length. Until that ruling this is accepted for dedicated show networks, as
  ADR-055 accepts cleartext redeem.
- Because a pairing token is only ever handed out once, a plugin that loses
  it has to be paired again.

## Related

- [ADR-024](ADR-024-identity-authorization-and-audit.md): principals, roles
  and tokens.
- [ADR-055](ADR-055-one-command-install-and-node-enrollment.md): node
  enrollment and the first unauthenticated write.
