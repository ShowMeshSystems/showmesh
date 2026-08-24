package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is ADR-033's HTTP surface: GET/PUT /api/v1/config/show.mode
// and its revisions list. Handler shape copied from rendersettings.go.
//
// THE READ GATE IS A DELIBERATE DEPARTURE from every other configuration
// singleton in this package, and it is the whole reason this file does not
// simply say "mirrors render.settings exactly".
//
// Every other singleton gates even its GET on config:write, because
// ADR-024 decision 4 defines no config:read scope and config.go's own
// argument is that a configuration read is nearer principal management
// than telemetry. That argument does not survive ADR-033 decision 3, which
// requires the mode to be persistently visible in the Operator UI and
// reported by showmeshctl. An indicator that only an admin can see is not
// persistent visibility: the operator standing at the console during a
// show holds show:macro:run and observation:read, not config:write, and
// for that operator a config:write-gated indicator renders as a 403 or as
// nothing at all - which is exactly the trap decision 3 exists to close.
//
// So the current-value GET is gated on observation:read, the narrowest
// existing scope that every signed-in role already holds (identity's own
// readScopes, held by viewer, operator and admin alike). No new scope is
// minted. The revisions list keeps the ordinary config:write gate: that is
// history carrying principal names, which is audit-adjacent and is not
// what decision 3 asks to be visible. The WRITE is unchanged from every
// other kind: config:write, through writeGuard, with an audited principal
// per ADR-024.

const maxShowModeConfigRequestBodyBytes = 1024

// resolveShowMode reads the show.mode configuration kind's current value:
// the stored mode, and whether a revision has ever actually been written
// ("configured"). The default when nothing has ever been written is
// [config.ShowModeDefaultPayload] ("program"), reported with
// configured=false.
//
// Nothing here ever produces "unknown". A coordinator that cannot read its
// own store returns an error, and the caller reports the failure; it does
// not manufacture a mode. "unknown" is the NODE's word for a mode it has
// never received (ADR-033 decision 5), and it lives in internal/agent.
//
// obj and rev are the exact store records the returned payload was decoded
// from. A caller building a response MUST use these instead of re-reading
// the store: a re-read can observe a revision a concurrent PUT activated
// after this function's own read, pairing the NEW revision number and
// metadata with the OLD decoded payload. obj and rev are zero when
// configured is false.
func resolveShowMode(ctx context.Context, cs ConfigStore) (payload config.ShowModePayload, obj store.ConfigObjectRecord, rev store.ConfigRevisionRecord, configured bool, err error) {
	obj, err = cs.GetConfigObject(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.ShowModeDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	case err != nil:
		return config.ShowModePayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.mode config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.ShowModeDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	}

	rev, err = cs.GetConfigRevision(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.ShowModePayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.mode config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeShowModePayload(rev.PayloadJSON)
	if verr != nil {
		// A stored row this package never wrote in this shape is a
		// store-integrity error, not a validation outcome to recover from.
		return config.ShowModePayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: decode show.mode payload: %s", verr.Error())
	}
	return payload, obj, rev, true, nil
}

// handleGetShowModeConfig serves GET /api/v1/config/show.mode. See this
// file's own header comment for why this read is gated on observation:read
// rather than config:write. "Nothing has ever been written" is never a 404
// here - the payload has a well-defined default - so this always answers
// 200, reporting whether the current value is the default or a stored
// choice via Source/Revision.
func (h *handlers) handleGetShowModeConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	payload, obj, rev, configured, err := resolveShowMode(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve show.mode", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.ShowModeConfigResponse{
			ServerTime: formatTime(now), Kind: config.ShowModeConfigKind,
			Revision: 0, Payload: v1.ConfigShowModePayload{Mode: payload.Mode},
			UpdatedAt: formatTime(now), Source: "default",
			ResolumeWebSocketEffect: showModeResolumeEffect(payload.Mode),
		})
		return
	}

	// obj and rev are resolveShowMode's own records; see its doc comment
	// for why this must not re-read the store.
	jsonWrite(w, mapShowModeConfigResponse(now, rev, obj, payload))
}

// handleGetShowModeConfigRevisions serves
// GET /api/v1/config/show.mode/revisions: every revision's metadata,
// newest first. Gated on config:write like every other kind's revision
// history - the open read is the CURRENT value, not the history.
func (h *handlers) handleGetShowModeConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get show.mode config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list show.mode config revisions", err)
		return
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.ShowModeConfigKind, Revisions: out})
}

// handlePutShowModeConfig serves PUT /api/v1/config/show.mode: validates a
// full replacement, appends an immutable revision, and activates it in the
// SAME transaction as its audit log entry (ADR-024 decision 11).
//
// This handler changes what the system DOES and never who may do it
// (ADR-033 decision 6): the mode written here is read by the Resolume
// footprint switch and pushed to nodes, and it gates no command path
// anywhere. ADR-033 decision 4 forbids any code that would let it.
func (h *handlers) handlePutShowModeConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	body, err := io.ReadAll(io.LimitReader(r.Body, maxShowModeConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxShowModeConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}

	payload, verr := config.DecodeShowModePayload(string(body))
	if verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Error()))
		return
	}

	payloadJSON, err := config.EncodeShowModePayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.mode config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ShowModeConfigKind, ObjectID: config.ShowModeConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.ShowModeSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ShowModeConfigKind, config.ShowModeConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.ShowModeConfigKind,
			Params: map[string]any{
				"revision": nextRevisionNo,
				"mode":     payload.Mode,
			},
			Kind: identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "write show.mode config revision", writeErr)
		return
	}

	jsonWrite(w, mapShowModeConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowModeConfigKind, ID: config.ShowModeConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// showModeResolumeEffect is [v1.ShowModeConfigResponse.
// ResolumeWebSocketEffect]'s text for one mode. It names the mode as the
// reason for the behaviour, per ADR-033 decision 3.
func showModeResolumeEffect(mode string) string {
	if mode == config.ShowModeShow {
		return "show mode: the Resolume WebSocket wake-up channel is held CLOSED, reducing this " +
			"coordinator's footprint on Arena while a show runs. Returning to program mode reopens it " +
			"without a coordinator restart."
	}
	return "program mode: the Resolume WebSocket wake-up channel is held OPEN for prompt " +
		"clip-change evidence while programming. Switching to show mode closes it without a " +
		"coordinator restart."
}

func mapShowModeConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload config.ShowModePayload) v1.ShowModeConfigResponse {
	return v1.ShowModeConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowModeConfigKind, Revision: rev.Revision,
		Payload:                 v1.ConfigShowModePayload{Mode: payload.Mode},
		UpdatedAt:               formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:    nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName:  nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                  rev.Source,
		ResolumeWebSocketEffect: showModeResolumeEffect(payload.Mode),
	}
}
