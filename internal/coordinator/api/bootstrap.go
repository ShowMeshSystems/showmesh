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

// This file is ADR-024 decision 9's network-reachable half of bootstrap:
// POST /api/v1/bootstrap, the endpoint that closes what the Step 6 spec
// calls "a hole in the middle of the feature" — identity.Service.ClaimBootstrap
// and identity.Service.HasAnyPrincipal already exist and are fully tested,
// but nothing in this package called either one before this file existed,
// which meant there was no way to create the first principal at all and
// the entire write surface was unusable end to end. The other half —
// a coordinator subcommand doing the identical claim locally against the
// data volume — is cmd/showmesh-coordinator/main.go's, not this package's;
// see that file for why a host-level path exists alongside this one.

// handleClaimBootstrap serves POST /api/v1/bootstrap. Unauthenticated by
// construction, exactly like POST /api/v1/session (decision 8's reasoning
// applies identically: no principal exists yet for a credential to name)
// — registered with no [handlers.writeGuard], and bounded by the SAME
// [handlers.loginLimiter] instance POST /api/v1/session uses, per this
// step's spec ("It must be bounded by the same login limiter as POST
// /session, for the same reason"): an unauthenticated, argon2id-costed
// endpoint reachable by anyone on the VLAN is exactly decision 8's threat
// model, and a bootstrap code is guessable in principle the same way a
// password is, even though in practice it is 160 bits of crypto/rand
// entropy rather than an operator-chosen password.
//
// A successful claim creates the first administrator and immediately
// mints a session for it — mirroring handleCreateSession's own shape
// (login-cost bound, then AuthenticatePassword-equivalent verification,
// then CreateSession, then the decision-11 audit-before-cookie ordering)
// closely enough that this handler reuses [handlers.sessionResponse]
// verbatim for its success body rather than inventing a second response
// shape POST /api/v1/session already defines.
func (h *handlers) handleClaimBootstrap(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	var req v1.BootstrapRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxSessionRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"request body must be JSON matching {\"code\":string,\"name\":string,\"password\":string,\"deviceLabel\":string}"))
		return
	}

	code := strings.TrimSpace(req.Code)
	name := strings.TrimSpace(req.Name)
	deviceLabel := strings.TrimSpace(req.DeviceLabel)
	if code == "" || name == "" || req.Password == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("code, name, and password are all required and must be non-empty"))
		return
	}

	source := loginSource(r)

	// Identical shape to handleCreateSession's own login-cost bound — same
	// loginLimiter instance, same delay-then-acquire-then-release sequence
	// (see that handler's doc comment for why the delay runs BEFORE
	// acquiring a slot, never while holding one) — per this step's spec
	// requirement that bootstrap "be bounded by the same login limiter as
	// POST /session, for the same reason".
	h.loginLimiter.delay(r.Context(), source)

	if !h.loginLimiter.acquire(r.Context()) {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfterSeconds(h.loginLimiter.queueWait)))
		writeProblem(w, h.logger, now, tooManyRequestsProblem(
			"too many concurrent login/bootstrap attempts; try again shortly (ADR-024 decision 8)"))
		return
	}
	defer h.loginLimiter.release()

	// ClaimBootstrap (Step 7 seam 0) now writes its own "bootstrap.claim"
	// audit entry atomically with the principal creation and the bootstrap
	// row's claim (ADR-024 decision 11's same-transaction rule) — see that
	// method's doc comment. h.clientAddr(r) is passed through so the audit
	// entry can record it under [Options.TrustClientAddr]'s existing rule.
	// deviceLabel restores the F6 review finding's other half (it fell off
	// the entry entirely in an earlier version of this handler); identity.
	// FormPassword is this endpoint's genuine credential — a network caller
	// verified a password over HTTP, unlike the host-shell `bootstrap`
	// subcommand's filesystem-access credential (identity.FormCLI) — see
	// [identity.Service.ClaimBootstrap]'s doc comment for why the two must
	// stay distinguishable in the audit log.
	principal, err := h.deps.Identity.ClaimBootstrap(r.Context(), code, name, req.Password, deviceLabel, h.clientAddr(r), identity.FormPassword, now)
	switch {
	case errors.Is(err, identity.ErrInvalidCredential),
		errors.Is(err, identity.ErrBootstrapClaimed),
		errors.Is(err, identity.ErrBootstrapExpired),
		errors.Is(err, identity.ErrBootstrapNotAvailable):
		h.loginLimiter.recordFailure(source)
		// The audit target is the ATTEMPTED name, exactly like
		// handleCreateSession's own failure path — never the code, which
		// this method never passes to auditAuthFailure at all, and never
		// a distinct reason per sentinel: decision 1's "a caller must not
		// be able to distinguish" reasoning for AuthenticatePassword
		// applies here identically — an unclaimed-vs-expired-vs-wrong-code
		// distinction is exactly the oracle a bootstrap-code guesser would
		// want, and there is no legitimate caller on the other end of this
		// endpoint (unlike a human mistyping their own password) who needs
		// the more specific answer.
		h.auditAuthFailure(r, now, name, "bootstrap claim failed")
		writeProblem(w, h.logger, now, unauthorizedProblem("invalid, already-used, or expired bootstrap code"))
		return
	case err != nil:
		h.writeInternalError(w, now, "claim bootstrap", err)
		return
	}
	h.loginLimiter.recordSuccess(source)

	// CreateSession (Step 7 seam 0) writes its own "session.create" audit
	// entry atomically with the session row, identical to
	// handleCreateSession's own path — see that method's doc comment.
	sess, secret, err := h.deps.Identity.CreateSession(r.Context(), principal.ID, principal.Name, deviceLabel, h.clientAddr(r), now)
	if err != nil {
		h.writeInternalError(w, now, "create session after bootstrap claim", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: secret, Path: sessionCookiePath,
		HttpOnly: true, Secure: h.secureCookie, SameSite: http.SameSiteLaxMode,
		MaxAge: int(identity.SessionMaxIdle / time.Second), // matching handleCreateSession's identical MaxAge expression
	})

	ac := authContext{ok: true, result: identity.Authenticated{Principal: principal, Form: identity.FormSession, CredentialID: sess.ID}}
	jsonWrite(w, h.sessionResponse(r, ac, now, &sess))
}
