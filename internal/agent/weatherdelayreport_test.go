package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/weatherdelay"
)

func weatherDelayStateMessage(t *testing.T, active bool, kind string, revision int64) mqttproto.WeatherDelayMessage {
	t.Helper()
	startedAt, startedBy := time.Time{}, ""
	if active {
		startedAt, startedBy = time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC), "admin-1"
	}
	msg, err := mqttproto.NewWeatherDelayMessage(active, kind, startedAt, startedBy, revision, mqttproto.WeatherDelayPlan{}, time.Date(2026, 10, 31, 20, 0, 1, 0, time.UTC))
	if err != nil {
		t.Fatalf("NewWeatherDelayMessage: %v", err)
	}
	return msg
}

// A start that reaches only the node records the newest coordinator state
// the node had seen, which is what the coordinator later compares.
func TestWeatherDelayReportCarriesTheStateSeenWhenALocalStartArrived(t *testing.T) {
	holder := NewWeatherDelayHolder(t.TempDir(), discardLogger())
	if got := holder.Report(); got == nil || got.Active || got.HeldRevision != 0 || got.Revision != 0 {
		t.Fatalf("Report() before any state = %+v, want not active at 0", got)
	}

	holder.SetFromMessage(weatherDelayStateMessage(t, false, "", 6))
	startedAt := time.Date(2026, 10, 31, 21, 15, 0, 0, time.UTC)
	if err := holder.SetActiveLocal(weatherdelay.KindCancelNight, startedAt, "node-local"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	got := holder.Report()
	want := mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindCancelNight, StartedAt: startedAt, HeldRevision: 6, Revision: 6}
	if got == nil || *got != want {
		t.Fatalf("Report() = %+v, want %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("Report() does not validate: %v", err)
	}
}

// The adopted state arrives numbered above the held one, so the resume that
// follows it is newer than the held state and clears the node.
func TestWeatherDelayAdoptedStateDoesNotBlockTheLaterResume(t *testing.T) {
	holder := NewWeatherDelayHolder(t.TempDir(), discardLogger())
	holder.SetFromMessage(weatherDelayStateMessage(t, false, "", 6))
	if err := holder.SetActiveLocal(weatherdelay.KindDelay, time.Now(), "node-local"); err != nil {
		t.Fatalf("SetActiveLocal: %v", err)
	}

	if got := holder.SetFromMessage(weatherDelayStateMessage(t, false, "", 6)); got != weatherDelayUnchanged {
		t.Fatalf("the coordinator's republished not-active state at the held number cleared the node (transition %d)", got)
	}
	if got := holder.SetFromMessage(weatherDelayStateMessage(t, true, weatherdelay.KindDelay, 7)); got != weatherDelayUnchanged {
		t.Fatalf("the adopted state changed the node (transition %d), want unchanged", got)
	}
	if report := holder.Report(); report.HeldRevision != 6 || report.Revision != 7 || !report.Active {
		t.Fatalf("Report() after the adopted state = %+v, want active, held 6, seen 7", report)
	}
	if got := holder.SetFromMessage(weatherDelayStateMessage(t, false, "", 8)); got != weatherDelayCleared {
		t.Fatalf("the resume after an adopted delay did not clear the node (transition %d)", got)
	}
}

func TestHeartbeatCarriesTheWeatherDelayReport(t *testing.T) {
	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	report := &mqttproto.HealthWeatherDelay{Active: true, Kind: weatherdelay.KindDelay, HeldRevision: 3, Revision: 3}
	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, pub, "media-03", "boot-1", time.Now(), time.Now, ticks, nil,
			func() *mqttproto.HealthWeatherDelay { return report }, discardLogger())
	}()

	ticks <- time.Now()
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the heartbeat publish")
	}
	cancel()
	<-done

	pub.mu.Lock()
	payload := pub.calls[0].payload
	pub.mu.Unlock()
	_, health := decodeHealth(t, payload)
	if health.WeatherDelay == nil || *health.WeatherDelay != *report {
		t.Fatalf("heartbeat WeatherDelay = %+v, want %+v", health.WeatherDelay, report)
	}
}
