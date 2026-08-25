package agent

import (
	"bytes"
	"context"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// recordingProcess is a [pipeline.ProcessHandle] that keeps every byte
// written to its stdin, so a test can assert on the frames that actually
// reached the pipeline rather than only on the writer's own counters.
type recordingProcess struct {
	mu     sync.Mutex
	writes [][]byte
	exitCh chan pipeline.ExitResult
}

func (p *recordingProcess) Wait() pipeline.ExitResult { return <-p.exitCh }

func (p *recordingProcess) Kill() error {
	select {
	case p.exitCh <- pipeline.ExitResult{Signaled: true}:
	default:
	}
	return nil
}

func (p *recordingProcess) Pid() int                  { return 1 }
func (p *recordingProcess) Stdin() (io.Writer, error) { return p, nil }

func (p *recordingProcess) Write(b []byte) (int, error) {
	frame := make([]byte, len(b))
	copy(frame, b)
	p.mu.Lock()
	p.writes = append(p.writes, frame)
	p.mu.Unlock()
	return len(b), nil
}

func (p *recordingProcess) frames() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([][]byte, len(p.writes))
	copy(out, p.writes)
	return out
}

// recordingStarter hands out [recordingProcess] handles and records the
// argv each was started with.
type recordingStarter struct {
	mu    sync.Mutex
	procs []*recordingProcess
	argv  [][]string
}

func (s *recordingStarter) Start(_ context.Context, _ string, args []string, onRunningMarker func()) (pipeline.ProcessHandle, error) {
	p := &recordingProcess{exitCh: make(chan pipeline.ExitResult, 1)}
	s.mu.Lock()
	s.procs = append(s.procs, p)
	s.argv = append(s.argv, args)
	s.mu.Unlock()
	if onRunningMarker != nil {
		onRunningMarker()
	}
	return p, nil
}

func (s *recordingStarter) started() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.procs)
}

// frames returns every frame written to every process this starter handed
// out, in the order the processes were started.
func (s *recordingStarter) frames() [][]byte {
	s.mu.Lock()
	procs := append([]*recordingProcess(nil), s.procs...)
	s.mu.Unlock()
	var out [][]byte
	for _, p := range procs {
		out = append(out, p.frames()...)
	}
	return out
}

// coldNode builds the render subsystem exactly as agent.go's Run does for a
// node on which NOTHING else is running: no coordinator, no broker, no FPP
// master feeding the MultiSync timeline, no asset manifest, no FSEQ file, no
// held cue catalog and no persisted assignment. The asset dir is a fresh
// empty temp dir, which is all three of those last absences at once.
func coldNode(t *testing.T) (*renderOperations, *recordingStarter, *logCapture) {
	t.Helper()
	dir := t.TempDir()
	clock := &fakeClock{t: time.Now()}
	starter := &recordingStarter{}
	logs := &logCapture{}
	sup := pipeline.NewSupervisor(clock.now, starter.Start, discardLogger())
	store := pipeline.NewAssignmentStore(dir)

	persisted, err := store.Load()
	if err != nil {
		t.Fatalf("loading persisted assignments from a cold node: %v", err)
	}
	if len(persisted) != 0 {
		t.Fatalf("a cold node must hold no persisted assignment, got %d", len(persisted))
	}

	ops := newRenderOperations(sup, store, dir, multisync.NewTimeline(clock.now, multisync.Config{}), nil, logs)
	t.Cleanup(func() {
		ops.Shutdown()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})
	return ops, starter, logs
}

// logCapture is a [pipeline.Logger] that keeps every message, so a test can
// assert on what a startup path told the operator — including that a path
// which must stay silent said nothing at all.
type logCapture struct {
	mu       sync.Mutex
	messages []string
}

func (l *logCapture) record(msg string) {
	l.mu.Lock()
	l.messages = append(l.messages, msg)
	l.mu.Unlock()
}

func (l *logCapture) Info(msg string, _ ...any)  { l.record(msg) }
func (l *logCapture) Warn(msg string, _ ...any)  { l.record(msg) }
func (l *logCapture) Error(msg string, _ ...any) { l.record(msg) }

func (l *logCapture) contains(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.messages {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func (l *logCapture) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.messages)
}

// diagnosticTestSurface is a small surface so one frame is a few kilobytes
// rather than six megabytes, at the reference 40 fps profile's frame rate.
var diagnosticTestSurface = config.DiagnosticSurface{
	SurfaceID: "diagnostic-surface",
	Width:     64,
	Height:    8,
	FrameRate: 40,
}

// TestDiagnosticSurfaceRendersFromAColdNode is the owner ruling on diagnostic idle output as a
// test: starting from a node with nothing running but the agent, the
// diagnostic idle pattern must actually reach the pipeline. It asserts the
// cold state first (nothing draws, which is what the ruling was filed
// about) so the frames afterwards can only have come from the node-local
// diagnostic surface.
func TestDiagnosticSurfaceRendersFromAColdNode(t *testing.T) {
	ops, starter, _ := coldNode(t)

	time.Sleep(150 * time.Millisecond)
	if got := starter.started(); got != 0 {
		t.Fatalf("a cold node started %d pipeline processes before the diagnostic surface, want 0", got)
	}
	if got := len(starter.frames()); got != 0 {
		t.Fatalf("a cold node wrote %d frames before the diagnostic surface, want 0", got)
	}

	if err := ops.StartDiagnosticSurface(diagnosticTestSurface, time.Now); err != nil {
		t.Fatalf("StartDiagnosticSurface on a cold node: %v", err)
	}

	frameBytes := diagnosticTestSurface.Width * diagnosticTestSurface.Height * diagnosticBytesPerPixel
	frames := waitForFrames(t, starter, 4)
	for i, f := range frames {
		if len(f) != frameBytes {
			t.Fatalf("frame %d is %d bytes, want %d (width*height*bytesPerPixel)", i, len(f), frameBytes)
		}
		if !bytes.Contains(f, []byte{0x30, 0x30, 0x30}) {
			t.Fatalf("frame %d carries no diagnostic background fill", i)
		}
		if !bytes.Contains(f, []byte{0xFF, 0xFF, 0xFF}) {
			t.Fatalf("frame %d carries no diagnostic bar", i)
		}
		if bytes.Equal(f, make([]byte, frameBytes)) {
			t.Fatalf("frame %d is all black, which is what a cold node already drew", i)
		}
	}

	// The owner's refinement on ruling 3: diagnostic must be GENERATED
	// content, never a still picture, because a frozen frame on a house
	// cannot be told apart from a crashed renderer.
	if bytes.Equal(frames[0], frames[len(frames)-1]) {
		t.Fatal("the diagnostic pattern never changed across the captured frames; it must be generated content, not a held frame")
	}
}

// TestDiagnosticSurfaceNeedsNoTimeline pins the specific dependency the owner ruling
// names: the pattern must not stop when the MultiSync timeline says
// anything at all, because on a cold node no FPP master is sending one.
func TestDiagnosticSurfaceNeedsNoTimeline(t *testing.T) {
	ops, starter, _ := coldNode(t)
	if err := ops.StartDiagnosticSurface(diagnosticTestSurface, time.Now); err != nil {
		t.Fatalf("StartDiagnosticSurface: %v", err)
	}
	waitForFrames(t, starter, 2)

	// Drive the node's shared timeline into playing, the one state that
	// makes an ASSIGNED surface draw content instead of the idle output.
	// The diagnostic surface must be untouched by it.
	ops.timeline.Observe(multisync.SyncPacket{Action: multisync.SyncActionStart, Filename: "show.fseq"}, "test")
	ops.timeline.Observe(multisync.SyncPacket{Action: multisync.SyncActionSync, FrameNumber: 100, SecondsElapsed: 2.5, Filename: "show.fseq"}, "test")

	before := len(starter.frames())
	frames := waitForFrames(t, starter, before+4)
	last := frames[len(frames)-1]
	if !bytes.Contains(last, []byte{0xFF, 0xFF, 0xFF}) {
		t.Fatal("the diagnostic surface stopped drawing its pattern once the timeline reported a position")
	}
}

// TestDiagnosticSurfaceNeverDisplacesARunningWriter keeps the diagnostic
// surface from taking a surface a real assignment already owns: a node that
// resumed a persisted assignment at boot must keep rendering it.
func TestDiagnosticSurfaceNeverDisplacesARunningWriter(t *testing.T) {
	ops, _, _ := coldNode(t)
	if err := ops.StartDiagnosticSurface(diagnosticTestSurface, time.Now); err != nil {
		t.Fatalf("first StartDiagnosticSurface: %v", err)
	}
	err := ops.StartDiagnosticSurface(diagnosticTestSurface, time.Now)
	if err == nil {
		t.Fatal("a second StartDiagnosticSurface on a surface that already has a running frame writer was accepted")
	}
	if !strings.Contains(err.Error(), "already has a running frame writer") {
		t.Fatalf("error does not name the reason: %v", err)
	}
}

// TestDiagnosticSurfaceIfConfiguredHonoursTheSwitch is the startup gate: a
// node that was never asked for a diagnostic surface must not invent one,
// and a node that was must get one without a coordinator in the picture.
func TestDiagnosticSurfaceIfConfiguredHonoursTheSwitch(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		ops, starter, logs := coldNode(t)
		ops.StartDiagnosticSurfaceIfConfigured(config.DiagnosticSurface{}, time.Now)
		time.Sleep(150 * time.Millisecond)
		if got := starter.started(); got != 0 {
			t.Fatalf("an unconfigured node started %d pipeline processes, want 0", got)
		}
		if got := logs.count(); got != 0 {
			t.Fatalf("an unconfigured node logged %d diagnostic-surface messages, want none: it must be silent, not warn every boot", got)
		}
	})
	t.Run("enabled", func(t *testing.T) {
		ops, starter, logs := coldNode(t)
		ops.StartDiagnosticSurfaceIfConfigured(diagnosticTestSurface, time.Now)
		waitForFrames(t, starter, 2)
		if !ops.hasRunningFrameWriter(diagnosticTestSurface.SurfaceID) {
			t.Fatal("no frame writer is running for the configured diagnostic surface")
		}
		if !logs.contains("started the node-local diagnostic surface") {
			t.Fatal("the node never said it started its diagnostic surface")
		}
	})
	t.Run("configured but unstartable", func(t *testing.T) {
		ops, starter, logs := coldNode(t)
		bad := diagnosticTestSurface
		bad.SurfaceID = "not a surface id"
		ops.StartDiagnosticSurfaceIfConfigured(bad, time.Now)
		if got := starter.started(); got != 0 {
			t.Fatalf("an unstartable diagnostic surface started %d pipeline processes, want 0", got)
		}
		if !logs.contains("failed to start the node-local diagnostic surface") {
			t.Fatal("a diagnostic surface that could not start said nothing about it")
		}
	})
}

// TestDiagnosticSurfaceStatesItsDegradedOutput keeps a diagnostic surface
// with no NDI source name from reporting a healthy output it does not have.
func TestDiagnosticSurfaceStatesItsDegradedOutput(t *testing.T) {
	ops, _, _ := coldNode(t)
	if err := ops.StartDiagnosticSurface(diagnosticTestSurface, time.Now); err != nil {
		t.Fatalf("StartDiagnosticSurface: %v", err)
	}
	snap, ok := ops.sup.Snapshot(diagnosticTestSurface.SurfaceID)
	if !ok {
		t.Fatal("the diagnostic surface has no supervisor snapshot")
	}
	if snap.TransportAvailable == nil {
		t.Fatal("a diagnostic surface with no NDI source name reported no degraded-output evidence")
	}
	if *snap.TransportAvailable {
		t.Fatal("a diagnostic surface with no NDI source name reported an available transport")
	}
	if !strings.Contains(snap.TransportReason, "sourceName") {
		t.Fatalf("the degraded-output reason does not name the missing source name: %q", snap.TransportReason)
	}
}

// TestDiagnosticSurfaceRefusesInvalidConfiguration proves the refusals are
// stated rather than silently degrading to a dark surface.
func TestDiagnosticSurfaceRefusesInvalidConfiguration(t *testing.T) {
	cases := map[string]config.DiagnosticSurface{
		"no surface id":   {Width: 64, Height: 8, FrameRate: 40},
		"bad surface id":  {SurfaceID: "not a surface id", Width: 64, Height: 8, FrameRate: 40},
		"zero geometry":   {SurfaceID: "diagnostic-surface", Width: 0, Height: 8, FrameRate: 40},
		"zero frame rate": {SurfaceID: "diagnostic-surface", Width: 64, Height: 8, FrameRate: 0},
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			ops, _, _ := coldNode(t)
			if err := ops.StartDiagnosticSurface(d, time.Now); err == nil {
				t.Fatal("accepted an invalid diagnostic surface configuration")
			}
		})
	}
}

// waitForFrames blocks until starter has recorded at least want frames, or
// fails the test. Polled rather than slept for a fixed duration so a slow
// CI machine does not turn a timing assumption into a flake.
func waitForFrames(t *testing.T, starter *recordingStarter, want int) [][]byte {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		frames := starter.frames()
		if len(frames) >= want {
			return frames
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d frames reached the pipeline within the deadline, want %d", len(frames), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
