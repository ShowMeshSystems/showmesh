package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// slowFirstLoadEngine holds the first Load until release closes, the way a
// slow node's cold prepare does, and counts every Load and Start.
type slowFirstLoadEngine struct {
	activationAvailableEngine
	mu          sync.Mutex
	loads       int
	starts      int
	loadStarted chan struct{}
	release     chan struct{}
}

func (e *slowFirstLoadEngine) Load(ctx context.Context, handle audio.EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (audio.EngineObservation, error) {
	e.mu.Lock()
	e.loads++
	first := e.loads == 1
	e.mu.Unlock()
	if first {
		close(e.loadStarted)
		<-e.release
	}
	return e.activationAvailableEngine.Load(ctx, handle, media, duration)
}

func (e *slowFirstLoadEngine) Start(ctx context.Context, handle audio.EngineHandle, position time.Duration) (audio.EngineObservation, error) {
	e.mu.Lock()
	e.starts++
	e.mu.Unlock()
	return e.activationAvailableEngine.Start(ctx, handle, position)
}

func (e *slowFirstLoadEngine) counts() (loads, starts int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loads, e.starts
}

type startRaceRig struct {
	engine  *slowFirstLoadEngine
	mgr     *audio.Manager
	trigger *multiSyncCueAudioTrigger
	op      *cueActivationOperation
	clock   *fakeClock
}

// newStartRaceRig holds two cold cues: cue-a, triggered by a.fseq, and cue-b.
func newStartRaceRig(t *testing.T) *startRaceRig {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 23, 19, 59, 59, 0, time.UTC)}
	engine := &slowFirstLoadEngine{
		activationAvailableEngine: activationAvailableEngine{audio.NewFakeEngine(clock.now)},
		loadStarted:               make(chan struct{}),
		release:                   make(chan struct{}),
	}
	mgr := audio.NewManager(engine, audio.NewFileSessionStore(dir), dir, fixedAudioDecoder{}, clock.now, nil)
	media := newScriptableClockSource(time.Unix(5_000_000_000, 0))
	media.setStep(time.Millisecond)
	mgr.SetClockSource(media)
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
		MultisyncStartLeadMs: 5,
	})
	resetTriggerRegistry(t)

	hashA := writeAssetFixture(t, dir, "song-a.wav", []byte("pretend this is song a"))
	hashB := writeAssetFixture(t, dir, "song-b.wav", []byte("pretend this is song b"))
	catalogStore := heldcatalog.NewFileStore(dir)
	saveHeld(t, catalogStore, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{
		{
			CueID: "cue-a", CueRevision: 1,
			Outputs:  cuecatalog.Outputs{Audio: &cuecatalog.AudioOutput{Asset: "song-a-asset", Filename: "song-a.wav", AssetHashes: []string{hashA}}},
			Triggers: []string{"a.fseq"},
		},
		{
			CueID: "cue-b", CueRevision: 1,
			Outputs: cuecatalog.Outputs{Audio: &cuecatalog.AudioOutput{Asset: "song-b-asset", Filename: "song-b.wav", AssetHashes: []string{hashB}}},
		},
	})

	trigger := newMultiSyncCueAudioTrigger(discardLogger(), clock.now, nil)
	timeline := multisync.NewTimeline(clock.now, multisync.Config{})
	trigger.SetSources(catalogStore, mgr, dir, timeline, nil)
	return &startRaceRig{
		engine: engine, mgr: mgr, trigger: trigger, clock: clock,
		op: &cueActivationOperation{assetDir: dir, catalogStore: catalogStore, audioMgr: mgr},
	}
}

// startColdSequence delivers a.fseq's START and returns once its cold Load is
// held, with a channel that closes when the START has been handled.
func (r *startRaceRig) startColdSequence(t *testing.T) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
			Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence, Filename: "a.fseq",
		})
	}()
	select {
	case <-r.engine.loadStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the START's cold prepare never reached the engine")
	}
	return done
}

type activationOutcome struct {
	result OperationResult
	err    error
}

func (r *startRaceRig) activateAsync(t *testing.T, activationID, cueID string) <-chan activationOutcome {
	t.Helper()
	params := activationParams(t, testActivation(activationID, cueID, 1, "halloween-2026", 3, "rev-a", 0))
	out := make(chan activationOutcome, 1)
	go func() {
		res, err := r.op.activate(context.Background(), params, r.clock.now)
		out <- activationOutcome{result: res, err: err}
	}()
	return out
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatalf("%s never finished", what)
	}
}

func waitActivation(t *testing.T, ch <-chan activationOutcome) activationOutcome {
	t.Helper()
	select {
	case out := <-ch:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("cue.activate never finished")
		return activationOutcome{}
	}
}

// TestCueActivationDuringMultiSyncColdStartAdoptsThatStart covers a slow node
// where cue.activate for the same song arrives while the MultiSync START is
// still cold-loading it: the song loads and starts once, and the activation
// reports the MultiSync start rather than a failure.
func TestCueActivationDuringMultiSyncColdStartAdoptsThatStart(t *testing.T) {
	r := newStartRaceRig(t)

	startDone := r.startColdSequence(t)
	activation := r.activateAsync(t, "act-during-start", "cue-a")
	time.Sleep(50 * time.Millisecond)
	close(r.engine.release)

	waitDone(t, startDone, "the MultiSync START")
	out := waitActivation(t, activation)
	if loads, starts := r.engine.counts(); loads != 1 || starts != 1 {
		t.Errorf("engine loads = %d, starts = %d, want exactly one of each", loads, starts)
	}
	if out.err != nil {
		t.Fatalf("activate: %v", out.err)
	}
	if !out.result.Confirmed {
		t.Fatalf("activate did not confirm: %+v", out.result)
	}
	if value, ok := out.result.Value.(map[string]any); !ok || value[pkgaudio.ResultStartTrigger] != pkgaudio.StartTriggerMultiSync {
		t.Fatalf("result value = %#v, want the MultiSync start reported", out.result.Value)
	}
	if rec, ok := cueActivationTriggerRegistry.get(cueActivationAudioSessionID); !ok || rec.Trigger != pkgaudio.StartTriggerMultiSync || rec.CueID != "cue-a" {
		t.Fatalf("start record = %+v, want the MultiSync start of cue-a", rec)
	}
	cueSessionPosition(t, r.mgr)
}

// TestMultiSyncStartDuringCueActivationLeavesItAlone covers the opposite
// order: a MultiSync START for the song cue.activate is still loading waits
// for that start and leaves it playing.
func TestMultiSyncStartDuringCueActivationLeavesItAlone(t *testing.T) {
	r := newStartRaceRig(t)

	activation := r.activateAsync(t, "act-first", "cue-a")
	select {
	case <-r.engine.loadStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("the activation's cold prepare never reached the engine")
	}
	startDone := make(chan struct{})
	go func() {
		defer close(startDone)
		r.trigger.HandleSequencePacket(context.Background(), multisync.SyncPacket{
			Action: multisync.SyncActionStart, FileType: multisync.SyncFileTypeSequence, Filename: "a.fseq",
		})
	}()
	time.Sleep(50 * time.Millisecond)
	close(r.engine.release)

	out := waitActivation(t, activation)
	waitDone(t, startDone, "the MultiSync START")
	if out.err != nil || !out.result.Confirmed {
		t.Fatalf("activate = %+v, %v; want confirmed", out.result, out.err)
	}
	if loads, starts := r.engine.counts(); loads != 1 || starts != 1 {
		t.Fatalf("engine loads = %d, starts = %d, want exactly one of each", loads, starts)
	}
	if rec, _ := cueActivationTriggerRegistry.get(cueActivationAudioSessionID); rec.Trigger != pkgaudio.StartTriggerCoordinator {
		t.Fatalf("start record = %+v, want the coordinator start left in place", rec)
	}
}

// TestCueActivationOfOtherMediaDuringMultiSyncStartStillPreempts keeps a
// different song's activation from waiting on, or deferring to, a MultiSync
// start still loading another song.
func TestCueActivationOfOtherMediaDuringMultiSyncStartStillPreempts(t *testing.T) {
	r := newStartRaceRig(t)

	startDone := r.startColdSequence(t)
	activation := r.activateAsync(t, "act-other-song", "cue-b")
	time.Sleep(50 * time.Millisecond)
	close(r.engine.release)

	waitDone(t, startDone, "the MultiSync START")
	out := waitActivation(t, activation)
	if out.err != nil || !out.result.Confirmed {
		t.Fatalf("activate = %+v, %v; want confirmed", out.result, out.err)
	}
	if value, ok := out.result.Value.(map[string]any); ok && value[pkgaudio.ResultStartTrigger] == pkgaudio.StartTriggerMultiSync {
		t.Fatalf("result value = %#v, want the activation's own start, not the MultiSync one", value)
	}
	if rec, _ := cueActivationTriggerRegistry.get(cueActivationAudioSessionID); rec.CueID != "cue-b" {
		t.Fatalf("start record = %+v, want cue-b's activation", rec)
	}
	identity, ok := r.mgr.LoadedMediaIdentity(cueActivationAudioSessionID)
	if want := r.trigger.targetIdentity(cueAudioTriggerEntry{CueID: "cue-b", Audio: cuecatalog.AudioOutput{Asset: "song-b-asset", AssetHashes: activationHashes(t, r)}}); !ok || identity != want {
		t.Fatalf("cue session holds %q, want song b (%q)", identity, want)
	}
	cueSessionPosition(t, r.mgr)
}

func activationHashes(t *testing.T, r *startRaceRig) []string {
	t.Helper()
	rec, ok, err := r.op.catalogStore.Load()
	if err != nil || !ok {
		t.Fatalf("load held catalog: %v", err)
	}
	for _, e := range rec.Entries {
		if e.CueID == "cue-b" {
			return e.Outputs.Audio.AssetHashes
		}
	}
	t.Fatal("cue-b not in the held catalog")
	return nil
}
