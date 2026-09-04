package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is audio.node.silence's own dispatch acceptance proof,
// following audiodispatch_test.go's established pattern one file over:
// a real store.Store, a real identity.Service over it, and
// fakeAudioPublisher standing in for the one genuinely external
// dependency.

func TestAudioNodeSilenceDispatchRefusedForbiddenViewer(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", `{}`, token)
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

func TestAudioNodeSilenceDispatchNotReachableByGET(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodGet, "/api/v1/nodes/node-a/audio/silence", "", token)
	resp, _ := doRawRequest(t, api.Handler, req)
	if resp.StatusCode == http.StatusOK {
		t.Fatal("GET against the audio.node.silence dispatch path must never succeed (ADR-024 decision 6)")
	}
	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 - GET must never dispatch", setup.pub.count())
	}
}

// audioNodeSilenceAgentEvidence builds the mqttproto.ResultPayload a real
// agent's silenceNode (internal/agent/audionodesilenceops.go) produces:
// Evidence.Value carries "sessionsFound"/"sessions", never a single
// top-level "outcome" key the way audio.session.* evidence does.
func audioNodeSilenceAgentEvidence(outcome string, sessions []map[string]any) mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: outcome,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio.silence",
			Value: map[string]any{
				"sessionsFound": float64(len(sessions)),
				"sessions":      toAnySlice(sessions),
			},
		},
	}
}

func toAnySlice(sessions []map[string]any) []any {
	out := make([]any, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, s)
	}
	return out
}

// TestAudioNodeSilenceDispatchReturnsPerSessionResultsAndCount is this
// route's own happy-path acceptance proof: a confirmed dispatch reports
// both the per-session results and the count the agent reported, read
// from the agent's own sessionsFound/sessions evidence shape - not
// audiodispatch.go's single sessionId/outcome pair, which this evidence
// does not carry.
func TestAudioNodeSilenceDispatchReturnsPerSessionResultsAndCount(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = audioNodeSilenceAgentEvidence(mqttproto.OutcomeConfirmed, []map[string]any{
		{"sessionId": "cue", "outcome": "stopped", "reason": ""},
		{"sessionId": "blackAndSilence", "outcome": "stopped", "reason": ""},
	})
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", `{}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1", setup.pub.count())
	}
	if setup.pub.lastAction != "audio.node.silence" {
		t.Fatalf("dispatched action = %q, want audio.node.silence", setup.pub.lastAction)
	}
	if len(setup.pub.lastParams) != 0 {
		t.Fatalf("dispatched params = %v, want empty - audio.node.silence takes no params", setup.pub.lastParams)
	}

	var decoded struct {
		Command struct {
			Outcome       string `json:"outcome"`
			SessionsFound int    `json:"sessionsFound"`
			Sessions      []struct {
				SessionID string `json:"sessionId"`
				Outcome   string `json:"outcome"`
			} `json:"sessions"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Command.Outcome != "confirmed" {
		t.Fatalf("outcome = %q, want confirmed", decoded.Command.Outcome)
	}
	if decoded.Command.SessionsFound != 2 {
		t.Fatalf("sessionsFound = %d, want 2", decoded.Command.SessionsFound)
	}
	if len(decoded.Command.Sessions) != 2 {
		t.Fatalf("sessions = %v, want 2 entries", decoded.Command.Sessions)
	}
	if decoded.Command.Sessions[0].SessionID != "cue" || decoded.Command.Sessions[1].SessionID != "blackAndSilence" {
		t.Fatalf("sessions = %+v, want cue then blackAndSilence in order", decoded.Command.Sessions)
	}
}

// TestAudioNodeSilenceDispatchSurfacesAgentRefusalReason proves finding
// 5's own path: a node whose agent predates audio.node.silence refuses it
// through pkg/audio.Operation's closed set (internal/agent/command.go's
// h.ops lookup, on an old agent build, never populates this action), and
// the coordinator must surface that agent's OWN refusal reason verbatim,
// as outcome "refused", not flatten it into a generic failure or
// "unconfirmable".
func TestAudioNodeSilenceDispatchSurfacesAgentRefusalReason(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	const agentReason = `operation "audio.node.silence" is not on the agent's allowlist`
	setup.pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeRefused,
		Reason:  agentReason,
	}
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", `{}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var decoded struct {
		Command struct {
			Outcome       string `json:"outcome"`
			Reason        string `json:"reason"`
			SessionsFound int    `json:"sessionsFound"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if decoded.Command.Outcome != "refused" {
		t.Fatalf("outcome = %q, want refused", decoded.Command.Outcome)
	}
	if decoded.Command.Reason != agentReason {
		t.Fatalf("reason = %q, want the agent's own refusal reason %q verbatim", decoded.Command.Reason, agentReason)
	}
	if decoded.Command.SessionsFound != 0 {
		t.Fatalf("sessionsFound = %d, want 0 - a refusal never dispatched to any session", decoded.Command.SessionsFound)
	}
}

// TestAudioNodeSilenceIsSafetyExemptAndDispatchesDegraded proves
// audio.node.silence's own ADR-024 decision 11 safety-class exemption
// (audiodispatch.go's audioSafetyExemptActions): it still dispatches -
// with AttributionDegraded true - when the audit store cannot be
// written, never refused, matching stop/clear/output.mute's own
// exemption exactly.
func TestAudioNodeSilenceIsSafetyExemptAndDispatchesDegraded(t *testing.T) {
	if !audioSafetyExemptActions["audio.node.silence"] {
		t.Fatal(`audioSafetyExemptActions["audio.node.silence"] = false, want true`)
	}

	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = audioNodeSilenceAgentEvidence(mqttproto.OutcomeConfirmed, nil)
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	installFailAuditTrigger(t, setup.storeDir)

	req := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", `{}`, token)
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
}

// TestAudioNodeSilenceDispatchReplayReturnsExistingOutcomeWithoutRepublishing
// mirrors TestAudioSessionDispatchReplayReturnsExistingOutcomeWithoutRepublishing
// one file over: a reused idempotency key against the SAME node dispatches
// nothing and returns the original command's own result, flagged
// replay:true.
func TestAudioNodeSilenceDispatchReplayReturnsExistingOutcomeWithoutRepublishing(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = audioNodeSilenceAgentEvidence(mqttproto.OutcomeConfirmed, []map[string]any{
		{"sessionId": "cue", "outcome": "stopped", "reason": ""},
	})
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	body := `{"idempotencyKey":"fixed-silence-key-1"}`
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", body, token)
	resp1, _ := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d", resp1.StatusCode)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after first dispatch = %d, want 1", setup.pub.count())
	}

	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", body, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replayed dispatch status = %d; body: %s", resp2.StatusCode, body2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after replay = %d, want still 1 - a replayed idempotency key must not re-dispatch", setup.pub.count())
	}
	var decoded struct {
		Command struct {
			Replay        bool `json:"replay"`
			SessionsFound int  `json:"sessionsFound"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body2, &decoded); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !decoded.Command.Replay {
		t.Fatal("replayed response did not report replay:true")
	}
	if decoded.Command.SessionsFound != 1 {
		t.Fatalf("replayed sessionsFound = %d, want the ORIGINAL dispatch's own count 1", decoded.Command.SessionsFound)
	}
}

// TestAudioNodeSilenceDispatchReplayAcrossDifferentNodeIsConflict mirrors
// TestAudioSessionDispatchReplayAcrossDifferentNodeIsConflict one file
// over: the SAME idempotency key reused against a DIFFERENT node is a 409
// conflict, never answered as a replay of the original node's result.
func TestAudioNodeSilenceDispatchReplayAcrossDifferentNodeIsConflict(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = audioNodeSilenceAgentEvidence(mqttproto.OutcomeConfirmed, nil)
	op := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, op.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	key := `{"idempotencyKey":"reused-silence-key"}`
	req1 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-a/audio/silence", key, token)
	if resp, body := doRawRequest(t, api.Handler, req1); resp.StatusCode != http.StatusOK {
		t.Fatalf("first dispatch status = %d; body: %s", resp.StatusCode, body)
	}

	req2 := newAudioRequest(t, http.MethodPost, "/api/v1/nodes/node-b/audio/silence", key, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("cross-node reuse status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	if setup.pub.count() != 1 {
		t.Fatalf("publish count after cross-node reuse = %d, want still 1 - a conflict must never dispatch", setup.pub.count())
	}
}
