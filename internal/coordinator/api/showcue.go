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
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// HTTP surface for the "show.cue" config kind (TRACK-H-H1-SPEC.md section
// 2, internal/coordinator/config/showcue.go). Follows showobjects.go's
// show.surface handlers exactly: an operator-chosen collection, GET
// list/get/revisions, PUT writes gated on config:write. Reuses
// showconfig.go's shared helpers (getActiveShowConfigRevision,
// handleGetShowConfigRevisions, writeShowConfigRevision, mapValidationError)
// rather than reimplementing them.
//
// listConfigObjectSummaries (showconfig.go) is NOT reused here for the
// same reason showobjects.go's own doc comment gives for "show" and
// "show.surface": show.cue's stored payload has "name", not "label".

// cueLookup returns config.DecodeShowPlaylistPayload's resolveCue
// callback — reporting whether id names a "show.cue" object with an
// active revision and, when it does, that Cue's own "show" — together
// with a pointer to the first store error the callback hits, if any.
// resolveCue's own return shape (string, bool) has no room for an error,
// so a GetConfigObject/GetConfigRevision failure, or a stored payload
// jsonUnmarshalStrict cannot decode, all collapse to (_, false) exactly
// like an honestly nonexistent cue would. That is wrong for
// DecodeShowPlaylistPayload's resulting ValidationCodeFieldUnknownReference:
// it reads to the operator as "your cue reference is wrong" when the real
// cause is a store failure that deserves a 500. The caller consults
// *storeErr after decoding to tell the two apart without changing
// DecodeShowPlaylistPayload's callback signature. store.ErrConfigObjectNotFound
// is not recorded: that is the honest "no such object" case, not a store
// failure, and must stay a 400.
func (h *handlers) cueLookup(ctx context.Context) (resolveCue func(cueID string) (show string, ok bool), storeErr *error) {
	var firstErr error
	resolveCue = func(cueID string) (string, bool) {
		obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
		if err != nil {
			if !errors.Is(err, store.ErrConfigObjectNotFound) && firstErr == nil {
				firstErr = err
			}
			return "", false
		}
		if obj.CurrentRevision == 0 {
			return "", false
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowCueConfigKind, cueID, obj.CurrentRevision)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return "", false
		}
		var head struct {
			Show string `json:"show"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			return "", false
		}
		return head.Show, true
	}
	return resolveCue, &firstErr
}

// listShowCueSummaries lists every "show.cue" config object with an
// active revision, optionally narrowed to those whose "show" field
// equals showFilter (empty means no filter).
func (h *handlers) listShowCueSummaries(ctx context.Context, showFilter string) ([]v1.ConfigObjectSummary, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.ShowCueConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list show.cue config objects: %w", err)
	}
	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowCueConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active show.cue config revision for %q: %w", obj.ID, err)
		}
		var head struct {
			Show string `json:"show"`
			Name string `json:"name"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode show.cue config payload head for %q: %w", obj.ID, err)
		}
		if showFilter != "" && head.Show != showFilter {
			continue
		}
		out = append(out, v1.ConfigObjectSummary{
			ID: obj.ID, Label: head.Name, Show: head.Show,
			CurrentRevision: obj.CurrentRevision, UpdatedAt: formatTime(obj.UpdatedAt),
		})
	}
	return out, nil
}

func (h *handlers) handleListShowCues(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if r.URL.Query().Has("node") {
		writeProblem(w, h.logger, now, unsupportedNodeFilterProblem(config.ShowCueConfigKind))
		return
	}
	objs, err := h.listShowCueSummaries(r.Context(), r.URL.Query().Get("show"))
	if err != nil {
		h.writeInternalError(w, now, "list show.cue config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.ShowCueConfigKind, Objects: objs})
}

func (h *handlers) handleGetShowCue(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.ShowCueConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active show.cue config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.ShowCuePayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode show.cue config payload", err)
		return
	}
	jsonWrite(w, mapShowCueConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutShowCue(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("cue id", id); verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}
	precondition, precondProblem := parseRevisionPrecondition(r)
	if precondProblem != nil {
		writeProblem(w, h.logger, now, *precondProblem)
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read show.cue request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeShowCuePayload(string(raw), h.showExists(r.Context()), h.audioNodeExists(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	if problem, err := h.refuseShowChange(r.Context(), config.ShowCueConfigKind, id, payload.Show); err != nil {
		h.writeInternalError(w, now, "check stored show.cue show before write", err)
		return
	} else if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	payloadJSON, err := config.EncodeShowCuePayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.cue config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowCueConfigKind, id, payloadJSON, precondition,
		map[string]any{"show": payload.Show, "name": payload.Name})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write show.cue config revision", writeErr)
		return
	}

	jsonWrite(w, mapShowCueConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowCueConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handleGetShowCueRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.ShowCueConfigKind)
}

// handleDeleteShowCue serves DELETE /api/v1/config/show.cue/{id}: a
// tombstone. A show.playlist entry naming this cue afterward is not
// refused here and is not cascaded: show.playlist resolves its own
// entries' cue references through GetConfigObject at the point it is
// actually used, so the gap surfaces there. This codebase has no
// pre-flight readiness check for show.playlist entries the way ADR-029
// gives show.action's own targets and night.session's action bindings; a
// dangling reference here fails at dispatch time, not before, same as any
// other reference this package resolves.
func (h *handlers) handleDeleteShowCue(w http.ResponseWriter, r *http.Request) {
	h.handleDeleteShowConfigObject(w, r, config.ShowCueConfigKind, nil)
}

func mapConfigShowCueOutputs(o config.ShowCueOutputs) v1.ConfigShowCueOutputs {
	out := v1.ConfigShowCueOutputs{}
	if o.Render != nil {
		out.Render = &v1.ConfigShowCueRenderOutput{Sequence: o.Render.Sequence}
	}
	if o.Audio != nil {
		out.Audio = &v1.ConfigShowCueAudioOutput{Asset: o.Audio.Asset, StartOffsetMillis: o.Audio.StartOffsetMillis, Target: o.Audio.Target}
	}
	if o.LTC != nil {
		out.LTC = &v1.ConfigShowCueLTCOutput{StartOffsetMillis: o.LTC.StartOffsetMillis, Target: o.LTC.Target}
	}
	if o.Announcement != nil {
		out.Announcement = &v1.ConfigShowCueAnnouncementOutput{
			Policy: o.Announcement.Policy, DuckGainDb: o.Announcement.DuckGainDb, FadeMillis: o.Announcement.FadeMillis,
			Target: o.Announcement.Target,
		}
	}
	return out
}

func mapShowCueConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.ShowCuePayload) v1.ShowCueConfigResponse {
	return v1.ShowCueConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowCueConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload: v1.ConfigShowCue{
			Show: p.Show, Name: p.Name, Outputs: mapConfigShowCueOutputs(p.Outputs),
		},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
