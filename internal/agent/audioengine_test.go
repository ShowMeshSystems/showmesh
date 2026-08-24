package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	"github.com/showmeshsystems/showmesh/internal/agent/audio/gstengine"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// TestBuildGstEngineConfigUsesTheProbedDeviceChannelCount proves a route
// discovery already blessed does not get asked for fewer channels than
// it actually negotiated: a four-output interface bound to only 3
// program/LTC channels must still build a Config carrying ChannelCount
// 4, or the engine's interleave stage requests only 3 sink pads against
// a device that refuses fewer than its own negotiated channel count.
//
// Channels is deliberately set LOWER than the binding needs (2, not the
// device's real 4) and LTCChannels is what actually carries the wider
// evidence (4) -- exactly the shape [audio.Discover] produces for a
// device offering a channel range, where the unconstrained probe
// fixates low. A resolver that read RouteEvidence.Channels instead of
// LTCChannels would see 2, never widen past the binding's own floor of
// 3, and this test would fail with ChannelCount = 3.
func TestBuildGstEngineConfigUsesTheProbedDeviceChannelCount(t *testing.T) {
	withAudioDiscoverer(t, audio.Discovery{
		Routes: []audio.RouteEvidence{
			{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 48000}, LTCChannels: 4},
		},
	})
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	node := audioNodeConfig{
		ProgramRoute:    "hw:1,0",
		LTCRoute:        "hw:1,0",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		Revision:        1,
	}
	cfg, _, source := buildGstEngineConfig(context.Background(), t.TempDir(), node)
	if cfg.ChannelCount != 4 {
		t.Errorf("ChannelCount = %d, want 4 (this route's own explicit channel-count probe evidence, not just the bindings' highest index, and not the unconstrained probe's under-reported 2)", cfg.ChannelCount)
	}
	if source == "" {
		t.Error("channelCountSource is empty, want a stated reason")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestBuildGstEngineConfigCallsDiscoveryOnlyOnce proves the sample-rate
// and channel-count resolvers share one discovery run: audioDiscoverer
// shells out to real device probes, so calling it twice per rebuild
// would double every rebuild's real probing cost and let the two
// resolvers read the device's state from two different points in time.
func TestBuildGstEngineConfigCallsDiscoveryOnlyOnce(t *testing.T) {
	orig := audioDiscoverer
	calls := 0
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		calls++
		return audio.Discovery{
			Routes: []audio.RouteEvidence{
				{Device: "hw:1,0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100}},
			},
		}
	}
	t.Cleanup(func() { audioDiscoverer = orig })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	node := audioNodeConfig{
		ProgramRoute:    "hw:1,0",
		LTCRoute:        "hw:1,0",
		ProgramChannels: []int{1, 2},
		Revision:        1,
	}
	buildGstEngineConfig(context.Background(), t.TempDir(), node)
	if calls != 1 {
		t.Errorf("audioDiscoverer called %d times, want exactly 1", calls)
	}
}

// closeCountingEngine is an [audio.Engine] that records how often it was
// closed, so a rebind can be checked for releasing the engine it
// replaced rather than leaving it holding an output device.
type closeCountingEngine struct {
	audio.Engine
	closed int
}

func (e *closeCountingEngine) Close() error {
	e.closed++
	return nil
}

func TestRebindClosesTheEngineItReplaced(t *testing.T) {
	prev := &closeCountingEngine{Engine: audio.NewFakeEngine(time.Now)}
	closeReplacedEngine(prev, nil)
	if prev.closed != 1 {
		t.Fatalf("outgoing engine closed %d times, want 1: a gstengine holds its output device until it is closed", prev.closed)
	}
}

func TestRebindEngineHandsBackTheEngineItReplaced(t *testing.T) {
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(t.TempDir()), t.TempDir(), audio.RealDecoder{}, time.Now, nil)

	first := audio.NewFakeEngine(time.Now)
	if prev := mgr.RebindEngine(switchable, first, audio.RebindReasonEngineRebind); prev != nil {
		t.Fatalf("first rebind returned %v, want nil: nothing was bound before it", prev)
	}
	second := audio.NewFakeEngine(time.Now)
	prev := mgr.RebindEngine(switchable, second, audio.RebindReasonEngineRebind)
	if prev != audio.Engine(first) {
		t.Fatal("rebind did not hand back the engine it replaced, so nothing can release it")
	}
}

// TestBuildGstEngineConfigCarriesTheBindingsLTCChannel proves the LTC
// channel an operator declared actually reaches the engine that has to
// generate on it: a config built with LTCChannel zero can never run LTC,
// and nothing else in the agent would report that as a fault.
func TestBuildGstEngineConfigCarriesTheBindingsLTCChannel(t *testing.T) {
	orig := audioDiscoverer
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		return audio.Discovery{}
	}
	t.Cleanup(func() { audioDiscoverer = orig })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	node := audioNodeConfig{
		ProgramRoute:    "hw:1,0",
		LTCRoute:        "hw:1,0",
		ProgramChannels: []int{1, 2},
		LTCChannel:      3,
		Revision:        4,
	}
	cfg, _, _ := buildGstEngineConfig(context.Background(), t.TempDir(), node)
	if cfg.LTCChannel != 3 {
		t.Errorf("LTCChannel = %d, want 3", cfg.LTCChannel)
	}
	if cfg.ChannelCount != 3 {
		t.Errorf("ChannelCount = %d, want 3", cfg.ChannelCount)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// deviceHeldTracker simulates one exclusive ALSA device shared by every
// engine double a test builds: at most one engine may hold it at a time,
// matching a real alsasink opened for exclusive access.
type deviceHeldTracker struct {
	mu   sync.Mutex
	held bool
}

func (d *deviceHeldTracker) isHeld() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.held
}

func (d *deviceHeldTracker) open() {
	d.mu.Lock()
	d.held = true
	d.mu.Unlock()
}

func (d *deviceHeldTracker) close() {
	d.mu.Lock()
	d.held = false
	d.mu.Unlock()
}

// exclusiveDeviceEngine is an [audio.Engine] double standing in for a
// gstengine bound to an exclusive ALSA device: it marks tracker held
// while constructed, and releases it only on Close.
type exclusiveDeviceEngine struct {
	audio.Engine
	tracker *deviceHeldTracker
}

func (e *exclusiveDeviceEngine) Close() error {
	e.tracker.close()
	return nil
}

// TestRebuildReleasesTheDeviceBeforeProbingAndBuildingTheReplacement
// reproduces the defect: pushing a second audio.node revision to a
// node that is already playing must not probe the route or construct
// the replacement engine while the outgoing engine still holds the
// device. On unmodified code (probe, then build, then rebind, then
// close) this test fails, because the outgoing engine from the first
// rebuild is still open when the second rebuild probes and builds.
func TestRebuildReleasesTheDeviceBeforeProbingAndBuildingTheReplacement(t *testing.T) {
	origNewEngine := newGstEngine
	origDiscoverer := audioDiscoverer
	t.Cleanup(func() {
		newGstEngine = origNewEngine
		audioDiscoverer = origDiscoverer
	})
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	tracker := &deviceHeldTracker{}
	var probedWhileHeld, builtWhileHeld bool

	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		if tracker.isHeld() {
			builtWhileHeld = true
		}
		tracker.open()
		return &exclusiveDeviceEngine{Engine: audio.NewFakeEngine(time.Now), tracker: tracker}, nil
	}
	audioDiscoverer = func(ctx context.Context, enum audio.Enumerator) audio.Discovery {
		if tracker.isHeld() {
			probedWhileHeld = true
		}
		return audio.Discovery{Routes: []audio.RouteEvidence{{
			Device:      "hw:1,0",
			ProbeResult: audio.ProbeResult{Available: true, Channels: 2, Rate: 44100},
		}}}
	}

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	r := newAudioEngineRebuilder(dir, switchable, mgr, nil)

	node := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}
	r.rebuild(node) // first engine occupies the device; nothing to observe it busy yet.

	// Only the SECOND rebuild is under test: it must not observe the
	// first engine's device still held.
	probedWhileHeld, builtWhileHeld = false, false
	node2 := node
	node2.Revision = 2
	r.rebuild(node2)

	if probedWhileHeld {
		t.Error("the route probe ran while the outgoing engine still held the device: on real ALSA this observes EBUSY and silently falls back to 48000")
	}
	if builtWhileHeld {
		t.Error("the replacement engine was constructed while the outgoing engine still held the device: it is opened against a device that is not free")
	}
}

// fakeAssetDecoder is a canned [audio.Decoder] reporting every file as a
// ready, two-second audio asset, matching internal/agent/audio's own
// staticDecoder test helper (unexported there, so reproduced here).
type fakeAssetDecoder struct{}

func (fakeAssetDecoder) Decode(_ context.Context, _ string) audio.DecodeResult {
	return audio.DecodeResult{
		Available: true, TypeIdentified: true, MIMEType: "audio/wav", Decoded: true,
		Discoverer: audio.DiscovererEvidence{Ran: true, Duration: 2 * time.Second},
	}
}

// writeTestAudioAsset writes content under dir and returns a
// [pkgaudio.MediaRef] whose identity matches it, so ProbeAsset's real
// identity check passes; mirrors internal/agent/audio's own
// writeTestAsset test helper (unexported there).
func writeTestAudioAsset(t *testing.T, dir, filename, assetID string, content []byte) pkgaudio.MediaRef {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), content, 0o644); err != nil {
		t.Fatalf("write test asset: %v", err)
	}
	sum := sha256.Sum256(content)
	return pkgaudio.MediaRef{
		AssetID: assetID, ContentHash: "sha256:" + hex.EncodeToString(sum[:]),
		SizeBytes: int64(len(content)), RuntimeFilename: filename,
	}
}

// closeObservingEngine is an [audio.Engine] double whose Close reports
// whether the session it was built to observe had already been failed
// by the time Close ran: proof of ORDER, not merely that both events
// eventually happened.
type closeObservingEngine struct {
	audio.Engine
	observe func() (sessionAlreadyFailed bool)
	result  bool
	closed  bool
}

func (e *closeObservingEngine) Close() error {
	e.closed = true
	e.result = e.observe()
	return nil
}

// TestRebuildInvalidatesSessionsBeforeClosingTheOutgoingEngine proves a
// session with a live engine handle is failed against the OLD binding
// before the outgoing engine's device is released, never after, which
// would let a call in flight reach a closed engine with a handle it no
// longer owns.
func TestRebuildInvalidatesSessionsBeforeClosingTheOutgoingEngine(t *testing.T) {
	origNewEngine := newGstEngine
	t.Cleanup(func() { newGstEngine = origNewEngine })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, fakeAssetDecoder{}, time.Now, nil)

	const id = pkgaudio.SessionID("show-session")
	media := writeTestAudioAsset(t, dir, "a.wav", "asset-a", []byte("aaa"))
	ctx := context.Background()

	outgoing := &closeObservingEngine{Engine: audio.NewFakeEngine(time.Now)}
	outgoing.observe = func() bool {
		for _, s := range mgr.Snapshot(ctx) {
			if s.ID == id {
				return s.State == pkgaudio.StateFailed
			}
		}
		return false
	}
	switchable.Set(outgoing)

	mgr.Apply(ctx, id, "apply-1", 1, pkgaudio.ApplyRequest{Media: pkgaudio.SetField(media)})
	mgr.Start(ctx, id, "start-1", 2)

	precondition := mgr.Snapshot(ctx)
	if len(precondition) != 1 || precondition[0].State != pkgaudio.StatePlaying {
		t.Fatalf("precondition: session snapshot = %+v, want one session Playing", precondition)
	}

	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		return audio.NewFakeEngine(time.Now), nil
	}

	r := newAudioEngineRebuilder(dir, switchable, mgr, nil)
	r.rebuild(audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 5})

	if !outgoing.closed {
		t.Fatal("the outgoing engine was never closed")
	}
	if !outgoing.result {
		t.Error("session was not yet Failed when the outgoing engine was closed: invalidation did not happen before close")
	}
	final := mgr.Snapshot(ctx)
	if len(final) != 1 || final[0].State != pkgaudio.StateFailed || final[0].Fault != pkgaudio.FaultRouteChanged {
		t.Errorf("session after rebuild = %+v, want Failed with FaultRouteChanged", final)
	}
}

// TestRebuildLeavesNoEngineBoundWhenTheReplacementFailsToBuild guards a
// defensive backstop, not a path production can reach: [newGstEngine]'s
// doc comment explains that [gstengine.New] returns a non-nil error only
// when cfg fails cfg.Validate(), and rebuild only ever calls it with a
// cfg that already passed the identical structural Validate() earlier
// in the same call. This test forces the branch anyway (via the
// [newGstEngine] test seam) to prove the deliberate, tested choice for
// IF it is ever reached: the node is left with no engine bound
// (reporting [audio.SwitchableEngineRebindInProgressReason], since a
// binding was in fact delivered) rather than a broken one, and the
// device the outgoing engine held is still released.
func TestRebuildLeavesNoEngineBoundWhenTheReplacementFailsToBuild(t *testing.T) {
	origNewEngine := newGstEngine
	t.Cleanup(func() { newGstEngine = origNewEngine })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)

	outgoing := &closeCountingEngine{Engine: audio.NewFakeEngine(time.Now)}
	switchable.Set(outgoing)

	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		return nil, os.ErrInvalid
	}

	r := newAudioEngineRebuilder(dir, switchable, mgr, nil)
	r.rebuild(audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1})

	if outgoing.closed != 1 {
		t.Errorf("outgoing engine closed %d times, want 1: its device must still be released even though the replacement failed", outgoing.closed)
	}
	if ok, reason := switchable.Available(); ok || reason != audio.SwitchableEngineRebindInProgressReason {
		t.Errorf("switchable.Available() after a failed rebuild = (%v, %q), want (false, %q): a binding WAS delivered, so this must not read as never-configured, and no broken engine is left bound", ok, reason, audio.SwitchableEngineRebindInProgressReason)
	}
}

// TestRebuildRejectsAnInvalidBindingWithoutTouchingTheOutgoingEngine
// proves validation runs BEFORE anything is torn down: a binding this
// node structurally cannot build an engine from must be refused without
// closing (or otherwise disturbing) a working outgoing engine, since
// tearing one down for a binding that was never going to build costs a
// node its audio for nothing.
func TestRebuildRejectsAnInvalidBindingWithoutTouchingTheOutgoingEngine(t *testing.T) {
	origNewEngine := newGstEngine
	t.Cleanup(func() { newGstEngine = origNewEngine })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		t.Fatal("newGstEngine was called for a structurally invalid binding")
		return nil, nil
	}

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)

	outgoing := &closeCountingEngine{Engine: audio.NewFakeEngine(time.Now)}
	switchable.Set(outgoing)

	r := newAudioEngineRebuilder(dir, switchable, mgr, nil)
	// ProgramChannels is empty: cfg.Validate() must reject this before
	// anything else runs.
	r.rebuild(audioNodeConfig{ProgramRoute: "hw:1,0", Revision: 1})

	if outgoing.closed != 0 {
		t.Errorf("outgoing engine closed %d times, want 0: an invalid binding must never tear down a working engine", outgoing.closed)
	}
	if ok, reason := switchable.Available(); ok || reason != audio.FakeEngineUnavailableReason {
		t.Errorf("switchable.Available() after a rejected binding = (%v, %q), want (%v, %q): still delegating to the outgoing engine, not detached", ok, reason, false, audio.FakeEngineUnavailableReason)
	}
}

// TestAudioEngineRebuilderBindReleasesTheEngineItReplaces proves
// [audioEngineRebuilder.bind] honors [audio.SwitchableEngine.Set]'s own
// contract: the caller must close whatever engine Set's return value
// names, since a gstengine keeps holding its output device until
// something closes it. This is the regression a direct
// r.switchable.Set(engine) (discarding the return) introduced: the
// engine a bind replaces would never be released.
func TestAudioEngineRebuilderBindReleasesTheEngineItReplaces(t *testing.T) {
	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	r := newAudioEngineRebuilder(dir, switchable, mgr, nil)

	first := &closeCountingEngine{Engine: audio.NewFakeEngine(time.Now)}
	r.bind(first)
	if first.closed != 0 {
		t.Fatalf("bind closed the engine it just installed: closed=%d, want 0", first.closed)
	}

	second := &closeCountingEngine{Engine: audio.NewFakeEngine(time.Now)}
	r.bind(second)
	if first.closed != 1 {
		t.Errorf("bind did not release the engine it replaced: first.closed=%d, want 1", first.closed)
	}
}

// orderedCloseEngine is an [audio.Engine] double that records whether it
// was ever closed, safe for concurrent use: TestRebuildSerializesConcurrentRebuilds
// closes it from whichever goroutine's rebuild call displaces it.
type orderedCloseEngine struct {
	audio.Engine
	mu     sync.Mutex
	closed bool
}

func (e *orderedCloseEngine) Close() error {
	e.mu.Lock()
	e.closed = true
	e.mu.Unlock()
	return nil
}

func (e *orderedCloseEngine) isClosed() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.closed
}

// TestRebuildSerializesConcurrentRebuilds reproduces the regression a
// reviewer confirmed by running: two audio.node.configure deliveries
// reach audioBinding.onNode from different goroutines (onNode runs with
// its own lock released), so two rebuild calls can be genuinely
// concurrent. Without serialization, a slower rebuild whose probe or
// build takes longer can Set its engine AFTER a faster, later rebuild
// already Set its own, orphaning the faster rebuild's engine: it is
// never closed, and it keeps holding the ALSA device forever. This test
// engineers exactly that interleaving (first rebuild's build blocks,
// second rebuild's build is fast) and asserts both that the second
// rebuild cannot even reach its build step until the first one finishes
// (serialization), and that exactly one of the two built engines ends up
// closed (no orphan, no double-close).
func TestRebuildSerializesConcurrentRebuilds(t *testing.T) {
	origNewEngine := newGstEngine
	t.Cleanup(func() { newGstEngine = origNewEngine })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	r := newAudioEngineRebuilder(dir, switchable, mgr, nil)

	var mu sync.Mutex
	var built []*orderedCloseEngine
	release1 := make(chan struct{})
	build1Started := make(chan struct{})
	build2Started := make(chan struct{})

	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		mu.Lock()
		idx := len(built)
		e := &orderedCloseEngine{Engine: audio.NewFakeEngine(time.Now)}
		built = append(built, e)
		mu.Unlock()
		if idx == 0 {
			close(build1Started)
			<-release1
		} else {
			close(build2Started)
		}
		return e, nil
	}

	node1 := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 1}
	node2 := node1
	node2.Revision = 2

	done1 := make(chan struct{})
	go func() {
		defer close(done1)
		r.rebuild(node1)
	}()
	<-build1Started

	done2 := make(chan struct{})
	go func() {
		defer close(done2)
		r.rebuild(node2)
	}()

	select {
	case <-build2Started:
		t.Fatal("the second rebuild reached its own build step while the first rebuild's build was still in flight: rebuilds are not serialized")
	case <-time.After(200 * time.Millisecond):
	}

	close(release1)
	<-done1
	<-done2
	<-build2Started

	mu.Lock()
	defer mu.Unlock()
	if len(built) != 2 {
		t.Fatalf("built %d engines, want 2", len(built))
	}
	closedCount := 0
	for _, e := range built {
		if e.isClosed() {
			closedCount++
		}
	}
	if closedCount != 1 {
		t.Errorf("closed %d of 2 built engines, want exactly 1: 0 means an orphaned engine still holding the device, 2 means the surviving engine was wrongly closed too", closedCount)
	}
}

// raceCloseEngine is an [audio.Engine] double that counts how many times
// Close was called, safe for concurrent use:
// TestRebuildDropsAnOlderRevisionThatLostTheLockRace uses the count to
// prove the surviving engine is never double-closed and a dropped
// engine, if one was ever built, is not orphaned.
type raceCloseEngine struct {
	audio.Engine
	mu    sync.Mutex
	count int
}

func (e *raceCloseEngine) Close() error {
	e.mu.Lock()
	e.count++
	e.mu.Unlock()
	return nil
}

func (e *raceCloseEngine) closeCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.count
}

// TestRebuildDropsAnOlderRevisionThatLostTheLockRace pins the ordering
// invariant rebuild owns beyond plain mutual exclusion.
// [audioBinding.applyNode] records a newer revision and releases its own
// lock BEFORE calling onNode, so two deliveries (an older revision and a
// newer one) can call [audioEngineRebuilder.rebuild] from different
// goroutines. rebuild serializes on r.mu, but serialization alone does
// not order revisions: if the older revision's rebuild happens to
// acquire r.mu SECOND, after the newer revision's rebuild already bound
// its engine, it still runs to completion, closes the newer engine, and
// binds its own, leaving the node's [audio.SwitchableEngine] bound to a
// stale configuration.
//
// This test forces exactly that ordering, deterministically, with no
// dependence on scheduling for WHICH goroutine wins the lock: the newer
// revision's rebuild is started first and, because it is uncontested, is
// guaranteed to acquire r.mu before anything else runs. It then blocks
// inside the injected newGstEngine hook WHILE STILL HOLDING r.mu. The
// older revision's rebuild is only started after that point, so its own
// r.mu.Lock() call is guaranteed to block until the newer revision's
// rebuild finishes and releases r.mu, regardless of goroutine scheduling.
func TestRebuildDropsAnOlderRevisionThatLostTheLockRace(t *testing.T) {
	origNewEngine := newGstEngine
	t.Cleanup(func() { newGstEngine = origNewEngine })
	t.Setenv(envGstAudioSinkOverride, "fakesink")

	dir := t.TempDir()
	switchable := audio.NewSwitchableEngine()
	mgr := audio.NewManager(switchable, audio.NewFileSessionStore(dir), dir, audio.RealDecoder{}, time.Now, nil)
	r := newAudioEngineRebuilder(dir, switchable, mgr, nil)

	var mu sync.Mutex
	var built []*raceCloseEngine
	releaseNewer := make(chan struct{})
	newerBuildStarted := make(chan struct{})

	newGstEngine = func(cfg gstengine.Config) (audio.Engine, error) {
		mu.Lock()
		idx := len(built)
		e := &raceCloseEngine{Engine: audio.NewFakeEngine(time.Now)}
		built = append(built, e)
		mu.Unlock()
		if idx == 0 {
			close(newerBuildStarted)
			<-releaseNewer
		}
		return e, nil
	}

	older := audioNodeConfig{ProgramRoute: "hw:1,0", ProgramChannels: []int{1, 2}, Revision: 5}
	newer := older
	newer.Revision = 6

	doneNewer := make(chan struct{})
	go func() {
		defer close(doneNewer)
		r.rebuild(newer)
	}()
	// By the time newerBuildStarted closes, rebuild(newer) already holds
	// r.mu: it locks r.mu before ever calling newGstEngine. Starting the
	// older revision's rebuild only after this point guarantees it
	// blocks on r.mu.
	<-newerBuildStarted

	doneOlder := make(chan struct{})
	go func() {
		defer close(doneOlder)
		r.rebuild(older)
	}()

	close(releaseNewer)
	<-doneNewer
	<-doneOlder

	mu.Lock()
	n := len(built)
	newerEngine := built[0]
	var olderEngine *raceCloseEngine
	if n > 1 {
		olderEngine = built[1]
	}
	mu.Unlock()

	sentinel := audio.NewFakeEngine(time.Now)
	bound := switchable.Set(sentinel)

	if bound != audio.Engine(newerEngine) {
		t.Errorf("final bound engine is not revision 6's engine: revision 5's rebuild won the lock race second and overwrote it with a stale binding")
	}
	if got := newerEngine.closeCount(); got != 0 {
		t.Errorf("revision 6's engine was closed %d times, want 0: it is the engine that should still be bound", got)
	}
	if n != 1 {
		t.Errorf("built %d engines, want exactly 1: the dropped revision must return before it reaches the build step, not build an engine and then close it", n)
	}
	if olderEngine != nil && olderEngine.closeCount() != 1 {
		t.Errorf("revision 5's engine was built and closed %d times, want exactly 1: 0 orphans a device handle, more than 1 double-closes it", olderEngine.closeCount())
	}
}
