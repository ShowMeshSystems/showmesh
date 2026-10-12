package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	agentconfig "github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

func TestHeldRouteRedetectNoticesAGoneDeviceWithoutAReconnect(t *testing.T) {
	lastKnownGoodRoutes.reset()
	heldRouteMissCounts.reset()
	t.Cleanup(lastKnownGoodRoutes.reset)
	t.Cleanup(heldRouteMissCounts.reset)
	withAudioEngineAvailable(t, true, "")
	const dev = "hw:CARD=M4,DEV=0"
	node := audioNodeConfig{ProgramRoute: dev, ProgramChannels: []int{1, 2}}
	withAudioEngineHeldNode(t, node, true)
	withAudioConfiguredNode(t, node, true)
	withAudioDiscoverer(t, audio.Discovery{EngineUsable: true, HasHardwareCards: true, HardwareEnumerated: true,
		Routes: []audio.RouteEvidence{{Device: "hw:CARD=Other,DEV=0", ProbeResult: audio.ProbeResult{Available: true, Channels: 2}}}})

	ticks := make(chan time.Time)
	passes := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeldRouteRedetect(ctx, ticks, func() (audioNodeConfig, bool) { return node, true }, func() {
			detectAudioCapabilities(ctx)
			passes <- struct{}{}
		})
	}()
	for i := 0; i < 2; i++ {
		ticks <- time.Time{}
		<-passes
	}
	cancel()
	<-done

	gone := heldRouteMissCounts.observe(nil, node, true, false)
	if !gone[dev] {
		t.Fatalf("after two periodic passes gone = %v, want %s missing", gone, dev)
	}
}

func TestHeldRouteRedetectSkipsWhenNothingIsHeld(t *testing.T) {
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	close(ticks)
	runHeldRouteRedetect(context.Background(), ticks, func() (audioNodeConfig, bool) { return audioNodeConfig{}, false }, func() {
		t.Fatal("detected with no held route")
	})
}

func heldRouteRedetectTick(t *testing.T, pub *fakePublisher) {
	t.Helper()
	scheduleHeldRouteRedetection(context.Background(), pub, agentconfig.Config{NodeID: "media-03"}, "boot-1", time.Now(), nil, discardLogger())
	waitForCapabilityGateIdle(t, 2*time.Second)
}

func TestHeldRouteRedetectionPublishesHelloOnlyWhenTheRoutesChange(t *testing.T) {
	resetCapabilityCacheForTest(t)
	resetCapabilityGateForTest(t)
	lastKnownGoodRoutes.reset()
	heldRouteMissCounts.reset()
	t.Cleanup(lastKnownGoodRoutes.reset)
	t.Cleanup(heldRouteMissCounts.reset)
	withAudioEngineAvailable(t, false, "the output did not open")
	withAudioConfiguredNode(t, audioNodeConfig{ProgramRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{1, 2}}, true)

	render := capability.Capability{ID: "render.surface", Version: 1}
	detectedCapabilityCache.store(append(capability.Set{render}, heldRouteMissPass(t, true)...))
	pub := newFakePublisher()

	for i := 0; i < 3; i++ {
		heldRouteRedetectTick(t, pub)
	}
	if calls := pub.snapshot(); len(calls) != 0 {
		t.Fatalf("published %d messages over three unchanged ticks, want none", len(calls))
	}

	heldRouteMissPass(t, false)
	for i := 0; i < 4; i++ {
		heldRouteRedetectTick(t, pub)
	}
	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("published %d messages after the route went missing and stayed missing, want exactly one hello", len(calls))
	}
	_, hello := decodeHello(t, calls[0].payload)
	if _, ok := hello.Capabilities.Lookup("render.surface"); !ok {
		t.Fatalf("hello capabilities = %v, want the last detected non-audio capabilities kept", hello.Capabilities)
	}
	local, _ := hello.Capabilities.Lookup("audio.output.local")
	if missing, _ := local.Attributes["missingRoutes"].([]any); len(missing) != 1 {
		t.Fatalf("hello missingRoutes = %v, want the unplugged route", local.Attributes["missingRoutes"])
	}
}

func TestHeldRouteRedetectionSkipsTheTickWhileADetectionIsRunning(t *testing.T) {
	resetCapabilityGateForTest(t)
	started, release := make(chan uint64), make(chan struct{})
	capabilityGate.trigger(func(gen uint64) {
		started <- gen
		<-release
	})
	gen := <-started

	if capabilityGate.triggerIfIdle(func(uint64) { t.Error("a tick ran while a detection was running") }) {
		t.Fatal("triggerIfIdle started a run while a detection was running")
	}
	if !capabilityGate.isCurrent(gen) {
		t.Fatal("the tick superseded the running detection, so its result would be dropped")
	}
	close(release)
}
