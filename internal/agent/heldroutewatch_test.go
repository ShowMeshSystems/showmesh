package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	agentconfig "github.com/showmeshsystems/showmesh/internal/agent/config"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

const (
	watchedRoute = "hw:CARD=M4,DEV=0"
	otherRoute   = "hw:CARD=Other,DEV=0"
)

func resetHeldRouteWatchState(t *testing.T) {
	t.Helper()
	for _, reset := range []func(){lastKnownGoodRoutes.reset, heldRouteMissCounts.reset, lastAudioDiscovery.reset} {
		reset()
		t.Cleanup(reset)
	}
}

// withAudioOutputListing makes every watch tick list exactly devices.
func withAudioOutputListing(t *testing.T, devices ...string) {
	t.Helper()
	orig := audioOutputLister
	audioOutputLister = func(context.Context, audio.Enumerator, audio.PipeWireEnumerator) audio.OutputListing {
		return audio.OutputListing{Devices: devices, Complete: true}
	}
	t.Cleanup(func() { audioOutputLister = orig })
}

// failOnAudioProbe fails the test if anything runs the probing discovery.
func failOnAudioProbe(t *testing.T) {
	t.Helper()
	orig := audioDiscoverer
	audioDiscoverer = func(context.Context, audio.Enumerator) audio.Discovery {
		t.Error("a watch tick ran the probing discovery, which opens every card")
		return audio.Discovery{}
	}
	t.Cleanup(func() { audioDiscoverer = orig })
}

type listingEnumerator struct{ devices []string }

func (e listingEnumerator) Devices(context.Context) ([]string, error) { return e.devices, nil }
func (e listingEnumerator) HasHardwareCards(context.Context) (bool, error) {
	return len(e.devices) > 0, nil
}

type absentPipeWire struct{}

func (absentPipeWire) Nodes(context.Context) ([]audio.PipeWireNode, bool, error) {
	return nil, false, nil
}

func TestHeldRouteRedetectNoticesAGoneDeviceWithoutAReconnect(t *testing.T) {
	resetHeldRouteWatchState(t)
	withAudioEngineAvailable(t, true, "")
	node := audioNodeConfig{ProgramRoute: watchedRoute, ProgramChannels: []int{1, 2}}
	withAudioEngineHeldNode(t, node, true)
	withAudioConfiguredNode(t, node, true)
	heldRouteMissPass(t, true)
	failOnAudioProbe(t)
	withAudioOutputListing(t, otherRoute)

	ticks := make(chan time.Time)
	passes := make(chan capability.Set)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeldRouteRedetect(ctx, ticks, func() (audioNodeConfig, bool) { return node, true }, func() {
			set, _ := redetectHeldRouteCapabilities(ctx)
			passes <- set
		})
	}()
	ticks <- time.Time{}
	if set := <-passes; !heldRouteAdvertised(set) {
		t.Fatal("one missed listing: want the route still advertised")
	}
	ticks <- time.Time{}
	set := <-passes
	cancel()
	<-done

	if heldRouteAdvertised(set) {
		t.Fatal("two missed listings: want the route no longer advertised")
	}
	local, _ := set.Lookup("audio.output.local")
	if missing, _ := local.Attributes["missingRoutes"].([]map[string]any); len(missing) != 1 || missing[0]["route"] != watchedRoute {
		t.Fatalf("missingRoutes = %v, want %s", local.Attributes["missingRoutes"], watchedRoute)
	}

	withAudioOutputListing(t, otherRoute, watchedRoute)
	set, _ = redetectHeldRouteCapabilities(context.Background())
	local, _ = set.Lookup("audio.output.local")
	if !heldRouteAdvertised(set) || local.Attributes["missingRoutes"] != nil {
		t.Fatalf("route listed again: advertised %v missingRoutes %v, want it back and none missing", heldRouteAdvertised(set), local.Attributes["missingRoutes"])
	}
}

func TestHeldRouteWatchTickOpensNoDevice(t *testing.T) {
	resetCapabilityCacheForTest(t)
	resetCapabilityGateForTest(t)
	resetHeldRouteWatchState(t)
	withAudioEngineAvailable(t, false, "the output did not open")
	withAudioConfiguredNode(t, audioNodeConfig{ProgramRoute: watchedRoute, ProgramChannels: []int{1, 2}}, true)
	detectedCapabilityCache.store(heldRouteMissPass(t, true))

	failOnAudioProbe(t)
	origEnum, origPW := audioEnumerator, audioPipeWireEnumerator
	audioEnumerator, audioPipeWireEnumerator = listingEnumerator{devices: []string{otherRoute}}, absentPipeWire{}
	t.Cleanup(func() { audioEnumerator, audioPipeWireEnumerator = origEnum, origPW })

	pub := newFakePublisher()
	for i := 0; i < 3; i++ {
		heldRouteRedetectTick(t, pub)
	}
	if calls := pub.snapshot(); len(calls) != 1 {
		t.Fatalf("published %d messages, want the one hello reporting the route missing from the listing alone", len(calls))
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
	resetHeldRouteWatchState(t)
	withAudioEngineAvailable(t, false, "the output did not open")
	withAudioConfiguredNode(t, audioNodeConfig{ProgramRoute: "hw:CARD=M4,DEV=0", ProgramChannels: []int{1, 2}}, true)

	render := capability.Capability{ID: "render.surface", Version: 1}
	detectedCapabilityCache.store(append(capability.Set{render}, heldRouteMissPass(t, true)...))
	pub := newFakePublisher()
	failOnAudioProbe(t)
	withAudioOutputListing(t, otherRoute, watchedRoute)

	for i := 0; i < 3; i++ {
		heldRouteRedetectTick(t, pub)
	}
	if calls := pub.snapshot(); len(calls) != 0 {
		t.Fatalf("published %d messages over three unchanged ticks, want none", len(calls))
	}

	withAudioOutputListing(t, otherRoute)
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
