package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// HTTP surface for the night.session and night.session.active config
// kinds (Track F seam F1, internal/coordinator/config's
// nightsession.go/nightsessionactive.go). Follows show.action/show.macro
// and show/show.active's own precedent exactly (showconfig.go's and
// showobjects.go's own top doc comments): reads use
// readAnyGuard(showConfigReadScopes, ...), writes use
// writeGuard(&scopeConfigWrite, ...), and every write reuses
// writeShowConfigRevision (showconfig.go) for its audited,
// single-transaction revision write.
//
// night.session's stored payload begins with the same {"show":...,
// "label":...} shape show.action/show.macro do, so listConfigObjectSummaries
// (showconfig.go) is reused verbatim for the list route rather than a
// third hand-written summary decoder.

// nightSessionAssetCurrent is [config.AssetCurrent] for this coordinator's
// asset store: a current (non-superseded) asset for (show, sequence,
// target) with an implied TargetKind of "node" (store.AssetTargetKindNode)
// — every night.session asset reference is per rendering/playout target,
// never per show. A store error is treated as "no such asset", the same
// posture noAssetStore's own ListAssets stub already establishes for "no
// asset store wired in".
func (h *handlers) nightSessionAssetCurrent(ctx context.Context) config.AssetCurrent {
	return func(show, sequence, target string) bool {
		recs, err := h.deps.Assets.ListAssets(ctx, store.AssetFilter{ShowID: show, SequenceID: sequence, NodeID: target})
		if err != nil {
			return false
		}
		for _, r := range recs {
			if r.SupersededAt == nil {
				return true
			}
		}
		return false
	}
}

// nightSessionActionResolver is [config.ActionResolver]: unlike
// h.showActionLookup (showconfig.go), which decodes an action's
// target.integration for show.macro's localFallback rule, this decodes
// the action's own "show" field, because ADR-027's namespace rule needs
// it compared against the session's own show. A separate function rather
// than widening showActionLookup's return, so show.macro's own callers
// are untouched by a change this seam needed — found in review: neither
// this function's predecessor nor its caller ever compared shows, so a
// christmas-2026 action bound into a halloween-2026 session was silently
// accepted.
func (h *handlers) nightSessionActionResolver(ctx context.Context) config.ActionResolver {
	return func(id string) (string, bool) {
		obj, err := h.deps.Config.GetConfigObject(ctx, config.ShowActionConfigKind, id)
		if err != nil || obj.CurrentRevision == 0 {
			return "", false
		}
		rev, err := h.deps.Config.GetConfigRevision(ctx, config.ShowActionConfigKind, id, obj.CurrentRevision)
		if err != nil {
			return "", false
		}
		var head struct {
			Show string `json:"show"`
		}
		if err := jsonUnmarshalStrict(rev.PayloadJSON, &head); err != nil {
			return "", false
		}
		return head.Show, true
	}
}

// nightSessionExists is [config.NightSessionExists] — the
// night.session.active singleton's own reference check, mirroring
// h.showExists (showobjects.go) one kind over.
func (h *handlers) nightSessionExists(ctx context.Context) config.NightSessionExists {
	return func(id string) bool {
		obj, err := h.deps.Config.GetConfigObject(ctx, config.NightSessionConfigKind, id)
		if err != nil {
			return false
		}
		return obj.CurrentRevision > 0
	}
}

// --- kind "night.session" ---

func (h *handlers) handleListNightSessions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	if r.URL.Query().Has("node") {
		writeProblem(w, h.logger, now, unsupportedNodeFilterProblem(config.NightSessionConfigKind))
		return
	}
	objs, err := listConfigObjectSummaries(r.Context(), h.deps.Config, config.NightSessionConfigKind, r.URL.Query().Get("show"))
	if err != nil {
		h.writeInternalError(w, now, "list night.session config objects", err)
		return
	}
	jsonWrite(w, v1.ConfigObjectsListResponse{ServerTime: formatTime(now), Kind: config.NightSessionConfigKind, Objects: objs})
}

func (h *handlers) handleGetNightSession(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	id := r.PathValue("id")
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.NightSessionConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active night.session config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.NightSessionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode night.session config payload", err)
		return
	}
	jsonWrite(w, mapNightSessionConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutNightSession(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := r.PathValue("id")
	if verr := config.ValidateShowObjectID("session id", id); verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read night.session request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	endpoints, err := currentFPPEndpoints(r.Context(), h.deps.FPP)
	if err != nil {
		h.writeInternalError(w, now, "list fpp instances for night.session validation", err)
		return
	}

	payload, verr := config.DecodeNightSessionPayload(string(raw), endpoints,
		h.nightSessionAssetCurrent(r.Context()), h.nightSessionActionResolver(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeNightSessionPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode night.session config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.NightSessionConfigKind, id, payloadJSON,
		map[string]any{"show": payload.Show, "label": payload.Label})
	if writeErr != nil {
		h.writeInternalError(w, now, "write night.session config revision", writeErr)
		return
	}

	jsonWrite(w, mapNightSessionConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.NightSessionConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

func (h *handlers) handleGetNightSessionRevisions(w http.ResponseWriter, r *http.Request) {
	h.handleGetShowConfigRevisions(w, r, config.NightSessionConfigKind)
}

// handleGetNightSessionRevision serves GET
// /config/night.session/{id}/revisions/{revision}: the FULL payload of one
// past, immutable revision, not only its metadata — the seam spec's own
// "showmeshctl night revision" requirement, and ADR-009's "revisions
// immutable, history queryable" taken literally rather than only through
// the metadata-only /revisions list every other show config kind ships.
// No other config kind in this package exposes this route yet; that is a
// gap for a future seam to close on those kinds, not a reason to withhold
// it here.
func (h *handlers) handleGetNightSessionRevision(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	kind := config.NightSessionConfigKind
	id := r.PathValue("id")

	revNum, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revNum <= 0 {
		writeProblem(w, h.logger, now, invalidParameterProblem("revision must be a positive integer"))
		return
	}

	rev, err := h.deps.Config.GetConfigRevision(r.Context(), kind, id, revNum)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		writeProblem(w, h.logger, now, resourceNotFoundProblem(fmt.Sprintf("no revision %d of night.session %q", revNum, id)))
		return
	}
	if err != nil {
		h.writeInternalError(w, now, "get night.session config revision", err)
		return
	}
	var payload config.NightSessionPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode night.session config payload", err)
		return
	}

	obj, err := h.deps.Config.GetConfigObject(r.Context(), kind, id)
	if err != nil {
		h.writeInternalError(w, now, "get night.session config object", err)
		return
	}
	jsonWrite(w, mapNightSessionConfigResponse(now, rev, obj, payload))
}

// mapNightSessionConfigResponse renders rev — a SPECIFIC revision, which
// on the GET .../revisions/{revision} route is not necessarily obj's
// current one — as the wire response. UpdatedAt uses rev.CreatedAt, never
// obj.UpdatedAt: found in review, obj.UpdatedAt is the OBJECT's latest
// write time, so GET .../revisions/1 on an object now at revision 3 was
// reporting revision 1 as "updated" at revision 3's timestamp. On the
// current-revision routes (GET/PUT .../night.session/{id}) the two times
// coincide, since obj.UpdatedAt is set from the very same write that
// created rev; this function does not special-case that — it uses
// rev.CreatedAt uniformly, which is correct on both routes rather than
// merely equal by coincidence on one of them.
func mapNightSessionConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.NightSessionPayload) v1.NightSessionConfigResponse {
	return v1.NightSessionConfigResponse{
		ServerTime: formatTime(now), Kind: config.NightSessionConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                mapConfigNightSession(p),
		UpdatedAt:              formatTime(rev.CreatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

func mapConfigNightSession(p config.NightSessionPayload) v1.ConfigNightSession {
	return v1.ConfigNightSession{
		Show: p.Show, Label: p.Label,
		ShowPlaylist:              v1.ConfigNightSessionFPPPlaylist{FPPInstanceID: p.ShowPlaylist.FPPInstanceID, Playlist: p.ShowPlaylist.Playlist},
		Resting:                   mapConfigNightSessionResting(p.Resting),
		EnterShow:                 v1.ConfigNightSessionEnterShow{Cues: mapConfigNightSessionCues(p.EnterShow.Cues), BlackoutHoldMs: p.EnterShow.BlackoutHoldMs},
		EnterResting:              v1.ConfigNightSessionEnterResting{Cues: mapConfigNightSessionCues(p.EnterResting.Cues), BlackoutAfterShowMs: p.EnterResting.BlackoutAfterShowMs},
		AnnouncementDefaultPolicy: p.AnnouncementDefaultPolicy,
	}
}

func mapConfigNightSessionAssetRef(a config.NightSessionAssetRef) v1.ConfigNightSessionAssetRef {
	return v1.ConfigNightSessionAssetRef{Show: a.Show, Sequence: a.Sequence, Target: a.Target}
}

func mapConfigNightSessionResting(r config.NightSessionResting) v1.ConfigNightSessionResting {
	out := v1.ConfigNightSessionResting{
		FPPInstanceID: r.FPPInstanceID, Playlist: r.Playlist, EndOfNightPlaylist: r.EndOfNightPlaylist,
		TimelineAsset: mapConfigNightSessionAssetRef(r.TimelineAsset), EndOfNightRepeat: r.EndOfNightRepeat,
	}
	if r.BackgroundAudio != nil {
		items := make([]v1.ConfigNightSessionBackgroundAudioItem, 0, len(r.BackgroundAudio.Items))
		for _, it := range r.BackgroundAudio.Items {
			items = append(items, v1.ConfigNightSessionBackgroundAudioItem{
				ItemID: it.ItemID, Show: it.Asset.Show, Sequence: it.Asset.Sequence, Target: it.Asset.Target,
			})
		}
		out.BackgroundAudio = &v1.ConfigNightSessionBackgroundAudio{
			Items: items, Repeat: r.BackgroundAudio.Repeat, Resume: r.BackgroundAudio.Resume,
			ItemTransition: r.BackgroundAudio.ItemTransition, CrossfadeMs: r.BackgroundAudio.CrossfadeMs,
			MaxGainDb: r.BackgroundAudio.MaxGainDb,
		}
	}
	return out
}

func mapConfigNightSessionCues(cues []config.NightSessionCue) []v1.ConfigNightSessionCue {
	out := make([]v1.ConfigNightSessionCue, 0, len(cues))
	for _, c := range cues {
		out = append(out, v1.ConfigNightSessionCue{
			Name: c.Name, Role: c.Role, Action: c.Action, OffsetMs: c.OffsetMs,
			FadeDurationMs: c.FadeDurationMs, Barrier: c.Barrier, OnFailure: c.OnFailure,
			AnnouncementPolicy: c.AnnouncementPolicy,
		})
	}
	return out
}

// --- kind "night.session.active" (singleton) ---

func (h *handlers) handleGetNightSessionActive(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	rev, obj, problem, err := h.getActiveShowConfigRevision(r.Context(), config.NightSessionActiveConfigKind, config.NightSessionActiveObjectID)
	if err != nil {
		h.writeInternalError(w, now, "get active night.session.active config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	var payload config.NightSessionActivePayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode night.session.active config payload", err)
		return
	}
	jsonWrite(w, mapNightSessionActiveConfigResponse(now, rev, obj, payload))
}

func (h *handlers) handlePutNightSessionActive(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())
	id := config.NightSessionActiveObjectID

	raw, err := io.ReadAll(io.LimitReader(r.Body, maxShowConfigRequestBodyBytes+1))
	if err != nil {
		h.writeInternalError(w, now, "read night.session.active request body", err)
		return
	}
	if len(raw) > maxShowConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body exceeds the maximum accepted size"))
		return
	}

	payload, verr := config.DecodeNightSessionActivePayload(string(raw), h.nightSessionExists(r.Context()))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeNightSessionActivePayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode night.session.active config payload", err)
		return
	}

	activated, nextRevisionNo, writeErr := h.writeShowConfigRevision(r, now, ac, config.NightSessionActiveConfigKind, id, payloadJSON,
		map[string]any{"session": payload.Session})
	if writeErr != nil {
		h.writeInternalError(w, now, "write night.session.active config revision", writeErr)
		return
	}

	jsonWrite(w, mapNightSessionActiveConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.NightSessionActiveConfigKind, ID: id, CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// handleGetNightSessionActiveRevisions serves GET
// /config/night.session.active/revisions. night.session.active has no
// {id} path segment (a singleton route), mirroring
// handleGetShowActiveRevisions (showobjects.go) exactly, with
// [config.NightSessionActiveObjectID] standing in for the path value it
// does not have.
func (h *handlers) handleGetNightSessionActiveRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	kind := config.NightSessionActiveConfigKind
	id := config.NightSessionActiveObjectID

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(r.Context(), kind, id)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		// No object at all yet: activeRevision stays 0.
	case err != nil:
		h.writeInternalError(w, now, "get "+kind+" config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(r.Context(), kind, id)
	if err != nil {
		h.writeInternalError(w, now, "list "+kind+" config revisions", err)
		return
	}

	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: kind, Revisions: out})
}

func mapNightSessionActiveConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, p config.NightSessionActivePayload) v1.NightSessionActiveConfigResponse {
	return v1.NightSessionActiveConfigResponse{
		ServerTime: formatTime(now), Kind: config.NightSessionActiveConfigKind, ID: obj.ID, Revision: rev.Revision,
		Payload:                v1.ConfigNightSessionActive{Session: p.Session},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
