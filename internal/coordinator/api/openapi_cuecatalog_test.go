package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track H seam H3's own conformance coverage, following
// openapi_showcueplaylist_test.go's exact pattern one seam over: every
// schema this seam added is validated against a REAL response from a real
// coordinator wiring, never hand-built JSON.

// TestOpenAPICueCatalogDocumentIsWellFormed extends
// TestOpenAPIDocumentIsWellFormed's own compile-sanity check with every
// schema this seam added.
func TestOpenAPICueCatalogDocumentIsWellFormed(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"CueCatalogRenderOutput", "CueCatalogAudioOutput", "CueCatalogLTCOutput",
		"CueCatalogAnnouncementOutput", "CueCatalogOutputs", "CueCatalogEntry", "CueCatalogResponse",
		"CueCatalogAcknowledgeRequest", "CueCatalogAcknowledgeResponse",
		"CueCatalogDeployRequest", "CueCatalogDeployResponse", "CueCatalogDeployResult",
	} {
		compileSchema(t, c, name)
	}
}

const cueCatalogValidCueBody = `{
	"show": "halloween-2026",
	"name": "Thriller",
	"outputs": {
		"render": {"sequence": "thriller"},
		"audio": {"asset": "thriller-audience", "startOffsetMillis": 0}
	}
}`

const cueCatalogDraftCueBody = `{
	"show": "halloween-2026",
	"name": "Unused Draft",
	"outputs": {
		"render": {"sequence": "unused-draft"}
	}
}`

// setUpCueCatalogFixture wires an admin API with two declared nodes
// (render-01, render-02, from assetManifestAdminAPI), a show.surface
// assigning render-01 (and only render-01) to the show, a referenced
// show.cue, an unreferenced (draft) show.cue, and a show.playlist that
// references only the first — so a catalog test can assert both the
// render-node-scoping rule and the draft-exclusion rule in one fixture.
func setUpCueCatalogFixture(t *testing.T) (*API, string) {
	t.Helper()
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]

	putSurfaceReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage", validSurfaceBodyNDI, auth)
	putSurfaceResp, putSurfaceBody := doRawRequest(t, api.Handler, putSurfaceReq)
	if putSurfaceResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface: status = %d, want 200; body: %s", putSurfaceResp.StatusCode, putSurfaceBody)
	}

	// render-01 also gets an audio.node object, so this fixture can assert
	// the audio/ltc/announcement scoping rule (gated on audio.node
	// presence) the same way it already asserts the render/surface one.
	// Written directly to the store rather than through PUT
	// /api/v1/config/audio.node, since that route additionally requires
	// advertised placement evidence (audionode_test.go's own concern, not
	// this fixture's).
	mustPutAudioNodeDirect(t, st, "render-01")

	putCueReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/thriller", cueCatalogValidCueBody, auth)
	putCueResp, putCueBody := doRawRequest(t, api.Handler, putCueReq)
	if putCueResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue/thriller: status = %d, want 200; body: %s", putCueResp.StatusCode, putCueBody)
	}

	putDraftReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/unused-draft", cueCatalogDraftCueBody, auth)
	putDraftResp, putDraftBody := doRawRequest(t, api.Handler, putDraftReq)
	if putDraftResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue/unused-draft: status = %d, want 200; body: %s", putDraftResp.StatusCode, putDraftBody)
	}

	playlistBody := `{
		"show": "halloween-2026",
		"name": "Main show",
		"runner": "fpp",
		"mismatchPolicy": "hold",
		"fpp": {
			"instanceUuid": "11111111-1111-1111-1111-111111111111",
			"playlistName": "Halloween Main",
			"playlistHash": "` + playlistHash64 + `"
		},
		"entries": [
			{"id": "e1", "cue": "thriller", "fpp": {"section": "mainPlaylist", "position": 0}}
		]
	}`
	putPlaylistReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/main", playlistBody, auth)
	putPlaylistResp, putPlaylistBody := doRawRequest(t, api.Handler, putPlaylistReq)
	if putPlaylistResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist: status = %d, want 200; body: %s", putPlaylistResp.StatusCode, putPlaylistBody)
	}

	return api, token
}

// mustPutAudioNodeDirect writes a minimal, valid audio.node object whose id
// is nodeID directly to st, bypassing the PUT /api/v1/config/audio.node
// route's advertised-placement-evidence check (audionode_test.go's own
// concern) — this fixture only needs the object's PRESENCE, which is the
// fact [assetsync.ResolveCueCatalog] gates audio/ltc/announcement output
// inclusion on.
func mustPutAudioNodeDirect(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{
		ProgramRoute: "hw:0,0", LTCRoute: "hw:0,0",
		ProgramChannels: []int{1, 2}, LTCChannel: 3,
		ClockDomain:           "single-interface",
		ClockDomainProvenance: "one physical interface, both routes on it",
	})
	if err != nil {
		t.Fatalf("encode audio.node payload: %v", err)
	}
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.AudioNodeConfigKind, ObjectID: nodeID, Revision: 1, PayloadJSON: raw, Source: "api",
	}); err != nil {
		t.Fatalf("create audio.node config revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.AudioNodeConfigKind, nodeID, 1); err != nil {
		t.Fatalf("activate audio.node config revision: %v", err)
	}
}

// --- local wire-decoding shapes ---

type cueCatalogRenderOutputForTest struct {
	Sequence    string   `json:"sequence"`
	AssetHashes []string `json:"assetHashes"`
}
type cueCatalogAudioOutputForTest struct {
	Asset             string   `json:"asset"`
	StartOffsetMillis int      `json:"startOffsetMillis"`
	AssetHashes       []string `json:"assetHashes"`
}
type cueCatalogOutputsForTest struct {
	Render *cueCatalogRenderOutputForTest `json:"render"`
	Audio  *cueCatalogAudioOutputForTest  `json:"audio"`
}
type cueCatalogEntryForTest struct {
	CueID       string                   `json:"cueId"`
	CueRevision int64                    `json:"cueRevision"`
	Outputs     cueCatalogOutputsForTest `json:"outputs"`
}
type cueCatalogResponseForTest struct {
	Node       string                   `json:"node"`
	Configured bool                     `json:"configured"`
	Show       string                   `json:"show"`
	Generation *int64                   `json:"generation"`
	Revision   string                   `json:"revision"`
	Entries    []cueCatalogEntryForTest `json:"entries"`
}
type cueCatalogAcknowledgeResponseForTest struct {
	Node                 string `json:"node"`
	Configured           bool   `json:"configured"`
	Status               string `json:"status"`
	AcknowledgedRevision string `json:"acknowledgedRevision"`
	CurrentRevision      string `json:"currentRevision"`
}

// TestOpenAPICueCatalogResponsesMatchRealResponses proves every response
// TRACK-H-H3-SPEC.md section 4 requires against a real coordinator wiring,
// and exercises the seam's own closed decisions: an unconfigured
// show.active authorizes nothing, a draft Cue never appears, render is
// scoped to a node holding a surface, and acknowledge reports
// catalog-current/catalog-stale correctly.
func TestOpenAPICueCatalogResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	api, token := setUpCueCatalogFixture(t)
	auth := map[string]string{"Authorization": "Bearer " + token}

	// Before show.active is ever written: configured must be false, and
	// this is a real grant of nothing, not a fabricated generation 0.
	_, unconfiguredBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01/cue-catalog", auth)
	assertMatchesSchema(t, c, "CueCatalogResponse", unconfiguredBody)
	var unconfigured cueCatalogResponseForTest
	if err := json.Unmarshal(unconfiguredBody, &unconfigured); err != nil {
		t.Fatalf("decode unconfigured cue-catalog response: %v", err)
	}
	if unconfigured.Configured {
		t.Fatalf("GET cue-catalog before show.active is configured: Configured = true, want false")
	}
	if unconfigured.Generation != nil {
		t.Fatalf("GET cue-catalog before show.active is configured: Generation = %v, want nil (no fabricated grant)", *unconfigured.Generation)
	}
	if len(unconfigured.Entries) != 0 {
		t.Fatalf("GET cue-catalog before show.active is configured: Entries = %v, want empty", unconfigured.Entries)
	}

	mustPutShowActive(t, api, token, "halloween-2026")

	// render-01 holds the surface: render output is present, and the
	// draft Cue never appears.
	_, node1Body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01/cue-catalog", auth)
	assertMatchesSchema(t, c, "CueCatalogResponse", node1Body)
	var node1 cueCatalogResponseForTest
	if err := json.Unmarshal(node1Body, &node1); err != nil {
		t.Fatalf("decode render-01 cue-catalog response: %v", err)
	}
	if !node1.Configured {
		t.Fatalf("GET cue-catalog for render-01: Configured = false, want true")
	}
	if node1.Generation == nil || *node1.Generation == 0 {
		t.Fatalf("GET cue-catalog for render-01: Generation = %v, want a positive generation", node1.Generation)
	}
	if len(node1.Entries) != 1 {
		t.Fatalf("GET cue-catalog for render-01: Entries = %v, want exactly the referenced Cue (draft excluded)", node1.Entries)
	}
	entry := node1.Entries[0]
	if entry.CueID != "thriller" {
		t.Fatalf("GET cue-catalog for render-01: entry CueID = %q, want %q", entry.CueID, "thriller")
	}
	if entry.Outputs.Render == nil || entry.Outputs.Render.Sequence != "thriller" {
		t.Fatalf("GET cue-catalog for render-01: render output = %+v, want sequence=thriller (render-01 holds a surface)", entry.Outputs.Render)
	}
	if entry.Outputs.Audio == nil || entry.Outputs.Audio.Asset != "thriller-audience" {
		t.Fatalf("GET cue-catalog for render-01: audio output = %+v, want asset=thriller-audience", entry.Outputs.Audio)
	}

	// render-02 holds no surface in this show: render output must be
	// absent even though the Cue itself is otherwise identical.
	_, node2Body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-02/cue-catalog", auth)
	assertMatchesSchema(t, c, "CueCatalogResponse", node2Body)
	var node2 cueCatalogResponseForTest
	if err := json.Unmarshal(node2Body, &node2); err != nil {
		t.Fatalf("decode render-02 cue-catalog response: %v", err)
	}
	if len(node2.Entries) != 1 || node2.Entries[0].Outputs.Render != nil {
		t.Fatalf("GET cue-catalog for render-02 (no surface): entries = %+v, want render output absent", node2.Entries)
	}
	// render-02 also has no audio.node object: audio output must be absent
	// too, even though the Cue declares it.
	if node2.Entries[0].Outputs.Audio != nil {
		t.Fatalf("GET cue-catalog for render-02 (no audio.node object): audio output = %+v, want absent", node2.Entries[0].Outputs.Audio)
	}

	// Catalog revision stability: resolving twice with nothing changed
	// produces the identical revision.
	_, node1BodyAgain := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01/cue-catalog", auth)
	var node1Again cueCatalogResponseForTest
	if err := json.Unmarshal(node1BodyAgain, &node1Again); err != nil {
		t.Fatalf("decode second render-01 cue-catalog response: %v", err)
	}
	if node1Again.Revision != node1.Revision {
		t.Fatalf("GET cue-catalog for render-01 twice: revision changed from %q to %q with nothing modified", node1.Revision, node1Again.Revision)
	}

	// 404 for an undeclared node.
	undeclaredResp, undeclaredBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/no-such-node/cue-catalog", auth)
	if undeclaredResp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET cue-catalog for undeclared node: status = %d, want 404; body: %s", undeclaredResp.StatusCode, undeclaredBody)
	}
	assertMatchesSchema(t, c, "Problem", undeclaredBody)

	// Acknowledge with the resolved revision: catalog-current.
	ackCurrentReq := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/acknowledge",
		`{"revision":"`+node1.Revision+`","show":"halloween-2026","generation":`+strconv.FormatInt(*node1.Generation, 10)+`}`, auth)
	ackCurrentResp, ackCurrentBody := doRawRequest(t, api.Handler, ackCurrentReq)
	if ackCurrentResp.StatusCode != http.StatusOK {
		t.Fatalf("POST cue-catalog/acknowledge (current): status = %d, want 200; body: %s", ackCurrentResp.StatusCode, ackCurrentBody)
	}
	assertMatchesSchema(t, c, "CueCatalogAcknowledgeResponse", ackCurrentBody)
	var ackCurrent cueCatalogAcknowledgeResponseForTest
	if err := json.Unmarshal(ackCurrentBody, &ackCurrent); err != nil {
		t.Fatalf("decode acknowledge (current) response: %v", err)
	}
	if ackCurrent.Status != "catalog-current" {
		t.Fatalf("POST cue-catalog/acknowledge with the current revision: status = %q, want catalog-current", ackCurrent.Status)
	}
	if ackCurrent.CurrentRevision != node1.Revision {
		t.Fatalf("acknowledge (current) response CurrentRevision = %q, want %q", ackCurrent.CurrentRevision, node1.Revision)
	}

	// Acknowledge with a stale revision: catalog-stale, naming both.
	ackStaleReq := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/acknowledge",
		`{"revision":"stale-revision-value","show":"halloween-2026","generation":`+strconv.FormatInt(*node1.Generation, 10)+`}`, auth)
	ackStaleResp, ackStaleBody := doRawRequest(t, api.Handler, ackStaleReq)
	if ackStaleResp.StatusCode != http.StatusOK {
		t.Fatalf("POST cue-catalog/acknowledge (stale): status = %d, want 200; body: %s", ackStaleResp.StatusCode, ackStaleBody)
	}
	assertMatchesSchema(t, c, "CueCatalogAcknowledgeResponse", ackStaleBody)
	var ackStale cueCatalogAcknowledgeResponseForTest
	if err := json.Unmarshal(ackStaleBody, &ackStale); err != nil {
		t.Fatalf("decode acknowledge (stale) response: %v", err)
	}
	if ackStale.Status != "catalog-stale" {
		t.Fatalf("POST cue-catalog/acknowledge with a stale revision: status = %q, want catalog-stale", ackStale.Status)
	}
	if ackStale.AcknowledgedRevision != "stale-revision-value" || ackStale.CurrentRevision != node1.Revision {
		t.Fatalf("acknowledge (stale) response = %+v, want acknowledgedRevision=stale-revision-value currentRevision=%q", ackStale, node1.Revision)
	}

	// A validation-error sample on the acknowledge route, to prove the
	// Problem shape one more time on this seam's own refusal path.
	badAckReq := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/acknowledge", `{"revision":"","show":"halloween-2026","generation":1}`, auth)
	badAckResp, badAckBody := doRawRequest(t, api.Handler, badAckReq)
	if badAckResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("POST cue-catalog/acknowledge with empty revision: status = %d, want 400; body: %s", badAckResp.StatusCode, badAckBody)
	}
	assertMatchesSchema(t, c, "Problem", badAckBody)
}

// TestOpenAPICueCatalogAcknowledgeRequestBodyReferencesDocumentedSchema
// resolves the document pointer assertMatchesSchema never reads:
// paths./nodes/{nodeId}/cue-catalog/acknowledge.post.requestBody.content.application/json.schema.$ref.
func TestOpenAPICueCatalogAcknowledgeRequestBodyReferencesDocumentedSchema(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/nodes/{nodeId}/cue-catalog/acknowledge"); got != "CueCatalogAcknowledgeRequest" {
		t.Errorf("POST /nodes/{nodeId}/cue-catalog/acknowledge requestBody schema = %q, want CueCatalogAcknowledgeRequest", got)
	}
}

// TestOpenAPICueCatalogDeployRequestBodyReferencesDocumentedSchema is
// TestOpenAPICueCatalogAcknowledgeRequestBodyReferencesDocumentedSchema's
// own sibling for the deploy route this build item adds.
func TestOpenAPICueCatalogDeployRequestBodyReferencesDocumentedSchema(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/nodes/{nodeId}/cue-catalog/deploy"); got != "CueCatalogDeployRequest" {
		t.Errorf("POST /nodes/{nodeId}/cue-catalog/deploy requestBody schema = %q, want CueCatalogDeployRequest", got)
	}
}
