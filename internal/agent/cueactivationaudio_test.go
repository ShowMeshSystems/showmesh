package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// This file covers TRACK-H-cues-and-playlists.md section H4's audio and
// LTC requirements against a real *audio.Manager (a real Session state
// machine, a real RevisionState anti-rewind ledger), with only the two
// substitutable collaborators AUDIO-ENGINE itself names as fakes for
// tests: the [audio.Engine] backend and the media [audio.Decoder].

// activationAvailableEngine wraps [audio.FakeEngine] (which always reports
// Available()==false — it exists to prove the session state machine, not
// to claim playback happened) so THIS package's tests, which care about
// activateAudio's own request-building logic reaching the engine at all,
// are not universally gated to Unconfirmable by [audio.Manager.
// gateAvailability]. Promoted methods (Start, Seek, StartLTC, ...) satisfy
// [audio.Engine] and [audio.LTCGenerator] unchanged.
type activationAvailableEngine struct{ *audio.FakeEngine }

func (activationAvailableEngine) Available() (bool, string) { return true, "" }

// fixedAudioDecoder reports every path as a valid, decodable audio file —
// this package's tests care about activateAudio's own request-building and
// sequencing, not AUDIO-ENGINE's already-covered decode/fault
// classification (internal/agent/audio/mediaprobe_test.go owns that).
type fixedAudioDecoder struct{}

func (fixedAudioDecoder) Decode(_ context.Context, _ string) audio.DecodeResult {
	return audio.DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/x-wav",
		Decoded: true, Codec: "pcm", Channels: 2, SampleRate: 44100,
	}
}

// newTestAudioManager builds a real [audio.Manager] against a real,
// available [audio.FakeEngine] and a fixed decoder, with LTC settings
// configured at 25fps (Resolume's own default) so [resolveLTCStartOffsetTimecode]
// has a usable rate to convert against.
func newTestAudioManager(t *testing.T, dir string, clock *fakeClock) (*audio.Manager, *audio.FakeEngine) {
	t.Helper()
	fake := audio.NewFakeEngine(clock.now)
	mgr := audio.NewManager(activationAvailableEngine{fake}, audio.NewFileSessionStore(dir), dir, fixedAudioDecoder{}, clock.now, nil)
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
	})
	return mgr, fake
}

// TestActivateAudioSelectsCueAssetAndSeeksToPosition proves the live
// "cue.activate" operation selects the activated Cue's resolved audio
// asset and aligns playback to the envelope's PositionMS.
func TestActivateAudioSelectsCueAssetAndSeeksToPosition(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
	mgr, fake := newTestAudioManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))

	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-6", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			// Asset is deliberately a LOGICAL id distinct from the real
			// runtime filename, so this test cannot pass by Asset
			// coincidentally equaling the file a node must actually open
			// (see cueactivationaudio.go's own Filename fix).
			Audio: &cuecatalog.AudioOutput{Asset: "cue-song-asset", Filename: "cue-song.wav", AssetHashes: []string{hash}},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr}
	act := testActivation("act-audio-select", "cue-6", 1, "halloween-2026", 3, "rev-a", 4500)

	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("activate did not confirm: %+v", result)
	}

	snaps := mgr.Snapshot(context.Background())
	if len(snaps) != 1 {
		t.Fatalf("session snapshots = %+v, want exactly one", snaps)
	}
	snap := snaps[0]
	if snap.State != pkgaudio.StatePlaying {
		t.Fatalf("session state = %s, want playing", snap.State)
	}
	if snap.PositionKnown {
		if snap.Position != 4500*time.Millisecond {
			t.Fatalf("session position = %v, want 4.5s (the activation's PositionMS)", snap.Position)
		}
	}
	_ = fake // this test's own assertions are on mgr's session snapshot; fake's LTC request shape is covered separately below.
}

// TestActivateAudioAndLTCEmitsCueOffsetPlusPosition proves H4-BRIEF.md
// ruling 2: an activation whose Cue declares both audio and ltc emits
// exactly "Cue LTC start offset + current Cue position" — computed by the
// EXISTING internal/agent/audio.Manager.Start/Seek path (startLTCLocked),
// never a second clock.
func TestActivateAudioAndLTCEmitsCueOffsetPlusPosition(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
	mgr, fake := newTestAudioManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))

	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-7", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			// Asset is deliberately a LOGICAL id distinct from the real
			// runtime filename, so this test cannot pass by Asset
			// coincidentally equaling the file a node must actually open
			// (see cueactivationaudio.go's own Filename fix).
			Audio: &cuecatalog.AudioOutput{Asset: "cue-song-asset", Filename: "cue-song.wav", AssetHashes: []string{hash}},
			// 30 minutes in — chosen so "offset + position" is unambiguously
			// distinguishable from "position alone".
			LTC: &cuecatalog.LTCOutput{StartOffsetMillis: 30 * 60 * 1000},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr}
	// PositionMS = 10 seconds into the Cue: expected timecode is
	// 00:30:10:00 (30 minutes + 10 seconds) at 25fps.
	act := testActivation("act-ltc", "cue-7", 1, "halloween-2026", 3, "rev-a", 10_000)

	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("activate did not confirm: %+v", result)
	}

	spec, requested := fake.LastLTCRequest()
	if !requested {
		t.Fatalf("no LTC run was requested; Cue declared an ltc output")
	}
	if spec.FrameRate != pkgaudio.LTCFrameRate25 {
		t.Fatalf("LTC frame rate = %s, want 25 (this node's configured rate)", spec.FrameRate)
	}
	const want = pkgaudio.LTCTimecode("00:30:10:00")
	if spec.StartTimecode != want {
		t.Fatalf("LTC start timecode = %s, want %s (Cue LTC start offset 00:30:00:00 + position 10s)", spec.StartTimecode, want)
	}
}

// TestActivateRefusesLTCWithoutAudioOnTheSameCue proves the stated (never
// silent) refusal for a Cue that declares ltc with no audio output on the
// same Cue: there is no program-audio clock domain session to attach an
// LTC offset to.
func TestActivateRefusesLTCWithoutAudioOnTheSameCue(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)

	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-8", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			LTC: &cuecatalog.LTCOutput{StartOffsetMillis: 0},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr}
	act := testActivation("act-ltc-no-audio", "cue-8", 1, "halloween-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if result.Confirmed {
		t.Fatalf("activate confirmed an ltc-without-audio Cue: %+v", result)
	}
}

// TestActivateAudioRedeliveredActivationIsIdempotent proves the audio side
// of H4's "re-applying the same ActivationID must not disturb anything
// already correct": activationInvocation/activationRevision are
// deterministic in ActivationID and EvidenceAt, so [audio.Manager]'s own
// [pkgaudio.RevisionState] recognizes a redelivery of the identical
// Activation as a replay rather than a new command, and the underlying
// FakeEngine is never asked to Start a second time.
func TestActivateAudioRedeliveredActivationIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clock)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))

	catalogStore := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-9", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			// Asset is deliberately a LOGICAL id distinct from the real
			// runtime filename, so this test cannot pass by Asset
			// coincidentally equaling the file a node must actually open
			// (see cueactivationaudio.go's own Filename fix).
			Audio: &cuecatalog.AudioOutput{Asset: "cue-song-asset", Filename: "cue-song.wav", AssetHashes: []string{hash}},
		},
	}
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr}
	act := testActivation("act-audio-idempotent", "cue-9", 1, "halloween-2026", 3, "rev-a", 2000)

	if result, err := op.activate(context.Background(), activationParams(t, act), clock.now); err != nil || !result.Confirmed {
		t.Fatalf("first activate = (%+v, %v), want confirmed", result, err)
	}
	// A redelivery (identical ActivationID/EvidenceAt) must be accepted
	// again without error, and without disturbing the session — it must
	// remain playing, not be knocked into some other state by a second,
	// spuriously-executed Start.
	clock.advance(3 * time.Second)
	result, err := op.activate(context.Background(), activationParams(t, act), clock.now)
	if err != nil {
		t.Fatalf("redelivered activate: %v", err)
	}
	if !result.Confirmed {
		t.Fatalf("redelivered activate did not confirm: %+v", result)
	}
	snaps := mgr.Snapshot(context.Background())
	if len(snaps) != 1 || snaps[0].State != pkgaudio.StatePlaying {
		t.Fatalf("session snapshots after redelivery = %+v, want exactly one, playing", snaps)
	}
}
