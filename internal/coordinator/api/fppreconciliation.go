package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fppreconcile"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is TRACK-H-H2-SPEC.md §5's read route: it renders
// [fppreconcile.Reconcile]'s answer for GET requests, and holds no
// reconciliation logic of its own — see that package's own doc comment
// for why it lives outside this package entirely (shared by this route, a
// future activation seam, and its own tests).

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

	// [Dependencies.AssetManifests] doubles as this route's *store.Store —
	// see that field's own doc comment for why it is a concrete type with
	// no safe no-op default, matching [handleAssetManifest]'s identical
	// nil check one file over.
	if h.deps.AssetManifests == nil {
		h.writeInternalError(w, now, "reconcile fpp playlist entry observation",
			fmt.Errorf("api: no store wired in to reconcile against"))
		return
	}
	result, err := fppreconcile.Reconcile(ctx, h.deps.AssetManifests, obs)
	if err != nil {
		h.writeInternalError(w, now, "reconcile fpp playlist entry observation", err)
		return
	}

	jsonWrite(w, mapFPPPlaylistEntryReconciliation(result, now))
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
