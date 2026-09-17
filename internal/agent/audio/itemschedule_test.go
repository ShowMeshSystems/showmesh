package audio

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// perFileDecoder is a [Decoder] whose result can be set per filename and
// changed mid-test, so a test can prove a probe that once returned
// [MediaUnknown] later succeeds -- the "successor not ready yet" case
// [maybeStageNextItemLocked] retries.
type perFileDecoder struct {
	mu      sync.Mutex
	def     DecodeResult
	results map[string]DecodeResult
}

func newPerFileDecoder(def DecodeResult) *perFileDecoder {
	return &perFileDecoder{def: def, results: map[string]DecodeResult{}}
}

func (d *perFileDecoder) setResult(filename string, r DecodeResult) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.results[filename] = r
}

func (d *perFileDecoder) Decode(_ context.Context, path string) DecodeResult {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r, ok := d.results[filepath.Base(path)]; ok {
		return r
	}
	return d.def
}

// knownDurationResult is a ready, known-duration decode result, matching
// staticDecoder's own shape but per-file via [perFileDecoder].
func knownDurationResult(d time.Duration) DecodeResult {
	return DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/wav", Decoded: true,
		Discoverer: DiscovererEvidence{Ran: true, Duration: d},
	}
}

// unknownDurationResult is a genuinely READY result with no known
// duration (mediaprobe.go ruling 5: gst-discoverer-1.0 unresolvable on
// this node), never a fault -- the case R2's decoder-end fallback covers.
func unknownDurationResult() DecodeResult {
	return DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/wav", Decoded: true,
		Discoverer: DiscovererEvidence{Ran: false},
	}
}

// unavailableResult is [MediaUnknown]: the probe itself could not run
// right now, a transient/retryable state, never a fault.
func unavailableResult(reason string) DecodeResult {
	return DecodeResult{Available: false, Reason: reason}
}

// scheduleTestEngine wraps [FakeEngine] and lets a test decouple an
// item's own DECODED (engine-clock) completion from the duration this
// package scheduled from: Load's own duration argument is overridden per
// asset id, while the probe evidence the schedule reads never changes.
// It also records every Load/Start call, in order, so a test can prove
// exactly when (which watch tick) an engine call actually happened.
type scheduleTestEngine struct {
	*FakeEngine
	mu        sync.Mutex
	overrides map[string]time.Duration
	loads     []EngineHandle
	starts    []EngineHandle
}

func newScheduleTestEngine(now func() time.Time) *scheduleTestEngine {
	return &scheduleTestEngine{FakeEngine: NewFakeEngine(now), overrides: map[string]time.Duration{}}
}

func (e *scheduleTestEngine) Available() (bool, string) { return true, "" }

func (e *scheduleTestEngine) setOverride(assetID string, actual time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.overrides[assetID] = actual
}

func (e *scheduleTestEngine) Load(ctx context.Context, handle EngineHandle, media pkgaudio.MediaRef, duration time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	if d, ok := e.overrides[media.AssetID]; ok {
		duration = d
	}
	e.loads = append(e.loads, handle)
	e.mu.Unlock()
	return e.FakeEngine.Load(ctx, handle, media, duration)
}

func (e *scheduleTestEngine) Start(ctx context.Context, handle EngineHandle, position time.Duration) (EngineObservation, error) {
	e.mu.Lock()
	e.starts = append(e.starts, handle)
	e.mu.Unlock()
	return e.FakeEngine.Start(ctx, handle, position)
}

func (e *scheduleTestEngine) startCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.starts)
}

func (e *scheduleTestEngine) loadCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.loads)
}

// boundaryFixture is a scheduled playlist session against a fake media
// clock, a controllable per-file decoder, and an engine that records
// every Load/Start call and can decouple its own completion timing from
// the scheduled duration.
type boundaryFixture struct {
	m      *Manager
	engine *scheduleTestEngine
	clk    *clock
	media  *fakeClockSource
	dec    *perFileDecoder
	logBuf *bytes.Buffer
	id     pkgaudio.SessionID
	dir    string
}

func newBoundaryFixture(t *testing.T) *boundaryFixture {
	t.Helper()
	c := newClock(time.Unix(1_700_000_000, 0))
	dir := t.TempDir()
	engine := newScheduleTestEngine(c.now)
	dec := newPerFileDecoder(knownDurationResult(2 * time.Second))
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	m := NewManager(engine, NewFileSessionStore(dir), dir, dec, c.now, logger)
	media := newFakeClockSource(time.Unix(4_000_000_000, 0))
	m.SetClockSource(media)

	const id = pkgaudio.SessionID("bed-1")
	return &boundaryFixture{m: m, engine: engine, clk: c, media: media, dec: dec, logBuf: &logBuf, id: id, dir: dir}
}

// applyPlaylist applies a playlist of items (each i*2s apart in the
// asset's default duration, overridable via f.dec) with the given repeat
// mode and item transition.
func (f *boundaryFixture) applyPlaylist(t *testing.T, items []pkgaudio.PlaylistItem, repeat pkgaudio.RepeatMode, transition pkgaudio.ItemTransition) {
	t.Helper()
	pl := pkgaudio.PlaylistRef{
		OwnerKind: "show", OwnerID: "night-session", OwnerRevision: 1,
		Repeat: repeat, Resume: pkgaudio.ResumePolicyRestart, RequestedTransition: transition,
		Items: items,
	}
	if out := f.m.Apply(context.Background(), f.id, "inv-apply", 1, pkgaudio.ApplyRequest{Playlist: pkgaudio.SetField(pl)}); out.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("Apply refused: %s", out.Reason)
	}
}

func (f *boundaryFixture) item(t *testing.T, itemID, filename, assetID string, index int) pkgaudio.PlaylistItem {
	t.Helper()
	ref := writeTestAsset(t, f.dir, filename, assetID, []byte(itemID))
	return pkgaudio.PlaylistItem{ItemID: itemID, Index: index, Media: ref}
}

// startAt starts f's session at lead ahead of the current media instant,
// advancing the media clock to T0 so the call does not wait in real
// time, mirroring [scheduledFixture.startScheduled].
func (f *boundaryFixture) startAt(t *testing.T, lead time.Duration) (pkgaudio.OutcomeResult, time.Time) {
	t.Helper()
	t0 := f.media.Now(context.Background()).Time.Add(lead)
	before := f.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.StartAt(context.Background(), f.id, "inv-start", 2, t0.UnixNano())
	}()
	f.media.waitForReads(t, before+1)
	f.media.advance(lead)
	select {
	case out := <-done:
		return out, t0
	case <-time.After(10 * time.Second):
		t.Fatal("StartAt never returned after the media clock reached T0")
		return pkgaudio.OutcomeResult{}, t0
	}
}

// resumeAt resumes f's session at instant t0 on the media clock,
// blocking (via a background goroutine) exactly like startAt. point is
// forwarded to [Manager.ResumeAt] verbatim; nil means no override.
func (f *boundaryFixture) resumeAt(t *testing.T, invocation pkgaudio.InvocationID, revision pkgaudio.Revision, t0 time.Time, point *ResumePoint) pkgaudio.OutcomeResult {
	t.Helper()
	before := f.media.reads()
	done := make(chan pkgaudio.OutcomeResult, 1)
	go func() {
		done <- f.m.ResumeAt(context.Background(), f.id, invocation, revision, t0.UnixNano(), point)
	}()
	f.media.waitForReads(t, before+1)
	lead := t0.Sub(f.media.Now(context.Background()).Time)
	if lead > 0 {
		f.media.advance(lead)
	}
	select {
	case out := <-done:
		return out
	case <-time.After(10 * time.Second):
		t.Fatal("ResumeAt never returned after the media clock reached its instant")
		return pkgaudio.OutcomeResult{}
	}
}

func (f *boundaryFixture) session(t *testing.T) *Session {
	t.Helper()
	s, ok := f.m.get(f.id)
	if !ok {
		t.Fatal("session not found")
	}
	return s
}

func (f *boundaryFixture) currentItemID(t *testing.T) string {
	t.Helper()
	s := f.session(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentItemID
}

func (f *boundaryFixture) state(t *testing.T) pkgaudio.State {
	t.Helper()
	s := f.session(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// TestScheduledItemBoundaryIgnoresAnEarlyDecoderEnd proves R2: a two-item
// playlist's second item starts exactly at T+d0 on the media clock, even
// though this node's own decoder reports item-a's playback finished well
// before that.
func TestScheduledItemBoundaryIgnoresAnEarlyDecoderEnd(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(3*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	// The decoder reports item-a's own playback complete after just 1s of
	// engine-clock time, well short of its scheduled 3s.
	f.engine.setOverride("asset-a", 1*time.Second)

	out, _ := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	// Advance the engine's own clock past item-a's overridden 1s decode
	// length, and the media clock to just short of the boundary. A tick
	// here must not advance: the decoder says complete, but the boundary
	// has not arrived yet. It must, however, already have LOADED item-b
	// onto the engine ahead of the boundary (R2's "prepared early enough
	// to be ready at its boundary"): one Load for item-a's own initial
	// prepare, plus one for item-b staged ahead of time.
	f.clk.advance(1500 * time.Millisecond)
	f.media.advance(3*time.Second - 100*time.Millisecond)
	f.m.watchTick(ctx)
	if got := f.engine.startCount(); got != 1 {
		t.Fatalf("Start call count before the boundary = %d, want 1 (must not have advanced early)", got)
	}
	if got := f.currentItemID(t); got != "item-a" {
		t.Fatalf("current item before the boundary = %q, want item-a", got)
	}
	if got := f.engine.loadCount(); got != 2 {
		t.Fatalf("Load call count before the boundary = %d, want 2 (item-b must already be staged ahead of the boundary)", got)
	}

	// Now reach exactly T+d0.
	f.media.advance(100 * time.Millisecond)
	f.m.watchTick(ctx)
	if got := f.engine.startCount(); got != 2 {
		t.Fatalf("Start call count at the boundary = %d, want 2", got)
	}
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item at the boundary = %q, want item-b", got)
	}
	if got := f.state(t); got != pkgaudio.StatePlaying {
		t.Fatalf("state at the boundary = %q, want playing", got)
	}
	// No further Load: the boundary promoted the already-staged handle
	// rather than re-preparing it.
	if got := f.engine.loadCount(); got != 2 {
		t.Fatalf("Load call count at the boundary = %d, want 2 (no re-prepare of an already-staged item)", got)
	}
}

// TestScheduledItemBoundaryIgnoresALateDecoderEnd is the other half of
// R2's requirement: item-a's own decoder has NOT reported completion by
// T+d0 (it is still "playing" by engine evidence), but the boundary
// still cuts it off and starts item-b on time.
func TestScheduledItemBoundaryIgnoresALateDecoderEnd(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(3*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	// The decoder never finishes within this test's own time budget.
	f.engine.setOverride("asset-a", time.Hour)

	out, _ := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	f.clk.advance(500 * time.Millisecond) // decoder still nowhere near done
	f.media.advance(3 * time.Second)      // but the boundary has arrived
	f.m.watchTick(ctx)

	if got := f.engine.startCount(); got != 2 {
		t.Fatalf("Start call count at the boundary = %d, want 2 (a late decoder must not block the transition)", got)
	}
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item at the boundary = %q, want item-b", got)
	}
}

// TestScheduledItemBoundarySameForGaplessTransition proves this
// repository's gapless/crossfade finding: neither the Engine interface
// nor gstengine implements a continuous multi-item sample timeline, so a
// playlist tagged Gapless is governed by the exact same scheduled
// boundary rule as Sequential -- ItemTransition is never read by
// anything in this package.
func TestScheduledItemBoundarySameForGaplessTransition(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(3*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionGapless)

	out, _ := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	f.media.advance(3*time.Second - 50*time.Millisecond)
	f.m.watchTick(ctx)
	if got := f.engine.startCount(); got != 1 {
		t.Fatalf("Start call count before the boundary = %d, want 1", got)
	}

	f.media.advance(50 * time.Millisecond)
	f.m.watchTick(ctx)
	if got := f.engine.startCount(); got != 2 {
		t.Fatalf("Start call count at the boundary = %d, want 2 (Gapless follows the same scheduled boundary rule as Sequential)", got)
	}
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item at the boundary = %q, want item-b", got)
	}
}

// TestScheduledItemBoundarySameForCrossfadeTransition is
// TestScheduledItemBoundarySameForGaplessTransition's own crossfade
// case: RequestedTransition is never read anywhere in this package's
// scheduling path, so Crossfade follows the identical scheduled boundary
// rule too.
func TestScheduledItemBoundarySameForCrossfadeTransition(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(3*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionCrossfade)

	out, _ := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	f.media.advance(3*time.Second - 50*time.Millisecond)
	f.m.watchTick(ctx)
	if got := f.engine.startCount(); got != 1 {
		t.Fatalf("Start call count before the boundary = %d, want 1", got)
	}

	f.media.advance(50 * time.Millisecond)
	f.m.watchTick(ctx)
	if got := f.engine.startCount(); got != 2 {
		t.Fatalf("Start call count at the boundary = %d, want 2 (Crossfade follows the same scheduled boundary rule as Sequential)", got)
	}
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item at the boundary = %q, want item-b", got)
	}
}

// TestScheduledItemNotReadyByBoundaryStartsWhenReady proves R2's "the
// next item is prepared early enough" fallback: a successor whose probe
// cannot run yet at the boundary does not fail the session, and does not
// start early either -- it starts on the first tick it becomes ready,
// reported as late with the session's own item gap signals and a warn
// log.
func TestScheduledItemNotReadyByBoundaryStartsWhenReady(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(1*time.Second))
	f.dec.setResult("b.wav", unavailableResult("decoder busy"))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	out, _ := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	// Reach the boundary while item-b's probe is still unavailable.
	f.media.advance(1 * time.Second)
	f.m.watchTick(ctx)
	if got := f.engine.startCount(); got != 1 {
		t.Fatalf("Start call count while the successor is not ready = %d, want 1 (must not have failed or skipped)", got)
	}
	if got := f.state(t); got != pkgaudio.StatePlaying {
		t.Fatalf("state while the successor is not ready = %q, want playing (not failed)", got)
	}

	// The decoder becomes available, a little later.
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.media.advance(200 * time.Millisecond)
	f.m.watchTick(ctx)

	if got := f.engine.startCount(); got != 2 {
		t.Fatalf("Start call count once ready = %d, want 2", got)
	}
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item once ready = %q, want item-b", got)
	}

	s := f.session(t)
	s.mu.Lock()
	gapKnown, gap := s.gapKnown, s.gap
	s.mu.Unlock()
	if !gapKnown {
		t.Fatal("gap signal not known after a late scheduled transition")
	}
	if gap < 200*time.Millisecond || gap > 210*time.Millisecond {
		t.Fatalf("gap (lateness) = %v, want ~200ms", gap)
	}
	if !strings.Contains(f.logBuf.String(), "late") {
		t.Fatalf("no warn log naming the lateness; log = %q", f.logBuf.String())
	}
}

// TestScheduledBoundaryMissedWarnsWhenSuccessorNeverReady proves a
// boundary reached with a durably not-ready successor (its probe keeps
// returning MediaUnknown, never resolving) still leaves log evidence on
// the very first tick that finds it stuck, and reports the gap as
// unknown rather than a stale or fabricated value while it waits.
func TestScheduledBoundaryMissedWarnsWhenSuccessorNeverReady(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(1*time.Second))
	f.dec.setResult("b.wav", unavailableResult("decoder busy"))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	out, _ := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	// Reach the boundary; item-b's probe never resolves within this test.
	f.media.advance(1 * time.Second)
	f.m.watchTick(ctx)

	if !strings.Contains(f.logBuf.String(), "not ready") {
		t.Fatalf("no warn log for a missed boundary whose successor is still not ready; log = %q", f.logBuf.String())
	}
	if got := f.state(t); got != pkgaudio.StatePlaying {
		t.Fatalf("state while durably stuck on a not-ready successor = %q, want playing (not failed)", got)
	}

	s := f.session(t)
	s.mu.Lock()
	gapKnown, gapReason := s.gapKnown, s.gapReason
	s.mu.Unlock()
	if gapKnown {
		t.Fatal("gap reported known while stuck on a not-ready successor")
	}
	if gapReason == "" {
		t.Fatal("gap reason empty while stuck on a not-ready successor, want the current lateness stated")
	}
}

// TestScheduledItemUnknownDurationFallsBackToDecoderEnd proves R2's
// other fallback: an item whose duration this node never learned is
// advanced by the ordinary decoder-end path, reported with a reason
// naming duration_unknown, and the schedule re-anchors for the item
// after it.
func TestScheduledItemUnknownDurationFallsBackToDecoderEnd(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", unknownDurationResult())
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	// A ready-but-unknown-duration item's own probe.Duration is zero, so
	// the engine is told to decouple its own completion timing rather
	// than never finishing (FakeEngine never auto-completes a zero
	// duration handle).
	f.engine.setOverride("asset-a", 2*time.Second)

	out, _ := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	s := f.session(t)
	s.mu.Lock()
	if s.schedule == nil || s.schedule.boundaryKnown {
		s.mu.Unlock()
		t.Fatal("schedule reports a known boundary for a duration-unknown item")
	}
	s.mu.Unlock()

	f.clk.advance(2500 * time.Millisecond)
	f.media.advance(2500 * time.Millisecond)
	f.m.watchTick(ctx)

	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item after decoder-end fallback = %q, want item-b", got)
	}
	if !strings.Contains(f.logBuf.String(), "duration unknown") {
		t.Fatalf("no warn log naming the duration-unknown fallback; log = %q", f.logBuf.String())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schedule == nil {
		t.Fatal("schedule was dropped entirely after a duration-unknown fallback; want a best-effort re-anchor")
	}
	if !s.schedule.boundaryKnown {
		t.Fatal("schedule did not re-learn a boundary for item-b, whose own duration is known")
	}
}

// TestScheduledPlaylistRepeatWrapsBoundaryArithmetic proves R2's repeat
// case: a repeating playlist's boundary keeps accumulating item
// durations across the lap, never resetting to the playlist's own start
// instant.
func TestScheduledPlaylistRepeatWrapsBoundaryArithmetic(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(2*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(3*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatPlaylist, pkgaudio.ItemTransitionSequential)

	out, t0 := f.startAt(t, 20*time.Millisecond)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("StartAt = %q (%s), want started", out.Outcome, out.Reason)
	}

	// Lap 1 -> item-b at T0+2s.
	f.media.advance(2 * time.Second)
	f.m.watchTick(ctx)
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item after the first boundary = %q, want item-b", got)
	}

	// Lap 2 (wrap) -> item-a again at T0+2s+3s, never reset to T0.
	f.media.advance(3 * time.Second)
	f.m.watchTick(ctx)
	if got := f.currentItemID(t); got != "item-a" {
		t.Fatalf("current item after the wrap = %q, want item-a", got)
	}

	s := f.session(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	wantBoundary := t0.Add(2 * time.Second).Add(3 * time.Second).Add(2 * time.Second)
	if !s.schedule.boundaryKnown || !s.schedule.boundaryAt.Equal(wantBoundary) {
		t.Fatalf("boundary after the wrap = %v (known=%v), want %v", s.schedule.boundaryAt, s.schedule.boundaryKnown, wantBoundary)
	}
}

// TestSessionStartedOnArrivalKeepsDecoderEndAdvance is the regression R2
// itself calls for: a session started with no scheduled instant never
// gains a schedule at all, and every item transition is driven by
// decoder end exactly as before this seam existed.
func TestSessionStartedOnArrivalKeepsDecoderEndAdvance(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(2*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(3*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	if out := f.m.Start(ctx, f.id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}

	s := f.session(t)
	s.mu.Lock()
	hasSchedule := s.schedule != nil
	s.mu.Unlock()
	if hasSchedule {
		t.Fatal("an on-arrival start established a schedule; it must stay decoder-end driven")
	}

	f.clk.advance(2500 * time.Millisecond)
	f.m.watchTick(ctx)
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item after decoder-end advance = %q, want item-b", got)
	}
}

// TestResumeAtWithPointResumesTheNamedPointAtTheInstant proves R1+R2
// together, amended: a scheduled resume at R carrying a resume point
// naming item k at position p resumes item k at p at R, and re-anchors
// the next boundary to R - p + d_k.
func TestResumeAtWithPointResumesTheNamedPointAtTheInstant(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(10*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	if out := f.m.Start(ctx, f.id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	if out := f.m.Pause(ctx, f.id, "inv-pause", 3); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Pause = %q (%s), want position", out.Outcome, out.Reason)
	}

	// The coordinator names a resume point on a DIFFERENT item (item-b)
	// than the one this node happens to be paused on (item-a), exactly
	// ADR-049 decision 4's amended cross-node push.
	const wantPosition = 1500 * time.Millisecond
	point := &ResumePoint{ItemID: "item-b", Index: 1, Position: wantPosition}

	r := f.media.Now(ctx).Time.Add(30 * time.Millisecond)
	out := f.resumeAt(t, "inv-resume", 4, r, point)
	if out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("ResumeAt = %q (%s), want started", out.Outcome, out.Reason)
	}
	if got := f.currentItemID(t); got != "item-b" {
		t.Fatalf("current item after the scheduled resume = %q, want item-b (the named resume point's item, not this node's own pause item)", got)
	}

	snap := f.m.Snapshot(ctx)
	var pos time.Duration
	var known bool
	for _, s := range snap {
		if s.ID == f.id {
			pos, known = s.Position, s.PositionKnown
		}
	}
	if !known || pos != wantPosition {
		t.Fatalf("resumed position = %v (known=%v), want %v", pos, known, wantPosition)
	}

	s := f.session(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	wantItemStart := r.Add(-wantPosition)
	wantBoundary := wantItemStart.Add(4 * time.Second)
	if s.schedule == nil {
		t.Fatal("no schedule established after a scheduled resume")
	}
	if !s.schedule.itemStartAt.Equal(wantItemStart) {
		t.Fatalf("re-anchored T_k = %v, want %v (R - p)", s.schedule.itemStartAt, wantItemStart)
	}
	if !s.schedule.boundaryKnown || !s.schedule.boundaryAt.Equal(wantBoundary) {
		t.Fatalf("re-anchored boundary = %v (known=%v), want %v (R - p + d_k)", s.schedule.boundaryAt, s.schedule.boundaryKnown, wantBoundary)
	}
}

// TestResumeAtWithMismatchedPointIsRefused proves a resume point naming
// an item that does not match this session's own playlist at that index
// is refused, naming both, rather than silently substituting whatever IS
// there.
func TestResumeAtWithMismatchedPointIsRefused(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	itemB := f.item(t, "item-b", "b.wav", "asset-b", 1)
	f.dec.setResult("a.wav", knownDurationResult(10*time.Second))
	f.dec.setResult("b.wav", knownDurationResult(4*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA, itemB}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	if out := f.m.Start(ctx, f.id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	if out := f.m.Pause(ctx, f.id, "inv-pause", 3); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Pause = %q (%s), want position", out.Outcome, out.Reason)
	}

	// Refused before ever resolving the schedule, so this calls ResumeAt
	// directly rather than through f.resumeAt, which blocks waiting for a
	// media-clock read that a refusal this early never makes.
	point := &ResumePoint{ItemID: "item-does-not-exist", Index: 1, Position: time.Second}
	r := f.media.Now(ctx).Time.Add(30 * time.Millisecond)
	out := f.m.ResumeAt(ctx, f.id, "inv-resume", 4, r.UnixNano(), point)
	if out.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("ResumeAt(mismatched point) = %q (%s), want refused", out.Outcome, out.Reason)
	}
	if !containsString(out.Reason, "item-does-not-exist") || !containsString(out.Reason, "index 1") {
		t.Fatalf("refusal reason = %q, want it to name the mismatched item and index", out.Reason)
	}
}

// TestResumeWithoutScheduledParamIsUnchanged proves R1: a plain Resume
// with no ParamScheduledAtNs behaves exactly as before this seam
// existed, and never establishes a schedule.
func TestResumeWithoutScheduledParamIsUnchanged(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	f.dec.setResult("a.wav", knownDurationResult(2*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	if out := f.m.Start(ctx, f.id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	if out := f.m.Pause(ctx, f.id, "inv-pause", 3); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Pause = %q (%s), want position", out.Outcome, out.Reason)
	}
	if out := f.m.Resume(ctx, f.id, "inv-resume", 4, nil); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Resume = %q (%s), want started", out.Outcome, out.Reason)
	}

	s := f.session(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.schedule != nil {
		t.Fatal("an unscheduled Resume established a schedule; it must stay decoder-end driven")
	}
}

// TestResumeAtPastInstantIsRefused mirrors StartAt's own show-continues
// rule for Resume: a resume instant this node's media clock has already
// passed is refused, never started late.
func TestResumeAtPastInstantIsRefused(t *testing.T) {
	f := newBoundaryFixture(t)
	ctx := context.Background()
	itemA := f.item(t, "item-a", "a.wav", "asset-a", 0)
	f.dec.setResult("a.wav", knownDurationResult(2*time.Second))
	f.applyPlaylist(t, []pkgaudio.PlaylistItem{itemA}, pkgaudio.RepeatNone, pkgaudio.ItemTransitionSequential)

	if out := f.m.Start(ctx, f.id, "inv-start", 2); out.Outcome != pkgaudio.OutcomeStarted {
		t.Fatalf("Start = %q (%s), want started", out.Outcome, out.Reason)
	}
	if out := f.m.Pause(ctx, f.id, "inv-pause", 3); out.Outcome != pkgaudio.OutcomePosition {
		t.Fatalf("Pause = %q (%s), want position", out.Outcome, out.Reason)
	}

	past := f.media.Now(ctx).Time.Add(-500 * time.Millisecond)
	out := f.m.ResumeAt(ctx, f.id, "inv-resume", 4, past.UnixNano(), nil)
	if out.Outcome != pkgaudio.OutcomeRefused {
		t.Fatalf("ResumeAt(past) outcome = %q (reason %q), want refused", out.Outcome, out.Reason)
	}
	if !containsString(out.Reason, pkgaudio.ReasonScheduledStartInPast) {
		t.Fatalf("refusal reason = %q, want it to carry %q", out.Reason, pkgaudio.ReasonScheduledStartInPast)
	}
	if got := f.state(t); got != pkgaudio.StatePaused {
		t.Fatalf("state after a refused scheduled resume = %q, want still paused", got)
	}
}
