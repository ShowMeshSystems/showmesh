package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is TRACK-H-H2-SPEC.md §5's read route: it renders
// [fppreconcile.Reconcile]'s answer for GET requests, and holds no
// reconciliation logic of its own — see that package's own doc comment
// for why it lives outside this package entirely (shared by this route, a
// future activation seam, and its own tests).

// StoreFPPReconciliation adapts a concrete *store.Store into
// [FPPReconciliationStore] for production wiring (coordinator.go): the
// interface exists so this field can carry a nil-safe refusing default
// even though [fppreconcile.Reconcile] itself requires a concrete
// *store.Store, so this is the one place that concreteness is allowed to
// show.
type StoreFPPReconciliation struct {
	Store *store.Store
}

// ReconcileFPPPlaylistEntryObservation implements [FPPReconciliationStore]
// by delegating straight to [fppreconcile.Reconcile]; this type adds no
// behavior of its own.
func (s StoreFPPReconciliation) ReconcileFPPPlaylistEntryObservation(ctx context.Context, obs store.FPPPlaylistEntryObservationRecord) (fppreconcile.Result, error) {
	return fppreconcile.Reconcile(ctx, s.Store, obs)
}

// PlaylistReadinessForFPPPlaylist implements [FPPReconciliationStore] by
// delegating straight to [fppreconcile.PlaylistReadiness].
func (s StoreFPPReconciliation) PlaylistReadinessForFPPPlaylist(ctx context.Context, playlistID string, revision int64, p config.ShowPlaylistPayload) (fppreconcile.Report, error) {
	return fppreconcile.PlaylistReadiness(ctx, s.Store, playlistID, revision, p)
}

// handleGetFPPPlaylistEntryReconciliation serves GET
// /api/v1/integrations/fpp/playlist-entry-observations/{instanceUuid}/reconciliation,
// H2 spec §5 and §7 ("Expose it as a read route so an operator can see
// what the coordinator makes of the current observation"), behind
// readGuard(observation:read, ...), matching every other FPP read
// surface. 404s when no observation has ever been accepted for
// instanceUuid — reconciliation has nothing to resolve without one.
func (h *handlers) handleGetFPPPlaylistEntryReconciliation(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	instanceUUID := r.PathValue("instanceUuid")

	obs, err := h.deps.FPPObservations.GetFPPPlaylistEntryObservation(ctx, instanceUUID)
	if err != nil {
		if errors.Is(err, store.ErrFPPPlaylistEntryObservationNotFound) {
			writeProblem(w, h.logger, now, resourceNotFoundProblem(
				"no accepted playlist-entry observation for instanceUuid "+instanceUUID))
			return
		}
		h.writeInternalError(w, now, "get fpp playlist entry observation", err)
		return
	}

	result, err := h.deps.FPPReconciliation.ReconcileFPPPlaylistEntryObservation(ctx, obs)
	if err != nil {
		h.writeInternalError(w, now, "reconcile fpp playlist entry observation", err)
		return
	}

	jsonWrite(w, mapFPPPlaylistEntryReconciliation(result, now))
}

// handleGetFPPPlaylistReadiness serves GET
// /api/v1/integrations/fpp/playlists/{playlistId}/readiness, H2 spec §6
// and §7 ("readiness nobody can see is not readiness"), behind
// readGuard(observation:read, ...), matching this file's own
// reconciliation route and every other FPP read surface. Readiness is an
// FPP-specific concept (H2 §6 is entirely about FPP-backed playlists), so
// this route (not the generic GET /config/show.playlist/{id}) is where
// it lives, and it refuses a playlist that is not fpp-runner rather than
// silently reporting it "ready": readiness §6 defines does not apply to
// one.
func (h *handlers) handleGetFPPPlaylistReadiness(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	playlistID := r.PathValue("playlistId")

	rev, obj, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowPlaylistConfigKind, playlistID)
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
	if payload.Runner != config.ShowPlaylistRunnerFPP || payload.FPP == nil {
		// Readiness applies only to fpp-runner playlists (§6).
		writeProblem(w, h.logger, now, invalidParameterProblem(
			"playlist "+playlistID+" is not fpp-runner; readiness applies only to fpp-runner playlists"))
		return
	}

	report, err := h.deps.FPPReconciliation.PlaylistReadinessForFPPPlaylist(ctx, playlistID, obj.CurrentRevision, payload)
	if err != nil {
		h.writeInternalError(w, now, "compute fpp playlist readiness", err)
		return
	}

	jsonWrite(w, v1.FPPPlaylistReadinessResponse{
		PlaylistID:       report.PlaylistID,
		Ready:            report.Ready,
		FailingCondition: string(report.FailingCondition),
		Reason:           report.Reason,
		Warning:          report.Warning,
		ServerTime:       formatTime(now),
	})
}

// mapFPPPlaylistEntryReconciliation renders result for the wire.
func mapFPPPlaylistEntryReconciliation(result fppreconcile.Result, now time.Time) v1.FPPPlaylistEntryReconciliationResponse {
	return v1.FPPPlaylistEntryReconciliationResponse{
		InstanceUUID: result.InstanceUUID,
		Outcome:      string(result.Outcome),
		Reason:       result.Reason,

		ObservedPlaylistHash:     result.ObservedPlaylistHash,
		ObservedEntryKey:         result.ObservedEntryKey,
		ObservedSection:          result.ObservedSection,
		ObservedPosition:         result.ObservedPosition,
		ObservedSequenceFilename: result.ObservedSequenceFilename,
		ObservedMediaFilename:    result.ObservedMediaFilename,
		ObservedAction:           result.ObservedAction,
		ObservedUnavailable:      result.ObservedUnavailable,

		PlaylistID:          result.PlaylistID,
		PlaylistRevision:    result.PlaylistRevision,
		Show:                result.Show,
		BindingPlaylistHash: result.BindingPlaylistHash,
		BindingPlaylistName: result.BindingPlaylistName,

		EntryID:     result.EntryID,
		CueID:       result.CueID,
		CueRevision: result.CueRevision,

		DefinitionAvailable: result.DefinitionAvailable,

		ServerTime: formatTime(now),
	}
}
