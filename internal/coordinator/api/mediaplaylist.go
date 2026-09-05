package api

import (
	"errors"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// HTTP surface for the "media.playlist" config kind
// (internal/coordinator/config/mediaplaylist.go). Follows show.playlist
// (showplaylist.go) exactly: same route shape, same scopes, same
// immutable-show-on-PUT rule (refuseShowChange). Unlike show.playlist,
// media.playlist's stored payload begins with the same {"show":...,
// "label":...} shape show.action/show.macro/night.session already do, so
// listConfigObjectSummaries (showconfig.go) is reused verbatim for the
// list route rather than a kind-specific summary decoder.

func (h *handlers) handleListMediaPlaylists(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if r.URL.Query().Has("node") {
		writeProblem(w, h.logger, now, unsupportedNodeFilterProblem(config.MediaPlaylistConfigKind))
		return
	}
	objs, err := listConfigObjectSummaries(r.Context(), h.deps.Config, config.MediaPlaylistConfigKind, r.URL.Query().Get("show"))
	if err != nil {
		h.writeInternalError(w, now, "list media.playlist config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.MediaPlaylistConfigKind, Objects: objs})
}

func (h *handlers) handleGetMediaPlaylist(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.MediaPlaylistConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active media.playlist config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.MediaPlaylistPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode media.playlist config payload", err)
		return
	}
	jsonWrite(w, mapMediaPlaylistConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutMediaPlaylist(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("media playlist id", id); verr != nil {
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
		h.writeInternalError(w, now, "read media.playlist request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeMediaPlaylistPayload(string(raw), h.showExists(r.Context()), h.nightSessionAssetCurrent(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	if problem, err := h.refuseShowChange(r.Context(), config.MediaPlaylistConfigKind, id, payload.Show); err != nil {
		h.writeInternalError(w, now, "check stored media.playlist show before write", err)
		return
	} else if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	payloadJSON, err := config.EncodeMediaPlaylistPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode media.playlist config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.MediaPlaylistConfigKind, id, payloadJSON, precondition,
		map[string]any{"show": payload.Show, "label": payload.Label, "itemCount": len(payload.Items)})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write media.playlist config revision", writeErr)
		return
	}

	jsonWrite(w, mapMediaPlaylistConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.MediaPlaylistConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handleGetMediaPlaylistRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.MediaPlaylistConfigKind)
}

// handleDeleteMediaPlaylist serves DELETE /api/v1/config/media.playlist/{id}:
// a tombstone. Nothing in this codebase's reference graph names a
// media.playlist id from another configuration object (night.session's own
// inline resting.backgroundAudio block is a separate, independent bed), so
// there is no dangling reference to consider on this side.
func (h *handlers) handleDeleteMediaPlaylist(w http.ResponseWriter, r *http.Request) {
	h.handleDeleteShowConfigObject(w, r, config.MediaPlaylistConfigKind, nil)
}

func mapConfigMediaPlaylistItems(items []config.MediaPlaylistItem) []v1.ConfigMediaPlaylistItem {
	out := make([]v1.ConfigMediaPlaylistItem, 0, len(items))
	for _, i := range items {
		out = append(out, v1.ConfigMediaPlaylistItem{
			Kind: i.Kind, Show: i.Asset.Show, Sequence: i.Asset.Sequence, Target: i.Asset.Target,
		})
	}
	return out
}

func mapConfigMediaPlaylist(p config.MediaPlaylistPayload) v1.ConfigMediaPlaylist {
	return v1.ConfigMediaPlaylist{
		Label: p.Label, Show: p.Show, Items: mapConfigMediaPlaylistItems(p.Items),
		Repeat: p.Repeat, Resume: p.Resume, ItemTransition: p.ItemTransition, CrossfadeMs: p.CrossfadeMs,
		MaxGainDb: p.MaxGainDb, FadeOutMs: p.FadeOutMs, FadeInMs: p.FadeInMs,
	}
}

func mapMediaPlaylistConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.MediaPlaylistPayload) v1.MediaPlaylistConfigResponse {
	return v1.MediaPlaylistConfigResponse{
		ServerTime: formatTime(now), Kind: config.MediaPlaylistConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                mapConfigMediaPlaylist(p),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
