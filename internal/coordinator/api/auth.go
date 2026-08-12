package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
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

// RevalidateSession and RevalidateToken match AuthenticateSession/
// AuthenticateToken's own "every credential fails" posture under this
// no-op default — see [Hub.revalidateSubscribers]'s only caller of either,
// which already treats any error identically to a revoked credential.
func (noIdentityService) RevalidateSession(context.Context, string, time.Time) (identity.Authenticated, error) {
	return identity.Authenticated{}, identity.ErrInvalidCredential
}

func (noIdentityService) RevalidateToken(context.Context, string) (identity.Authenticated, error) {
	return identity.Authenticated{}, identity.ErrInvalidCredential
}

func (noIdentityService) CreateSession(context.Context, string, string, string, string, time.Time) (identity.Session, string, error) {
	return identity.Session{}, "", errIdentityNotConfigured
}

func (noIdentityService) RevokeSession(context.Context, string) error {
	return errIdentityNotConfigured
}

func (noIdentityService) ListSessions(context.Context, string) ([]identity.Session, error) {
	return nil, nil
}

func (noIdentityService) HasAnyPrincipal(context.Context) (bool, error) { return false, nil }

// EnsureBootstrap no-ops successfully: this package's own callers never
// invoke it (it is coordinator-startup-only — see
// internal/coordinator/coordinator.go), and a coordinator with no identity
// dependency wired in has nothing to bootstrap.
func (noIdentityService) EnsureBootstrap(context.Context) error { return nil }

func (noIdentityService) ClaimBootstrap(context.Context, string, string, string, string, string, identity.CredentialForm, time.Time) (identity.Principal, error) {
	return identity.Principal{}, errIdentityNotConfigured
}

// AuditedWrite's no-op default returns an error, never a fabricated
// success — an unwired identity dependency has no store.Tx to hand fn at
// all, so there is nothing safe to do here but refuse, matching every
// other write method's identical posture in this default (CreateSession,
// ClaimBootstrap, CreatePrincipal, ...).
func (noIdentityService) AuditedWrite(context.Context, func(context.Context, *store.Tx) (identity.AuditEntry, error)) error {
	return errIdentityNotConfigured
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

func (noIdentityService) InvalidateAllSessions(context.Context) error {
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
type credentialFailure struct {
	form   identity.CredentialForm
	reason string
}

func resolveCredential(ctx context.Context, svc identity.Service, r *http.Request, now time.Time) (authContext, *credentialFailure) {
	if h := r.Header.Get("Authorization"); h != "" {
		tok, ok := bearerToken(h)
		if !ok {
			return authContext{}, &credentialFailure{form: identity.FormToken, reason: "malformed_authorization"}
		}
		auth, err := svc.AuthenticateToken(ctx, tok)
		if err != nil {
			return authContext{}, &credentialFailure{form: identity.FormToken, reason: "invalid_token"}
		}
		return authContext{result: auth, ok: true, raw: tok}, nil
	}

	c, err := r.Cookie(sessionCookieName)
	if errors.Is(err, http.ErrNoCookie) {
		return authContext{}, nil
	}
	if err != nil || c.Value == "" {
		return authContext{}, &credentialFailure{form: identity.FormSession, reason: "malformed_session_cookie"}
	}
	auth, aerr := svc.AuthenticateSession(ctx, c.Value, now)
	if aerr != nil {
		return authContext{}, &credentialFailure{form: identity.FormSession, reason: "invalid_session"}
	}
	return authContext{result: auth, ok: true, raw: c.Value}, nil
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
//
// limiter throttles BOTH of this function's audit-write paths per source —
// the credential-in-url rejection below and [resolveCredential]'s own
// failure branch further down. A review finding caught the first: that
// path, unlike POST /api/v1/session and POST /api/v1/bootstrap, was
// reachable by ANY request to ANY route with no authentication and no
// bound at all, so a source repeating it could grow the append-only audit
// table without limit at full request rate (decision 11 already names
// disk exhaustion as a case this coordinator must survive; an unthrottled
// unauthenticated INSERT source is a self-inflicted way to reach it). A
// second review pass, prompted by an operator's own stale-cookie browser
// tab producing six credential.resolve rows in a few seconds of ordinary
// page loads (a stale showmesh_session cookie is expected under ADR-024
// decision 5 — cookies are scoped by host and ignore port — and nothing
// clears it until sign-in or its 90-day expiry), found the identical
// defect one branch over: resolveCredential's failure path runs on every
// request to every open read route, which is most of this API's traffic
// by design, and had no bound either. Worse than raw growth: audit
// retention keeps only the newest N rows, so an unbounded credential.resolve
// source quietly evicts genuine attribution history — the exact record
// decision 11 exists to keep.
//
// Both paths reuse the SAME per-source delay bookkeeping [handlers.loginLimiter]
// already uses for login failures — [loginLimiter.delay]/[loginLimiter.recordFailure],
// never [loginLimiter.acquire] — bounding the SUSTAINED rate from one
// source without inventing a second, independent mechanism and without
// coupling either path to the login concurrency bound itself: acquiring a
// concurrency slot here would let a flood on either path queue out and
// 429 a genuinely different source's correct-password login, reproducing
// the exact cross-source starvation shape finding 2 exists to prevent,
// just one endpoint over. This is a real but partial mitigation, honestly
// short of a hard cap: many concurrent requests from one source can still
// each observe a low delay before any of their own recordFailure calls
// have landed, so a sufficiently parallel burst is throttled less than a
// sequential one — see this task's report.
func withIdentity(identitySvc identity.Service, limiter *loginLimiter, logger *slog.Logger, clock func() time.Time, trustClientAddr bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			now := clock()

			if strings.Contains(r.URL.RawQuery, identity.TokenPrefix) {
				source := loginSource(r)
				if limiter != nil {
					limiter.delay(r.Context(), source)
				}

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
				if limiter != nil {
					limiter.recordFailure(source)
				}
				writeProblem(w, logger, now, credentialInURLProblem(
					"a credential must never be presented as a query parameter; use Authorization: Bearer or the session cookie"))
				return
			}

			ac, failure := resolveCredential(r.Context(), identitySvc, r, now)
			if failure != nil {
				// Throttled the same way as the credential-in-url path
				// above, and for the identical reason: this branch runs on
				// EVERY request, on every route, including every open read
				// — see the doc comment on limiter above this function for
				// the incident that surfaced it (a stale session cookie
				// from an unrelated local stack, ordinary page loads,
				// six audit rows in a few seconds).
				source := loginSource(r)
				if limiter != nil {
					limiter.delay(r.Context(), source)
				}

				entry := identity.AuditEntry{
					Timestamp: now,
					Form:      failure.form,
					Action:    "credential.resolve",
					Target:    r.URL.Path,
					Params:    map[string]any{"reason": failure.reason},
					Kind:      identity.AuditAuthFail,
				}
				if trustClientAddr {
					entry.ClientAddr = r.RemoteAddr
				}
				if err := identitySvc.WriteAudit(r.Context(), entry); err != nil && logger != nil {
					logger.Warn("api: failed to audit credential resolution failure", "form", failure.form, "reason", failure.reason, "error", err)
				}
				if limiter != nil {
					limiter.recordFailure(source)
				}
			}
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
//
// The check is a DENY-LIST — "require the header unless the credential is
// a bearer token" — never an allow-list keyed on FormSession specifically.
// A review finding caught the earlier `== identity.FormSession` form: only
// two CredentialForm values ever reach this point today (resolveCredential
// only ever constructs FormSession or FormToken), so the two read
// identically FOR NOW, but identity.CredentialForm is a plain string and
// this package's own report already flagged formPassword/formCLI as
// values the type grows beyond its two authenticating constants — an
// allow-list silently exempts every one of those from CSRF the day one of
// them becomes reachable through this guard, where a deny-list still
// requires the header for anything that is not proven, by name, to be
// exempt.
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
		if ac.result.Form != identity.FormToken && !sameOriginCSRFOK(r) {
			writeProblem(w, h.logger, now, csrfProblem())
			return
		}
		next(w, r)
	}
}

// sameOriginCSRFOK is ADR-024 decision 6's CSRF predicate: "a write
// authenticated by cookie requires Sec-Fetch-Site: same-origin, and is
// rejected when the header is absent." Factored out of [handlers.writeGuard]
// (Step 7 seam 0, S0-2) into this one function, so [handlers.loginCSRFGuard]
// — POST /api/v1/session and POST /api/v1/bootstrap's identical requirement,
// decided 2026-08-12: strict — calls the SAME predicate rather than a
// second, hand-copied comparison that could silently drift out of sync
// with this one. There is deliberately one CSRF rule in this codebase, not
// two.
func sameOriginCSRFOK(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Site") == "same-origin"
}

// loginCSRFGuard wraps next with S0-2's login CSRF rule for
// POST /api/v1/session and POST /api/v1/bootstrap. Both endpoints are
// unauthenticated by construction (ADR-024 decision 8: there is no
// principal yet for a credential to name), so — unlike [handlers.writeGuard]
// — this predicate runs on its OWN, with no auth or scope gate before or
// after it; that ordering difference is the only thing that may differ
// between the two call sites, per S0-2's spec. There is also no bearer
// exemption here: writeGuard's exemption exists because nothing attaches
// an Authorization header automatically, but a login request has no
// pre-existing credential to BE bearer-shaped in the first place, so the
// exemption has no case to apply to.
//
// What this costs, stated rather than discovered later: a curl login must
// pass the header explicitly, and a browser that sends no Sec-Fetch-Site
// at all (Safari before 16.4) cannot log in via the cookie path — ADR-024
// decision 6 already bars exactly those devices from cookie-authenticated
// writes, so the cookie they are being denied here could not have
// performed one anyway; their path is decision 5's bearer-paste
// break-glass affordance, unchanged.
func (h *handlers) loginCSRFGuard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !sameOriginCSRFOK(r) {
			// loginCSRFProblem, never csrfProblem: the latter's Detail
			// claims "a cookie-authenticated write" and "a bearer-token-
			// authenticated request is exempt", neither of which is true
			// for these two unauthenticated-by-construction endpoints — see
			// [loginCSRFProblem]'s doc comment.
			writeProblem(w, h.logger, h.now(), loginCSRFProblem())
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
// rule — "a write that cannot be attributed does not proceed" — applied at
// this layer to a write whose audit entry is NOT written in the same
// transaction as its state change: session.revoke's own Dispatch entry
// (session.go's handleDeleteSession), which must be written and confirmed
// successful BEFORE the revoke itself runs, per decision 11's
// write-before-dispatch rule for an effect that has not happened yet (the
// same rule a command dispatched to an agent follows, and the closest
// analogue a coordinator-local effect has to one — see that handler's own
// doc comment). session.create (both POST /api/v1/session and
// POST /api/v1/bootstrap) no longer uses this: as of Step 7 seam 0,
// identity.Service.CreateSession and identity.Service.ClaimBootstrap write
// their OWN audit entry inside the SAME transaction as the state change
// (ADR-024 decision 11's same-transaction rule, via
// identity.Service.AuditedWrite), which is a strictly stronger guarantee
// than this method provides and closes the atomicity gap this method used
// to paper over for those two writes.
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
		Timestamp: now, Form: identity.FormPassword, ClientAddr: h.clientAddr(r),
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
