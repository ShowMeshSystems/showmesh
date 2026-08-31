package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/gstengine"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestRunAudioRestoreRetryTickResolvesOnceTheDeviceEnumerates reproduces
// the defect this driver exists to fix: a session deferred at boot
// because the retained audio.node binding was redelivered before
// discovery had probe evidence for its route stays pending forever with
// nothing but a NEW audio.node.configure
// delivery to unstick it — this is exactly runAudioRestoreRetryTick's own
// reason to exist. The device "enumerates late" here as
// audioDiscoverer's own injected result changing mid-test, with no
// binding redelivery and no coordinator involved at all.
func TestRunAudioRestoreRetryTickResolvesOnceTheDeviceEnumerates(t *testing.T) {
	origDiscoverer := audioDiscoverer
	origNewEngine := newGstEngine
	t.Cleanup(func() {
		audioDiscoverer = origDiscoverer
		newGstEngine = origNewEngine
	})
	// Deliberately NOT envGstAudioSinkOverride=fakesink: a non-hardware
	// sink gets buildGstEngineConfig's own scaffolding fallback rate and
	// channel count regardless of probe evidence (audioengine.go), which
	// would skip the "no advertised probe evidence" refusal branch this
	// test exists to exercise. newGstEngine is overridden below instead,
	// so the eventual successful build still needs no real ALSA device.
	dir := t.TempDir()
	ctx := context.Background()
	const id = pkgaudio.SessionID("restore-retry-session")

	// "Boot 1": a real, available engine, session reaches Playing, its
	// Playing record lands on disk.
	m1 := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	ref := writeTestAudioAsset(t, dir, "retry.wav", "asset-retry", []byte("content-retry"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}

	// "Boot 2": no probe evidence for the route yet — the device has not
	// enumerated.
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	switchable := audio.NewSwitchableEngine()
	m2 := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount before any binding = %d, want 1", m2.PendingRestoreCount())
	}

	// Before any automatic attempt has run, the session must still
	// report a restore as queued -- attempts=0 is ambiguous on its own
	// between "nothing queued" and "queued, no attempt yet"; only
	// RestorePending resolves that.
	pending := findSessionSnapshot(t, m2.Snapshot(ctx), id)
	if !pending.RestorePending {
		t.Fatalf("RestorePending = false with PendingRestoreCount=1, want true")
	}
	if pending.RestoreAttempts != 0 || pending.RestoreNextAttempt != 0 {
		t.Fatalf("RestoreAttempts/RestoreNextAttempt before any automatic attempt = (%d, %v), want (0, 0)", pending.RestoreAttempts, pending.RestoreNextAttempt)
	}

	r := newAudioEngineRebuilder(ctx, dir, switchable, m2, nil)
	node := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}

	// The retained binding is redelivered once, before discovery has run:
	// refused, re-queued, still pending.
	r.rebuild(node)
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount after the refused bind = %d, want 1 (still pending, not consumed)", m2.PendingRestoreCount())
	}

	currentNode := func() (audioNodeConfig, bool) { return node, true }
	// availableFakeEngine, not a bare FakeEngine: resolution now also
	// requires the engine to report available, and FakeEngine always
	// reports false by design.
	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		return availableFakeEngine{audio.NewFakeEngine(time.Now)}, nil
	}

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }

	// Tick 1: still no probe evidence — an attempt runs (the driver
	// re-probes on every attempt) and refuses again.
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)
	if retry.attempts != 1 {
		t.Fatalf("attempts after tick 1 = %d, want 1", retry.attempts)
	}
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount after tick 1 = %d, want 1 (still no probe evidence)", m2.PendingRestoreCount())
	}
	attempts, next, reason := m2.RestoreRetryStatus(id, now)
	if attempts != 1 || next <= 0 || reason == "" {
		t.Fatalf("RestoreRetryStatus after tick 1 = (attempts=%d next=%v reason=%q), want (1, >0, non-empty)", attempts, next, reason)
	}

	// Tick 2, before the backoff delay has elapsed: no attempt.
	now = now.Add(2 * time.Second)
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)
	if retry.attempts != 1 {
		t.Fatalf("attempts after the too-early tick = %d, want 1 (backoff not yet elapsed)", retry.attempts)
	}

	// The device enumerates: discovery now has probe evidence for the
	// route. Advance past the first backoff delay (5s) and tick again.
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{Routes: []audio.RouteEvidence{{
			Device:      "hw:1,0",
			ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100},
		}}}
	}
	now = now.Add(5 * time.Second)
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)

	if got := m2.PendingRestoreCount(); got != 0 {
		t.Fatalf("PendingRestoreCount after the device enumerated = %d, want 0 (the automatic retry must resolve it with no binding redelivery)", got)
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts after resolution = %d, want 0 (reset)", retry.attempts)
	}
	attempts, next, reason = m2.RestoreRetryStatus(id, now)
	if attempts != 0 || next != 0 || reason != "" {
		t.Fatalf("RestoreRetryStatus after resolution = (attempts=%d next=%v reason=%q), want all zero", attempts, next, reason)
	}

	found := false
	for _, snap := range m2.Snapshot(ctx) {
		if snap.ID != id {
			continue
		}
		found = true
		if snap.State != pkgaudio.StatePlaying {
			t.Errorf("session state after automatic resolution = %q, want Playing", snap.State)
		}
		if snap.RestorePending {
			t.Errorf("RestorePending after automatic resolution = true, want false")
		}
	}
	if !found {
		t.Fatalf("session %s missing from m2's snapshot after resolution", id)
	}
}

// TestRunAudioRestoreRetryTickNeverTearsDownAnEngineAConcurrentBindJustFixed
// reproduces a TOCTOU: the driver's own decision to act must be atomic
// with respect to [audioEngineRebuilder]'s mu, not merely adjacent to
// it. The old shape read engineAvailable() BEFORE calling rebuild, so a
// genuine coordinator-delivered audio.node.configure that lands and
// resolves everything in the gap between that read and the driver's own
// rebuild call was invisible to the check: the driver, still trusting
// its now-stale "unavailable" answer, called rebuild anyway and tore the
// concurrent bind's own working engine back down -- failing every
// session that binding had just restored, for no benefit to the one
// session this driver was trying to help.
//
// The race is reproduced deterministically, not via goroutine timing:
// currentNode is the last callback the driver invokes before its own
// rebuild call, so hooking it to perform the "concurrent" bind
// reproduces the exact interleaving a real race could produce, without
// depending on scheduler luck. r.rebuildIfUnavailable is the fix under
// test: it checks the engine's own availability atomically with the
// decision to rebuild, under the same lock, so it sees the concurrent
// bind's own resolution rather than a stale answer.
func TestRunAudioRestoreRetryTickNeverTearsDownAnEngineAConcurrentBindJustFixed(t *testing.T) {
	origDiscoverer := audioDiscoverer
	origNewEngine := newGstEngine
	t.Cleanup(func() {
		audioDiscoverer = origDiscoverer
		newGstEngine = origNewEngine
	})
	dir := t.TempDir()
	ctx := context.Background()
	const id = pkgaudio.SessionID("toctou-victim-session")

	m1 := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	ref := writeTestAudioAsset(t, dir, "toctou.wav", "asset-toctou", []byte("content-toctou"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}

	// "Boot 2": no probe evidence yet, session deferred and pending.
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	switchable := audio.NewSwitchableEngine()
	m2 := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount before any binding = %d, want 1", m2.PendingRestoreCount())
	}

	r := newAudioEngineRebuilder(ctx, dir, switchable, m2, nil)
	node := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}
	// availableFakeEngine, not a bare FakeEngine: FakeEngine always
	// reports Available()==false by design (it proves the session state
	// machine, not that playback happened), which would make the
	// concurrent bind below indistinguishable from "still unavailable"
	// and this test would pass for the wrong reason on unfixed code too.
	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		return availableFakeEngine{audio.NewFakeEngine(time.Now)}, nil
	}

	// currentNode is the injection point: the FIRST time the driver calls
	// it (immediately before its own rebuild call), a genuine
	// coordinator-delivered binding finishes right here -- discovery now
	// has probe evidence, and a direct r.rebuild call (exactly what
	// audioBinding's onNode callback would do) binds a real, working
	// engine and resolves the pending session for real.
	raced := false
	currentNode := func() (audioNodeConfig, bool) {
		if !raced {
			raced = true
			audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
				return audio.Discovery{Routes: []audio.RouteEvidence{{
					Device:      "hw:1,0",
					ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100},
				}}}
			}
			r.rebuild(node)
		}
		return node, true
	}

	preRace := findSessionSnapshot(t, m2.Snapshot(ctx), id)
	if preRace.PositionKnown {
		t.Fatalf("precondition: session already has known position (implies a loaded handle) before the race, test would prove nothing")
	}

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }

	// The driver's own tick calls rebuildIfUnavailable, which does not
	// read availability until it holds r.mu -- by the time it checks,
	// currentNode has already triggered the race and the engine
	// genuinely is available, so it must decline to act.
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)

	if !raced {
		t.Fatalf("test setup failed: currentNode's race injection never ran")
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts after a skipped (already-available) outcome = %d, want 0", retry.attempts)
	}

	after := findSessionSnapshot(t, m2.Snapshot(ctx), id)
	if after.Fault == pkgaudio.FaultRouteChanged {
		t.Fatalf("session fault after the retry tick = %v, want no route-changed fault: the tick tore down the engine a concurrent bind had just established", after.Fault)
	}
	// State, not PositionKnown: a restore is itself a discontinuity, so
	// timingKnown (and so PositionKnown) starts false right after
	// resolution regardless of whether anything went wrong here — see
	// restoreOne's own Playing-branch comment. State staying Playing
	// with no route-changed fault is what proves the concurrent bind's
	// resolution survived; PositionKnown proves nothing either way.
	if after.State != pkgaudio.StatePlaying {
		t.Fatalf("session state after the retry tick = %v, want Playing: the concurrent bind's own resolution must survive the driver's own tick", after.State)
	}
}

// availableFakeEngine wraps [audio.FakeEngine], which always reports
// Available()==false by design, so a test needing a double that reports
// itself genuinely available (proving the retry driver's own atomicity
// against a REAL "the engine is fine now" answer, not merely against
// "still unavailable") has one. Matches
// internal/agent/cueactivationaudio_test.go's identical
// activationAvailableEngine wrapper.
type availableFakeEngine struct{ *audio.FakeEngine }

func (availableFakeEngine) Available() (bool, string) { return true, "" }

// findSessionSnapshot locates id's [audio.SessionSnapshot] within snaps,
// failing the test if absent.
func findSessionSnapshot(t *testing.T, snaps []audio.SessionSnapshot, id pkgaudio.SessionID) audio.SessionSnapshot {
	t.Helper()
	for _, snap := range snaps {
		if snap.ID == id {
			return snap
		}
	}
	t.Fatalf("no session snapshot found for %q", id)
	return audio.SessionSnapshot{}
}

// TestRunAudioRestoreRetryTickCountsNothingWhenRebuildReportsSkipped
// proves the driver defers entirely to rebuild's own Skipped decision
// (produced by [audioEngineRebuilder.rebuildIfUnavailable] when the
// engine already reports available, checked atomically -- see that
// method's own doc comment): a skipped attempt must count against
// nothing -- no attempt increment, no backoff advance, no status
// change -- exactly as if the tick had found nothing to do at all.
func TestRunAudioRestoreRetryTickCountsNothingWhenRebuildReportsSkipped(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const id = pkgaudio.SessionID("skipped-session")

	m1 := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	ref := writeTestAudioAsset(t, dir, "skipped.wav", "asset-skipped", []byte("content-skipped"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}

	switchable := audio.NewSwitchableEngine()
	m2 := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount before any binding = %d, want 1", m2.PendingRestoreCount())
	}

	calls := 0
	rebuild := func(audioNodeConfig) audioRebuildOutcome {
		calls++
		return audioRebuildOutcome{Skipped: true}
	}
	currentNode := func() (audioNodeConfig, bool) {
		return audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}, true
	}

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	runAudioRestoreRetryTick(m2, currentNode, rebuild, switchable.Available, func() time.Time { return now }, now, &retry, nil)

	if calls != 1 {
		t.Fatalf("rebuild was called %d times, want exactly 1 (the driver must still call it -- the skip decision lives inside rebuild, not before it)", calls)
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts after a skipped outcome = %d, want 0", retry.attempts)
	}
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount changed to %d after a skipped outcome, want unchanged at 1", m2.PendingRestoreCount())
	}
	attempts, next, reason := m2.RestoreRetryStatus(id, now)
	if attempts != 0 || next != 0 || reason != "" {
		t.Fatalf("RestoreRetryStatus after a skipped outcome = (attempts=%d next=%v reason=%q), want all zero", attempts, next, reason)
	}
}

// TestRunAudioRestoreRetryTickBoundsAutomaticAttempts proves the
// schedule is genuinely bounded: once every entry in
// audioRestoreRetryDelays has been used, further due ticks must not call
// rebuild again on their own — an operator-visible standing fault, not
// an unbounded retry loop, is what this driver leaves in place past that
// point. It also proves exhaustion is reported honestly:
// next_attempt_ms must go to zero once the schedule is exhausted, not
// keep counting down to an attempt that will never happen.
func TestRunAudioRestoreRetryTickBoundsAutomaticAttempts(t *testing.T) {
	calls := 0
	rebuild := func(audioNodeConfig) audioRebuildOutcome {
		calls++
		return audioRebuildOutcome{Attempted: true, Available: false, Reason: "still refused"}
	}
	currentNode := func() (audioNodeConfig, bool) {
		return audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}, true
	}

	dir := t.TempDir()
	ctx := context.Background()
	const id = pkgaudio.SessionID("bounded-session")
	m1 := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	ref := writeTestAudioAsset(t, dir, "bounded.wav", "asset-bounded", []byte("content-bounded"))
	m1.Apply(ctx, id, "inv-apply", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(ref)})
	if r := m1.Start(ctx, id, "inv-start", 2); r.Outcome == pkgaudio.OutcomeRefused {
		t.Fatalf("start: unexpectedly refused: %+v", r)
	}
	switchable := audio.NewSwitchableEngine()
	m2 := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }

	// Drive one tick per scheduled delay, always landing exactly on (or
	// past) the due time, until the schedule is exhausted.
	for i, delay := range audioRestoreRetryDelays {
		runAudioRestoreRetryTick(m2, currentNode, rebuild, switchable.Available, nowFn, now, &retry, nil)
		if calls != i+1 {
			t.Fatalf("calls after scheduled attempt %d = %d, want %d", i+1, calls, i+1)
		}
		now = now.Add(delay)
	}

	// The last attempt just used up the schedule: exhaustion must be
	// reported immediately, not one tick later -- attempts stays at the
	// bound, but next_attempt_ms must already read zero.
	attempts, next, reason := m2.RestoreRetryStatus(id, now)
	if attempts != len(audioRestoreRetryDelays) {
		t.Fatalf("attempts after the schedule is exhausted = %d, want %d", attempts, len(audioRestoreRetryDelays))
	}
	if next != 0 {
		t.Fatalf("next_attempt after the schedule is exhausted = %v, want 0: a countdown to an attempt that will never happen is dishonest", next)
	}
	if reason == "" {
		t.Fatalf("last_reason after the schedule is exhausted is empty, want the final attempt's own reason preserved")
	}

	// Further due ticks must not call rebuild again, and must not
	// disturb the exhausted status already reported.
	callsAtBound := calls
	for i := 0; i < 3; i++ {
		now = now.Add(10 * time.Minute)
		runAudioRestoreRetryTick(m2, currentNode, rebuild, switchable.Available, nowFn, now, &retry, nil)
	}
	if calls != callsAtBound {
		t.Fatalf("rebuild was called %d more time(s) after the bounded schedule was exhausted, want 0 more", calls-callsAtBound)
	}
	attempts, next, _ = m2.RestoreRetryStatus(id, now)
	if attempts != len(audioRestoreRetryDelays) || next != 0 {
		t.Fatalf("RestoreRetryStatus after further idle ticks past exhaustion = (attempts=%d next=%v), want unchanged (%d, 0)", attempts, next, len(audioRestoreRetryDelays))
	}
}

// TestRunAudioRestoreRetryTickRebuildsAnUnavailableEngineWithNoPendingRestore
// proves a node with a delivered binding but no persisted session (so
// PendingRestoreCount stays 0) still gets an unavailable engine rebuilt:
// only the widened engine-availability trigger can drive this case.
func TestRunAudioRestoreRetryTickRebuildsAnUnavailableEngineWithNoPendingRestore(t *testing.T) {
	origDiscoverer := audioDiscoverer
	origNewEngine := newGstEngine
	t.Cleanup(func() {
		audioDiscoverer = origDiscoverer
		newGstEngine = origNewEngine
	})
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	dir := t.TempDir()
	ctx := context.Background()

	switchable := audio.NewSwitchableEngine()
	m2 := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if m2.PendingRestoreCount() != 0 {
		t.Fatalf("PendingRestoreCount with no persisted session = %d, want 0", m2.PendingRestoreCount())
	}
	if ok, _ := switchable.Available(); ok {
		t.Fatalf("precondition: switchable engine already available before any binding, test would prove nothing")
	}

	r := newAudioEngineRebuilder(ctx, dir, switchable, m2, nil)
	node := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}
	currentNode := func() (audioNodeConfig, bool) { return node, true }

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }

	// Tick 1: no pending restore, but the engine is unavailable -- the
	// widened trigger must still run an attempt. No probe evidence yet,
	// so the attempt refuses again.
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)
	if retry.attempts != 1 {
		t.Fatalf("attempts after tick 1 = %d, want 1 (zero pending restores must not stop this driver when the engine is unavailable)", retry.attempts)
	}
	if ok, _ := switchable.Available(); ok {
		t.Fatalf("engine reports available after tick 1, want still unavailable (no probe evidence yet)")
	}

	// The device enumerates: discovery now has probe evidence, and the
	// rebuild will bind a genuinely available engine.
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{Routes: []audio.RouteEvidence{{
			Device:      "hw:1,0",
			ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100},
		}}}
	}
	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		return availableFakeEngine{audio.NewFakeEngine(time.Now)}, nil
	}
	now = now.Add(5 * time.Second)
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)

	if ok, _ := switchable.Available(); !ok {
		t.Fatalf("engine reports unavailable after the device enumerated, want available")
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts after resolution = %d, want 0 (reset)", retry.attempts)
	}
}

// TestRunAudioRestoreRetryTickDoesNothingOnAHealthyNode proves a node
// with an available engine and no pending restore never counts an
// attempt.
func TestRunAudioRestoreRetryTickDoesNothingOnAHealthyNode(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	m2 := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if m2.PendingRestoreCount() != 0 {
		t.Fatalf("PendingRestoreCount on a fresh node = %d, want 0", m2.PendingRestoreCount())
	}

	calls := 0
	rebuild := func(audioNodeConfig) audioRebuildOutcome {
		calls++
		return audioRebuildOutcome{Skipped: true}
	}
	currentNode := func() (audioNodeConfig, bool) {
		return audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}, true
	}
	engineAvailable := func() (bool, string) { return true, "" }

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	for i := 0; i < 3; i++ {
		runAudioRestoreRetryTick(m2, currentNode, rebuild, engineAvailable, nowFn, now, &retry, nil)
		now = now.Add(10 * time.Minute)
	}

	if calls != 0 {
		t.Fatalf("rebuild was called %d time(s) on a healthy node, want 0", calls)
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts on a healthy node = %d, want 0", retry.attempts)
	}
}

// TestRunAudioRestoreRetryTickIdleNodeNeverCountsAnAttempt proves the
// "no binding yet" guard still stops this driver even though an unbound
// [audio.SwitchableEngine] reports unavailable on every idle node.
func TestRunAudioRestoreRetryTickIdleNodeNeverCountsAnAttempt(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	switchable := audio.NewSwitchableEngine()
	m2 := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}
	if ok, _ := switchable.Available(); ok {
		t.Fatalf("precondition: switchable engine reports available with no binding, test would prove nothing")
	}

	calls := 0
	rebuild := func(audioNodeConfig) audioRebuildOutcome {
		calls++
		return audioRebuildOutcome{}
	}
	currentNode := func() (audioNodeConfig, bool) { return audioNodeConfig{}, false }

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }
	for i := 0; i < 3; i++ {
		runAudioRestoreRetryTick(m2, currentNode, rebuild, switchable.Available, nowFn, now, &retry, nil)
		now = now.Add(10 * time.Minute)
	}

	if calls != 0 {
		t.Fatalf("rebuild was called %d time(s) with no binding accepted, want 0", calls)
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts with no binding accepted = %d, want 0", retry.attempts)
	}
}

// TestRunAudioRestoreRetryTickRecoversAnEngineThatBreaksMidShow proves the
// widened trigger also drives recovery when an engine that WAS available
// goes unavailable later with nothing pending, not only at boot.
func TestRunAudioRestoreRetryTickRecoversAnEngineThatBreaksMidShow(t *testing.T) {
	origDiscoverer := audioDiscoverer
	origNewEngine := newGstEngine
	t.Cleanup(func() {
		audioDiscoverer = origDiscoverer
		newGstEngine = origNewEngine
	})
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	dir := t.TempDir()
	ctx := context.Background()

	switchable := audio.NewSwitchableEngine()
	m2 := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	if err := m2.RestoreAll(ctx); err != nil {
		t.Fatalf("RestoreAll: %v", err)
	}

	// Healthy mid-show: the bound engine reports available.
	switchable.Set(availableFakeEngine{audio.NewFakeEngine(time.Now)})
	if ok, _ := switchable.Available(); !ok {
		t.Fatalf("precondition: engine not available before it breaks, test would prove nothing")
	}

	// The pipeline dies: Available() flips to false and stays false, with
	// nothing pending to signal it. A bare FakeEngine always reports
	// unavailable, so setting it stands in for that flip.
	switchable.Set(audio.NewFakeEngine(time.Now))
	if m2.PendingRestoreCount() != 0 {
		t.Fatalf("PendingRestoreCount with no session at all = %d, want 0", m2.PendingRestoreCount())
	}
	if ok, _ := switchable.Available(); ok {
		t.Fatalf("precondition: engine already available after it broke, test would prove nothing")
	}

	r := newAudioEngineRebuilder(ctx, dir, switchable, m2, nil)
	node := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}
	currentNode := func() (audioNodeConfig, bool) { return node, true }

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }

	// Tick 1: nothing pending, but the engine is unavailable -- the
	// widened trigger must still run an attempt. No probe evidence yet,
	// so the attempt refuses again.
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)
	if retry.attempts != 1 {
		t.Fatalf("attempts after tick 1 = %d, want 1 (an engine that broke mid-show must still be retried)", retry.attempts)
	}

	// The device enumerates again: discovery now has probe evidence, and
	// the rebuild will bind a genuinely available engine.
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		return audio.Discovery{Routes: []audio.RouteEvidence{{
			Device:      "hw:1,0",
			ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100},
		}}}
	}
	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		return availableFakeEngine{audio.NewFakeEngine(time.Now)}, nil
	}
	now = now.Add(5 * time.Second)
	runAudioRestoreRetryTick(m2, currentNode, r.rebuildIfUnavailable, switchable.Available, nowFn, now, &retry, nil)

	if ok, _ := switchable.Available(); !ok {
		t.Fatalf("engine reports unavailable after the rebuild, want available")
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts after resolution = %d, want 0 (reset)", retry.attempts)
	}
}
