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

	r := newAudioEngineRebuilder(ctx, dir, switchable, m2, nil)
	node := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}

	// The retained binding is redelivered once, before discovery has run:
	// refused, re-queued, still pending.
	r.rebuild(node)
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount after the refused bind = %d, want 1 (still pending, not consumed)", m2.PendingRestoreCount())
	}

	currentNode := func() (audioNodeConfig, bool) { return node, true }
	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		return audio.NewFakeEngine(time.Now), nil
	}

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	nowFn := func() time.Time { return now }

	// Tick 1: still no probe evidence — an attempt runs (the driver
	// re-probes on every attempt) and refuses again.
	runAudioRestoreRetryTick(m2, switchable.Available, currentNode, r.rebuildResult, nowFn, now, &retry, nil)
	if retry.attempts != 1 {
		t.Fatalf("attempts after tick 1 = %d, want 1", retry.attempts)
	}
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount after tick 1 = %d, want 1 (still no probe evidence)", m2.PendingRestoreCount())
	}
	attempts, next, reason := m2.RestoreRetryStatus(now)
	if attempts != 1 || next <= 0 || reason == "" {
		t.Fatalf("RestoreRetryStatus after tick 1 = (attempts=%d next=%v reason=%q), want (1, >0, non-empty)", attempts, next, reason)
	}

	// Tick 2, before the backoff delay has elapsed: no attempt.
	now = now.Add(2 * time.Second)
	runAudioRestoreRetryTick(m2, switchable.Available, currentNode, r.rebuildResult, nowFn, now, &retry, nil)
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
	runAudioRestoreRetryTick(m2, switchable.Available, currentNode, r.rebuildResult, nowFn, now, &retry, nil)

	if got := m2.PendingRestoreCount(); got != 0 {
		t.Fatalf("PendingRestoreCount after the device enumerated = %d, want 0 (the automatic retry must resolve it with no binding redelivery)", got)
	}
	if retry.attempts != 0 {
		t.Fatalf("attempts after resolution = %d, want 0 (reset)", retry.attempts)
	}
	attempts, next, reason = m2.RestoreRetryStatus(now)
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
	}
	if !found {
		t.Fatalf("session %s missing from m2's snapshot after resolution", id)
	}
}

// TestRunAudioRestoreRetryTickNeverActsWhileTheEngineIsAlreadyAvailable
// proves the engineAvailable gate is load-bearing: when the bound engine
// already reports available, whatever is keeping a session pending is
// not a device problem, and the automatic retry must never call
// rebuild — doing so would invalidate every OTHER session already
// playing on that working engine for no benefit to the stuck one.
func TestRunAudioRestoreRetryTickNeverActsWhileTheEngineIsAlreadyAvailable(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	const id = pkgaudio.SessionID("already-available-session")

	m1 := audio.NewManager(audio.NewFakeEngine(time.Now), audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)
	ref := writeTestAudioAsset(t, dir, "already.wav", "asset-already", []byte("content-already"))
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
		return audioRebuildOutcome{Attempted: true, Available: true}
	}
	currentNode := func() (audioNodeConfig, bool) {
		return audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}, true
	}
	engineAvailable := func() (bool, string) { return true, "" }

	var retry audioRestoreRetryer
	now := time.Unix(1_700_000_000, 0)
	runAudioRestoreRetryTick(m2, engineAvailable, currentNode, rebuild, func() time.Time { return now }, now, &retry, nil)

	if calls != 0 {
		t.Fatalf("rebuild was called %d times while the engine already reported available, want 0", calls)
	}
	if m2.PendingRestoreCount() != 1 {
		t.Fatalf("PendingRestoreCount changed to %d without any rebuild call, want unchanged at 1", m2.PendingRestoreCount())
	}
}

// TestRunAudioRestoreRetryTickBoundsAutomaticAttempts proves the
// schedule is genuinely bounded: once every entry in
// audioRestoreRetryDelays has been used, further due ticks must not call
// rebuild again on their own — an operator-visible standing fault, not
// an unbounded retry loop, is what this driver leaves in place past that
// point.
func TestRunAudioRestoreRetryTickBoundsAutomaticAttempts(t *testing.T) {
	calls := 0
	rebuild := func(audioNodeConfig) audioRebuildOutcome {
		calls++
		return audioRebuildOutcome{Attempted: true, Available: false, Reason: "still refused"}
	}
	currentNode := func() (audioNodeConfig, bool) {
		return audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}, true
	}
	engineAvailable := func() (bool, string) { return false, "unavailable" }

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
		runAudioRestoreRetryTick(m2, engineAvailable, currentNode, rebuild, nowFn, now, &retry, nil)
		if calls != i+1 {
			t.Fatalf("calls after scheduled attempt %d = %d, want %d", i+1, calls, i+1)
		}
		now = now.Add(delay)
	}

	// The schedule is now exhausted: further due ticks must not call
	// rebuild again.
	callsAtBound := calls
	for i := 0; i < 3; i++ {
		now = now.Add(10 * time.Minute)
		runAudioRestoreRetryTick(m2, engineAvailable, currentNode, rebuild, nowFn, now, &retry, nil)
	}
	if calls != callsAtBound {
		t.Fatalf("rebuild was called %d more time(s) after the bounded schedule was exhausted, want 0 more", calls-callsAtBound)
	}
}
