package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is nightinstanceparticipation.go's coverage, and the two tests
// that matter run the asymmetry in both directions. One of them alone
// would pass against a change that simply stopped checking anything.

const nightParticipationShow = "halloween-2026"

// reachableFPPView builds one configured FPP instance whose derived health
// is healthy when reachable is true and failed when it is false, using the
// same fpp.reachable/fpp.fppd.state pair deriveInstanceHealth reads.
func reachableFPPView(t *testing.T, instanceID string, reachable bool) FPPInstanceView {
	t.Helper()
	res := observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID}
	at := testNow.Add(-time.Second)
	return FPPInstanceView{
		InstanceID: instanceID,
		Endpoint:   "http://10.0.1.20",
		Observations: []observation.Observation{
			mustObs(observation.Measured(res, "fpp.reachable", reachable, at,
				observation.WithSource("fpp-rest"), observation.WithValidFor(time.Minute), observation.WithCollectedAt(at))),
			mustObs(observation.Measured(res, "fpp.fppd.state", "running", at,
				observation.WithSource("fpp-rest"), observation.WithValidFor(time.Minute), observation.WithCollectedAt(at))),
		},
	}
}

// reachableResolumeView is [reachableFPPView] one integration over.
func reachableResolumeView(t *testing.T, instanceID string, reachable bool) ResolumeInstanceView {
	t.Helper()
	res := observation.ResourceRef{Kind: observation.ResourceResolume, ID: instanceID}
	at := testNow.Add(-time.Second)
	return ResolumeInstanceView{
		InstanceID: instanceID,
		Observations: []observation.Observation{
			mustObs(observation.Measured(res, "resolume.reachable", reachable, at,
				observation.WithSource("resolume-rest"), observation.WithValidFor(time.Minute), observation.WithCollectedAt(at))),
			mustObs(observation.Measured(res, "resolume.composition.identified", "identified", at,
				observation.WithSource("resolume-survey"), observation.WithValidFor(time.Hour), observation.WithCollectedAt(at))),
		},
	}
}

// newNightParticipationFixture wires an admin API whose show object body
// is showBody (which is where the participation selection lives), with
// that show activated, and with exactly the configured FPP and Resolume
// instances given.
//
// It returns the fixture's own instance counts so a caller can assert the
// world it is testing against is not empty before it asserts an absence:
// "no instance was selected" and "no instance exists to select" produce an
// identical, passing check list, and only the count tells them apart.
func newNightParticipationFixture(t *testing.T, showBody string, fpp []FPPInstanceView, resolume []ResolumeInstanceView) (*API, int, int) {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := assetManifestTestDeps(t, svc, st)
	deps.FPP = &fakeFPPLister{views: fpp}
	deps.Resolume = &fakeResolumeLister{views: resolume}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	mustPutShow(t, api, token, nightParticipationShow, showBody)
	mustPutShowActive(t, api, token, nightParticipationShow)

	// Read the counts back through the wired dependency, not off the
	// slices this function was handed: a lister that silently drops its
	// views would otherwise still report a populated fixture.
	gotFPP, err := api.h.deps.FPP.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("fixture: list fpp instances: %v", err)
	}
	gotResolume, err := api.h.deps.Resolume.ListInstances(context.Background())
	if err != nil {
		t.Fatalf("fixture: list resolume instances: %v", err)
	}
	return api, len(gotFPP), len(gotResolume)
}

// nightParticipationChecks runs the check family under test.
func nightParticipationChecks(t *testing.T, api *API) []nightReadinessCheck {
	t.Helper()
	return api.h.nightCheckShowInstanceParticipation(context.Background(), testNow, nightParticipationShow)
}

func findCheck(checks []nightReadinessCheck, name string) (nightReadinessCheck, bool) {
	for _, c := range checks {
		if c.name == name {
			return c, true
		}
	}
	return nightReadinessCheck{}, false
}

// TestNightParticipationSelectedInstanceMissingOrUnhealthyFailsReadiness is
// the loud half of the asymmetry: an instance the active show says takes
// part tonight must redden readiness when it is absent from this
// coordinator's configuration entirely, and when it is configured but
// unhealthy.
func TestNightParticipationSelectedInstanceMissingOrUnhealthyFailsReadiness(t *testing.T) {
	body := `{"name":"Halloween 2026","notes":"","fppInstances":["player-01","ghost-01"],"resolumeInstances":[]}`
	api, fppCount, resolumeCount := newNightParticipationFixture(t,
		body,
		[]FPPInstanceView{reachableFPPView(t, "player-01", false)},
		nil)
	if fppCount != 1 {
		t.Fatalf("fixture: configured FPP instances = %d, want 1", fppCount)
	}
	if resolumeCount != 0 {
		t.Fatalf("fixture: configured Resolume instances = %d, want 0", resolumeCount)
	}

	checks := nightParticipationChecks(t, api)
	if len(checks) == 0 {
		t.Fatalf("no participation checks were produced at all")
	}

	absent, ok := findCheck(checks, "participation:fpp:ghost-01")
	if !ok {
		t.Fatalf("no participation:fpp:ghost-01 check; got %v", checkNames(checks))
	}
	if absent.health != nightHealthFailed() {
		t.Errorf("selected-but-absent instance: health = %q, want %q", absent.health, nightHealthFailed())
	}
	if !strings.Contains(absent.reason, "no such instance is configured on this coordinator") {
		t.Errorf("selected-but-absent instance: reason = %q, want it to say the instance is not configured", absent.reason)
	}

	unhealthy, ok := findCheck(checks, "participation:fpp:player-01")
	if !ok {
		t.Fatalf("no participation:fpp:player-01 check; got %v", checkNames(checks))
	}
	if unhealthy.health != nightHealthFailed() {
		t.Errorf("selected-and-unhealthy instance: health = %q, want %q", unhealthy.health, nightHealthFailed())
	}

	if got := nightOutcomeFromChecks(checks); got != "not_ready" {
		t.Errorf("aggregate outcome = %q, want %q", got, "not_ready")
	}
}

// TestNightParticipationUnselectedUnhealthyInstanceDoesNotRedden is the
// quiet half, and it is a separate test on purpose: a change that silenced
// participation altogether would pass this one and fail its sibling above.
// A connected, unhealthy FPP host and a connected, unreachable Resolume
// instance that no active show selects are not tonight's problem.
func TestNightParticipationUnselectedUnhealthyInstanceDoesNotRedden(t *testing.T) {
	body := `{"name":"Halloween 2026","notes":"","fppInstances":["player-01"],"resolumeInstances":[]}`
	api, fppCount, resolumeCount := newNightParticipationFixture(t,
		body,
		[]FPPInstanceView{reachableFPPView(t, "player-01", true), reachableFPPView(t, "spare-01", false)},
		[]ResolumeInstanceView{reachableResolumeView(t, "arena-1", false)})
	// The absence asserted below is only meaningful against a populated
	// world: assert the unhealthy instances actually exist first.
	if fppCount != 2 {
		t.Fatalf("fixture: configured FPP instances = %d, want 2", fppCount)
	}
	if resolumeCount != 1 {
		t.Fatalf("fixture: configured Resolume instances = %d, want 1", resolumeCount)
	}

	checks := nightParticipationChecks(t, api)

	// Positive control: the machinery ran and did check the one selected
	// instance, so an empty or skipped check list cannot be what makes the
	// absences below pass.
	selected, ok := findCheck(checks, "participation:fpp:player-01")
	if !ok {
		t.Fatalf("no participation:fpp:player-01 check; got %v", checkNames(checks))
	}
	if selected.health != nightHealthHealthy() {
		t.Fatalf("selected healthy instance: health = %q, want %q", selected.health, nightHealthHealthy())
	}

	if c, ok := findCheck(checks, "participation:fpp:spare-01"); ok {
		t.Errorf("unselected unhealthy FPP instance produced a check: %+v", c)
	}
	if c, ok := findCheck(checks, "participation:resolume:arena-1"); ok {
		t.Errorf("unselected unhealthy Resolume instance produced a check: %+v", c)
	}
	if got := nightOutcomeFromChecks(checks); got != "ready" {
		t.Errorf("aggregate outcome = %q, want %q; checks: %v", got, "ready", checks)
	}
}

// TestNightParticipationUnrecordedSelectionCountsEveryInstance is the
// upgrade case: a show that predates participation has no selection, and
// the default has to be the loud one. Reading an unrecorded selection as
// "nothing takes part" would turn this check list green for every show
// that exists today.
func TestNightParticipationUnrecordedSelectionCountsEveryInstance(t *testing.T) {
	api, fppCount, _ := newNightParticipationFixture(t,
		`{"name":"Halloween 2026","notes":""}`,
		[]FPPInstanceView{reachableFPPView(t, "player-01", false)},
		nil)
	if fppCount != 1 {
		t.Fatalf("fixture: configured FPP instances = %d, want 1", fppCount)
	}

	checks := nightParticipationChecks(t, api)
	c, ok := findCheck(checks, "participation:fpp:player-01")
	if !ok {
		t.Fatalf("an unrecorded selection produced no check for a configured, unhealthy instance; got %v", checkNames(checks))
	}
	if c.health != nightHealthFailed() {
		t.Errorf("health = %q, want %q", c.health, nightHealthFailed())
	}
}

// TestInstanceParticipationStatesAreKeptApart pins the four reachable
// states of the rendered field, and above all that a show with no recorded
// selection reports "selection_unrecorded" rather than
// "not_participating". Collapsing those two would make
// "not_participating" reachable two ways, one of them meaning "nobody was
// ever asked", and permanently unusable as evidence.
func TestInstanceParticipationStatesAreKeptApart(t *testing.T) {
	t.Run("unrecorded selection", func(t *testing.T) {
		api, _, _ := newNightParticipationFixture(t, `{"name":"Halloween 2026","notes":""}`,
			[]FPPInstanceView{reachableFPPView(t, "player-01", true)}, nil)
		got := api.h.resolveInstanceParticipation(context.Background()).forFPP("player-01")
		if got.State != "selection_unrecorded" {
			t.Fatalf("state = %q, want %q", got.State, "selection_unrecorded")
		}
		if got.Reason == nil || *got.Reason != participationSelectionUnrecordedReason {
			t.Errorf("reason = %v, want %q", got.Reason, participationSelectionUnrecordedReason)
		}
		if got.Show != nightParticipationShow {
			t.Errorf("show = %q, want %q", got.Show, nightParticipationShow)
		}
	})

	t.Run("explicitly empty selection", func(t *testing.T) {
		api, _, _ := newNightParticipationFixture(t,
			`{"name":"Halloween 2026","notes":"","fppInstances":[],"resolumeInstances":[]}`,
			[]FPPInstanceView{reachableFPPView(t, "player-01", true)}, nil)
		got := api.h.resolveInstanceParticipation(context.Background()).forFPP("player-01")
		if got.State != "not_participating" {
			t.Fatalf("state = %q, want %q", got.State, "not_participating")
		}
	})

	t.Run("selected", func(t *testing.T) {
		api, _, _ := newNightParticipationFixture(t,
			`{"name":"Halloween 2026","notes":"","fppInstances":["player-01"]}`,
			[]FPPInstanceView{reachableFPPView(t, "player-01", true)}, nil)
		got := api.h.resolveInstanceParticipation(context.Background()).forFPP("player-01")
		if got.State != "participating" {
			t.Fatalf("state = %q, want %q", got.State, "participating")
		}
	})
}

func checkNames(checks []nightReadinessCheck) []string {
	names := make([]string, 0, len(checks))
	for _, c := range checks {
		names = append(names, c.name)
	}
	return names
}

// TestPutShowSignalsTheStreamHub is the definition-of-done half that makes
// participation observable: without this poke, every rendered instance's
// showParticipation keeps its old value until the hub's own recompute
// interval happens to come round.
func TestPutShowSignalsTheStreamHub(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	api := New(assetManifestTestDeps(t, svc, st), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	if api.h.notifyStream == nil {
		t.Fatal("New did not wire a stream notifier onto the handlers")
	}
	pokes := 0
	api.h.notifyStream = func() { pokes++ }

	mustPutShow(t, api, token, nightParticipationShow, `{"name":"Halloween 2026","notes":"","fppInstances":["player-01"]}`)
	if pokes != 1 {
		t.Fatalf("stream pokes after a participation write = %d, want 1", pokes)
	}
}
