package agent

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// Two retained state messages of different kinds arriving back to back
// leave the node with two react calls in flight: registerWeatherDelay runs
// SetFromMessage inline and then starts each react in its own goroutine, so
// the delay's react can run after the cancel's. Either order must end on
// the kind the node now holds.

// newKindRaceTestOps is newCancelKindTestOps with both kinds' alert files
// present and an empty holder, so the state topic starts the delay.
func newKindRaceTestOps(t *testing.T) (*weatherDelayOperations, *weatherDelayRecordingEngine) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(weatherDelayBackground.Wait)
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)
	delayHash := writeAssetFixture(t, dir, "delay.wav", []byte("delay alert audio"))
	cancelHash := writeAssetFixture(t, dir, "cancel.wav", []byte("cancel alert audio"))
	holder := NewWeatherDelayHolder(dir, discardLogger())
	holder.rec.Plan = mqttproto.WeatherDelayPlan{
		RepeatCount: 10,
		Delay:       &mqttproto.WeatherDelayAlertAssetRef{AssetID: "delay-asset", ContentHash: delayHash, Filename: "delay.wav"},
		CancelNight: &mqttproto.WeatherDelayAlertAssetRef{AssetID: "cancel-asset", ContentHash: cancelHash, Filename: "cancel.wav"},
	}
	return &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}, engine
}

// kindRaceTransitions applies a delay message then a cancel message to the
// holder, returning both transitions still to be reacted to.
func kindRaceTransitions(t *testing.T, ops *weatherDelayOperations) (delayTransition, cancelTransition weatherDelayTransition) {
	t.Helper()
	plan := ops.holder.Current().Plan
	startedAt := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	delayMsg, err := mqttproto.NewWeatherDelayMessage(true, weatherdelay.KindDelay, startedAt, "op-1", 1, plan, startedAt)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage(delay): %v", err)
	}
	cancelMsg, err := mqttproto.NewWeatherDelayMessage(true, weatherdelay.KindCancelNight, startedAt, "op-1", 2, plan, startedAt)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage(cancelNight): %v", err)
	}
	delayTransition = ops.holder.SetFromMessage(delayMsg)
	cancelTransition = ops.holder.SetFromMessage(cancelMsg)
	if delayTransition != weatherDelayStarted || cancelTransition != weatherDelayKindChanged {
		t.Fatalf("transitions = %v, %v, want started then kindChanged", delayTransition, cancelTransition)
	}
	return delayTransition, cancelTransition
}

// assertOnlyTheCancelAlertPlays waits for the cancel alert to be the only
// alert session playing.
func assertOnlyTheCancelAlertPlays(t *testing.T, ops *weatherDelayOperations) {
	t.Helper()
	waitForAlertSessionPlaying(t, ops, weatherdelay.KindCancelNight, true)
	waitForAlertSessionPlaying(t, ops, weatherdelay.KindDelay, false)
}

func TestWeatherDelayDelayReactAfterACancelReactKeepsTheCancelAlert(t *testing.T) {
	ops, _ := newKindRaceTestOps(t)
	delayTransition, cancelTransition := kindRaceTransitions(t, ops)

	ops.react(context.Background(), cancelTransition, weatherdelay.KindCancelNight)
	ops.react(context.Background(), delayTransition, weatherdelay.KindDelay)

	if k := ops.holder.Current().Kind; k != weatherdelay.KindCancelNight {
		t.Fatalf("holder kind = %q, want cancelNight", k)
	}
	assertOnlyTheCancelAlertPlays(t, ops)
}

func TestWeatherDelayConcurrentKindReactsEndOnTheCancelAlert(t *testing.T) {
	ops, _ := newKindRaceTestOps(t)
	delayTransition, cancelTransition := kindRaceTransitions(t, ops)

	var wg sync.WaitGroup
	gate := make(chan struct{})
	for _, r := range []struct {
		transition weatherDelayTransition
		kind       string
	}{{delayTransition, weatherdelay.KindDelay}, {cancelTransition, weatherdelay.KindCancelNight}} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			ops.react(context.Background(), r.transition, r.kind)
		}()
	}
	close(gate)
	wg.Wait()

	assertOnlyTheCancelAlertPlays(t, ops)
}

// TestWeatherDelayThreeDeliveriesOfOneKindStartTheAlertOnce drives the
// three shapes a start arrives in at the same moment: the MQTT operation
// and the signed HTTP route both call startHeld, and the retained state
// topic calls react on whatever transition its message produced. Whichever
// wins, exactly one Start reaches the engine.
func TestWeatherDelayThreeDeliveriesOfOneKindStartTheAlertOnce(t *testing.T) {
	ops, engine := newKindRaceTestOps(t)
	plan := ops.holder.Current().Plan
	startedAt := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	msg, err := mqttproto.NewWeatherDelayMessage(true, weatherdelay.KindDelay, startedAt, "op-1", 1, plan, startedAt)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage(delay): %v", err)
	}

	deliveries := []func(){
		func() { ops.startHeld(context.Background(), weatherdelay.KindDelay, startedAt) },
		func() { ops.startHeld(context.Background(), weatherdelay.KindDelay, startedAt) },
		func() { ops.react(context.Background(), ops.holder.SetFromMessage(msg), weatherdelay.KindDelay) },
	}
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for _, deliver := range deliveries {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-gate
			deliver()
		}()
	}
	close(gate)
	wg.Wait()

	if got := startCount(engine); got != 1 {
		t.Fatalf("engine Start calls = %d, want exactly 1 (three deliveries of one kind must not restart or stack the alert)", got)
	}
	waitForAlertSessionPlaying(t, ops, weatherdelay.KindDelay, true)
}
