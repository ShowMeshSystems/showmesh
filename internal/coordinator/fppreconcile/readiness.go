package fppreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// ReadinessCondition is the closed, five-member vocabulary
// TRACK-H-H2-SPEC.md section 6 fixes for why an FPP-backed Playlist is not
// ready, checked in this order and stopped at the first failing one — "an
// FPP-backed Playlist is ready when all of these hold, and reports the
// exact failing one when not."
type ReadinessCondition string

const (
	// ReadinessDefinitionMissing: no definition is stored for the
	// binding's (instanceUuid, playlistHash).
	ReadinessDefinitionMissing ReadinessCondition = "definition-missing"
	// ReadinessEntryNotInDefinition: an entry's (section, position) does
	// not exist in the stored definition.
	ReadinessEntryNotInDefinition ReadinessCondition = "entry-not-in-definition"
	// ReadinessEntryFilenameMismatch: an entry's expected filename does
	// not match the definition at that position.
	ReadinessEntryFilenameMismatch ReadinessCondition = "entry-filename-mismatch"
	// ReadinessCueNotReady: a referenced Cue does not exist, has never
	// been activated, or belongs to a different show than the Playlist —
	// the narrow reading of "passes its own readiness" this seam takes,
	// since no richer per-Cue readiness check exists anywhere in this
	// codebase yet (see this file's own doc comment below).
	ReadinessCueNotReady ReadinessCondition = "cue-not-ready"
	// ReadinessObservationHashMismatch: the latest accepted observation
	// for this instance carries a playlistHash different from the
	// binding's. Section 6's own text: "a warning rather than a failure
	// when no observation has been received at all."
	ReadinessObservationHashMismatch ReadinessCondition = "observation-hash-mismatch"
)

// Report is [PlaylistReadiness]'s result.
type Report struct {
	PlaylistID string
	Ready      bool
	// FailingCondition is the exact one of the five [ReadinessCondition]
	// values that made Ready false. Empty when Ready.
	FailingCondition ReadinessCondition
	// Reason explains FailingCondition. Empty when Ready and there is no
	// warning either.
	Reason string
	// Warning is set when the observation-hash-mismatch check did not
	// fail readiness (no comparable observation exists yet) but is still
	// worth surfacing to an operator — section 6's own "the normal
	// afternoon state, not a fault" case. Never set alongside
	// FailingCondition == ReadinessObservationHashMismatch: that
	// combination is the hard-failure form of this same check, not the
	// warning form.
	Warning string
}

// PlaylistReadiness computes TRACK-H-H2-SPEC.md section 6 for one
// fpp-runner show.playlist binding, already resolved to its current
// object id, revision, and decoded payload by the caller (the read route
// that already fetched it to render the Playlist, or a test). p.Runner
// must be [config.ShowPlaylistRunnerFPP] with a non-nil p.FPP; anything
// else is a caller contract violation, not one of the five conditions,
// and is reported as a plain error.
//
// Cue readiness (section 6's fourth condition, "passes its own
// readiness") is deliberately narrow here: exists, has an active
// revision, and belongs to the same show. No richer Cue readiness concept
// (render/audio/LTC path health, asset presence) exists anywhere in this
// codebase as of this seam — Track F's night readiness checks are the
// closest precedent, and they check FPP/asset evidence directly rather
// than through any shared "Cue readiness" function, because none exists
// yet. Building one is out of this seam's scope (H2 ships no activation);
// this is the narrower, safer reading of a spec phrase this codebase does
// not yet have infrastructure to answer more richly.
func PlaylistReadiness(ctx context.Context, st *store.Store, playlistID string, revision int64, p config.ShowPlaylistPayload) (Report, error) {
	if p.Runner != config.ShowPlaylistRunnerFPP || p.FPP == nil {
		return Report{}, fmt.Errorf("fppreconcile: playlist readiness requires an fpp-runner playlist with an fpp binding, got runner %q", p.Runner)
	}
	report := Report{PlaylistID: playlistID, Ready: true}

	// Condition 1: a definition is stored for (instanceUuid, playlistHash).
	defRec, err := st.GetFPPPlaylistDefinition(ctx, p.FPP.InstanceUUID, p.FPP.PlaylistHash)
	if errors.Is(err, store.ErrFPPPlaylistDefinitionNotFound) {
		report.Ready = false
		report.FailingCondition = ReadinessDefinitionMissing
		report.Reason = fmt.Sprintf("no playlist definition is stored for instance %q hash %q", p.FPP.InstanceUUID, p.FPP.PlaylistHash)
		return report, nil
	}
	if err != nil {
		return Report{}, fmt.Errorf("fppreconcile: get playlist definition: %w", err)
	}

	entries, err := fppidentity.ParseDefinitionEntries(defRec.DefinitionJSON)
	if err != nil {
		return Report{}, fmt.Errorf("fppreconcile: parse stored definition entries: %w", err)
	}
	type sectionPosition struct {
		section  string
		position int
	}
	byKey := make(map[sectionPosition]fppidentity.DefinitionEntry, len(entries))
	for _, e := range entries {
		byKey[sectionPosition{section: e.Section, position: e.Position}] = e
	}

	// Conditions 2 and 3, in one pass over the binding's entries: every
	// entry's (section, position) must exist in the definition, and its
	// declared filenames (when any are declared) must match it there.
	for _, entry := range p.Entries {
		if entry.FPP == nil {
			continue
		}
		key := sectionPosition{section: entry.FPP.Section, position: entry.FPP.Position}
		def, ok := byKey[key]
		if !ok {
			report.Ready = false
			report.FailingCondition = ReadinessEntryNotInDefinition
			report.Reason = fmt.Sprintf("entry %q (section %q position %d) has no matching entry in the stored definition", entry.ID, entry.FPP.Section, entry.FPP.Position)
			return report, nil
		}
		if entry.FPP.ExpectedSequenceFilename != "" && entry.FPP.ExpectedSequenceFilename != def.SequenceName {
			report.Ready = false
			report.FailingCondition = ReadinessEntryFilenameMismatch
			report.Reason = fmt.Sprintf("entry %q expects sequence filename %q, the stored definition has %q at that position", entry.ID, entry.FPP.ExpectedSequenceFilename, def.SequenceName)
			return report, nil
		}
		if entry.FPP.ExpectedMediaFilename != "" && entry.FPP.ExpectedMediaFilename != def.MediaName {
			report.Ready = false
			report.FailingCondition = ReadinessEntryFilenameMismatch
			report.Reason = fmt.Sprintf("entry %q expects media filename %q, the stored definition has %q at that position", entry.ID, entry.FPP.ExpectedMediaFilename, def.MediaName)
			return report, nil
		}
	}

	// Condition 4: every referenced Cue exists, belongs to the same Show,
	// and passes its own (narrow) readiness — see this function's own doc
	// comment.
	for _, entry := range p.Entries {
		if cond, reason, err := cueReady(ctx, st, entry.Cue, p.Show); err != nil {
			return Report{}, err
		} else if cond != "" {
			report.Ready = false
			report.FailingCondition = cond
			report.Reason = reason
			return report, nil
		}
	}

	// Condition 5: the latest accepted observation, when one exists,
	// carries the same playlistHash. A warning, not a failure, when no
	// observation has been received at all — "the normal afternoon
	// state, not a fault." An observation that exists but could not
	// establish identity (contracts section 1.4) carries no hash to
	// compare either, and is treated the same way for the same reason:
	// there is nothing to disagree with.
	obs, err := st.GetFPPPlaylistEntryObservation(ctx, p.FPP.InstanceUUID)
	switch {
	case errors.Is(err, store.ErrFPPPlaylistEntryObservationNotFound):
		report.Warning = "no observation has been received yet for this instance; this is the normal afternoon state, not a fault"
	case err != nil:
		return Report{}, fmt.Errorf("fppreconcile: get latest observation: %w", err)
	case obs.Unavailable != "":
		report.Warning = fmt.Sprintf("the latest observation for this instance could not establish identity (%s), so its playlistHash cannot be compared", obs.Unavailable)
	case obs.PlaylistHash != p.FPP.PlaylistHash:
		report.Ready = false
		report.FailingCondition = ReadinessObservationHashMismatch
		report.Reason = fmt.Sprintf("the latest observation's playlistHash (%s) differs from the bound playlistHash (%s); the FPP playlist was edited since import", obs.PlaylistHash, p.FPP.PlaylistHash)
		return report, nil
	}

	return report, nil
}

// cueReady implements condition 4's narrow Cue check (see
// [PlaylistReadiness]'s own doc comment): exists, has an active revision,
// and belongs to playlistShow. Returns ("", "", nil) when ready.
func cueReady(ctx context.Context, st *store.Store, cueID, playlistShow string) (ReadinessCondition, string, error) {
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return ReadinessCueNotReady, fmt.Sprintf("cue %q does not exist", cueID), nil
	}
	if err != nil {
		return "", "", fmt.Errorf("fppreconcile: get cue %q: %w", cueID, err)
	}
	if obj.CurrentRevision == 0 {
		return ReadinessCueNotReady, fmt.Sprintf("cue %q has never been activated", cueID), nil
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, cueID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		return ReadinessCueNotReady, fmt.Sprintf("cue %q's current revision could not be read", cueID), nil
	}
	if err != nil {
		return "", "", fmt.Errorf("fppreconcile: get cue %q revision %d: %w", cueID, obj.CurrentRevision, err)
	}
	var cuePayload config.ShowCuePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &cuePayload); err != nil {
		return ReadinessCueNotReady, fmt.Sprintf("cue %q's stored revision could not be decoded", cueID), nil
	}
	if cuePayload.Show != playlistShow {
		return ReadinessCueNotReady, fmt.Sprintf("cue %q belongs to show %q, not this playlist's own show %q", cueID, cuePayload.Show, playlistShow), nil
	}
	return "", "", nil
}
