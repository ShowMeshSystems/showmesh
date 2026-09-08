package api

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/currentrun"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Part 2's own coverage: AutoDeployCueCatalog deploys when
// nothing is currently running from the node's held catalog (acceptance
// criterion 1: "edit a cue between shows, then run readiness: the new
// catalog auto-deploys"), and holds, dispatching nothing at all and
// leaving the node's acknowledgement untouched, when something is
// (acceptance criterion 2: "edit a cue mid-cue while an activation from that catalog
// is running on that node: the deploy is HELD, the node stays on the
// catalog it is executing"). Reuses newCueCatalogDeployFixture's own
// pattern (cuecatalogdeploy_test.go), adding [Dependencies.CurrentRuns] so
// the hold decision is controlled directly rather than by faking FPP/audio
// observation evidence underneath it.

func newCueCatalogAutoDeployFixture(t *testing.T, runs []currentrun.Run) (api *API, st *store.Store, pub *fakeAudioPublisher, token string) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token = mustIssueToken(t, svc, admin.ID)
	pub = &fakeAudioPublisher{}
	deps := assetManifestTestDeps(t, svc, st)
	deps.AudioPublisher = pub
	deps.CurrentRuns = currentRunsReaderFake{snapshot: currentrun.Snapshot{Runs: runs}}
	api = New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026","notes":""}`)
	mustPutShowActive(t, api, token, "halloween-2026")
	return api, st, pub, token
}

// TestAutoDeployCueCatalog_DeploysWhenNothingRunning is acceptance
// criterion 1: authoring between shows (nothing playing, no evidence of
// anything playing because nothing is happening) must not read as
// "unknown, so hold"; it is a real, positive "safe to deploy" answer.
func TestAutoDeployCueCatalog_DeploysWhenNothingRunning(t *testing.T) {
	api, st, pub, token := newCueCatalogAutoDeployFixture(t, nil)
	auth := map[string]string{"Authorization": "Bearer " + token}
	revision := resolvedCueCatalogRevision(t, api, "render-01", auth)

	observedAt := testNow
	pub.result = mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.cuecatalog.revision", Value: revision,
			ObservedAt: &observedAt, CollectedAt: observedAt,
		},
	}

	api.AutoDeployCueCatalog(context.Background(), testNow, "render-01")

	if pub.count() != 1 {
		t.Fatalf("publish count = %d, want 1: nothing running must not hold an automatic deploy", pub.count())
	}
	ack, err := st.GetNodeCueCatalogAck(context.Background(), "render-01")
	if err != nil {
		t.Fatalf("GetNodeCueCatalogAck: %v", err)
	}
	if ack.Revision != revision {
		t.Fatalf("acknowledged revision = %q, want %q", ack.Revision, revision)
	}
}

// TestAutoDeployCueCatalog_HeldWhenActivationRunning is acceptance
// criterion 2's own auto-deploy half: a currently playing FPP run holds
// the deploy outright. No command is ever dispatched, so the node stays
// on whatever catalog it is actually executing.
func TestAutoDeployCueCatalog_HeldWhenActivationRunning(t *testing.T) {
	api, st, pub, _ := newCueCatalogAutoDeployFixture(t, []currentrun.Run{
		{ID: "fpp:player-01", Runner: currentrun.RunnerFPP,
			Playback: currentrun.Playback{State: "playing"}, Freshness: currentrun.Freshness{State: "current"}},
	})

	api.AutoDeployCueCatalog(context.Background(), testNow, "render-01")

	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0: a deploy must never dispatch while a cue is running", pub.count())
	}
	if _, err := st.GetNodeCueCatalogAck(context.Background(), "render-01"); err != store.ErrNodeCueCatalogAckNotFound {
		t.Fatalf("GetNodeCueCatalogAck after a held auto-deploy: err = %v, want ErrNodeCueCatalogAckNotFound (the node must stay on whatever it already held)", err)
	}
}

// TestAutoDeployCueCatalog_HeldWhenPlaybackEvidenceIsStale is a regression
// test for a real reviewed defect: a run whose LAST REPORTED state reads
// idle must still hold when that evidence itself is not fresh, because a
// stale idle reading can predate a Cue that started after the reporting
// source went quiet. Reading staleness as "confirmed idle" would let an
// automatic deploy land under a Cue this coordinator simply has not heard
// about yet.
func TestAutoDeployCueCatalog_HeldWhenPlaybackEvidenceIsStale(t *testing.T) {
	api, st, pub, _ := newCueCatalogAutoDeployFixture(t, []currentrun.Run{
		{ID: "fpp:player-01", Runner: currentrun.RunnerFPP,
			Playback: currentrun.Playback{State: "idle"}, Freshness: currentrun.Freshness{State: "stale"}},
	})

	api.AutoDeployCueCatalog(context.Background(), testNow, "render-01")

	if pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0: stale playback evidence must hold, not be read as confirmed idle", pub.count())
	}
	if _, err := st.GetNodeCueCatalogAck(context.Background(), "render-01"); err != store.ErrNodeCueCatalogAckNotFound {
		t.Fatalf("GetNodeCueCatalogAck after a stale-evidence hold: err = %v, want ErrNodeCueCatalogAckNotFound", err)
	}
}
