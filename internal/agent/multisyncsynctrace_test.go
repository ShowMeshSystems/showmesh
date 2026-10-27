package agent

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

func newSyncTraceTestTrigger(t *testing.T, clockState agentclock.State) (*multiSyncCueAudioTrigger, *audio.Manager, *scriptableClockSource, *fakeClock, func() string) {
	t.Helper()
	dir := t.TempDir()
	clk := &fakeClock{t: time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newTestAudioManager(t, dir, clk)
	media := newScriptableClockSource(time.Unix(5_000_000_000, 0))
	media.state = clockState
	mgr.SetClockSource(media)
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
		MultisyncStartLeadMs: 100,
	})
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-trace", "trace.fseq", "cue-song-asset", "cue-song.wav", hash, 1000)

	logger, buf := capturingLogger()
	trigger := newMultiSyncCueAudioTrigger(logger, clk.now, nil)
	trigger.SetSources(catalogStore, mgr, dir, multisync.NewTimeline(clk.now, multisync.Config{}), nil)
	return trigger, mgr, media, clk, buf.String
}

func syncPacket(action multisync.SyncAction, seconds float32) multisync.SyncPacket {
	return multisync.SyncPacket{Action: action, FileType: multisync.SyncFileTypeSequence, Filename: "trace.fseq", SecondsElapsed: seconds}
}

// A node whose clock is not locked starts at once rather than at T0, so it
// must land where the song is now, not lead ahead of it.
func TestMultiSyncCueAudioUnlockedClockStartsWhereTheSongIs(t *testing.T) {
	trigger, mgr, _, _, _ := newSyncTraceTestTrigger(t, agentclock.StateAcquiring)

	trigger.HandleSequencePacket(context.Background(), syncPacket(multisync.SyncActionStart, 0))

	if pos := cueSessionPosition(t, mgr); pos != time.Second {
		t.Fatalf("position = %v, want 1s: the Cue's offset, with no time passed since the packet", pos)
	}
}

// After a START, SYNC packets that agree with the node's media clock trace
// as zero error, and STOP logs one summary line for the song.
func TestMultiSyncCueAudioSyncTraceReportsErrorAgainstFPP(t *testing.T) {
	trigger, _, media, _, logs := newSyncTraceTestTrigger(t, agentclock.StateLocked)

	before := media.reads()
	done := make(chan struct{})
	go func() {
		defer close(done)
		trigger.HandleSequencePacket(context.Background(), syncPacket(multisync.SyncActionStart, 0))
	}()
	media.waitForReads(t, before+2)
	media.advance(200 * time.Millisecond)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("START never returned after the media clock reached T0")
	}

	media.advance(800 * time.Millisecond) // one second after the START packet
	for _, seconds := range []float32{1, 2, 3} {
		trigger.HandleSequencePacket(context.Background(), syncPacket(multisync.SyncActionSync, seconds))
		media.advance(time.Second)
	}
	trigger.HandleSequencePacket(context.Background(), syncPacket(multisync.SyncActionStop, 3))

	out := logs()
	if !strings.Contains(out, "audio_start_position_ms=1100") {
		t.Fatalf("start log does not show the 1s offset plus the 100ms lead:\n%s", out)
	}
	if got := strings.Count(out, `msg="multisync: cue audio against FPP"`); got != 3 {
		t.Fatalf("logged %d SYNC samples, want 3:\n%s", got, out)
	}
	if !strings.Contains(out, "first_error_ms=0 last_error_ms=0") || !strings.Contains(out, "samples=3") {
		t.Fatalf("summary does not report three samples with zero error:\n%s", out)
	}
}

// A SYNC packet read off the socket while the listener loop was stalled is
// stamped when it was received, not when the loop got to it.
func TestMultiSyncCueAudioSyncIsStampedAtReceiveAfterAStall(t *testing.T) {
	trigger, _, media, clk, logs := newSyncTraceTestTrigger(t, agentclock.StateLocked)

	before := media.reads()
	done := make(chan struct{})
	go func() {
		defer close(done)
		trigger.HandleSequencePacket(context.Background(), syncPacket(multisync.SyncActionStart, 0))
	}()
	media.waitForReads(t, before+2)
	media.advance(200 * time.Millisecond)
	<-done

	media.advance(800 * time.Millisecond)
	receivedAt := clk.now()
	const stall = 2 * time.Second
	media.advance(stall)
	clk.advance(stall)
	trigger.HandleSequencePacketAt(context.Background(), syncPacket(multisync.SyncActionSync, 1), receivedAt)

	if out := logs(); !strings.Contains(out, " error_ms=0") {
		t.Fatalf("SYNC received 1s after START and processed 2s later should trace zero error:\n%s", out)
	}
}

// gatedLoadEngine is a FakeEngine whose Load blocks while a gate is set,
// standing in for a slow Prepare that holds its session lock.
type gatedLoadEngine struct {
	*audio.FakeEngine
	mu      sync.Mutex
	gate    chan struct{}
	started chan struct{}
}

func (e *gatedLoadEngine) Available() (bool, string) { return true, "" }

func (e *gatedLoadEngine) hold() (started <-chan struct{}, release func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.gate, e.started = make(chan struct{}), make(chan struct{})
	return e.started, func() { close(e.gate) }
}

func (e *gatedLoadEngine) Load(ctx context.Context, handle audio.EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (audio.EngineObservation, error) {
	e.mu.Lock()
	gate, started := e.gate, e.started
	e.mu.Unlock()
	if gate != nil {
		close(started)
		<-gate
	}
	return e.FakeEngine.Load(ctx, handle, media, duration)
}

// SYNC packets sent over the real socket keep being traced while another
// session's Prepare holds its lock for seconds.
func TestMultiSyncListenerKeepsTracingDuringAnotherSessionsPrepare(t *testing.T) {
	dir := t.TempDir()
	gated := &gatedLoadEngine{FakeEngine: audio.NewFakeEngine(time.Now)}
	mgr := audio.NewManager(gated, audio.NewFileSessionStore(dir), dir, fixedAudioDecoder{}, time.Now, nil)
	media := newScriptableClockSource(time.Unix(5_000_000_000, 0))
	media.setStep(50 * time.Millisecond)
	mgr.SetClockSource(media)
	mgr.SetSettings(audio.Settings{
		DefaultFadeCurve: pkgaudio.FadeCurveLinear, DefaultFadeDurationMs: 500,
		LTCFrameRate: pkgaudio.LTCFrameRate25, LTCDefaultStartOffset: "00:00:00:00",
		MultisyncStartLeadMs: 100,
	})
	resetTriggerRegistry(t)

	hash := writeAssetFixture(t, dir, "cue-song.wav", []byte("pretend this is wav audio content"))
	catalogStore := heldcatalog.NewFileStore(dir)
	newTriggerTestCatalog(t, catalogStore, "cue-trace", "trace.fseq", "cue-song-asset", "cue-song.wav", hash, 0)

	_, buf := capturingLogger()
	var logMu sync.Mutex
	logs := func() string { logMu.Lock(); defer logMu.Unlock(); return buf.String() }
	logger := slog.New(slog.NewTextHandler(lockedWriter{&logMu, buf}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	trigger := newMultiSyncCueAudioTrigger(logger, time.Now, nil)
	trigger.SetSources(catalogStore, mgr, dir, multisync.NewTimeline(time.Now, multisync.Config{}), nil)

	probe, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := probe.LocalAddr().String()
	_ = probe.Close()

	ctx, cancel := context.WithCancel(context.Background())
	status := newMultiSyncStatus()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMultiSyncListener(ctx, "test-node", addr, "", multisync.NewTimeline(time.Now, multisync.Config{}), status, newFPPConnectState(), trigger, logger)
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitFor(t, 5*time.Second, "listener never bound", func() bool { l, _, _ := status.get(); return l })

	conn, err := net.Dial("udp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	send := func(action multisync.SyncAction, seconds float32) {
		b, err := multisync.EncodeSync(syncPacket(action, seconds))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Write(b); err != nil {
			t.Fatal(err)
		}
	}

	send(multisync.SyncActionStart, 0)
	waitFor(t, 5*time.Second, "START never scheduled the cue audio", func() bool {
		return strings.Contains(logs(), "scheduled on its sequence's START")
	})

	loadStarted, release := gated.hold()
	stagingID := pkgaudio.SessionID(cueactivation.PrepareStagingSessionID)
	ref := pkgaudio.MediaRef{AssetID: "cue-song-asset", ContentHash: hash, RuntimeFilename: "cue-song.wav"}
	mgr.Apply(context.Background(), stagingID, "stage-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	// The Prepare writes session state on return, so it must finish before TempDir cleanup runs.
	prepareDone := make(chan struct{})
	t.Cleanup(func() { release(); <-prepareDone })
	go func() {
		defer close(prepareDone)
		mgr.Prepare(context.Background(), stagingID, "stage-prepare", 2)
	}()
	select {
	case <-loadStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("the staging Prepare never reached the engine")
	}

	send(multisync.SyncActionSync, 1)
	waitFor(t, 2*time.Second, "SYNC was not traced while the staging Prepare held its session", func() bool {
		return strings.Contains(logs(), "cue audio against FPP")
	})
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func waitFor(t *testing.T, d time.Duration, msg string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(d); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatal(msg)
}
