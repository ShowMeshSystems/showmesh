package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

func putSurfaceBody(t *testing.T, api *API, token, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage", body, map[string]string{"Authorization": "Bearer " + token})
	resp, raw := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface status = %d, want 200; body: %s", resp.StatusCode, raw)
	}
}

func surfaceEditFixture(t *testing.T) (*API, *store.Store, *fakeRenderPublisher, *liveRenderLister, string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	token := mustIssueToken(t, svc, mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin).ID)
	renderPub := &fakeRenderPublisher{}
	live := &liveRenderLister{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.RenderPublisher = renderPub
	deps.Observations = &dynamicObservationLister{}
	deps.Render = live
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	renderPutActiveShow(t, st, "halloween-2026")
	return api, st, renderPub, live, token
}

func renderApplyPublishes(pub *fakeRenderPublisher) []mqttCmdEnvelopeForTest {
	pub.mu.Lock()
	defer pub.mu.Unlock()
	var out []mqttCmdEnvelopeForTest
	for _, env := range pub.payload {
		if env.Payload.Action == "render.surface.apply" {
			out = append(out, env)
		}
	}
	return out
}

func TestPutShowSurfaceGeometryChangeReappliesRunningSurface(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
	renderCommandPollInterval = 5 * time.Millisecond
	api, st, renderPub, live, token := surfaceEditFixture(t)
	defer func() {
		api.h.surfaceReapplyWG.Wait()
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()
	hash := strings.Repeat("c", 64)
	renderCreateAsset(t, st, "halloween-2026", "thriller", store.AssetTargetKindShow, "", hash, "thriller.fseq")

	putSurfaceBody(t, api, token, validSurfaceBodyNDI)
	live.obs = ([]observation.Observation{
		surfacePipelineStateObs("render-01", "garage", "running", testNow, testNow),
		surfaceContentFSEQFilenameObs("render-01", "garage", "thriller.fseq", testNow, testNow),
	})

	edited := strings.Replace(validSurfaceBodyNDI, `"frameRate": 40`, `"frameRate": 20`, 1)
	putSurfaceBody(t, api, token, edited)
	waitForPublishCount(t, renderPub, "render.surface.apply", 1)

	applies := renderApplyPublishes(renderPub)
	if len(applies) != 1 {
		t.Fatalf("render.surface.apply publishes = %d, want 1", len(applies))
	}
	params := applies[0].Payload.Params
	if params["surfaceId"] != "garage" || params["frameRate"] != float64(20) {
		t.Fatalf("re-apply params surfaceId=%v frameRate=%v, want garage and 20", params["surfaceId"], params["frameRate"])
	}
	if params["fseqFilename"] != "thriller.fseq" || params["fseqContentHash"] != hash {
		t.Fatalf("re-apply content = %v/%v, want the running thriller.fseq and its hash", params["fseqFilename"], params["fseqContentHash"])
	}

	putSurfaceBody(t, api, token, strings.Replace(edited, `"Garage Door"`, `"Garage"`, 1))
	time.Sleep(100 * time.Millisecond)
	if got := len(renderApplyPublishes(renderPub)); got != 1 {
		t.Fatalf("render.surface.apply publishes after a rename = %d, want still 1", got)
	}
}

func TestPutShowSurfaceGeometryChangeSkipsUnassignedSurface(t *testing.T) {
	api, _, renderPub, _, token := surfaceEditFixture(t)
	t.Cleanup(api.h.surfaceReapplyWG.Wait)

	putSurfaceBody(t, api, token, validSurfaceBodyNDI)
	putSurfaceBody(t, api, token, strings.Replace(validSurfaceBodyNDI, `"frameRate": 40`, `"frameRate": 20`, 1))
	time.Sleep(100 * time.Millisecond)

	if got := len(renderApplyPublishes(renderPub)); got != 0 {
		t.Fatalf("render.surface.apply publishes = %d, want 0 for a surface the node is not running", got)
	}
}
