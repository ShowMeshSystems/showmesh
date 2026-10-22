package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const eventsMQTTPath = "/api/v1/config/events.mqtt"

type publishedMessage struct {
	broker, topic string
	qos           byte
	retain        bool
	payload       []byte
}

type recordingBrokers struct {
	mu       sync.Mutex
	messages []publishedMessage
	failFor  map[string]error
}

func (r *recordingBrokers) Publish(_ context.Context, id, topic string, qos byte, retain bool, payload []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.failFor[topic]; err != nil {
		return err
	}
	r.messages = append(r.messages, publishedMessage{id, topic, qos, retain, payload})
	return nil
}

func (r *recordingBrokers) AwaitResponse(context.Context, string, broker.ResponseRequest) (broker.Message, error) {
	return broker.Message{}, errors.New("not used")
}

func (r *recordingBrokers) topics() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.messages))
	for _, m := range r.messages {
		out = append(out, m.topic)
	}
	return out
}

// scriptedEvents is an EventReader holding a fixed list and honouring since.
type scriptedEvents struct{ records []EventRecord }

func (s *scriptedEvents) ListEvents(_ context.Context, since uint64, limit int) ([]EventRecord, bool, error) {
	var out []EventRecord
	for _, r := range s.records {
		if r.Seq > since && len(out) < limit {
			out = append(out, r)
		}
	}
	return out, false, nil
}

func (s *scriptedEvents) LatestEventSeq(context.Context) (uint64, error) {
	if len(s.records) == 0 {
		return 0, nil
	}
	return s.records[len(s.records)-1].Seq, nil
}

func (s *scriptedEvents) OldestEventSeq(context.Context) (uint64, bool, error) { return 0, false, nil }

func eventsMQTTTestAPI(t *testing.T, change func(*Dependencies)) (api *API, st *store.Store, deps Dependencies, adminToken string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	adminToken = mustIssueToken(t, svc, mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin).ID)
	deps = configTestDeps(svc, st)
	deps.NightSessions = st
	deps.IntegrationBrokers = StaticIntegrationBrokers{
		{ID: "ha", URL: "tcp://127.0.0.1:1883"},
		{ID: config.BuiltInIntegrationBrokerID, URL: "tcp://127.0.0.1:1883", TopicPrefix: config.BuiltInIntegrationTopicPrefix},
	}
	if change != nil {
		change(&deps)
	}
	return New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()}), st, deps, adminToken
}

func putEventsMQTT(t *testing.T, api *API, token, body string) (*http.Response, map[string]any) {
	t.Helper()
	resp, raw := doRawRequest(t, api.Handler, newJSONRequest(t, http.MethodPut, eventsMQTTPath, body, map[string]string{"Authorization": "Bearer " + token}))
	return resp, decodeMap(t, raw)
}

func TestEventsMQTTConfigDefaultsToOff(t *testing.T) {
	api, _, _, admin := eventsMQTTTestAPI(t, nil)

	resp, body := doRequest(t, api.Handler, http.MethodGet, eventsMQTTPath, map[string]string{"Authorization": "Bearer " + admin})
	m := decodeMap(t, body)
	payload, _ := m["payload"].(map[string]any)
	if resp.StatusCode != http.StatusOK || m["revision"] != float64(0) || payload["enabled"] != false || payload["topicPrefix"] != "showmesh" {
		t.Fatalf("status %d body %s, want 200, revision 0, off, prefix showmesh", resp.StatusCode, body)
	}
}

func TestEventsMQTTConfigSavesAndMergesPartialUpdates(t *testing.T) {
	api, _, _, admin := eventsMQTTTestAPI(t, nil)

	if resp, m := putEventsMQTT(t, api, admin, `{"enabled":true,"broker":"ha","topicPrefix":"barn/lights"}`); resp.StatusCode != http.StatusOK || m["revision"] != float64(1) {
		t.Fatalf("first PUT status %d body %v, want 200 revision 1", resp.StatusCode, m)
	}
	resp, m := putEventsMQTT(t, api, admin, `{"enabled":false}`)
	payload, _ := m["payload"].(map[string]any)
	if resp.StatusCode != http.StatusOK || payload["broker"] != "ha" || payload["topicPrefix"] != "barn/lights" || payload["enabled"] != false {
		t.Fatalf("partial PUT status %d body %v, want the stored broker and prefix kept", resp.StatusCode, m)
	}
}

func TestEventsMQTTConfigRefusesWhatCannotWork(t *testing.T) {
	api, _, _, admin := eventsMQTTTestAPI(t, nil)
	for name, body := range map[string]string{
		"undeclared broker": `{"enabled":true,"broker":"nowhere"}`,
		"built-in default":  `{"enabled":true,"broker":"built-in"}`,
		"built-in root":     `{"enabled":true,"broker":"built-in","topicPrefix":"showmesh-actions"}`,
		"enabled no broker": `{"enabled":true}`,
		"wildcard plus":     `{"topicPrefix":"a/+/b"}`,
		"wildcard hash":     `{"topicPrefix":"a/#"}`,
		"leading slash":     `{"topicPrefix":"/a"}`,
		"trailing slash":    `{"topicPrefix":"a/"}`,
		"empty level":       `{"topicPrefix":"a//b"}`,
		"empty prefix":      `{"topicPrefix":""}`,
		"null field":        `{"broker":null}`,
		"unknown field":     `{"retain":true}`,
		"wrong type":        `{"enabled":"yes"}`,
	} {
		if resp, m := putEventsMQTT(t, api, admin, body); resp.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: status %d body %v, want 400", name, resp.StatusCode, m)
		}
	}
}

func TestEventsMQTTConfigAcceptsTheBuiltInBrokerUnderItsTopics(t *testing.T) {
	api, _, _, admin := eventsMQTTTestAPI(t, nil)
	if resp, m := putEventsMQTT(t, api, admin, `{"enabled":true,"broker":"built-in","topicPrefix":"showmesh-actions/events"}`); resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d body %v, want 200", resp.StatusCode, m)
	}
}

func newTestForwarder(deps Dependencies, events EventReader, brokers MQTTBrokerRegistry) *EventsMQTTForwarder {
	deps.Events = events
	deps.MQTTBrokers = brokers
	return NewEventsMQTTForwarder(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
}

func scriptedRecord(seq uint64, category string) EventRecord {
	return EventRecord{Seq: seq, RecordedAt: testNow, Source: "night", Resource: observation.ResourceRef{Kind: observation.ResourceCoordinator, ID: "night"}, Category: category, Severity: "warning", Summary: "x"}
}

func TestEventsMQTTForwarderPublishesNewEventsOnTheCategoryTopic(t *testing.T) {
	api, _, deps, admin := eventsMQTTTestAPI(t, nil)
	putEventsMQTT(t, api, admin, `{"enabled":true,"broker":"ha","topicPrefix":"barn"}`)
	events := &scriptedEvents{records: []EventRecord{scriptedRecord(1, "old.event")}}
	brokers := &recordingBrokers{}
	f := newTestForwarder(deps, events, brokers)
	ctx := context.Background()

	f.tick(ctx) // starts at the latest event, so seq 1 is never sent
	events.records = append(events.records, scriptedRecord(2, "night.command.refused"))
	f.tick(ctx)

	var event *publishedMessage
	for i, m := range brokers.messages {
		if m.topic == "barn/events/night.command.refused" {
			event = &brokers.messages[i]
		}
		if m.topic == "barn/events/old.event" {
			t.Fatalf("an event recorded before the forwarder started was replayed")
		}
	}
	if event == nil {
		t.Fatalf("topics published = %v, want barn/events/night.command.refused", brokers.topics())
	}
	var got map[string]any
	if err := json.Unmarshal(event.payload, &got); err != nil {
		t.Fatalf("event payload is not JSON: %v", err)
	}
	if event.broker != "ha" || event.qos != 1 || event.retain || got["category"] != "night.command.refused" || got["seq"] != float64(2) {
		t.Fatalf("event message = %+v payload %v, want broker ha, QoS 1, not retained, seq 2", *event, got)
	}
}

func TestEventsMQTTForwarderPublishesTheNightStateRetainedOnceUntilItChanges(t *testing.T) {
	api, _, deps, admin := eventsMQTTTestAPI(t, nil)
	putEventsMQTT(t, api, admin, `{"enabled":true,"broker":"ha"}`)
	brokers := &recordingBrokers{}
	f := newTestForwarder(deps, &scriptedEvents{}, brokers)
	ctx := context.Background()

	f.tick(ctx)
	f.tick(ctx)

	if topics := brokers.topics(); len(topics) != 1 || topics[0] != "showmesh/night/state" {
		t.Fatalf("topics published = %v, want the night state exactly once", topics)
	}
	m := brokers.messages[0]
	var st map[string]any
	if err := json.Unmarshal(m.payload, &st); err != nil {
		t.Fatalf("state payload is not JSON: %v", err)
	}
	if !m.retain || m.qos != 1 || st["state"] != "inactive" {
		t.Fatalf("state message retain=%v qos=%d payload=%v, want retained, QoS 1, state inactive", m.retain, m.qos, st)
	}

	putEventsMQTT(t, api, admin, `{"topicPrefix":"barn"}`)
	f.tick(ctx)
	if topics := brokers.topics(); len(topics) != 2 || topics[1] != "barn/night/state" {
		t.Fatalf("topics after a prefix change = %v, want the state republished on barn/night/state", topics)
	}
}

func TestEventsMQTTForwarderFailedPublishDropsTheBatchAndTheNextBatchIsSent(t *testing.T) {
	api, _, deps, admin := eventsMQTTTestAPI(t, nil)
	putEventsMQTT(t, api, admin, `{"enabled":true,"broker":"ha"}`)
	events := &scriptedEvents{}
	brokers := &recordingBrokers{failFor: map[string]error{"showmesh/events/first": errors.New("broker down")}}
	f := newTestForwarder(deps, events, brokers)
	ctx := context.Background()

	f.tick(ctx)
	events.records = []EventRecord{scriptedRecord(1, "first"), scriptedRecord(2, "second")}
	f.tick(ctx)
	brokers.failFor = nil
	events.records = append(events.records, scriptedRecord(3, "third"))
	f.tick(ctx)

	var sawThird bool
	for _, topic := range brokers.topics() {
		switch topic {
		case "showmesh/events/first", "showmesh/events/second":
			t.Fatalf("topics published = %v, want the failed batch dropped and never resent", brokers.topics())
		case "showmesh/events/third":
			sawThird = true
		}
	}
	if !sawThird {
		t.Fatalf("topics published = %v, want the event recorded after the broker came back", brokers.topics())
	}
}

func TestEventsMQTTForwarderDisabledPublishesNothingAndSkipsEventsRecordedWhileOff(t *testing.T) {
	api, _, deps, admin := eventsMQTTTestAPI(t, nil)
	events := &scriptedEvents{}
	brokers := &recordingBrokers{}
	f := newTestForwarder(deps, events, brokers)
	ctx := context.Background()

	f.tick(ctx)
	events.records = []EventRecord{scriptedRecord(1, "while.off")}
	f.tick(ctx)
	if len(brokers.topics()) != 0 {
		t.Fatalf("topics published while disabled = %v, want none", brokers.topics())
	}

	putEventsMQTT(t, api, admin, `{"enabled":true,"broker":"ha"}`)
	f.tick(ctx)
	for _, topic := range brokers.topics() {
		if topic == "showmesh/events/while.off" {
			t.Fatalf("an event recorded while publishing was off was sent after it was turned on")
		}
	}
}

var _ = time.Second
