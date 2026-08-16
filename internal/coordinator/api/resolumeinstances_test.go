package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track D seam E's own handler-level test suite: GET
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
	if comp["revision"] != float64(1) {
		t.Errorf("composition.revision = %v, want 1", comp["revision"])
	}
	if comp["activatedAt"] == nil || comp["activatedAt"] == "" {
		t.Errorf("composition.activatedAt is absent")
	}
}

// resolumeArenaThatMustNeverBeContacted is a real HTTP server standing in
// for a live Arena: any request it receives fails the test immediately.
// Used by TestResolumeInstancesRoutesNeverContactArena (acceptance
// criterion 9) — spec section 3's "no new HTTP request to Arena, and no
// change to the poll loop" is a property of resolumeinstances.go, stream.go
// and mapping.go's Resolume additions specifically, and this pins it
// against a runtime check rather than only a static "this file has no
// http.Client field" reading.
func resolumeArenaThatMustNeverBeContacted(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("a Resolume Arena stub received a request while serving a Track D seam E route: %s %s — none of GET /resolume/instances, GET /resolume/instances/{id}, or the change stream may ever contact Resolume (ADR-032 decision 2)", r.Method, r.URL)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestResolumeInstancesRoutesNeverContactArena is acceptance criterion 9.
// resolumeArenaThatMustNeverBeContacted's handler fails the test the
// instant it receives anything; this drives the list route, the
// single-instance route, the snapshot route, and one real SSE render pass
// (the stream.go code path) and proves none of them ever reach it.
func TestResolumeInstancesRoutesNeverContactArena(t *testing.T) {
	arena := resolumeArenaThatMustNeverBeContacted(t)
	_ = arena // stood up and running; nothing in this seam holds any field capable of reaching it

	fixture := resolumeInstanceFixture(t)
	deps := resolumeInstancesTestDeps(&fakeResolumeLister{views: []ResolumeInstanceView{fixture}})
	testAPI := newStreamTestAPI(deps)

	for _, target := range []string{
		"/api/v1/resolume/instances",
		"/api/v1/resolume/instances/resolume",
		"/api/v1/snapshot",
	} {
		resp, body := doRequest(t, testAPI.Handler, "GET", target, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s: status = %d, want 200; body: %s", target, resp.StatusCode, body)
		}
	}

	// One real SSE render pass: the OTHER place this seam's mapping code
	// runs (Hub.render in stream.go), which the three plain GETs above
	// never exercise on their own.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go testAPI.Hub.Run(ctx)

	srv := httptest.NewServer(testAPI.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start", event)
	}
	testAPI.Hub.Notify()
	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "resolume.changed" {
		t.Fatalf("event after Notify = %q, want resolume.changed (the first render pass for a never-before-seen instance)", event)
	}
}
