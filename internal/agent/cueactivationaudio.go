package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
func activationRevision(act cueactivation.Activation, step int) pkgaudio.Revision {
	return pkgaudio.Revision(cueactivation.AudioSessionRevision(act.EvidenceAt, step))
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
		return "", fmt.Errorf("cue.activate: outputs.announcement.policy %q is not a recognized mix policy: %w", policy, err)
	}
	if mp == pkgaudio.MixPolicyUnsupported {
		return "", fmt.Errorf("cue.activate: outputs.announcement.policy %q is not directly authorable", policy)
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
func activateAudio(ctx context.Context, mgr *audio.Manager, assetDir string, act cueactivation.Activation, out cuecatalog.AudioOutput, ltc *cuecatalog.LTCOutput, announcement *cuecatalog.AnnouncementOutput) error {
	if out.Filename == "" {
		return fmt.Errorf("cue.activate: audio output for Cue %q resolves asset %q to no runtime filename (no matching asset uploaded); refusing to open anything by asset id (ADR-043 decision 6)", act.CueID, out.Asset)
	}

	contentHash := firstAssetHash(out.AssetHashes)
	var sizeBytes int64
	if info, err := os.Stat(filepath.Join(assetDir, out.Filename)); err == nil {
		sizeBytes = info.Size()
	}

	id := cueActivationAudioSessionID
	sourceRole := pkgaudio.SourceRoleShow
	var mixPolicy *pkgaudio.MixPolicy
	if announcement != nil {
		id = pkgaudio.SessionID(cueactivation.AnnouncementSessionID)
		sourceRole = pkgaudio.SourceRoleAnnouncement
		mp, err := announcementMixPolicy(announcement.Policy)
		if err != nil {
			return err
		}
		mixPolicy = &mp
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

	applyOutcome := mgr.Apply(ctx, id, activationInvocation(act, "apply"), activationRevision(act, activationStepApply), req)
	if audioOutcomeFailed(applyOutcome) {
		return fmt.Errorf("cue.activate: audio.session.apply for Cue %q: %s: %s", act.CueID, applyOutcome.Outcome, applyOutcome.Reason)
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
		promoteOutcome := mgr.Promote(ctx, pkgaudio.SessionID(cueactivation.PrepareStagingSessionID), id, activationInvocation(act, "start"), activationRevision(act, activationStepStart))
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
			mgr.Clear(ctx, pkgaudio.SessionID(cueactivation.PrepareStagingSessionID), activationInvocation(act, "clear-stage"), activationRevision(act, activationStepStart))
		}
	}

	if !started {
		prepOutcome := mgr.Prepare(ctx, id, activationInvocation(act, "prepare"), activationRevision(act, activationStepPrepare))
		if audioOutcomeFailed(prepOutcome) {
			return fmt.Errorf("cue.activate: audio.session.prepare for Cue %q: %s: %s", act.CueID, prepOutcome.Outcome, prepOutcome.Reason)
		}

		startOutcome := mgr.Start(ctx, id, activationInvocation(act, "start"), activationRevision(act, activationStepStart))
		if audioOutcomeFailed(startOutcome) {
			return fmt.Errorf("cue.activate: audio.session.start for Cue %q: %s: %s", act.CueID, startOutcome.Outcome, startOutcome.Reason)
		}
	}

	seekOutcome := mgr.Seek(ctx, id, activationInvocation(act, "seek"), activationRevision(act, activationStepSeek), time.Duration(act.PositionMS)*time.Millisecond)
	if audioOutcomeFailed(seekOutcome) {
		return fmt.Errorf("cue.activate: audio.session.seek for Cue %q to position %dms: %s: %s", act.CueID, act.PositionMS, seekOutcome.Outcome, seekOutcome.Reason)
	}
	return nil
}
