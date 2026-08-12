package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is ADR-024's authentication and authorization boundary: it
// resolves a request's credential (a session cookie or a bearer token) to
// at most one [identity.Authenticated] principal, and gates routes on
// that resolution. It does not implement password verification, session
// or token storage, or the audit trail — those belong to
// internal/coordinator/identity, already built and not this task's to
// change (see that package's own doc comment for the layering rule this
// file honors: identity imports store, api imports identity, neither
// store nor identity may import api).

// noIdentityService is [Dependencies.Identity]'s nil-safe default,
// matching this package's existing "a dependency nobody has wired in yet
// is not this API failing" posture (see [Dependencies.withDefaults]'s doc
// comment in api.go). Every credential fails to authenticate — every
// Authenticate* method returns [identity.ErrInvalidCredential] — so
// [resolveCredential] always resolves ac.ok=false under it, which is what
// makes every route this package gates degrade safely with no special
// casing anywhere else: readGuard/readGuardAll/requireScope/writeGuard
// all already treat ac.ok=false as "not authenticated", and every other
// method returns a plain error (never nil, never a fabricated success) so
// a mutating call reaches this default and fails loudly rather than
// silently pretending to work.
type noIdentityService struct{}

var errIdentityNotConfigured = errors.New("api: no identity.Service was wired into this API's Dependencies")

func (noIdentityService) AuthenticatePassword(context.Context, string, string) (identity.Principal, error) {
	return identity.Principal{}, identity.ErrInvalidCredential
}

func (noIdentityService) AuthenticateToken(context.Context, string) (identity.Authenticated, error) {
	return identity.Authenticated{}, identity.ErrInvalidCredential
}

func (noIdentityService) AuthenticateSession(context.Context, string, time.Time) (identity.Authenticated, error) {
	return identity.Authenticated{}, identity.ErrInvalidCredential
}

func (noIdentityService) CreateSession(context.Context, string, string, time.Time) (identity.Session, string, error) {
	return identity.Session{}, "", errIdentityNotConfigured
}

func (noIdentityService) RevokeSession(context.Context, string) error {
	return errIdentityNotConfigured
}

func (noIdentityService) ListSessions(context.Context, string) ([]identity.Session, error) {
	return nil, nil
}

func (noIdentityService) HasAnyPrincipal(context.Context) (bool, error) { return false, nil }

func (noIdentityService) ClaimBootstrap(context.Context, string, string, string, time.Time) (identity.Principal, error) {
	return identity.Principal{}, errIdentityNotConfigured
}

func (noIdentityService) CreatePrincipal(context.Context, string, identity.Kind, identity.Role, string) (identity.Principal, error) {
	return identity.Principal{}, errIdentityNotConfigured
}

func (noIdentityService) SetPassword(context.Context, string, string) (identity.Principal, error) {
	return identity.Principal{}, errIdentityNotConfigured
}

func (noIdentityService) SetDisabled(context.Context, string, bool) (identity.Principal, error) {
	return identity.Principal{}, errIdentityNotConfigured
}

func (noIdentityService) SetRole(context.Context, string, identity.Role) (identity.Principal, error) {
	return identity.Principal{}, errIdentityNotConfigured
}

func (noIdentityService) RevokeAllSessions(context.Context, string) error {
	return errIdentityNotConfigured
}

func (noIdentityService) ListPrincipals(context.Context) ([]identity.Principal, error) {
	return nil, nil
}

func (noIdentityService) GetPrincipal(context.Context, string) (identity.Principal, error) {
	return identity.Principal{}, errIdentityNotConfigured
}

func (noIdentityService) IssueToken(context.Context, string, string, *time.Time) (identity.Token, error) {
	return identity.Token{}, errIdentityNotConfigured
}

func (noIdentityService) RevokeToken(context.Context, string) error { return errIdentityNotConfigured }

func (noIdentityService) ListTokens(context.Context, string) ([]identity.TokenInfo, error) {
	return nil, nil
}

// WriteAudit no-ops successfully rather than erroring: an unwired
// identity dependency should not turn every OTHER 401 this default
// already produces into a 500 from a failed best-effort audit write (see
// withIdentity's credential-in-url path and auditAuthFailure, both of
// which call this even when nothing else about identity is configured).
func (noIdentityService) WriteAudit(context.Context, identity.AuditEntry) error { return nil }

func (noIdentityService) ListAudit(context.Context, int64, int) ([]identity.AuditEntry, error) {
	return nil, nil
}

// sessionCookieName is the HttpOnly cookie ADR-024 decision 5 mints.
const sessionCookieName = "showmesh_session"

// sessionCookiePath scopes the cookie to this API's own routes, not the
// whole origin — the UI container's static assets never need it, and
// ADR-024 decision 5's host-scoping caveat (no TLS by default means no
// scheme scoping either) is unaffected either way. ui/nginx.conf forwards
// Set-Cookie/Cookie verbatim with no proxy_cookie_path rewrite (ADR-024's
// consequences section pins this), so a coordinator-set Path survives to
// the browser unchanged.
const sessionCookiePath = "/api"

// formPassword marks an audit entry's Form when a request authenticated a
// login attempt with a name/password pair, not a pre-existing credential.
// [identity.CredentialForm]'s own two named constants, FormSession and
// FormToken, both describe a credential that already existed before this
// request (see that type's doc comment); neither applies to the one
// request whose entire job is to create a session in the first place.
// identity.CredentialForm is a plain string underneath, so this value
// compiles, stores, and decodes exactly like FormSession/FormToken even
// though it is not one of identity's own named consts — a deliberate,
// flagged extension of a value set this task does not own the type of;
// see this package's report.
const formPassword identity.CredentialForm = "password"

// authCtxKeyType is unexported so no other package can construct a
// colliding context key.
type authCtxKeyType struct{}

var authCtxKey authCtxKeyType

// authContext is what [withIdentity] resolves per request and stores in
// its context, for every downstream route guard and handler to read via
// [authFromContext].
type authContext struct {
	// result and ok together are "no credential presented" (ok=false,
	// result zero) vs. "a credential presented and validated" (ok=true).
	// There is deliberately no third state distinguishing "presented but
	// invalid" from "absent": ADR-024 decision 6 requires a malformed or
	// non-Bearer Authorization header to be indistinguishable, to every
	// downstream consumer, from no header at all — both must produce
	// exactly ok=false, never a fallthrough to the cookie and never a
	// different problem class.
	result identity.Authenticated
	ok     bool

	// raw is the exact secret that authenticated this request — the
	// cookie value or the bearer token — kept ONLY so GET /api/v1/stream
	// (stream.go) can revalidate the same credential periodically per
	// ADR-024 decision 5. It is never logged (withRequestLogging never
	// reads the context) and never rendered on the wire; it lives no
	// longer than the request context itself, the same lifetime the
	// secret already has sitting in r.Header/r.Cookie for this request
	// regardless of anything this file does.
	raw string
}

func authFromContext(ctx context.Context) authContext {
	ac, _ := ctx.Value(authCtxKey).(authContext)
	return ac
}

func withAuthContext(ctx context.Context, ac authContext) context.Context {
	return context.WithValue(ctx, authCtxKey, ac)
}

// resolveCredential implements ADR-024 decision 6's non-negotiable
// ordering: an Authorization header, if present AT ALL, is the only
// credential path considered for this request. Malformed or non-Bearer is
// a failed resolution, never a silent fallthrough to the session cookie —
// this is what closes the URL-userinfo attack decision 6 names by name
// (a browser attaching "Authorization: Basic ..." to a top-level
// navigation, with a SameSite=Lax cookie riding alongside it, must not
// let a middleware that "falls through" skip the CSRF check on a
// cookie-authenticated write). Only when the header is entirely absent
// does a session cookie get a chance.
//
// now is threaded through explicitly, not read from a clock closed over
// this function, so [identity.Service.AuthenticateSession]'s sliding
// write agrees with the rest of this request's own notion of "now" —
// matching that method's own doc comment on its now parameter.
func resolveCredential(ctx context.Context, svc identity.Service, r *http.Request, now time.Time) authContext {
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := bearerToken(h)
		if !ok {
			return authContext{}
		}
		auth, err := svc.AuthenticateToken(ctx, tok)
		if err != nil {
			return authContext{}
		}
		return authContext{result: auth, ok: true, raw: tok}
	}

	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return authContext{}
	}
	auth, aerr := svc.AuthenticateSession(ctx, c.Value, now)
	if aerr != nil {
		return authContext{}
	}
	return authContext{result: auth, ok: true, raw: c.Value}
}

// withIdentity is this API's replacement for the retired
// SHOWMESH_API_TOKEN shared-secret check. It runs after version
// negotiation (CLAUDE.md and this task's spec both require version skew
// stay diagnosable without credentials) and on EVERY request regardless
// of route or [Options.CloseReads]:
//
//  1. ADR-024 decision 1's URL rule: a query string containing
//     [identity.TokenPrefix] is rejected outright, 400, and audited,
//     before anything else runs — including before the credential
//     resolution below, since a caller who put a token in the query
//     string gets no benefit from also trying it there.
//  2. Credential resolution (see [resolveCredential]) into the request
//     context, unconditionally. This is what makes ADR-024 decision 5's
//     "sliding happens on ANY cookie-bearing request including a read"
//     true even when [Options.CloseReads] is false and the route being
//     hit has no scope gate at all — a per-route guard could not do this,
//     because most routes never run one.
func withIdentity(identitySvc identity.Service, logger *slog.Logger, clock func() time.Time) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			now := clock()

			if strings.Contains(r.URL.RawQuery, identity.TokenPrefix) {
				// Never the query string itself, in the problem detail or
				// anywhere else — see withRequestLogging's identical
				// posture in middleware.go for RawQuery. Best-effort: an
				// audit-write failure here must not turn a security
				// rejection into a 500 that hides the real reason.
				if err := identitySvc.WriteAudit(r.Context(), identity.AuditEntry{
					Timestamp: now, Action: "credential_in_url", Target: r.URL.Path,
					Kind: identity.AuditAuthFail,
				}); err != nil && logger != nil {
					logger.Warn("api: failed to audit a credential-in-url rejection", "error", err)
				}
				writeProblem(w, logger, now, credentialInURLProblem(
					"a credential must never be presented as a query parameter; use Authorization: Bearer or the session cookie"))
				return
			}

			ac := resolveCredential(r.Context(), identitySvc, r, now)
			next.ServeHTTP(w, r.WithContext(withAuthContext(r.Context(), ac)))
		})
	}
}

// readAllScopes is every read scope the v1 read surface is gated by
// (ADR-024 decision 4). GET /api/v1/snapshot and GET /api/v1/stream each
// span every resource those four scopes individually cover, so — absent a
// combined scope ADR-024 does not define — both require the full bundle
// when [Options.CloseReads] is true. In practice this never narrows
// access beyond a single scope's worth, because every built-in role that
// holds any read scope holds all four together (see
// internal/coordinator/identity.Role.Scopes): the read scopes are never
// granted individually today. See this package's report for this
// reasoning recorded as a deliberate choice, not an oversight.
var readAllScopes = []identity.Scope{
	identity.ScopeNodeRead, identity.ScopeFPPRead, identity.ScopeObservationRead, identity.ScopeEventRead,
}

// readGuard enforces ADR-024 decision 2's "reads keep ADR-021's posture"
// rule: a no-op when h.closeReads is false (the default), so every
// existing v1 read client keeps working with no credential exactly as
// before this step; a full auth+scope check when true.
func (h *handlers) readGuard(scope identity.Scope, next http.HandlerFunc) http.HandlerFunc {
	return h.readGuardAll([]identity.Scope{scope}, next)
}

// readGuardAll is [handlers.readGuard]'s multi-scope form, for
// GET /api/v1/snapshot and GET /api/v1/stream — see [readAllScopes].
func (h *handlers) readGuardAll(scopes []identity.Scope, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.closeReads {
			next(w, r)
			return
		}
		now := h.now()
		ac := authFromContext(r.Context())
		if !ac.ok {
			writeProblem(w, h.logger, now, unauthorizedProblem(
				"this coordinator has closed the read API; a valid credential is required"))
			return
		}
		for _, s := range scopes {
			if !ac.result.Principal.Role.Has(s) {
				writeProblem(w, h.logger, now, forbiddenProblem(s))
				return
			}
		}
		next(w, r)
	}
}

// requireScope always enforces auth+scope, regardless of h.closeReads —
// used for surfaces ADR-024 adds that were never part of the original
// open-by-default read posture. GET /api/v1/audit is the only Step 6
// caller: audit is a new, always-sensitive surface, not one of the four
// scopes decision 4 names as gating "the v1 read API [that] actually
// serves" pre-existing resources.
func (h *handlers) requireScope(scope identity.Scope, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := h.now()
		ac := authFromContext(r.Context())
		if !ac.ok {
			writeProblem(w, h.logger, now, unauthorizedProblem("this endpoint requires authentication"))
			return
		}
		if !ac.result.Principal.Role.Has(scope) {
			writeProblem(w, h.logger, now, forbiddenProblem(scope))
			return
		}
		next(w, r)
	}
}

// writeGuard is [handlers.requireScope]'s write-route sibling: identical
// auth+scope enforcement (scope may be nil to mean "any authenticated
// principal, no specific scope" — DELETE /api/v1/session, which is not
// gated by any of the ADR-024 decision 4 scopes), plus ADR-024 decision
// 6's CSRF rule.
//
// The CSRF check is keyed on ac.result.Form — the credential that
// ACTUALLY authenticated this request, resolved once by [withIdentity]
// and never re-derived here from header presence. That is the load-
// bearing property decision 6 spends a full paragraph on: an
// implementation reading "if an Authorization header is present, skip the
// CSRF check" is exploitable via URL userinfo, because [resolveCredential]
// already turned a malformed Authorization header into ac.ok=false before
// this guard ever runs — there is no header-presence signal left here to
// misuse even by accident.
func (h *handlers) writeGuard(scope *identity.Scope, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		now := h.now()
		ac := authFromContext(r.Context())
		if !ac.ok {
			writeProblem(w, h.logger, now, unauthorizedProblem("this endpoint requires authentication"))
			return
		}
		if scope != nil && !ac.result.Principal.Role.Has(*scope) {
			writeProblem(w, h.logger, now, forbiddenProblem(*scope))
			return
		}
		if ac.result.Form == identity.FormSession && r.Header.Get("Sec-Fetch-Site") != "same-origin" {
			writeProblem(w, h.logger, now, csrfProblem())
			return
		}
		next(w, r)
	}
}

// clientAddr returns r's remote address for audit attribution, or "" —
// see [Options.TrustClientAddr]'s doc comment for why this defaults off
// and implements no X-Forwarded-For recovery.
func (h *handlers) clientAddr(r *http.Request) string {
	if !h.trustClientAddr {
		return ""
	}
	return r.RemoteAddr
}

// writeAuditOrFail writes entry and reports whether it succeeded, writing
// a 500 problem itself on failure. This is ADR-024 decision 11's default
// rule — "a write that cannot be attributed does not proceed" — applied
// at this layer to the two writes Step 6 adds (session create, session
// revoke): neither is in decision 11's blackout/stop/power-off safety
// class, so neither gets that exemption. See session.go's callers for how
// this is sequenced relative to the actual state change; true same-
// transaction atomicity with identity.Service's own writes is not
// achievable from this package (identity/store are not this task's to
// change), which this package's report states as a known limitation
// rather than a silent gap.
func (h *handlers) writeAuditOrFail(ctx context.Context, w http.ResponseWriter, now time.Time, entry identity.AuditEntry) bool {
	if err := h.deps.Identity.WriteAudit(ctx, entry); err != nil {
		h.writeInternalError(w, now, "write audit entry", err)
		return false
	}
	return true
}

// auditAuthFailure writes a best-effort AuditAuthFail entry for a failed
// POST /api/v1/session attempt. Best-effort, not gated like
// [handlers.writeAuditOrFail]: an authentication FAILURE is a record of an
// attempt that granted nothing, not a state change decision 11's "does
// not proceed" rule is protecting — refusing to tell a caller "invalid
// password" just because the audit table could not be written would make
// an audit outage block login diagnosis for no attribution benefit (no
// principal was ever authenticated to attribute it to).
func (h *handlers) auditAuthFailure(r *http.Request, now time.Time, attemptedName, reason string) {
	err := h.deps.Identity.WriteAudit(r.Context(), identity.AuditEntry{
		Timestamp: now, Form: formPassword, ClientAddr: h.clientAddr(r),
		Action: "session.create", Target: attemptedName,
		Params: map[string]any{"reason": reason},
		Kind:   identity.AuditAuthFail,
	})
	if err != nil && h.logger != nil {
		h.logger.Warn("api: failed to audit a login failure", "error", err)
	}
}

// scopeStrings converts a [identity.Scope] slice to its wire form, never
// nil — see [v1.SessionResponse.Scopes]'s doc comment.
func scopeStrings(scopes []identity.Scope) []string {
	out := make([]string, len(scopes))
	for i, s := range scopes {
		out[i] = string(s)
	}
	return out
}
