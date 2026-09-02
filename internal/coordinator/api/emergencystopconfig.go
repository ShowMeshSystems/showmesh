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

// GET/PUT/revisions for the "show.emergencystop" configuration kind:
// authoring the three trigger levels' own optional follow-up action
// lists. The four TRIGGER routes (emergencystop.go) are the "someone
// presses the button" surface; this file is "an admin decides what
// happens when they do." Handler shape copied from showmode.go, gated on
// config:write like the majority of this package's singleton kinds (not
// show.mode's own open-read exception, which rests on ADR-033 decision
// 3's persistent-visibility requirement, and nothing establishes that for
// this kind).

const maxEmergencyStopConfigRequestBodyBytes = 8192

func resolveEmergencyStopConfig(ctx context.Context, cs ConfigStore, actionResolver config.EmergencyStopActionResolver) (payload config.EmergencyStopPayload, obj store.ConfigObjectRecord, rev store.ConfigRevisionRecord, configured bool, err error) {
	obj, err = cs.GetConfigObject(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.EmergencyStopDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	case err != nil:
		return config.EmergencyStopPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.emergencystop config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.EmergencyStopDefaultPayload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	}

	rev, err = cs.GetConfigRevision(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.EmergencyStopPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: get show.emergencystop config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeEmergencyStopPayload(rev.PayloadJSON, actionResolver)
	if verr != nil {
		return config.EmergencyStopPayload{}, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, fmt.Errorf("api: decode show.emergencystop payload: %s", verr.Error())
	}
	return payload, obj, rev, true, nil
}

// handleGetEmergencyStopConfig serves GET /api/v1/config/show.emergencystop.
// "Nothing has ever been written" is never a 404 here: the payload has a
// well-defined default (every level empty), so this always answers 200.
func (h *handlers) handleGetEmergencyStopConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	payload, obj, rev, configured, err := resolveEmergencyStopConfig(ctx, h.deps.Config, h.emergencyStopActionResolver(ctx))
	if err != nil {
		h.writeInternalError(w, now, "resolve show.emergencystop", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.EmergencyStopConfigResponse{
			ServerTime: formatTime(now), Kind: config.ShowEmergencyStopConfigKind,
			Revision: 0, Payload: mapEmergencyStopPayload(payload),
			UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}
	jsonWrite(w, mapEmergencyStopConfigResponse(now, rev, obj, payload))
}

// handleGetEmergencyStopConfigRevisions serves
// GET /api/v1/config/show.emergencystop/revisions, on
// handleGetShowModeConfigRevisions's own identical single-read shape (see
// its doc comment for why deriving activeRevision from ListConfigRevisions
// alone, rather than a second GetConfigObject call, removes a torn-read
// race rather than merely narrowing it).
func (h *handlers) handleGetEmergencyStopConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	revs, err := h.deps.Config.ListConfigRevisions(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list show.emergencystop config revisions", err)
		return
	}
	activeRevision := int64(0)
	if n := len(revs); n > 0 {
		activeRevision = revs[n-1].Revision
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.ShowEmergencyStopConfigKind, Revisions: out})
}

// handlePutEmergencyStopConfig serves PUT /api/v1/config/show.emergencystop:
// validates a full replacement, appends an immutable revision, and
// activates it in the SAME transaction as its audit log entry (ADR-024
// decision 11).
func (h *handlers) handlePutEmergencyStopConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxEmergencyStopConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxEmergencyStopConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}

	payload, verr := config.DecodeEmergencyStopPayload(string(body), h.emergencyStopActionResolver(ctx))
	if verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Error()))
		return
	}
	payloadJSON, err := config.EncodeEmergencyStopPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.emergencystop config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID); gerr == nil {
			currentRevision = obj.CurrentRevision
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}
		if err := checkRevisionPrecondition(config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID, precondition, currentRevision); err != nil {
			return identity.AuditEntry{}, err
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ShowEmergencyStopConfigKind, ObjectID: config.ShowEmergencyStopConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.ShowEmergencyStopSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ShowEmergencyStopConfigKind, config.ShowEmergencyStopConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.ShowEmergencyStopConfigKind,
			Params: map[string]any{"revision": nextRevisionNo},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write show.emergencystop config revision", writeErr)
		return
	}

	jsonWrite(w, mapEmergencyStopConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowEmergencyStopConfigKind, ID: config.ShowEmergencyStopConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// emergencyStopActionResolver checks a referenced show.action id EXISTS,
// of any show. See [config.EmergencyStopActionResolver]'s own doc
// comment for why this kind is never scoped to one show's own namespace.
func (h *handlers) emergencyStopActionResolver(ctx context.Context) config.EmergencyStopActionResolver {
	return func(actionID string) bool {
		_, err := h.deps.Config.GetConfigObject(ctx, config.ShowActionConfigKind, actionID)
		return err == nil
	}
}

func mapEmergencyStopPayload(p config.EmergencyStopPayload) v1.ConfigEmergencyStopPayload {
	return v1.ConfigEmergencyStopPayload{
		Stop:          v1.ConfigEmergencyStopLevelPayload{Actions: nonNilStrings(p.Stop.Actions)},
		StopPowerDown: v1.ConfigEmergencyStopLevelPayload{Actions: nonNilStrings(p.StopPowerDown.Actions)},
		HardStop:      v1.ConfigEmergencyStopLevelPayload{Actions: nonNilStrings(p.HardStop.Actions)},
	}
}

// nonNilStrings turns a nil slice into an empty one so the wire always
// carries "actions":[] rather than "actions":null for a level nothing was
// ever configured for.
func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func mapEmergencyStopConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload config.EmergencyStopPayload) v1.EmergencyStopConfigResponse {
	return v1.EmergencyStopConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowEmergencyStopConfigKind, Revision: rev.Revision,
		Payload:                mapEmergencyStopPayload(payload),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
