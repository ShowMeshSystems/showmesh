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
// identity check passes — mirrors internal/agent/audio's own
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
// by the time Close ran — proof of ORDER, not merely that both events
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
// before the outgoing engine's device is released — never after, which
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

// TestRebuildLeavesNoEngineBoundWhenTheReplacementFailsToBuild proves
// the deliberate, tested choice for the window this reordering opens: if
// building the replacement fails AFTER the outgoing engine is already
// closed, the node is left with no engine bound (reporting
// [audio.SwitchableEngineNoBindingReason]) rather than a broken one, and
// the device the outgoing engine held is still released.
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
	if ok, reason := switchable.Available(); ok || reason != audio.SwitchableEngineNoBindingReason {
		t.Errorf("switchable.Available() after a failed rebuild = (%v, %q), want (false, %q): no broken engine left bound", ok, reason, audio.SwitchableEngineNoBindingReason)
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
