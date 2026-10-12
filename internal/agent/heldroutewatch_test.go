package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
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
