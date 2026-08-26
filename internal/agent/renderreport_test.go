package agent

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
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

// TestToRenderSurfaceReportCarriesRealFrameCounters is a direct regression
// test for a defect this seam's review found: toRenderSurfaceReport
// hardcoded FramesWritten/FramesLate/FramesDropped to 0 behind a stale
// "B2a has no frame writer; B3 populates this" comment, so every frame
// counter this system ever published was a zero regardless of what the
// frame writer actually counted. FramesRate (ADR-040) must round-trip
// too, including staying nil when unmeasured — never a fabricated zero.
func TestToRenderSurfaceReportCarriesRealFrameCounters(t *testing.T) {
	rate := 39.4
	framesAt := time.Date(2026, 8, 17, 12, 0, 45, 0, time.UTC)
	stateAt := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) // deliberately earlier than framesAt
	snap := pipeline.Snapshot{
		SurfaceID:        "wall-1",
		State:            pipeline.StateRunning,
		FramesWritten:    1234,
		FramesLate:       12,
		FramesDropped:    3,
		FramesRate:       &rate,
		FramesObservedAt: framesAt,
		ObservedAt:       stateAt,
	}
	got := toRenderSurfaceReport(snap)
	if got.FramesWritten != 1234 {
		t.Errorf("FramesWritten = %d, want 1234 (got the real supervisor snapshot value, not a hardcoded 0)", got.FramesWritten)
	}
	if got.FramesLate != 12 {
		t.Errorf("FramesLate = %d, want 12", got.FramesLate)
	}
	if got.FramesDropped != 3 {
		t.Errorf("FramesDropped = %d, want 3", got.FramesDropped)
	}
	if got.FramesRate == nil || *got.FramesRate != rate {
		t.Errorf("FramesRate = %v, want %v", got.FramesRate, rate)
	}

	// This issue's own fix: FramesObservedAt is carried independently of
	// ObservedAt, never collapsed onto it. Proven here with two
	// deliberately different values.
	if !got.FramesObservedAt.Equal(framesAt) {
		t.Errorf("FramesObservedAt = %v, want %v (the frame writer's own window-close timestamp)", got.FramesObservedAt, framesAt)
	}
	if !got.ObservedAt.Equal(stateAt) {
		t.Errorf("ObservedAt = %v, want %v (the pipeline-lifecycle timestamp, unperturbed by frame evidence)", got.ObservedAt, stateAt)
	}

	unmeasured := toRenderSurfaceReport(pipeline.Snapshot{SurfaceID: "wall-1", State: pipeline.StateRunning})
	if unmeasured.FramesRate != nil {
		t.Errorf("FramesRate = %v for an unmeasured snapshot, want nil (never a fabricated zero)", unmeasured.FramesRate)
	}
	if !unmeasured.FramesObservedAt.IsZero() {
		t.Errorf("FramesObservedAt = %v for a snapshot whose frame writer never sampled, want zero", unmeasured.FramesObservedAt)
	}
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
	if _, ok := sup.AwaitState(awaitCtx, "surface-1", []pipeline.State{pipeline.StateRunning}, time.Time{}, -1, 5*time.Millisecond); !ok {
		t.Fatalf("setup: pipeline never reached running")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, ticks, nil, discardLogger())
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

// TestRunRenderReportIncludesHeldFilesAndEvents is review round 1 finding
// 1's regression test: before this fix, fppConnectHeldStore's Held() and
// Events() had no non-test caller at all, so ADR-044 decision 8's
// "reported as an unbound held file the operator can claim" had no place
// to actually reach an operator, since xLights never inspects the
// playlist POST's status. A completed-but-unbound held file and an
// unknown-playlist evidence event must both appear in a published report.
func TestRunRenderReportIncludesHeldFilesAndEvents(t *testing.T) {
	clock := newTestClock()
	sup := pipeline.NewSupervisor(clock.now, nil, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	held := newTestFPPConnectHeldStore(t)
	neverActive := func() (string, bool, bool) { return "", false, false }
	outcome, reason, rec := held.WriteChunk("sequences", "Unbound.fseq", 0, 3, strings.NewReader("abc"), 3, 1<<30, 1<<30, time.Now(), neverActive)
	if outcome != fppConnectChunkCompleted {
		t.Fatalf("setup: outcome = %v reason = %q, want completed", outcome, reason)
	}
	if rec.Bound {
		t.Fatalf("setup: record unexpectedly bound: %+v", rec)
	}
	held.RecordUnknownPlaylist("Mystery", []string{"Foo.fseq"}, time.Now())

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, newMultiSyncStatus(), newFPPConnectHTTPStatus(), held, time.Now, ticks, nil, discardLogger())
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
	report := decodeRenderReport(t, calls[0].payload)

	if report.FPPConnectHeldCount != 1 {
		t.Fatalf("FPPConnectHeldCount = %d, want 1", report.FPPConnectHeldCount)
	}
	if len(report.FPPConnectHeld) != 1 {
		t.Fatalf("got %d held files, want 1: %+v", len(report.FPPConnectHeld), report.FPPConnectHeld)
	}
	f := report.FPPConnectHeld[0]
	if f.Name != "Unbound.fseq" || f.Dir != "sequences" {
		t.Fatalf("held file = %+v, want sequences/Unbound.fseq", f)
	}
	if f.Bound {
		t.Fatalf("held file = %+v, want unbound", f)
	}
	if f.UnboundReason == "" {
		t.Fatal("UnboundReason is empty, want it to name why this file is unbound")
	}

	var foundEvent bool
	for _, ev := range report.FPPConnectHeldEvents {
		if ev.Kind == "unknown" && ev.Name == "Mystery" {
			foundEvent = true
		}
	}
	if !foundEvent {
		t.Fatalf("no unknown-playlist event in the published report; events = %+v", report.FPPConnectHeldEvents)
	}
}

// TestRunRenderReportStaysUnderEnvelopeLimitWithAnOversizedPlaylistPost is
// review round 3 finding 2's regression test: one unauthenticated
// POST /api/playlist/{name} can name tens of thousands of distinct
// sequenceName/mediaName values in a single 1 MiB body. Before this fix,
// every one of them rode the resulting event onto every subsequent render
// report, with no cap anywhere between the store and RenderPayload.
// Validate, and mqttproto.NewRenderEnvelope calls Validate itself before
// marshalling, so an unbounded field would not merely bloat one publish,
// it would make every future publish fail outright (a report skipped
// forever, not degraded once). This proves the publish still succeeds and
// the resulting bytes still decode under the wire envelope limit.
func TestRunRenderReportStaysUnderEnvelopeLimitWithAnOversizedPlaylistPost(t *testing.T) {
	clock := newTestClock()
	sup := pipeline.NewSupervisor(clock.now, nil, discardLogger())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		sup.Shutdown(ctx)
	})

	held := newTestFPPConnectHeldStore(t)
	view := fakeFPPConnectView{enabled: true}
	srv := startFPPConnectTestServer(t, view, "node-1", held)

	// Build a playlist body naming as many distinct sequenceName entries
	// as fit under fppConnectMaxPlaylistBodyBytes (1 MiB), against an
	// unknown show name so every one lands in a single "unknown" event.
	var sb strings.Builder
	sb.WriteString(`{"mainPlaylist":[`)
	for i := 0; sb.Len() < fppConnectMaxPlaylistBodyBytes-256; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		fmt.Fprintf(&sb, `{"sequenceName":"S%08d.fseq"}`, i)
	}
	sb.WriteString(`]}`)

	resp, body := postPlaylist(t, srv, "DoesNotExist", []byte(sb.String()))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, body)
	}

	events := held.Events()
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if len(events[0].Entries) > fppConnectMaxEventEntries {
		t.Fatalf("event carries %d entries, want at most %d", len(events[0].Entries), fppConnectMaxEventEntries)
	}
	if events[0].EntriesTruncated == 0 {
		t.Fatal("EntriesTruncated = 0, want a positive count given how oversized the posted body was")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, newMultiSyncStatus(), newFPPConnectHTTPStatus(), held, time.Now, ticks, nil, discardLogger())
	}()

	select {
	case ticks <- time.Now():
	case <-time.After(5 * time.Second):
		t.Fatal("timed out sending tick")
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for publish (an unbounded field would make NewRenderEnvelope's own Validate call refuse to build the envelope at all)")
	}
	cancel()
	<-done

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("got %d publish calls, want 1", len(calls))
	}
	report := decodeRenderReport(t, calls[0].payload)
	if len(report.FPPConnectHeldEvents) == 0 {
		t.Fatal("no events in the published report")
	}
}

// TestRunRenderReportPublishesOnTrigger proves a signal on triggered
// produces an immediate publish, out of cadence: the render-state
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
		runRenderReport(ctx, pub, "media-03", sup, newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, nil, triggered, discardLogger())
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
	if _, ok := sup.AwaitState(awaitCtx, "surface-1", []pipeline.State{pipeline.StateStarting}, time.Time{}, -1, 5*time.Millisecond); !ok {
		t.Fatalf("setup: pipeline never reached starting")
	}

	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		runRenderReport(ctx, pub, "media-03", sup, newMultiSyncStatus(), newFPPConnectHTTPStatus(), newTestFPPConnectHeldStore(t), time.Now, ticks, nil, discardLogger())
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
func (p *fakeRenderProcess) Pid() int                  { return 1 }
func (p *fakeRenderProcess) Stdin() (io.Writer, error) { return io.Discard, nil }
