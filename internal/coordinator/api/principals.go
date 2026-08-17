package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Track G seam G-5: identity administration over the API —
// principals, their roles and enabled state, their passwords, and their
// API tokens. Before this file, every one of these was reachable only
// through a `showmesh-coordinator` host subcommand (ADR-024 decision 9's
// bootstrap/lockout-recovery posture), which requires container exec
// access on a distroless coordinator image with no shell. This is
// ScopePrincipalWrite's first caller and ScopePrincipalRead's only one.
//
// Bootstrap (creating the FIRST principal) stays coordinator-local per
// ADR-024 decision 9 — no principal exists yet to authenticate a request
// against, so there is nothing this file could gate. The coordinator
// subcommands remain the break-glass path for a coordinator with no
// reachable administrator (subcommands.go's own updated help text says
// so); this file is the ordinary path once at least one admin exists.
//
// identity.Service.CreatePrincipal/SetRole/SetDisabled/SetPassword/
// IssueToken/RevokeToken have no AuditedWrite-closure form (unlike
// CreateSession/ClaimBootstrap), so every mutation below follows
// session.go's handleDeleteSession precedent: a Dispatch audit entry is
// written and must succeed BEFORE the mutation runs (ADR-024 decision
// 11's fail-closed rule — principal:write is one of the two scopes that
// stays fail-closed on audit, never the blackout/stop/power-off
// exemption), and a best-effort Outcome entry follows, recording what
// actually happened without gating the response on it.

// maxPrincipalRequestBodyBytes bounds every request body this file
// decodes, mirroring session.go's maxSessionRequestBodyBytes: generous for
// a name/role/kind/password or a token label, small enough that a
// misbehaving caller cannot make a handler buffer an unbounded body before
// validation runs.
const maxPrincipalRequestBodyBytes = 8 * 1024

// scopePrincipalWrite exists only so api.go's route registration can take
// its address — [handlers.writeGuard] takes *identity.Scope, and
// identity.ScopePrincipalWrite is a typed string constant, whose address Go
// does not allow taking directly. Mirrors scopeConfigWrite (config.go).
var scopePrincipalWrite = identity.ScopePrincipalWrite

func mapPrincipalObject(p identity.Principal) v1.PrincipalObject {
	return v1.PrincipalObject{
		ID: p.ID, Name: p.Name, Kind: string(p.Kind), Role: string(p.Role),
		Disabled: p.Disabled, HasPassword: p.HasPassword, Reserved: p.Reserved,
		CreatedAt: formatTime(p.CreatedAt),
	}
}

func mapTokenObject(t identity.TokenInfo) v1.TokenObject {
	return v1.TokenObject{
		ID: t.ID, PrincipalID: t.PrincipalID, Hint: t.Hint, Label: t.Label,
		CreatedAt:  formatTime(t.CreatedAt),
		ExpiresAt:  formatTimePtr(t.ExpiresAt),
		LastUsedAt: formatTimePtr(t.LastUsedAt),
	}
}

// handleListPrincipals serves GET /api/v1/principals. Requires
// principal:read; the reserved recovery principal is included (Reserved:
// true) — see [identity.Principal.Reserved]'s own doc comment: "visible
// wherever principals are listed".
func (h *handlers) handleListPrincipals(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	principals, err := h.deps.Identity.ListPrincipals(r.Context())
	if err != nil {
		h.writeInternalError(w, now, "list principals", err)
		return
	}
	out := make([]v1.PrincipalObject, 0, len(principals))
	for _, p := range principals {
		out = append(out, mapPrincipalObject(p))
	}
	jsonWrite(w, v1.PrincipalsResponse{ServerTime: formatTime(now), Principals: out})
}

// handleGetPrincipal serves GET /api/v1/principals/{id}. Requires
// principal:read.
func (h *handlers) handleGetPrincipal(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	p, err := h.deps.Identity.GetPrincipal(r.Context(), id)
	if errors.Is(err, store.ErrPrincipalNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem("no principal with id "+strconv.Quote(id)))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get principal", err)
		return
	}
	jsonWrite(w, v1.PrincipalResponse{ServerTime: formatTime(now), Principal: mapPrincipalObject(p)})
}

// handleCreatePrincipal serves POST /api/v1/principals. Requires
// principal:write. Never guarded by [handlers.wouldLockOutAdministration]:
// creating a principal can only ever add a way to authenticate, never
// remove one.
func (h *handlers) handleCreatePrincipal(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	var req v1.CreatePrincipalRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxPrincipalRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			`request body must be JSON matching {"name":string,"kind":"human"|"machine","role":string,"password":string}`))
		return
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("name is required and must be non-empty"))
		return
	}

	var kind identity.Kind
	switch strings.TrimSpace(req.Kind) {
	case string(identity.KindHuman):
		kind = identity.KindHuman
	case string(identity.KindMachine):
		kind = identity.KindMachine
	default:
		writeProblem(w, h.logger, now, invalidParameterProblem(
			fmt.Sprintf("kind must be %q or %q", identity.KindHuman, identity.KindMachine)))
		return
	}

	role, err := identity.ParseRole(strings.TrimSpace(req.Role))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	// A password is optional here exactly like the create-principal
	// subcommand: an empty password is CreatePrincipal's own documented
	// tolerance for a machine principal that will only ever use an issued
	// token (identity.Service.CreatePrincipal's doc comment). Never placed
	// in the audit entry's Params below.
	var created identity.Principal
	mutateErr, dispatchOK := h.auditPrincipalWrite(w, r, now, "principal.create", name,
		map[string]any{"kind": string(kind), "role": string(role)},
		func() error {
			p, err := h.deps.Identity.CreatePrincipal(r.Context(), name, kind, role, req.Password)
			created = p
			return err
		})
	if !dispatchOK {
		return
	}
	if mutateErr != nil {
		switch {
		case errors.Is(mutateErr, identity.ErrReservedPrincipal):
			writeProblem(w, h.logger, now, invalidParameterProblem(mutateErr.Error()))
		case errors.Is(mutateErr, store.ErrPrincipalNameTaken):
			writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("a principal named %q already exists", name)))
		default:
			h.writeInternalError(w, now, "create principal", mutateErr)
		}
		return
	}
	jsonWrite(w, v1.PrincipalResponse{ServerTime: formatTime(now), Principal: mapPrincipalObject(created)})
}

// handleSetPrincipalRole serves PUT /api/v1/principals/{id}/role. Requires
// principal:write. Guarded by [handlers.wouldLockOutAdministration] when
// the requested role would no longer hold principal:write — demoting the
// coordinator's last reachable administrator is exactly [handlers.handleDisablePrincipal]'s
// lockout in a different disguise.
func (h *handlers) handleSetPrincipalRole(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	var req v1.SetPrincipalRoleRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxPrincipalRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(`request body must be JSON matching {"role":string}`))
		return
	}
	role, err := identity.ParseRole(strings.TrimSpace(req.Role))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	if !role.Has(identity.ScopePrincipalWrite) {
		locked, lerr := h.wouldLockOutAdministration(r.Context(), now, id, "")
		if lerr != nil {
			h.writeInternalError(w, now, "check administrator lockout", lerr)
			return
		}
		if locked {
			writeProblem(w, h.logger, now, principalLockoutProblem(
				"changing this principal's role would leave no enabled principal able to reach principal:write; "+
					"promote another principal to admin first, or use the coordinator's own break-glass subcommands"))
			return
		}
	}

	var updated identity.Principal
	mutateErr, dispatchOK := h.auditPrincipalWrite(w, r, now, "principal.set_role", id,
		map[string]any{"role": string(role)},
		func() error {
			p, err := h.deps.Identity.SetRole(r.Context(), id, role)
			updated = p
			return err
		})
	if !dispatchOK {
		return
	}
	if mutateErr != nil {
		h.writePrincipalMutationError(w, now, mutateErr, id)
		return
	}
	jsonWrite(w, v1.PrincipalResponse{ServerTime: formatTime(now), Principal: mapPrincipalObject(updated)})
}

// handleEnablePrincipal serves POST /api/v1/principals/{id}/enable.
// Requires principal:write. Never guarded by lockout: re-enabling only
// ever adds back a way to authenticate.
func (h *handlers) handleEnablePrincipal(w http.ResponseWriter, r *http.Request) {
	h.setPrincipalDisabled(w, r, false)
}

// handleDisablePrincipal serves POST /api/v1/principals/{id}/disable.
// Requires principal:write. Guarded by
// [handlers.wouldLockOutAdministration] (requirement 3, ADR-039 decision
// 8): refuses to disable the coordinator's last enabled administrator.
func (h *handlers) handleDisablePrincipal(w http.ResponseWriter, r *http.Request) {
	h.setPrincipalDisabled(w, r, true)
}

func (h *handlers) setPrincipalDisabled(w http.ResponseWriter, r *http.Request, disabled bool) {
	now := h.now()
	id := r.PathValue("id")

	if disabled {
		locked, lerr := h.wouldLockOutAdministration(r.Context(), now, id, "")
		if lerr != nil {
			h.writeInternalError(w, now, "check administrator lockout", lerr)
			return
		}
		if locked {
			writeProblem(w, h.logger, now, principalLockoutProblem(
				"disabling this principal would leave no enabled principal able to reach principal:write; "+
					"enable or promote another administrator first, or use the coordinator's own break-glass subcommands"))
			return
		}
	}

	action := "principal.enable"
	if disabled {
		action = "principal.disable"
	}

	var updated identity.Principal
	mutateErr, dispatchOK := h.auditPrincipalWrite(w, r, now, action, id, nil, func() error {
		p, err := h.deps.Identity.SetDisabled(r.Context(), id, disabled)
		updated = p
		return err
	})
	if !dispatchOK {
		return
	}
	if mutateErr != nil {
		h.writePrincipalMutationError(w, now, mutateErr, id)
		return
	}
	jsonWrite(w, v1.PrincipalResponse{ServerTime: formatTime(now), Principal: mapPrincipalObject(updated)})
}

// handleResetPrincipalPassword serves POST /api/v1/principals/{id}/password.
// Requires principal:write. Never guarded by lockout: a password reset
// bumps the target's generation (identity.Service.SetPassword ->
// store.Store.SetPrincipalPasswordHash), invalidating THEIR OWN existing
// sessions and tokens, but the new password itself is a credential, not a
// removal of one — the operator resetting it can sign in again with it
// immediately.
func (h *handlers) handleResetPrincipalPassword(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	var req v1.SetPrincipalPasswordRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxPrincipalRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(`request body must be JSON matching {"password":string}`))
		return
	}
	if req.Password == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("password is required and must be non-empty"))
		return
	}

	var updated identity.Principal
	mutateErr, dispatchOK := h.auditPrincipalWrite(w, r, now, "principal.reset_password", id, nil, func() error {
		p, err := h.deps.Identity.SetPassword(r.Context(), id, req.Password)
		updated = p
		return err
	})
	if !dispatchOK {
		return
	}
	if mutateErr != nil {
		h.writePrincipalMutationError(w, now, mutateErr, id)
		return
	}
	jsonWrite(w, v1.PrincipalResponse{ServerTime: formatTime(now), Principal: mapPrincipalObject(updated)})
}

// handleListPrincipalTokens serves GET /api/v1/principals/{id}/tokens.
// Requires principal:read. 404s on an unknown principal id rather than
// silently rendering an empty list, so a typo'd id is visible.
func (h *handlers) handleListPrincipalTokens(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	if _, err := h.deps.Identity.GetPrincipal(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrPrincipalNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem("no principal with id "+strconv.Quote(id)))
			return
		}
		h.writeInternalError(w, now, "get principal", err)
		return
	}

	tokens, err := h.deps.Identity.ListTokens(r.Context(), id)
	if err != nil {
		h.writeInternalError(w, now, "list tokens", err)
		return
	}
	out := make([]v1.TokenObject, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, mapTokenObject(t))
	}
	jsonWrite(w, v1.TokensResponse{ServerTime: formatTime(now), Tokens: out})
}

// handleIssuePrincipalToken serves POST /api/v1/principals/{id}/tokens.
// Requires principal:write. The response's Value field is this token's
// only appearance anywhere on the wire, ever again (ADR-024 decision 1) —
// GET .../tokens and this same endpoint's own audit entry never carry it.
func (h *handlers) handleIssuePrincipalToken(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")

	var req v1.IssueTokenRequest
	if r.ContentLength != 0 {
		dec := json.NewDecoder(io.LimitReader(r.Body, maxPrincipalRequestBodyBytes+1))
		if err := dec.Decode(&req); err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				`request body, if present, must be JSON matching {"label":string,"expiresAt":string|null}`))
			return
		}
	}

	// ExpiresAt absent or null both mean "never expires" (ADR-024 decision
	// 1's default) — see v1.IssueTokenRequest's own doc comment for why a
	// pointer already collapses both cases to the one meaning this needs.
	var expiresAt *time.Time
	if req.ExpiresAt != nil {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			writeProblem(w, h.logger, now, invalidParameterProblem(
				"expiresAt must be an RFC3339 timestamp, or absent/null for no expiry"))
			return
		}
		expiresAt = &t
	}

	label := strings.TrimSpace(req.Label)

	var tok identity.Token
	mutateErr, dispatchOK := h.auditPrincipalWrite(w, r, now, "token.issue", id,
		map[string]any{"label": label},
		func() error {
			t, err := h.deps.Identity.IssueToken(r.Context(), id, label, expiresAt)
			tok = t
			return err
		})
	if !dispatchOK {
		return
	}
	if mutateErr != nil {
		h.writePrincipalMutationError(w, now, mutateErr, id)
		return
	}

	jsonWrite(w, v1.IssueTokenResponse{
		ServerTime: formatTime(now),
		Token: v1.TokenObject{
			ID: tok.ID, PrincipalID: id, Hint: tok.Hint, Label: label,
			CreatedAt: formatTime(now), ExpiresAt: formatTimePtr(expiresAt),
		},
		Value: tok.Value,
	})
}

// handleRevokeToken serves DELETE /api/v1/principals/{id}/tokens/{tokenId}.
// Requires principal:write. Guarded by
// [handlers.wouldLockOutAdministration] (requirement 3): refuses to revoke
// the last credential able to reach principal:write.
func (h *handlers) handleRevokeToken(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	tokenID := r.PathValue("tokenId")

	tokens, err := h.deps.Identity.ListTokens(r.Context(), id)
	if err != nil {
		h.writeInternalError(w, now, "list tokens", err)
		return
	}
	owned := false
	for _, t := range tokens {
		if t.ID == tokenID {
			owned = true
			break
		}
	}
	if !owned {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no token "+strconv.Quote(tokenID)+" belongs to principal "+strconv.Quote(id)))
		return
	}

	locked, lerr := h.wouldLockOutAdministration(r.Context(), now, "", tokenID)
	if lerr != nil {
		h.writeInternalError(w, now, "check administrator lockout", lerr)
		return
	}
	if locked {
		writeProblem(w, h.logger, now, principalLockoutProblem(
			"revoking this token would leave no enabled principal able to reach principal:write via a password "+
				"or another active token; issue a replacement credential first, or use the coordinator's own "+
				"break-glass subcommands"))
		return
	}

	mutateErr, dispatchOK := h.auditPrincipalWrite(w, r, now, "token.revoke", tokenID, nil, func() error {
		return h.deps.Identity.RevokeToken(r.Context(), tokenID)
	})
	if !dispatchOK {
		return
	}
	if mutateErr != nil {
		h.writeInternalError(w, now, "revoke token", mutateErr)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writePrincipalMutationError maps the errors SetRole/SetDisabled/
// SetPassword/IssueToken can return onto a problem response. Shared so the
// three refusal cases (reserved principal, unknown principal, anything
// else) are classified identically everywhere in this file.
func (h *handlers) writePrincipalMutationError(w http.ResponseWriter, now time.Time, err error, id string) {
	switch {
	case errors.Is(err, identity.ErrReservedPrincipal):
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
	case errors.Is(err, store.ErrPrincipalNotFound):
		writeProblem(w, h.logger, now, resourceNotFoundProblem("no principal with id "+strconv.Quote(id)))
	default:
		h.writeInternalError(w, now, "update principal", err)
	}
}

// auditPrincipalWrite runs ADR-024 decision 11's fail-closed dispatch/
// outcome pattern for one principal or token mutation, matching
// session.go's handleDeleteSession precedent exactly: identity.Service's
// CreatePrincipal/SetRole/SetDisabled/SetPassword/IssueToken/RevokeToken
// have no AuditedWrite-closure form the way CreateSession/ClaimBootstrap
// do, so the Dispatch entry is written and must succeed BEFORE mutate
// runs — principal:write stays fail-closed on audit (decision 11 names it
// alongside config:write, never the blackout/stop/power-off exemption) —
// and a best-effort Outcome entry follows, recording what mutate actually
// did without gating the response on that second write.
//
// Returns (nil, false) when the Dispatch write itself failed: a 500
// problem has already been written and the caller must return
// immediately without calling mutate at all. Otherwise returns mutate's
// own error (nil on success) and dispatchOK=true; the caller maps a
// non-nil error to the right problem itself, since only it knows what
// mutate was trying to do.
func (h *handlers) auditPrincipalWrite(
	w http.ResponseWriter, r *http.Request, now time.Time,
	action, target string, params map[string]any,
	mutate func() error,
) (mutateErr error, dispatchOK bool) {
	ac := authFromContext(r.Context())
	commandID := uuid.NewString()

	if !h.writeAuditOrFail(r.Context(), w, now, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: action, Target: target, Params: params,
		Kind: identity.AuditDispatch, CommandID: commandID,
	}) {
		return nil, false
	}

	mutateErr = mutate()

	outcomeNow := h.now()
	outcome := identity.AuditEntry{
		Timestamp: outcomeNow, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: action, Target: target,
		Kind: identity.AuditOutcome, CommandID: commandID,
		// This coordinator-local effect resolves synchronously within this
		// same request — the outcome is always definitely known by the
		// time this entry is written, matching session.go's identical
		// reasoning for session.revoke.
		OutcomeState: string(observation.StateCurrent),
	}
	if mutateErr != nil {
		outcome.Outcome = "failed"
		outcome.OutcomeReason = mutateErr.Error()
	} else {
		outcome.Outcome = "succeeded"
	}
	if err := h.deps.Identity.WriteAudit(r.Context(), outcome); err != nil && h.logger != nil {
		h.logger.Warn("api: failed to write outcome audit entry", "action", action, "error", err, "command_id", commandID)
	}
	return mutateErr, true
}

// isEnabledAdmin reports whether p currently holds principal:write and is
// enabled — the population [handlers.wouldLockOutAdministration] counts.
func isEnabledAdmin(p identity.Principal) bool {
	return !p.Disabled && p.Role.Has(identity.ScopePrincipalWrite)
}

// principalHasReachableCredential reports whether p can still authenticate
// by some means as of now: a set password, or an active, unexpired token
// other than excludeTokenID. Deliberately does NOT count an open session:
// a session cannot be re-minted the way a password or a token can once it
// is gone, so counting one here would let this guard pass while leaving
// the operator one browser-restart away from being unable to sign back
// in — an overly conservative check (it may refuse a token revoke a live
// session would in fact have survived) is the safe direction per
// requirement 3: the refusal costs a retry, the alternative costs an
// unrecoverable lockout.
func (h *handlers) principalHasReachableCredential(ctx context.Context, p identity.Principal, excludeTokenID string, now time.Time) (bool, error) {
	if p.HasPassword {
		return true, nil
	}
	tokens, err := h.deps.Identity.ListTokens(ctx, p.ID)
	if err != nil {
		return false, err
	}
	for _, t := range tokens {
		if t.ID == excludeTokenID {
			continue
		}
		if t.ExpiresAt != nil && !now.Before(*t.ExpiresAt) {
			continue
		}
		return true, nil
	}
	return false, nil
}

// wouldLockOutAdministration reports whether, after excluding
// excludePrincipalID entirely (a disable or a role change away from
// principal:write) and excluding excludeTokenID from every remaining
// principal's own credential set (a token revoke), NO enabled principal
// holding principal:write would still have a way to authenticate.
// Requirement 3 / ADR-039 decision 8: the caller refuses the write rather
// than let it produce that state.
//
// Exactly one of excludePrincipalID/excludeTokenID is normally non-empty:
// disable and role-change pass a principal id and no token id; a token
// revoke passes no principal id and the token's own id, which is safe to
// exclude from every principal's check unconditionally since token ids are
// globally unique and only ever match their own owner's list.
func (h *handlers) wouldLockOutAdministration(ctx context.Context, now time.Time, excludePrincipalID, excludeTokenID string) (bool, error) {
	principals, err := h.deps.Identity.ListPrincipals(ctx)
	if err != nil {
		return false, err
	}
	for _, p := range principals {
		if p.ID == excludePrincipalID || !isEnabledAdmin(p) {
			continue
		}
		ok, err := h.principalHasReachableCredential(ctx, p, excludeTokenID, now)
		if err != nil {
			return false, err
		}
		if ok {
			return false, nil
		}
	}
	return true, nil
}
