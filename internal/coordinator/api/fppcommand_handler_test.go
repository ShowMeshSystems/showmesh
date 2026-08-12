package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file is Step 7 seam C's own acceptance-criteria proof, driving a
// real [API] (real store.Store, real identity.Service, a real HTTP fake
// standing in for FPP) exactly the way this package's other test files
// require (contract section 1: no hand-built wire structs asserted
// against a fake). It does NOT touch bench/fpp-multisync or any real FPP
// — every dispatch target here is an httptest.Server this file controls.

// dynamicObservationLister is [fakeObservationLister]'s mutable sibling:
// this handler's confirmation loop (fppcommand_handler.go's
// confirmFPPStatus) polls [ObservationLister] repeatedly over real wall
// time, so a test proving "confirmed once evidence catches up" or
// "unconfirmed because it never does" needs a lister whose answer can
// change, or never change, across that poll window — [fakeObservationLister]'s
// static field cannot express either.
type dynamicObservationLister struct {
	mu  sync.Mutex
	obs []observation.Observation
	err error
}

func (d *dynamicObservationLister) ListObservations(context.Context, ObservationFilter) ([]observation.Observation, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.err != nil {
		return nil, d.err
	}
	return append([]observation.Observation(nil), d.obs...), nil
}

func (d *dynamicObservationLister) setObs(obs []observation.Observation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.obs = obs
}

// fppStatusObs builds a current fpp.status observation, mirroring exactly
// what internal/coordinator/collector/fpp.SignalStatus produces (this
// package deliberately does not import that package — see
// fppcommand_handler.go's own doc comment — so this literal string is
// this test file's own copy of the same wire signal name).
func fppStatusObs(instanceID, value string, at time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID("fpp.status"), value, at,
		observation.WithValidFor(time.Hour),
	))
}

// newFakeFPPCommandServer stands in for a bench (or, in a real
// deployment, a live) fppd's GET /api/command/... endpoint. Every request
// this test's dispatch reaches is recorded so a test can assert exactly
// one command was issued, never a retry and never a second one.
type fakeFPPCommandServer struct {
	mu       sync.Mutex
	requests []string // recorded EscapedPath() of every request received
	status   int
	body     string
}

func newFakeFPPCommandServer(t *testing.T, status int, body string) (*httptest.Server, *fakeFPPCommandServer) {
	t.Helper()
	f := &fakeFPPCommandServer{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.EscapedPath())
		f.mu.Unlock()
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeFPPCommandServer) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// fppCommandTestSetup bundles a real store.Store (so h.deps.Commands is
// the real production implementation, per [CommandStore]'s own doc
// comment), a real identity.Service over the SAME store (so an operator
// token, and the fail_audit trigger, both act against exactly what the
// handler under test writes to), and storeDir (needed only by tests that
// install the fail_audit trigger via a second raw connection — see
// internal/coordinator/identity/audited_write_test.go's identical
// pattern, reproduced here because package api cannot import that
// package's unexported test helpers).
type fppCommandTestSetup struct {
	st        *store.Store
	storeDir  string
	svc       identity.Service
	obs       *dynamicObservationLister
	fppLister *fakeFPPLister
}

func newFPPCommandTestSetup(t *testing.T, now func() time.Time) *fppCommandTestSetup {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &fppCommandTestSetup{
		st: st, storeDir: storeDir, svc: svc,
		obs:       &dynamicObservationLister{},
		fppLister: &fakeFPPLister{},
	}
}

func (s *fppCommandTestSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: s.fppLister, Observations: s.obs,
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, Commands: s.st,
	}
}

// installFailAuditTrigger lives in config_test.go in this package: seam A
// and seam C both needed it and wrote it independently, which is how the
// merge found the duplicate. One copy, because two copies of an
// injected-failure helper are two things that can stop agreeing about
// what "the audit store is failing" means.

func stopPlaylistBody(idempotencyKey string) string {
	return `{"action":"stopPlaylist","idempotencyKey":"` + idempotencyKey + `"}`
}

func newFPPCommandRequest(t *testing.T, instanceID, body, bearerToken string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fpp/"+instanceID+"/commands", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return req
}

// --- Acceptance criterion 1: 401 unauthenticated, 403 for a viewer
// naming fpp:command, 200 for an operator. ---

func TestFPPCommandRefusedUnauthenticated(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), "")
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
}

func TestFPPCommandRefusedForbiddenViewerNamesScope(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, "fpp:command") {
		t.Errorf("problem detail = %q, want it to name the missing scope fpp:command", detail)
	}
}

func TestFPPCommandAcceptedForOperator(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Errorf("outcome = %v, want \"confirmed\" (fpp.status was already \"idle\")", cmd["outcome"])
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}

// --- Acceptance criterion 2: a dispatch that succeeds (200 from FPP)
// while observed state never moves is reported unconfirmed, never
// successful. This is the criterion the spec singles out: "a confirmation
// test that would pass against an implementation which reports success on
// a 200 is worthless." See this task's report for the mutation run
// against this exact test. ---

func TestFPPCommandDispatchedButStateNeverMovesIsUnconfirmed(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// fpp.status stays "playing" for the whole confirmation window: FPP
	// accepted the command (200 "Stopped") but the state this endpoint
	// asked for never actually arrives.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	start := time.Now()
	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the REQUEST succeeded; only the command's own outcome is unconfirmed); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — FPP answered 200 but fpp.status never reached \"idle\"", cmd["outcome"])
	}
	if cmd["outcomeReason"] == "" || cmd["outcomeReason"] == nil {
		t.Errorf("outcomeReason is empty, want a reason for an unconfirmed outcome (ADR-020)")
	}
	if cmd["outcomeState"] != "current" {
		t.Errorf("outcomeState = %v, want \"current\" (the evidence WAS obtained; it just showed the wrong value)", cmd["outcomeState"])
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("handler returned after %v, want it to have actually waited out the confirm deadline (~120ms) rather than giving up early", elapsed)
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1 (dispatch is not retried while confirmation is pending)", srv.hitCount())
	}
}

// TestFPPCommandConfirmsOnceEvidenceCatchesUp is the positive control for
// the test above: the SAME "never confirms" shape, except fpp.status
// flips to "idle" partway through the wait — proving the confirmation
// loop is actually watching for a state CHANGE, not merely timing out and
// then always reporting one fixed answer regardless of what the evidence
// says.
func TestFPPCommandConfirmsOnceEvidenceCatchesUp(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	go func() {
		time.Sleep(60 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow)})
	}()

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\" once fpp.status transitioned to \"idle\" mid-wait", cmd["outcome"])
	}
}

// --- Acceptance criterion 3: Stop Playlist succeeds with the audit store
// failing, via the real SQLite trigger. ---

func TestFPPCommandSucceedsWithAuditStoreFailing(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow)})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// Install the trigger AFTER creating the principal/token: neither
	// CreatePrincipal nor IssueToken writes to audit_log (see
	// identity/service.go), so this is the exact moment BUILD-PLAN's
	// criterion describes — every subsequent audit write fails.
	installFailAuditTrigger(t, setup.storeDir)

	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — Stop Playlist is in ADR-024 decision 11's blackout/stop/power-off "+
			"safety class and must proceed regardless of an audit-write failure; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Errorf("outcome = %v, want \"confirmed\" — the command itself must still run and confirm normally", cmd["outcome"])
	}
	if cmd["attributionDegraded"] != true {
		t.Errorf("attributionDegraded = %v, want true (the audit write did fail)", cmd["attributionDegraded"])
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1 — the command must actually dispatch, not merely appear to", srv.hitCount())
	}

	// And the audit log genuinely holds nothing for this command — the
	// degradation is real, not merely reported.
	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, e := range entries {
		if e.CommandID == cmd["id"] {
			t.Errorf("found an audit_log entry for command %v despite the fail_audit trigger being installed: %+v", cmd["id"], e)
		}
	}
}

// --- Acceptance criterion 4 & 5: a replayed idempotency key dispatches
// nothing and returns the original result; dispatch and outcome are
// separate, correlated audit entries. ---

func TestFPPCommandReplayDispatchesNothingAndReturnsOriginalResult(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	first := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("replay-key"), token)
	resp1, body1 := doRawRequest(t, api.Handler, first)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the first dispatch, want 1", srv.hitCount())
	}
	m1 := decodeMap(t, body1)
	cmd1, _ := m1["command"].(map[string]any)

	second := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("replay-key"), token)
	resp2, body2 := doRawRequest(t, api.Handler, second)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the replay, want still 1 — a replay must dispatch nothing", srv.hitCount())
	}
	m2 := decodeMap(t, body2)
	cmd2, _ := m2["command"].(map[string]any)

	if cmd2["replay"] != true {
		t.Errorf("replay = %v, want true", cmd2["replay"])
	}
	if cmd2["id"] != cmd1["id"] {
		t.Errorf("replay id = %v, want the original command's id %v", cmd2["id"], cmd1["id"])
	}
	if cmd2["outcome"] != cmd1["outcome"] {
		t.Errorf("replay outcome = %v, want the original result %v", cmd2["outcome"], cmd1["outcome"])
	}

	// Criterion 5: dispatch and outcome are separate, correlated audit
	// entries; the replay writes its OWN third entry marked as a replay,
	// never mutating either of the first two.
	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var dispatchCount, outcomeCount, replayCount int
	for _, e := range entries {
		if e.CommandID != cmd1["id"] {
			continue
		}
		switch e.Kind {
		case identity.AuditDispatch:
			dispatchCount++
		case identity.AuditOutcome:
			outcomeCount++
		case identity.AuditReplay:
			replayCount++
		}
	}
	if dispatchCount != 1 {
		t.Errorf("dispatch audit entries for this command = %d, want exactly 1", dispatchCount)
	}
	if outcomeCount != 1 {
		t.Errorf("outcome audit entries for this command = %d, want exactly 1", outcomeCount)
	}
	if replayCount != 1 {
		t.Errorf("replay audit entries for this command = %d, want exactly 1", replayCount)
	}
}

// --- OpenAPI conformance for this endpoint's own response shape,
// including the replay shape (whose "outcome" may differ in practice from
// every other schema-checked response in openapi_test.go). ---

func TestOpenAPIFPPCommandResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)

	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("openapi-key"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "FPPCommandResponse", body)

	// The replay shape too — validated separately since replay is the one
	// case Outcome/timestamps can differ in practice from a fresh dispatch.
	req2 := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("openapi-key"), token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	assertMatchesSchema(t, c, "FPPCommandResponse", body2)
}
