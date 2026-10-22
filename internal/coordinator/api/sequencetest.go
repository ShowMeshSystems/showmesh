package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// show.sequencetest (ADR-067): GET and PUT of one show's sequence test list.

const (
	sequenceTestNotActiveDetail = "These test sequences only run while this show is the active show."
	sequenceTestNightOpenDetail = "Test sequences are off while a night session is open. They come back when the night stops."
)

func (h *handlers) resolveSequenceTest(ctx context.Context, showID string) (payload config.SequenceTestPayload, obj store.ConfigObjectRecord, rev store.ConfigRevisionRecord, configured bool, err error) {
	payload = config.SequenceTestPayload{Show: showID, Entries: []config.SequenceTestEntry{}}
	obj, err = h.deps.Config.GetConfigObject(ctx, config.ShowSequenceTestConfigKind, showID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return payload, store.ConfigObjectRecord{}, store.ConfigRevisionRecord{}, false, nil
	case err != nil:
		return payload, obj, rev, false, fmt.Errorf("api: get show.sequencetest %q: %w", showID, err)
	case obj.CurrentRevision == 0:
		return payload, obj, rev, false, nil
	}
	rev, err = h.deps.Config.GetConfigRevision(ctx, config.ShowSequenceTestConfigKind, showID, obj.CurrentRevision)
	if err != nil {
		return payload, obj, rev, false, fmt.Errorf("api: get show.sequencetest %q revision %d: %w", showID, obj.CurrentRevision, err)
	}
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		return payload, obj, rev, false, fmt.Errorf("api: decode show.sequencetest %q: %w", showID, err)
	}
	if payload.Entries == nil {
		payload.Entries = []config.SequenceTestEntry{}
	}
	return payload, obj, rev, true, nil
}

// sequenceTestRunning reports whether showID's mappings run now and, when
// they do not, the operator-facing reason.
func (h *handlers) sequenceTestRunning(ctx context.Context, showID string) (bool, string, error) {
	if h.deps.AssetManifests == nil {
		return false, sequenceTestNotActiveDetail, nil
	}
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		return false, "", err
	}
	if !active.Configured || active.ShowID != showID {
		return false, sequenceTestNotActiveDetail, nil
	}
	open, err := assetsync.NightSessionOpen(ctx, h.deps.AssetManifests)
	if err != nil {
		return false, "", err
	}
	if open {
		return false, sequenceTestNightOpenDetail, nil
	}
	return true, "", nil
}

func (h *handlers) writeSequenceTestResponse(w http.ResponseWriter, ctx context.Context, now time.Time, showID string, payload config.SequenceTestPayload, obj store.ConfigObjectRecord, rev store.ConfigRevisionRecord, configured bool) {
	running, reason, err := h.sequenceTestRunning(ctx, showID)
	if err != nil {
		h.writeInternalError(w, now, "resolve sequence testing state", err)
		return
	}
	entries := make([]v1.ConfigSequenceTestEntry, 0, len(payload.Entries))
	for _, e := range payload.Entries {
		entries = append(entries, v1.ConfigSequenceTestEntry{SequenceFilename: e.SequenceFilename, Cue: e.Cue})
	}
	resp := v1.SequenceTestConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowSequenceTestConfigKind, ID: showID,
		Payload: v1.ConfigSequenceTestPayload{Show: showID, Entries: entries},
		Running: running, NotRunningReason: reason,
		UpdatedAt: formatTime(now), Source: "default",
	}
	if configured {
		resp.Revision = rev.Revision
		resp.UpdatedAt = formatTime(obj.UpdatedAt)
		resp.CreatedByPrincipalID = nonEmptyStrPtr(rev.CreatedByPrincipalID)
		resp.CreatedByPrincipalName = nonEmptyStrPtr(rev.CreatedByPrincipalName)
		resp.Source = rev.Source
	}
	jsonWrite(w, resp)
}

// handleGetSequenceTest serves GET /api/v1/config/show.sequencetest/{id}.
func (h *handlers) handleGetSequenceTest(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("show id", id); verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Detail))
		return
	}
	payload, obj, rev, configured, err := h.resolveSequenceTest(r.Context(), id)
	if err != nil {
		h.writeInternalError(w, now, "resolve show.sequencetest", err)
		return
	}
	h.writeSequenceTestResponse(w, r.Context(), now, id, payload, obj, rev, configured)
}

// handlePutSequenceTest serves PUT /api/v1/config/show.sequencetest/{id},
// a full replacement written in the same transaction as its audit entry.
func (h *handlers) handlePutSequenceTest(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("show id", id); verr != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Detail))
		return
	}
	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}
	var claims map[string][]string
	if h.deps.AssetManifests != nil {
		claims, err = assetsync.PlaylistSequenceFilenameClaims(ctx, h.deps.AssetManifests, id)
		if err != nil {
			h.writeInternalError(w, now, "resolve playlist sequence filenames", err)
			return
		}
	}
	resolveCue, cueLookupErr := h.cueLookup(ctx)
	payload, verr := config.DecodeSequenceTestPayload(string(body), id, h.showExists(ctx), resolveCue, claims)
	if verr != nil {
		if *cueLookupErr != nil {
			h.writeInternalError(w, now, "resolve show.cue reference", *cueLookupErr)
			return
		}
		writeProblem(w, h.logger, now, invalidParameterProblem(verr.Detail))
		return
	}
	payloadJSON, err := config.EncodeSequenceTestPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.sequencetest payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		currentRevision := int64(0)
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.ShowSequenceTestConfigKind, id); gerr == nil {
			currentRevision = obj.CurrentRevision
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}
		if err := checkRevisionPrecondition(config.ShowSequenceTestConfigKind, id, precondition, currentRevision); err != nil {
			return identity.AuditEntry{}, err
		}
		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ShowSequenceTestConfigKind, ObjectID: id,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.ShowSequenceTestSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ShowSequenceTestConfigKind, id, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.ShowSequenceTestConfigKind + "/" + id,
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
		h.writeInternalError(w, now, "write show.sequencetest revision", writeErr)
		return
	}
	h.writeSequenceTestResponse(w, ctx, now, id, payload, store.ConfigObjectRecord{
		Kind: config.ShowSequenceTestConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, activated, true)
}
