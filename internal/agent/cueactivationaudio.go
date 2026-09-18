package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// cueActivationAudioSessionID is the one audio session id this node's
// cue.activate operation drives. TRACK-H-cues-and-playlists.md section H4
// consumes [audio.Manager]'s existing session model rather than inventing
// a parallel one: ADR-043 decision 9 forbids treating [pkgaudio.
// PlaylistRef] as the show-level authoring model, and this package goes
// further and never even builds one — every Cue activation selects a
// single resolved asset via [pkgaudio.ApplyRequest.Media], never a
// PlaylistRef. One well-known session id (rather than one minted per
// activation) is what lets "re-applying the same ActivationID" and "a
// later activation supersedes this one" both address the same session
// across calls, matching ADR-026's N=1-per-node convention applied here to
// the one Cue-driven show session a node can run at a time.
// The id itself comes from pkg/cueactivation so the coordinator's
// blackAndSilence stop addresses the same session this creates.
const cueActivationAudioSessionID = pkgaudio.SessionID(cueactivation.AudioSessionID)

// activationInvocation derives a stable [pkgaudio.InvocationID] for one
// named step of act's audio activation. Deterministic in act.ActivationID:
// a redelivery of the identical Activation produces the identical
// invocation id for the identical step, which is what lets [audio.
// Manager]'s own [pkgaudio.RevisionState] — internal/agent/audio's
// existing anti-rewind/idempotent-replay ledger (pkg/audio/identity.go) —
// recognize the redelivery as a replay and return its already-recorded
// outcome rather than re-executing the engine call a second time. This is
// TRACK-H-cues-and-playlists.md's own instruction to "follow the existing
// idempotency-cache behavior... rather than inventing a second mechanism"
// applied to the audio path specifically; cueactivationrender.go's
// surfaceAlreadyActivated applies the identical instruction to the render
// path by a direct state comparison instead, since [pipeline.
// AssignmentStore] has no analogous per-invocation ledger.
func activationInvocation(act cueactivation.Activation, step string) pkgaudio.InvocationID {
	return pkgaudio.InvocationID(act.ActivationID + ":" + step)
}

// Step indices [activationRevision] derives its four strictly-increasing
// revisions from, one per audio.Manager call activateAudio makes — aliases
// of [cueactivation.AudioSessionStep*] (never independently numbered: see
// that constant block's own doc comment for why the coordinator's own
// blackAndSilence stop must sort after every one of these).
const (
	activationStepApply   = cueactivation.AudioSessionStepApply
	activationStepPrepare = cueactivation.AudioSessionStepPrepare
	activationStepStart   = cueactivation.AudioSessionStepStart
	activationStepSeek    = cueactivation.AudioSessionStepSeek
)

// unalignedFallbackStepStart/Seek sort past AudioSessionStepStop so a
// missed-instant fallback's own revisions never collide with the
// already-consumed scheduled Start step or a coordinator Stop.
const (
	unalignedFallbackStepStart = cueactivation.AudioSessionStepStop + 1
	unalignedFallbackStepSeek  = cueactivation.AudioSessionStepStop + 2
)

// activationRevision derives one step's [pkgaudio.Revision] from act.
// EvidenceAt via [cueactivation.AudioSessionRevision] — the one shared rule
// the coordinator's own blackAndSilence stop dispatch also derives its
// revision through, see that function's own doc comment for why a second,
// independently written copy of this rule is exactly what left
// blackAndSilence unable to silence anything. EvidenceAt is identical
// across a redelivery of the identical Activation (part of the envelope's
// own full state), and — because it is a real wall-clock reading the
// runner took at the moment it observed this activation — practically
// guaranteed to exceed every prior activation's own revisions for this
// node's one show session.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: no real runner clock has been
// observed to produce two distinct activations with the identical
// nanosecond EvidenceAt.
//
// at is normally act.EvidenceAt, but activateAudio passes a fresher now()
// instead whenever the MultiSync trigger already touched this session at
// a later, wall-clock-derived revision (multisynccueaudio.go).
func activationRevision(at time.Time, step int) pkgaudio.Revision {
	return pkgaudio.Revision(cueactivation.AudioSessionRevision(at, step))
}

// audioOutcomeFailed reports whether outcome is one of the three
// [pkgaudio.Manager] outcomes that mean the step did not take effect —
// matching audiosessionops.go's sessionOp's identical
// Refused/Failed/Unconfirmable-means-not-confirmed convention.
func audioOutcomeFailed(outcome pkgaudio.OutcomeResult) bool {
	switch outcome.Outcome {
	case pkgaudio.OutcomeRefused, pkgaudio.OutcomeFailed, pkgaudio.OutcomeUnconfirmable:
		return true
	default:
		return false
	}
}

// resolveLTCStartOffsetTimecode converts startOffsetMillis (the Cue's
// resolved LTC start offset, H0.3) into the [pkgaudio.LTCTimecode]
// session-override shape [pkgaudio.ApplyRequest.LTCStartOffset] carries,
// at mgr's currently-configured LTC frame rate. ok is false when this
// node's audio.settings do not yet name a usable rate — in that case the
// caller sends no session override at all, and [audio.Manager]'s own
// existing default-offset/no-LTC-without-settings behavior applies
// unchanged (see internal/agent/audio/ltclifecycle.go's
// resolveLTCSpec/startLTCLocked, the ONE place this codebase actually
// starts LTC generation); this function never fabricates a rate to force
// a timecode into existence.
func resolveLTCStartOffsetTimecode(mgr *audio.Manager, startOffsetMillis int) (pkgaudio.LTCTimecode, bool) {
	settings := mgr.SettingsSnapshot()
	if !settings.Configured {
		return "", false
	}
	if err := settings.LTCFrameRate.Validate(); err != nil {
		return "", false
	}
	tc, err := pkgaudio.LTCTimecode("00:00:00:00").Advance(time.Duration(startOffsetMillis)*time.Millisecond, settings.LTCFrameRate)
	if err != nil {
		return "", false
	}
	return tc, true
}

// announcementMixPolicy maps [cuecatalog.AnnouncementOutput.Policy]
// (already validated at authoring time — config/showcue.go's
// showCueAnnouncementPolicies is exactly [pkgaudio.MixPolicy]'s own
// "duck"/"mix"/"interrupt" members, spelled identically) onto
// [pkgaudio.MixPolicy], refusing anything that is not one of those three
// closed-vocabulary strings rather than passing an unvalidated value
// through to [pkgaudio.ApplyRequest.MixPolicy].
func announcementMixPolicy(policy string) (pkgaudio.MixPolicy, error) {
	mp := pkgaudio.MixPolicy(policy)
	if err := mp.Validate(); err != nil {
		return "", fmt.Errorf("mix policy %q is not valid: %w", policy, err)
	}
	if mp == pkgaudio.MixPolicyUnsupported {
		return "", fmt.Errorf("mix policy %q cannot be set directly", policy)
	}
	return mp, nil
}

// activateAudio is TRACK-H-cues-and-playlists.md section H4's audio (and,
// transitively, LTC) requirement: select the Cue's resolved audio asset,
// apply it to the ONE session act's Cue actually belongs to, and align
// playback to act.PositionMS via Seek.
//
// A Cue that declares the `announcement` output (H0.4) runs in
// [cueactivation.AnnouncementSessionID] as [pkgaudio.SourceRoleAnnouncement]
// with its declared duck/mix/interrupt [pkgaudio.MixPolicy] set on Apply —
// TRACK-H-cues-and-playlists.md section H5 build item 2's own fix: this used to hardcode
// [pkgaudio.SourceRoleShow] and never set MixPolicy at all, which is what
// silently made every announcement play as an ordinary show Cue with no mix
// relationship to whatever background session was already running. Every
// other Cue (declaring `audio` without `announcement`) is unchanged: it
// runs in [cueActivationAudioSessionID] as [pkgaudio.SourceRoleShow], which
// is also the only role [audio.Manager.startLTCLocked] ever starts LTC for
// (internal/agent/audio/ltclifecycle.go's isShowSessionLocked) — so an
// announcement Cue that also declares `ltc` still emits none, by
// construction, never a special case here.
//
// When ltc is non-nil, the session's own LTCStartOffset override is set on
// Apply (before Start), so [audio.Manager.startLTCLocked] — the ONE
// existing path this package ever drives LTC through, per ADR-018 and this
// seam's own H4-BRIEF.md ruling 2 — computes exactly "Cue LTC start offset
// + current Cue position" the moment Start (and, since act.PositionMS may
// place the activation mid-Cue, Seek) runs. Nothing in this function calls
// ltcgen, LTCGenerator.StartLTC, or multisync.Timeline directly; the
// position crosses into the audio clock domain as data (PositionMS, a
// plain time.Duration argument to Seek), never as a second clock.
// activateAudio's string return is the unaligned reason: either
// [startUnalignedOnArrival]'s missed-instant fallback once its own start
// and seek both succeeded, or the scheduled start's own confirmed outcome
// naming why THIS node's clock could not honor the instant it started
// on arrival instead ([pkgaudio.ReasonScheduledStartIgnored]); empty on
// every other successful path.
func activateAudio(ctx context.Context, mgr *audio.Manager, assetDir string, act cueactivation.Activation, out cuecatalog.AudioOutput, ltc *cuecatalog.LTCOutput, announcement *cuecatalog.AnnouncementOutput, now func() time.Time) (string, *audioStartTriggerRecord, error) {
	if out.Filename == "" {
		return "", nil, fmt.Errorf("cue %q's audio asset %q has not been uploaded to this node, upload it and redeploy", act.CueID, out.Asset)
	}

	contentHash := firstAssetHash(out.AssetHashes)
	var sizeBytes int64
	if info, err := os.Stat(filepath.Join(assetDir, out.Filename)); err == nil {
		sizeBytes = info.Size()
	}

	// A resting or preshow bed must never keep playing once any show
	// cue starts, cut immediately rather than wait on a later
	// night-controller pause command. Announcement sessions never cut it.
	if announcement == nil {
		mgr.CutBackgroundBed(ctx, pkgaudio.SessionID(cueactivation.BackgroundSessionID))
	}

	id := cueActivationAudioSessionID
	sourceRole := pkgaudio.SourceRoleShow
	var mixPolicy *pkgaudio.MixPolicy
	if announcement != nil {
		id = pkgaudio.SessionID(cueactivation.AnnouncementSessionID)
		sourceRole = pkgaudio.SourceRoleAnnouncement
		mp, err := announcementMixPolicy(announcement.Policy)
		if err != nil {
			return "", nil, err
		}
		mixPolicy = &mp
	}

	target := audio.TargetMediaIdentity(pkgaudio.MediaRef{AssetID: out.Asset, ContentHash: contentHash})

	// ADR-051 decision 6/item 7: a session Playing under this exact
	// MultiSync trigger is left alone. One touched but not playing already
	// consumed a wall-clock revision above act.EvidenceAt, so derive from now().
	revisionAt := act.EvidenceAt
	if announcement == nil && cueActivationTriggerRegistry != nil {
		if rec, ok := cueActivationTriggerRegistry.get(id); ok &&
			rec.Trigger == pkgaudio.StartTriggerMultiSync && rec.CueID == act.CueID && rec.MediaIdentity == target {
			playing := false
			if identity, loaded := mgr.LoadedMediaIdentity(id); loaded && identity == target {
				for _, snap := range mgr.Snapshot(ctx) {
					if snap.ID == id && snap.State == pkgaudio.StatePlaying {
						playing = true
					}
				}
			}
			if playing {
				return "", &rec, nil
			}
			revisionAt = now()
		}
	}

	// recordCoordinatorStart is called once this function's own start
	// path actually confirms, never MultiSync's, which records its own
	// evidence directly (multisynccueaudio.go), so the audio session
	// report and a later activation's own restart guard above always see
	// one of the two trigger values, never a blank one once a session has
	// actually played (ADR-051 decision 6).
	recordCoordinatorStart := func() {
		if cueActivationTriggerRegistry == nil {
			return
		}
		cueActivationTriggerRegistry.set(id, audioStartTriggerRecord{
			Trigger: pkgaudio.StartTriggerCoordinator, CueID: act.CueID, MediaIdentity: target,
		})
	}

	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(sourceRole),
		Media: pkgaudio.SetField(pkgaudio.MediaRef{
			AssetID:     out.Asset,
			ContentHash: contentHash,
			SizeBytes:   sizeBytes,
			// RuntimeFilename is the name internal/agent/audio.ProbeAsset
			// (and the engine's own open path) actually opens and
			// hash-verifies on every probe — out.Asset is a logical
			// identity, never a filename (ADR-043 decision 6), matching
			// the render side's identical fix one file over.
			RuntimeFilename: out.Filename,
		}),
	}
	if mixPolicy != nil {
		req.MixPolicy = pkgaudio.SetField(*mixPolicy)
	}
	if ltc != nil {
		if tc, ok := resolveLTCStartOffsetTimecode(mgr, ltc.StartOffsetMillis); ok {
			req.LTCStartOffset = pkgaudio.SetField(tc)
		}
	}

	applyOutcome := mgr.Apply(ctx, id, activationInvocation(act, "apply"), activationRevision(revisionAt, activationStepApply), req)
	if audioOutcomeFailed(applyOutcome) {
		return "", nil, fmt.Errorf("cue %q's audio setup failed (%s): %s", act.CueID, applyOutcome.Outcome, applyOutcome.Reason)
	}

	position := time.Duration(act.PositionMS) * time.Millisecond

	// ADR-049 decision 3: an activation the coordinator scheduled a
	// shared multi-node start instant for must present position at
	// act's own T0, on every node, in one engine call, a Start-then-
	// Seek pair cannot do that (see [audio.Manager.StartAtPosition]'s
	// own doc comment). This bypasses [audio.Manager.Promote]
	// deliberately rather than teaching it to schedule too: Promote
	// exists purely to skip a redundant media load when a coordinator-
	// staged handle already matches, and a scheduled activation's own
	// coordinator-side reading round (internal/coordinator/api's
	// scheduleCueActivations) already pays for its own Prepare under a
	// throwaway session, so there is no staged handle on
	// [cueactivation.PrepareStagingSessionID] worth promoting from here.
	// The ordinary Promote-refusal fallback below, Prepare then Start,
	// is exactly what this path also does, with StartAtPosition in place
	// of Start.
	if act.ScheduledAtNs != nil {
		if announcement == nil {
			// Never Promoted from on this path, above, so the
			// Promote-refusal cleanup below never runs for it either:
			// without this, a prepare-ahead round that staged this same
			// Cue in advance would leave that stage loaded and
			// unreleased until some future, unrelated Cue's own
			// prepare-ahead cycle happens to overwrite it.
			mgr.Clear(ctx, pkgaudio.SessionID(cueactivation.PrepareStagingSessionID), activationInvocation(act, "clear-stage"), activationRevision(revisionAt, activationStepStart))
		}
		prepOutcome := mgr.Prepare(ctx, id, activationInvocation(act, "prepare"), activationRevision(revisionAt, activationStepPrepare))
		if audioOutcomeFailed(prepOutcome) {
			return "", nil, fmt.Errorf("cue %q's audio was not ready (%s): %s", act.CueID, prepOutcome.Outcome, prepOutcome.Reason)
		}
		startOutcome := mgr.StartAtPosition(ctx, id, activationInvocation(act, "start"), activationRevision(revisionAt, activationStepStart), *act.ScheduledAtNs, position)
		if audioOutcomeFailed(startOutcome) {
			if startOutcome.Outcome == pkgaudio.OutcomeRefused && strings.HasPrefix(startOutcome.Reason, pkgaudio.ReasonScheduledStartInPast) {
				return startUnalignedOnArrival(ctx, mgr, id, act, revisionAt, position, startOutcome.Reason, target)
			}
			return "", nil, fmt.Errorf("cue %q's audio did not start (%s): %s", act.CueID, startOutcome.Outcome, startOutcome.Reason)
		}
		recordCoordinatorStart()
		// A started outcome still carries [pkgaudio.ReasonScheduledStartIgnored]
		// in its Reason when this node's own clock could not honor the
		// instant (resolveScheduleLocked's ignored-instant note, set onto
		// the outcome by [audio.Manager.start] once it succeeds): this node
		// confirmed and is playing, just not at the shared instant, exactly
		// the same unaligned-but-confirmed shape startUnalignedOnArrival
		// reports for a missed instant, so it is surfaced identically
		// rather than silently dropped.
		if strings.HasPrefix(startOutcome.Reason, pkgaudio.ReasonScheduledStartIgnored) {
			return startOutcome.Reason, nil, nil
		}
		return "", nil, nil
	}

	started := false
	if announcement == nil {
		// A coordinator-scheduled prepare-ahead may already have this Cue's
		// content loaded under the staging session (see [audio.Manager.
		// Promote] and [cueactivation.PrepareStagingSessionID]'s own doc
		// comments). Promote's own identity check is the single source of
		// truth for whether that staged content still matches what this
		// activation now wants; this call never guesses. Promote uses the
		// Start step's own invocation and revision: on success it occupies
		// that step exactly as an ordinary Start would have, so the Start
		// call below is skipped rather than repeated. On any refusal — no
		// session was staged, it wasn't ready, or its content no longer
		// matches — [Manager.Promote] has touched nothing on id, so falling
		// through to the ordinary Prepare+Start pair below runs exactly as
		// it does when nothing was ever staged.
		promoteOutcome := mgr.Promote(ctx, pkgaudio.SessionID(cueactivation.PrepareStagingSessionID), id, activationInvocation(act, "start"), activationRevision(revisionAt, activationStepStart))
		if promoteOutcome.Outcome == pkgaudio.OutcomeStarted {
			started = true
		} else {
			// Discard a stale or no-longer-useful stage rather than leave it
			// holding a loaded branch until the next prepare-ahead cycle
			// overwrites it. Best-effort: Clear on a staging session that
			// was never created (the common case — nothing was staged yet)
			// reports Stopped, not a failure, and this Cue's own activation
			// must not fail because cleanup of a session it does not itself
			// own had nothing to do.
			//
			// Session.dispatchExemptFromStaleRevision's own THE TRADE
			// paragraph (session.go) describes a delayed clear tearing down
			// a newer session established in the meantime; that danger does
			// not reach this call for two reasons. The staging session id
			// is single purpose, so no newer session ever exists under it
			// for a late clear to tear down, and this Clear is a
			// synchronous in-process call inside one activation, not a
			// dispatched command that can be delayed between broker and
			// agent.
			mgr.Clear(ctx, pkgaudio.SessionID(cueactivation.PrepareStagingSessionID), activationInvocation(act, "clear-stage"), activationRevision(revisionAt, activationStepStart))
		}
	}

	if !started {
		prepOutcome := mgr.Prepare(ctx, id, activationInvocation(act, "prepare"), activationRevision(revisionAt, activationStepPrepare))
		if audioOutcomeFailed(prepOutcome) {
			return "", nil, fmt.Errorf("cue %q's audio was not ready (%s): %s", act.CueID, prepOutcome.Outcome, prepOutcome.Reason)
		}

		startOutcome := mgr.Start(ctx, id, activationInvocation(act, "start"), activationRevision(revisionAt, activationStepStart))
		if audioOutcomeFailed(startOutcome) {
			return "", nil, fmt.Errorf("cue %q's audio did not start (%s): %s", act.CueID, startOutcome.Outcome, startOutcome.Reason)
		}
	}

	seekOutcome := mgr.Seek(ctx, id, activationInvocation(act, "seek"), activationRevision(revisionAt, activationStepSeek), position)
	if audioOutcomeFailed(seekOutcome) {
		return "", nil, fmt.Errorf("cue %q's audio did not move to %dms (%s): %s", act.CueID, act.PositionMS, seekOutcome.Outcome, seekOutcome.Reason)
	}
	recordCoordinatorStart()
	return "", nil, nil
}

// startUnalignedOnArrival is activateAudio's own fallback for a scheduled
// start already missed by the time StartAtPosition runs: start on arrival,
// seek to position, and report the node as confirmed with the unaligned
// reason (not apply-failed) once both actually succeed: the node is
// playing, just not at the scheduled instant. A failed start or seek here
// still returns an error: the fallback did not actually take effect.
func startUnalignedOnArrival(ctx context.Context, mgr *audio.Manager, id pkgaudio.SessionID, act cueactivation.Activation, revisionAt time.Time, position time.Duration, missedReason, mediaIdentity string) (string, *audioStartTriggerRecord, error) {
	startOutcome := mgr.Start(ctx, id, activationInvocation(act, "start-unaligned"), activationRevision(revisionAt, unalignedFallbackStepStart))
	if audioOutcomeFailed(startOutcome) {
		return "", nil, fmt.Errorf("cue %q's audio did not start (%s): %s", act.CueID, startOutcome.Outcome, startOutcome.Reason)
	}
	seekOutcome := mgr.Seek(ctx, id, activationInvocation(act, "seek-unaligned"), activationRevision(revisionAt, unalignedFallbackStepSeek), position)
	if audioOutcomeFailed(seekOutcome) {
		return "", nil, fmt.Errorf("cue %q's audio did not move to %dms (%s): %s", act.CueID, act.PositionMS, seekOutcome.Outcome, seekOutcome.Reason)
	}
	if cueActivationTriggerRegistry != nil {
		cueActivationTriggerRegistry.set(id, audioStartTriggerRecord{
			Trigger: pkgaudio.StartTriggerCoordinator, CueID: act.CueID, MediaIdentity: mediaIdentity,
		})
	}
	return fmt.Sprintf("Started on arrival instead of at the scheduled time: %s", missedReason), nil, nil
}
