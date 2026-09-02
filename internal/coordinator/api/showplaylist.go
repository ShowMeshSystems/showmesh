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
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// HTTP surface for the "show.playlist" config kind (TRACK-H-H1-SPEC.md
// section 3, internal/coordinator/config/showplaylist.go). Follows
// showcue.go exactly. config.DecodeShowPlaylistPayload's resolveCue
// callback is h.cueLookup (showcue.go) — the store lookup lives here,
// never in the config package, which must never import api or store
// (importgraph_test.go enforces it).

// listShowPlaylistSummaries lists every "show.playlist" config object
// with an active revision, optionally narrowed to those whose "show"
// field equals showFilter (empty means no filter).
func (h *handlers) listShowPlaylistSummaries(ctx context.Context, showFilter string) ([]v1.ConfigObjectSummary, error) {
	objs, err := h.deps.Config.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, fmt.Errorf("list show.playlist config objects: %w", err)
	}
	out := make([]v1.ConfigObjectSummary, 0, len(objs))
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return nil, fmt.Errorf("get active show.playlist config revision for %q: %w", obj.ID, err)
		}
		var head struct {
			Show string `json:"show"`
			Name string `json:"name"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return nil, fmt.Errorf("decode show.playlist config payload head for %q: %w", obj.ID, err)
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

func (h *handlers) handleListShowPlaylists(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if r.URL.Query().Has("node") {
		writeProblem(w, h.logger, now, unsupportedNodeFilterProblem(config.ShowPlaylistConfigKind))
		return
	}
	objs, err := h.listShowPlaylistSummaries(r.Context(), r.URL.Query().Get("show"))
	if err != nil {
		h.writeInternalError(w, now, "list show.playlist config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.ShowPlaylistConfigKind, Objects: objs})
}

func (h *handlers) handleGetShowPlaylist(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.ShowPlaylistConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active show.playlist config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.ShowPlaylistPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode show.playlist config payload", err)
		return
	}
	jsonWrite(w, mapShowPlaylistConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutShowPlaylist(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("playlist id", id); verr != nil {
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
		h.writeInternalError(w, now, "read show.playlist request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	resolveCue, cueLookupErr := h.cueLookup(r.Context())
	payload, verr := config.DecodeShowPlaylistPayload(string(raw), h.showExists(r.Context()), resolveCue)
	if verr != nil {
		// A store failure inside resolveCue collapses to "cue not found"
		// (cueLookup's own doc comment): that is a 400 telling the operator
		// their reference is wrong when the real cause is a store failure.
		// Distinguish it here, after decoding, rather than changing
		// resolveCue's (string, bool) signature.
		if *cueLookupErr != nil {
			h.writeInternalError(w, now, "resolve cue reference for show.playlist validation", *cueLookupErr)
			return
		}
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	if problem, err := h.refuseShowChange(r.Context(), config.ShowPlaylistConfigKind, id, payload.Show); err != nil {
		h.writeInternalError(w, now, "check stored show.playlist show before write", err)
		return
	} else if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}

	// TRACK-H-cues-and-playlists.md section H5 build item 8's own ruling: an
	// operator-invisible alphabetical pick between two authored
	// showmesh-audio playlists is not acceptable for what plays out of a
	// speaker (showmeshaudiodispatch.go used to silently apply only
	// objs[0] and log a warning). Refuse the write that would create a
	// SECOND showmesh-audio playlist for the same Show instead — updating
	// the one that already exists is unaffected, since it is excluded by
	// its own id.
	if payload.Runner == config.ShowPlaylistRunnerShowmeshAudio && h.deps.AssetManifests != nil {
		existing, _, err := assetsync.ListShowmeshAudioPlaylists(r.Context(), h.deps.AssetManifests, payload.Show)
		if err != nil {
			h.writeInternalError(w, now, "list existing showmesh-audio playlists for this show", err)
			return
		}
		for _, obj := range existing {
			if obj.ID != id {
				writeProblem(w, h.logger, now, showmeshAudioPlaylistConflictProblem(obj.ID, id))
				return
			}
		}
	}

	payloadJSON, err := config.EncodeShowPlaylistPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode show.playlist config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.ShowPlaylistConfigKind, id, payloadJSON, precondition,
		map[string]any{"show": payload.Show, "name": payload.Name, "runner": payload.Runner, "entryCount": len(payload.Entries)})
	if writeErr != nil {
		var conflict *errConfigRevisionPreconditionFailed
		if errors.As(writeErr, &conflict) {
			writeProblem(w, h.logger, now, configRevisionConflictProblem(conflict))
			return
		}
		h.writeInternalError(w, now, "write show.playlist config revision", writeErr)
		return
	}

	// TRACK-H-cues-and-playlists.md section H5 build item 8's trigger-hole
	// fix: a cue-catalog deploy is a SOUND authorization gate, but it is
	// not the only thing that must re-apply this Playlist. Changing
	// showmeshAudio.repeat or reordering entries over the same Cue set
	// changes THIS playlist's own revision without changing any Cue's own
	// outputs, so [cuecatalog.ComputeRevision] (derived from Cue entries,
	// never playlist structure) does not change and no catalog deploy is
	// ever triggered by this write. Re-apply here too, best-effort, for
	// every node this Show's showmesh-audio Playlist could run on — the
	// identical background-only best-effort posture
	// applyShowmeshAudioPlaylistIfAny's own doc comment already states for
	// its catalog-deploy trigger.
	if payload.Runner == config.ShowPlaylistRunnerShowmeshAudio {
		h.reapplyShowmeshAudioPlaylistIfActive(r.Context(), h.now(), payload.Show)
	}

	jsonWrite(w, mapShowPlaylistConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ShowPlaylistConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// reapplyShowmeshAudioPlaylistIfActive is [handlers.handlePutShowPlaylist]'s
// own trigger for a showmesh-audio playlist edit that does not change the
// active Show's Cue catalog revision (see that call site's own doc
// comment). Best-effort and silent-on-error like every other autonomous
// follow-on dispatch in this package (applyShowmeshAudioPlaylistIfAny's own
// doc comment): the write itself already succeeded regardless of whether
// this re-apply does.
func (h *handlers) reapplyShowmeshAudioPlaylistIfActive(ctx context.Context, now time.Time, showID string) {
	if h.deps.AssetManifests == nil || h.deps.Nodes == nil {
		return
	}
	active, err := assetsync.ResolveActiveShow(ctx, h.deps.AssetManifests)
	if err != nil {
		h.logWarn("showmesh-audio: resolve active show for playlist re-apply failed", "show", showID, "error", err)
		return
	}
	if !active.Configured || active.ShowID != showID {
		// Not the active Show: nothing authorized to re-apply to (H0.7).
		return
	}
	views, err := h.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		h.logWarn("showmesh-audio: list nodes for playlist re-apply failed", "show", showID, "error", err)
		return
	}
	for _, v := range views {
		h.applyShowmeshAudioPlaylistIfAny(ctx, now, v.NodeID, active)
	}
}

func (h *handlers) handleGetShowPlaylistRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.ShowPlaylistConfigKind)
}

func mapConfigShowPlaylistEntries(entries []config.ShowPlaylistEntry) []v1.ConfigShowPlaylistEntry {
	out := make([]v1.ConfigShowPlaylistEntry, 0, len(entries))
	for _, e := range entries {
		entry := v1.ConfigShowPlaylistEntry{ID: e.ID, Cue: e.Cue}
		if e.FPP != nil {
			entry.FPP = &v1.ConfigShowPlaylistEntryFPP{
				Section: e.FPP.Section, Position: e.FPP.Position,
				ExpectedSequenceFilename: e.FPP.ExpectedSequenceFilename,
				ExpectedMediaFilename:    e.FPP.ExpectedMediaFilename,
			}
		}
		out = append(out, entry)
	}
	return out
}

func mapConfigShowPlaylist(p config.ShowPlaylistPayload) v1.ConfigShowPlaylist {
	out := v1.ConfigShowPlaylist{
		Show: p.Show, Name: p.Name, Runner: p.Runner,
		MismatchPolicy: p.MismatchPolicy, SafeCueRef: p.SafeCueRef,
		Entries: mapConfigShowPlaylistEntries(p.Entries),
	}
	if p.FPP != nil {
		out.FPP = &v1.ConfigShowPlaylistFPPBinding{
			InstanceUUID: p.FPP.InstanceUUID, PlaylistName: p.FPP.PlaylistName, PlaylistHash: p.FPP.PlaylistHash,
		}
	}
	if p.ShowmeshAudio != nil {
		out.ShowmeshAudio = &v1.ConfigShowPlaylistShowmeshAudio{Repeat: p.ShowmeshAudio.Repeat}
	}
	return out
}

func mapShowPlaylistConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.ShowPlaylistPayload) v1.ShowPlaylistConfigResponse {
	return v1.ShowPlaylistConfigResponse{
		ServerTime: formatTime(now), Kind: config.ShowPlaylistConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                mapConfigShowPlaylist(p),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
