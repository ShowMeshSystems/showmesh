package fppreconcile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// ReadinessCondition was TRACK-H-H2-SPEC.md section 6's closed,
// five-member vocabulary for why an FPP-backed Playlist is not ready,
// checked in order and stopped at the first failing one — "an FPP-backed
// Playlist is ready when all of these hold, and reports the exact failing
// one when not." That vocabulary is now open (docs/build/
// IDENTIFIER-REGISTER.md's "Playlist readiness conditions" section) and
// has taken four additions since; see docs/build/
// TRACK-H-cues-and-playlists.md section H6 for the full account,
// including which conditions remain out of scope this season and why.
//
// [ReadinessDefinitionSuperseded] and [ReadinessEvidenceUnavailable]
// close the same gap. The [ReadinessObservationHashMismatch] check below
// only ever compares against the latest PLAYBACK observation, and FPP's
// own Playlist::GetInfo() legitimately returns no identity once a
// playlist goes idle, so the last observation of every run erases the
// only evidence that check reads: from the moment a playlist finishes
// until the next one starts, an edited-but-never-played FPP playlist read
// ready. ReadinessDefinitionSuperseded answers the same question from
// evidence that does not require playback at all (the plugin posts the
// full definition on every re-scan, not only on play);
// ReadinessEvidenceUnavailable makes "the observation-based check could
// not run" its own failing condition rather than a warning alongside
// Ready == true, because "I could not check" must never render the same
// as "I checked and it is fine."
//
// [ReadinessNodeCatalogStale] and [ReadinessExclusiveClaimConflict] reuse
// the existing resolvers rather than deriving a second one. This type's
// own const list below is the current, authoritative member count;
// nothing else in this codebase should restate a fixed number, since the
// list is still growing.
type ReadinessCondition string

const (
	// ReadinessDefinitionMissing: no definition is stored for the
	// binding's (instanceUuid, playlistHash).
	ReadinessDefinitionMissing ReadinessCondition = "definition-missing"
	// ReadinessDefinitionSuperseded: a newer stored definition exists for
	// the same instance and playlist name, under a hash different from
	// the binding's. This is evidence the FPP playlist was edited that
	// exists independent of playback — the plugin re-scans and re-posts
	// on its own schedule, not only when a playlist plays — so this
	// condition can fail readiness with FPP sitting idle and nothing
	// having been played since the edit, which [ReadinessObservationHashMismatch]
	// below cannot do. "Same playlist name" and "newer" are both decided
	// against the stored definitions themselves, never against the
	// binding's own copies of that data — see [PlaylistReadiness]'s own
	// condition-2 comment for why.
	//
	// This condition only ever compares the bound definition against
	// OTHER stored definitions; it says nothing about whether the bound
	// definition itself is still an accurate read of FPP right now. A
	// playlist that genuinely has not changed in months, and a playlist
	// whose plugin has stopped re-posting altogether (a dead or
	// unreachable plugin), produce the identical row set — no newer row
	// exists to compare against in either case — so both read as Ready
	// here. That is a deliberate, narrower boundary than
	// [ReadinessEvidenceUnavailable]'s, not an oversight: an observation
	// can be marked Unavailable by the collector itself when it received
	// something but could not identify it (contracts §1.4), giving this
	// package a positive "I could not check" signal to fail on. The
	// definitions table has no equivalent marker for "the plugin went
	// quiet" — silence here is indistinguishable from "confirmed
	// unchanged" — so promoting silence itself to a failure would also
	// fail every playlist that is genuinely unedited. Detecting a plugin
	// that has stopped reporting at all is a liveness question (see
	// internal/coordinator/inventory), not a per-playlist readiness
	// question, and belongs there, not here.
	ReadinessDefinitionSuperseded ReadinessCondition = "definition-superseded"
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
	// ReadinessEvidenceUnavailable: an observation exists for this
	// instance but could not establish identity (contracts section 1.4),
	// so it carries no playlistHash to compare. Unlike "no observation
	// has been received at all" (still a warning: nothing has happened
	// yet to check), this is a required check that DID have evidence to
	// look at and could not conclude anything from it — "I could not
	// check" must not read as "I checked and it is fine."
	ReadinessEvidenceUnavailable ReadinessCondition = "evidence-unavailable"

	// ReadinessNodeRenderUnassigned: a referenced Cue declares
	// outputs.render, and a node holding one of the active show's
	// show.surface objects currently has no CONFIRMED render assignment for
	// it. Unlike ReadinessObservationHashMismatch, there is no warning
	// form: cue.activate's render path (internal/agent/
	// cueactivationrender.go's activateRender) refuses outright when a node
	// holds no assignment at all, so every sub-case below is equally
	// show-stopping as an OUTCOME.
	//
	// The CAUSE is not uniform, though, and naming it correctly is this
	// condition's whole reason for existing: the original defect was that
	// a real refusal reason got folded into a free-text array and the
	// operator was pointed at the wrong thing. Reporting "node X holds no
	// render assignment" whenever the node is simply not reporting —
	// powered off, unreachable, or silent since a coordinator restart —
	// would repeat that same defect
	// one layer up: an operator would hunt for a missing assignment on a
	// node that is actually down. So [Report.Reason] names which of these
	// actually holds, using node liveness evidence (internal/coordinator/
	// inventory's LWT+health derivation) alongside the render-assignment
	// signal itself:
	//
	//   - the node is not currently reporting at all (liveness is offline
	//     or unknown) — the assignment cannot be confirmed OR denied, and
	//     the reason says so instead of asserting the node holds none;
	//   - the node is reporting normally (liveness online) and has no
	//     current render assignment for this surface — a real, actionable
	//     unassignment; or
	//   - the node's own surface.pipeline.state evidence for this surface
	//     exists but has aged past its ValidFor window (stale) or carries
	//     no observation time (unknown age) — see nodeHoldsRenderAssignment
	//     for why this is folded into "unassigned" rather than given its
	//     own condition, and why that is a stated decision, not an
	//     oversight: a definite verdict must never be reported from
	//     evidence that was never actually current, the same freshness
	//     discipline this file's other conditions already apply.
	//
	// FailingCondition itself stays this one value in every sub-case —
	// the closed vocabulary section 6 defines is not reopened here — only
	// Reason varies.
	ReadinessNodeRenderUnassigned ReadinessCondition = "node-render-unassigned"

	// ReadinessNodeCatalogStale: a node holding at least one resolved
	// output for this Playlist's Show (render, audio, LTC, or
	// announcement — see [assetsync.ResolveCueCatalog]) has not
	// acknowledged the exact catalog revision the active show currently
	// requires, per [NodeCatalogAckStatus]'s three-way vocabulary
	// (catalog-current/catalog-stale/catalog-unacknowledged). Skipped
	// entirely — never a failure and never a warning — when this
	// Playlist's own Show is not the currently active show: a node only
	// ever holds a catalog for the active show, so there is nothing this
	// condition could compare against for any other Show (see
	// [nodeCatalogReadiness]'s own doc comment).
	ReadinessNodeCatalogStale ReadinessCondition = "node-catalog-stale"

	// ReadinessExclusiveClaimConflict: two Cues this Show's Playlists
	// could concurrently run hold a colliding H0.5 exclusive resource
	// claim (TRACK-H-cues-and-playlists.md section H0.5), exactly as
	// [assetsync.ResolveCueCatalog] already computes and the cue-catalog
	// deploy path already refuses on
	// (internal/coordinator/api/cuecatalogdeploy.go). This condition
	// reads that SAME computation rather than deriving a second one — see
	// [exclusiveClaimReadiness]'s own doc comment.
	ReadinessExclusiveClaimConflict ReadinessCondition = "exclusive-claim-conflict"
)

// Report is [PlaylistReadiness]'s result.
type Report struct {
	PlaylistID string
	Ready      bool
	// FailingCondition is the exact one of the [ReadinessCondition]
	// values that made Ready false. Empty when Ready.
	FailingCondition ReadinessCondition
	// Reason explains FailingCondition. Empty when Ready and there is no
	// warning either.
	Reason string
	// Warning is set when a condition did not fail readiness outright but
	// is still worth surfacing to an operator: either the
	// observation-hash-mismatch check's non-fatal form (no comparable
	// observation exists yet — section 6's own "the normal afternoon
	// state, not a fault" case), or exclusive-claim-conflict's own
	// undecodable-cue case (see [exclusiveClaimReadiness]'s own doc
	// comment) — a stored show.cue elsewhere in the store could not be
	// decoded, so this condition could not be verified one way or the
	// other. Both can be present at once, joined by "; " rather than one
	// overwriting the other. Never set alongside FailingCondition ==
	// ReadinessObservationHashMismatch: that combination is the
	// hard-failure form of the observation check, not its warning form.
	Warning string
}

// PlaylistReadiness computes TRACK-H-H2-SPEC.md section 6 for one
// fpp-runner show.playlist binding, already resolved to its current
// object id, revision, and decoded payload by the caller (the read route
// that already fetched it to render the Playlist, or a test). p.Runner
// must be [config.ShowPlaylistRunnerFPP] with a non-nil p.FPP; anything
// else is a caller contract violation, not one of the eight conditions,
// and is reported as a plain error.
//
// Cue readiness (section 6's fifth condition, "passes its own
// readiness") is deliberately narrow here: exists, has an active
// revision, and belongs to the same show. No richer Cue readiness concept
// (render/audio/LTC path health, asset presence) exists anywhere in this
// codebase as of this seam — Track F's night readiness checks are the
// closest precedent, and they check FPP/asset evidence directly rather
// than through any shared "Cue readiness" function, because none exists
// yet. Building one is out of this seam's scope (H2 ships no activation);
// this is the narrower, safer reading of a spec phrase this codebase does
// not yet have infrastructure to answer more richly.
func PlaylistReadiness(ctx context.Context, st *store.Store, logger *slog.Logger, playlistID string, revision int64, p config.ShowPlaylistPayload) (Report, error) {
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

	// Condition 2: a newer definition is stored for this
	// instance under the same playlist name but a different hash — the
	// plugin re-scanned and re-posted after the operator edited the FPP
	// playlist. This is checked against the definition store directly,
	// never against an observation, so it answers the question with FPP
	// idle and nothing played since the edit (see [ReadinessDefinitionSuperseded]'s
	// own doc comment for why the observation-based check below cannot).
	//
	// "Same playlist" is matched against defRec.PlaylistName — the bound
	// definition's OWN stored name — never against p.FPP.PlaylistName
	// (the binding's copy of that name). The two are normally equal, but
	// nothing keeps them equal, and matching on the binding's copy
	// instead of the definition's own name means a definition whose name
	// has drifted from the binding would never surface as superseding:
	// the loop below would silently exclude every row that could prove
	// it. defRec is already the definition everything else in this
	// function is evaluated against, so its own name, not the binding's
	// possibly-stale copy of it, is the identity to match candidates on.
	//
	// "Newer" is decided by ReceivedAt, the coordinator's own insertion
	// timestamp — fpp_playlist_definitions is insert-only and never
	// overwrites a row (store/fppplaylistdefinitions.go's own doc
	// comment), so ReceivedAt reflects the true order the coordinator
	// learned about each definition. CapturedAt is not used for this
	// ordering: it is plugin-supplied wall-clock time, and a plugin
	// restart, host clock skew, or two definitions captured inside the
	// same coarse timestamp can all give a genuinely newer definition a
	// CapturedAt equal to or earlier than the bound one's. A candidate is
	// excluded here only when it is providably received BEFORE the bound
	// definition; equal-or-later is never treated as "ignore" — it is
	// still different content under the same playlist name, and is
	// reported as superseding rather than silently passed over.
	instanceDefs, err := st.ListFPPPlaylistDefinitionsByInstance(ctx, p.FPP.InstanceUUID)
	if err != nil {
		return Report{}, fmt.Errorf("fppreconcile: list playlist definitions for instance: %w", err)
	}
	var newest *store.FPPPlaylistDefinitionRecord
	for i := range instanceDefs {
		d := &instanceDefs[i]
		if d.PlaylistName != defRec.PlaylistName || d.PlaylistHash == p.FPP.PlaylistHash {
			continue
		}
		if d.ReceivedAt.Before(defRec.ReceivedAt) {
			continue
		}
		if newest == nil || d.ReceivedAt.After(newest.ReceivedAt) {
			newest = d
		}
	}
	if newest != nil {
		report.Ready = false
		report.FailingCondition = ReadinessDefinitionSuperseded
		report.Reason = fmt.Sprintf(
			"a newer playlist definition (hash %s, captured %s) is stored for instance %q playlist %q than the bound hash %s (captured %s); the FPP playlist was edited since import",
			newest.PlaylistHash, newest.CapturedAt.Format(time.RFC3339), p.FPP.InstanceUUID, p.FPP.PlaylistName, p.FPP.PlaylistHash, defRec.CapturedAt.Format(time.RFC3339))
		return report, nil
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

	// Conditions 3 and 4, in one pass over the binding's entries: every
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

	// Condition 5: every referenced Cue exists, belongs to the same Show,
	// and passes its own (narrow) readiness — see this function's own doc
	// comment.
	for _, entry := range p.Entries {
		if cond, reason, err := cueReady(ctx, st, logger, entry.Cue, p.Show); err != nil {
			return Report{}, err
		} else if cond != "" {
			report.Ready = false
			report.FailingCondition = cond
			report.Reason = reason
			return report, nil
		}
	}

	// Condition 6: the latest accepted observation, when one exists,
	// carries the same playlistHash. A warning, not a failure, when no
	// observation has been received at all — "the normal afternoon
	// state, not a fault": there is nothing yet for this check to have
	// evaluated. An observation that exists but could not establish
	// identity (contracts section 1.4) is different: this check DID have
	// evidence to look at and could not conclude anything from it, so it
	// fails as [ReadinessEvidenceUnavailable] rather than warning:
	// "I could not check" must not read as "I checked and it is fine."
	//
	// This binding-specific check runs BEFORE the fleet-wide conditions
	// below on purpose: node-render-unassigned, exclusive-claim-conflict
	// and node-catalog-stale hold or fail identically for every Playlist
	// of a Show, so on an undeployed fleet a Playlist whose OWN binding
	// has drifted would otherwise never surface that. PlaylistReadiness
	// stops at the first failing condition, and every Playlist would
	// report the same fleet-wide failure instead. Checking this one first
	// means a binding problem is never masked by a fleet-wide one.
	obs, err := st.GetFPPPlaylistEntryObservation(ctx, p.FPP.InstanceUUID)
	switch {
	case errors.Is(err, store.ErrFPPPlaylistEntryObservationNotFound):
		report.Warning = "no observation has been received yet for this instance; this is the normal afternoon state, not a fault"
	case err != nil:
		return Report{}, fmt.Errorf("fppreconcile: get latest observation: %w", err)
	case obs.Unavailable != "":
		report.Ready = false
		report.FailingCondition = ReadinessEvidenceUnavailable
		report.Reason = fmt.Sprintf("the latest observation for this instance could not establish identity (%s), so its playlistHash cannot be compared", obs.Unavailable)
		return report, nil
	case obs.PlaylistHash != p.FPP.PlaylistHash:
		report.Ready = false
		report.FailingCondition = ReadinessObservationHashMismatch
		report.Reason = fmt.Sprintf("the latest observation's playlistHash (%s) differs from the bound playlistHash (%s); the FPP playlist was edited since import", obs.PlaylistHash, p.FPP.PlaylistHash)
		return report, nil
	}

	// Condition 7: every node holding a show.surface object for this
	// Playlist's own Show must currently hold a render assignment for it,
	// PROVIDED some referenced Cue actually declares outputs.render:
	// config.DeriveShowCueClaims expands one Cue's render output through
	// every one of the Show's surfaces rather than naming one, so there is
	// no per-Cue surface to check against; the Show's own show.surface
	// objects are the only place "which surfaces this show's cues target"
	// can be answered. Skipped entirely when no entry's Cue declares a
	// render output, matching condition 5's own per-entry Cue lookups
	// (already known-good by this point: every entry above already passed
	// cueReady).
	if cond, reason, err := nodeRenderAssignmentReadiness(ctx, st, p); err != nil {
		return Report{}, err
	} else if cond != "" {
		report.Ready = false
		report.FailingCondition = cond
		report.Reason = reason
		return report, nil
	}

	// Condition 8: no two Cues this Show's Playlists could
	// concurrently run hold a colliding H0.5 exclusive claim.
	if cond, reason, warning, err := exclusiveClaimReadiness(ctx, st, logger, p); err != nil {
		return Report{}, err
	} else if cond != "" {
		report.Ready = false
		report.FailingCondition = cond
		report.Reason = reason
		return report, nil
	} else if warning != "" {
		report.Warning = appendWarning(report.Warning, warning)
	}

	// Condition 9: every node participating in this Show's catalog has
	// acknowledged the active show's currently required catalog revision.
	if cond, reason, err := nodeCatalogReadiness(ctx, st, p); err != nil {
		return Report{}, err
	} else if cond != "" {
		report.Ready = false
		report.FailingCondition = cond
		report.Reason = reason
		return report, nil
	}

	return report, nil
}

// appendWarning combines two independently-produced Report.Warning texts
// (the observation check's and [exclusiveClaimReadiness]'s undecodable-cue
// case can both fire on the same report) without either silently
// overwriting the other.
func appendWarning(existing, add string) string {
	if existing == "" {
		return add
	}
	return existing + "; " + add
}

// nodeRenderAssignmentReadiness implements condition 7 (see
// [PlaylistReadiness]'s own doc comment). It runs only when at least one of
// p's entries references a Cue declaring outputs.render; otherwise this
// Playlist has no render obligation to check and it returns ("", "", nil)
// immediately.
func nodeRenderAssignmentReadiness(ctx context.Context, st *store.Store, p config.ShowPlaylistPayload) (ReadinessCondition, string, error) {
	declaresRender := false
	for _, entry := range p.Entries {
		render, ok, err := cueDeclaresRenderOutput(ctx, st, entry.Cue)
		if err != nil {
			return "", "", err
		}
		if ok && render {
			declaresRender = true
			break
		}
	}
	if !declaresRender {
		return "", "", nil
	}

	surfaces, err := st.ListConfigObjects(ctx, config.ShowSurfaceConfigKind)
	if err != nil {
		return "", "", fmt.Errorf("fppreconcile: list show.surface config objects: %w", err)
	}

	now := time.Now()
	liveness, err := nodeLivenessLookup(ctx, st, now)
	if err != nil {
		return "", "", err
	}

	for _, obj := range surfaces {
		if obj.CurrentRevision == 0 {
			continue
		}
		rev, err := st.GetConfigRevision(ctx, config.ShowSurfaceConfigKind, obj.ID, obj.CurrentRevision)
		if err != nil {
			return "", "", fmt.Errorf("fppreconcile: get show.surface %q revision: %w", obj.ID, err)
		}
		surface, verr := config.DecodeShowSurfacePayload(rev.PayloadJSON, alwaysTrueForReadiness, alwaysTrueForReadiness)
		if verr != nil {
			// A stored payload that no longer decodes is not this
			// condition's question to answer; it would already have
			// surfaced (loudly) wherever that surface is actually applied
			// or dispatched to. Skip it rather than fail readiness on a
			// concern condition 6 does not own.
			continue
		}
		if surface.Show != p.Show {
			continue
		}
		assigned, reason, err := nodeHoldsRenderAssignment(ctx, st, now, liveness, surface.Node, obj.ID)
		if err != nil {
			return "", "", err
		}
		if !assigned {
			return ReadinessNodeRenderUnassigned, reason, nil
		}
	}
	return "", "", nil
}

// cueDeclaresRenderOutput reports whether cueID's stored Cue declares
// outputs.render. ok is false when the Cue does not exist or its current
// revision cannot be read/decoded — condition 4 (cueReady), which always
// runs before this one within [PlaylistReadiness], has already reported
// any such Cue as ReadinessCueNotReady and returned early, so this
// function reaching a "not ok" case here would mean that invariant broke;
// it fails safe (treated as "no render output declared") rather than
// erroring a second time for the same underlying problem.
func cueDeclaresRenderOutput(ctx context.Context, st *store.Store, cueID string) (render bool, ok bool, err error) {
	obj, err := st.GetConfigObject(ctx, config.ShowCueConfigKind, cueID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("fppreconcile: get cue %q: %w", cueID, err)
	}
	if obj.CurrentRevision == 0 {
		return false, false, nil
	}
	rev, err := st.GetConfigRevision(ctx, config.ShowCueConfigKind, cueID, obj.CurrentRevision)
	if errors.Is(err, store.ErrConfigRevisionNotFound) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("fppreconcile: get cue %q revision %d: %w", cueID, obj.CurrentRevision, err)
	}
	var payload config.ShowCuePayload
	if err := json.Unmarshal([]byte(rev.PayloadJSON), &payload); err != nil {
		return false, false, nil
	}
	return payload.Outputs.Render != nil, true, nil
}

// readinessRenderPipelineStateSignal mirrors internal/coordinator/
// collector/noderender.SignalSurfacePipelineState's exact wire spelling,
// and readinessRenderNodeSourceFor mirrors that same package's SourceFor,
// without importing it: this package is a dependency of
// internal/coordinator/api (FPPReconciliationStore), and that package's
// own TestPackageNeverImportsACollector forbids importing any
// internal/coordinator/collector/... package transitively, matching
// internal/coordinator/api/renderdispatch.go's own renderSignalPipelineState
// and renderNodeSourceFor, reimplemented there for the identical reason.
// Both sides of the format are pinned by that file's own
// TestRenderNodeSourceForMatchesNodeRenderPackage.
const readinessRenderPipelineStateSignal = observation.SignalID("surface.pipeline.state")

func readinessRenderNodeSourceFor(nodeID string) string {
	return "node-render:" + nodeID
}

// nodeLivenessInfo is one node's derived liveness verdict, captured at a
// fixed instant so every surface checked within the same
// [nodeRenderAssignmentReadiness] call sees a consistent view rather than
// each surface's lookup racing the clock independently.
type nodeLivenessInfo struct {
	liveness inventory.Liveness
	reason   string
}

// nodeLivenessLookup snapshots every node's current liveness verdict
// (internal/coordinator/inventory's LWT+health derivation) at now, keyed by
// node id, for [nodeHoldsRenderAssignment] to consult when it cannot tell
// from the render-assignment evidence alone whether a node is genuinely
// unassigned or simply not reporting. inventory.New is cheap to construct
// (no goroutines, no broker wiring — see its own doc comment) and reused
// per condition-6 evaluation rather than per surface, so this is one
// ListNodes scan per readiness check, not one per surface.
func nodeLivenessLookup(ctx context.Context, st *store.Store, now time.Time) (map[string]nodeLivenessInfo, error) {
	views, err := inventory.New(st, nil).Snapshot(ctx, now)
	if err != nil {
		return nil, fmt.Errorf("fppreconcile: snapshot node liveness: %w", err)
	}
	byNode := make(map[string]nodeLivenessInfo, len(views))
	for _, v := range views {
		byNode[v.NodeID] = nodeLivenessInfo{liveness: v.Liveness, reason: v.LivenessReason}
	}
	return byNode, nil
}

// livenessOrUnknown returns liveness[nodeID], or an [inventory.LivenessUnknown]
// fallback carrying its own reason when nodeID has no store row at all — a
// node inventory has never heard from (no hello, no LWT, no health ever
// recorded) is absent from [inventory.Manager.Snapshot]'s result entirely,
// not merely present with empty evidence, but that absence means exactly
// the same thing [inventory] would derive from a zero-value record: no
// last-will evidence of an online state.
func livenessOrUnknown(liveness map[string]nodeLivenessInfo, nodeID string) nodeLivenessInfo {
	if info, ok := liveness[nodeID]; ok {
		return info
	}
	return nodeLivenessInfo{liveness: inventory.LivenessUnknown, reason: "no inventory evidence has ever been observed for this node"}
}

// nodeHoldsRenderAssignment reports whether nodeID currently has a
// CONFIRMED, current render assignment for surfaceID — i.e. whether
// cue.activate's render path (internal/agent/cueactivationrender.go's
// activateRender) has something to activate onto — and, when it does not,
// a reason naming the specific cause rather than asserting one outcome for
// three different situations. See [ReadinessNodeRenderUnassigned]'s own
// doc comment for why naming the cause, not just the outcome, is this
// condition's whole reason for existing.
//
// Filtered to nodeID's own source, matching internal/coordinator/api/
// renderdispatch.go's evaluateRenderSurfaceState: two nodes can both hold
// a row for the same surfaceID during a reassignment, and a stale reading
// from the WRONG node must never stand in for this one's own evidence.
//
// Three sub-cases all report assigned == false, with different reasons:
//
//  1. A value-bearing observation exists for nodeID's own source but has
//     aged past its ValidFor window (StateStale) or carries no observation
//     time (StateUnknownAge). This package's own decision — stated here,
//     not left implicit — is to treat that the same as "unassigned" rather
//     than invent a fourth outcome: an aged-out reading is not confirming
//     evidence of anything, and a Playlist must not read as ready on the
//     strength of a signal that stopped being current — the same
//     freshness discipline this file's other conditions already apply,
//     and this condition must not be the one place that skips it.
//  2. No observation row exists at all, or the node explicitly reported
//     dropping the surface (Absence != ""), AND the node's own liveness
//     evidence says it is currently reporting (online): a real,
//     actionable unassignment — the node is there, and it has none.
//  3. The same absence, but the node's liveness evidence says otherwise
//     (offline, or unknown — never heard from, or its evidence is itself
//     stale/contradictory): the assignment cannot be confirmed OR denied,
//     and the reason says exactly that instead of asserting the node
//     "holds no render assignment," which would send an operator hunting
//     for a config problem on a node that is simply not there.
func nodeHoldsRenderAssignment(ctx context.Context, st *store.Store, now time.Time, liveness map[string]nodeLivenessInfo, nodeID, surfaceID string) (assigned bool, reason string, err error) {
	obs, err := st.ListObservations(ctx, store.ObservationFilter{
		ResourceKind: observation.ResourceSurface,
		ResourceID:   surfaceID,
		Signal:       readinessRenderPipelineStateSignal,
	})
	if err != nil {
		return false, "", fmt.Errorf("fppreconcile: list surface.pipeline.state observations for surface %q: %w", surfaceID, err)
	}
	wantSource := readinessRenderNodeSourceFor(nodeID)
	var found observation.Observation
	var hasEvidence bool
	for _, o := range obs {
		if o.Source != wantSource {
			continue
		}
		found, hasEvidence = o, true
		break
	}

	if hasEvidence && found.Absence == "" {
		if state := found.StateAt(now); state != observation.StateCurrent {
			detail := string(state)
			if found.ObservedAt != nil {
				detail = fmt.Sprintf("%s, last observed at %s", detail, found.ObservedAt.Format(time.RFC3339))
			}
			return false, fmt.Sprintf(
				"node %q's render assignment evidence for surface %q is %s rather than current; not treated as a confirmed assignment",
				nodeID, surfaceID, detail), nil
		}
		return true, "", nil
	}

	// Either no row was ever recorded for this node's own source, or the
	// node explicitly reported dropping the surface. Which one that
	// actually means for the node depends on whether the node itself is
	// reporting at all right now — the render-assignment signal alone
	// cannot tell the two apart, but node liveness can.
	info := livenessOrUnknown(liveness, nodeID)
	if info.liveness == inventory.LivenessOnline {
		return false, fmt.Sprintf(
			"node %q is reporting and holds no render assignment for surface %q, which this show's cues target", nodeID, surfaceID), nil
	}
	return false, fmt.Sprintf(
		"node %q's render assignment for surface %q cannot be confirmed: the node itself is not currently reporting (%s)",
		nodeID, surfaceID, info.reason), nil
}

// alwaysTrueForReadiness satisfies [config.DecodeShowSurfacePayload]'s
// showExists/nodeDeclared callbacks for a payload already read from the
// store by its own object id: it already exists by construction, the same
// reasoning internal/coordinator/api/renderdispatch.go's own alwaysTrue
// documents.
func alwaysTrueForReadiness(string) bool { return true }

// exclusiveClaimReadiness implements condition 8 (see [PlaylistReadiness]'s
// own doc comment): TRACK-H-cues-and-playlists.md section H0.6's "Readiness
// rejects... conflicting claims across the Playlists a Show can run
// concurrently." It is evaluated against p.Show directly, not against
// whichever show.active happens to be configured right now — matching
// [participatingNodesForShow] in internal/coordinator/cueactivate/decide.go's
// own reasoning ("Generation is irrelevant to which nodes participate"):
// an H0.5 claim collision is a fact about how p.Show's Cues and Playlists
// are AUTHORED, not about runtime activation, so it is worth catching
// before this Show is ever made active, not only once it is.
//
// It reuses [assetsync.ResolveCueCatalog]'s own conflict detection
// (Catalog.Conflicts) — the SAME computation
// internal/coordinator/api/cuecatalogdeploy.go already refuses a deploy
// on — rather than deriving a second one: TRACK-H-cues-and-playlists.md's
// own H0.5 ruling ("a claim conflict is a readiness fact about the
// catalog, not a resolution failure") is exactly what makes it safe to
// read Conflicts here instead of failing the whole resolution.
//
// Every declared node is checked (not only nodes p.Entries themselves
// touch): a claim conflict between two Cues is a fact about the Show as a
// whole, and the SAME conflict is visible on every node whose catalog
// includes both colliding Cues, so scoping to p's own entries would only
// mean this Playlist's own readiness sometimes fails to see a conflict
// entirely owned by two OTHER Playlists of the same Show — exactly the
// case H0.6's "across the Playlists a Show can run concurrently" calls
// out. Nodes are visited in id order and the first conflict found is
// reported, so which pair is named is deterministic rather than dependent
// on store iteration order.
//
// [assetsync.ResolveCueCatalog] decodes EVERY stored show.cue in the
// WHOLE store (every Show, not only p.Show) before it ever gets to
// filtering by show — internal/coordinator/assetsync is out of this
// seam's bounds to change, so that ordering cannot be fixed here. Left
// unhandled, one undecodable show.cue anywhere would turn this condition
// into a hard error for every Playlist of every Show, the exact "local
// fault becomes a total one" shape this lane exists to remove. This
// function instead recognizes that one specific, stable failure shape
// (see [undecodableCueID]) and reports it the way a genuinely
// unverifiable condition is reported elsewhere in this file (compare
// PlaylistReadiness's own observation-unavailable handling): a named,
// non-fatal Warning rather than Ready=false — this function cannot tell
// whether the corrupted Cue could ever collide with p.Show's own claims,
// so it neither asserts a conflict nor silently claims there is none. Any
// OTHER error from ResolveCueCatalog (a genuine store failure, for
// instance) still fails this condition hard, exactly as before.
func exclusiveClaimReadiness(ctx context.Context, st *store.Store, logger *slog.Logger, p config.ShowPlaylistPayload) (cond ReadinessCondition, reason string, warning string, err error) {
	active := assetsync.ActiveShow{Configured: true, ShowID: p.Show}
	nodes, err := st.ListNodeDeclarations(ctx)
	if err != nil {
		return "", "", "", fmt.Errorf("fppreconcile: list node declarations: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	for _, n := range nodes {
		catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, n.NodeID)
		if err != nil {
			if cueID, ok := undecodableCueID(err); ok {
				if logger != nil {
					logger.Warn("fppreconcile: exclusive-claim-conflict could not be fully evaluated; a stored show.cue could not be decoded", "cueId", cueID, "error", err)
				}
				return "", "", fmt.Sprintf("exclusive-claim-conflict could not be verified: cue %q has a stored revision that could not be decoded", cueID), nil
			}
			return "", "", "", fmt.Errorf("fppreconcile: resolve cue catalog for node %q: %w", n.NodeID, err)
		}
		if len(catalog.Conflicts) > 0 {
			return ReadinessExclusiveClaimConflict, catalog.Conflicts[0].Detail(), "", nil
		}
	}
	return "", "", "", nil
}

// undecodableCueID recognizes the one, stable error text
// [assetsync.ResolveCueCatalog] returns when a stored show.cue revision
// anywhere in the store fails to decode ("assetsync: resolve cue catalog:
// decode stored show.cue %q: ..."), extracting the offending cue id.
// assetsync exposes no sentinel error for this shape, and this package
// must not add one to it (out of this seam's bounds) — matching that one
// stable prefix is the only lever [exclusiveClaimReadiness] has to tell
// "a specific stored cue is corrupted" apart from any other resolver
// failure, which must keep failing this condition hard rather than being
// silently swallowed. ok is false, and cueID is meaningless, for any
// error that does not match.
func undecodableCueID(err error) (cueID string, ok bool) {
	const prefix = `assetsync: resolve cue catalog: decode stored show.cue "`
	rest, ok := strings.CutPrefix(err.Error(), prefix)
	if !ok {
		return "", false
	}
	id, _, ok := strings.Cut(rest, `":`)
	if !ok {
		return "", false
	}
	return id, true
}

// nodeCatalogReadiness implements condition 9 (see [PlaylistReadiness]'s
// own doc comment): TRACK-H-cues-and-playlists.md section H0.6's
// "Readiness rejects... a node without the authorized catalog revision."
//
// Unlike [exclusiveClaimReadiness], this condition genuinely needs the
// REAL currently active show, not merely p.Show: a node's cue-catalog
// acknowledgement ([NodeCatalogAckStatus]) is only ever compared against
// the exact revision [assetsync.ResolveCueCatalog] computes for the
// active show's own generation (TRACK-H-H3-SPEC.md section 3.1), and
// fabricating a generation for a Show that is not actually active would
// compare a node's real acknowledgement against a revision the
// coordinator never actually asked it to hold — a false "stale" report,
// which is worse than not checking at all. So when p.Show is not the
// currently active show, this condition has nothing correct to compare
// against and is skipped entirely: neither a failure nor a warning, the
// same "not this condition's question to answer" posture
// [nodeRenderAssignmentReadiness] takes for a surface payload that no
// longer decodes.
//
// A node "participates" for this purpose when [assetsync.ResolveCueCatalog]
// resolves it at least one non-empty output anywhere in the active show's
// catalog (render, audio, LTC, or announcement) — the same test
// [participatingNodesForShow] in internal/coordinator/cueactivate/decide.go
// applies for its own, unrelated purpose (the blackAndSilence effect's
// target set), reimplemented narrowly here because that function is
// unexported and internal/coordinator/cueactivate is out of this seam's
// bounds to modify. A node with no resolved output has no catalog
// obligation at all and is silently skipped, matching
// [NodeCatalogAckStatus]'s own currentRevision == "" rule.
func nodeCatalogReadiness(ctx context.Context, st *store.Store, p config.ShowPlaylistPayload) (ReadinessCondition, string, error) {
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		return "", "", fmt.Errorf("fppreconcile: resolve active show: %w", err)
	}
	if !active.Configured || active.ShowID != p.Show {
		return "", "", nil
	}

	nodes, err := st.ListNodeDeclarations(ctx)
	if err != nil {
		return "", "", fmt.Errorf("fppreconcile: list node declarations: %w", err)
	}
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].NodeID < nodes[j].NodeID })
	for _, n := range nodes {
		catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, n.NodeID)
		if err != nil {
			return "", "", fmt.Errorf("fppreconcile: resolve cue catalog for node %q: %w", n.NodeID, err)
		}
		if !catalogHasAnyOutput(catalog) {
			continue
		}
		status, ackRevision, _, err := NodeCatalogAckStatus(ctx, st, n.NodeID, catalog.Revision)
		if err != nil {
			return "", "", fmt.Errorf("fppreconcile: resolve node catalog ack status for %q: %w", n.NodeID, err)
		}
		if status != v1.CueCatalogStatusCurrent {
			return ReadinessNodeCatalogStale, fmt.Sprintf(
				"node %q has not acknowledged the active show's required catalog revision %q (status: %s, acknowledged revision: %q)",
				n.NodeID, catalog.Revision, status, ackRevision), nil
		}
	}
	return "", "", nil
}

// catalogHasAnyOutput reports whether catalog resolves at least one
// non-empty output (render, audio, LTC, or announcement) for ANY Cue —
// i.e. whether the node this catalog was resolved for actually
// participates in the active show at all, mirroring
// internal/coordinator/cueactivate/decide.go's own unexported
// hasAnyOutput, reimplemented here for the identical reason
// [nodeCatalogReadiness]'s own doc comment gives.
func catalogHasAnyOutput(catalog assetsync.Catalog) bool {
	for _, e := range catalog.Entries {
		if e.Outputs.Render != nil || e.Outputs.Audio != nil || e.Outputs.LTC != nil || e.Outputs.Announcement != nil {
			return true
		}
	}
	return false
}

// cueReady implements condition 5's narrow Cue check (see
// [PlaylistReadiness]'s own doc comment): exists, has an active revision,
// and belongs to playlistShow. Returns ("", "", nil) when ready.
//
// A stored revision that fails to decode is demoted to
// [ReadinessCueNotReady] rather than failed as an error: an operator
// still needs a readiness answer for the rest of the Playlist. Because
// that demotion swallows the actual decode error, it is logged at warn
// level, naming the cue id, so the corruption is not otherwise invisible.
func cueReady(ctx context.Context, st *store.Store, logger *slog.Logger, cueID, playlistShow string) (ReadinessCondition, string, error) {
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
		if logger != nil {
			logger.Warn("fppreconcile: stored cue revision could not be decoded; reporting cue-not-ready", "cueId", cueID, "error", err)
		}
		return ReadinessCueNotReady, fmt.Sprintf("cue %q's stored revision could not be decoded", cueID), nil
	}
	if cuePayload.Show != playlistShow {
		return ReadinessCueNotReady, fmt.Sprintf("cue %q belongs to show %q, not this playlist's own show %q", cueID, cuePayload.Show, playlistShow), nil
	}
	return "", "", nil
}
