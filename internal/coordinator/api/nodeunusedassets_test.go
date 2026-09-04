package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is this seam's own test suite: GET /nodes/{nodeId}/assets/unused
// (nodeunusedassets.go's own doc comment explains why this reuses
// assetsync's existing Extra field and ResolveCueCatalog resolution rather
// than a second computation) and POST /nodes/{nodeId}/assets/remove.

// TestOpenAPINodeUnusedAssetsDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this seam added, mirroring TestOpenAPICueCatalogDocumentIsWellFormed's
// identical pattern one seam over.
func TestOpenAPINodeUnusedAssetsDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"UnusedAsset", "NodeUnusedAssetsResponse", "RemoveNodeAssetRequest",
		"RemoveNodeAssetResponse", "RemoveNodeAssetResult",
	} {
		compileSchema(t, c, name)
	}
}

type v1UnusedAssetForTest struct {
	ContentHash string  `json:"contentHash"`
	Filename    string  `json:"filename"`
	SizeBytes   int64   `json:"sizeBytes"`
	Sequence    *string `json:"sequence"`
}

type v1NodeUnusedAssetsResponseForTest struct {
	ServerTime string                 `json:"serverTime"`
	Node       string                 `json:"node"`
	State      string                 `json:"state"`
	Reason     *string                `json:"reason"`
	ObservedAt *string                `json:"observedAt"`
	Unused     []v1UnusedAssetForTest `json:"unused"`
}

func getNodeUnusedAssets(t *testing.T, api *API, auth map[string]string, nodeID string) (*http.Response, v1NodeUnusedAssetsResponseForTest, []byte) {
	t.Helper()
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/"+nodeID+"/assets/unused", auth)
	var decoded v1NodeUnusedAssetsResponseForTest
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode unused assets response: %v\nbody: %s", err, body)
		}
	}
	return resp, decoded, body
}

// TestGetNodeUnusedAssetsListsWithSequence is acceptance criterion 1: an
// asset the node holds that the active show no longer expects (here, a
// superseded upload) is listed, named by the sequence it was uploaded
// under.
func TestGetNodeUnusedAssetsListsWithSequence(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	oldAsset := uploadOneAsset(t, api, auth, "render-01", "opening", "Opening.fseq", []byte("version one"))
	newAsset := uploadOneAsset(t, api, auth, "render-01", "opening", "Opening.fseq", []byte("version two, different bytes"))
	if oldAsset.ContentHash == newAsset.ContentHash {
		t.Fatalf("fixture bug: two uploads produced the same content hash")
	}

	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01", []store.NodeAssetInventoryRecord{
		{NodeID: "render-01", ContentHash: newAsset.ContentHash, RuntimeFilename: newAsset.RuntimeFilename, SizeBytes: newAsset.SizeBytes, VerifiedAt: testNow},
		{NodeID: "render-01", ContentHash: oldAsset.ContentHash, RuntimeFilename: oldAsset.RuntimeFilename, SizeBytes: oldAsset.SizeBytes, VerifiedAt: testNow},
	}, store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}

	resp, decoded, body := getNodeUnusedAssets(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "NodeUnusedAssetsResponse", body)
	if decoded.State != "ready" {
		t.Fatalf("state = %q, want %q (the CURRENT asset is held, so the node is ready); body: %s", decoded.State, "ready", body)
	}
	if len(decoded.Unused) != 1 {
		t.Fatalf("unused = %+v, want exactly one entry", decoded.Unused)
	}
	u := decoded.Unused[0]
	if u.ContentHash != oldAsset.ContentHash || u.Filename != oldAsset.RuntimeFilename {
		t.Fatalf("unused[0] = %+v, want the superseded upload (%q/%q)", u, oldAsset.ContentHash, oldAsset.RuntimeFilename)
	}
	if u.Sequence == nil || *u.Sequence != "opening" {
		t.Fatalf("unused[0].Sequence = %v, want %q", u.Sequence, "opening")
	}
}

// TestGetNodeUnusedAssetsUnknownWithholdsList pins this route's own
// absent-evidence rule (design note point 1): a node that has never
// reported its inventory is Unknown, and the unused list is withheld
// entirely, never rendered as an empty "nothing unused" - the same
// "unknown never renders as ready/empty" rule GET .../assets already
// enforces, reused rather than reinvented here.
func TestGetNodeUnusedAssetsUnknownWithholdsList(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	// render-01 is declared by the fixture but has never reported an
	// inventory (no ReplaceNodeAssetInventory call) - never_reported.

	resp, decoded, body := getNodeUnusedAssets(t, api, auth, "render-01")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if decoded.State != "unknown" {
		t.Fatalf("state = %q, want %q; body: %s", decoded.State, "unknown", body)
	}
	if decoded.Reason == nil || *decoded.Reason == "" {
		t.Fatalf("reason = %v, want a non-empty explanation on an unknown verdict; body: %s", decoded.Reason, body)
	}
	if len(decoded.Unused) != 0 {
		t.Fatalf("unused = %+v, want empty when State is unknown", decoded.Unused)
	}
	if decoded.ObservedAt != nil {
		t.Fatalf("observedAt = %v, want nil for an unknown verdict", *decoded.ObservedAt)
	}
}

// removeNodeAssetFixture builds an active show, a show.surface for
// render-01, one current render asset for sequence "thriller", a Cue
// declaring that render output, and an fpp-runner Playlist referencing the
// Cue - exactly what ResolveCueCatalog needs to include the Cue and attach
// the asset's content hash to its Render.AssetHashes, mirroring
// fallbackprograms_test.go's identical fixture shape one file over.
func removeNodeAssetFixture(t *testing.T) (api *API, st *store.Store, auth map[string]string, asset v1AssetForTest) {
	t.Helper()
	api, st, auth = assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]

	surfacePayload, err := config.EncodeShowSurfacePayload(config.ShowSurfacePayload{
		Show: "halloween-2026", Name: "garage", Node: "render-01",
		ChannelRange: config.ShowSurfaceChannelRange{StartChannel: 1, ChannelCount: 12},
		Geometry:     config.ShowSurfaceGeometry{Width: 2, Height: 2, PixelFormat: config.ShowSurfacePixelFormatRGB},
		FrameRate:    40,
		Output:       config.ShowSurfaceOutput{Transport: config.ShowSurfaceTransportNDI, NDI: &config.ShowSurfaceNDIOutput{SourceName: "test"}},
	})
	if err != nil {
		t.Fatalf("encode surface: %v", err)
	}
	putConfigForTest(t, st, config.ShowSurfaceConfigKind, "garage", surfacePayload)

	asset = uploadOneAsset(t, api, auth, "render-01", "thriller", "thriller.fseq", []byte("thriller bytes"))

	cuePayload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: "halloween-2026", Name: "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	if err != nil {
		t.Fatalf("encode cue: %v", err)
	}
	putConfigForTest(t, st, config.ShowCueConfigKind, "thriller", cuePayload)

	playlistPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{InstanceUUID: "M4-7840e12f81da4191c0d00fbb6a889314", PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	if err != nil {
		t.Fatalf("encode playlist: %v", err)
	}
	putConfigForTest(t, st, config.ShowPlaylistConfigKind, "main", playlistPayload)

	mustPutShowActive(t, api, token, "halloween-2026")

	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{NodeID: "render-01", ContentHash: asset.ContentHash, RuntimeFilename: asset.RuntimeFilename, SizeBytes: asset.SizeBytes, VerifiedAt: testNow}},
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}
	return api, st, auth, asset
}

// TestPostRemoveNodeAssetRefusesWhenCueReferencesAsset is acceptance
// criterion 3: removing an asset a Cue DOES use is refused, and the
// response names the Cue - the safety property the issue calls out as the
// one that matters ("a refusal that does not name the cue is not the
// outcome, because the operator cannot act on it").
func TestPostRemoveNodeAssetRefusesWhenCueReferencesAsset(t *testing.T) {
	api, _, auth, asset := removeNodeAssetFixture(t)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/remove",
		`{"contentHash":"`+asset.ContentHash+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
	var problem struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(body, &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if !containsAll(problem.Detail, `"thriller"`) {
		t.Fatalf("problem detail = %q, want it to name cue \"thriller\"", problem.Detail)
	}
}

// TestPostRemoveNodeAssetDispatchesAndConfirms is acceptance criterion 2's
// dispatch half: an asset no Cue references is removed by dispatching
// asset.remove to the node, and a confirmed node result is reported back
// as such. This proves DISPATCH and CONFIRMATION-FROM-THE-NODE'S-OWN-RESULT
// only - see the test's own final assertions for what it deliberately does
// NOT claim (the coordinator's inventory row is unchanged until the next
// report, matching RemoveNodeAssetResult's own doc comment).
func TestPostRemoveNodeAssetDispatchesAndConfirms(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	pub := &fakeAudioPublisher{result: mqttproto.ResultPayload{Outcome: mqttproto.OutcomeConfirmed}}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AudioPublisher = pub
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShowActive(t, api, token, "halloween-2026")

	leftover := uploadOneAsset(t, api, auth, "render-01", "orphan", "orphan.fseq", []byte("nobody references this"))
	// Supersede it so it is no longer expected (the same "genuinely unused"
	// shape TestGetNodeUnusedAssetsListsWithSequence builds), then seed the
	// node as still holding the now-stale bytes.
	uploadOneAsset(t, api, auth, "render-01", "orphan", "orphan.fseq", []byte("replacement bytes"))
	if err := st.ReplaceNodeAssetInventory(context.Background(), "render-01",
		[]store.NodeAssetInventoryRecord{{NodeID: "render-01", ContentHash: leftover.ContentHash, RuntimeFilename: leftover.RuntimeFilename, SizeBytes: leftover.SizeBytes, VerifiedAt: testNow}},
		store.NodeAssetReportRecord{ReportedAt: testNow, Complete: true}); err != nil {
		t.Fatalf("seed inventory: %v", err)
	}

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/remove",
		`{"contentHash":"`+leftover.ContentHash+`"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, newOpenAPICompiler(t), "RemoveNodeAssetResponse", body)
	var result struct {
		Command struct {
			Outcome     string `json:"outcome"`
			ContentHash string `json:"contentHash"`
			Node        string `json:"node"`
			CommandID   string `json:"commandId"`
		} `json:"command"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Command.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("outcome = %q, want confirmed; body: %s", result.Command.Outcome, body)
	}
	if result.Command.ContentHash != leftover.ContentHash || result.Command.Node != "render-01" || result.Command.CommandID == "" {
		t.Fatalf("result = %+v, body: %s", result.Command, body)
	}
	if pub.lastAction != "asset.remove" {
		t.Fatalf("dispatched action = %q, want asset.remove", pub.lastAction)
	}
	if pub.lastParams["contentHash"] != leftover.ContentHash || pub.lastParams["filename"] != leftover.RuntimeFilename {
		t.Fatalf("dispatched params = %+v, want contentHash/filename matching the leftover asset", pub.lastParams)
	}

	// What this response does NOT claim: the coordinator's own
	// node-asset-inventory row still lists the leftover hash as held - a
	// confirmed AGENT result is not the same evidence as the node's NEXT
	// inventory report, which this dispatch never waits for.
	inv, err := st.GetNodeAssetInventory(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	stillListed := false
	for _, item := range inv {
		if item.ContentHash == leftover.ContentHash {
			stillListed = true
		}
	}
	if !stillListed {
		t.Fatalf("inventory no longer lists the removed hash - this test's own premise (the response does not update inventory) is now false; re-check RemoveNodeAssetResult's doc comment against reality")
	}
}

// TestPostRemoveNodeAssetRefusesUnknownContentHash proves this route never
// dispatches a command for a hash it has no evidence the node holds.
func TestPostRemoveNodeAssetRefusesUnknownContentHash(t *testing.T) {
	api, _, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/assets/remove", `{"contentHash":"sha256:nowhere"}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
}
