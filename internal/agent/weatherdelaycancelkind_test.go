package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// Covers the node's cancel-night rules: a wrong alert never keeps playing,
// and a delay start never downgrades a cancelled night.

func alertSessionPlaying(t *testing.T, ops *weatherDelayOperations) bool {
	t.Helper()
	for _, s := range ops.audioMgr.Snapshot(context.Background()) {
		if s.ID == weatherDelayAlertSessionID && s.State == pkgaudio.StatePlaying {
			return true
		}
	}
	return false
}

func newCancelKindTestOps(t *testing.T, withCancelRef, withCancelFile bool) (*weatherDelayOperations, *weatherDelayRecordingEngine) {
	t.Helper()
	t.Cleanup(weatherDelayBackground.Wait)
	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, engine := newWeatherDelayTestManager(t, dir, clock)
	delayHash := writeAssetFixture(t, dir, "delay.wav", []byte("delay alert audio"))
	plan := mqttproto.WeatherDelayPlan{
		RepeatCount: 10,
		Delay:       &mqttproto.WeatherDelayAlertAssetRef{AssetID: "delay-asset", ContentHash: delayHash, Filename: "delay.wav"},
	}
	if withCancelRef {
		hash := "0000000000000000000000000000000000000000000000000000000000000000"
		if withCancelFile {
			hash = writeAssetFixture(t, dir, "cancel.wav", []byte("cancel alert audio"))
		}
		plan.CancelNight = &mqttproto.WeatherDelayAlertAssetRef{AssetID: "cancel-asset", ContentHash: hash, Filename: "cancel.wav"}
	}
	holder := NewWeatherDelayHolder(dir, discardLogger())
	holder.rec.Plan = plan
	return &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}, engine
}

func TestWeatherDelayChangeToCancelWithNoCancelAlertStopsTheDelayAlert(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ref, file  bool
		wantReason string
	}{
		{"no cancel alert configured", false, false, "No alert sound is set for cancelNight. Set one to play an alert."},
		{"cancel alert file not on the node", true, false, "The alert sound cancel.wav is not on this node. Send it to this node to play it."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ops, _ := newCancelKindTestOps(t, tc.ref, tc.file)
			if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, time.Now()); !played {
				t.Fatalf("delay start did not play: %s", reason)
			}
			if !alertSessionPlaying(t, ops) {
				t.Fatal("delay alert is not playing before the change")
			}

			played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindCancelNight, time.Now())
			if played {
				t.Fatal("cancel start reported an alert playing with no cancel alert available")
			}
			if reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", reason, tc.wantReason)
			}
			if alertSessionPlaying(t, ops) {
				t.Fatal("the delay alert is still playing after the night was cancelled")
			}
			if got := ops.holder.Current().Kind; got != weatherdelay.KindCancelNight {
				t.Fatalf("holder kind = %q, want cancelNight", got)
			}
		})
	}
}

func TestWeatherDelayStateMessageChangeToCancelWithNoCancelAlertStopsTheDelayAlert(t *testing.T) {
	ops, _ := newCancelKindTestOps(t, false, false)
	plan := ops.holder.Current().Plan
	startedAt := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)

	delayMsg, err := mqttproto.NewWeatherDelayMessage(true, weatherdelay.KindDelay, startedAt, "op-1", 1, plan, startedAt)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage(delay): %v", err)
	}
	ops.react(context.Background(), ops.holder.SetFromMessage(delayMsg), weatherdelay.KindDelay)
	if !alertSessionPlaying(t, ops) {
		t.Fatal("delay alert is not playing before the change")
	}

	cancelMsg, err := mqttproto.NewWeatherDelayMessage(true, weatherdelay.KindCancelNight, startedAt, "op-1", 2, plan, startedAt)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage(cancelNight): %v", err)
	}
	transition := ops.holder.SetFromMessage(cancelMsg)
	if transition != weatherDelayKindChanged {
		t.Fatalf("transition = %v, want weatherDelayKindChanged", transition)
	}
	ops.react(context.Background(), transition, weatherdelay.KindCancelNight)
	if alertSessionPlaying(t, ops) {
		t.Fatal("the delay alert is still playing after the night was cancelled")
	}
}

// startCount counts engine Start calls, one per alert actually started.
func startCount(engine *weatherDelayRecordingEngine) int {
	n := 0
	for _, c := range engine.snapshot() {
		if c.kind == "start" {
			n++
		}
	}
	return n
}

func TestWeatherDelayStartOperationNeverDowngradesACancelledNight(t *testing.T) {
	ops, engine := newCancelKindTestOps(t, true, true)
	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindCancelNight, time.Now()); !played {
		t.Fatalf("cancel start did not play: %s", reason)
	}
	before := startCount(engine)

	res, err := ops.start(context.Background(), map[string]any{"kind": weatherdelay.KindDelay}, time.Now)
	if err != nil {
		t.Fatalf("start(delay): %v", err)
	}
	if !res.Confirmed {
		t.Fatal("start(delay) not confirmed, want success")
	}
	value, _ := res.Value.(map[string]any)
	if value["kind"] != weatherdelay.KindCancelNight {
		t.Fatalf("Value.kind = %v, want cancelNight", value["kind"])
	}
	if value["message"] != weatherDelayAlreadyCancelledMessage {
		t.Fatalf("Value.message = %v, want the already-cancelled sentence", value["message"])
	}
	if value["alertPlaying"] != true {
		t.Fatalf("Value.alertPlaying = %v, want true for the cancel alert", value["alertPlaying"])
	}
	if got := ops.holder.Current().Kind; got != weatherdelay.KindCancelNight {
		t.Fatalf("holder kind = %q, want cancelNight", got)
	}
	if after := startCount(engine); after != before {
		t.Fatalf("engine Start calls went from %d to %d, want the cancel alert left alone", before, after)
	}
	live, err := engine.LiveHandles(context.Background())
	if err != nil {
		t.Fatalf("LiveHandles: %v", err)
	}
	if len(live) != 1 || !strings.HasSuffix(string(live[0]), "cancelNight-0") {
		t.Fatalf("live engine handles = %v, want only the cancel alert", live)
	}
}

func TestWeatherDelaySignedHTTPStartNeverDowngradesACancelledNight(t *testing.T) {
	t.Cleanup(weatherDelayBackground.Wait)
	f := newWeatherDelayHTTPTestFixture(t, false)
	hash := writeAssetFixture(t, f.dir, "cancel.wav", []byte("cancel alert audio"))
	f.holder.rec.Plan = weatherDelayTestPlan(weatherdelay.KindCancelNight, "cancel-asset", hash, "cancel.wav", 3)
	ops := &weatherDelayOperations{holder: f.holder, audioMgr: f.mgr, assetDir: f.dir}
	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindCancelNight, time.Now()); !played {
		t.Fatalf("cancel start did not play: %s", reason)
	}

	body, err := json.Marshal(f.sign(t, weatherdelay.KindDelay, time.Now()))
	if err != nil {
		t.Fatalf("marshal signed request: %v", err)
	}
	resp := postStart(t, f.srvURL, body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, _ := io.ReadAll(resp.Body)
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got["kind"] != weatherdelay.KindCancelNight || got["message"] != weatherDelayAlreadyCancelledMessage {
		t.Fatalf("response = %s, want kind cancelNight and the already-cancelled sentence", raw)
	}
	if k := f.holder.Current().Kind; k != weatherdelay.KindCancelNight {
		t.Fatalf("holder kind = %q, want cancelNight", k)
	}
	if !alertSessionPlaying(t, ops) {
		t.Fatal("the cancel alert stopped after a delay start")
	}
}

func TestWeatherDelayStateMessageDowngradesOnlyOnANewerStateNumber(t *testing.T) {
	ops, engine := newCancelKindTestOps(t, true, true)
	plan := ops.holder.Current().Plan
	startedAt := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	msg := func(kind string, revision int64) mqttproto.WeatherDelayMessage {
		m, err := mqttproto.NewWeatherDelayMessage(true, kind, startedAt, "op-1", revision, plan, startedAt)
		if err != nil {
			t.Fatalf("NewWeatherDelayMessage(%s, %d): %v", kind, revision, err)
		}
		return m
	}

	ops.react(context.Background(), ops.holder.SetFromMessage(msg(weatherdelay.KindCancelNight, 5)), weatherdelay.KindCancelNight)
	before := startCount(engine)

	for _, rev := range []int64{4, 5} {
		if tr := ops.holder.SetFromMessage(msg(weatherdelay.KindDelay, rev)); tr != weatherDelayUnchanged {
			t.Fatalf("delay message at %d: transition = %v, want weatherDelayUnchanged", rev, tr)
		}
		if k := ops.holder.Current().Kind; k != weatherdelay.KindCancelNight {
			t.Fatalf("delay message at %d: holder kind = %q, want cancelNight", rev, k)
		}
	}
	if after := startCount(engine); after != before || !alertSessionPlaying(t, ops) {
		t.Fatalf("the cancel alert was disturbed by an older delay message (starts %d to %d)", before, after)
	}

	if tr := ops.holder.SetFromMessage(msg(weatherdelay.KindDelay, 6)); tr != weatherDelayKindChanged {
		t.Fatalf("delay message at 6: transition = %v, want weatherDelayKindChanged", tr)
	}
	if k := ops.holder.Current().Kind; k != weatherdelay.KindDelay {
		t.Fatalf("holder kind = %q after a newer delay message, want delay", k)
	}
}

func TestWeatherDelayLocalCancelIsNotDowngradedByTheRetainedDelay(t *testing.T) {
	ops, _ := newCancelKindTestOps(t, true, true)
	plan := ops.holder.Current().Plan
	startedAt := time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)
	delayMsg, err := mqttproto.NewWeatherDelayMessage(true, weatherdelay.KindDelay, startedAt, "op-1", 3, plan, startedAt)
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage: %v", err)
	}
	ops.react(context.Background(), ops.holder.SetFromMessage(delayMsg), weatherdelay.KindDelay)
	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindCancelNight, time.Now()); !played {
		t.Fatalf("cancel start did not play: %s", reason)
	}

	if tr := ops.holder.SetFromMessage(delayMsg); tr != weatherDelayUnchanged {
		t.Fatalf("redelivered retained delay: transition = %v, want weatherDelayUnchanged", tr)
	}
	if k := ops.holder.Current().Kind; k != weatherdelay.KindCancelNight {
		t.Fatalf("holder kind = %q, want cancelNight", k)
	}
}
