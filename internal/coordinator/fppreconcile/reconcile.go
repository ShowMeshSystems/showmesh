// Package fppreconcile resolves one accepted FPP playlist-entry
// observation against the active Show's fpp-runner show.playlist
// bindings (TRACK-H-H2-SPEC.md section 5), and separately computes one
// FPP-backed Playlist's readiness (section 6). Both are pure of HTTP: this
// package has no handler, no request/response type, and no route. That is
// deliberate — the read route (internal/coordinator/api's reconciliation
// GET), a future activation seam, and this package's own tests all call
// the same [Reconcile] function, so none of them can ever compute a
// different answer for the same evidence.
//
// This package does not re-implement sequence monotonicity
// (FPP-PLUGIN-COORDINATOR-CONTRACTS.md section 1.5): that rule is
// enforced at ingestion (internal/coordinator/api/fppobservations.go), and
// a regressed observation never reaches this package because it was never
// accepted into the store this package reads from.
package fppreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Outcome is the closed vocabulary TRACK-H-H2-SPEC.md section 5 fixes for
// resolving an accepted observation, in the exact order [Reconcile] checks
// them and stops at the first refusal.
type Outcome string

const (
	// OutcomeIdentityUnavailable is section 5's own closing rule: an
	// unavailable observation (contracts section 1.4) carries no hash and
	// no entry key by contract, so there is nothing to match, and its
	// filenames must never be used to select one. Checked before, and
	// independent of, the six ordered steps below.
	OutcomeIdentityUnavailable Outcome = "identity-unavailable"

	// OutcomeUnbound is step 1: no active Show's fpp-runner show.playlist
	// names this observation's instanceUuid.
	OutcomeUnbound Outcome = "unbound"

	// OutcomeStaleImport is step 2: the observation's playlistHash differs
	// from the binding's. The old binding is held, not remapped — this is
	// the FPP-playlist-edited case the entry key exists to make visible.
	OutcomeStaleImport Outcome = "stale-import"

	// OutcomeUnknownEntry is step 3: no entry of the binding derives an
	// entry key matching the observation's.
	OutcomeUnknownEntry Outcome = "unknown-entry"

	// OutcomeEvidenceMismatch is step 4: the matched entry carries an
	// expected sequence or media filename that disagrees with what the
	// observation reported. The filenames never select the entry; they
	// only contradict it.
	OutcomeEvidenceMismatch Outcome = "evidence-mismatch"

	// OutcomeCrossShow is step 5: the binding's Show is not the currently
	// active Show. TRACK-H-H2-SPEC.md section 5 frames step 1 as
	// searching only the active Show's playlists; this implementation
	// searches every show at step 1 instead and lets THIS step be the
	// sole, authoritative active-show gate — see
	// fppRunnerBindingsForInstance's own doc comment for why: restricting
	// step 1 to the active show would make this outcome unreachable by
	// construction. [Reconcile] re-reads the active show fresh here
	// rather than reusing any earlier read, so a binding that WAS in the
	// active show when step 1 ran but no longer is by the time this step
	// runs is still caught.
	OutcomeCrossShow Outcome = "cross-show"

	// OutcomeResolved is step 6: the observation names exactly one
	// Playlist, entry, and Cue, pinned to the Playlist and Cue revisions
	// [Result] carries.
	OutcomeResolved Outcome = "resolved"
)

// IsMismatch reports whether o is one of the four contradicting outcomes
// H0.2's own text collapses into the operator-visible "mismatched" state:
// OutcomeStaleImport, OutcomeUnknownEntry, OutcomeEvidenceMismatch, and
// OutcomeCrossShow. This is the same outcome set cueactivate.Decide routes
// through its mismatch handling once an active-show binding is found;
// restated here because this package's own read routes report Outcome
// directly, with no dependency on cueactivate.
func (o Outcome) IsMismatch() bool {
	switch o {
	case OutcomeStaleImport, OutcomeUnknownEntry, OutcomeEvidenceMismatch, OutcomeCrossShow:
		return true
	default:
		return false
	}
}

// OperatorMismatchInstruction is the one-sentence, operator-facing notice
// GET /current-runs and the reconciliation route surface whenever
// [Outcome.IsMismatch] is true. It is a notice only: it names both
// remedies without claiming the coordinator did anything about the
// mismatch itself, and does not change the configured mismatch policy's
// own effect (hold/blackAndSilence/safeCue).
const OperatorMismatchInstruction = "Restart FPP, or re-import the playlist so the coordinator's binding and FPP agree."

// Result is [Reconcile]'s return value: the outcome it reached, plus every
// piece of observed and matched evidence a caller (the read route, a
// future activation seam) needs to explain it — never a bare error, per
// TRACK-H-H2-SPEC.md section 5's own framing ("a state with the observed
// evidence attached").
type Result struct {
	InstanceUUID string
	Outcome      Outcome
	// Reason is a short, human-readable explanation of Outcome. Always
	// set.
	Reason string

	// Observed* mirrors the fields the accepted observation itself
	// carried, regardless of Outcome — including for
	// OutcomeIdentityUnavailable, where ObservedPlaylistHash and
	// ObservedEntryKey are always empty by contract (contracts section
	// 1.4) and must never be read as identity.
	ObservedPlaylistHash string
	ObservedEntryKey     string
	// ObservedSection is a pointer for the same reason ObservedPosition
	// is: the empty string is a real FPP section (the common default
	// one), so a resolved observation reporting it must render
	// distinguishably from "no section reported" (nil). Set exactly when
	// ObservedPosition is: non-nil whenever obs.Unavailable is empty,
	// nil for OutcomeIdentityUnavailable.
	ObservedSection          *string
	ObservedPosition         *int
	ObservedSequenceFilename string
	ObservedMediaFilename    string
	ObservedAction           string
	// ObservedUnavailable is the contracts section 1.4 reason, set only
	// when Outcome is OutcomeIdentityUnavailable.
	ObservedUnavailable string

	// Binding* is populated once a same-instance, active-Show, fpp-runner
	// show.playlist binding is located — every Outcome except
	// OutcomeIdentityUnavailable and OutcomeUnbound.
	PlaylistID          string
	PlaylistRevision    int64
	Show                string
	BindingPlaylistHash string
	BindingPlaylistName string

	// Entry/Cue evidence, populated only when Outcome is OutcomeResolved.
	EntryID     string
	CueID       string
	CueRevision int64

	// DefinitionAvailable reports whether this coordinator holds a stored
	// playlist definition for the hash this result actually matched
	// against. It is populated ONLY for the four outcomes that reach
	// entry-key derivation (OutcomeUnknownEntry, OutcomeEvidenceMismatch,
	// OutcomeCrossShow, OutcomeResolved) — every one of which implies
	// step 2's hash comparison already succeeded. TRACK-H-H2-SPEC.md
	// section 5's own text: a missing definition is "not fatal to
	// matching by entry key ... It is fatal to readiness" — so this field
	// annotates the result without changing which of the six outcomes it
	// reached. It is always false, and not meaningful, for
	// OutcomeIdentityUnavailable, OutcomeUnbound, and OutcomeStaleImport,
	// none of which ever confirm a hash to look a definition up by.
	DefinitionAvailable bool
}

// candidateBinding is one active-Show, fpp-runner show.playlist object
// whose fpp.instanceUuid matches the observation under reconciliation.
type candidateBinding struct {
	objectID string
	revision int64
	payload  config.ShowPlaylistPayload
}

// Reconcile resolves obs — an already-accepted observation, read back
// exactly as [store.Store.GetFPPPlaylistEntryObservation] or
// [store.Store.ListFPPPlaylistEntryObservations] returned it — against
// st's current active Show and show.playlist configuration, per
// TRACK-H-H2-SPEC.md section 5. It performs no write of any kind: this is
// read-only resolution, never activation.
func Reconcile(ctx context.Context, st *store.Store, obs store.FPPPlaylistEntryObservationRecord) (Result, error) {
	result := Result{
		InstanceUUID:             obs.InstanceUUID,
		ObservedPlaylistHash:     obs.PlaylistHash,
		ObservedEntryKey:         obs.EntryKey,
		ObservedSequenceFilename: obs.SequenceFilename,
		ObservedMediaFilename:    obs.MediaFilename,
		ObservedAction:           obs.Action,
		ObservedUnavailable:      obs.Unavailable,
	}
	if obs.Unavailable == "" {
		pos := int(obs.Position)
		result.ObservedPosition = &pos
		section := obs.Section
		result.ObservedSection = &section
	}

	// Section 1.4 / H2 spec section 5's closing rule: an unavailable
	// observation carries no hash and no entry key by contract, so there
	// is nothing to match. Checked first and unconditionally — this is
	// NOT one of the six ordered steps, it is what makes reaching them at
	// all conditional on identity having been established.
	if obs.Unavailable != "" {
		result.Outcome = OutcomeIdentityUnavailable
		result.Reason = fmt.Sprintf("FPP could not establish identity for this observation (%s); its filenames are never used to select an entry", obs.Unavailable)
		return result, nil
	}

	// Step 1: locate the binding. This searches every fpp-runner
	// show.playlist naming obs.InstanceUUID, in ANY show, not only the
	// currently active one: step 5 is the step whose entire job is
	// enforcing "must belong to the active show", and it re-reads the
	// active show FRESH rather than reusing a value read here — exactly
	// the section 5 text this function's [OutcomeCrossShow] doc comment
	// quotes. Restricting THIS search to the active show would make
	// OutcomeCrossShow unreachable by construction (a binding outside the
	// active show would already be invisible here, never surfacing as
	// "found, but wrong show"), collapsing a real, distinct refusal into
	// OutcomeUnbound. A binding found in NO show at all for this instance
	// is the genuine OutcomeUnbound case: no ShowMesh output was ever
	// authorized by this instance, in any show, ever.
	candidates, err := fppRunnerBindingsForInstance(ctx, st, obs.InstanceUUID)
	if err != nil {
		return Result{}, err
	}
	if len(candidates) == 0 {
		result.Outcome = OutcomeUnbound
		result.Reason = "no ShowMesh output was ever authorized by this instance: no fpp-runner show.playlist names it"
		return result, nil
	}

	// TRACK-H-H2-SPEC.md section 5 step 1 disambiguates only by
	// instanceUuid; it does not say what happens when more than one
	// show.playlist (in any show) binds the SAME instance (an unusual,
	// but not forbidden, authoring choice). This is the narrower, safer
	// reading of a spec silence: narrow to candidates whose bound
	// playlistHash equals the observation's own hash (real evidence the
	// observation carries, and the thing step 2 would otherwise refuse
	// on for the wrong candidate); if that still leaves more than one
	// candidate, or none, prefer the active show's; then fall back to
	// the playlist-name and lexicographically-smallest-object-id
	// tiebreak for a fully deterministic result. This early read of the
	// active show is only a preference among ambiguous candidates; step
	// 5 below re-reads it fresh and is the sole authoritative gate.
	preferredShow, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return Result{}, fmt.Errorf("fppreconcile: resolve active show for candidate tiebreak: %w", err)
	}
	binding := chooseCandidateBinding(candidates, obs.PlaylistHash, obs.PlaylistName, preferredShow)
	result.PlaylistID = binding.objectID
	result.PlaylistRevision = binding.revision
	result.Show = binding.payload.Show
	result.BindingPlaylistHash = binding.payload.FPP.PlaylistHash
	result.BindingPlaylistName = binding.payload.FPP.PlaylistName

	// Step 2: compare the hash. The old binding is held, not remapped.
	if binding.payload.FPP.PlaylistHash != obs.PlaylistHash {
		result.Outcome = OutcomeStaleImport
		result.Reason = fmt.Sprintf(
			"the observed playlistHash (%s) differs from the bound playlistHash (%s); the FPP playlist was edited and re-imported",
			obs.PlaylistHash, binding.payload.FPP.PlaylistHash)
		return result, nil
	}

	// Step 3: derive and match the entry key.
	matched, ok := matchEntryByKey(binding.payload, obs.EntryKey)
	if !ok {
		result.Outcome = OutcomeUnknownEntry
		result.Reason = "no entry of the bound playlist derives an entry key matching the observation's"
		var defErr error
		result.DefinitionAvailable, defErr = definitionAvailable(ctx, st, obs.InstanceUUID, obs.PlaylistHash)
		if defErr != nil {
			return Result{}, defErr
		}
		return result, nil
	}

	// Step 4: corroborating evidence. The filenames never select the
	// entry; they only contradict it.
	if mismatch := evidenceMismatchReason(matched, obs); mismatch != "" {
		result.Outcome = OutcomeEvidenceMismatch
		result.Reason = mismatch
		var defErr error
		result.DefinitionAvailable, defErr = definitionAvailable(ctx, st, obs.InstanceUUID, obs.PlaylistHash)
		if defErr != nil {
			return Result{}, defErr
		}
		return result, nil
	}

	// Step 5: check the Show, against a FRESH read of active.active — the
	// active show can change between resolution and use, so this
	// deliberately does not reuse step 1's `active` value.
	activeNow, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return Result{}, fmt.Errorf("fppreconcile: re-resolve active show: %w", err)
	}
	if !activeNow.Configured || binding.payload.Show != activeNow.ShowID {
		result.Outcome = OutcomeCrossShow
		if !activeNow.Configured {
			result.Reason = "no active show is currently configured"
		} else {
			result.Reason = fmt.Sprintf("the bound playlist's show (%s) is not the currently active show (%s)", binding.payload.Show, activeNow.ShowID)
		}
		var defErr error
		result.DefinitionAvailable, defErr = definitionAvailable(ctx, st, obs.InstanceUUID, obs.PlaylistHash)
		if defErr != nil {
			return Result{}, defErr
		}
		return result, nil
	}

	// Step 6: resolved.
	result.Outcome = OutcomeResolved
	result.Reason = "the observation names exactly one Playlist, entry, and Cue"
	result.EntryID = matched.ID
	result.CueID = matched.Cue
	cueObj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, matched.Cue)
	if err != nil {
		return Result{}, fmt.Errorf("fppreconcile: read resolved cue %q: %w", matched.Cue, err)
	}
	result.CueRevision = cueObj.CurrentRevision
	var defErr error
	result.DefinitionAvailable, defErr = definitionAvailable(ctx, st, obs.InstanceUUID, obs.PlaylistHash)
	if defErr != nil {
		return Result{}, defErr
	}
	return result, nil
}

// fppRunnerBindingsForInstance lists every show.playlist object in showID
// whose current revision is an fpp-runner binding naming instanceUUID.
// Payloads that fail to decode are skipped, never failed on: a broken or
// unrelated show.playlist object must not make reconciliation fail for
// every other instance's own binding (mirrors
// referencedFPPPlaylistHashesByInstance's identical posture in
// internal/coordinator/api/fppplaylistdefinitions.go).
func fppRunnerBindingsForInstance(ctx context.Context, st *store.Store, instanceUUID string) ([]candidateBinding, error) {
	objs, err := st.ListConfigObjects(ctx, config.ShowPlaylistConfigKind)
	if err != nil {
		return nil, fmt.Errorf("fppreconcile: list show.playlist objects: %w", err)
	}
	var out []candidateBinding
	for _, obj := range objs {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowPlaylistConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			if errors.Is(err, store.ErrConfigRevisionNotFound) {
				continue
			}
			return nil, fmt.Errorf("fppreconcile: read show.playlist %q revision %d: %w", obj.ID, obj.CurrentRevision, err)
		}
		payload, decodeErr := decodeStoredShowPlaylistPayload(rev.PayloadJSON)
		if decodeErr != nil {
			continue
		}
		if payload.Runner != config.ShowPlaylistRunnerFPP || payload.FPP == nil {
			continue
		}
		if payload.FPP.InstanceUUID != instanceUUID {
			continue
		}
		out = append(out, candidateBinding{objectID: obj.ID, revision: obj.CurrentRevision, payload: payload})
	}
	return out, nil
}

// chooseCandidateBinding is documented on [Reconcile]'s own step-1 comment:
// narrow to a playlistHash match against the observation's own reported
// hash, then to the active show, then to a playlistName match, else fall
// back to the lexicographically smallest object id. Each narrowing step
// only applies when it actually narrows the pool to fewer candidates;
// otherwise the pool is left as it was and the next step tries.
func chooseCandidateBinding(candidates []candidateBinding, observedPlaylistHash, observedPlaylistName string, preferredShow assetsync.ActiveShow) candidateBinding {
	pool := candidates
	if hashMatches := filterCandidates(pool, func(c candidateBinding) bool {
		return c.payload.FPP.PlaylistHash == observedPlaylistHash
	}); len(hashMatches) > 0 && len(hashMatches) < len(pool) {
		pool = hashMatches
	}
	if len(pool) == 1 {
		return pool[0]
	}
	if preferredShow.Configured {
		if showMatches := filterCandidates(pool, func(c candidateBinding) bool {
			return c.payload.Show == preferredShow.ShowID
		}); len(showMatches) > 0 && len(showMatches) < len(pool) {
			pool = showMatches
		}
	}
	if len(pool) == 1 {
		return pool[0]
	}
	sort.Slice(pool, func(i, j int) bool { return pool[i].objectID < pool[j].objectID })
	if observedPlaylistName != "" {
		for _, c := range pool {
			if c.payload.FPP.PlaylistName == observedPlaylistName {
				return c
			}
		}
	}
	return pool[0]
}

// filterCandidates returns the subset of candidates matching keep.
func filterCandidates(candidates []candidateBinding, keep func(candidateBinding) bool) []candidateBinding {
	var out []candidateBinding
	for _, c := range candidates {
		if keep(c) {
			out = append(out, c)
		}
	}
	return out
}

// matchEntryByKey derives every fpp-bound entry's key and returns the one
// matching entryKey, if any.
func matchEntryByKey(p config.ShowPlaylistPayload, entryKey string) (config.ShowPlaylistEntry, bool) {
	for _, entry := range p.Entries {
		if entry.FPP == nil {
			continue
		}
		derived, err := config.DerivePlaylistEntryKey(p, entry.ID)
		if err != nil {
			continue
		}
		if derived == entryKey {
			return entry, true
		}
	}
	return config.ShowPlaylistEntry{}, false
}

// evidenceMismatchReason reports step 4's refusal reason, or "" when the
// matched entry's declared filenames (when any are declared) agree with
// what the observation reported.
func evidenceMismatchReason(entry config.ShowPlaylistEntry, obs store.FPPPlaylistEntryObservationRecord) string {
	if entry.FPP == nil {
		return ""
	}
	if entry.FPP.ExpectedSequenceFilename != "" && entry.FPP.ExpectedSequenceFilename != obs.SequenceFilename {
		return fmt.Sprintf("expected sequence filename %q does not match the observed %q", entry.FPP.ExpectedSequenceFilename, obs.SequenceFilename)
	}
	if entry.FPP.ExpectedMediaFilename != "" && entry.FPP.ExpectedMediaFilename != obs.MediaFilename {
		return fmt.Sprintf("expected media filename %q does not match the observed %q", entry.FPP.ExpectedMediaFilename, obs.MediaFilename)
	}
	return ""
}

// definitionAvailable reports whether a definition is stored for
// (instanceUUID, playlistHash): a small helper so every one of
// [Reconcile]'s four definition-checking return points reads identically
// rather than repeating the same errors.Is dance four times.
func definitionAvailable(ctx context.Context, st *store.Store, instanceUUID, playlistHash string) (bool, error) {
	_, err := st.GetFPPPlaylistDefinition(ctx, instanceUUID, playlistHash)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, store.ErrFPPPlaylistDefinitionNotFound):
		return false, nil
	default:
		return false, fmt.Errorf("fppreconcile: check stored definition for %q/%q: %w", instanceUUID, playlistHash, err)
	}
}

// decodeStoredShowPlaylistPayload unmarshals an ALREADY-PERSISTED,
// already-validated show.playlist revision directly, never through
// [config.DecodeShowPlaylistPayload]: that decoder exists to validate a
// write, and re-running its cross-reference checks (show exists, cue
// resolves) against a row this package only ever reads back would reject
// a payload that was valid when it was written — mirrors
// internal/coordinator/assetsync's alwaysTrue doc comment and
// internal/coordinator/api/fppplaylistdefinitions.go's identical plain
// json.Unmarshal of the same config kind.
func decodeStoredShowPlaylistPayload(payloadJSON string) (config.ShowPlaylistPayload, error) {
	var payload config.ShowPlaylistPayload
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return config.ShowPlaylistPayload{}, err
	}
	return payload, nil
}
