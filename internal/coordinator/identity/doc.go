// Package identity is ADR-024's implementation: principals, roles,
// scopes, password and API-token credentials, browser sessions, bootstrap,
// and the audit trail every write action produces. It is the mechanism
// ADR-021 rule 5 required before the first write endpoint could exist —
// Step 6 adds no write endpoint of its own (see BUILD-PLAN.md's Step 6),
// only this package and the internal/coordinator/store schema it persists
// against.
//
// # Layering
//
// This package imports internal/coordinator/store and is imported by
// internal/coordinator/api; it must never import internal/coordinator/api
// itself (an import-graph test enforces this the same way one already
// enforces cmd/showmeshctl never importing a coordinator package — see
// CLAUDE.md). store holds schemaV5's tables and exposes store-shaped
// record types (store.PrincipalRecord, store.TokenRecord,
// store.SessionRecord, store.AuditRecord, store.BootstrapRecord); this
// package holds the domain types (Principal, Session, Authenticated,
// AuditEntry) the API layer actually consumes, and Service's concrete
// implementation is the only place the two meet and convert between each
// other. This mirrors the existing split between store.HelloRecord and
// whatever internal/coordinator/inventory builds from it — store stores
// evidence, the calling package computes and exposes verdicts and domain
// objects — applied here to identity instead of liveness.
//
// # A signature this package deliberately does not implement literally
//
// The Step 6 seam contract's Session type comments ID as "opaque, high
// entropy; this is the cookie value". Taken literally and applied to every
// method that returns a Session, that would mean [Service.ListSessions]
// returns the live, bearer-equivalent cookie secret of every session
// belonging to a principal — including sessions other than the one making
// the request — in an ordinary JSON API response. That directly defeats
// the reason the cookie is HttpOnly in the first place (ADR-024 decision
// 5): the entire point of HttpOnly is that JavaScript on the page cannot
// read the authentication credential, and a listing endpoint that hands
// the same value back in a response body makes it readable by exactly the
// script HttpOnly was meant to stop. It also contradicts the contract's
// own [Authenticated.CredentialID] doc comment two paragraphs later,
// which says plainly that a session's audit-attribution identifier is
// "never the secret itself" — the contract cannot simultaneously mean
// "Session.ID is the cookie value" and "the session identifier used for
// attribution is never the cookie value" without one of those two
// sentences being wrong.
//
// This package resolves the contradiction in favor of the security
// property and the explicit "never the secret itself" sentence, not the
// offhand type comment: [Session.ID] is a stable, non-secret, per-row
// identifier, generated independently of the session's actual bearer
// secret, safe to return from [Service.ListSessions] and to accept as
// [Service.RevokeSession]'s argument. The literal cookie value the
// browser must present on every request is returned ONLY once, at the
// moment [Service.CreateSession] mints it, as that method's second return
// value — a signature change from the seam contract's single-Session
// return, made and flagged rather than made silently, exactly as the
// contract's own preamble asks a builder who disagrees with a signature
// to do. See [Service.CreateSession]'s doc comment for the mechanics, and
// the Step 6 build report for this reasoning restated for the
// orchestrating session.
package identity
