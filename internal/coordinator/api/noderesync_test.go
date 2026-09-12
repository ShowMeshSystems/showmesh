package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is the resync route's own test suite (noderesync.go):
// POST /nodes/{nodeId}/assets/resync. The dispatch mechanism itself
// (assetsync.Service's tick/maybeDispatch/dispatchFetch) already has its
// own test suite one package over; these tests exercise only this route's
// own contract: it accepts and returns promptly, names no outcome, and the
// evidence a caller later reads (GET /nodes/{nodeId}/assets) comes from the
// node's own report, never from anything this route claims at accept time.

func TestOpenAPINodeResyncDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{"ResyncNodeAssetsResponse", "ResyncNodeAssetsResult"} {
		compileSchema(t, c, name)
	}
}

type v1ResyncNodeAssetsResponseForTest struct {
	ServerTime string `json:"serverTime"`
	Resync     struct {
		Node                      string `json:"node"`
		AcceptedAt                string `json:"acceptedAt"`
		InventoryRequestCommandID string `json:"inventoryRequestCommandId"`
	} `json:"resync"`
}

// spyInventoryRequester records every RequestNodeInventory call this
// route makes, so a test can prove the issuer it stamps is the
// authenticated caller, never a package constant - InventoryRequester's
// own doc comment (noderesync.go).
type spyInventoryRequester struct {
	calls []inventoryRequestCall
	err   error
}

type inventoryRequestCall struct {
	nodeID string
	issuer mqttproto.CmdIssuer
}

func (s *spyInventoryRequester) RequestNodeInventory(_ context.Context, nodeID string, issuer mqttproto.CmdIssuer) (string, error) {
	s.calls = append(s.calls, inventoryRequestCall{nodeID: nodeID, issuer: issuer})
	if s.err != nil {
		return "", s.err
	}
	return "cmd-" + nodeID, nil
}

func TestPostResyncNodeAssetsUndeclaredNodeIs404(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/no-such-node/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestPostResyncNodeAssetsInvalidNodeIDIs400(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/Not_A_Valid_ID/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}

// TestPostResyncNodeAssetsDisabledSyncRefused pins the "asset sync disabled"
// error path: assetManifestAdminAPI never sets a ContentBaseURL, so
// dispatching an asset.fetch would be accepted but never actually deliver
// anything - this route refuses before accepting rather than promising a
// re-sync it cannot perform.
func TestPostResyncNodeAssetsDisabledSyncRefused(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if !containsAll(problem.Detail, "contentBaseUrl") {
		t.Fatalf("problem detail = %q, want it to name the disabled asset sync setting", problem.Detail)
	}
}

// TestPostResyncNodeAssetsAcceptedThenEvidence is this route's own
// acceptance criterion 1: the response is a 202 naming only acceptance (no
// outcome field exists on the wire at all - see ResyncNodeAssetsResult),
// it requests exactly the named node through the existing AssetSyncNudger
// hook (never a second delivery path, and never every declared node), and
// the node's readiness verdict immediately after acceptance is computed
// independently by GET /nodes/{nodeId}/assets from the node's own
// (still-missing) evidence - not_ready, exactly as it was before the POST.
// Only once a fresh report dated AFTER acceptance arrives does the SAME
// route's evidence flip to ready, proving the outcome came from that
// observation, never from the fact a command was dispatched.
func TestPostResyncNodeAssetsAcceptedThenEvidence(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	auth := map[string]string{"Authorization": "Bearer " + token}

	spy := &spyAssetSyncNudger{}
	invReq := &spyInventoryRequester{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSettings.(*fakeAssetSettingsSource).contentBaseURL = "https://coordinator.example"
	deps.AssetSyncNudger = spy
	deps.InventoryRequester = invReq
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	mustPutShowActive(t, api, token, "halloween-2026")
	asset := uploadOneAsset(t, api, auth, "render-01", "opening", "Thriller.fseq", []byte("content"))

	// render-01 reports in, holding nothing - the expected asset is missing,
	// so GET .../assets already reads not_ready before this route is ever
	// called.
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", nil,
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed empty report: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/resync", "", auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "ResyncNodeAssetsResponse", body)
	var decoded v1ResyncNodeAssetsResponseForTest
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, body)
	}
	if decoded.Resync.Node != "render-01" {
		t.Errorf("resync.node = %q, want %q", decoded.Resync.Node, "render-01")
	}
	if decoded.Resync.AcceptedAt == "" {
		t.Error("resync.acceptedAt is empty")
	}
	if !containsAll(string(body), `"acceptedAt"`) || containsAll(string(body), `"outcome"`) {
		t.Errorf("body must carry acceptance only, never an outcome field; body: %s", body)
	}
	if len(spy.requestedNode) != 0 {
		t.Fatalf("requestedNode = %v, want none: the route must no longer call RequestNode directly against a possibly-stale report, only RecordResyncIntent", spy.requestedNode)
	}
	if len(spy.recordedIntent) != 1 || spy.recordedIntent[0].nodeID != "render-01" || !spy.recordedIntent[0].issuedAt.Equal(testNow) {
		t.Fatalf("recordedIntent = %+v, want exactly one intent for render-01 issued at %v", spy.recordedIntent, testNow)
	}
	if len(invReq.calls) != 1 || invReq.calls[0].nodeID != "render-01" {
		t.Fatalf("inventory request calls = %+v, want exactly one for render-01", invReq.calls)
	}
	if invReq.calls[0].issuer.PrincipalID != admin.ID || invReq.calls[0].issuer.PrincipalName != admin.Name {
		t.Fatalf("inventory request issuer = %+v, want the authenticated caller (%s/%s), never a package constant",
			invReq.calls[0].issuer, admin.ID, admin.Name)
	}
	if decoded.Resync.InventoryRequestCommandID != "cmd-render-01" {
		t.Fatalf("resync.inventoryRequestCommandId = %q, want %q", decoded.Resync.InventoryRequestCommandID, "cmd-render-01")
	}

	// The returned id must name a real commands row - the acceptance
	// criterion this file's own header comment now closes: a caller-
	// visible commandID that names nothing is the same silent no-op as
	// never recording anything at all.
	cmdRec, err := st.GetCommand(context.Background(), decoded.Resync.InventoryRequestCommandID)
	if err != nil {
		t.Fatalf("GetCommand(%q): %v, want the dispatched command to exist", decoded.Resync.InventoryRequestCommandID, err)
	}
	if cmdRec.Action != "asset.inventory.request" || cmdRec.TargetKind != "node" || cmdRec.TargetID != "render-01" {
		t.Fatalf("command = %+v, want action asset.inventory.request against node render-01", cmdRec)
	}
	if cmdRec.IssuerPrincipalID != admin.ID || cmdRec.IssuerPrincipalName != admin.Name {
		t.Fatalf("command issuer = %s/%s, want the authenticated caller %s/%s", cmdRec.IssuerPrincipalID, cmdRec.IssuerPrincipalName, admin.ID, admin.Name)
	}
	if cmdRec.State != "dispatched" || cmdRec.DispatchedAt == nil {
		t.Fatalf("command state = %q, dispatchedAt = %v, want state dispatched with a dispatch time", cmdRec.State, cmdRec.DispatchedAt)
	}

	// The acceptance claims nothing: the manifest, read right after, still
	// reflects the node's own (unchanged) evidence.
	_, manifestAfterAccept, manifestBody := getNodeAssetManifest(t, api, auth, "render-01")
	if manifestAfterAccept.Manifest.State != "not_ready" {
		t.Fatalf("state right after acceptance = %q, want %q (accepting a resync must never itself claim an outcome); body: %s",
			manifestAfterAccept.Manifest.State, "not_ready", manifestBody)
	}

	// Only a report dated AFTER acceptance, now actually holding the
	// expected content hash, is evidence of anything - the same
	// FetchConfirmed rule assetsync/sync.go already enforces.
	confirmedAt := testNow.Add(time.Minute)
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{NodeID: "render-01", ContentHash: asset.ContentHash, RuntimeFilename: asset.RuntimeFilename, SizeBytes: asset.SizeBytes, VerifiedAt: confirmedAt}},
		store.NodeAssetReportRecord{ReportedAt: confirmedAt, Complete: true}); err != nil {
		t.Fatalf("seed post-dispatch report: %v", err)
	}

	_, manifestAfterEvidence, manifestBody2 := getNodeAssetManifest(t, api, auth, "render-01")
	if manifestAfterEvidence.Manifest.State != "ready" {
		t.Fatalf("state after fresh confirming evidence = %q, want %q; body: %s",
			manifestAfterEvidence.Manifest.State, "ready", manifestBody2)
	}
}

// TestPostResyncNodeAssetsFailedPublishIs503 proves a failed publish is a
// failure to the caller (owner ruling): a 202 for a request nothing was
// ever asked to act on is the same accepted-looking no-op this route's own
// acceptance criteria forbid, moved one step earlier. The intent is still
// recorded - that node's own next ordinary report still carries evidence
// fresh enough to trigger the repair, just later than a delivered push
// would have - and the commands row this route always writes is still
// recorded, resolved "failed", never left dangling just because the
// caller now also sees a failure.
func TestPostResyncNodeAssetsFailedPublishIs503(t *testing.T) {
	spy := &spyAssetSyncNudger{}
	invReq := &spyInventoryRequester{err: errors.New("simulated publish failure")}
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSettings.(*fakeAssetSettingsSource).contentBaseURL = "https://coordinator.example"
	deps.AssetSyncNudger = spy
	deps.InventoryRequester = invReq
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/resync", "",
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 when the inventory push fails; body: %s", resp.StatusCode, body)
	}
	var problem struct {
		Type   string `json:"type"`
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v\nbody: %s", err, body)
	}
	if problem.Type != ProblemTypeAssetResyncPublishFailed {
		t.Errorf("problem type = %q, want %q", problem.Type, ProblemTypeAssetResyncPublishFailed)
	}
	if !containsAll(problem.Detail, "render-01") || !containsAll(problem.Detail, "simulated publish failure") {
		t.Errorf("problem detail = %q, want it to name the node and the publish error", problem.Detail)
	}
	if len(spy.recordedIntent) != 1 || spy.recordedIntent[0].nodeID != "render-01" {
		t.Fatalf("recordedIntent = %+v, want the intent recorded even though the push failed", spy.recordedIntent)
	}

	// A publish failure must not be a silent no-op one step earlier than
	// the manifest-driven repair: a commands row exists, unconfirmed, with
	// an outcome_reason naming exactly why - the vocabulary this system
	// already uses for this ("collection_failed": nothing reached the
	// wire), never a null that renders as blank.
	commands, err := st.ListUnresolvedCommands(context.Background())
	if err != nil {
		t.Fatalf("ListUnresolvedCommands: %v", err)
	}
	var found *store.CommandRecord
	for i := range commands {
		if commands[i].Action == "asset.inventory.request" && commands[i].TargetID == "render-01" {
			found = &commands[i]
		}
	}
	if found != nil {
		t.Fatalf("command = %+v is still unresolved, want the publish failure to resolve it immediately", *found)
	}
	// A failed command resolves immediately, so it will not show up as
	// "unresolved" - list every command this store holds instead.
	all, err := st.ListCommands(context.Background(), store.MaxCommandPageSize)
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	found = nil
	for i := range all {
		if all[i].Action == "asset.inventory.request" && all[i].TargetID == "render-01" {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("no asset.inventory.request command recorded for render-01 among %+v, want the failed attempt recorded", all)
	}
	if found.State != "failed" {
		t.Fatalf("command state = %q, want %q", found.State, "failed")
	}
	if found.OutcomeState != string(observation.StateCollectionFailed) {
		t.Fatalf("command outcomeState = %q, want %q", found.OutcomeState, observation.StateCollectionFailed)
	}
	if found.OutcomeReason != "simulated publish failure" {
		t.Fatalf("command outcomeReason = %q, want it to name the publish error", found.OutcomeReason)
	}
	if found.IssuerPrincipalID != admin.ID {
		t.Fatalf("command issuer = %q, want the authenticated caller %q", found.IssuerPrincipalID, admin.ID)
	}
	if !containsAll(problem.Detail, found.ID) {
		t.Errorf("problem detail = %q, want it to name the failed command's id %q", problem.Detail, found.ID)
	}
}

// TestPostResyncNodeAssetsRequestsOnlyTheNamedNode proves two operators
// resyncing two different nodes are distinguishable: each POST must name
// only its own node, never leak into the other's request.
func TestPostResyncNodeAssetsRequestsOnlyTheNamedNode(t *testing.T) {
	spy := &spyAssetSyncNudger{}
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.AssetSettings.(*fakeAssetSettingsSource).contentBaseURL = "https://coordinator.example"
	deps.AssetSyncNudger = spy
	deps.InventoryRequester = &spyInventoryRequester{}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustDeclareNode(t, st, "render-02")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-02/assets/resync", "",
		map[string]string{"Authorization": "Bearer " + token})
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", resp.StatusCode, body)
	}
	if len(spy.recordedIntent) != 1 || spy.recordedIntent[0].nodeID != "render-02" {
		t.Fatalf("recordedIntent = %+v, want exactly one intent for render-02", spy.recordedIntent)
	}
}
