package agent

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

// Every alert starts on a session that has never played: an alert session
// is cleared when its alert stops and again before a new alert of the same
// kind starts, so the audio manager never Applies a new playlist over one
// whose item index has already advanced.

// alertItemIndex reports id's current item index, or -1 when the manager
// holds no such session.
func alertItemIndex(mgr *audio.Manager, id pkgaudio.SessionID) int {
	for _, s := range mgr.Snapshot(context.Background()) {
		if s.ID == id {
			return s.ItemIndex
		}
	}
	return -1
}

// alertSessionState reports id's state, or "" when the manager holds no
// such session.
func alertSessionState(mgr *audio.Manager, id pkgaudio.SessionID) pkgaudio.State {
	for _, s := range mgr.Snapshot(context.Background()) {
		if s.ID == id {
			return s.State
		}
	}
	return ""
}

// newAlertReuseOps builds a node whose alert assets decode to a known item
// duration, so a test can drive the alert from one item to the next.
func newAlertReuseOps(t *testing.T, repeatCount int, kinds ...string) (*weatherDelayOperations, *audio.Manager, *fakeClock, string) {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(weatherDelayBackground.Wait)
	clock := &fakeClock{t: time.Date(2026, 9, 19, 20, 0, 0, 0, time.UTC)}
	mgr, _ := newWeatherDelayTestManagerWithDecoder(t, dir, clock, weatherDelayDurationDecoder{})

	hash := writeAssetFixture(t, dir, "alert.wav", []byte("alert audio content"))
	ref := &mqttproto.WeatherDelayAlertAssetRef{AssetID: "alert-asset", ContentHash: hash, Filename: "alert.wav"}
	plan := mqttproto.WeatherDelayPlan{RepeatCount: repeatCount}
	for _, kind := range kinds {
		switch kind {
		case weatherdelay.KindDelay:
			plan.Delay = ref
		case weatherdelay.KindCancelNight:
			plan.CancelNight = ref
		}
	}
	holder := &WeatherDelayHolder{store: newWeatherDelayStore(dir)}
	holder.rec.Plan = plan
	return &weatherDelayOperations{holder: holder, audioMgr: mgr, assetDir: dir}, mgr, clock, dir
}

// TestWeatherDelaySecondDelayAfterAResumePlaysFromItemZero is the night
// with two delays in it: delay, resume, delay again. ADR-053 decision 7
// requires the configured repeat count every time, so the second alert must
// start at item 0 and reach the last item, not carry on from the index the
// first one left behind.
func TestWeatherDelaySecondDelayAfterAResumePlaysFromItemZero(t *testing.T) {
	ops, mgr, clock, _ := newAlertReuseOps(t, 5, weatherdelay.KindDelay)
	index := func() int { return alertItemIndex(mgr, weatherDelayAlertSessionID) }

	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, clock.now()); !played {
		t.Fatalf("the first delay alert did not play: %s", reason)
	}
	advanceAlertToItem(t, mgr, clock, index, 2)

	ops.doResume(context.Background())

	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, clock.now()); !played {
		t.Fatalf("the second delay alert did not play: %s", reason)
	}
	if got := index(); got != 0 {
		t.Fatalf("the second delay alert of the night starts at item %d, want 0; it must play all 5 repeats, not the %d left over", got, 5-got)
	}
	advanceAlertToItem(t, mgr, clock, index, 4)
	if got := alertSessionState(mgr, weatherDelayAlertSessionID); got == "" {
		t.Fatal("the second alert session vanished while it was still playing")
	}
}

// TestWeatherDelayRepeatedStartWhilePlayingDoesNotRestartTheAlert holds the
// rule the clear must not break: a second start of the kind already playing
// neither restarts that alert nor stacks another.
func TestWeatherDelayRepeatedStartWhilePlayingDoesNotRestartTheAlert(t *testing.T) {
	ops, mgr, clock, _ := newAlertReuseOps(t, 5, weatherdelay.KindDelay)
	index := func() int { return alertItemIndex(mgr, weatherDelayAlertSessionID) }

	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, clock.now()); !played {
		t.Fatalf("the delay alert did not play: %s", reason)
	}
	advanceAlertToItem(t, mgr, clock, index, 2)

	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, clock.now()); !played {
		t.Fatalf("a repeated start reported the alert not playing: %s", reason)
	}
	if got := index(); got != 2 {
		t.Fatalf("a repeated start moved the playing alert to item %d, want it left at 2", got)
	}
}

// TestWeatherDelayDelayAfterACancelledNightPlaysFromItemZero is the night
// after the cancelled one: a cancel alert replaces a delay alert that has
// advanced, the cancellation is cleared, and a later delay must still play
// its full repeat count from item 0.
func TestWeatherDelayDelayAfterACancelledNightPlaysFromItemZero(t *testing.T) {
	ops, mgr, clock, _ := newAlertReuseOps(t, 5, weatherdelay.KindDelay, weatherdelay.KindCancelNight)
	cancelSession := weatherDelayAlertSessionIDForKind(weatherdelay.KindCancelNight)
	delayIndex := func() int { return alertItemIndex(mgr, weatherDelayAlertSessionID) }
	cancelIndex := func() int { return alertItemIndex(mgr, cancelSession) }

	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, clock.now()); !played {
		t.Fatalf("the delay alert did not play: %s", reason)
	}
	advanceAlertToItem(t, mgr, clock, delayIndex, 2)

	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindCancelNight, clock.now()); !played {
		t.Fatalf("the cancel alert did not play: %s", reason)
	}
	if got := cancelIndex(); got != 0 {
		t.Fatalf("the cancel alert starts at item %d, want 0", got)
	}
	advanceAlertToItem(t, mgr, clock, cancelIndex, 2)

	ops.doResume(context.Background())

	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, clock.now()); !played {
		t.Fatalf("the delay alert on the later night did not play: %s", reason)
	}
	if got := delayIndex(); got != 0 {
		t.Fatalf("the delay alert on the later night starts at item %d, want 0", got)
	}
	advanceAlertToItem(t, mgr, clock, delayIndex, 4)
}

// TestWeatherDelayClearLeavesNothingOfTheAlertOnDisk proves the boot rule
// still holds with nothing left to restore: a cleared alert session has no
// persisted record at all, and a fresh manager restores none.
func TestWeatherDelayClearLeavesNothingOfTheAlertOnDisk(t *testing.T) {
	ops, mgr, clock, dir := newAlertReuseOps(t, 5, weatherdelay.KindDelay)
	if played, reason, _ := ops.doStart(context.Background(), weatherdelay.KindDelay, clock.now()); !played {
		t.Fatalf("the delay alert did not play: %s", reason)
	}
	advanceAlertToItem(t, mgr, clock, func() int { return alertItemIndex(mgr, weatherDelayAlertSessionID) }, 2)
	ops.doResume(context.Background())

	if found := filesMentioning(t, dir, string(weatherDelayAlertSessionID)); len(found) != 0 {
		t.Fatalf("the alert session is still on disk after a clear, in %v", found)
	}

	rebooted, _ := newWeatherDelayTestManagerWithDecoder(t, dir, clock, weatherDelayDurationDecoder{})
	restoreAudioSessionsAtBoot(context.Background(), rebooted, false, discardLogger())
	if got := alertSessionState(rebooted, weatherDelayAlertSessionID); got != "" {
		t.Fatalf("a fresh manager restored the cleared alert session in state %q, want no session at all", got)
	}
	assertNotPlayingAfterRestore(t, rebooted, weatherDelayLegacyAlertSessionID)
}

// filesMentioning lists every regular file under root whose contents
// contain needle.
func filesMentioning(t *testing.T, root, needle string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		if strings.Contains(string(data), needle) {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return found
}
