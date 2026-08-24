package api

import (
	"context"
	"net/http"
	"strconv"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the fresh reviewer's build item 5 own regression coverage:
// POST /nodes/{nodeId}/cue-catalog/acknowledge used to persist and audit
// req.Show and req.Generation verbatim, with no check against this
// coordinator's own resolved active show — a caller could store (and have
// audited) a show and generation that were never true. Reuses
// assetManifestAdminAPI (assetmanifest_test.go), which wires a declared
// node "render-01" and a Show "halloween-2026" but no show.active yet, so
// each test below controls exactly whether/what active show is configured.

// TestCueCatalogAcknowledgeRefusesShowMismatch proves an acknowledgement
// naming a show other than the resolved active show is refused outright
// and never reaches the store.
func TestCueCatalogAcknowledgeRefusesShowMismatch(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/acknowledge",
		`{"revision":"whatever","show":"not-the-active-show","generation":1}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("acknowledge with a mismatched show: status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	assertNoCueCatalogAckStored(t, st, "render-01")
}

// TestCueCatalogAcknowledgeRefusesGenerationMismatch is the identical proof
// for a generation that does not match the active show's resolved
// generation, holding show correct.
func TestCueCatalogAcknowledgeRefusesGenerationMismatch(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/acknowledge",
		`{"revision":"whatever","show":"halloween-2026","generation":999999}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("acknowledge with a mismatched generation: status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	assertNoCueCatalogAckStored(t, st, "render-01")
}

// TestCueCatalogAcknowledgeRefusesWithNoActiveShow proves an
// acknowledgement is refused (never stored) when no show.active is
// configured at all — there is no active show for req.Show/req.Generation
// to match, so any acknowledgement is a mismatch by construction.
func TestCueCatalogAcknowledgeRefusesWithNoActiveShow(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/acknowledge",
		`{"revision":"whatever","show":"halloween-2026","generation":1}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("acknowledge with no active show configured: status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	assertNoCueCatalogAckStored(t, st, "render-01")
}

// TestCueCatalogAcknowledgeAcceptsMatchingShowAndGeneration proves the
// happy path still works once show and generation both match the resolved
// active show — the mismatch check above must not be so strict it refuses
// a genuinely correct acknowledgement.
func TestCueCatalogAcknowledgeAcceptsMatchingShowAndGeneration(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	rec, err := st.GetConfigObject(context.Background(), config.ShowActiveConfigKind, config.ShowActiveObjectID)
	if err != nil {
		t.Fatalf("read show.active config object: %v", err)
	}
	generation := rec.CurrentRevision

	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/render-01/cue-catalog/acknowledge",
		`{"revision":"whatever","show":"halloween-2026","generation":`+strconv.FormatInt(generation, 10)+`}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("acknowledge with matching show/generation: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	ack, err := st.GetNodeCueCatalogAck(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("GetNodeCueCatalogAck: %v", err)
	}
	if ack.Revision != "whatever" || ack.ShowID != "halloween-2026" || ack.Generation != generation {
		t.Fatalf("stored ack = %+v, want revision=whatever show=halloween-2026 generation=%d", ack, generation)
	}
}

// assertNoCueCatalogAckStored fails t unless nodeID has never acknowledged
// a cue catalog, proving a refused acknowledgement never reached
// PutNodeCueCatalogAck.
func assertNoCueCatalogAckStored(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	if _, err := st.GetNodeCueCatalogAck(context.Background(), nodeID); err == nil {
		t.Fatalf("a refused acknowledgement was nonetheless stored for node %q", nodeID)
	} else if err != store.ErrNodeCueCatalogAckNotFound {
		t.Fatalf("GetNodeCueCatalogAck(%q): %v, want ErrNodeCueCatalogAckNotFound", nodeID, err)
	}
}
