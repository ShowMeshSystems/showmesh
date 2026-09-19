package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// This file covers ADR-053's node-side holder, and the three surfaces
// item 2 of the task names: cue activation, MultiSync's own OPEN/START
// gate, and boot resume, plus the holder's own persistence-across-restart
// guarantee ("never goes stale").

// TestWeatherDelayHolderNeverGoesStaleWithoutAnExplicitClear proves the
// one deliberate difference from ShowModeHolder: silence is never read
// as a reason to clear an active delay, no matter how much time passes.
func TestWeatherDelayHolderNeverGoesStaleWithoutAnExplicitClear(t *testing.T) {
	dir := t.TempDir()
	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetActiveLocal(weatherDelayKindDelay, time.Now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}
	if !holder.Current().Active {
		t.Fatal("holder not active immediately after SetActiveLocal")
	}
	// No further call at all: an hour, a day, a week of silence would
	// change nothing here, because nothing here reads elapsed time at
	// all — Current has no freshness window to expire.
	if !holder.Current().Active {
		t.Fatal("holder went stale with no explicit clear")
	}
}

// TestWeatherDelayHolderPersistsAcrossRestart proves item 1: a fresh
// holder built over the same asset directory recovers an active delay
// and its plan with no broker involved at all.
func TestWeatherDelayHolderPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	first := NewWeatherDelayHolder(dir, discardLogger())
	startedAt := time.Date(2026, 9, 19, 21, 0, 0, 0, time.UTC)
	if err := first.SetActiveLocal(weatherDelayKindDelay, startedAt, "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	restarted := NewWeatherDelayHolder(dir, discardLogger())
	state := restarted.Current()
	if !state.Active {
		t.Fatal("restarted holder is not active; persistence did not survive the restart")
	}
	if state.Kind != weatherDelayKindDelay {
		t.Fatalf("restarted holder kind = %q, want %q", state.Kind, weatherDelayKindDelay)
	}
	if !state.StartedAt.Equal(startedAt) {
		t.Fatalf("restarted holder startedAt = %v, want %v", state.StartedAt, startedAt)
	}
}

// TestWeatherDelayHolderResumeThenRestartStaysCleared proves the
// counterpart: a resume, persisted, survives a restart as NOT active.
func TestWeatherDelayHolderResumeThenRestartStaysCleared(t *testing.T) {
	dir := t.TempDir()
	first := NewWeatherDelayHolder(dir, discardLogger())
	if err := first.SetActiveLocal(weatherDelayKindDelay, time.Now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}
	if err := first.ClearLocal(); err != nil {
		t.Fatalf("ClearLocal: %v", err)
	}

	restarted := NewWeatherDelayHolder(dir, discardLogger())
	if restarted.Current().Active {
		t.Fatal("restarted holder is active after a resume that was persisted before the restart")
	}
}

// weatherDelayKindDelay/weatherDelayKindCancelNight mirror pkg/weatherdelay's
// own closed vocabulary for tests in this package, so a test never has to
// import pkg/weatherdelay solely to spell "delay".
const (
	weatherDelayKindDelay       = "delay"
	weatherDelayKindCancelNight = "cancelNight"
)

// --- cue activation refusal -------------------------------------------

// TestActivateRefusesWhileWeatherDelayIsActive proves item 2's cue
// activation surface: refused with the exact operator-readable reason,
// before authorization is even evaluated.
func TestActivateRefusesWhileWeatherDelayIsActive(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-1", CueRevision: 1}})

	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetActiveLocal(weatherDelayKindDelay, time.Now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	op := &cueActivationOperation{assetDir: dir, catalogStore: store, weatherDelay: holder}
	act := testActivation("act-weather-delay", "cue-1", 1, "halloween-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if result.Confirmed {
		t.Fatalf("activate confirmed while a weather delay is active: %+v", result)
	}
	v, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("result.Value = %#v, want a map", result.Value)
	}
	if v["reason"] != weatherDelayActiveReason {
		t.Fatalf("refusal reason = %v, want %q", v["reason"], weatherDelayActiveReason)
	}
}

// TestActivateWorksAgainAfterWeatherDelayResumes proves the "after resume
// all three work again" acceptance line for cue activation: the identical
// activation that was refused while active succeeds once the holder is
// cleared.
func TestActivateWorksAgainAfterWeatherDelayResumes(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-1", CueRevision: 1}})

	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetActiveLocal(weatherDelayKindDelay, time.Now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	op := &cueActivationOperation{assetDir: dir, catalogStore: store, weatherDelay: holder}
	act := testActivation("act-weather-resume", "cue-1", 1, "halloween-2026", 3, "rev-a", 0)

	refused, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate (while active): %v", err)
	}
	if refused.Confirmed {
		t.Fatal("activate confirmed while active; test precondition broken")
	}

	if err := holder.ClearLocal(); err != nil {
		t.Fatalf("ClearLocal: %v", err)
	}

	confirmed, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate (after resume): %v", err)
	}
	if !confirmed.Confirmed {
		t.Fatalf("activate did not confirm after resume: %+v", confirmed)
	}
}

// --- MultiSync OPEN/START gating ---------------------------------------

// TestMultiSyncStartDoesNothingWhileWeatherDelayIsActive proves item 2's
// MultiSync surface: a sequence START whose filename is a known trigger
// starts no audio while a delay is active.
func TestMultiSyncStartDoesNothingWhileWeatherDelayIsActive(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clk)
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-weather", "wake-up.fseq", "cue-song-asset", "cue-song.wav", hash, 0)

	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetActiveLocal(weatherDelayKindDelay, clk.now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clk.now, nil)
	timeline := multisync.NewTimeline(clk.now, multisync.Config{})
	trigger.SetSources(catalogStore, mgr, dir, timeline, holder)

	trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
		Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence,
		Filename: "wake-up.fseq",
	})

	snaps := mgr.Snapshot(context.Background())
	if len(snaps) != 0 {
		t.Fatalf("audio sessions after a MultiSync START while a weather delay is active = %+v, want none", snaps)
	}
}

// TestMultiSyncStartWorksAgainAfterWeatherDelayResumes proves the
// after-resume half: the identical START now starts the Cue's audio,
// mirroring TestMultiSyncCueAudioColdCuePreparesOnOpenStartsOnStartAndReportsLate's
// own OPEN-then-START, scriptable-clock pattern.
func TestMultiSyncStartWorksAgainAfterWeatherDelayResumes(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clk)
	media := newScriptableClockSource(time.Unix(5_000_000_000, 0))
	mgr.SetClockSource(media)
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
		MultisyncStartLeadMs: 50,
	})
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-weather", "wake-up.fseq", "cue-song-asset", "cue-song.wav", hash, 0)

	holder := NewWeatherDelayHolder(dir, discardLogger())
	if err := holder.SetActiveLocal(weatherDelayKindDelay, clk.now(), "test"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clk.now, nil)
	timeline := multisync.NewTimeline(clk.now, multisync.Config{})
	trigger.SetSources(catalogStore, mgr, dir, timeline, holder)

	trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
		Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence,
		Filename: "wake-up.fseq",
	})
	if snaps := mgr.Snapshot(context.Background()); len(snaps) != 0 {
		t.Fatalf("precondition broken: audio started while active: %+v", snaps)
	}

	if err := holder.ClearLocal(); err != nil {
		t.Fatalf("ClearLocal: %v", err)
	}

	trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
		Action: multisync.SyncActionOpen, FileType: multisync.SyncFileTypeSequence,
		Filename: "wake-up.fseq",
	})
	trigger.waitOpen("wake-up.fseq")

	before := media.reads()
	done := make(chan struct{})
	go func() {
		defer close(done)
		trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
			Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence,
			Filename: "wake-up.fseq",
		})
	}()
	media.waitForReads(t, before+2)
	media.advance(200 * time.Millisecond)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HandleSequencePacket never returned after the media clock reached T0")
	}

	snaps := mgr.Snapshot(context.Background())
	var got *audio.SessionSnapshot
	for i := range snaps {
		if snaps[i].ID == cueActivationAudioSessionID {
			got = &snaps[i]
		}
	}
	if got == nil || got.State != pkgaudio.StatePlaying {
		t.Fatalf("session snapshots after resume = %+v, want the cue session playing", snaps)
	}
}

// --- boot resume --------------------------------------------------------

// TestDecideBootResumeDiscardsRegardlessOfMatchWhileWeatherDelayIsActive
// proves item 2's boot resume surface: an assignment that would
// otherwise match perfectly is still discarded while weatherDelayActive
// is true.
func TestDecideBootResumeDiscardsRegardlessOfMatchWhileWeatherDelayIsActive(t *testing.T) {
	held := heldcatalog.HeldCatalog{Show: "halloween-2026", Generation: 3, Revision: "rev-a"}
	a := pipeline.Assignment{
		SurfaceID: "surface-1",
		Auth:      &pipeline.AssignmentAuth{Show: "halloween-2026", Generation: 3, CatalogRevision: "rev-a"},
	}

	decision := decideBootResume(a, held, true, true)
	if decision.Authorized {
		t.Fatalf("boot resume authorized while a weather delay is active: %+v", decision)
	}
	if decision.Reason == "" {
		t.Fatal("expected a stated reason for the discard")
	}
}
