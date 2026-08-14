package api

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file holds every FPP command confirmation predicate this endpoint
// ships, plus the ONE piece of plumbing they are all built from
// ([resolveConfirmationEvidence]) — Step 7 seam C review defects 2 and 3,
// generalized from fpp.status alone to any single (fpp, instanceID,
// signal) triple: the notBefore fence (an observation collected before
// dispatch can never confirm this command, no matter how strongly it
// already agrees) and [ResolveObservations]'s documented multi-source
// precedence (never "first row" — two collectors can report the same
// signal). Every predicate below calls this function rather than
// re-deriving either rule, per this step's own mandate: "every predicate
// reads through ResolveObservations... apply [the notBefore fence] to
// EVERY predicate, and do not let any new predicate bypass it."
//
// docs/bench/fpp-command-vocabulary.md section 4 is this file's
// authority: each function's own doc comment cites the section its
// reasoning comes from.

// resolveConfirmationEvidence resolves every candidate observation for
// (fpp, instanceID, signal) — via [ResolveObservations], never the first
// matching row — and reports:
//
//   - value: the resolved observation's own Value, meaningful only when
//     current is true.
//   - source: the resolved observation's own Source (for a reason
//     string naming provenance, ADR-011), or "unknown source".
//   - current: true only when a resolved observation exists, was
//     COLLECTED no earlier than notBefore (Step 7 seam C review defect
//     2's fence — pass the zero time.Time to disable it for a
//     PRE-dispatch read, e.g. [fppPrimitive.PreDispatchCheck] or
//     [fppPrimitive.CaptureBaseline], neither of which has a dispatch
//     instant yet to fence against), and reads [observation.StateCurrent]
//     as of now.
//   - outcomeState/outcomeReason: pkg/observation's six-value evidence
//     state and a human reason, populated whenever current is false;
//     both empty when current is true (the caller's own comparison
//     against a wanted value is what produces a reason in that case, if
//     any).
func resolveConfirmationEvidence(ctx context.Context, lister ObservationLister, instanceID, signal string, notBefore, now time.Time) (value any, source string, current bool, outcomeState, outcomeReason string) {
	kind := observation.ResourceFPP
	sig := observation.SignalID(signal)
	obs, err := lister.ListObservations(ctx, ObservationFilter{
		ResourceKind: &kind, ResourceID: &instanceID, Signal: &sig,
	})
	if err != nil {
		return nil, "", false, string(observation.StateCollectionFailed), "reading " + signal + " for confirmation: " + err.Error()
	}

	var candidates []observation.Observation
	for _, o := range obs {
		if o.Resource.Kind != kind || o.Resource.ID != instanceID || o.Signal != sig {
			continue
		}
		candidates = append(candidates, o)
	}
	if len(candidates) == 0 {
		return nil, "", false, string(observation.StateNotCollected), fmt.Sprintf("no %s observation is recorded for this instance yet", signal)
	}

	// ResolveObservations groups by (Resource, Signal); every candidate
	// here already shares that exact triple (the loop above filtered to
	// it), so this always resolves to exactly one winner.
	resolved := ResolveObservations(candidates)
	o := resolved[0]
	src := o.Source
	if src == "" {
		src = "unknown source"
	}

	if o.CollectedAt.Before(notBefore) {
		// ADR-003: a reading collected before dispatch can never confirm
		// this command, even one whose value already happens to match —
		// this is the fence Step 7 seam C review defect 2 added, and the
		// 179-microsecond false confirm it closes (see this file's own top
		// comment).
		return nil, src, false, string(observation.StateNotCollected), fmt.Sprintf(
			"no %s reading has arrived since this command was dispatched at %s; the most recent evidence is from "+
				"%s, via %s, and predates dispatch — it cannot confirm this command",
			signal, notBefore.Format(time.RFC3339), o.CollectedAt.Format(time.RFC3339), src)
	}

	state := o.StateAt(now)
	if state == observation.StateCurrent {
		return o.Value, src, true, string(state), ""
	}
	reason := o.Reason
	if reason == "" {
		reason = fmt.Sprintf("%s evidence state is %s", signal, state)
	}
	return o.Value, src, false, string(state), fmt.Sprintf("%s (via %s)", reason, src)
}

// evaluateFPPStatusEvidence is Step 7 seam C review defects 2 and 3's
// original fix, kept at its original signature (fppcommand_reconcile.go's
// startup sweep calls it directly, against every command it can still
// resolve — stopPlaylist, pausePlaylist, resumePlaylist all use this
// unchanged), now built on [resolveConfirmationEvidence] rather than
// carrying its own copy of the fence/precedence logic. Behavior, wording,
// and every existing test's assertions against it are unchanged by this
// refactor.
func evaluateFPPStatusEvidence(ctx context.Context, lister ObservationLister, instanceID, wantValue string, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	value, source, current, state, reason := resolveConfirmationEvidence(ctx, lister, instanceID, fppStatusSignal, notBefore, now)
	if !current {
		return false, state, reason
	}
	if value == wantValue {
		// Finding 6 (Step 8 review): a confirmed outcome must still state
		// its own confirming evidence — api/openapi.yaml and v1/commands.go
		// both document outcomeReason as non-empty whenever outcome is
		// confirmed OR unconfirmed, and an empty reason here was the code
		// disagreeing with a contract it does not own (this fix stays
		// inside fppcommand_evidence.go; that contract is not touched).
		return true, state, fmt.Sprintf("Reached the expected state (fpp.status %q, via %s).", wantValue, source)
	}
	return false, state, fmt.Sprintf("Not yet: fpp.status is %v, want %q (via %s).", value, wantValue, source)
}

// evaluateStartPlaylistEvidence is startPlaylist's own predicate (capture
// section 4): fpp.status == "playing" AND fpp.playlist.name == wantName,
// BOTH current, both collected at-or-after dispatch. Checking "playing"
// alone would credit ShowMesh with a start FPP's OWN scheduler performed
// — ADR-001 makes FPP the authoritative scheduler, and it starts
// playlists on its own schedule — which is the exact mirror of Step 7's
// 179-microsecond defect (a stop confirmed off a pre-dispatch reading);
// requiring the playlist NAME to also match makes that coincidence
// vanishingly unlikely rather than routine. Both signals are resolved
// independently through [resolveConfirmationEvidence], so a stale name
// alongside a fresh, matching status still reports unconfirmed.
func evaluateStartPlaylistEvidence(ctx context.Context, lister ObservationLister, instanceID, wantName string, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	statusVal, statusSource, statusCurrent, statusState, statusReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppStatusSignal, notBefore, now)
	if !statusCurrent {
		return false, statusState, statusReason
	}
	if statusVal != fppStatusValuePlaying {
		return false, statusState, fmt.Sprintf(
			"Not yet: fpp.status is %v, want %q (via %s).", statusVal, fppStatusValuePlaying, statusSource)
	}

	nameVal, nameSource, nameCurrent, nameState, nameReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppPlaylistNameSignal, notBefore, now)
	if !nameCurrent {
		return false, nameState, fmt.Sprintf("fpp.status is %q, but %s", fppStatusValuePlaying, nameReason)
	}
	if nameVal != wantName {
		// ADR-001: FPP is the authoritative scheduler and may have started
		// a DIFFERENT playlist between dispatch and this check — "playing"
		// alone must never be credited to this command (the same shape as
		// Step 7's 179-microsecond defect; see this file's own top
		// comment).
		return false, nameState, fmt.Sprintf(
			"fpp.status is %q, but a DIFFERENT playlist is playing: fpp.playlist.name is %v (via %s), want %q.",
			fppStatusValuePlaying, nameVal, nameSource, wantName)
	}
	// Finding 6 (Step 8 review): state the confirming evidence even on
	// success — see [evaluateFPPStatusEvidence]'s identical note.
	return true, nameState, fmt.Sprintf(
		"Playing the requested playlist (fpp.status %q via %s, fpp.playlist.name %q via %s).",
		fppStatusValuePlaying, statusSource, wantName, nameSource)
}

// evaluateFPPStopGracefullyEvidence is stopPlaylistGracefully's own
// predicate (capture sections 3.3 and 4): confirmed once fpp.status
// enters EITHER a stopping state OR idle — never idle alone, because
// capture section 3.3 measured a graceful stop holding "stopping
// gracefully" indefinitely against a 120-second running item, a terminal
// state bounded by show content, not by any deadline ShowMesh can
// choose.
//
// On a CONFIRMED result via a stopping (not idle) state, the reason
// states plainly that FPP accepted the graceful stop and the show is
// winding down but has NOT stopped — deliberately non-empty even though
// Outcome is "confirmed", so an operator reading this response cannot
// mistake "confirmed" for "the show has stopped."
func evaluateFPPStopGracefullyEvidence(ctx context.Context, lister ObservationLister, instanceID string, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	value, source, current, state, reason := resolveConfirmationEvidence(ctx, lister, instanceID, fppStatusSignal, notBefore, now)
	if !current {
		return false, state, reason
	}
	valStr, _ := value.(string)
	switch valStr {
	case fppStatusValueIdle:
		return true, state, fmt.Sprintf("The graceful stop completed; playback has ended (fpp.status %q, via %s).", fppStatusValueIdle, source)
	case fppStatusValueStoppingGracefully, fppStatusValueStoppingGracefullyAfterLoop:
		// Capture section 3.3 measured a graceful stop's terminal state as
		// bounded by the currently playing item's own remaining runtime
		// (a 120-second item held this state indefinitely), not by any
		// deadline ShowMesh can choose — so this branch confirms on
		// ENTERING a stopping state, never only on reaching idle. The
		// reason text below states plainly that the show has NOT stopped
		// even though Outcome is "confirmed": this is the exact defect the
		// owner's own live UI test caught (CLAUDE.md), and an operator
		// reading "confirmed" must never conclude playback has ended.
		return true, state, fmt.Sprintf(
			"FPP accepted the graceful stop. The show has NOT stopped yet — it's still running and will stop once "+
				"the current item finishes (fpp.status %q, via %s).", valStr, source)
	default:
		return false, state, fmt.Sprintf("fpp.status is %v, not yet stopping or idle (via %s).", value, source)
	}
}

// evaluateNextItemEvidence is nextPlaylistItem's own predicate (capture
// section 3.5/4): confirmed when fpp.playlist.index differs from a
// PRE-dispatch baseline (same collector source on both ends — see
// Finding 9 below), OR fpp.status == "idle" AND the pre-dispatch baseline
// shows the host was CURRENT and NOT already idle — the idle branch is
// required, not an embellishment: capture 3.5 measured Next Playlist Item
// at the last item ending the playlist entirely (index 3/3 -> idle,
// 0/0), so on a one-item playlist a single Next stops the show, and a
// predicate that only accepted a moved index would report unconfirmed
// for the case with the LARGEST effect.
//
// Finding 1 (Step 8 review, proved live against the bench fppd): the
// idle branch as originally written accepted fpp.status == "idle" with
// no check that the host was NOT already idle before dispatch, so a
// nextPlaylistItem dispatched against an idle-and-staying-idle host —
// capture section 2's own measured no-op ("Next Playlist Item" while
// idle answers 200 "Next Item Playing" and does nothing) — reported
// confirmed for a command that provably did nothing. The idle branch is
// gated on baseline.StatusKnown && baseline.StatusValue !=
// fppStatusValueIdle; when that gate does not hold, the idle branch is
// UNAVAILABLE and this predicate falls through to the unconfirmed tail
// rather than trusting a post-dispatch idle reading it cannot attribute
// to this command.
//
// Finding 9 (Step 8 review): fppCaptureIndexBaseline and this function's
// own resolveConfirmationEvidence call are two INDEPENDENT calls into
// [ResolveObservations], and both fpp-rest and fpp-mqtt emit
// fpp.playlist.index — so the two calls can pick DIFFERENT winning
// sources, which is a source flip between the baseline read and the
// confirming read, not FPP's own counter moving. The index-movement
// branch only fires when the confirming read's source matches
// baseline.IndexSource; a source flip is reported unconfirmed with that
// stated as the reason, never compared across series.
//
// Finding 13 (Step 8 review): a nil observation Value is not a reading —
// fppCaptureIndexBaseline already refuses to record IndexKnown for a nil
// baseline value, and this function additionally refuses to treat a nil
// CONFIRMING value as comparable, so neither side of the comparison can
// render as the literal string "<nil>" and be mistaken for a real index.
//
// Both branches here confirm on a counter/state FPP also advances on its
// own item boundaries — movement (or reaching idle) is NOT uniquely
// attributable to this command, and every reason string below says so.
// When baseline.IndexKnown is false (no current pre-dispatch reading
// existed — see [fppCaptureIndexBaseline]), the index-movement branch is
// skipped entirely rather than inventing a baseline to compare against;
// the idle branch still applies on its own (subject to its own gate
// above), since it needs no index baseline, only a status one.
func evaluateNextItemEvidence(ctx context.Context, lister ObservationLister, instanceID string, baseline fppBaseline, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	indexVal, indexSource, indexCurrent, indexState, indexReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppPlaylistIndexSignal, notBefore, now)

	indexComparable := baseline.IndexKnown && indexCurrent && indexVal != nil && baseline.IndexValue != nil
	sourceFlipped := indexComparable && indexSource != baseline.IndexSource
	if indexComparable && !sourceFlipped && fmt.Sprint(indexVal) != fmt.Sprint(baseline.IndexValue) {
		// Note kept short and factual (not a uniqueness disclaimer): this
		// counter also moves on FPP's own item boundaries, so the operator
		// needs to know it can advance on its own, not a longer
		// explanation of attribution.
		return true, indexState, fmt.Sprintf(
			"The item advanced. Note: this counter also moves on its own (fpp.playlist.index %v -> %v, via %s).",
			baseline.IndexValue, indexVal, indexSource)
	}

	statusVal, statusSource, statusCurrent, statusState, statusReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppStatusSignal, notBefore, now)
	idleBranchAvailable := baseline.StatusKnown && baseline.StatusValue != fppStatusValueIdle
	if idleBranchAvailable && statusCurrent {
		if s, _ := statusVal.(string); s == fppStatusValueIdle {
			// Next Playlist Item at the last item ends the playlist
			// (capture section 3.5) — accepted here as confirmation of the
			// command's largest possible effect. fpp.status also
			// transitions on FPP's own item/playlist boundaries, hence the
			// same short "moves on its own" note as the index-movement
			// branch above.
			return true, statusState, fmt.Sprintf(
				"That was the last item — Next ends the playlist. Note: this also moves on its own (fpp.status %q -> %q, via %s).",
				baseline.StatusValue, fppStatusValueIdle, statusSource)
		}
	}

	if !baseline.IndexKnown && !baseline.StatusKnown {
		return false, string(observation.StateNotCollected),
			"No pre-dispatch reading of fpp.playlist.index or fpp.status was available, so movement can't be evaluated."
	}
	if baseline.StatusKnown && baseline.StatusValue == fppStatusValueIdle {
		// Finding 1: the idle branch is deliberately unavailable here —
		// the host was ALREADY idle before dispatch (capture section 2's
		// measured no-op: "Next Playlist Item" while idle does nothing),
		// so fpp.status reading idle afterwards is not evidence this
		// command had any effect.
		alreadyIdleNote := "the host was ALREADY idle before dispatch, and Next Playlist Item does nothing while idle"
		switch {
		case !baseline.IndexKnown:
			return false, string(observation.StateNotCollected), fmt.Sprintf(
				"No pre-dispatch fpp.playlist.index reading was available, so movement can't be evaluated — and %s.", alreadyIdleNote)
		case sourceFlipped:
			return false, indexState, fmt.Sprintf(
				"fpp.playlist.index's confirming reading came from a different source (%s) than the pre-dispatch "+
					"baseline (%s), so no movement can be attributed to this command — and %s.",
				indexSource, baseline.IndexSource, alreadyIdleNote)
		case !indexCurrent:
			return false, indexState, fmt.Sprintf("%s — and %s.", indexReason, alreadyIdleNote)
		default:
			return false, indexState, fmt.Sprintf(
				"fpp.playlist.index is unchanged (%v, via %s) — and %s.", baseline.IndexValue, baseline.IndexSource, alreadyIdleNote)
		}
	}
	if !baseline.IndexKnown {
		return false, string(observation.StateNotCollected),
			"No pre-dispatch fpp.playlist.index reading was available, so movement can't be evaluated, and fpp.status has not reached idle."
	}
	if sourceFlipped {
		return false, indexState, fmt.Sprintf(
			"fpp.playlist.index's confirming reading came from a different source (%s) than the pre-dispatch baseline "+
				"(%s, value %v) — the readings aren't comparable, so no movement can be attributed to this command; "+
				"fpp.status has also not reached %q.", indexSource, baseline.IndexSource, baseline.IndexValue, fppStatusValueIdle)
	}
	if !indexCurrent {
		return false, indexState, indexReason
	}
	if !statusCurrent {
		return false, statusState, fmt.Sprintf(
			"fpp.playlist.index is unchanged (%v, via %s), and %s", baseline.IndexValue, indexSource, statusReason)
	}
	return false, indexState, fmt.Sprintf(
		"fpp.playlist.index is unchanged (%v, via %s), and fpp.status is %v (via %s), not %q.",
		baseline.IndexValue, indexSource, statusVal, statusSource, fppStatusValueIdle)
}

// evaluatePrevItemEvidence is prevPlaylistItem's own predicate (capture
// section 4): confirmed only when fpp.playlist.index differs from a
// PRE-dispatch baseline read from the SAME collector source (Finding 9,
// below) — no idle fallback, unlike [evaluateNextItemEvidence]: capture
// section 3.5 did not measure "Prev Playlist Item" ending a playlist the
// way Next does at the last item, so this predicate names only the one
// signal the capture actually exercised, and ignores baseline's
// StatusKnown/StatusValue fields entirely. Like Next, movement here is
// not uniquely attributable to this command (FPP's own item boundaries
// advance the same counter), and a missing baseline is reported
// unconfirmed rather than invented.
//
// Finding 9 (Step 8 review): fppCaptureIndexBaseline and this function's
// own resolveConfirmationEvidence call are two INDEPENDENT calls into
// [ResolveObservations], and both fpp-rest and fpp-mqtt emit
// fpp.playlist.index — so the two calls can pick DIFFERENT winning
// sources, which is a source flip, not FPP's own counter moving. A
// source flip is reported unconfirmed rather than compared as movement.
//
// Finding 13 (Step 8 review): a nil observation Value is not a reading;
// neither side of the comparison below is allowed to be nil.
func evaluatePrevItemEvidence(ctx context.Context, lister ObservationLister, instanceID string, baseline fppBaseline, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	if !baseline.IndexKnown {
		return false, string(observation.StateNotCollected),
			"No pre-dispatch fpp.playlist.index reading was available, so movement can't be evaluated."
	}
	indexVal, indexSource, indexCurrent, indexState, indexReason := resolveConfirmationEvidence(ctx, lister, instanceID, fppPlaylistIndexSignal, notBefore, now)
	if !indexCurrent {
		return false, indexState, indexReason
	}
	if indexVal == nil || baseline.IndexValue == nil {
		return false, indexState, fmt.Sprintf(
			"fpp.playlist.index carries no value (via %s) — that's not evidence of movement.", indexSource)
	}
	if indexSource != baseline.IndexSource {
		return false, indexState, fmt.Sprintf(
			"fpp.playlist.index's confirming reading came from a different source (%s) than the pre-dispatch baseline "+
				"(%s, value %v) — the readings aren't comparable, so no movement can be attributed to this command.",
			indexSource, baseline.IndexSource, baseline.IndexValue)
	}
	if fmt.Sprint(indexVal) != fmt.Sprint(baseline.IndexValue) {
		return true, indexState, fmt.Sprintf(
			"The item moved back. Note: this counter also moves on its own (fpp.playlist.index %v -> %v, via %s).",
			baseline.IndexValue, indexVal, indexSource)
	}
	return false, indexState, fmt.Sprintf(
		"fpp.playlist.index is unchanged (%v, via %s).", baseline.IndexValue, indexSource)
}

// toInt64 coerces an observation value to int64 for a numeric comparison,
// accepting every Go type this codebase's own value pipeline can produce
// for a stored or in-memory numeric observation: int64 (store.Store's own
// decodeObservationValue for value_kind "int64" — what a real fpp.volume
// observation round-tripped through SQLite always is), float64 (JSON
// numbers decoded generically, and this codebase's own float64 value
// kind), and int (a literal a test constructs by hand). Returns ok=false
// for anything else — never a silent zero standing in for "not a number".
//
// Finding 10 (Step 8 review): a float64 with a fractional part returns
// ok=false rather than being truncated. This is a deliberate choice
// between two readings of "compare a volume": truncation would make an
// observed 55.9 confirm a requested 55 (and also, by the same rule, a
// requested 56 would need rounding, not truncation, to have any chance —
// truncation is not even internally consistent about which integer a
// fraction is "close to"). FPP's own Volume Set clamps to a whole-number
// 0-100 (capture section 1.5) and this coordinator's own setVolume
// parameter is validated as an integer before dispatch
// ([fppParamInt]/[decodeFPPParamValue]), so a LEGITIMATE fpp.volume
// reading is always a whole number; a fractional one is not evidence of
// anything this command could have asked for, and comparing it via
// truncation risks a false confirmation exactly like the "ma": null
// lesson this project has already paid for once. Verified: mutating this
// branch to `return int64(n), true` (silent truncation) and rerunning
// TestToInt64RejectsFractionalFloat below turns it from failing to
// passing, proving the test would have caught the original defect.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case int:
		return int64(n), true
	case float64:
		if n != math.Trunc(n) {
			return 0, false
		}
		return int64(n), true
	default:
		return 0, false
	}
}

// evaluateSetVolumeEvidence is setVolume's own predicate (capture section
// 4): fpp.volume == the requested value, current, collected at-or-after
// dispatch.
func evaluateSetVolumeEvidence(ctx context.Context, lister ObservationLister, instanceID string, wantVolume int64, notBefore, now time.Time) (confirmed bool, outcomeState, outcomeReason string) {
	value, source, current, state, reason := resolveConfirmationEvidence(ctx, lister, instanceID, fppVolumeSignal, notBefore, now)
	if !current {
		return false, state, reason
	}
	got, ok := toInt64(value)
	if !ok {
		// This coordinator never truncates a fractional reading into a
		// coincidental match — see [toInt64]'s own doc comment (Finding
		// 10) for why exactness, not rounding or truncation, is the rule.
		return false, state, fmt.Sprintf("fpp.volume is %v, which is not a whole number, so it can't confirm or refute this request (via %s).", value, source)
	}
	if got != wantVolume {
		return false, state, fmt.Sprintf("fpp.volume is %d, want %d (via %s).", got, wantVolume, source)
	}
	// Finding 6 (Step 8 review): state the confirming evidence even on
	// success — see [evaluateFPPStatusEvidence]'s identical note.
	return true, state, fmt.Sprintf("Reached the expected volume (fpp.volume %d, via %s).", got, source)
}
