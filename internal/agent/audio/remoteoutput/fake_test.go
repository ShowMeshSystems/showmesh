package remoteoutput

import (
	"context"
	"errors"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

func fixedClock(t time.Time) func() time.Time { return func() time.Time { return t } }

func fullMixCapabilities() Capabilities {
	return Capabilities{
		SynchronizedMedia:           SupportSupported,
		PCMStream:                   SupportUnsupported,
		AdvanceProvisioning:         SupportSupported,
		ProvisioningAcknowledgement: SupportSupported,
		ReadinessObservation:        SupportUnsupported,
		Mixing:                      SupportSupported,
		Announcements:               SupportSupported,
		Ducking:                     SupportSupported,
		GainFades:                   SupportSupported,
		Looping:                     SupportSupported,
		Playlists:                   SupportSupported,
		Sequential:                  SupportSupported,
		Gapless:                     SupportSupported,
		Crossfade:                   SupportSupported,
		Seeking:                     SupportSupported,
		PositionReporting:           SupportSupported,
		AcceptedFormats:             map[string]Support{"mp3": SupportSupported, "wav": SupportSupported},
	}
}

// oneSessionOnlyCapabilities is the deliberately weaker profile the C8
// spec names: a destination that can only reproduce one media item at a
// time, with most of the richer behavior unknown or unsupported.
func oneSessionOnlyCapabilities() Capabilities {
	return Capabilities{
		SynchronizedMedia:           SupportSupported,
		PCMStream:                   SupportUnsupported,
		AdvanceProvisioning:         SupportSupported,
		ProvisioningAcknowledgement: SupportUnknown,
		ReadinessObservation:        SupportUnsupported,
		Mixing:                      SupportUnsupported,
		Announcements:               SupportUnknown,
		Ducking:                     SupportUnsupported,
		GainFades:                   SupportUnknown,
		Looping:                     SupportUnknown,
		Playlists:                   SupportUnsupported,
		Sequential:                  SupportUnknown,
		Gapless:                     SupportUnsupported,
		Crossfade:                   SupportUnsupported,
		Seeking:                     SupportUnknown,
		PositionReporting:           SupportUnsupported,
		AcceptedFormats:             map[string]Support{"mp3": SupportSupported},
	}
}

func assetOne() pkgaudio.MediaRef {
	return pkgaudio.MediaRef{AssetID: "asset-1", ContentHash: "hash-1", RuntimeFilename: "cue.mp3"}
}

// TestProvisioningNeverTriggeredByPlayout drives a full media
// select/start/pause/resume/seek/stop sequence through Apply/Observe and
// asserts Provision was never called. AUDIO-ENGINE section 8.1: start
// arriving must never itself begin a transfer.
func TestProvisioningNeverTriggeredByPlayout(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Unix(0, 0)), fullMixCapabilities())
	ctx := context.Background()
	media := assetOne()

	steps := []State{
		{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Transport: TransportPlaying, Loop: pkgaudio.RepeatNone, Gain: 1},
		{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Transport: TransportPaused, Loop: pkgaudio.RepeatNone, Gain: 1},
		{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Transport: TransportPlaying, Loop: pkgaudio.RepeatNone, Gain: 1},
		{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Transport: TransportPlaying, Position: 5 * time.Second, Seek: durPtr(5 * time.Second), Loop: pkgaudio.RepeatNone, Gain: 1},
		{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Transport: TransportStopped, Loop: pkgaudio.RepeatNone, Gain: 1},
	}
	for i, st := range steps {
		if _, _, err := f.Apply(ctx, pkgaudio.InvocationID("inv"+string(rune('0'+i))), pkgaudio.Revision(i+1), st); err != nil {
			t.Fatalf("Apply step %d: %v", i, err)
		}
	}
	if _, err := f.Observe(ctx); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	for _, entry := range f.CallLog() {
		if entry == "provision" {
			t.Fatal("CallLog contains a provision call after only playout commands were dispatched")
		}
	}
}

func TestProvisionRefusesInvalidTriggerDestinationOrMedia(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	ctx := context.Background()
	dest := Destination{ID: "d1", ConfigRevision: "r1"}

	if _, err := f.Provision(ctx, Trigger("start"), dest, assetOne()); !errors.Is(err, ErrUnknownTrigger) {
		t.Errorf("Provision(start trigger): got %v, want ErrUnknownTrigger", err)
	}
	if _, err := f.Provision(ctx, TriggerAssetIngested, Destination{}, assetOne()); !errors.Is(err, ErrDestinationIncomplete) {
		t.Errorf("Provision(empty destination): got %v, want ErrDestinationIncomplete", err)
	}
	if _, err := f.Provision(ctx, TriggerAssetIngested, dest, pkgaudio.MediaRef{}); !errors.Is(err, pkgaudio.ErrMediaRefIncomplete) {
		t.Errorf("Provision(empty media): got %v, want ErrMediaRefIncomplete", err)
	}
	for _, entry := range f.CallLog() {
		if entry == "provision" {
			t.Fatal("a refused Provision call must not reach the destination's own record")
		}
	}
}

func TestProvisionAcknowledgedIsNeverReady(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	dest := Destination{ID: "d1", ConfigRevision: "r1"}
	rec, err := f.Provision(context.Background(), TriggerAssetIngested, dest, assetOne())
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if rec.State != ProvisioningAcknowledged {
		t.Fatalf("Provision: got state %s, want Acknowledged", rec.State)
	}
	if rec.RemoteMediaID == "" {
		t.Error("Provision acknowledged: got empty RemoteMediaID, want one minted")
	}
	// The vocabulary this package exposes has no member spelling "ready";
	// an acknowledgement is the strongest state a generic adapter with
	// no destination-reported readiness can ever reach.
	if rec.State == ProvisioningState("ready") {
		t.Fatal("provisioning state must never be ready")
	}
}

func TestProvisionNoStatusInterfaceStaysAttempted(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	f.NoStatusInterface = true
	dest := Destination{ID: "d1", ConfigRevision: "r1"}
	ctx := context.Background()

	rec, err := f.Provision(ctx, TriggerAssetIngested, dest, assetOne())
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if rec.State != ProvisioningAttempted {
		t.Fatalf("Provision with no status interface: got state %s, want Attempted", rec.State)
	}

	status, err := f.ProvisioningStatus(ctx, dest, assetOne().ContentHash)
	if err != nil {
		t.Fatalf("ProvisioningStatus: %v", err)
	}
	if status.State != ProvisioningAttempted {
		t.Errorf("ProvisioningStatus with no status interface: got %s, want Attempted (absence of a status API is supported, never invented as Acknowledged)", status.State)
	}
}

func TestProvisionFailedTransfer(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	f.FailTransfer = true
	dest := Destination{ID: "d1", ConfigRevision: "r1"}

	rec, err := f.Provision(context.Background(), TriggerAssetIngested, dest, assetOne())
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if rec.State != ProvisioningFailed || rec.Reason == "" {
		t.Errorf("Provision(FailTransfer): got state=%s reason=%q, want Failed with a reason", rec.State, rec.Reason)
	}
}

func TestProvisionStatusUnattemptedBeforeAnyProvision(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	status, err := f.ProvisioningStatus(context.Background(), Destination{ID: "d1", ConfigRevision: "r1"}, "hash-1")
	if err != nil {
		t.Fatalf("ProvisioningStatus: %v", err)
	}
	if status.State != ProvisioningNotAttempted {
		t.Errorf("ProvisioningStatus before any Provision: got %s, want NotAttempted", status.State)
	}
}

func TestProvisionDuplicateIsIdempotentAndCoherent(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	dest := Destination{ID: "d1", ConfigRevision: "r1"}
	ctx := context.Background()

	first, err := f.Provision(ctx, TriggerAssetIngested, dest, assetOne())
	if err != nil {
		t.Fatalf("first Provision: %v", err)
	}
	second, err := f.Provision(ctx, TriggerRetry, dest, assetOne())
	if err != nil {
		t.Fatalf("duplicate Provision: %v", err)
	}
	if second.RemoteMediaID != first.RemoteMediaID {
		t.Errorf("duplicate provisioning: remote media id changed from %q to %q, want a stable remote identity", first.RemoteMediaID, second.RemoteMediaID)
	}
	if second.State != ProvisioningAcknowledged {
		t.Errorf("duplicate provisioning: got state %s, want Acknowledged", second.State)
	}

	provisionCalls := 0
	for _, entry := range f.CallLog() {
		if entry == "provision" {
			provisionCalls++
		}
	}
	if provisionCalls != 2 {
		t.Errorf("CallLog: got %d provision calls, want 2 (both attempts are recorded, neither silently dropped)", provisionCalls)
	}
}

func TestProvisionSameFilenameReplacedHashKeepsUnambiguousIdentity(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	dest := Destination{ID: "d1", ConfigRevision: "r1"}
	ctx := context.Background()

	oldAsset := pkgaudio.MediaRef{AssetID: "asset-1", ContentHash: "hash-old", RuntimeFilename: "cue.mp3"}
	newAsset := pkgaudio.MediaRef{AssetID: "asset-1", ContentHash: "hash-new", RuntimeFilename: "cue.mp3"}

	if _, err := f.Provision(ctx, TriggerAssetIngested, dest, oldAsset); err != nil {
		t.Fatalf("Provision(old): %v", err)
	}
	if _, err := f.Provision(ctx, TriggerConfigurationChanged, dest, newAsset); err != nil {
		t.Fatalf("Provision(new): %v", err)
	}

	oldRec, ok := f.Evidence().Get(dest, "hash-old")
	if !ok || oldRec.State != ProvisioningAcknowledged {
		t.Errorf("evidence for the replaced hash: got %+v, ok=%v, want it untouched and Acknowledged", oldRec, ok)
	}
	newRec, ok := f.Evidence().Get(dest, "hash-new")
	if !ok || newRec.State != ProvisioningAcknowledged {
		t.Errorf("evidence for the new hash: got %+v, ok=%v, want Acknowledged", newRec, ok)
	}
}

func TestRecordManualVerificationRequiresPinnedDestinationAndHash(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	dest := Destination{ID: "d1", ConfigRevision: "r1"}
	v := ManualVerification{Destination: dest, ContentHash: "h1", Operator: "eric", VerifiedAt: time.Now()}
	if err := f.RecordManualVerification(context.Background(), v); err != nil {
		t.Fatalf("RecordManualVerification: %v", err)
	}
	rec, ok := f.Evidence().Get(dest, "h1")
	if !ok || rec.State != ProvisioningManuallyVerified {
		t.Fatalf("Evidence after manual verification: got %+v, ok=%v, want ManuallyVerified", rec, ok)
	}
}

// TestCapabilityGatingRefusesUnconfirmedBehaviorRegardlessOfProfile
// exercises every independently-declarable behavior against both the
// full-mix and one-session-only capability profiles, proving Supported
// dispatches and both Unsupported and Unknown refuse identically.
func TestCapabilityGatingRefusesUnconfirmedBehaviorRegardlessOfProfile(t *testing.T) {
	media := assetOne()
	playlist := &pkgaudio.PlaylistRef{
		OwnerKind: "night_session", OwnerID: "ns-1",
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
		RequestedTransition: pkgaudio.ItemTransitionGapless,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-1", Index: 0, Media: media},
		},
	}
	crossfadePlaylist := &pkgaudio.PlaylistRef{
		OwnerKind: "night_session", OwnerID: "ns-1",
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
		RequestedTransition: pkgaudio.ItemTransitionCrossfade,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-1", Index: 0, Media: media},
		},
	}
	sequentialPlaylist := &pkgaudio.PlaylistRef{
		OwnerKind: "night_session", OwnerID: "ns-1",
		Repeat: pkgaudio.RepeatNone, Resume: pkgaudio.ResumePolicyRestart,
		RequestedTransition: pkgaudio.ItemTransitionSequential,
		Items: []pkgaudio.PlaylistItem{
			{ItemID: "item-1", Index: 0, Media: media},
		},
	}

	cases := []struct {
		name string
		st   State
	}{
		{"playlist_gapless", State{SourceRole: pkgaudio.SourceRoleShow, Playlist: playlist, Loop: pkgaudio.RepeatNone, Gain: 1}},
		{"playlist_crossfade", State{SourceRole: pkgaudio.SourceRoleShow, Playlist: crossfadePlaylist, Loop: pkgaudio.RepeatNone, Gain: 1}},
		{"playlist_sequential", State{SourceRole: pkgaudio.SourceRoleShow, Playlist: sequentialPlaylist, Loop: pkgaudio.RepeatNone, Gain: 1}},
		{"announcement", State{SourceRole: pkgaudio.SourceRoleAnnouncement, Media: &media, Loop: pkgaudio.RepeatNone, Gain: 1}},
		{"looping", State{SourceRole: pkgaudio.SourceRoleBackground, Media: &media, Loop: pkgaudio.RepeatItem, Gain: 1}},
		{"gain_fade", State{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Loop: pkgaudio.RepeatNone, Gain: 1, Fade: &pkgaudio.Fade{Curve: pkgaudio.FadeCurveLinear, Duration: time.Second, TargetGain: 0.5}}},
		{"ducking", State{SourceRole: pkgaudio.SourceRoleBackground, Media: &media, Loop: pkgaudio.RepeatNone, Gain: 1, Ducking: true}},
		{"mixing", State{SourceRole: pkgaudio.SourceRoleAnnouncement, Media: &media, Loop: pkgaudio.RepeatNone, Gain: 1, Mixing: true}},
		{"seeking", State{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Loop: pkgaudio.RepeatNone, Gain: 1, Seek: durPtr(3 * time.Second)}},
	}

	for _, profile := range []struct {
		name        string
		caps        Capabilities
		wantRefused bool
	}{
		{"full_mix", fullMixCapabilities(), false},
		{"one_session_only", oneSessionOnlyCapabilities(), true},
	} {
		for _, c := range cases {
			t.Run(profile.name+"/"+c.name, func(t *testing.T) {
				f := NewFakeDestination(fixedClock(time.Now()), profile.caps)
				_, obs, err := f.Apply(context.Background(), pkgaudio.InvocationID("inv-"+c.name), 1, c.st)
				if err != nil {
					t.Fatalf("Apply: %v", err)
				}
				refused := obs.Result.Outcome == pkgaudio.OutcomeRefused
				if refused != profile.wantRefused {
					t.Errorf("Apply(%s) against %s: got Outcome=%s, want refused=%v", c.name, profile.name, obs.Result.Outcome, profile.wantRefused)
				}
				if refused && obs.Result.Reason == "" {
					t.Error("a Refused outcome must carry a non-empty reason")
				}
			})
		}
	}
}

func TestObservePositionReportingUnsupportedNeverAttributesAPosition(t *testing.T) {
	caps := fullMixCapabilities()
	caps.PositionReporting = SupportUnsupported
	f := NewFakeDestination(fixedClock(time.Now()), caps)
	media := assetOne()
	st := State{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Transport: TransportPlaying, Position: 42 * time.Second, Loop: pkgaudio.RepeatNone, Gain: 1}
	if _, _, err := f.Apply(context.Background(), "inv-1", 1, st); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	obs, err := f.Observe(context.Background())
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if obs.State.Position != 0 {
		t.Errorf("Observe with PositionReporting unsupported: got Position=%v, want 0", obs.State.Position)
	}
	if obs.Result.Outcome != pkgaudio.OutcomeUnconfirmable {
		t.Errorf("Observe with PositionReporting unsupported: got Outcome=%s, want Unconfirmable", obs.Result.Outcome)
	}
}

// TestDelayedDuplicateAndOutOfOrderCommandsCannotOverwriteNewerState is
// the C8 seam's ruling 7 case: a stale command must never move desired
// state backward, a duplicate invocation must replay its own original
// decision, and an out-of-order lower revision must be refused.
func TestDelayedDuplicateAndOutOfOrderCommandsCannotOverwriteNewerState(t *testing.T) {
	f := NewFakeDestination(fixedClock(time.Now()), fullMixCapabilities())
	media := assetOne()
	ctx := context.Background()
	base := State{SourceRole: pkgaudio.SourceRoleShow, Media: &media, Loop: pkgaudio.RepeatNone, Gain: 1}

	first := base
	first.Transport = TransportPlaying
	decision, _, err := f.Apply(ctx, "inv-1", 5, first)
	if err != nil || !decision.Accepted {
		t.Fatalf("Apply(revision 5): decision=%+v err=%v, want accepted", decision, err)
	}

	newer := base
	newer.Transport = TransportPaused
	decision, _, err = f.Apply(ctx, "inv-2", 10, newer)
	if err != nil || !decision.Accepted {
		t.Fatalf("Apply(revision 10): decision=%+v err=%v, want accepted", decision, err)
	}

	// A delayed command carrying an older revision arrives after the
	// newer one: it must be refused and must not move lastApplied back.
	delayed := base
	delayed.Transport = TransportPlaying
	decision, obs, err := f.Apply(ctx, "inv-3", 7, delayed)
	if err != nil {
		t.Fatalf("Apply(delayed revision 7): %v", err)
	}
	if decision.Accepted {
		t.Error("Apply(delayed revision 7 after 10 was accepted): got Accepted=true, want false")
	}
	if obs.State.Transport != TransportPaused {
		t.Errorf("state after a refused delayed command: got Transport=%s, want it to stay Paused (the revision-10 state)", obs.State.Transport)
	}

	// A duplicate of the already-accepted invocation replays the same
	// decision rather than re-evaluating.
	replay, _, err := f.Apply(ctx, "inv-2", 10, newer)
	if err != nil {
		t.Fatalf("Apply(duplicate inv-2): %v", err)
	}
	if !replay.Accepted || replay.Revision != 10 {
		t.Errorf("replayed duplicate invocation: got %+v, want the original accepted revision-10 decision", replay)
	}

	// A duplicate invocation replayed with a different requested revision
	// is refused rather than silently accepted at the new value.
	mismatched, _, err := f.Apply(ctx, "inv-2", 99, newer)
	if err != nil {
		t.Fatalf("Apply(inv-2 with mismatched revision): %v", err)
	}
	if mismatched.Accepted {
		t.Error("Apply(same invocation, different revision): got Accepted=true, want false")
	}
	if mismatched.Result == nil || mismatched.Result.Reason != pkgaudio.ReasonInvocationRevisionMismatch {
		t.Errorf("mismatched replay result: got %+v, want ReasonInvocationRevisionMismatch", mismatched.Result)
	}
}

func durPtr(d time.Duration) *time.Duration { return &d }
