//go:build cgo

package gstengine

import (
	"errors"
	"testing"
	"time"

	"github.com/go-gst/go-gst/pkg/gst"
)

// This suite proves RES-019 section 7.2 candidate A's pipeline-clock and
// sink-backend reporting against a real GStreamer pipeline (fakesink, not
// a test double for this package's own logic), matching
// engine_real_integration_test.go's own "real pipeline, real elements"
// convention.

// fakeClockReader is a [ClockReader] double: readable until failAfter
// successful reads, then every further Now() call fails. A negative
// failAfter never fails. failCount, when non-zero, bounds the failure to
// that many reads before Now() starts succeeding again, for a transient
// (rather than permanent) failure.
type fakeClockReader struct {
	base      time.Time
	failAfter int
	failCount int
	reads     int
	closed    bool
}

func (r *fakeClockReader) Now() (time.Time, error) {
	r.reads++
	if r.failAfter >= 0 && r.reads > r.failAfter && (r.failCount == 0 || r.reads <= r.failAfter+r.failCount) {
		return time.Time{}, errors.New("fake PHC read failure")
	}
	return r.base.Add(time.Duration(r.reads) * time.Millisecond), nil
}

func (r *fakeClockReader) Close() error {
	r.closed = true
	return nil
}

func TestSinkBackendReportsTheConfiguredFactory(t *testing.T) {
	e := newTestEngine(t)
	if got := e.SinkBackend(); got != "fakesink" {
		t.Fatalf("SinkBackend() = %q, want %q", got, "fakesink")
	}
}

func TestClockSourceDefaultWhenNoClockConfigured(t *testing.T) {
	e := newTestEngine(t)
	source, reason := e.ClockSource()
	if source != clockSourceDefault || reason != "" {
		t.Fatalf("ClockSource() = (%q, %q), want (%q, \"\"): no clock was configured, so this is not a failure", source, reason, clockSourceDefault)
	}
}

func TestClockSourceDefaultCarriesAnUpstreamUnavailableReason(t *testing.T) {
	cfg := testConfig(resolveByRuntimeFilename)
	cfg.ClockUnavailableReason = "interface eth0 has no associated PHC"
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	source, reason := e.ClockSource()
	if source != clockSourceDefault {
		t.Fatalf("ClockSource() source = %q, want %q", source, clockSourceDefault)
	}
	if reason != cfg.ClockUnavailableReason {
		t.Fatalf("ClockSource() reason = %q, want the upstream reason %q unchanged", reason, cfg.ClockUnavailableReason)
	}
}

func TestClockSourcePHCWhenTheConfiguredClockIsReadable(t *testing.T) {
	reader := &fakeClockReader{base: time.Now(), failAfter: -1}
	cfg := testConfig(resolveByRuntimeFilename)
	cfg.Clock = reader
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	source, reason := e.ClockSource()
	if source != clockSourcePHC || reason != "" {
		t.Fatalf("ClockSource() = (%q, %q), want (%q, \"\")", source, reason, clockSourcePHC)
	}
	if reader.reads == 0 {
		t.Fatalf("the configured clock reader was never read; installClock must validate it before UseClock")
	}
	if e.pipeline.GetClock() == nil {
		t.Fatalf("pipeline.GetClock() is nil after a successful PHC clock install")
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !reader.closed {
		t.Fatalf("Close did not release the pipeline clock's own reader")
	}
}

// TestClockSourceRealtimeWhenConfiguredClockKindIsRealtime proves a
// readable Clock reports exactly the kind cfg.ClockKind names, not
// always "phc" -- the exact defect the orchestrator found: a realtime
// reader wired for a node with no PHC hardware still reported "phc",
// which hid the running node's own real clock source from every operator
// and from RES-019's own evidence.
func TestClockSourceRealtimeWhenConfiguredClockKindIsRealtime(t *testing.T) {
	reader := &fakeClockReader{base: time.Now(), failAfter: -1}
	cfg := testConfig(resolveByRuntimeFilename)
	cfg.Clock = reader
	cfg.ClockKind = ClockKindRealtime
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	source, reason := e.ClockSource()
	if source != clockSourceRealtime || reason != "" {
		t.Fatalf("ClockSource() = (%q, %q), want (%q, \"\")", source, reason, clockSourceRealtime)
	}
	if source == clockSourcePHC {
		t.Fatalf("ClockSource() reported %q for a realtime reader; a reader must never be reported under another reader's name", source)
	}
}

func TestClockSourceFallsBackToDefaultWhenTheConfiguredClockIsUnreadable(t *testing.T) {
	reader := &fakeClockReader{base: time.Now(), failAfter: 0} // fails on the very first read
	cfg := testConfig(resolveByRuntimeFilename)
	cfg.Clock = reader
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	source, reason := e.ClockSource()
	if source != clockSourceDefault {
		t.Fatalf("ClockSource() source = %q, want %q: an unreadable PHC clock must not be fatal", source, clockSourceDefault)
	}
	if reason == "" {
		t.Fatalf("ClockSource() reason is empty; a configured-but-failed PHC clock must state why")
	}

	if err := e.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !reader.closed {
		t.Fatalf("Close did not release the pipeline clock's own reader even though it was never installed")
	}
}

// TestClockSourceReportsUnreadableAfterInstall proves a clock that reads
// fine at install time and later fails no longer reports [clockSourcePHC]:
// the case neither of the two suites above cover, and the one a real PHC
// failure (a NIC reset, a removed interface) actually produces.
func TestClockSourceReportsUnreadableAfterInstall(t *testing.T) {
	reader := &fakeClockReader{base: time.Now(), failAfter: -1}
	cfg := testConfig(resolveByRuntimeFilename)
	cfg.Clock = reader
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	if source, _ := e.ClockSource(); source != clockSourcePHC {
		t.Fatalf("precondition: ClockSource() = %q, want %q before the reader ever fails", source, clockSourcePHC)
	}

	reader.failAfter = reader.reads
	e.phcClockGetTime(gst.Clock(nil))

	source, reason := e.ClockSource()
	if source == clockSourcePHC {
		t.Fatalf("ClockSource() still reports %q after the configured clock became unreadable", clockSourcePHC)
	}
	if source != clockSourceDefault {
		t.Fatalf("ClockSource() source = %q, want %q", source, clockSourceDefault)
	}
	if reason == "" {
		t.Fatalf("ClockSource() reason is empty; a clock that stopped being readable must state why")
	}
}

// TestClockSourceRestoresPHCAfterTransientFailure proves a single transient
// read failure does not pin ClockSource to clockSourceDefault forever
// (re-review finding B, PR #454): the pipeline's UseClock is never undone,
// so once the configured clock is readable again the report must say PHC
// again, not stay stuck on a failure that already recovered.
func TestClockSourceRestoresPHCAfterTransientFailure(t *testing.T) {
	reader := &fakeClockReader{base: time.Now(), failAfter: -1}
	cfg := testConfig(resolveByRuntimeFilename)
	cfg.Clock = reader
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: gstengine unavailable in this environment: %s", reason)
	}
	t.Cleanup(func() { _ = e.Close() })

	reader.failAfter = reader.reads
	reader.failCount = 1
	e.phcClockGetTime(gst.Clock(nil))
	if source, _ := e.ClockSource(); source != clockSourceDefault {
		t.Fatalf("precondition: ClockSource() = %q, want %q right after the transient failure", source, clockSourceDefault)
	}

	e.phcClockGetTime(gst.Clock(nil))
	source, reason := e.ClockSource()
	if source != clockSourcePHC {
		t.Fatalf("ClockSource() = %q after the configured clock recovered, want %q", source, clockSourcePHC)
	}
	if reason != "" {
		t.Fatalf("ClockSource() reason = %q, want empty once the clock is readable again", reason)
	}
}

// TestPhcClockGetTimeCallCost measures phcClockGetTime's own per-call
// cost against an already-open [fakeClockReader] (in-memory, no real
// syscall): the callback's own overhead, isolated from clock_gettime's
// real cost, which a caller cannot measure portably without a live PHC
// device (see internal/agent/clock's own TestReadPHCCallCost, skipped
// off real hardware). GStreamer's own scheduling calls this on a hot
// path and must never block: a regression that adds an allocation, a
// lock, or a syscall per call would show up here as a large jump, not as
// a specific assertion this test hard-codes a threshold for.
func TestPhcClockGetTimeCallCost(t *testing.T) {
	reader := &fakeClockReader{base: time.Now(), failAfter: -1}
	e := &Engine{cfg: Config{Clock: reader}}

	const calls = 100000
	start := time.Now()
	for i := 0; i < calls; i++ {
		e.phcClockGetTime(gst.Clock(nil))
	}
	elapsed := time.Since(start)
	perCall := elapsed / calls
	t.Logf("phcClockGetTime: %d calls in %s (%s/call, in-memory reader, excludes real clock_gettime cost)", calls, elapsed, perCall)
	if perCall > time.Millisecond {
		t.Fatalf("phcClockGetTime took %s/call against an in-memory reader; want well under 1ms (GStreamer calls this from its own scheduling thread)", perCall)
	}
}

// TestPipewiresinkPipelineReachesPlaying builds a real pipeline against
// "pipewiresink" itself, skipped with its own reason when that plugin is
// not registered on this host: this development machine and most CI
// runners have no PipeWire daemon at all, matching this package's other
// environment-gated real-integration tests (newTestEngine's own doc
// comment).
func TestPipewiresinkPipelineReachesPlaying(t *testing.T) {
	gst.Init()
	if gst.ElementFactoryFind(pipewireAudioSinkFactoryForTest) == nil {
		t.Skip("skipping: pipewiresink is not registered on this host (no gstreamer1.0-pipewire plugin)")
	}
	cfg := testConfig(resolveByRuntimeFilename)
	cfg.SinkFactory = pipewireAudioSinkFactoryForTest
	cfg.SinkProperties = map[string]any{}
	e, err := New(cfg)
	if err != nil {
		t.Fatalf("New: unexpected structural config error: %v", err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if ok, reason := e.Available(); !ok {
		t.Skipf("skipping: pipewiresink is registered but this environment could not reach PLAYING against it: %s", reason)
	}
	if got := e.SinkBackend(); got != pipewireAudioSinkFactoryForTest {
		t.Fatalf("SinkBackend() = %q, want %q", got, pipewireAudioSinkFactoryForTest)
	}
}

// pipewireAudioSinkFactoryForTest names the GStreamer element factory
// this test builds against directly. Not [internal/agent's own
// pipewireAudioSinkFactory] constant (that lives one package over, and
// this package must not import internal/agent; it is the other
// direction of this codebase's dependency graph), but the identical
// string, "pipewiresink".
const pipewireAudioSinkFactoryForTest = "pipewiresink"
