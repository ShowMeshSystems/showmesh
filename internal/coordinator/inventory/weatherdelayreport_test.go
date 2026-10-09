package inventory

import (
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

func healthMessage(t *testing.T, nodeID string, sequence uint64, report *mqttproto.HealthWeatherDelay, retained bool) broker.Message {
	t.Helper()
	env, err := mqttproto.NewHealthEnvelope(nil, nodeID, mqttproto.HealthPayload{BootID: "boot-1", Sequence: sequence, WeatherDelay: report})
	if err != nil {
		t.Fatalf("build health envelope: %v", err)
	}
	return broker.Message{Topic: healthTopic(t, nodeID), Payload: mustEnvelopeBytes(t, env), Retained: retained}
}

func TestHandleHealthHandsOnOnlyALiveWeatherDelayReport(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 31, 20, 0, 0, 0, time.UTC)}
	m := newTestManager(t, clock)
	type got struct {
		nodeID string
		report mqttproto.HealthWeatherDelay
	}
	var reports []got
	WithOnWeatherDelayReport(func(nodeID string, report mqttproto.HealthWeatherDelay) {
		reports = append(reports, got{nodeID, report})
	})(m)

	active := &mqttproto.HealthWeatherDelay{Active: true, Kind: mqttproto.WeatherDelayKindDelay, HeldRevision: 2, Revision: 2}

	m.HandleMessage(healthMessage(t, "node-a", 0, active, true))
	if len(reports) != 0 {
		t.Fatalf("a retained heartbeat reached the report hook: %+v", reports)
	}

	m.HandleMessage(healthMessage(t, "node-a", 1, nil, false))
	if len(reports) != 0 {
		t.Fatalf("a heartbeat with no weather delay member reached the report hook: %+v", reports)
	}

	m.HandleMessage(healthMessage(t, "node-a", 2, &mqttproto.HealthWeatherDelay{Active: true, Kind: "storm"}, false))
	if len(reports) != 0 {
		t.Fatalf("a malformed report reached the report hook: %+v", reports)
	}
	rec, err := m.store.GetNode(t.Context(), "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health == nil || rec.Health.Sequence != 2 {
		t.Fatalf("Health = %+v, want the heartbeat carrying the malformed report stored at sequence 2", rec.Health)
	}

	m.HandleMessage(healthMessage(t, "node-a", 3, active, false))
	if len(reports) != 1 || reports[0].nodeID != "node-a" || reports[0].report != *active {
		t.Fatalf("reports = %+v, want exactly the live report from node-a", reports)
	}
}
