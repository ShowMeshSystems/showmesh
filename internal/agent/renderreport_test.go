package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/pipeline"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func decodeRenderReport(t *testing.T, payload []byte) mqttproto.RenderPayload {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	p, err := mqttproto.DecodeRenderPayload(env)
	if err != nil {
		t.Fatalf("DecodeRenderPayload() error = %v", err)
	}
	return p
}

// TestRunRenderReportPublishesOnTick proves a tick produces a retained
// publish on the node's observed/render topic reflecting the supervisor's
// real state, following runAssetInventory's identical test shape.
func TestRunRenderReportPublishesOnTick(t *testing.T) {
	clock := newTestClock()
	fs := &fakeRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer awaitCancel()
	if _, ok := sup.AwaitState(awaitCtx, "surface-1", []pipeline.State{pipeline.StateRunning}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("setup: pipeline never reached running")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for runRenderReport to return")
	}

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	if calls[0].topic != "showmesh/nodes/media-03/observed/render" {
		t.Fatalf("topic = %q, want showmesh/nodes/media-03/observed/render", calls[0].topic)
	}
	if !calls[0].retain {
		t.Fatalf("retain = false, want true (ObservedDeliveryPolicy is retained)")
	}
	if calls[0].qos != mqttproto.ObservedDeliveryPolicy.QoS {
		t.Fatalf("qos = %d, want %d", calls[0].qos, mqttproto.ObservedDeliveryPolicy.QoS)
	}

	report := decodeRenderReport(t, calls[0].payload)
	if len(report.Surfaces) != 1 {
		t.Fatalf("got %d surfaces, want 1", len(report.Surfaces))
	}
	if report.Surfaces[0].SurfaceID != "surface-1" {
		t.Fatalf("SurfaceID = %q, want surface-1", report.Surfaces[0].SurfaceID)
	}
	if report.Surfaces[0].PipelineState != mqttproto.RenderPipelineStateRunning {
		t.Fatalf("PipelineState = %q, want %q", report.Surfaces[0].PipelineState, mqttproto.RenderPipelineStateRunning)
	}
}

// TestRunRenderReportPublishesOnTrigger proves a signal on triggered
// produces an immediate publish, out of cadence — the render-state
// counterpart to runAssetInventory's identical trigger behaviour.
func TestRunRenderReportPublishesOnTrigger(t *testing.T) {
	clock := newTestClock()
	fs := &fakeRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	pub := newFakePublisher()
	triggered := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, time.Now, nil, triggered, discardLogger())
	}()

	select {
	case triggered <- struct{}{}:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending trigger")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	<-done

	if len(pub.snapshot()) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(pub.snapshot()))
	}
}

// TestRunRenderReportStartingStateDecodes is a regression test for F8: a
// report published while a surface is StateStarting must round-trip through
// DecodeRenderPayload, exactly as the publisher's own decoder would see it
// on the wire. Before the fix, StateStarting carried an empty Reason, which
// RenderPayload.Validate refuses for any non-running state — so a tick
// landing during startup produced a message the coordinator's own decoder
// would discard whole.
func TestRunRenderReportStartingStateDecodes(t *testing.T) {
	clock := newTestClock()
	fs := &blockingRenderStarter{}
	sup := pipeline.NewSupervisor(clock.now, fs.Start, discardLogger())
	t.Cleanup(func() {
		fs.release()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	if err := sup.Apply(pipeline.DefaultTestPatternSpec("surface-1")); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer awaitCancel()
	if _, ok := sup.AwaitState(awaitCtx, "surface-1", []pipeline.State{pipeline.StateStarting}, time.Time{}, 5*time.Millisecond); !ok {
		t.Fatalf("setup: pipeline never reached starting")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish")
	}

	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}

	// The bug this guards against makes DecodeRenderPayload itself fail, so
	// decodeRenderReport (which calls it) is the assertion.
	report := decodeRenderReport(t, calls[0].payload)
	if len(report.Surfaces) != 1 {
		t.Fatalf("got %d surfaces, want 1", len(report.Surfaces))
	}
	if report.Surfaces[0].PipelineState != mqttproto.RenderPipelineStateStarting {
		t.Fatalf("PipelineState = %q, want %q", report.Surfaces[0].PipelineState, mqttproto.RenderPipelineStateStarting)
	}
	if report.Surfaces[0].Reason == "" {
		t.Fatalf("Reason is empty for a non-running state, which RenderPayload.Validate should have refused")
	}
}

// blockingRenderStarter never calls onRunningMarker and never exits on its
// own, so the supervisor stays in StateStarting until release() is called —
// unlike fakeRenderStarter, which fires the marker immediately.
type blockingRenderStarter struct {
	exitCh chan pipeline.ExitResult
}

func (f *blockingRenderStarter) Start(_ context.Context, _ string, _ []string, _ func()) (pipeline.ProcessHandle, error) {
	f.exitCh = make(chan pipeline.ExitResult, 1)
	return &fakeRenderProcess{exitCh: f.exitCh}, nil
}

func (f *blockingRenderStarter) release() {
	if f.exitCh != nil {
		select {
		case f.exitCh <- pipeline.ExitResult{Signaled: true}:
		default:
		}
	}
}

// newTestClock is a small time.Time-returning helper distinct from
// heartbeat_test.go's own fakeClock type (unexported in this package, and
// this file intentionally does not reach into it) — a fixed, monotonically
// advancing wrapper is unnecessary here since these tests do not assert on
// backoff timing, only on publish behaviour.
type testClock struct{ t time.Time }

func newTestClock() *testClock      { return &testClock{t: time.Now()} }
func (c *testClock) now() time.Time { return c.t }

// fakeRenderStarter is a minimal pipeline.ProcessStarter for this file's
// tests: it reports the running marker immediately and never exits on its
// own, matching pipeline package's own fakeStarter default behaviour
// (duplicated here rather than exported across the package boundary, since
// pipeline's fakes are deliberately test-only and unexported).
type fakeRenderStarter struct{}

func (f *fakeRenderStarter) Start(_ context.Context, _ string, _ []string, onRunningMarker func()) (pipeline.ProcessHandle, error) {
	if onRunningMarker != nil {
		onRunningMarker()
	}
	return &fakeRenderProcess{exitCh: make(chan pipeline.ExitResult, 1)}, nil
}

type fakeRenderProcess struct {
	exitCh chan pipeline.ExitResult
}

func (p *fakeRenderProcess) Wait() pipeline.ExitResult { return <-p.exitCh }
func (p *fakeRenderProcess) Kill() error {
	select {
	case p.exitCh <- pipeline.ExitResult{Signaled: true}:
	default:
	}
	return nil
}
func (p *fakeRenderProcess) Pid() int { return 1 }
