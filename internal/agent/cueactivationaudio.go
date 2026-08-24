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
// revisions from, one per audio.Manager call activateAudio makes.
const (
	activationStepApply = iota
	activationStepPrepare
	activationStepStart
	activationStepSeek
)

// activationRevision derives one step's [pkgaudio.Revision] from act.
// EvidenceAt: identical across a redelivery of the identical Activation
// (EvidenceAt is part of the envelope's own full state, so a redelivery
// carries the identical timestamp), and — because it is a real wall-clock
// reading the runner took at the moment it observed this activation —
// practically guaranteed to exceed every prior activation's own revisions
// for this node's one show session.
//
// SHOWMESH HYPOTHESIS, NOT MEASURED: no real runner clock has been
// observed to produce two distinct activations with the identical
// nanosecond EvidenceAt, and this multiplies by 4 (this file's four
// possible steps) rather than a larger factor specifically to stay well
// clear of uint64 overflow for any EvidenceAt in this project's real
// operating window (SHOWMESH HYPOTHESIS: no such window remotely
// approaches uint64/4 nanoseconds from the Unix epoch).
func activationRevision(act cueactivation.Activation, step int) pkgaudio.Revision {
	return pkgaudio.Revision(uint64(act.EvidenceAt.UnixNano())*4 + uint64(step))
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

// activateAudio is TRACK-H-cues-and-playlists.md section H4's audio (and,
// transitively, LTC) requirement: select the Cue's resolved audio asset,
// apply it to this node's one show session (Apply, Prepare, Start), and
// align playback to act.PositionMS via Seek.
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
func activateAudio(ctx context.Context, mgr *audio.Manager, assetDir string, act cueactivation.Activation, out cuecatalog.AudioOutput, ltc *cuecatalog.LTCOutput) error {
	if out.Filename == "" {
		return fmt.Errorf("cue.activate: audio output for Cue %q resolves asset %q to no runtime filename (no matching asset uploaded); refusing to open anything by asset id (ADR-043 decision 6)", act.CueID, out.Asset)
	}

	contentHash := firstAssetHash(out.AssetHashes)
	var sizeBytes int64
	if info, err := os.Stat(filepath.Join(assetDir, out.Filename)); err == nil {
		sizeBytes = info.Size()
	}

	req := pkgaudio.ApplyRequest{
		SourceRole: pkgaudio.SetField(pkgaudio.SourceRoleShow),
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
	if ltc != nil {
		if tc, ok := resolveLTCStartOffsetTimecode(mgr, ltc.StartOffsetMillis); ok {
			req.LTCStartOffset = pkgaudio.SetField(tc)
		}
	}

	id := cueActivationAudioSessionID

	applyOutcome := mgr.Apply(ctx, id, activationInvocation(act, "apply"), activationRevision(act, activationStepApply), req)
	if audioOutcomeFailed(applyOutcome) {
		return fmt.Errorf("cue.activate: audio.session.apply for Cue %q: %s: %s", act.CueID, applyOutcome.Outcome, applyOutcome.Reason)
	}

	prepOutcome := mgr.Prepare(ctx, id, activationInvocation(act, "prepare"), activationRevision(act, activationStepPrepare))
	if audioOutcomeFailed(prepOutcome) {
		return fmt.Errorf("cue.activate: audio.session.prepare for Cue %q: %s: %s", act.CueID, prepOutcome.Outcome, prepOutcome.Reason)
	}

	startOutcome := mgr.Start(ctx, id, activationInvocation(act, "start"), activationRevision(act, activationStepStart))
	if audioOutcomeFailed(startOutcome) {
		return fmt.Errorf("cue.activate: audio.session.start for Cue %q: %s: %s", act.CueID, startOutcome.Outcome, startOutcome.Reason)
	}

	seekOutcome := mgr.Seek(ctx, id, activationInvocation(act, "seek"), activationRevision(act, activationStepSeek), time.Duration(act.PositionMS)*time.Millisecond)
	if audioOutcomeFailed(seekOutcome) {
		return fmt.Errorf("cue.activate: audio.session.seek for Cue %q to position %dms: %s: %s", act.CueID, act.PositionMS, seekOutcome.Outcome, seekOutcome.Reason)
	}
	return nil
}
