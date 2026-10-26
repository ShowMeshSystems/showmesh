package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	agentclock "github.com/showmeshsystems/showmesh/internal/agent/clock"
	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
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
