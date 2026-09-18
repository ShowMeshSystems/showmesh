package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// This file covers ADR-051's node-side start path (multisynccueaudio.go)
// against a real *audio.Manager, a real *heldcatalog.FileStore, and a
// scriptable media clock, matching cueactivationaudio_test.go's own
// standing convention of proving behavior against real collaborators
// rather than mocks of them.

// scriptableClockSource is a media clock a test can advance independently
// of the Manager's own wall clock, mirroring internal/agent/audio's own
// (unexported, package-private) fakeClockSource one package over: this
// package cannot import that type, so it is reproduced here rather than
// exported solely for a test in a different package to reuse.
type scriptableClockSource struct {
	mu       sync.Mutex
	state    agentclock.State
	media    time.Time
	nowCalls int
}

func newScriptableClockSource(start time.Time) *scriptableClockSource {
	return &scriptableClockSource{state: agentclock.StateLocked, media: start}
}

func (f *scriptableClockSource) Poll(context.Context) agentclock.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return agentclock.Status{State: f.state, Timescale: agentclock.TimescalePTP}
}

func (f *scriptableClockSource) Now(context.Context) agentclock.MediaTime {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nowCalls++
	return agentclock.MediaTime{Time: f.media, Valid: true}
}

func (f *scriptableClockSource) reads() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nowCalls
}

// waitForReads blocks until this clock has been read at least want times,
// so a test only advances the clock once the code under test has already
// sampled it, matching internal/agent/audio's own fakeClockSource.
// waitForReads exactly, one package over.
func (f *scriptableClockSource) waitForReads(t *testing.T, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for f.reads() < want {
		if time.Now().After(deadline) {
			t.Fatalf("media clock was read %d times, waiting for %d", f.reads(), want)
		}
		time.Sleep(time.Millisecond)
	}
}

func (f *scriptableClockSource) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.media = f.media.Add(d)
}

// newTriggerTestCatalog saves a single-entry held catalog naming filename
// as cueID's one trigger, with an audio output at startOffsetMillis.
func newTriggerTestCatalog(t *testing.T, store *heldcatalog.FileStore, cueID, filename, assetID, wavFilename, hash string, startOffsetMillis int) {
	t.Helper()
	entry := cuecatalog.Entry{
		CueID: cueID, CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			Audio: &cuecatalog.AudioOutput{
				Asset: assetID, Filename: wavFilename, AssetHashes: []string{hash},
				StartOffsetMillis: startOffsetMillis,
			},
		},
		Triggers: []string{filename},
	}
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})
}

// resetTriggerRegistry installs a fresh cueActivationTriggerRegistry
// (package-level, audiostarttrigger.go) for the duration of one test, and
// restores it to nil afterward so tests never leak evidence into each
// other through this package-level var.
func resetTriggerRegistry(t *testing.T) *audioStartTriggerRegistry {
	t.Helper()
	reg := newAudioStartTriggerRegistry()
	cueActivationTriggerRegistry = reg
	t.Cleanup(func() { cueActivationTriggerRegistry = nil })
	return reg
}

// TestMultiSyncCueAudioArmedStartsAtArrivalPlusLead proves ADR-051
// decision 1's armed path: a Cue already prepared ahead of time under the
// prepare-ahead staging session starts on the sequence's own START
// packet, at arrival plus the configured lead, at the Cue's own
// startOffsetMillis, presented on the SAME engine call
// ([Manager.PromoteAtPosition] reusing the staged handle, never a second
// decode).
func TestMultiSyncCueAudioArmedStartsAtArrivalPlusLead(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, fake := newTestAudioManager(t, dir, clk)
	media := newScriptableClockSource(time.Unix(5_000_000_000, 0))
	mgr.SetClockSource(media)
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
		MultisyncStartLeadMs: 100,
	})
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-armed", "wake-up.fseq", "cue-song-asset", "cue-song.wav", hash, 2500)

	// Arm: stage the exact audio under the prepare-ahead staging session,
	// exactly as the coordinator's own prepare-ahead dispatch does.
	stagedRef := pkgaudio.MediaRef{AssetID: "cue-song-asset", ContentHash: hash, RuntimeFilename: "cue-song.wav"}
	stagingID := pkgaudio.SessionID(cueactivation.PrepareStagingSessionID)
	if r := mgr.Apply(context.Background(), stagingID, "stage-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(stagedRef)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("staging apply refused: %+v", r)
	}
	if r := mgr.Prepare(context.Background(), stagingID, "stage-prepare", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("staging prepare refused: %+v", r)
	}
	stagedHandle, ok := fake.LastLoadedHandle()
	if !ok {
		t.Fatal("no Load was recorded after staging Apply+Prepare; test setup is broken")
	}

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clk.now)
	timeline := multisync.NewTimeline(clk.now, multisync.Config{})
	trigger.SetSources(catalogStore, mgr, dir, timeline)

	before := media.reads()
	done := make(chan struct{})
	go func() {
		defer close(done)
		trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
			Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence,
			Filename: "wake-up.fseq",
		})
	}()
	// One read for arrival (HandleSequencePacket, before any other work)
	// plus one for resolveScheduleLocked's own mediaNow -- see that
	// function's own doc comment.
	media.waitForReads(t, before+2)
	media.advance(200 * time.Millisecond) // past the 100ms configured lead

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HandleSequencePacket never returned after the media clock reached T0")
	}

	gotHandle, ok := fake.LastLoadedHandle()
	if !ok {
		t.Fatal("no Load was ever recorded")
	}
	if gotHandle != stagedHandle {
		t.Fatalf("last loaded handle = %q, want it unchanged at the staging handle %q: a second Load ran, meaning the staged handle was not actually reused", gotHandle, stagedHandle)
	}

	snaps := mgr.Snapshot(context.Background())
	var got *audio.SessionSnapshot
	for i := range snaps {
		if snaps[i].ID == cueActivationAudioSessionID {
			got = &snaps[i]
		}
	}
	if got == nil {
		t.Fatalf("no cue audio session snapshot found: %+v", snaps)
	}
	if got.State != pkgaudio.StatePlaying {
		t.Fatalf("session state = %s, want playing", got.State)
	}
	if got.PositionKnown && got.Position != 2500*time.Millisecond {
		t.Fatalf("session position = %v, want 2.5s (the Cue's startOffsetMillis)", got.Position)
	}

	rec, ok := cueActivationTriggerRegistry.get(cueActivationAudioSessionID)
	if !ok {
		t.Fatal("no start-trigger evidence recorded")
	}
	if rec.Trigger != pkgaudio.StartTriggerMultiSync {
		t.Fatalf("recorded trigger = %q, want %q", rec.Trigger, pkgaudio.StartTriggerMultiSync)
	}
	if rec.SequenceFilename != "wake-up.fseq" {
		t.Fatalf("recorded sequence filename = %q, want wake-up.fseq", rec.SequenceFilename)
	}
	if rec.LeadMs != 100 {
		t.Fatalf("recorded lead = %d, want 100", rec.LeadMs)
	}
	if rec.PreparedLate {
		t.Fatal("recorded preparedLate = true, want false: this Cue was armed ahead of time")
	}
}

// TestMultiSyncCueAudioColdCuePreparesOnOpenStartsOnStartAndReportsLate
// proves ADR-051 decision 3's cold-start path: a Cue never armed ahead of
// time is applied and prepared on its sequence's own OPEN packet, starts
// on START, and reports preparedLate.
func TestMultiSyncCueAudioColdCuePreparesOnOpenStartsOnStartAndReportsLate(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
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
	newTriggerTestCatalog(t, catalogStore, "cue-cold", "kpop-audio.fseq", "cue-song-asset", "cue-song.wav", hash, 0)

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clk.now)
	timeline := multisync.NewTimeline(clk.now, multisync.Config{})
	trigger.SetSources(catalogStore, mgr, dir, timeline)

	// OPEN: nothing was armed, so this must apply and prepare on the cue
	// session directly. No clock scheduling is involved (Prepare never
	// consults a media clock), so this call returns synchronously.
	trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
		Action: multisync.SyncActionOpen, FileType: multisync.SyncFileTypeSequence,
		Filename: "kpop-audio.fseq",
	})

	identity, ok := mgr.LoadedMediaIdentity(cueActivationAudioSessionID)
	if !ok {
		t.Fatal("cue session holds no loaded handle after OPEN; the cold prepare did not run")
	}
	wantIdentity := audio.TargetMediaIdentity(pkgaudio.MediaRef{AssetID: "cue-song-asset", ContentHash: hash})
	if identity != wantIdentity {
		t.Fatalf("loaded identity after OPEN = %q, want %q", identity, wantIdentity)
	}

	before := media.reads()
	done := make(chan struct{})
	go func() {
		defer close(done)
		trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
			Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence,
			Filename: "kpop-audio.fseq",
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
		t.Fatalf("session snapshots = %+v, want the cue session playing", snaps)
	}

	rec, ok := cueActivationTriggerRegistry.get(cueActivationAudioSessionID)
	if !ok {
		t.Fatal("no start-trigger evidence recorded")
	}
	if !rec.PreparedLate {
		t.Fatal("recorded preparedLate = false, want true: this Cue was never armed ahead of time")
	}
	if rec.Trigger != pkgaudio.StartTriggerMultiSync {
		t.Fatalf("recorded trigger = %q, want %q", rec.Trigger, pkgaudio.StartTriggerMultiSync)
	}
}

// TestMultiSyncCueAudioMediaPacketsIgnored proves ADR-051 decision 1's
// "Media START packets are ignored": a packet whose FileType is Media
// never starts anything, even when its Filename coincidentally matches a
// registered trigger.
func TestMultiSyncCueAudioMediaPacketsIgnored(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clk)
	mgr.SetClockSource(newScriptableClockSource(time.Unix(5_000_000_000, 0)))
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-media", "wake-up.fseq", "cue-song-asset", "cue-song.wav", hash, 0)

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clk.now)
	trigger.SetSources(catalogStore, mgr, dir, multisync.NewTimeline(clk.now, multisync.Config{}))

	trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
		Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeMedia,
		Filename: "wake-up.fseq",
	})

	if snaps := mgr.Snapshot(context.Background()); len(snaps) != 0 {
		t.Fatalf("session snapshots = %+v, want none: a Media packet must never start a Cue's audio", snaps)
	}
}

// TestMultiSyncCueAudioUnknownFilenameIgnored proves a sequence filename
// not named as any Cue's trigger is silently ignored.
func TestMultiSyncCueAudioUnknownFilenameIgnored(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clk)
	mgr.SetClockSource(newScriptableClockSource(time.Unix(5_000_000_000, 0)))
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-known", "wake-up.fseq", "cue-song-asset", "cue-song.wav", hash, 0)

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clk.now)
	trigger.SetSources(catalogStore, mgr, dir, multisync.NewTimeline(clk.now, multisync.Config{}))

	trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
		Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence,
		Filename: "unrelated-sequence.fseq",
	})

	if snaps := mgr.Snapshot(context.Background()); len(snaps) != 0 {
		t.Fatalf("session snapshots = %+v, want none: an unknown filename must never start a Cue's audio", snaps)
	}
}

// TestMultiSyncCueAudioStopStopsAfterGrace proves ADR-051's own STOP
// grace: the cue session keeps playing until five sequence frames have
// elapsed, then stops.
func TestMultiSyncCueAudioStopStopsAfterGrace(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clk)
	mgr.SetClockSource(newScriptableClockSource(time.Unix(5_000_000_000, 0)))
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-stop", "wake-up.fseq", "cue-song-asset", "cue-song.wav", hash, 0)

	// Play the cue session directly, bypassing the START path entirely --
	// this test is only about STOP's own grace behavior.
	ref := pkgaudio.MediaRef{AssetID: "cue-song-asset", ContentHash: hash, RuntimeFilename: "cue-song.wav"}
	if r := mgr.Apply(context.Background(), cueActivationAudioSessionID, "apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)}); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("apply refused: %+v", r)
	}
	if r := mgr.Start(context.Background(), cueActivationAudioSessionID, "start", 2); r.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("start = %+v, want started", r)
	}

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clk.now)
	// A 10ms known step time makes the 5-frame grace 50ms -- fast enough
	// for a unit test's real-time sleeps without relying on the 250ms
	// unknown-step fallback.
	timeline := multisync.NewTimeline(clk.now, multisync.Config{})
	timeline.SetStepTime(10 * time.Millisecond)
	trigger.SetSources(catalogStore, mgr, dir, timeline)

	trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
		Action: multisync.SyncActionStop, FileType: multisync.SyncFileTypeSequence,
		Filename: "wake-up.fseq",
	})

	time.Sleep(20 * time.Millisecond)
	if snaps := mgr.Snapshot(context.Background()); len(snaps) != 1 || snaps[0].State != pkgaudio.StatePlaying {
		t.Fatalf("session state before the grace elapsed = %+v, want still playing", snaps)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		snaps := mgr.Snapshot(context.Background())
		if len(snaps) == 1 && snaps[0].State != pkgaudio.StatePlaying {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session state after the grace = %+v, want stopped", snaps)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// fixedFailedMultiSyncClockSource is an [audio.ClockSource] whose provider
// reports [agentclock.StateFailed], mirroring cueactivationaudio_test.go's
// own fixedFailedClockSource one package call site over.
type fixedFailedMultiSyncClockSource struct{}

func (fixedFailedMultiSyncClockSource) Poll(context.Context) agentclock.Status {
	return agentclock.Status{State: agentclock.StateFailed, Reason: "read-only management socket"}
}

func (fixedFailedMultiSyncClockSource) Now(context.Context) agentclock.MediaTime {
	return agentclock.MediaTime{Valid: false, Reason: "no clock reading while the provider has failed"}
}

// TestMultiSyncCueAudioUnlockedClockStartsOnArrivalWithReason proves
// ADR-051 decision 1's own fallback: "A node whose clock provider is not
// locked starts on arrival and says so, as today" -- the node still
// starts the Cue's audio (never silent), immediately rather than waiting
// out the lead, and logs why.
func TestMultiSyncCueAudioUnlockedClockStartsOnArrivalWithReason(t *testing.T) {
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 9, 17, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clk)
	mgr.SetClockSource(fixedFailedMultiSyncClockSource{})
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
		MultisyncStartLeadMs: 100,
	})
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-unlocked", "wake-up.fseq", "cue-song-asset", "cue-song.wav", hash, 1000)

	logger, buf := capturingLogger()
	trigger := newMultiSyncCueAudioTrigger(logger, clk.now)
	trigger.SetSources(catalogStore, mgr, dir, multisync.NewTimeline(clk.now, multisync.Config{}))

	done := make(chan struct{})
	go func() {
		defer close(done)
		trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
			Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence,
			Filename: "wake-up.fseq",
		})
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("HandleSequencePacket never returned: an unlocked clock must start on arrival immediately, never wait")
	}

	snaps := mgr.Snapshot(context.Background())
	var got *audio.SessionSnapshot
	for i := range snaps {
		if snaps[i].ID == cueActivationAudioSessionID {
			got = &snaps[i]
		}
	}
	if got == nil || got.State != pkgaudio.StatePlaying {
		t.Fatalf("session snapshots = %+v, want the cue session playing", snaps)
	}
	if got.PositionKnown && got.Position != 1000*time.Millisecond {
		t.Fatalf("session position = %v, want 1s (the Cue's startOffsetMillis)", got.Position)
	}

	rec, ok := cueActivationTriggerRegistry.get(cueActivationAudioSessionID)
	if !ok || rec.Trigger != pkgaudio.StartTriggerMultiSync {
		t.Fatalf("recorded trigger = %+v, want a multisync record", rec)
	}

	if !strings.Contains(buf.String(), pkgaudio.ReasonScheduledStartIgnored) || !strings.Contains(buf.String(), string(agentclock.StateFailed)) {
		t.Fatalf("log output = %q, want it to name %q and the clock provider's %q state", buf.String(), pkgaudio.ReasonScheduledStartIgnored, agentclock.StateFailed)
	}
}
