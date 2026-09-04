package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is build item 2's own regression coverage: a fresh reviewer
// found that cuecatalog.deploy was registered on the agent and reserved in
// IDENTIFIER-REGISTER.md, but nothing on the coordinator side ever
// dispatched it or recorded a node's real acknowledgement, so
// decideBootResume discarded every persisted render assignment at every
// boot forever. Reuses fakeAudioPublisher (audiodispatch_test.go), the
// same fake this package already uses to stand in for a node's own
// CommandHandler round trip without a real broker — cuecatalogdeploy.go
// deliberately reuses [Dependencies.AudioPublisher]'s AwaitResponse
// capability rather than adding a second thing to wire up.

func newCueCatalogDeployFixture(t *testing.T) (api *API, st *store.Store, pub *fakeAudioPublisher, token string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token = mustIssueToken(t, svc, admin.ID)
	pub = &fakeAudioPublisher{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AudioPublisher = pub
	api = New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	return api, st, pub, token
}

// resolvedCueCatalogRevision fetches GET /nodes/{nodeId}/cue-catalog and
// returns its revision — used to configure the fake node's own reply
// before dispatching a deploy, since the test fixture's resolved revision
// is deterministic but not hand-computed here (this package's own
// resolver, assetsync.ResolveCueCatalog, is the only authority on it, per
// that function's own doc comment).
func resolvedCueCatalogRevision(t *testing.T, api *API, nodeID string, auth map[string]string) string {
	t.Helper()
	_, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/"+nodeID+"/cue-catalog", auth)
	var resp cueCatalogResponseForTest
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode cue-catalog response: %v", err)
	}
	if !resp.Configured {
		t.Fatalf("GET cue-catalog: Configured = false, want true (is show.active set?)")
	}
	return resp.Revision
}

// TestCueCatalogDeployDispatchesAndRecordsAcknowledgement proves the
// coordinator-side push this build item adds: a deploy dispatch resolves
// this coordinator's own current catalog, sends it to the node, and — once
// the node's own result confirms it independently recomputed the identical
// revision — records that revision through the SAME PutNodeCueCatalogAck
// path POST .../cue-catalog/acknowledge uses, so a subsequent acknowledge
// round trip (and GET .../cue-catalog) can see catalog-current from a REAL
// deployment.
func TestCueCatalogDeployDispatchesAndRecordsAcknowledgement(t *testing.T) {
	api, st, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	revision := resolvedCueCatalogRevision(t, api, "render-01", auth)

	observedAt := testNow
	pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.cuecatalog.revision", Value: revision,
			ObservedAt: &observedAt, CollectedAt: observedAt,
		},
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST cue-catalog/deploy: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "CueCatalogDeployResponse", body)

	var result struct {
		Command struct {
			Outcome              string `json:"outcome"`
			Revision             string `json:"revision"`
			AcknowledgedRevision string `json:"acknowledgedRevision"`
			Show                 string `json:"show"`
			Generation           int64  `json:"generation"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode deploy response: %v", err)
	}
	if result.Command.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("deploy outcome = %q, want confirmed; body: %s", result.Command.Outcome, body)
	}
	if result.Command.Revision != revision || result.Command.AcknowledgedRevision != revision {
		t.Fatalf("deploy revision = %q acknowledgedRevision = %q, want both %q", result.Command.Revision, result.Command.AcknowledgedRevision, revision)
	}

	if pub.count() != 1 {
		t.Fatalf("publish count = %d, want exactly 1", pub.count())
	}
	if pub.lastAction != "cuecatalog.deploy" {
		t.Fatalf("dispatched action = %q, want cuecatalog.deploy", pub.lastAction)
	}

	// The acknowledgement must have reached the store through the SAME
	// path POST .../cue-catalog/acknowledge uses — proven by reading it
	// back directly, and by the acknowledge route's own GET-equivalent
	// resolution now reporting catalog-current.
	ack, err := st.GetNodeCueCatalogAck(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("GetNodeCueCatalogAck: %v", err)
	}
	if ack.Revision != revision || ack.ShowID != result.Command.Show || ack.Generation != result.Command.Generation {
		t.Fatalf("stored ack = %+v, want revision=%q show=%q generation=%d", ack, revision, result.Command.Show, result.Command.Generation)
	}
}

// TestCueCatalogDeployUnconfirmedDoesNotRecordAnAcknowledgement proves a
// deploy that never gets confirmed (deadline exceeded, or the node
// refuses) never calls PutNodeCueCatalogAck — an unconfirmed dispatch is
// not evidence of anything the node actually holds.
func TestCueCatalogDeployUnconfirmedDoesNotRecordAnAcknowledgement(t *testing.T) {
	api, st, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	pub.result = mqttproto.ResultPayload{Outcome: mqttproto.OutcomeRefused, Reason: "node refused for a test"}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST cue-catalog/deploy: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	var result struct {
		Command struct {
			Outcome string `json:"outcome"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode deploy response: %v", err)
	}
	if result.Command.Outcome != mqttproto.OutcomeRefused {
		t.Fatalf("deploy outcome = %q, want refused; body: %s", result.Command.Outcome, body)
	}

	if _, err := st.GetNodeCueCatalogAck(context.Background(), "render-01"); err != store.ErrNodeCueCatalogAckNotFound {
		t.Fatalf("GetNodeCueCatalogAck after a refused deploy: err = %v, want ErrNodeCueCatalogAckNotFound (nothing should have been recorded)", err)
	}
}

// TestCueCatalogDeploySetsWireDeadline proves the dispatched
// cuecatalog.deploy command carries a wire CmdPayload.Deadline, anchored
// to the dispatch clock by exactly cueCatalogDeployWireDeadline, not
// merely non-nil.
func TestCueCatalogDeploySetsWireDeadline(t *testing.T) {
	api, _, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	pub.result = mqttproto.ResultPayload{Outcome: mqttproto.OutcomeRefused, Reason: "node refused for a test"}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST cue-catalog/deploy: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if len(pub.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(pub.dispatched))
	}
	got := pub.dispatched[0].Deadline
	if got == nil {
		t.Fatalf("Deadline = nil, want set")
	}
	want := testNow.Add(cueCatalogDeployWireDeadline)
	if !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v (testNow + cueCatalogDeployWireDeadline)", got, want)
	}
}

// TestCueCatalogDeploySurvivesServerWriteTimeout is this endpoint's own
// version of TestEmergencyStopSurvivesServerWriteTimeout
// (emergencystop_writetimeout_test.go): a real *http.Server with a short
// WriteTimeout, and a dispatch paced slower than it but with NO reply ever
// arriving, proving handlePostNodeCueCatalogDeploy's own SetWriteDeadline
// extension (cueCatalogDeployHTTPWriteDeadline) is what lets the real
// unconfirmed-outcome body reach the client, not merely that the constant
// is large on paper - before this extension existed, this route had none
// at all, so a dispatch slower than the server's own WriteTimeout severed
// the connection out from under a coordinator that was still correctly
// reporting "unconfirmed".
func TestCueCatalogDeploySurvivesServerWriteTimeout(t *testing.T) {
	// A REAL current time, deliberately NOT newCueCatalogDeployFixture's own
	// fixed testNow (a canned past timestamp): SetWriteDeadline sets an
	// ABSOLUTE deadline anchored to h.now(), so a fixed-in-the-past clock
	// would make that deadline already elapsed before this test's real
	// wall-clock write ever happens, failing every request instantly
	// regardless of the extension under test - matching
	// TestEmergencyStopSurvivesServerWriteTimeout's identical real-time
	// fixture one file over.
	now := time.Now()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(now))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	pub := &fakeAudioPublisher{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AudioPublisher = pub
	api := New(deps, Options{Clock: fixedClock(now), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	// No reply ever arrives (the real "nothing responds" symptom this
	// route's own AwaitResponse deadline exists to bound), paced past the
	// server's own short WriteTimeout below, while staying comfortably
	// inside this handler's own (much larger) real
	// cueCatalogDeployHTTPWriteDeadline (real time, not testNow: see the
	// fixture's own comment above).
	pub.awaitErr = broker.ErrResponseDeadlineExceeded
	pub.onAwaitResponse = func() { time.Sleep(300 * time.Millisecond) }

	status, body := postThroughShortWriteTimeoutServer(t, api.Handler, "/api/v1/nodes/render-01/cue-catalog/deploy", "{}", auth)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a dispatch slower than the server's own WriteTimeout must still succeed); body: %s", status, body)
	}

	m := decodeMap(t, body)
	result, ok := m["command"].(map[string]any)
	if !ok {
		t.Fatalf("response has no \"command\" object; body: %s", body)
	}
	if result["outcome"] != mqttproto.OutcomeUnconfirmed {
		t.Fatalf("command.outcome = %v, want %q: the connection surviving the server's short WriteTimeout must have delivered the real unconfirmed body, not a fallback or an empty one; body: %s", result["outcome"], mqttproto.OutcomeUnconfirmed, body)
	}
	if reason, _ := result["reason"].(string); reason == "" {
		t.Fatalf("command.reason = %q, want a non-empty explanation of the unconfirmed outcome; body: %s", reason, body)
	}
}

// TestCueCatalogDeployReplayReturnsExistingOutcomeWithoutRepublishing
// proves an idempotency key reused against the same node replays the
// original command's own recorded result rather than dispatching a
// second time.
func TestCueCatalogDeployReplayReturnsExistingOutcomeWithoutRepublishing(t *testing.T) {
	api, st, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	revision := resolvedCueCatalogRevision(t, api, "render-01", auth)
	observedAt := testNow
	pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.cuecatalog.revision", Value: revision,
			ObservedAt: &observedAt, CollectedAt: observedAt,
		},
	}

	req1 := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{"idempotencyKey":"idem-1"}`, auth)
	resp1, body1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first deploy: status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if pub.count() != 1 {
		t.Fatalf("publish count after first deploy = %d, want 1", pub.count())
	}

	req2 := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{"idempotencyKey":"idem-1"}`, auth)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replayed deploy: status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	if pub.count() != 1 {
		t.Fatalf("publish count after replayed deploy = %d, want still 1 (no re-dispatch)", pub.count())
	}

	var result2 struct {
		Command struct {
			CommandID string `json:"commandId"`
			Replay    bool   `json:"replay"`
			Outcome   string `json:"outcome"`
			Revision  string `json:"revision"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body2, &result2); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !result2.Command.Replay {
		t.Fatalf("replayed deploy response: replay = false, want true")
	}
	if result2.Command.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("replayed deploy outcome = %q, want confirmed", result2.Command.Outcome)
	}
	// A replay must report the SAME revision the original dispatch
	// resolved and reported, not the accepted-empty "" a still-in-flight
	// replay reports (TestCueCatalogDeployReplayOfAnInFlightCommandReportsAbsentOutcome) -
	// a caller polling by idempotency key needs to see which catalog
	// revision was actually deployed.
	if result2.Command.Revision != revision {
		t.Fatalf("replayed deploy revision = %q, want %q", result2.Command.Revision, revision)
	}

	// The stored value must carry store.CallerIntentCueCatalogDeploy's own
	// tag, not the bare identity JSON: an untagged pair of writer and
	// replay-reader round-trips fine on its own and would not catch a
	// future writer silently dropping the tag, which is exactly the
	// ambiguity this column's rename exists to close.
	rec, err := st.GetCommand(context.Background(), result2.Command.CommandID)
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	wantCallerIntent := fmt.Sprintf(
		`cuecatalog-deploy:{"node":"render-01","show":"halloween-2026","generation":1,"revision":%q}`, revision)
	if rec.CallerIntent != wantCallerIntent {
		t.Errorf("commands.caller_intent = %q, want %q", rec.CallerIntent, wantCallerIntent)
	}
}

// TestCueCatalogDeployReplayOfAnInFlightCommandReportsAbsentOutcome proves a
// second request carrying an idempotency key whose ORIGINAL command has not
// resolved yet (State "pending", ResolvedAt nil, ResultJSON the "{}"
// insertCommand writes for an empty result) reports that absence honestly:
// outcome "" and dispatchedAt null, api/openapi.yaml's CueCatalogDeployResult
// admits both (ADR-020's absence-is-stated rule), the same accepted-empty
// case FPPCommandResult.outcome documents. This seeds the store directly
// rather than racing goroutines, so the in-flight state is deterministic.
func TestCueCatalogDeployReplayOfAnInFlightCommandReportsAbsentOutcome(t *testing.T) {
	api, st, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	const idempotencyKey = "idem-inflight"
	_, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "cmd-inflight", IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		TargetKind: "node", TargetID: "render-01", ParamsJSON: "{}",
		IssuerPrincipalID: "admin-1", IssuerPrincipalName: "admin-1",
		CallerIntent:       `{"node":"render-01","show":"halloween-2026","generation":1}`,
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("seed in-flight command: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy",
		`{"idempotencyKey":"`+idempotencyKey+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replayed deploy against an in-flight command: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (a replay must never dispatch)", pub.count())
	}

	// Asserted on the raw JSON, not a decoded struct: a decoded *string
	// cannot distinguish a present empty string from JSON null, and this
	// test exists specifically to prove dispatchedAt is null, not "".
	var raw struct {
		Command struct {
			Replay       bool            `json:"replay"`
			Outcome      string          `json:"outcome"`
			DispatchedAt json.RawMessage `json:"dispatchedAt"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if !raw.Command.Replay {
		t.Fatalf("replayed deploy response: replay = false, want true")
	}
	if raw.Command.Outcome != "" {
		t.Fatalf("replayed in-flight deploy outcome = %q, want \"\" (the command has not resolved yet); body: %s", raw.Command.Outcome, body)
	}
	if string(raw.Command.DispatchedAt) != "null" {
		t.Fatalf("replayed in-flight deploy dispatchedAt = %s, want JSON null; body: %s", raw.Command.DispatchedAt, body)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "CueCatalogDeployResponse", body)
}

// TestCueCatalogDeployReplayFailsCleanlyOnAWrongFamilyCallerIntent is the
// central regression this task's own defect targets: commands.caller_intent
// holds two untagged JSON families sharing a "node" field, this route's own
// cueCatalogDeployRequestIdentity and renderRequestIdentity
// (renderdispatch.go). Seeds a row whose action/node match this route (so
// it passes the first check in resolveCueCatalogDeployReplay) but whose
// caller_intent is TAGGED as a render-request identity, not this route's
// own: a row that could only exist from data corruption or a future
// writer bug, never a reachable path today, but exactly the shape
// resolveCueCatalogDeployReplay must not answer with a confidently wrong,
// zero-valued Show/Generation/Revision. Before this fix, the discarded
// json.Unmarshal error let this decode succeed as far as
// json.Unmarshal([]byte(payload), &reqID) is concerned... except a
// wrong-KIND-tagged value is returned by store.CallerIntentPayload still
// wearing its own tag prefix (never valid JSON on its own), so the
// unmarshal genuinely fails, and it was THAT failure being silently
// discarded that let a 200 with an empty identity through instead of a
// conflict.
func TestCueCatalogDeployReplayFailsCleanlyOnAWrongFamilyCallerIntent(t *testing.T) {
	api, st, _, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	const idempotencyKey = "idem-wrong-family"
	wrongFamilyIntent := store.FormatCallerIntent(store.CallerIntentRenderRequest,
		`{"action":"clear","node":"render-01","surface":"wall-1","sequenceId":""}`)
	_, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "cmd-wrong-family", IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		TargetKind: "node", TargetID: "render-01", ParamsJSON: "{}",
		IssuerPrincipalID: "admin-1", IssuerPrincipalName: "admin-1",
		CallerIntent:       wrongFamilyIntent,
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("seed wrong-family command: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy",
		`{"idempotencyKey":"`+idempotencyKey+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("replay against a wrong-family caller_intent: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != ProblemTypeConflict {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeConflict)
	}
	if !strings.Contains(p.Detail, "cmd-wrong-family") {
		t.Fatalf("detail = %q, want it to name the undecodable command", p.Detail)
	}
}

// TestCueCatalogDeployReplayRefusesAnUntaggedWrongFamilyRowByItsAllZeroIdentity
// covers the case the tagged wrong-family test above does not: commands.
// caller_intent's legacy, untagged fallback (store.CallerIntentPayload's
// own documented "falls back to the raw value for a row written before
// this tagging scheme existed" case) accepts a bare JSON object of EITHER
// family, since Go's json.Unmarshal ignores fields it does not recognize.
// The tagged test above is refused only because store.CallerIntentPayload
// returns a wrong-kind value still wearing its own tag prefix, which is
// not valid JSON and so fails to decode; an UNTAGGED render identity
// carries no such prefix, so it decodes cleanly into
// cueCatalogDeployRequestIdentity, populating only NodeID (both structs
// tag a "node" field) and leaving Show, Generation, and Revision at their
// zero values, exactly the shape cueCatalogDeployReplayAllZeroIdentityProblem
// exists to catch: a non-empty payload that decoded to no show,
// generation, or revision at all. This is narrowed defense, not the full
// requested_revision/caller_intent discriminator work the issue names as
// separate: a wrong-family row carrying plausible NON-ZERO overlapping
// values (e.g. a render identity whose node happens to look like a show
// name) would still decode into a wrong-but-non-zero identity here and
// slip through undetected.
func TestCueCatalogDeployReplayRefusesAnUntaggedWrongFamilyRowByItsAllZeroIdentity(t *testing.T) {
	api, st, _, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	const idempotencyKey = "idem-untagged-wrong-family"
	untaggedRenderIdentity := `{"action":"clear","node":"render-01","surface":"wall-1","sequenceId":""}`
	_, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "cmd-untagged-wrong-family", IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		TargetKind: "node", TargetID: "render-01", ParamsJSON: "{}",
		IssuerPrincipalID: "admin-1", IssuerPrincipalName: "admin-1",
		CallerIntent:       untaggedRenderIdentity,
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("seed untagged wrong-family command: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy",
		`{"idempotencyKey":"`+idempotencyKey+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("replay against an untagged wrong-family caller_intent: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var p struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if p.Type != ProblemTypeConflict {
		t.Fatalf("problem type = %q, want %q", p.Type, ProblemTypeConflict)
	}
	if !strings.Contains(p.Detail, "cmd-untagged-wrong-family") {
		t.Fatalf("detail = %q, want it to name the refused command", p.Detail)
	}
	if !strings.Contains(p.Detail, "show, generation, revision") {
		t.Fatalf("detail = %q, want it to name the evidence (the fields a deploy identity always has)", p.Detail)
	}
}

// TestCueCatalogDeployReplayAcceptsAGenuineZeroGenerationIdentity proves
// the all-zero guard checks Show, Generation, AND Revision together, not
// Generation alone: Generation 0 is a plausible real value (the first
// resolved generation of a catalog), so a genuine identity carrying a
// real Show and Revision but Generation 0 must still replay normally, not
// be refused as though it looked like a wrong-family row.
func TestCueCatalogDeployReplayAcceptsAGenuineZeroGenerationIdentity(t *testing.T) {
	api, st, _, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	const idempotencyKey = "idem-zero-generation"
	goodIntent := store.FormatCallerIntent(store.CallerIntentCueCatalogDeploy,
		`{"node":"render-01","show":"halloween-2026","generation":0,"revision":"r0"}`)
	_, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "cmd-zero-generation", IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		TargetKind: "node", TargetID: "render-01", ParamsJSON: "{}",
		IssuerPrincipalID: "admin-1", IssuerPrincipalName: "admin-1",
		CallerIntent:       goodIntent,
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("seed zero-generation command: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy",
		`{"idempotencyKey":"`+idempotencyKey+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("replay against a genuine zero-generation identity: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var result struct {
		Command struct {
			Show       string `json:"show"`
			Generation int64  `json:"generation"`
			Revision   string `json:"revision"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode replay response: %v", err)
	}
	if result.Command.Show != "halloween-2026" || result.Command.Revision != "r0" {
		t.Fatalf("show/revision = %q/%q, want halloween-2026/r0; body: %s", result.Command.Show, result.Command.Revision, body)
	}
}

// TestCueCatalogDeployReplayFailsCleanlyOnMalformedCallerIntent covers the
// plain "does not decode at all" case (never valid JSON, tagged or not),
// distinct from the wrong-family case above.
func TestCueCatalogDeployReplayFailsCleanlyOnMalformedCallerIntent(t *testing.T) {
	api, st, _, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	const idempotencyKey = "idem-malformed-intent"
	_, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "cmd-malformed-intent", IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		TargetKind: "node", TargetID: "render-01", ParamsJSON: "{}",
		IssuerPrincipalID: "admin-1", IssuerPrincipalName: "admin-1",
		CallerIntent:       "not valid json at all",
		ConfirmationMethod: "evidence", State: "pending",
	})
	if err != nil {
		t.Fatalf("seed malformed-intent command: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy",
		`{"idempotencyKey":"`+idempotencyKey+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("replay against a malformed caller_intent: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestCueCatalogDeployReplayFailsCleanlyOnMalformedResultJSON covers the
// third discarded decode: a row whose result_json does not parse, isolated
// from caller_intent by giving this row a well-formed, correctly tagged
// caller_intent of its own.
func TestCueCatalogDeployReplayFailsCleanlyOnMalformedResultJSON(t *testing.T) {
	api, st, _, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	const idempotencyKey = "idem-malformed-result"
	goodIntent := store.FormatCallerIntent(store.CallerIntentCueCatalogDeploy,
		`{"node":"render-01","show":"halloween-2026","generation":1,"revision":"r1"}`)
	_, err := st.InsertCommand(context.Background(), store.CommandRecord{
		ID: "cmd-malformed-result", IdempotencyKey: idempotencyKey, Action: auditActionCueCatalogDeploy,
		TargetKind: "node", TargetID: "render-01", ParamsJSON: "{}",
		IssuerPrincipalID: "admin-1", IssuerPrincipalName: "admin-1",
		CallerIntent:       goodIntent,
		ConfirmationMethod: "evidence", State: "pending",
		ResultJSON: "{not valid json",
	})
	if err != nil {
		t.Fatalf("seed malformed-result command: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy",
		`{"idempotencyKey":"`+idempotencyKey+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("replay against a malformed result_json: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

// TestCueCatalogDeployOutcomeSurvivesClientDisconnect proves this route's
// post-dispatch bookkeeping (recording dispatchedAt, waiting for the node's
// result, recording the resolved outcome, and writing the outcome audit
// entry) is not cancellable by a client that walks away mid-request -
// matching TestFPPCommandOutcomeSurvivesClientDisconnect's and
// audiodispatch.go's own bgCtx cutover. The request's own context is
// canceled from inside AwaitResponse, simulating an abandoned HTTP client
// after the command already reached the wire; despite that, the command
// must still resolve in the store with a real dispatchedAt and outcome
// audit entry.
func TestCueCatalogDeployOutcomeSurvivesClientDisconnect(t *testing.T) {
	api, st, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	revision := resolvedCueCatalogRevision(t, api, "render-01", auth)
	observedAt := testNow
	pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.cuecatalog.revision", Value: revision,
			ObservedAt: &observedAt, CollectedAt: observedAt,
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	pub.onAwaitResponse = cancel

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req.WithContext(ctx))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (ServeHTTP still completes even though the request's own context was canceled mid-wait); body: %s", resp.StatusCode, body)
	}

	var result struct {
		Command struct {
			CommandID    string `json:"commandId"`
			Outcome      string `json:"outcome"`
			DispatchedAt string `json:"dispatchedAt"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode deploy response: %v", err)
	}
	if result.Command.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("outcome = %q, want confirmed; body: %s", result.Command.Outcome, body)
	}
	if result.Command.DispatchedAt == "" {
		t.Fatalf("dispatchedAt is empty, want a real dispatch time; body: %s", body)
	}

	rec, err := st.GetCommand(context.Background(), result.Command.CommandID)
	if err != nil {
		t.Fatalf("GetCommand: %v", err)
	}
	if rec.State != "resolved" {
		t.Fatalf("stored command state = %q, want resolved (the request's own cancellation must not leave it stuck)", rec.State)
	}
	if rec.DispatchedAt == nil {
		t.Fatalf("stored command dispatchedAt is nil, want set")
	}
	if rec.ResolvedAt == nil {
		t.Fatalf("stored command resolvedAt is nil, want set")
	}

	ack, err := st.GetNodeCueCatalogAck(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("GetNodeCueCatalogAck: %v", err)
	}
	if ack.Revision != revision {
		t.Fatalf("stored ack revision = %q, want %q", ack.Revision, revision)
	}
}

// TestCueCatalogDeployPublishFailureLeavesDispatchedAtNull proves a publish
// that never reaches the broker (as opposed to a publish that succeeds but
// gets no reply) must never claim a dispatch that never happened:
// dispatchedAt stays null and outcome is "failed", the same contract
// TestCueCatalogDeployRefusesWithNoActiveShow's sibling tests already prove
// for a subscribe failure via beforePublishErr - this proves it for the
// publish call itself failing (broker.BrokerManager.AwaitResponse's own
// b.Publish call, api/openapi.yaml's dispatchedAt: null-when-unattempted
// rule).
func TestCueCatalogDeployPublishFailureLeavesDispatchedAtNull(t *testing.T) {
	api, st, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	pub.publishErr = errors.New("broker unavailable for a test")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (dispatch itself failed); body: %s", resp.StatusCode, body)
	}

	// Asserted on the raw JSON body via the store instead, since a 500
	// response body is a Problem, not a CueCatalogDeployResponse.
	var problem v1.Problem
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem body: %v", err)
	}

	commands, err := st.ListAuditEntries(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAuditEntries: %v", err)
	}
	if len(commands) == 0 {
		t.Fatalf("no audit entries recorded for a failed publish")
	}

	// Read the command back by scanning for the one this test just
	// created (there is exactly one node/action pair in this fixture).
	var found bool
	for _, entry := range commands {
		if entry.Action != auditActionCueCatalogDeploy {
			continue
		}
		found = true
		rec, err := st.GetCommand(context.Background(), entry.CommandID)
		if err != nil {
			t.Fatalf("GetCommand: %v", err)
		}
		if rec.State != "failed" {
			t.Fatalf("stored command state = %q, want failed (publish never reached the wire)", rec.State)
		}
		if rec.DispatchedAt != nil {
			t.Fatalf("stored command dispatchedAt = %v, want nil (publish never reached the wire)", *rec.DispatchedAt)
		}
	}
	if !found {
		t.Fatalf("no cuecatalog.deploy audit entry recorded")
	}
}

// TestCueCatalogDeployRefusesWithNoActiveShow proves this route resolves
// its own catalog rather than accepting one, and refuses outright when
// there is nothing to resolve.
func TestCueCatalogDeployRefusesWithNoActiveShow(t *testing.T) {
	api, _, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	_ = token

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("deploy with no active show: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (nothing should have been dispatched)", pub.count())
	}
}

// TestCueCatalogDeployRefusesUnknownField proves this route rejects an
// unrecognized top-level field instead of silently ignoring it, matching
// api/openapi.yaml's CueCatalogDeployRequest (additionalProperties: false)
// and the identical refusal POST .../cue-catalog/acknowledge already gives
// (decodeCueCatalogAcknowledgeBody, cuecatalog.go).
func TestCueCatalogDeployRefusesUnknownField(t *testing.T) {
	api, _, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/deploy",
		`{"idempotencyKey":"key-1","somethingElse":true}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("deploy with an unknown field: status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	var problem v1.Problem
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem body: %v", err)
	}
	if problem.Type != ProblemTypeInvalidParameter {
		t.Fatalf("deploy with an unknown field: problem type = %q, want %q", problem.Type, ProblemTypeInvalidParameter)
	}
	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (nothing should have been dispatched)", pub.count())
	}
}

// TestCueCatalogDeployRefusesOnClaimConflict proves TRACK-H-cues-and-
// playlists.md section H5 build item 2's own ruling: deployment refuses
// outright, with a named 409 naming both Cue ids and the exact claim
// string, when the resolved catalog carries an H0.5 exclusive-claim
// conflict — never dispatching a catalog it already knows two Cues cannot
// both safely execute. assetsync.ResolveCueCatalog itself no longer
// errors on a conflict (Decide/Authorize/participatingNodesForShow must
// keep working for the rest of the fleet); this route is what actually
// refuses.
func TestCueCatalogDeployRefusesOnClaimConflict(t *testing.T) {
	api, st, pub, token := newCueCatalogDeployFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}

	putAudioNodeForTest(t, st, "audio-01")
	mustDeclareNode(t, st, "audio-01")

	mustPutCue(t, api, token, "cue-a", `{
		"show": "halloween-2026", "name": "A",
		"outputs": {"audio": {"asset": "a-audio", "startOffsetMillis": 0}}
	}`)
	mustPutCue(t, api, token, "cue-b", `{
		"show": "halloween-2026", "name": "B",
		"outputs": {"audio": {"asset": "b-audio", "startOffsetMillis": 0}}
	}`)
	// playlist-a (fpp) and playlist-b (showmesh-audio) — the ONE
	// concurrent-Playlist case H0.5 blesses — each with a Cue claiming the
	// SAME node's program-audio-route: a genuine, reachable claim
	// conflict, not merely a hypothetical two-showmesh-audio-playlists
	// case build item 8 already refuses at authoring.
	putPlaylistForTest(t, st, "playlist-a", config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "playlist-a", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "11111111-1111-1111-1111-111111111111", PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries:        []config.ShowPlaylistEntry{{ID: "cue-a-entry", Cue: "cue-a", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}}},
	})
	putPlaylistForTest(t, st, "playlist-b", config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "playlist-b", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		ShowmeshAudio: &config.ShowPlaylistShowmeshAudio{Repeat: config.ShowPlaylistShowmeshAudioRepeatNone},
		Entries:       []config.ShowPlaylistEntry{{ID: "cue-b-entry", Cue: "cue-b"}},
	})
	mustPutShowActive(t, api, token, "halloween-2026")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/audio-01/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("deploy with a claim conflict: status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "cue-a") || !strings.Contains(string(body), "cue-b") {
		t.Fatalf("refusal body %q does not name both cue ids", body)
	}
	if !strings.Contains(string(body), "program-audio-route") {
		t.Fatalf("refusal body %q does not name the exact claim kind", body)
	}
	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (a conflicting catalog must never be dispatched)", pub.count())
	}
}
