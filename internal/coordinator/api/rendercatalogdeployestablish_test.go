package api

import (
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is the coordinator-side regression coverage for the Track H
// render-assignment gap (see TRACK-H-cues-and-playlists.md): nothing ever
// created a node's persisted render assignment except an
// operator dispatching render.surface.apply by hand, and ADR-043's H0.7
// clears assignments at boot — together, a render node that reboots
// mid-show never renders again on its own. cuecatalogdeploy.go's
// establishRenderAssignments closes that gap by resolving every
// show.surface for the node/show a confirmed cuecatalog.deploy just
// covered and establishing each one with NO sequence selected. See
// test/integration/render_catalog_deploy_establish_test.go for this same
// claim proved against real coordinator/agent binaries.

// establishTestFixture wires BOTH publish paths a confirmed cuecatalog.
// deploy now drives: AudioPublisher (cuecatalog.deploy's own
// AwaitResponse round trip, matching cuecatalogdeploy_test.go's own
// newCueCatalogDeployFixture) and RenderPublisher/Observations (the
// render.surface.apply establishment dispatch this file is actually
// about, matching renderdispatch_test.go's own newRenderDispatchTestSetup).
func establishTestFixture(t *testing.T) (api *API, st *store.Store, deployPub *fakeAudioPublisher, renderPub *fakeRenderPublisher, obs *dynamicObservationLister, token string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token = mustIssueToken(t, svc, admin.ID)
	deployPub = &fakeAudioPublisher{}
	renderPub = &fakeRenderPublisher{}
	obs = &dynamicObservationLister{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AudioPublisher = deployPub
	deps.RenderPublisher = renderPub
	deps.Observations = obs
	api = New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	return api, st, deployPub, renderPub, obs, token
}

// deployConfirmedCatalog drives POST .../cue-catalog/deploy to a
// CONFIRMED outcome, matching cuecatalogdeploy_test.go's own dispatch
// setup — the establishment this file tests only ever runs on that
// branch (handlePostNodeCueCatalogDeploy's own res.Outcome ==
// mqttproto.OutcomeConfirmed && acknowledgedRevision != "" guard).
func deployConfirmedCatalog(t *testing.T, api *API, st *store.Store, deployPub *fakeAudioPublisher, nodeID, token string) (revision string) {
	t.Helper()
	auth := map[string]string{"Authorization": "Bearer " + token}
	revision = resolvedCueCatalogRevision(t, api, nodeID, auth)
	observedAt := testNow
	deployPub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.cuecatalog.revision", Value: revision,
			ObservedAt: &observedAt, CollectedAt: observedAt,
		},
	}
	req := newJSONRequest(t, http.MethodPost, "/api/v1/nodes/"+nodeID+"/cue-catalog/deploy", `{}`, auth)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST cue-catalog/deploy: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	return revision
}

// TestCueCatalogDeployEstablishesUnassignedSurfaceWithNoSequence is the
// core claim: a confirmed cuecatalog.deploy, with NO manual render apply
// ever dispatched, establishes the node's own show.surface with a
// render.surface.apply carrying no fseqFilename/fseqContentHash — the
// "declared, no content yet" shape build contract ruling 4 (renderops.go's
// renderApplyKnownKeys/buildFSEQAssignment) already tolerated — plus the
// H3 authorization tuple from the JUST-DEPLOYED catalog.
func TestCueCatalogDeployEstablishesUnassignedSurfaceWithNoSequence(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
	renderCommandPollInterval = 5 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	api, st, deployPub, renderPub, _, token := establishTestFixture(t)
	renderPutSurface(t, st, "wall-1", "halloween-2026", "render-01")
	renderPutActiveShow(t, st, "halloween-2026")

	revision := deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)

	if got := renderPub.count(); got != 1 {
		t.Fatalf("render.surface.apply publish count = %d, want exactly 1", got)
	}
	env := renderPub.payload[0]
	if env.Payload.Action != "render.surface.apply" {
		t.Fatalf("dispatched action = %q, want render.surface.apply", env.Payload.Action)
	}
	if got := env.Payload.Params["surfaceId"]; got != "wall-1" {
		t.Fatalf("dispatched surfaceId = %v, want wall-1", got)
	}
	if got := env.Payload.Params["show"]; got != "halloween-2026" {
		t.Fatalf("dispatched show = %v, want halloween-2026", got)
	}
	// generation/catalogRevision must be the JUST-DEPLOYED catalog's own
	// identity — never the caller's, and never left zero-valued — so a
	// later boot resumes exactly this assignment (decideBootResume,
	// internal/agent/bootresume.go).
	if got := env.Payload.Params["generation"]; got != float64(1) {
		t.Fatalf("dispatched generation = %v, want 1 (show.active's own first revision)", got)
	}
	if got := env.Payload.Params["catalogRevision"]; got != revision {
		t.Fatalf("dispatched catalogRevision = %v, want the deployed catalog's own revision %q", got, revision)
	}
	for _, key := range []string{"fseqFilename", "fseqContentHash"} {
		if _, ok := env.Payload.Params[key]; ok {
			t.Errorf("dispatched params has key %q, want it absent entirely — build contract ruling 4's own tolerant no-FSEQ shape requires an ABSENT key, not an empty one", key)
		}
	}
	for _, key := range []string{"channelRange", "geometry", "frameRate", "idleOutput", "generation", "catalogRevision"} {
		if _, ok := env.Payload.Params[key]; !ok {
			t.Errorf("dispatched params missing key %q — a later cue activation's own buildAssignedSpec needs it once a real fseqFilename is merged in", key)
		}
	}
}

// TestCueCatalogDeploySkipsEstablishingAnAlreadyAssignedSurface proves
// establishRenderAssignments' own safety guard: a surface the node
// already holds a CURRENT render assignment for (a real, cue-activated
// one, or a previously established one) is never touched by a redeploy —
// re-dispatching render.surface.apply with no sequence would blow that
// assignment away and replace it with an idle test pattern, which a
// catalog redeployed mid-show (adding a Cue, not recovering from a
// reboot) must never do.
func TestCueCatalogDeploySkipsEstablishingAnAlreadyAssignedSurface(t *testing.T) {
	api, st, deployPub, renderPub, obs, token := establishTestFixture(t)
	renderPutSurface(t, st, "wall-1", "halloween-2026", "render-01")
	renderPutActiveShow(t, st, "halloween-2026")

	obs.setObs([]observation.Observation{surfacePipelineStateObs("render-01", "wall-1", "running", testNow, testNow)})

	deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)

	if got := renderPub.count(); got != 0 {
		t.Fatalf("render.surface.apply publish count = %d, want 0 — an already-assigned surface must never be re-established", got)
	}
}

// TestCueCatalogDeployDoesNotEstablishSurfacesOnOtherNodesOrShows proves
// establishment is scoped exactly like the deployed catalog itself: only
// this node's own show.surface objects belonging to the active Show, never
// another node's, and never a surface left over from a Show that is no
// longer active.
func TestCueCatalogDeployDoesNotEstablishSurfacesOnOtherNodesOrShows(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
	renderCommandPollInterval = 5 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	api, st, deployPub, renderPub, _, token := establishTestFixture(t)
	mustDeclareNode(t, st, "render-02")
	renderPutShow(t, st, "spring-2027", "Spring 2027")
	renderPutSurface(t, st, "other-node-surface", "halloween-2026", "render-02")
	renderPutSurface(t, st, "other-show-surface", "spring-2027", "render-01")
	renderPutActiveShow(t, st, "halloween-2026")

	deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)

	if got := renderPub.count(); got != 0 {
		t.Fatalf("render.surface.apply publish count = %d, want 0 — neither surface belongs to (render-01, halloween-2026)", got)
	}
}
