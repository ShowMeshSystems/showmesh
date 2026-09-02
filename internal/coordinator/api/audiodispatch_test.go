package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
// with a canned mqttproto.ResultPayload the test configures - standing in
// for a node's own CommandHandler round trip without a real broker.
type fakeAudioPublisher struct {
	mu           sync.Mutex
	publishCount int
	lastAction   string
	lastParams   map[string]any

	result     mqttproto.ResultPayload
	awaitErr   error
	publishErr error

	// beforePublishErr, when set, makes AwaitResponse fail exactly the
	// way broker.BrokerManager.AwaitResponse fails when subscribing (or
	// deadline validation) fails BEFORE Publish is ever called: wrapped
	// in broker.ErrResponseFailedBeforePublish, and nothing published.
	beforePublishErr error

	// noAutoCorrelate disables this fake's own default behavior of
	// stamping f.result's CommandID/IdempotencyKey/Action from the
	// dispatched command envelope before returning it - a real agent's
	// result always carries the SAME identifiers it was dispatched
	// under, so this fake does the same unless a test deliberately wants
	// to prove that a result carrying someone else's identifiers is
	// rejected rather than accepted (see
	// TestAudioSessionDispatchRejectsUncorrelatedResult).
	noAutoCorrelate bool

	// resultsByAction, when non-nil, answers AwaitResponse per dispatched
	// action rather than with one global f.result - Track F seam F5's
	// own announcement tests need a duck (audio.gain.fade) to succeed
	// while the announcement's own action fails, in the SAME test, which
	// one shared result field cannot express. An action absent from this
	// map falls back to f.result.
	resultsByAction map[string]mqttproto.ResultPayload

	// resultsByNode, when non-nil, answers AwaitResponse per dispatched
	// TARGET NODE and action (key "nodeID" alone matches every action on
	// that node; key "nodeID:action" matches only that one action, and
	// takes priority), checked before resultsByAction - this file's own
	// multi-node tests need the SAME action (e.g. "apply") to confirm on
	// one node and refuse on another, or one specific action on one node
	// to refuse while every other action on that SAME node confirms
	// (e.g. a node's clear and apply confirm but its start is refused),
	// in the SAME test - neither of which resultsByAction (keyed only by
	// action) can express. A node/action absent from both key forms
	// falls through to resultsByAction, then f.result.
	resultsByNode map[string]mqttproto.ResultPayload

	// dispatched records every command this fake was handed, in order.
	// lastAction/lastParams answer "what was the final one"; a test that
	// must prove a command was NEVER sent, or must read the params of an
	// earlier one in a multi-step sequence, needs the whole list.
	dispatched []dispatchedAudioCommand

	// onAwaitResponse, when set, runs synchronously before AwaitResponse
	// does anything else - a test's only way to inject an event (such as
	// canceling the caller's own request context) at the exact moment a
	// real broker round trip would be in flight, without a real broker.
	onAwaitResponse func()
}

// dispatchedAudioCommand is one recorded publish: the action string, the
// target node id, and the params it carried.
type dispatchedAudioCommand struct {
	Action   string
	NodeID   string
	Params   map[string]any
	Deadline *time.Time
}

func (f *fakeAudioPublisher) Publish(_ context.Context, _ string, _ byte, _ bool, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.publishCount++
	var env struct {
		NodeID  string `json:"nodeId"`
		Payload struct {
			Action   string         `json:"action"`
			Params   map[string]any `json:"params"`
			Deadline *time.Time     `json:"deadline"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(payload, &env)
	f.lastAction = env.Payload.Action
	f.lastParams = env.Payload.Params
	f.dispatched = append(f.dispatched, dispatchedAudioCommand{
		Action: env.Payload.Action, NodeID: env.NodeID, Params: env.Payload.Params,
		Deadline: env.Payload.Deadline,
	})
	return f.publishErr
}

func (f *fakeAudioPublisher) AwaitResponse(_ context.Context, req broker.ResponseRequest) (broker.Message, error) {
	if f.onAwaitResponse != nil {
		f.onAwaitResponse()
	}
	if f.beforePublishErr != nil {
		return broker.Message{}, fmt.Errorf("%w: %w", broker.ErrResponseFailedBeforePublish, f.beforePublishErr)
	}
	if f.publishErr != nil {
		// Mirrors broker.BrokerManager.AwaitResponse: a failed Publish call
		// means nothing reached the wire, wrapped in the same sentinel a
		// failed subscribe already reports.
		return broker.Message{}, fmt.Errorf("%w: %w", broker.ErrResponseFailedBeforePublish, f.publishErr)
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

	cmdEnv, err := mqttproto.DecodeEnvelope(req.PublishPayload)
	if err != nil {
		return broker.Message{}, err
	}
	cmd, err := mqttproto.DecodeCmdPayload(cmdEnv)
	if err != nil {
		return broker.Message{}, err
	}

	result := f.result
	if f.resultsByAction != nil {
		if r, ok := f.resultsByAction[cmd.Action]; ok {
			result = r
		}
	}
	if f.resultsByNode != nil {
		if r, ok := f.resultsByNode[cmdEnv.NodeID]; ok {
			result = r
		}
		if r, ok := f.resultsByNode[cmdEnv.NodeID+":"+cmd.Action]; ok {
			result = r
		}
	}
	if !f.noAutoCorrelate {
		result.CommandID = cmd.CommandID
		result.IdempotencyKey = cmd.IdempotencyKey
		result.Action = cmd.Action
	}

	env, err := mqttproto.NewResultEnvelope(time.Now, cmdEnv.NodeID, result)
	if err != nil {
		return broker.Message{}, err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return broker.Message{}, err
	}
	msg := broker.Message{Topic: req.ResponseTopic, Payload: raw}
	// A real broker only ever hands AwaitResponse a message its own
	// req.Match accepted; this fake enforces the identical contract
	// rather than handing the caller every message unconditionally.
	if req.Match != nil && !req.Match(msg) {
		return broker.Message{}, broker.ErrResponseDeadlineExceeded
	}
	return msg, nil
}

func (f *fakeAudioPublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.publishCount
}

type audioDispatchTestSetup struct {
	st       *store.Store
	svc      identity.Service
	pub      *fakeAudioPublisher
	storeDir string
}

func newAudioDispatchTestSetup(t *testing.T, now func() time.Time) *audioDispatchTestSetup {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &audioDispatchTestSetup{st: st, svc: svc, pub: &fakeAudioPublisher{}, storeDir: storeDir}
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
		t.Fatalf("publish count = %d, want 0 - an unauthenticated request must never reach dispatch", setup.pub.count())
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
		t.Fatalf("publish count = %d, want 0 - a forbidden request must never reach dispatch", setup.pub.count())
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

	rec, err := setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession: %v", err)
	}
	if rec.NodeID != "node-a" || rec.Revision != 1 {
		t.Fatalf("persisted record = %+v", rec)
	}
}

// TestAudioSessionDispatchSetsWireDeadlineForListedAction proves the half
// of audioCommandDeadlineActions's boolean check that a mutation to
// "always true" cannot fake: a listed action (audio.session.stop) must
// carry a wire CmdPayload.Deadline, generous (audioCommandWireDeadline)
// and anchored to the dispatch clock, not merely non-nil.
func TestAudioSessionDispatchSetsWireDeadlineForListedAction(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
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
	if len(setup.pub.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(setup.pub.dispatched))
	}
	got := setup.pub.dispatched[0].Deadline
	if got == nil {
		t.Fatalf("Deadline = nil, want set for listed action audio.session.stop")
	}
	want := testNow.Add(audioCommandWireDeadline)
	if !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v (testNow + audioCommandWireDeadline)", got, want)
	}
}

// TestAudioSessionDispatchLeavesUnlistedActionWithoutDeadline proves the
// other half of audioCommandDeadlineActions's boolean check that a
// mutation to "always false" cannot fake, and is the entire point of the
// positive-list design: an action not on the list (audio.gain.set, which
// dispatches through the SAME executeAudioSessionDispatch) must carry a
// nil wire Deadline, exactly as it did before this change, so an
// unclassified future action (e.g. a steady-playback re-affirm) is safe
// by default rather than safe by someone remembering an exemption.
func TestAudioSessionDispatchLeavesUnlistedActionWithoutDeadline(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session.gain",
			Value:  map[string]any{"sessionId": "night-session", "gainDb": -6.0},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/gain", `{"revision":1,"params":{"gainDb":-6.0}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if len(setup.pub.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(setup.pub.dispatched))
	}
	if setup.pub.dispatched[0].Action != "audio.gain.set" {
		t.Fatalf("dispatched action = %q, want audio.gain.set", setup.pub.dispatched[0].Action)
	}
	if got := setup.pub.dispatched[0].Deadline; got != nil {
		t.Fatalf("Deadline = %v, want nil for unlisted action audio.gain.set", *got)
	}
}

// TestAudioSessionDispatchSameSessionIDDifferentNodesAreIndependent is
// this seam's own end-to-end acceptance proof: "night-session" (like the
// real cue and blackAndSilence session ids) is not unique to one node,
// and two nodes dispatching a command against that SAME session id must
// each keep their own durable desired-state record and their own
// revision counter - neither node's write may drop or be dropped by the
// other's, which is exactly what audio_sessions' pre-schemaV20 `id`-only
// primary key allowed to happen.
func TestAudioSessionDispatchSameSessionIDDifferentNodesAreIndependent(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "stopped", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	reqA := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", `{"revision":1}`, token)
	if resp, body := doRawRequest(t, api.Handler, reqA); resp.StatusCode != http.StatusOK {
		t.Fatalf("node-a dispatch status = %d; body: %s", resp.StatusCode, body)
	}
	reqB := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-b/audio/sessions/night-session/stop", `{"revision":1}`, token)
	if resp, body := doRawRequest(t, api.Handler, reqB); resp.StatusCode != http.StatusOK {
		t.Fatalf("node-b dispatch status = %d; body: %s", resp.StatusCode, body)
	}

	recA, err := setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession node-a: %v", err)
	}
	recB, err := setup.st.GetAudioSession(context.Background(), "node-b", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession node-b: %v", err)
	}
	if recA.NodeID != "node-a" || recA.Revision != 1 {
		t.Fatalf("node-a record = %+v", recA)
	}
	if recB.NodeID != "node-b" || recB.Revision != 1 {
		t.Fatalf("node-b record = %+v", recB)
	}

	// Advance node-a to revision 2; node-b's own row, still at revision 1,
	// must be untouched and must not block a later node-b write at a
	// revision that would be a rewind only if the two nodes shared a
	// counter.
	reqA2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", `{"revision":2}`, token)
	if resp, body := doRawRequest(t, api.Handler, reqA2); resp.StatusCode != http.StatusOK {
		t.Fatalf("node-a second dispatch status = %d; body: %s", resp.StatusCode, body)
	}

	recA, err = setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession node-a after advance: %v", err)
	}
	if recA.Revision != 2 {
		t.Fatalf("node-a revision = %d, want 2", recA.Revision)
	}
	recB, err = setup.st.GetAudioSession(context.Background(), "node-b", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession node-b after node-a advanced: %v", err)
	}
	if recB.Revision != 1 {
		t.Fatalf("node-b revision = %d, want still 1 (node-a's advance must not touch node-b's row)", recB.Revision)
	}

	reqB2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-b/audio/sessions/night-session/stop", `{"revision":2}`, token)
	if resp, body := doRawRequest(t, api.Handler, reqB2); resp.StatusCode != http.StatusOK {
		t.Fatalf("node-b second dispatch status = %d; body: %s", resp.StatusCode, body)
	}
	recB, err = setup.st.GetAudioSession(context.Background(), "node-b", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession node-b after advance: %v", err)
	}
	if recB.Revision != 2 {
		t.Fatalf("node-b revision = %d, want 2 (node-b's own write must not have been dropped)", recB.Revision)
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
		t.Fatalf("publish count after replay = %d, want still 1 - a replayed idempotency key must not re-dispatch", setup.pub.count())
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
// claimed the key - mirroring resolveResolumeActionReplay's identical
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

	// Same idempotency key, DIFFERENT op (stop instead of start) - this
	// must never be answered as a replay of the start above.
	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", key, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("conflicting-action replay status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after conflicting reuse = %d, want still 1 - a conflict must never dispatch", setup.pub.count())
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
		t.Fatalf("publish count = %d, want 0 - GET must never dispatch", setup.pub.count())
	}
}

// TestAudioSessionDispatchSendsRevisionInParams proves the fix for the
// defect where params["revision"] was never set: internal/agent's own
// parseAudioSessionCommon (audiosessionops.go) requires it and refuses
// every dispatch with "params.revision is required" without it, a
// failure this coordinator-side fake never reproduced because it
// manufactures the agent's own reply instead of running the real
// parsing code.
func TestAudioSessionDispatchSendsRevisionInParams(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeUnconfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "stopped", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", `{"revision":7}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	rev, ok := setup.pub.lastParams["revision"]
	if !ok {
		t.Fatal(`dispatched params carry no "revision" key - internal/agent's parseAudioSessionCommon requires it and refuses every dispatch without it`)
	}
	if got, ok := rev.(float64); !ok || got != 7 {
		t.Fatalf("dispatched params.revision = %v, want 7", rev)
	}
}

// TestAudioSessionDispatchRejectsUncorrelatedResult proves a message
// arriving on the command's own result topic but carrying ANOTHER
// command's identifiers is never accepted as this command's result:
// this dispatch must time out honestly (reporting unconfirmable) rather
// than reporting someone else's outcome as its own.
func TestAudioSessionDispatchRejectsUncorrelatedResult(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.noAutoCorrelate = true
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "some-other-command", IdempotencyKey: "some-other-key", Action: "audio.session.stop",
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "stopped", "reason": ""},
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
	var decoded struct {
		Command struct {
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Command.Outcome != "unconfirmable" {
		t.Fatalf("outcome = %q, want unconfirmable - a mismatched result must never be accepted as this command's own", decoded.Command.Outcome)
	}
}

// TestAudioSessionDispatchRefusedResultDoesNotPersistDesiredState proves
// a refused outcome never writes the session store: a low, refused
// revision must not overwrite a previously-accepted, higher revision's
// desired state.
func TestAudioSessionDispatchRefusedResultDoesNotPersistDesiredState(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "position", "reason": ""},
		},
	}
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":5,"params":{"media":{"assetId":"clip-1"}}}`, token)
	if resp, body := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d; body: %s", resp.StatusCode, body)
	}

	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeRefused, Reason: "stale revision",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "refused", "reason": "stale revision"},
		},
	}
	// Revision 10 here is deliberately HIGHER than the accepted revision
	// 5 above: a refused result must never be persisted regardless of
	// whether its OWN revision would otherwise satisfy the store's
	// separate anti-rewind guard (audiosessions.go's ON CONFLICT WHERE
	// clause) - this isolates "only an accepted outcome is persisted"
	// from "revision only advances", which a lower revision would not.
	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":10,"params":{"media":{"assetId":"clip-EVIL"}}}`, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second apply status = %d; body: %s", resp2.StatusCode, body2)
	}

	rec, err := setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession: %v", err)
	}
	if rec.Revision != 5 {
		t.Fatalf("stored revision = %d, want 5 - a refused outcome must never be persisted, even at a higher revision", rec.Revision)
	}
	if strings.Contains(rec.DesiredJSON, "clip-EVIL") {
		t.Fatalf("stored desired state = %q, must not contain the refused request's own params", rec.DesiredJSON)
	}
}

// TestAudioSessionDispatchPauseMergesRatherThanErasesDesiredState proves
// a pause command - whose own params carry only
// sessionId/invocationId/revision - merges onto the session's prior
// desired state rather than replacing it outright, which would erase
// the previously-applied media reference.
func TestAudioSessionDispatchPauseMergesRatherThanErasesDesiredState(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "position", "reason": ""},
		},
	}
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":1,"params":{"media":{"assetId":"clip-1"}}}`, token)
	if resp, body := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d; body: %s", resp.StatusCode, body)
	}

	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/pause",
		`{"revision":2}`, token)
	if resp, body := doRawRequest(t, api.Handler, req2); resp.StatusCode != http.StatusOK {
		t.Fatalf("pause status = %d; body: %s", resp.StatusCode, body)
	}

	rec, err := setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession: %v", err)
	}
	if rec.Revision != 2 {
		t.Fatalf("stored revision = %d, want 2", rec.Revision)
	}
	if !strings.Contains(rec.DesiredJSON, "clip-1") {
		t.Fatalf("stored desired state = %q, lost the media applied before the pause", rec.DesiredJSON)
	}
}

// TestAudioSessionDispatchAuditWriteFailureRunsWithDegradedAttribution
// proves ADR-024 decision 11's amendment (owner ruling, 2026-08-26):
// a NON-exempt action (pause carries no ADR-024 decision 11
// safety-class exemption) still dispatches, with AttributionDegraded
// true, when the audit store cannot be written - never refused. See
// TestAudioSessionSafetyExemptActionsDispatchDegraded for stop/clear/
// output.mute, whose own exemption predates this amendment and is
// unchanged by it.
func TestAudioSessionDispatchAuditWriteFailureRunsWithDegradedAttribution(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "paused", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, setup.storeDir)

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/pause", `{"revision":1}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (dispatched with degraded attribution, never refused); body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1 - an audit-write failure must not stop dispatch", setup.pub.count())
	}
	var decoded struct {
		Command struct {
			AttributionDegraded bool `json:"attributionDegraded"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v; body: %s", err, body)
	}
	if !decoded.Command.AttributionDegraded {
		t.Errorf("attributionDegraded = false, want true (the audit store is failing)")
	}
}

// TestAudioSessionSafetyExemptActionsDispatchDegraded proves ADR-024
// decision 11's safety-class exemption for this file's audience-audio
// equivalent of blackout: audio.session.stop, audio.session.clear, and
// audio.output.mute must still dispatch - with AttributionDegraded true
// - when the audit store cannot be written, never refused. A refusal
// here would leave an operator unable to silence the show over a full
// audit disk, which ADR-024 decision 7 makes strictly worse than the
// coordinator being switched off.
func TestAudioSessionSafetyExemptActionsDispatchDegraded(t *testing.T) {
	for _, path := range []string{"stop", "clear", "output/mute"} {
		t.Run(path, func(t *testing.T) {
			setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
			setup.pub.result = mqttproto.ResultPayload{
				Outcome: mqttproto.OutcomeConfirmed,
				Evidence: &mqttproto.ResultEvidence{
					Value: map[string]any{"outcome": "stopped", "reason": ""},
				},
			}
			op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
			token := mustIssueToken(t, setup.svc, op.ID)
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

			installFailAuditTrigger(t, setup.storeDir)

			req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/"+path, `{"revision":1}`, token)
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 (dispatched with degraded attribution, never refused); body: %s", resp.StatusCode, body)
			}
			if setup.pub.count() != 1 {
				t.Fatalf("publish count = %d, want 1 - the safety-class exemption must still dispatch", setup.pub.count())
			}
			var decoded struct {
				Command struct {
					AttributionDegraded bool `json:"attributionDegraded"`
				} `json:"command"`
			}
			if err := json.Unmarshal(body, &decoded); err != nil {
				t.Fatalf("decode response: %v; body: %s", err, body)
			}
			if !decoded.Command.AttributionDegraded {
				t.Errorf("attributionDegraded = false, want true (the audit store is failing)")
			}
		})
	}
}

// TestAudioSessionDispatchReplayAcrossDifferentNodeIsConflict proves an
// idempotency key reused against the SAME action but a DIFFERENT target
// node is a 409 conflict, never answered as a replay of the ORIGINAL
// node's result - a session reassigned to a different node must not let
// a redelivered/reused key replay the old node's outcome as if it
// answered a request to the new one.
func TestAudioSessionDispatchReplayAcrossDifferentNodeIsConflict(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "stopped", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	key := `{"revision":1,"idempotencyKey":"reused-across-nodes"}`
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop", key, token)
	if resp, body := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d; body: %s", resp.StatusCode, body)
	}

	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-b/audio/sessions/night-session/stop", key, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("cross-node reuse status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after cross-node reuse = %d, want still 1 - a conflict must never dispatch", setup.pub.count())
	}
}

// TestAudioSessionDispatchBeforePublishFailureDoesNotClaimDispatch proves
// finding 12's distinction: when AwaitResponse fails before anything is
// published (broker.ErrResponseFailedBeforePublish), the stored commands
// row must not claim DispatchedAt - that would assert a publish that
// never reached the wire, indistinguishable from a genuine dispatch whose
// result never arrived.
func TestAudioSessionDispatchBeforePublishFailureDoesNotClaimDispatch(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.beforePublishErr = errors.New("injected subscribe failure")
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/stop",
		`{"revision":1,"idempotencyKey":"before-publish-key"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 - nothing reached the wire", setup.pub.count())
	}

	rec, err := setup.st.GetCommandByIdempotencyKey(context.Background(), "before-publish-key")
	if err != nil {
		t.Fatalf("GetCommandByIdempotencyKey: %v", err)
	}
	if rec.DispatchedAt != nil {
		t.Fatalf("DispatchedAt = %v, want nil - nothing was ever published", *rec.DispatchedAt)
	}
}

// TestAudioSessionDispatchUnconfirmablePersistsDesiredState proves that
// an unconfirmable outcome - the ordinary result of a dispatch against
// every shipped engine today, and the outcome a real engine also
// produces whenever it applies a command but cannot confirm it - still
// persists desired state. Without this, the audio_sessions table never
// gets a row in any configuration this project ships. It also proves the
// store's own anti-rewind guard, not an outcome check, is what stops a
// stale revision from rewinding an already-persisted record.
func TestAudioSessionDispatchUnconfirmablePersistsDesiredState(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeUnconfirmed,
		Reason:  "operation applied, but the post-write read-back evidence did not match the requested value",
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "unconfirmable", "reason": "engine applied it but confirmation timed out"},
		},
	}
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":5,"params":{"media":{"assetId":"clip-1"}}}`, token)
	resp1, body1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d; body: %s", resp1.StatusCode, body1)
	}
	var decoded1 struct {
		Command struct {
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body1, &decoded1); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded1.Command.Outcome != "unconfirmable" {
		t.Fatalf("outcome = %q, want unconfirmable", decoded1.Command.Outcome)
	}

	rec, err := setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession after unconfirmable dispatch: %v", err)
	}
	if rec.NodeID != "node-a" || rec.Revision != 5 {
		t.Fatalf("persisted record = %+v, want nodeID node-a revision 5", rec)
	}
	if !strings.Contains(rec.DesiredJSON, "clip-1") {
		t.Fatalf("persisted desired state = %q, want it to contain the dispatched media", rec.DesiredJSON)
	}

	// A stale, lower revision must still not rewind the persisted record
	// even though this outcome is also unconfirmable - that guarantee
	// lives in the store's own ON CONFLICT ... WHERE clause
	// (audiosessions.go), not in which outcomes this file chooses to
	// persist.
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeUnconfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "unconfirmable", "reason": "confirmation timed out"},
		},
	}
	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":2,"params":{"media":{"assetId":"clip-STALE"}}}`, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("second apply status = %d; body: %s", resp2.StatusCode, body2)
	}

	rec2, err := setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession after stale-revision dispatch: %v", err)
	}
	if rec2.Revision != 5 {
		t.Fatalf("stored revision after a stale-revision dispatch = %d, want it to stay at 5", rec2.Revision)
	}
	if strings.Contains(rec2.DesiredJSON, "clip-STALE") {
		t.Fatalf("stored desired state = %q, must not contain the stale-revision request's own params", rec2.DesiredJSON)
	}
}

// TestAudioSessionDispatchDeadlineExceededPersistsDesiredState proves
// that a dispatch whose confirmation deadline expires - the node may
// well have already acted - also persists desired state, not only the
// decoded-result path. A mismatched result is what drives the fake
// publisher to return broker.ErrResponseDeadlineExceeded, the same path
// a real deadline takes.
func TestAudioSessionDispatchDeadlineExceededPersistsDesiredState(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.noAutoCorrelate = true
	setup.pub.result = mqttproto.ResultPayload{
		CommandID: "some-other-command", IdempotencyKey: "some-other-key", Action: "audio.session.apply",
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Value: map[string]any{"outcome": "position", "reason": ""},
		},
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/sessions/night-session/apply",
		`{"revision":5,"params":{"media":{"assetId":"clip-1"}}}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status = %d; body: %s", resp.StatusCode, body)
	}
	var decoded struct {
		Command struct {
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Command.Outcome != "unconfirmable" {
		t.Fatalf("outcome = %q, want unconfirmable", decoded.Command.Outcome)
	}

	rec, err := setup.st.GetAudioSession(context.Background(), "node-a", "night-session")
	if err != nil {
		t.Fatalf("GetAudioSession after deadline-exceeded dispatch: %v", err)
	}
	if rec.NodeID != "node-a" || rec.Revision != 5 {
		t.Fatalf("persisted record = %+v, want nodeID node-a revision 5", rec)
	}
}

// TestAudioOutcomeShouldPersistRejectsUnrecognisedOutcomes proves the
// persistence gate is an allowlist. An outcome this coordinator does not
// recognise is not evidence that the command reached the node.
func TestAudioOutcomeShouldPersistRejectsUnrecognisedOutcomes(t *testing.T) {
	for _, outcome := range []string{"", "ok", "success", "acknowledged", "refused", "failed"} {
		if audioOutcomeShouldPersist(outcome) {
			t.Errorf("audioOutcomeShouldPersist(%q) = true, want false", outcome)
		}
	}
	for _, outcome := range []string{"started", "position", "gain", "fade_complete", "stopped", "completed", "unconfirmable"} {
		if !audioOutcomeShouldPersist(outcome) {
			t.Errorf("audioOutcomeShouldPersist(%q) = false, want true", outcome)
		}
	}
}

// evidenceResult builds a mqttproto.ResultPayload carrying the given
// outcome/reason pair as node evidence, the shape mapResultOutcome reads
// its finer-grained outcome from.
func evidenceResult(outcome string, reason any) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		CommandID: "irrelevant", IdempotencyKey: "irrelevant", Action: "audio.session.stop",
		Outcome: mqttproto.OutcomeUnconfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session.stop",
			Value:  map[string]any{"outcome": outcome, "reason": reason},
		},
	}
}

// TestMapResultOutcomeRejectsReasonlessRequiredOutcome proves the
// coordinator's fix for each of the three outcomes pkgaudio.
// OutcomeResult.Validate requires a reason for: the coordinator keeps
// the node's own outcome
// word and replaces a blank reason with its own statement of the
// violation, asserted exactly so a mutation that swaps the outcome word
// into the wrong message, or reuses one outcome's text for another, is
// caught.
func TestMapResultOutcomeRejectsReasonlessRequiredOutcome(t *testing.T) {
	for _, outcome := range []string{"refused", "failed", "unconfirmable"} {
		gotOutcome, gotReason := mapResultOutcome(evidenceResult(outcome, ""))
		if gotOutcome != outcome {
			t.Errorf("mapResultOutcome(%q, reason=\"\") outcome = %q, want %q (verdict must survive)", outcome, gotOutcome, outcome)
		}
		wantReason := fmt.Sprintf("node reported outcome %q with no reason; the coordinator did not receive one and the outcome requires one", outcome)
		if gotReason != wantReason {
			t.Errorf("mapResultOutcome(%q, reason=\"\") reason = %q, want %q", outcome, gotReason, wantReason)
		}
	}
}

// TestMapResultOutcomeLeavesReasonedRefusalUntouched is the acceptance
// test for this fix: enforcement must never rewrite a reason the node
// actually supplied. A refusal that already carries a reason must pass
// through byte for byte, because an over-eager fix that rewrites good
// reasons is a worse bug than the gap it closes.
func TestMapResultOutcomeLeavesReasonedRefusalUntouched(t *testing.T) {
	const reason = "downstream device reported input already in use by another session"
	gotOutcome, gotReason := mapResultOutcome(evidenceResult("refused", reason))
	if gotOutcome != "refused" {
		t.Fatalf("outcome = %q, want refused", gotOutcome)
	}
	if gotReason != reason {
		t.Fatalf("reason = %q, want %q unchanged", gotReason, reason)
	}
}

// TestMapResultOutcomeLeavesUnrequiredOutcomeReasonEmpty proves
// enforcement does not leak onto outcomes outcomesRequiringReason does
// not cover. "started" is an observation, not a failure; an empty
// reason on it is not a violation and must stay empty.
func TestMapResultOutcomeLeavesUnrequiredOutcomeReasonEmpty(t *testing.T) {
	gotOutcome, gotReason := mapResultOutcome(evidenceResult("started", ""))
	if gotOutcome != "started" {
		t.Fatalf("outcome = %q, want started", gotOutcome)
	}
	if gotReason != "" {
		t.Fatalf("reason = %q, want empty (started does not require a reason)", gotReason)
	}
}

// TestMapResultOutcomeTreatsNonStringReasonAsMissing proves the
// unchecked type assertion on v["reason"] (r, _ := v["reason"].(string))
// does not let a non-string reason read as satisfied, or panic: a
// refusal whose "reason" field is a number must be treated exactly like
// a refusal with no "reason" field at all.
func TestMapResultOutcomeTreatsNonStringReasonAsMissing(t *testing.T) {
	gotOutcome, gotReason := mapResultOutcome(evidenceResult("refused", 42))
	if gotOutcome != "refused" {
		t.Fatalf("outcome = %q, want refused", gotOutcome)
	}
	wantReason := `node reported outcome "refused" with no reason; the coordinator did not receive one and the outcome requires one`
	if gotReason != wantReason {
		t.Fatalf("reason = %q, want %q", gotReason, wantReason)
	}
}
