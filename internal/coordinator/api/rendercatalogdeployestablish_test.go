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
// establishRenderAssignments' own safety guard: a surface the node already
// holds a CURRENT render assignment for REAL content (a real, cue-
// activated one, or a manually applied one — evidenced here by
// surface.content.fseq_filename, exactly as internal/agent/renderreport.go's
// applyContentIdentity reports one) is never touched by a redeploy —
// re-dispatching render.surface.apply with no sequence would blow that
// assignment away and replace it with an idle test pattern, which a
// catalog redeployed mid-show (adding a Cue, not recovering from a
// reboot) must never do.
func TestCueCatalogDeploySkipsEstablishingAnAlreadyAssignedSurface(t *testing.T) {
	api, st, deployPub, renderPub, obs, token := establishTestFixture(t)
	renderPutSurface(t, st, "wall-1", "halloween-2026", "render-01")
	renderPutActiveShow(t, st, "halloween-2026")

	obs.setObs([]observation.Observation{
		surfacePipelineStateObs("render-01", "wall-1", "running", testNow, testNow),
		surfaceContentFSEQFilenameObs("render-01", "wall-1", "halloween-2026/wall-1.fseq", testNow, testNow),
	})

	deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)

	if got := renderPub.count(); got != 0 {
		t.Fatalf("render.surface.apply publish count = %d, want 0 — an already-assigned surface holding real content must never be re-established", got)
	}
}

// TestCueCatalogDeployReestablishesIdlePlaceholderToRefreshStaleAuthTuple
// is defect 3's own regression: a surface establishRenderAssignment
// already established once (no sequence, an idle placeholder — reported
// "running" but with NO surface.content.fseq_filename evidence, since no
// cue has ever activated onto it) must still be re-established on a LATER
// redeploy under a NEW catalog generation, so its persisted H3
// authorization tuple (generation, catalogRevision) is refreshed to match
// what the node now holds. Skipping it forever — the old "assigned means
// skip, unconditionally" guard — pins that tuple to the FIRST generation
// that ever established it: stale at the very next generation bump, and
// therefore guaranteed to fail decideBootResume's own tuple comparison
// (internal/agent/bootresume.go) at the node's next reboot regardless of
// how current its held catalog actually is.
func TestCueCatalogDeployReestablishesIdlePlaceholderToRefreshStaleAuthTuple(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
	renderCommandPollInterval = 5 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	api, st, deployPub, renderPub, obs, token := establishTestFixture(t)
	renderPutSurface(t, st, "wall-1", "halloween-2026", "render-01")
	renderPutActiveShow(t, st, "halloween-2026")

	// An idle placeholder: "running" evidence, but no real content — the
	// exact shape establishRenderAssignment's own earlier no-sequence
	// apply leaves behind.
	obs.setObs([]observation.Observation{surfacePipelineStateObs("render-01", "wall-1", "running", testNow, testNow)})

	firstRevision := deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)
	if got := renderPub.count(); got != 1 {
		t.Fatalf("after the first deploy, render.surface.apply publish count = %d, want 1 (the initial establishment)", got)
	}

	// A repeat deploy of the SAME (unchanged) catalog must NOT re-dispatch
	// — nothing to refresh, and re-dispatching would restart the pipeline
	// for no reason.
	secondRevision := deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)
	if secondRevision != firstRevision {
		t.Fatalf("redeployed the same unchanged catalog but got a different revision (%q vs %q); this test needs an unchanged catalog to prove the no-op case", secondRevision, firstRevision)
	}
	if got := renderPub.count(); got != 1 {
		t.Fatalf("after redeploying an UNCHANGED catalog, render.surface.apply publish count = %d, want still 1 (no redundant dispatch)", got)
	}

	// Bump the active show's own generation (show.active's config revision
	// — assetsync.ActiveShow.Generation) so the next deploy resolves a
	// genuinely new catalog generation/revision — this must now
	// re-establish wall-1 to refresh its stale tuple.
	renderPutActiveShow(t, st, "halloween-2026")

	thirdRevision := deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)
	if thirdRevision == firstRevision {
		t.Fatalf("expected the bumped show.surface generation to produce a new catalog revision, got the same one (%q) — test setup did not actually bump anything", thirdRevision)
	}
	if got := renderPub.count(); got != 2 {
		t.Fatalf("after the generation bump, render.surface.apply publish count = %d, want 2 — the stale idle placeholder must be re-established", got)
	}
	env := renderPub.payload[1]
	if got := env.Payload.Params["generation"]; got != float64(2) {
		t.Fatalf("re-established generation = %v, want 2 (the new catalog's own identity, re-stamped)", got)
	}
	if got := env.Payload.Params["catalogRevision"]; got != thirdRevision {
		t.Fatalf("re-established catalogRevision = %v, want the new catalog's own revision %q", got, thirdRevision)
	}
}

// TestCueCatalogDeployReestablishesAfterAssignmentIsLost is defect 1 and 2's
// own regression, run together exactly as the reviewer's own sequence
// does: a node that ALREADY holds a confirmed establishment reboots (its
// pipeline-state evidence flips to FRESH StateFailed evidence, exactly as
// agent.go's boot-resume loop reports a discarded or unresumeable
// assignment via pipeline.Supervisor.MarkResumeFailed — never an absence),
// and the SAME unchanged catalog is redeployed immediately afterward, not
// 45 seconds later once that evidence would have aged into StateStale on
// its own. Before defect 1's fix, StateFailed's own freshness satisfied
// the old "is evidence current" guard and establishment was skipped as
// "already assigned," establishing nothing. Before defect 2's fix, even
// once the guard correctly said "not assigned," the establishment
// idempotency key — keyed on catalog content alone — collided with the
// FIRST, already-CONFIRMED establishment command for this exact (node,
// surface, show, generation, revision) tuple and silently replayed it
// instead of dispatching again.
//
// The reboot evidence deliberately ALSO carries a real
// surface.content.fseq_filename reading (MarkResumeFailed's "held a
// persisted assignment at boot but could not resume it" sub-case leaves
// the assignment file itself in place — only the DISCARDED-as-unauthorized
// sub-case removes it — so a real filename can genuinely still be on
// record even though the pipeline state is Failed). This is deliberate,
// not incidental: it is what stops defect 3's independent "no real
// content, safe to refresh" fallback from masking a regression of defect
// 1's own fix. If the guard ever reverts to a bare freshness check
// (StateFailed miscounted as "assigned"), THIS test still fails even
// though defect 3's fix alone would have re-established a plain idle
// placeholder anyway — see renderSurfaceHasRealContent's own doc comment:
// with real content evidence present, establishRenderAssignment only ever
// reaches the re-establish path via defect 1's OWN "not assigned" verdict,
// never via defect 3's idle-refresh fallback.
func TestCueCatalogDeployReestablishesAfterAssignmentIsLost(t *testing.T) {
	renderCommandConfirmDeadline = 50 * time.Millisecond
	renderCommandPollInterval = 5 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	api, st, deployPub, renderPub, obs, token := establishTestFixture(t)
	renderPutSurface(t, st, "wall-1", "halloween-2026", "render-01")
	renderPutActiveShow(t, st, "halloween-2026")

	firstRevision := deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)
	if got := renderPub.count(); got != 1 {
		t.Fatalf("after the first deploy, render.surface.apply publish count = %d, want 1 (the initial establishment)", got)
	}

	// The node reboots: its persisted assignment fails to resume (FSEQ
	// missing, content-hash mismatch, ...) and it reports FRESH StateFailed
	// evidence for wall-1 — never an absence — while the render report
	// STILL names the real FSEQ that assignment held (see this test's own
	// doc comment on why that combination is deliberate).
	rebootObservedAt := testNow.Add(time.Minute)
	obs.setObs([]observation.Observation{
		surfacePipelineStateObs("render-01", "wall-1", "failed", rebootObservedAt, rebootObservedAt),
		surfaceContentFSEQFilenameObs("render-01", "wall-1", "halloween-2026/wall-1.fseq", rebootObservedAt, rebootObservedAt),
	})

	// Redeploy the SAME unchanged catalog IMMEDIATELY — not after the
	// evidence has aged into StateStale on its own, which is the only
	// thing that let the old skip-on-freshness guard ever recover.
	secondRevision := deployConfirmedCatalog(t, api, st, deployPub, "render-01", token)
	if secondRevision != firstRevision {
		t.Fatalf("redeployed the same unchanged catalog but got a different revision (%q vs %q); this test needs an unchanged catalog to prove the loss-recovery case", secondRevision, firstRevision)
	}
	if got := renderPub.count(); got != 2 {
		t.Fatalf("after the reboot and immediate redeploy, render.surface.apply publish count = %d, want 2 — the node holds no working assignment and must be re-established", got)
	}
	env := renderPub.payload[1]
	if got := env.Payload.Params["surfaceId"]; got != "wall-1" {
		t.Fatalf("re-established surfaceId = %v, want wall-1", got)
	}
	if got := env.Payload.Params["catalogRevision"]; got != secondRevision {
		t.Fatalf("re-established catalogRevision = %v, want the redeployed catalog's own revision %q", got, secondRevision)
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
