package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is the audio session dispatch acceptance proof, following
// renderdispatch_test.go's established pattern: a real store.Store, a
// real identity.Service over it, and a fake in place of the one
// genuinely external dependency (here, the MQTT publish-and-await).

// fakeAudioPublisher records every publish and, on AwaitResponse, replies
// with a canned mqttproto.ResultPayload the test configures — standing in
// for a node's own CommandHandler round trip without a real broker.
type fakeAudioPublisher struct {
	mu           sync.Mutex
	publishCount int
	lastAction   string
	lastParams   map[string]any

	result     mqttproto.ResultPayload
	awaitErr   error
	publishErr error
}

func (f *fakeAudioPublisher) Publish(_ context.Context, _ string, _ byte, _ bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishCount++
	var env struct {
		Payload struct {
			Action string         `json:"action"`
			Params map[string]any `json:"params"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(payload, &env)
	f.lastAction = env.Payload.Action
	f.lastParams = env.Payload.Params
	return f.publishErr
}

func (f *fakeAudioPublisher) AwaitResponse(_ context.Context, req broker.ResponseRequest) (broker.Message, error) {
	if f.publishErr != nil {
		return broker.Message{}, f.publishErr
	}
	if err := f.Publish(context.Background(), req.PublishTopic, 0, false, req.PublishPayload); err != nil {
		return broker.Message{}, err
	}
	f.mu.Lock()
	f.publishCount-- // Publish above double-counted; correct it back to one call per dispatch.
	f.publishCount++
	f.mu.Unlock()
	if f.awaitErr != nil {
		return broker.Message{}, f.awaitErr
	}
	env, err := mqttproto.NewResultEnvelope(time.Now, "node-a", f.result)
	if err != nil {
		return broker.Message{}, err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return broker.Message{}, err
	}
	return broker.Message{Topic: req.ResponseTopic, Payload: raw}, nil
}

func (f *fakeAudioPublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.publishCount
}

type audioDispatchTestSetup struct {
	st  *store.Store
	svc identity.Service
	pub *fakeAudioPublisher
}

func newAudioDispatchTestSetup(t *testing.T, now func() time.Time) *audioDispatchTestSetup {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "db"), nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &audioDispatchTestSetup{st: st, svc: svc, pub: &fakeAudioPublisher{}}
}

func (s *audioDispatchTestSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &dynamicObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, Commands: s.st, Config: s.st,
		AudioPublisher: s.pub, AudioSessions: s.st,
	}
}

func newAudioRequest(t *testing.T, method, path, body, bearerToken string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return req
}

func TestAudioSessionDispatchRefusedUnauthenticated(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", `{"revision":1}`, "")
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — an unauthenticated request must never reach dispatch", setup.pub.count())
	}
}

func TestAudioSessionDispatchRefusedForbiddenViewer(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", `{"revision":1}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "audio:command") {
		t.Fatalf("body = %q, want it to name the missing scope audio:command", body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — a forbidden request must never reach dispatch", setup.pub.count())
	}
}

func TestAudioSessionDispatchConfirmsFromNodeEvidence(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.session.stop",
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "operation applied, but the post-write read-back evidence did not match the requested value",
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session.stop",
			Value:  map[string]any{"sessionId": "night-session", "outcome": "stopped", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", `{"revision":1}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1", setup.pub.count())
	}
	if setup.pub.lastAction != "audio.session.stop" {
		t.Fatalf("dispatched action = %q, want audio.session.stop", setup.pub.lastAction)
	}
	if setup.pub.lastParams["sessionId"] != "night-session" {
		t.Fatalf("dispatched params.sessionId = %v, want night-session", setup.pub.lastParams["sessionId"])
	}
	var decoded struct {
		Command struct {
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Command.Outcome != "stopped" {
		t.Fatalf("outcome = %q, want stopped (from node evidence, not the transport-level mqttproto outcome)", decoded.Command.Outcome)
	}

	rec, err := setup.st.GetAudioSession(context.Background(), "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession: %v", err)
	}
	if rec.NodeID != "node-a" || rec.Revision != 1 {
		t.Fatalf("persisted record = %+v", rec)
	}
}

func TestAudioSessionDispatchReplayReturnsExistingOutcomeWithoutRepublishing(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.session.start",
		Outcome: mqttproto.OutcomeUnconfirmed, Reason: "operation applied, but the post-write read-back evidence did not match the requested value",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "started", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"revision":1,"idempotencyKey":"fixed-key-1"}`
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/start", body, token)
	resp1, _ := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d", resp1.StatusCode)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after first dispatch = %d, want 1", setup.pub.count())
	}

	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/start", body, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replayed dispatch status = %d; body: %s", resp2.StatusCode, body2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after replay = %d, want still 1 — a replayed idempotency key must not re-dispatch", setup.pub.count())
	}
	var decoded struct {
		Command struct {
			Replay  bool   `json:"replay"`
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body2, &decoded); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !decoded.Command.Replay {
		t.Fatal("replayed response did not report replay:true")
	}
	if decoded.Command.Outcome != "started" {
		t.Fatalf("replayed outcome = %q, want the ORIGINAL dispatch's own outcome %q, not a generic collector state", decoded.Command.Outcome, "started")
	}
}

// TestAudioSessionDispatchConflictingReplayIsRefusedNotAnswered proves an
// idempotency key reused against a DIFFERENT action is a 409 conflict,
// never silently answered as if it belonged to the first command that
// claimed the key — mirroring resolveResolumeActionReplay's identical
// rule one file over.
func TestAudioSessionDispatchConflictingReplayIsRefusedNotAnswered(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.session.start",
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "started", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	key := `{"revision":1,"idempotencyKey":"reused-key-1"}`
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/start", key, token)
	resp1, _ := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d", resp1.StatusCode)
	}

	// Same idempotency key, DIFFERENT op (stop instead of start) — this
	// must never be answered as a replay of the start above.
	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", key, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting-action replay status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after conflicting reuse = %d, want still 1 — a conflict must never dispatch", setup.pub.count())
	}
}

func TestAudioSessionDispatchNotReachableByGET(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodGet, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", "", token)
	resp, _ := doRawRequest(t, api.Handler, req)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("GET against an audio.session.* dispatch path must never succeed (ADR-024 decision 6)")
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 — GET must never dispatch", setup.pub.count())
	}
}
