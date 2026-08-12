package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// maxSessionRequestBodyBytes bounds POST /api/v1/session's request body,
// so an unauthenticated caller (login is unauthenticated by construction —
// ADR-024 decision 8) cannot make this coordinator buffer an arbitrarily
// large body before rejecting it. A SHOWMESH HYPOTHESIS: large enough for
// {name, password, deviceLabel} with generous headroom, small enough that
// it costs nothing to enforce.
const maxSessionRequestBodyBytes = 8 * 1024

// handleGetSession serves GET /api/v1/session (ADR-024 decisions 5 and
// 12). Always reachable with no credential at all — see
// [v1.SessionResponse]'s doc comment for why "signed out" must be a
// readable state here, never a 401 this endpoint itself produces.
func (h *handlers) handleGetSession(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	jsonWrite(w, h.sessionResponse(r, ac, now, nil))
}

// sessionResponse builds the shared GET/POST body. created is non-nil
// only right after POST /api/v1/session mints a brand-new session, so the
// response can report its device label — GET does not otherwise carry the
// full session row (see this method's own use below for why a listing
// lookup was judged not worth its own store round trip for this step).
func (h *handlers) sessionResponse(r *http.Request, ac authContext, now time.Time, created *identity.Session) v1.SessionResponse {
	// bootstrapRequired is computed unconditionally, authenticated or not
	// — ADR-024 decision 9's banner has to reach a device that has never
	// authenticated at all, which is exactly the ac.ok=false case below.
	// An error here fails toward "show the banner": see
	// [v1.SessionResponse.BootstrapRequired]'s doc comment for why hiding
	// a real unclaimed state behind a transient store error is the wrong
	// direction to fail in.
	hasAny, err := h.deps.Identity.HasAnyPrincipal(r.Context())
	bootstrapRequired := err != nil || !hasAny
	if err != nil && h.logger != nil {
		h.logger.Warn("api: failed to check bootstrap state for GET/POST /api/v1/session; reporting bootstrapRequired=true", "error", err)
	}

	if !ac.ok {
		return v1.SessionResponse{
			ServerTime:        formatTime(now),
			Authenticated:     false,
			Scopes:            []string{},
			ScopesState:       "not_applicable",
			BootstrapRequired: bootstrapRequired,
		}
	}

	form := string(ac.result.Form)
	resp := v1.SessionResponse{
		ServerTime:    formatTime(now),
		Authenticated: true,
		Principal: &v1.PrincipalSummary{
			ID: ac.result.Principal.ID, Name: ac.result.Principal.Name,
			Kind: string(ac.result.Principal.Kind), Role: string(ac.result.Principal.Role),
		},
		CredentialForm:    &form,
		Scopes:            scopeStrings(ac.result.Principal.Role.Scopes()),
		ScopesState:       "current",
		BootstrapRequired: bootstrapRequired,
	}

	switch {
	case created != nil:
		resp.Session = &v1.SessionInfo{ID: created.ID, DeviceLabel: created.DeviceLabel, CreatedAt: formatTime(created.CreatedAt)}
	case ac.result.Form == identity.FormSession:
		// GET /api/v1/session on an existing cookie: report the session's
		// non-secret identity from what authenticating it already
		// resolved, without a second store round trip — CredentialID IS
		// the session's own [identity.Session.ID] (see that type's doc
		// comment), so this needs no lookup for the fields this step
		// actually renders. DeviceLabel and CreatedAt are left empty
		// here deliberately narrower than what a "your devices" listing
		// would eventually want (ADR-024 decision 5 names one), which is
		// not a Step 6 endpoint; see this package's report.
		resp.Session = &v1.SessionInfo{ID: ac.result.CredentialID}
	}
	return resp
}

// handleCreateSession serves POST /api/v1/session (ADR-024 decisions 1,
// 5, and 8). Unauthenticated by construction — decision 8 states this
// explicitly — so this handler is registered directly, with no
// [handlers.writeGuard]: there is no pre-existing credential to check a
// scope or a CSRF header against, only a name and password to verify.
func (h *handlers) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	var req v1.CreateSessionRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSessionRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"request body must be JSON matching {\"name\":string,\"password\":string,\"deviceLabel\":string}"))
		return
	}

	name := strings.TrimSpace(req.Name)
	deviceLabel := strings.TrimSpace(req.DeviceLabel)
	if name == "" || req.Password == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("name and password are both required and must be non-empty"))
		return
	}

	source := loginSource(r)

	if !h.loginLimiter.acquire(r.Context()) {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(h.loginLimiter.queueWait)))
		writeProblem(w, h.logger, now, tooManyRequestsProblem(
			"too many concurrent login attempts; try again shortly (ADR-024 decision 8)"))
		return
	}
	defer h.loginLimiter.release()

	// The per-source delay is applied BEFORE verification, so each
	// additional failure from this source slows every subsequent attempt
	// from it — see loginlimiter.go. It is never applied per-principal:
	// a correct password from a slowed source still succeeds, only later.
	h.loginLimiter.delay(r.Context(), source)

	principal, err := h.deps.Identity.AuthenticatePassword(r.Context(), name, req.Password)
	switch {
	case errors.Is(err, identity.ErrInvalidCredential):
		h.loginLimiter.recordFailure(source)
		h.auditAuthFailure(r, now, name, "invalid credentials")
		writeProblem(w, h.logger, now, unauthorizedProblem("invalid name or password"))
		return
	case errors.Is(err, identity.ErrDisabled):
		h.loginLimiter.recordFailure(source)
		h.auditAuthFailure(r, now, name, "principal disabled")
		writeProblem(w, h.logger, now, unauthorizedProblem("this account has been disabled by an administrator"))
		return
	case err != nil:
		h.writeInternalError(w, now, "authenticate password", err)
		return
	}
	h.loginLimiter.recordSuccess(source)

	sess, secret, err := h.deps.Identity.CreateSession(r.Context(), principal.ID, deviceLabel, now)
	if err != nil {
		h.writeInternalError(w, now, "create session", err)
		return
	}

	// ADR-024 decision 11's default rule applied here (see
	// writeAuditOrFail's doc comment in auth.go): the audit entry must
	// exist before this handler hands back a working credential. On
	// failure, the cookie is never set — the caller gets a 500 and no
	// session, even though the session row itself now exists, orphaned
	// and unreferenced by anything the caller received. That row is
	// harmless (nothing can present its secret; it was only ever
	// returned to this call frame) and is exactly the imperfect-but-safe
	// outcome this package's report names as a known limitation of not
	// owning store/identity's transaction boundary.
	if !h.writeAuditOrFail(r.Context(), w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: principal.ID, PrincipalName: principal.Name,
		Form: formPassword, ClientAddr: h.clientAddr(r),
		Action: "session.create", Target: sess.ID,
		Params: map[string]any{"deviceLabel": deviceLabel},
		Kind:   identity.AuditAdmin,
	}) {
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: secret, Path: sessionCookiePath,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode,
		MaxAge: int(identity.SessionMaxIdle / time.Second),
	})

	ac := authContext{ok: true, result: identity.Authenticated{Principal: principal, Form: identity.FormSession, CredentialID: sess.ID}}
	jsonWrite(w, h.sessionResponse(r, ac, now, &sess))
}

// handleDeleteSession serves DELETE /api/v1/session (ADR-024 decisions 5
// and 6). Registered behind [handlers.writeGuard](nil, ...) — nil because
// no ADR-024 decision 4 scope gates it, only "an authenticated principal"
// — so by the time this method runs, ac.ok is already true and, for a
// session-authenticated request, Sec-Fetch-Site has already been checked
// regardless of what this handler goes on to do with the body.
//
// An empty body revokes the session that authenticated THIS request,
// which requires that credential to be the session cookie itself. A body
// naming a sessionId instead revokes that specific session — self-service
// management of one of this principal's OTHER sessions (decision 5:
// "device-scoped and individually revocable") — and works under either
// credential form, bearer included, as long as the named session belongs
// to the authenticated principal; this is also this step's one genuine
// bearer-authenticated write, which is what proves decision 6's exemption
// against a real success path rather than only a rejection.
func (h *handlers) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())

	var req v1.DeleteSessionRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(io.LimitReader(r.Body, maxSessionRequestBodyBytes+1))
		if err := dec.Decode(&req); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				"request body, if present, must be JSON matching {\"sessionId\":string}"))
			return
		}
	}

	targetID := strings.TrimSpace(req.SessionID)
	clearOwnCookie := false

	if targetID == "" {
		if ac.result.Form != identity.FormSession {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				"DELETE /api/v1/session with no body revokes the session that authenticated this request; this request authenticated with a bearer token, which has no associated session — pass {\"sessionId\":...} to revoke a specific session instead"))
			return
		}
		targetID = ac.result.CredentialID
		clearOwnCookie = true
	} else {
		sessions, err := h.deps.Identity.ListSessions(r.Context(), ac.result.Principal.ID)
		if err != nil {
			h.writeInternalError(w, now, "list sessions", err)
			return
		}
		owned := false
		for _, s := range sessions {
			if s.ID == targetID {
				owned = true
				break
			}
		}
		if !owned {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(
				"no session with id "+strconv.Quote(targetID)+" belongs to this principal"))
			return
		}
		clearOwnCookie = ac.result.Form == identity.FormSession && targetID == ac.result.CredentialID
	}

	// ADR-024 decision 11's default rule again: the audit entry is
	// written BEFORE the revoke itself, mirroring decision 11's own
	// "for a command dispatched to an agent the dispatch entry is written
	// before dispatch. If that write fails, the command is refused" —
	// session revocation is the closest analogue this step has to that
	// shape (a coordinator-local effect this package cannot wrap in the
	// same store transaction as the audit write; see auth.go's
	// writeAuditOrFail doc comment).
	if !h.writeAuditOrFail(r.Context(), w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: "session.revoke", Target: targetID,
		Kind: identity.AuditAdmin,
	}) {
		return
	}

	if err := h.deps.Identity.RevokeSession(r.Context(), targetID); err != nil {
		h.writeInternalError(w, now, "revoke session", err)
		return
	}

	if clearOwnCookie {
		http.SetCookie(w, &http.Cookie{
			Name: sessionCookieName, Value: "", Path: sessionCookiePath,
			HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode,
			MaxAge: -1,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// retryAfterSeconds rounds d up to a whole number of seconds, minimum 1 —
// the Retry-After header's unit — never 0, which would tell a client "try
// again immediately" for a bound that exists specifically to slow it
// down.
func retryAfterSeconds(d time.Duration) int {
	secs := int((d + time.Second - 1) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return secs
}
