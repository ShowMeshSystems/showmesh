package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
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

// TestCueCatalogDeployReplayReturnsExistingOutcomeWithoutRepublishing
// proves an idempotency key reused against the same node replays the
// original command's own recorded result rather than dispatching a
// second time.
func TestCueCatalogDeployReplayReturnsExistingOutcomeWithoutRepublishing(t *testing.T) {
	api, _, pub, token := newCueCatalogDeployFixture(t)
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
			Replay  bool   `json:"replay"`
			Outcome string `json:"outcome"`
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
		RequestedRevision:  `{"node":"render-01","show":"halloween-2026","generation":1}`,
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
