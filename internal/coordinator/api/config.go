package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 7 seam A: the configuration write surface (RES-008
// D1), and the seam that proves ADR-024 decision 11's same-transaction
// rule — a configuration write is a coordinator-local state change, which
// is the one shape that rule can be exercised against at all (a command
// dispatched to an agent uses decision 11's write-before-dispatch rule
// instead; see internal/coordinator/identity/audit.go's AuditedWrite doc
// comment).
//
// Reads on this surface require config:write and are NEVER open under
// [Options.CloseReads] — this is a deliberate decision, not an oversight,
// and it is recorded here because a reviewer should be able to see it was
// decided rather than defaulted. ADR-024 decision 4 defines exactly four
// read scopes (node:read, fpp:read, observation:read, event:read) and the
// four fixed role bundles are pinned to that list; adding a config:read
// scope now would change bundles the record already fixed, for a surface
// this step's spec explicitly does not ask for. GET /api/v1/audit already
// set the precedent this file follows: "a new, always-sensitive surface"
// uses [handlers.requireScope] regardless of the open-reads posture,
// rather than [handlers.readGuard]. Configuration is exactly that class —
// which FPP hosts the coordinator polls is not the kind of fact the
// "reads stay open so a credential problem never costs the operator
// visibility of the show" reasoning (ADR-024 decision 2) was written to
// protect; it is nearer principal management than it is to node/FPP
// telemetry, so it is gated identically to audit:read's own admin-only
// scope (config:write is admin-only — see identity/types.go's
// adminOnlyScopes).
//
// maxConfigRequestBodyBytes bounds PUT /api/v1/config/fpp.endpoints'
// request body, mirroring session.go's maxSessionRequestBodyBytes: large
// enough for a realistic endpoint list (a reference installation names
// three FPP hosts; even a large installation's list is a few hundred
// bytes per entry), small enough that a malicious or misbehaving caller
// cannot make this handler buffer an unbounded body before validation
// ever runs.
const maxConfigRequestBodyBytes = 64 * 1024

// scopeConfigWrite exists only so api.go's route registration can take its
// address: [handlers.writeGuard] takes *identity.Scope (nil means "any
// authenticated principal, no specific scope" — see that method's doc
// comment), and identity.ScopeConfigWrite is a typed string CONSTANT,
// which Go does not allow taking the address of directly.
var scopeConfigWrite = identity.ScopeConfigWrite

// handleGetFPPEndpointsConfig serves GET /api/v1/config/fpp.endpoints: the
// active revision and its decoded payload. 404 resourceNotFoundProblem
// when no revision has ever been activated — matching
// [handlers.handleFPPInstance]'s existing "the named singular resource
// does not exist" convention — rather than a 200 with an empty payload,
// because "not configured yet" and "configured with zero endpoints" are
// different facts a client needs to tell apart (the collector's own
// not-configured/zero-endpoints distinction, one layer up).
func (h *handlers) handleGetFPPEndpointsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no fpp.endpoints configuration has been created yet; PUT one to create it"))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get fpp.endpoints config object", err)
		return
	}
	if obj.CurrentRevision == 0 {
		// CreateConfigObject (store/config.go) can produce a row with
		// current_revision == 0, "declared, nothing active" — seam 0's own
		// doc comment names this as a state a caller can deliberately
		// produce. This seam's PUT handler never leaves an object in that
		// state (it always activates the revision it just created), but
		// this branch exists so a future caller of CreateConfigObject for
		// this kind cannot make this endpoint panic or fabricate a
		// revision that does not exist.
		writeProblem(w, h.logger, now, resourceNotFoundProblem(
			"no fpp.endpoints configuration has been created yet; PUT one to create it"))
		return
	}

	rev, err := h.deps.Config.GetConfigRevision(r.Context(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		// store/config.go's ActivateConfigRevision doc comment (F10): the
		// active pointer CAN name a revision this store does not hold if a
		// caller elsewhere ever violated the "activate only what you just
		// created" contract. Surfaced as an internal error, not a 404: the
		// resource (the config object) demonstrably exists, so "not found"
		// would misreport what is actually a store-integrity condition.
		h.writeInternalError(w, now, "get active fpp.endpoints config revision", err)
		return
	}

	endpoints, err := config.DecodeFPPEndpointsPayload(rev.PayloadJSON)
	if err != nil {
		h.writeInternalError(w, now, "decode fpp.endpoints config payload", err)
		return
	}

	jsonWrite(w, mapFPPEndpointsConfigResponse(now, rev, obj, endpoints))
}

// handleGetFPPEndpointsConfigRevisions serves
// GET /api/v1/config/fpp.endpoints/revisions: every revision's metadata,
// newest first. Unlike handleGetFPPEndpointsConfig, an object that has
// never been created is a 200 with an empty list — "no history yet" is not
// an absent resource the way "no active configuration" is, and this
// mirrors every other list endpoint in this package (GET /api/v1/nodes,
// GET /api/v1/events, ...), none of which 404s on an empty result.
func (h *handlers) handleGetFPPEndpointsConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		// No object at all yet: activeRevision stays 0, which never
		// matches any real revision number (config_revisions.revision
		// starts at 1 — store/config.go's CreateConfigRevision doc
		// comment), so mapConfigRevisionMeta's Active comparison below
		// correctly marks nothing active.
	case err != nil:
		h.writeInternalError(w, now, "get fpp.endpoints config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list fpp.endpoints config revisions", err)
		return
	}

	// store.Store.ListConfigRevisions returns oldest first (ORDER BY
	// revision); this endpoint's own contract (and this package's spec) is
	// "newest first", so this reverses rather than asking the store
	// package to grow a second ordering for one caller.
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}

	jsonWrite(w, v1.ConfigRevisionsResponse{
		ServerTime: formatTime(now),
		Kind:       config.FPPEndpointsConfigKind,
		Revisions:  out,
	})
}

// fppEndpointsConfigPutRequest is PUT /api/v1/config/fpp.endpoints'
// request body: a bare payload, no envelope — unlike a response, a write
// request carries no serverTime for the client to be missing (contract
// section 6.2's serverTime rule is a RESPONSE guarantee), so there is
// nothing this envelope would add over the payload alone.
type fppEndpointsConfigPutRequest struct {
	Endpoints []v1.ConfigFPPEndpoint `json:"endpoints"`
}

// handlePutFPPEndpointsConfig serves PUT /api/v1/config/fpp.endpoints:
// validates, appends an immutable revision, activates it, and returns the
// new active revision. Registered behind
// [handlers.writeGuard](&identity.ScopeConfigWrite, ...), so by the time
// this runs the request already carries an authenticated principal holding
// config:write and has passed decision 6's CSRF check.
//
// ADR-009 requires invalid configuration be REJECTED BEFORE ACTIVATION,
// and this function's ordering is what makes that literally true: decoding
// and [config.ValidateFPPEndpoints] both run and can both fail BEFORE
// [identity.Service.AuditedWrite] is ever called, so a rejected write never
// opens a transaction and never leaves a config_revisions row behind — the
// "rejected write leaves no revision behind" half of this seam's A1
// deliverable is a consequence of this function's control flow, not a
// separate check.
//
// The write is FAIL-CLOSED ON AUDIT (ADR-024 decision 11's rule for
// config:write, the opposite of seam C's blackout/stop/power-off
// exemption): [identity.Service.AuditedWrite] lands the new
// config_revisions row, the config_objects activation, and the audit_log
// entry in ONE transaction (via [store.Store.InTx]) or none of the three —
// see identity/audit.go's AuditedWrite doc comment and this seam's own
// report for the real SQLite-trigger test proving it.
func (h *handlers) handlePutFPPEndpointsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())

	var req fppEndpointsConfigPutRequest
	dec := json.NewDecoder(io.LimitReader(r.Body, maxConfigRequestBodyBytes+1))
	if err := dec.Decode(&req); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(
			`request body must be JSON matching {"endpoints":[{"id":string,"url":string},...]}`))
		return
	}

	endpoints := make([]config.FPPEndpoint, 0, len(req.Endpoints))
	for _, e := range req.Endpoints {
		endpoints = append(endpoints, config.FPPEndpoint{ID: e.ID, URL: e.URL})
	}

	// ADR-009: rejected before activation. Nothing below this point has
	// run yet — no revision has been created, and AuditedWrite has not
	// been called — so a validation failure here leaves the store
	// untouched.
	if err := config.ValidateFPPEndpoints(endpoints); err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	payloadJSON, err := config.EncodeFPPEndpointsPayload(endpoints)
	if err != nil {
		h.writeInternalError(w, now, "encode fpp.endpoints config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(r.Context(), func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		// Computed INSIDE the transaction, not before AuditedWrite was
		// called: the single-connection pool (store/tx.go's InTx doc
		// comment) serializes every concurrent InTx call, so reading the
		// current pointer here — rather than via a separate
		// h.deps.Config.GetConfigObject call before this closure runs — is
		// what makes "read current revision, then create the next one"
		// race-free against a second concurrent PUT, with no extra locking
		// this package would otherwise have to invent.
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind:                   config.FPPEndpointsConfigKind,
			ObjectID:               config.FPPEndpointsConfigObjectID,
			Revision:               nextRevisionNo,
			PayloadJSON:            payloadJSON,
			CreatedByPrincipalID:   ac.result.Principal.ID,
			CreatedByPrincipalName: ac.result.Principal.Name,
			Source:                 config.FPPEndpointsSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}

		if _, aerr := tx.ActivateConfigRevision(ctx, config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}

		activated = rec

		return identity.AuditEntry{
			Timestamp:     now,
			PrincipalID:   ac.result.Principal.ID,
			PrincipalName: ac.result.Principal.Name,
			Form:          ac.result.Form,
			CredentialID:  ac.result.CredentialID,
			ClientAddr:    h.clientAddr(r),
			Action:        "config.write",
			Target:        config.FPPEndpointsConfigKind,
			Params: map[string]any{
				"revision":      nextRevisionNo,
				"endpointCount": len(endpoints),
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		// ADR-024 decision 11's fail-closed rule for config:write: whether
		// writeErr wraps [identity.ErrAuditWrite] (the audit append itself
		// failed) or is a plain store error from CreateConfigRevision/
		// ActivateConfigRevision, [store.Store.InTx] has already rolled
		// back the WHOLE transaction — no config_revisions row and no
		// config_objects activation survive either way. This function does
		// not need to distinguish the two failure modes to answer the
		// client correctly: either way, the write did not happen, so both
		// map to the identical 500 [bootstrap.go]'s handleClaimBootstrap
		// already establishes as this codebase's convention for "an
		// AuditedWrite closure failed" (its `case err != nil:` branch, no
		// special case for ErrAuditWrite).
		h.writeInternalError(w, now, "write fpp.endpoints config revision", writeErr)
		return
	}

	jsonWrite(w, mapFPPEndpointsConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.FPPEndpointsConfigKind, ID: config.FPPEndpointsConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, endpoints))
}

// restartRequiredReason is A4's stated fact, carried on every
// FPPEndpointsConfigResponse rather than left for the operator to
// discover: "a configuration change here does not hot-reload the
// collector — RES-008 section 10 records restart-required as today's true
// and stable answer for every configuration change, not specific to this
// kind." It is a package-level constant, not computed per response,
// because it is unconditionally true for every response this endpoint
// produces today; a future config kind that DOES hot-reload would need its
// own reason, not a change to this one.
const restartRequiredReason = "this coordinator does not hot-reload configuration; the FPP collector polls the endpoint list " +
	"it read at startup, so this change takes effect the next time the coordinator restarts, not immediately"

// mapFPPEndpointsConfigResponse renders one fpp.endpoints revision plus its
// owning object's bookkeeping onto the wire. CreatedByPrincipalID/
// CreatedByPrincipalName ([nonEmptyStrPtr]) are nil exactly once in
// practice: the one revision the startup env->store migration creates has
// no principal at all (a startup migration has no principal —
// internal/coordinator's configsync.go), and this renders that honestly
// rather than fabricating one.
func mapFPPEndpointsConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, endpoints []config.FPPEndpoint) v1.FPPEndpointsConfigResponse {
	payload := v1.ConfigFPPEndpointsPayload{Endpoints: make([]v1.ConfigFPPEndpoint, 0, len(endpoints))}
	for _, e := range endpoints {
		payload.Endpoints = append(payload.Endpoints, v1.ConfigFPPEndpoint{ID: e.ID, URL: e.URL})
	}
	return v1.FPPEndpointsConfigResponse{
		ServerTime:             formatTime(now),
		Kind:                   config.FPPEndpointsConfigKind,
		Revision:               rev.Revision,
		Payload:                payload,
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
		RestartRequired:        true,
		RestartRequiredReason:  restartRequiredReason,
	}
}

// mapConfigRevisionMeta renders one config_revisions row's metadata,
// marking it Active when its revision number equals activeRevision.
func mapConfigRevisionMeta(rev store.ConfigRevisionRecord, activeRevision int64) v1.ConfigRevisionMeta {
	return v1.ConfigRevisionMeta{
		Revision:               rev.Revision,
		CreatedAt:              formatTime(rev.CreatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
		Note:                   rev.Note,
		Active:                 rev.Revision == activeRevision,
	}
}
