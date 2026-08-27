package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is rendersuperseded.go's own test suite: ADR-043's H0.7
// requires a render a node holds across a Show switch to be "reported as
// superseded with the Show and generation that authorized it... never
// reported as current or healthy." rendersuperseded.go is the ONE place
// that verdict is derived; these tests prove it compares against
// assetsync's real resolver (never a second, hand-rolled copy of
// TRACK-H-H3-SPEC.md's revision rule) and leaves every non-matching case
// untouched.

func mustSupersedeTestObs(t *testing.T, surfaceID string, sig observation.SignalID, value any, observedAt time.Time) observation.Observation {
	t.Helper()
	o, err := observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceSurface, ID: surfaceID}, sig, value, observedAt,
		observation.WithSource("node-render:render-01"), observation.WithValidFor(noderenderDefaultValidForTest),
	)
	if err != nil {
		t.Fatalf("observation.Measured(%s): %v", sig, err)
	}
	return o
}

// noderenderDefaultValidForTest mirrors noderender.DefaultValidFor without
// importing it for this narrow purpose — plenty of headroom against testNow
// so none of these fixtures reads as stale by construction.
const noderenderDefaultValidForTest = 45 * time.Second

func findSupersedeTestObs(t *testing.T, obs []observation.Observation, sig observation.SignalID) observation.Observation {
	t.Helper()
	for _, o := range obs {
		if o.Signal == sig {
			return o
		}
	}
	t.Fatalf("no observation for signal %q among %d", sig, len(obs))
	return observation.Observation{}
}

// TestApplySupersededVerdictNilStoreLeavesObservationsUnchanged proves
// [Dependencies.AssetManifests]'s documented nil-safe posture holds here
// too: with no cue-catalog data source wired in, the verdict is never
// applied and a surface renders exactly as the node reported it.
func TestApplySupersededVerdictNilStoreLeavesObservationsUnchanged(t *testing.T) {
	obs := []observation.Observation{
		mustSupersedeTestObs(t, "garage", observation.SignalID(renderSignalPipelineState), mqttproto.RenderPipelineStateRunning, testNow),
	}
	out := applySupersededVerdict(context.Background(), nil, "render-01", obs, testNow)
	got := findSupersedeTestObs(t, out, observation.SignalID(renderSignalPipelineState))
	if got.Value != mqttproto.RenderPipelineStateRunning {
		t.Errorf("Value = %v, want %q (nil store must never derive a verdict)", got.Value, mqttproto.RenderPipelineStateRunning)
	}
}

// TestApplySupersededVerdictNoActiveShowLeavesObservationsUnchanged proves
// a coordinator with a wired store but no show.active configured at all
// makes no supersede claim: there is no authority to compare against.
func TestApplySupersededVerdictNoActiveShowLeavesObservationsUnchanged(t *testing.T) {
	_, st, _ := assetManifestAdminAPI(t) // declares render-01 and registers a show, but never activates one

	obs := []observation.Observation{
		mustSupersedeTestObs(t, "garage", observation.SignalID(renderSignalPipelineState), mqttproto.RenderPipelineStateRunning, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentCatalogRevisionForSupersede, "some-revision", testNow),
	}
	out := applySupersededVerdict(context.Background(), st, "render-01", obs, testNow)
	got := findSupersedeTestObs(t, out, observation.SignalID(renderSignalPipelineState))
	if got.Value != mqttproto.RenderPipelineStateRunning {
		t.Errorf("Value = %v, want %q (no active show — nothing to compare against)", got.Value, mqttproto.RenderPipelineStateRunning)
	}
}

// TestApplySupersededVerdictNonRunningStateUntouched proves the overlay
// only ever touches a surface currently reporting "running" — a surface
// already reporting a real fault (failed) states that fault, and must not
// be overwritten by a superseded verdict that would hide it.
func TestApplySupersededVerdictNonRunningStateUntouched(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	obs := []observation.Observation{
		mustSupersedeTestObs(t, "garage", observation.SignalID(renderSignalPipelineState), mqttproto.RenderPipelineStateFailed, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentCatalogRevisionForSupersede, "a-revision-that-will-never-match", testNow),
	}
	out := applySupersededVerdict(context.Background(), st, "render-01", obs, testNow)
	got := findSupersedeTestObs(t, out, observation.SignalID(renderSignalPipelineState))
	if got.Value != mqttproto.RenderPipelineStateFailed {
		t.Errorf("Value = %v, want %q (a real fault must never be overwritten)", got.Value, mqttproto.RenderPipelineStateFailed)
	}
}

// TestApplySupersededVerdictMatchingRevisionStaysRunning proves the
// negative case for the acceptance criteria's second point: a surface
// reporting the CURRENTLY active show's own resolved catalog revision
// (the shape a redeploy-and-activate leaves behind) is never called
// superseded.
func TestApplySupersededVerdictMatchingRevisionStaysRunning(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	ctx := context.Background()
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !active.Configured {
		t.Fatalf("ResolveActiveShow: %v, configured=%v", err, active.Configured)
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}

	obs := []observation.Observation{
		mustSupersedeTestObs(t, "garage", observation.SignalID(renderSignalPipelineState), mqttproto.RenderPipelineStateRunning, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentCatalogRevisionForSupersede, catalog.Revision, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentShowForSupersede, active.ShowID, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentGenerationForSupersede, active.Generation, testNow),
	}
	out := applySupersededVerdict(ctx, st, "render-01", obs, testNow)
	got := findSupersedeTestObs(t, out, observation.SignalID(renderSignalPipelineState))
	if got.Value != mqttproto.RenderPipelineStateRunning {
		t.Errorf("Value = %v, want %q (this render's catalog revision matches the active resolution)", got.Value, mqttproto.RenderPipelineStateRunning)
	}
}

// TestApplySupersededVerdictOverridesOnRevisionMismatch is this issue's own
// central regression test: a surface holding a render authorized under a
// Show/generation/catalog revision that is no longer the active resolution
// (a show switch, exactly ADR-043 H0.7's case) reports superseded, naming
// the Show and generation that authorized what it holds, never plain
// "running".
func TestApplySupersededVerdictOverridesOnRevisionMismatch(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	ctx := context.Background()
	heldActive, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !heldActive.Configured {
		t.Fatalf("ResolveActiveShow (halloween-2026): %v, configured=%v", err, heldActive.Configured)
	}
	heldCatalog, err := assetsync.ResolveCueCatalog(ctx, st, heldActive, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (halloween-2026): %v", err)
	}

	// The operator switches shows during setup — H0.7's own scenario.
	mustPutShow(t, api, token, "lane14-other", `{"name":"Lane 14 Other","notes":""}`)
	mustPutShowActive(t, api, token, "lane14-other")

	nowActive, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !nowActive.Configured || nowActive.ShowID != "lane14-other" {
		t.Fatalf("ResolveActiveShow (lane14-other): %v, %+v", err, nowActive)
	}

	// obs states exactly what the node itself would report: still holding
	// the render it applied under halloween-2026, honestly "running".
	obs := []observation.Observation{
		mustSupersedeTestObs(t, "garage", observation.SignalID(renderSignalPipelineState), mqttproto.RenderPipelineStateRunning, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentCatalogRevisionForSupersede, heldCatalog.Revision, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentShowForSupersede, heldActive.ShowID, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentGenerationForSupersede, heldActive.Generation, testNow),
	}
	out := applySupersededVerdict(ctx, st, "render-01", obs, testNow)
	got := findSupersedeTestObs(t, out, observation.SignalID(renderSignalPipelineState))
	if got.Value != mqttproto.RenderPipelineStateSuperseded {
		t.Fatalf("Value = %v, want %q", got.Value, mqttproto.RenderPipelineStateSuperseded)
	}
	if !strings.Contains(got.Reason, "halloween-2026") {
		t.Errorf("Reason = %q, want it to name the held show %q", got.Reason, "halloween-2026")
	}
	if !strings.Contains(got.Reason, "lane14-other") {
		t.Errorf("Reason = %q, want it to name the currently active show %q", got.Reason, "lane14-other")
	}
	// Every other field on the observation is untouched: this is a
	// read-time relabeling, not a fresh evidence sample.
	if got.ObservedAt == nil || !got.ObservedAt.Equal(testNow) {
		t.Errorf("ObservedAt = %v, want unchanged at %v", got.ObservedAt, testNow)
	}
	if got.Source != "node-render:render-01" {
		t.Errorf("Source = %q, want unchanged", got.Source)
	}
	if got.Quality != observation.QualityDerived {
		t.Errorf("Quality = %q, want %q (this is a coordinator-derived verdict, not raw node telemetry)", got.Quality, observation.QualityDerived)
	}
}

// TestApplySupersededVerdictAuthoringSecondCueSameShowStaysRunning is a
// false-positive regression test: an operator authoring a SECOND Cue in
// the SAME still-active Show, at the SAME generation (show.active itself
// was never re-issued), must never
// make an already-running surface report superseded. Before this fix, the
// verdict was decided from the catalog REVISION, which hashes every Cue in
// the Show (pkg/cuecatalog.RevisionInput) and so changes the instant
// anyone authors anything — even a Cue that authorizes nothing new for
// this surface. This test proves the catalog revision changes (confirming
// it is a real trigger for the old bug) while the verdict still reads
// running, because ADR-043's H0.7 tuple — Show and generation — is what
// decides it now.
func TestApplySupersededVerdictAuthoringSecondCueSameShowStaysRunning(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	ctx := context.Background()

	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !active.Configured {
		t.Fatalf("ResolveActiveShow: %v, configured=%v", err, active.Configured)
	}
	beforeCatalog, err := assetsync.ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (before authoring): %v", err)
	}

	obs := []observation.Observation{
		mustSupersedeTestObs(t, "garage", observation.SignalID(renderSignalPipelineState), mqttproto.RenderPipelineStateRunning, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentCatalogRevisionForSupersede, beforeCatalog.Revision, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentShowForSupersede, active.ShowID, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentGenerationForSupersede, active.Generation, testNow),
	}

	// The operator authors a second Cue during setup — a directly
	// activatable announcement Cue, so it lands in the resolved catalog
	// (and its revision hash) with no show.playlist reference required.
	// show.active is never touched, so this Show's generation is unchanged.
	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.cue/second-cue", `{
		"show": "halloween-2026",
		"name": "Second Cue",
		"outputs": {
			"audio": {"asset": "second-cue-audio"},
			"announcement": {"policy": "mix", "fadeMillis": 0}
		}
	}`, auth)
	if resp, body := doRawRequest(t, api.Handler, putReq); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.cue/second-cue: status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	afterCatalog, err := assetsync.ResolveCueCatalog(ctx, st, active, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (after authoring): %v", err)
	}
	if afterCatalog.Revision == beforeCatalog.Revision {
		t.Fatalf("authoring a second cue left the catalog revision %q unchanged; this test needs it to change to reproduce the false-fire this issue fixes", beforeCatalog.Revision)
	}
	stillActive, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !stillActive.Configured || stillActive.ShowID != active.ShowID || stillActive.Generation != active.Generation {
		t.Fatalf("ResolveActiveShow (after authoring): %v, %+v, want unchanged %+v", err, stillActive, active)
	}

	out := applySupersededVerdict(ctx, st, "render-01", obs, testNow)
	got := findSupersedeTestObs(t, out, observation.SignalID(renderSignalPipelineState))
	if got.Value != mqttproto.RenderPipelineStateRunning {
		t.Fatalf("Value = %v, want %q (same show, same generation — a mere authoring-time catalog revision change must never supersede a current render)", got.Value, mqttproto.RenderPipelineStateRunning)
	}
}

// TestApplySupersededVerdictOverridesOnCueMismatchWithNoCatalogRevision
// proves the second, independent comparison path: a legacy assignment that
// carries a Cue but no catalog revision at all (one persisted before
// TRACK-H-H3-SPEC.md section 5 existed) is still caught, by checking its
// Cue against the resolved catalog's own entries, when the revision check
// alone has nothing to compare.
func TestApplySupersededVerdictOverridesOnCueMismatchWithNoCatalogRevision(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")
	ctx := context.Background()

	// No show.cue objects exist for halloween-2026 at all, so the resolved
	// catalog's Entries is empty — any held Cue id is, by construction, not
	// among them.
	obs := []observation.Observation{
		mustSupersedeTestObs(t, "garage", observation.SignalID(renderSignalPipelineState), mqttproto.RenderPipelineStateRunning, testNow),
		mustSupersedeTestObs(t, "garage", signalSurfaceContentCueIDForSupersede, "cue-from-a-retired-catalog", testNow),
	}
	out := applySupersededVerdict(ctx, st, "render-01", obs, testNow)
	got := findSupersedeTestObs(t, out, observation.SignalID(renderSignalPipelineState))
	if got.Value != mqttproto.RenderPipelineStateSuperseded {
		t.Fatalf("Value = %v, want %q (held cue is absent from the resolved catalog)", got.Value, mqttproto.RenderPipelineStateSuperseded)
	}
}

// TestNodeRenderObservationsReachRealAPISuperseded is the full HTTP-level
// proof, mirroring openapi_noderender_test.go's TestNodeRenderObservationsReachRealAPI
// exactly but through a real *store.Store carrying show.active/show writes,
// so GET /api/v1/nodes/{nodeId} itself — never a bare unit call — is what
// this test asserts against. It exercises both halves of the acceptance
// criteria: a render held across a show switch reports superseded, naming
// the show and generation that authorized it, and a fresh assignment
// carrying the new show's own resolved catalog revision clears it.
func TestNodeRenderObservationsReachRealAPISuperseded(t *testing.T) {
	api, st, auth := assetManifestAdminAPI(t)
	token := auth["Authorization"][len("Bearer "):]
	mustPutShowActive(t, api, token, "halloween-2026")

	ctx := context.Background()
	activeHalloween, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !activeHalloween.Configured {
		t.Fatalf("ResolveActiveShow (halloween-2026): %v, configured=%v", err, activeHalloween.Configured)
	}
	catalogHalloween, err := assetsync.ResolveCueCatalog(ctx, st, activeHalloween, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (halloween-2026): %v", err)
	}

	renderStore := noderender.NewStore()
	receivedAt := testNow.Add(-time.Second)
	renderStore.Put("render-01", mqttproto.RenderPayload{
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{
			{
				SurfaceID:           "garage",
				PipelineState:       mqttproto.RenderPipelineStateRunning,
				Since:               receivedAt,
				RestartCount:        0,
				ConsecutiveFailures: 0,
				Transport:           "ndi",
				ObservedAt:          receivedAt,
				FSEQFilename:        "halloween-01.fseq",
				FSEQContentHash:     "sha256:deadbeef",
				CatalogRevision:     catalogHalloween.Revision,
				Show:                activeHalloween.ShowID,
				Generation:          activeHalloween.Generation,
				ContentObservedAt:   receivedAt,
			},
		},
	}, false, receivedAt)

	nv := inventory.NodeView{NodeID: "render-01", FirstSeenAt: receivedAt, UpdatedAt: receivedAt, Liveness: inventory.LivenessOnline}
	deps := Dependencies{
		Nodes:          &fakeNodeLister{views: []inventory.NodeView{nv}},
		Observations:   storeObservationLister{st: st},
		Render:         renderStore,
		AssetManifests: st,
	}
	liveAPI := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	// --- while halloween-2026 is still active, the render reports running ---
	_, body := doRequest(t, liveAPI.Handler, "GET", "/api/v1/nodes/render-01", nil)
	if !strings.Contains(string(body), `"surface.pipeline.state","value":"running"`) {
		t.Fatalf("GET /nodes/render-01 (halloween-2026 active) body does not report running: %s", body)
	}

	// --- the operator switches the active show (ADR-043 H0.7's case) ---
	mustPutShow(t, api, token, "lane14-other", `{"name":"Lane 14 Other","notes":""}`)
	mustPutShowActive(t, api, token, "lane14-other")

	resp, superseded := doRequest(t, liveAPI.Handler, "GET", "/api/v1/nodes/render-01", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /nodes/render-01 (after switch): status = %d, want 200; body: %s", resp.StatusCode, superseded)
	}
	sBody := string(superseded)
	if !strings.Contains(sBody, `"surface.pipeline.state","value":"superseded"`) {
		t.Fatalf("GET /nodes/render-01 (after show switch) does not report superseded: %s", sBody)
	}
	if !strings.Contains(sBody, `"surface.content.show","value":"halloween-2026"`) {
		t.Errorf("GET /nodes/render-01 (after show switch) does not name the held show halloween-2026: %s", sBody)
	}
	if !strings.Contains(sBody, `"surface.content.generation","value":1`) {
		t.Errorf("GET /nodes/render-01 (after show switch) does not name the held generation 1: %s", sBody)
	}

	// --- redeploying the new show's catalog and activating one of its
	// cues clears the state: simulated here at the same Assignment/Auth
	// persistence layer a real cue activation uses
	// (internal/agent/cueactivationrender.go and renderops.go both feed
	// pipeline.Assignment.Auth through the identical mechanism
	// [applyContentIdentity] reads onto the wire — see that function's own
	// doc comment), since driving an actual FPP-triggered cue activation
	// end to end is docs/bench/track-h-chain's own scope, not this API
	// package's.
	activeLane14, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !activeLane14.Configured || activeLane14.ShowID != "lane14-other" {
		t.Fatalf("ResolveActiveShow (lane14-other): %v, %+v", err, activeLane14)
	}
	catalogLane14, err := assetsync.ResolveCueCatalog(ctx, st, activeLane14, "render-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog (lane14-other): %v", err)
	}
	redeployedAt := receivedAt.Add(time.Second)
	renderStore.Put("render-01", mqttproto.RenderPayload{
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{
			{
				SurfaceID:           "garage",
				PipelineState:       mqttproto.RenderPipelineStateRunning,
				Since:               redeployedAt,
				RestartCount:        0,
				ConsecutiveFailures: 0,
				Transport:           "ndi",
				ObservedAt:          redeployedAt,
				FSEQFilename:        "lane14-01.fseq",
				FSEQContentHash:     "sha256:cafef00d",
				CatalogRevision:     catalogLane14.Revision,
				Show:                activeLane14.ShowID,
				Generation:          activeLane14.Generation,
				ContentObservedAt:   redeployedAt,
			},
		},
	}, false, redeployedAt)

	_, cleared := doRequest(t, liveAPI.Handler, "GET", "/api/v1/nodes/render-01", nil)
	cBody := string(cleared)
	if !strings.Contains(cBody, `"surface.pipeline.state","value":"running"`) {
		t.Errorf("GET /nodes/render-01 (after redeploy+reassign under lane14-other) does not clear back to running: %s", cBody)
	}
	if strings.Contains(cBody, `"surface.pipeline.state","value":"superseded"`) {
		t.Errorf("GET /nodes/render-01 (after redeploy+reassign) still reports superseded: %s", cBody)
	}
}
