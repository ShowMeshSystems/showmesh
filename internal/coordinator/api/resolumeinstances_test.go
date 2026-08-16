package api

import (
	"net/http"
	"os/exec"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is the handler-level test suite for Resolume-as-observability: GET
// /resolume/instances, GET /resolume/instances/{id}, and their appearance
// in GET /snapshot. Every test drives the real handler/mapping code (this
// package's own standing rule — see fakes_test.go's top comment), never a
// hand-built v1 struct.

// resolumeInstancesTestDeps builds a Dependencies carrying resolume for
// Resolume and everything else defaulted to a no-op — the composition read
// path (h.deps.Config) is exercised separately, against a real *store.Store,
// by TestResolumeInstanceCompositionNullBeforeUploadNamedAfter below.
func resolumeInstancesTestDeps(resolume ResolumeLister) Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Resolume: resolume,
	}
}

// TestResolumeInstancesListReturnsEveryObservationWithProvenance is
// acceptance criterion 1: GET /resolume/instances returns the instance with
// every resolume.* observation, each carrying state, reason, and
// provenance (source).
func TestResolumeInstancesListReturnsEveryObservationWithProvenance(t *testing.T) {
	fixture := resolumeInstanceFixture(t)
	api := New(resolumeInstancesTestDeps(&fakeResolumeLister{views: []ResolumeInstanceView{fixture}}),
		Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	m := decodeMap(t, body)
	instances, _ := m["instances"].([]any)
	if len(instances) != 1 {
		t.Fatalf("instances = %v, want exactly 1", instances)
	}
	inst, _ := instances[0].(map[string]any)
	if inst["instanceId"] != "resolume" {
		t.Errorf("instanceId = %v, want %q", inst["instanceId"], "resolume")
	}
	obs, _ := inst["observations"].([]any)
	if len(obs) != len(fixture.Observations) {
		t.Fatalf("observations count = %d, want %d (every resolume.* signal the coordinator holds)", len(obs), len(fixture.Observations))
	}
	wantSignals := map[string]bool{
		"resolume.reachable": false, "resolume.composition.identified": false, "resolume.composition.name": false,
	}
	for _, o := range obs {
		row, _ := o.(map[string]any)
		sig, _ := row["signal"].(string)
		if _, known := wantSignals[sig]; !known {
			t.Errorf("unexpected signal %q in response", sig)
			continue
		}
		wantSignals[sig] = true
		if row["state"] == nil || row["state"] == "" {
			t.Errorf("signal %q: state is absent, want a stated evidence state", sig)
		}
		if row["source"] == nil || row["source"] == "" {
			t.Errorf("signal %q: source (provenance) is absent", sig)
		}
		// resolume.composition.name is permanently unsupported and must carry
		// its reason rather than being filtered out for looking empty (spec
		// section 2.2 rule 2).
		if sig == "resolume.composition.name" {
			if row["state"] != "unsupported" {
				t.Errorf("resolume.composition.name state = %v, want %q", row["state"], "unsupported")
			}
			if row["reason"] == nil || row["reason"] == "" {
				t.Errorf("resolume.composition.name: reason is absent despite state=unsupported")
			}
		}
	}
	for sig, seen := range wantSignals {
		if !seen {
			t.Errorf("signal %q from the fixture is missing from the response", sig)
		}
	}
}

// TestResolumeInstancesListUnconfiguredReturnsEmptyArray is half of
// acceptance criterion 2: an unconfigured coordinator (nothing to list)
// answers 200 with an empty array, never null, never a 404.
func TestResolumeInstancesListUnconfiguredReturnsEmptyArray(t *testing.T) {
	api := New(resolumeInstancesTestDeps(&fakeResolumeLister{}), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	instances, ok := m["instances"].([]any)
	if !ok {
		t.Fatalf(`"instances" is not a JSON array (want []): %s`, body)
	}
	if len(instances) != 0 {
		t.Errorf("instances = %v, want an empty array", instances)
	}
}

// TestResolumeInstanceSingleRouteUnconfiguredReturns404 is the other half
// of acceptance criterion 2: the single-instance route is the ordinary
// resource-not-found shape when nothing is configured.
func TestResolumeInstanceSingleRouteUnconfiguredReturns404(t *testing.T) {
	api := New(resolumeInstancesTestDeps(&fakeResolumeLister{}), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances/resolume", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeResourceNotFound {
		t.Errorf("problem type = %v, want %q", m["type"], ProblemTypeResourceNotFound)
	}
}

// TestResolumeInstanceSingleRouteReturnsConfiguredInstance proves the
// single-instance route answers 200 with the matching instance when one
// IS configured, and 404 for any other id — the ordinary GET
// /fpp/{instanceId} shape, applied here.
func TestResolumeInstanceSingleRouteReturnsConfiguredInstance(t *testing.T) {
	fixture := resolumeInstanceFixture(t)
	api := New(resolumeInstancesTestDeps(&fakeResolumeLister{views: []ResolumeInstanceView{fixture}}),
		Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances/resolume", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	inst, _ := m["instance"].(map[string]any)
	if inst["instanceId"] != "resolume" {
		t.Errorf("instanceId = %v, want %q", inst["instanceId"], "resolume")
	}

	resp2, body2 := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances/no-such-instance", nil)
	if resp2.StatusCode != http.StatusNotFound {
		t.Fatalf("status for unknown id = %d, want 404; body: %s", resp2.StatusCode, body2)
	}
}

// TestSnapshotIncludesResolumeInstances is acceptance criterion 3.
func TestSnapshotIncludesResolumeInstances(t *testing.T) {
	fixture := resolumeInstanceFixture(t)
	api := New(resolumeInstancesTestDeps(&fakeResolumeLister{views: []ResolumeInstanceView{fixture}}),
		Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	resolume, ok := m["resolume"].([]any)
	if !ok {
		t.Fatalf(`snapshot "resolume" is missing or not an array: %s`, body)
	}
	if len(resolume) != 1 {
		t.Fatalf("snapshot resolume = %v, want exactly 1 instance", resolume)
	}
	inst, _ := resolume[0].(map[string]any)
	if inst["instanceId"] != "resolume" {
		t.Errorf("snapshot resolume[0].instanceId = %v, want %q", inst["instanceId"], "resolume")
	}
}

// TestResolumeInstanceCompositionNullBeforeUploadNamedAfter is acceptance
// criterion 8, driven against a REAL *store.Store (config_test.go's own
// harness) rather than a fake, because composition is genuinely stored
// configuration (ADR-032), not something a fake can honestly stand in for.
func TestResolumeInstanceCompositionNullBeforeUploadNamedAfter(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	fixture := resolumeInstanceFixture(t)
	deps := configTestDeps(svc, st)
	deps.Resolume = &fakeResolumeLister{views: []ResolumeInstanceView{fixture}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances/resolume", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	inst, _ := m["instance"].(map[string]any)
	if inst["composition"] != nil {
		t.Errorf("composition before any upload = %v, want null", inst["composition"])
	}

	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	uploadResp, uploadBody := doResolumeCompositionUpload(t, api.Handler, "Holiday Test Show.avc", content,
		map[string]string{"Authorization": "Bearer " + token})
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("composition upload: status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}

	resp2, body2 := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances/resolume", nil)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("status after upload = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	inst2, _ := m2["instance"].(map[string]any)
	comp, ok := inst2["composition"].(map[string]any)
	if !ok {
		t.Fatalf("composition after upload = %v, want an object", inst2["composition"])
	}
	if comp["name"] != "Holiday Test Show" {
		t.Errorf("composition.name = %v, want %q", comp["name"], "Holiday Test Show")
	}
}

// TestResolumeInstanceCompositionOmitsRevisionAndActivatedAt is finding 4's
// own test (owner ruling, 2026-08-16): GET /resolume/instances is an open
// read with no credential by default, so revision and activatedAt — who
// changed the loaded show and when — must not appear on it; they stay on
// the gated GET /config/resolume/composition, which already carries both.
func TestResolumeInstanceCompositionOmitsRevisionAndActivatedAt(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	fixture := resolumeInstanceFixture(t)
	deps := configTestDeps(svc, st)
	deps.Resolume = &fakeResolumeLister{views: []ResolumeInstanceView{fixture}}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	content := mustReadResolumeCompositionTestdata(t, "complete.avc")
	uploadResp, uploadBody := doResolumeCompositionUpload(t, api.Handler, "Holiday Test Show.avc", content,
		map[string]string{"Authorization": "Bearer " + token})
	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("composition upload: status = %d, want 200; body: %s", uploadResp.StatusCode, uploadBody)
	}

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/resolume/instances/resolume", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	inst, _ := m["instance"].(map[string]any)
	comp, ok := inst["composition"].(map[string]any)
	if !ok {
		t.Fatalf("composition = %v, want an object", inst["composition"])
	}
	if _, present := comp["revision"]; present {
		t.Errorf("composition carries \"revision\" on the open read; it must stay on the gated GET /config/resolume/composition: %v", comp)
	}
	if _, present := comp["activatedAt"]; present {
		t.Errorf("composition carries \"activatedAt\" on the open read; it must stay on the gated GET /config/resolume/composition: %v", comp)
	}
}

// TestPackageNeverImportsACollector is acceptance criterion 9, replacing
// an earlier version of this file's own decorative httptest.Server (owner
// review finding 2, 2026-08-16): that server's URL was handed to nothing,
// so no production path could ever learn it, and the test could not fail
// no matter what the handler code did. This asserts the actual property
// mechanically instead — mirroring cmd/showmeshctl/importgraph_test.go and
// this repo's collector/resolume/guardosc_test.go's identical `go list
// -deps` technique: internal/coordinator/api transitively imports no
// internal/coordinator/collector/... package, so it structurally holds no
// client capable of reaching Arena, FPP, or any other collector-owned
// transport. It fails the moment such an import lands.
func TestPackageNeverImportsACollector(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps . failed: %v\noutput:\n%s", err, out)
	}
	const forbiddenBase = "github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	for _, dep := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if dep == forbiddenBase || strings.HasPrefix(dep, forbiddenBase+"/") {
			t.Errorf("internal/coordinator/api transitively imports %q — GET /resolume/instances, GET /resolume/instances/{id}, and the change stream must be servable from stored evidence alone", dep)
		}
	}
}
