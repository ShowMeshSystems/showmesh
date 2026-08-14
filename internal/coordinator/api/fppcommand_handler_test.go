package api

import (
	"context"
	"errors"
	"io"
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
// this test file's own copy of the same wire signal name). collectedAt is
// explicit, never left to default to real wall-clock time (Step 7 seam C
// review defect 2's own fence, evaluateFPPStatusEvidence, compares
// CollectedAt against the dispatch instant, and a test running against a
// FIXED clock — see fixedClock/testNow — cannot leave that to whatever
// moment the Go runtime happens to construct this value at without making
// the fence's pass/fail outcome a coincidence of wall-clock timing rather
// than a deliberate test condition).
func fppStatusObs(instanceID, value string, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID("fpp.status"), value, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource("fpp-rest"),
	))
}

// fppStatusObsUnknownAge is [fppStatusObs]'s unknown-observation-age
// sibling: a value is present (collected after dispatch, so it passes the
// pre-dispatch fence), but ObservedAt is nil — the retained-MQTT case
// ([observation.MeasuredUnknownAge]'s own doc comment) — so
// evaluateFPPStatusEvidence's o.StateAt(now) resolves to StateUnknownAge,
// never StateCurrent, regardless of Value.
func fppStatusObsUnknownAge(instanceID, value string, collectedAt time.Time) observation.Observation {
	return mustObs(observation.MeasuredUnknownAge(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID("fpp.status"), value,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource("fpp-rest"),
	))
}

// fppStatusObsFromSource is [fppStatusObs] with an explicit source,
// for Step 7 seam C review defect 3's own tests: fpp-rest and fpp-mqtt
// both emit fpp.status for the same resource, and which one a
// confirmation check trusts must be this package's documented precedence
// rule (precedence.go), never row order.
func fppStatusObsFromSource(instanceID, value, source string, observedAt, collectedAt time.Time) observation.Observation {
	return mustObs(observation.Measured(
		observation.ResourceRef{Kind: observation.ResourceFPP, ID: instanceID},
		observation.SignalID("fpp.status"), value, observedAt,
		observation.WithValidFor(time.Hour), observation.WithCollectedAt(collectedAt), observation.WithSource(source),
	))
}

// newFakeFPPCommandServer stands in for a bench (or, in a real
// deployment, a live) fppd's GET /api/command/... endpoint. Every request
// this test's dispatch reaches is recorded so a test can assert exactly
// one command was issued, never a retry and never a second one.
//
// requestTimes records real wall-clock time.Now() (never h.now(), which a
// test typically freezes via Options.Clock) for each request — finding 10's
// own need: proving the write-before-dispatch ORDER (the commands row, its
// desired_state row, and the dispatch audit entry all land before FPP is
// ever contacted) requires comparing two instants captured on a consistent
// clock. Comparing a frozen test clock against this server's real
// wall-clock hit time would not prove ordering at all — it would only
// prove the frozen instant differs from whenever the test happened to run,
// which is true regardless of correctness.
type fakeFPPCommandServer struct {
	mu            sync.Mutex
	requests      []string    // recorded EscapedPath() of every request received
	requestBodies []string    // recorded raw JSON body of every request received (Step 8: fppcommand.Client posts {"command":...,"args":[...]} to /api/command, so the PATH alone no longer distinguishes one dispatched primitive from another — the body does)
	requestTimes  []time.Time // real wall-clock time.Now() when each request was received
	status        int
	body          string
}

func newFakeFPPCommandServer(t *testing.T, status int, body string) (*httptest.Server, *fakeFPPCommandServer) {
	t.Helper()
	f := &fakeFPPCommandServer{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.requests = append(f.requests, r.URL.EscapedPath())
		f.requestBodies = append(f.requestBodies, string(raw))
		f.requestTimes = append(f.requestTimes, time.Now())
		f.mu.Unlock()
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

// newFailIfHitFPPCommandServer returns an httptest.Server that fails t
// immediately if it EVER receives a request. Used by
// TestFPPCommandNonSafetyClassPrimitiveFailsClosedWithAuditFailing to
// prove the fail-closed refusal dispatches NOTHING to FPP — a stronger
// check than reading hitCount() == 0 after the response returns, which
// only shows nothing had arrived BY THAT MOMENT and would not fail loudly
// if a future regression fired the dispatch on a goroutine racing the
// response write.
func newFailIfHitFPPCommandServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("FPP received a request (%s %s) despite the command being refused before dispatch (ADR-024 "+
			"decision 11's fail-closed default for a non-safety-class primitive)", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeFPPCommandServer) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// lastBody returns the most recently received request's raw JSON body, or
// "" if none has arrived yet.
func (f *fakeFPPCommandServer) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requestBodies) == 0 {
		return ""
	}
	return f.requestBodies[len(f.requestBodies)-1]
}

// firstRequestTime returns the real wall-clock instant the first request
// was received, or the zero Time if none has arrived yet.
func (f *fakeFPPCommandServer) firstRequestTime() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requestTimes) == 0 {
		return time.Time{}
	}
	return f.requestTimes[0]
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

// TestFPPCommandRejectsMissingIdempotencyKey is finding 8's own regression
// test: nothing previously proved this ENDPOINT actually calls
// [command.ValidateIdempotencyKey], only that the function itself works
// (pkg/command's own tests). Without the call, a request with no
// idempotencyKey defaults to the empty string, and two genuinely separate
// commands with no key both collide on the empty-string UNIQUE
// constraint — the second is reported as a REPLAY, dispatches nothing, and
// answers "outcome":"confirmed" while nothing ever reached FPP. This test
// asserts the FIRST line of defense: the endpoint refuses with 400 before
// ever reaching the store or dispatching anything.
func TestFPPCommandRejectsMissingIdempotencyKey(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", `{"action":"stopPlaylist"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no idempotencyKey supplied); body: %s", resp.StatusCode, body)
	}
	if srv.hitCount() != 0 {
		t.Errorf("FPP received %d requests, want 0 — a rejected request must dispatch nothing", srv.hitCount())
	}
	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("commands rows = %d, want 0 — a rejected request must not create a commands row either", len(rows))
	}
}

func TestFPPCommandAcceptedForOperator(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Collected AT testNow — the same instant [fixedClock] stamps this
	// request's own dispatch at (h.now() never advances in this test) —
	// which is deliberately NOT "before dispatch": under a frozen test
	// clock, this is what "the collector re-polled after we dispatched"
	// looks like. See TestFPPCommandDoesNotConfirmFromStalePreDispatchEvidence
	// immediately below for the negative case this positive case used to
	// be indistinguishable from (Step 7 seam C review defect 2).
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
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
		t.Errorf("outcome = %v, want \"confirmed\" (fpp.status reads \"idle\" as of evidence collected at/after dispatch)", cmd["outcome"])
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}

// TestFPPCommandDoesNotConfirmFromStalePreDispatchEvidence is Step 7 seam
// C review defect 2's own reproduction, fixed: an observation already
// reading "idle" — collected a full hour before this request ever
// dispatched — must NOT confirm the command, no matter how strongly it
// already agrees. Before this fix, checkFPPStatusOnce compared only
// o.StateAt(now) == StateCurrent && o.Value == wantValue, with no
// comparison against when the command was actually dispatched, so this
// exact scenario confirmed in 179 microseconds against a live bench fppd
// (see this task's report) — far too fast to have re-polled anything.
// This test proves the SAME shape fails to confirm early: no fresh
// evidence ever arrives, so it must wait out the full deadline and report
// unconfirmed.
func TestFPPCommandDoesNotConfirmFromStalePreDispatchEvidence(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Already "idle" — but collected a full hour before dispatch (testNow
	// is this request's own dispatch instant under the frozen clock).
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "idle", testNow.Add(-time.Hour), testNow.Add(-time.Hour)),
	})
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
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — a reading from BEFORE dispatch must never confirm, even one that "+
			"already agrees (this is exactly the false-confirm this fix closes)", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "predates dispatch") {
		t.Errorf("outcomeReason = %q, want it to say the evidence predates dispatch", reason)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("handler returned after %v, want it to have waited out the ~120ms deadline rather than confirming instantly "+
			"from stale evidence", elapsed)
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}

// TestFPPCommandConfirmsAlreadyIdleOnceFreshEvidenceArrives is defect 2's
// positive control, matching the task's own guidance: "a stop dispatched
// to an already-idle daemon is legitimate and must still resolve, but it
// must resolve on evidence that post-dates the dispatch, not on a stale
// reading that happens to agree." The seeded observation starts stale and
// pre-dispatch (exactly like the test above); a goroutine then delivers a
// FRESH "idle" reading, collected at/after dispatch, partway through the
// wait. This must confirm — proving the already-in-desired-state case
// still resolves — but only once, from the fresh evidence, not from the
// pre-existing stale one.
func TestFPPCommandConfirmsAlreadyIdleOnceFreshEvidenceArrives(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "idle", testNow.Add(-time.Hour), testNow.Add(-time.Hour)),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	go func() {
		time.Sleep(60 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
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
		t.Fatalf("outcome = %v, want \"confirmed\" once fresh, post-dispatch evidence arrives", cmd["outcome"])
	}
}

// --- Step 7 seam C review defect 3: source-blind confirmation was a coin
// flip between fpp-rest and fpp-mqtt reporting fpp.status for the same
// resource. This package's own documented precedence rule (precedence.go)
// must decide it, deterministically, regardless of which candidate a
// caller's slice happens to list first. ---

func TestFPPCommandConfirmationUsesSourcePrecedenceRegardlessOfRowOrder(t *testing.T) {
	// Same ObservedAt on both candidates, so tier-1's "later ObservedAt
	// wins" cannot decide it either — this specifically exercises
	// preferObservation's source tie-break ("fpp-rest beats fpp-mqtt",
	// precedence.go), not merely "pick the fresher one."
	rest := fppStatusObsFromSource("bench-fpp", "idle", "fpp-rest", testNow, testNow)
	mqtt := fppStatusObsFromSource("bench-fpp", "playing", "fpp-mqtt", testNow, testNow)

	for _, tc := range []struct {
		name string
		obs  []observation.Observation
	}{
		{"fpp-rest first", []observation.Observation{rest, mqtt}},
		{"fpp-mqtt first", []observation.Observation{mqtt, rest}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
			setup := newFPPCommandTestSetup(t, fixedClock(testNow))
			setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
			setup.obs.setObs(tc.obs)
			api := New(setup.deps(), Options{
				Clock: fixedClock(testNow), Logger: testLogger(),
				FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
			})

			operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
			token := mustIssueToken(t, setup.svc, operator.ID)

			req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-"+tc.name), token)
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
			m := decodeMap(t, body)
			cmd, _ := m["command"].(map[string]any)
			// fpp-rest reads "idle" (correct); fpp-mqtt reads "playing"
			// (stale/wrong). Regardless of row order, fpp-rest must win —
			// so this must ALWAYS confirm, never depend on which
			// candidate the test listed first.
			if cmd["outcome"] != "confirmed" {
				t.Errorf("outcome = %v, want \"confirmed\" (fpp-rest, the higher-precedence source, reads \"idle\")", cmd["outcome"])
			}
			reason, _ := cmd["outcomeReason"].(string)
			_ = reason // confirmed case carries no reason; nothing to assert here beyond outcome.
		})
	}
}

func TestFPPCommandConfirmationReasonNamesTheDecidingSource(t *testing.T) {
	// The MISMATCH case: fpp-rest (higher precedence) reads "playing"
	// (wrong); fpp-mqtt reads "idle" (would be right, but must NOT be
	// trusted over fpp-rest). Must resolve unconfirmed, on fpp-rest's
	// evidence, and the reason must name which source decided it
	// (ADR-011: evidence carries provenance).
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{
		fppStatusObsFromSource("bench-fpp", "playing", "fpp-rest", testNow, testNow),
		fppStatusObsFromSource("bench-fpp", "idle", "fpp-mqtt", testNow, testNow),
	})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
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
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — the higher-precedence source (fpp-rest) reads \"playing\", and "+
			"the lower-precedence source's \"idle\" must NOT be allowed to confirm falsely", cmd["outcome"])
	}
	reason, _ := cmd["outcomeReason"].(string)
	if !strings.Contains(reason, "fpp-rest") {
		t.Errorf("outcomeReason = %q, want it to name fpp-rest as the deciding source (ADR-011: evidence carries provenance)", reason)
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
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow, testNow)})
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

// TestFPPCommandUnconfirmedWithNoObservationHasReason is the "no-observation"
// path of evaluateFPPStatusEvidence: no fpp.status observation exists for
// this instance at all (never polled, or polled by no collector this
// coordinator has). v1.FPPCommandResult.outcomeReason's own doc comment and
// api/openapi.yaml both claim outcomeReason is non-empty whenever the
// outcome is not confirmed — this is one of three paths (the other two are
// TestFPPCommandUnconfirmedWithStaleUnknownAgeEvidenceHasReason and the
// dispatch-failure tests below) that were previously unprotected: only the
// "evidence is current but shows the wrong value" case
// (TestFPPCommandDispatchedButStateNeverMovesIsUnconfirmed) asserted a
// reason at all.
func TestFPPCommandUnconfirmedWithNoObservationHasReason(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// No observations seeded at all: setup.obs starts empty.
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
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
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — no fpp.status observation exists at all", cmd["outcome"])
	}
	if cmd["outcomeState"] != string(observation.StateNotCollected) {
		t.Errorf("outcomeState = %v, want %q", cmd["outcomeState"], observation.StateNotCollected)
	}
	reason, _ := cmd["outcomeReason"].(string)
	if reason == "" {
		t.Fatalf("outcomeReason is empty, want a non-empty, informative reason (ADR-020: absent evidence is stated, never omitted)")
	}
	if !strings.Contains(reason, "no fpp.status observation") {
		t.Errorf("outcomeReason = %q, want it to say no fpp.status observation is recorded", reason)
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}

// TestFPPCommandUnconfirmedWithStaleUnknownAgeEvidenceHasReason is the
// "stale/unknown-age" path: a value is present, collected after dispatch
// (so it passes the pre-dispatch fence), but its own observation time is
// genuinely unknown (the retained-MQTT case) — evaluateFPPStatusEvidence's
// o.StateAt(now) resolves to StateUnknownAge, never StateCurrent, and the
// generic `default` branch (fmt.Sprintf("fpp.status evidence state is %s",
// state)) is what must fire here, since o.Reason itself is empty for this
// shape of observation.
func TestFPPCommandUnconfirmedWithStaleUnknownAgeEvidenceHasReason(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObsUnknownAge("bench-fpp", "idle", testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
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
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — fpp.status's own observation time is unknown, which must never confirm even though Value already reads \"idle\"", cmd["outcome"])
	}
	if cmd["outcomeState"] != string(observation.StateUnknownAge) {
		t.Errorf("outcomeState = %v, want %q", cmd["outcomeState"], observation.StateUnknownAge)
	}
	reason, _ := cmd["outcomeReason"].(string)
	if reason == "" {
		t.Fatalf("outcomeReason is empty, want a non-empty, informative reason (ADR-020: absent evidence is stated, never omitted)")
	}
	if !strings.Contains(reason, "unknown_age") {
		t.Errorf("outcomeReason = %q, want it to name the unknown_age state", reason)
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}

// --- Finding 7: the dispatch-failure path had NO test at all, and could
// be made to report success by flipping `confirmed = true` on
// dispatchErr != nil without any test noticing. This sits directly on the
// ADR-003 acceptance criterion and covers the single most likely real
// failure: the FPP host is down or answering an error. ---

// TestFPPCommandDispatchFailureUnconfirmedWithTransportReason covers FPP
// being entirely unreachable: the fake server is closed before this
// handler ever dispatches, so client.StopPlaylist's own HTTP request fails
// with a transport-level error (connection refused), never a status code
// at all.
func TestFPPCommandDispatchFailureUnconfirmedWithTransportReason(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	closedURL := fppSrv.URL
	fppSrv.Close() // closed BEFORE any request — the endpoint is configured but unreachable.

	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: closedURL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the REQUEST succeeded; only the COMMAND's dispatch failed); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — FPP is unreachable, so dispatch itself failed and nothing was ever asked of it", cmd["outcome"])
	}
	if cmd["outcomeState"] != string(observation.StateCollectionFailed) {
		t.Errorf("outcomeState = %v, want %q", cmd["outcomeState"], observation.StateCollectionFailed)
	}
	reason, _ := cmd["outcomeReason"].(string)
	if reason == "" {
		t.Fatalf("outcomeReason is empty, want a reason naming the transport error")
	}
	if !strings.Contains(reason, "dispatching to FPP failed") {
		t.Errorf("outcomeReason = %q, want it to say dispatching to FPP failed", reason)
	}
	if cmd["dispatchedAt"] == nil {
		t.Errorf("dispatchedAt = %v, want a timestamp — dispatch WAS attempted (the client made a real HTTP request that failed), unlike TestFPPCommandDispatchNeverAttemptedLeavesDispatchedAtNull where fppcommand.New itself refuses to build a client", cmd["dispatchedAt"])
	}
}

// TestFPPCommandDispatchFailureNonSuccessStatusReportsTransportReason
// covers FPP answering with a real HTTP response that is not 2xx:
// fppcommand.Client.StopPlaylist reports this as an error (see that
// package's httpStatusError), so this handler's dispatchErr != nil branch
// fires exactly as it does for the connection-refused case above — the
// difference being dispatchedAt IS set here, since a real round-trip
// happened; only the connection-refused case in the test above ever leaves
// it meaningfully distinct (dispatch attempted vs. dispatch never even
// reaching fppcommand.New). Both must end unconfirmed with a stated
// reason naming the transport error.
func TestFPPCommandDispatchFailureNonSuccessStatusReportsTransportReason(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusInternalServerError, "internal error")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
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
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — a 500 from FPP must never report success", cmd["outcome"])
	}
	if cmd["outcomeState"] != string(observation.StateCollectionFailed) {
		t.Errorf("outcomeState = %v, want %q", cmd["outcomeState"], observation.StateCollectionFailed)
	}
	reason, _ := cmd["outcomeReason"].(string)
	if reason == "" {
		t.Fatalf("outcomeReason is empty, want a reason naming the transport error")
	}
	if !strings.Contains(reason, "dispatching to FPP failed") {
		t.Errorf("outcomeReason = %q, want it to say dispatching to FPP failed", reason)
	}
	if cmd["dispatchedAt"] == nil {
		t.Errorf("dispatchedAt = %v, want a timestamp — a real HTTP round-trip DID happen, even though FPP answered 500", cmd["dispatchedAt"])
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
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
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	go func() {
		time.Sleep(60 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
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
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

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

// --- ADR-024 decision 11's fail-closed default, corrected: Step 8 had
// inherited Step 7's ONE safety-class exemption (stopPlaylist) onto all
// eight primitives with no review, so an audit-write failure let
// startPlaylist — which makes the show DO something — proceed with an
// unaccountable actor exactly like a genuine stop. startPlaylist is not a
// member of [fppSafetyClass]'s exempt set (see fppcommand_primitives.go),
// so it must now fail CLOSED: refused, nothing dispatched, no commands
// row, no desired_state row. ---

func TestFPPCommandNonSafetyClassPrimitiveFailsClosedWithAuditFailing(t *testing.T) {
	fppSrv := newFailIfHitFPPCommandServer(t)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Idle at request time: startPlaylist's own PreDispatchCheck (ifBusy's
	// "refuse" default) must clear BEFORE this test ever reaches the audit
	// write it exists to fail — "nothing is playing" is never busy,
	// regardless of ifBusy, and needs no fpp.playlist.name evidence at all.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// Installed AFTER principal/token creation, exactly like
	// TestFPPCommandSucceedsWithAuditStoreFailing: neither CreatePrincipal
	// nor IssueToken writes to audit_log, so this is the exact moment every
	// SUBSEQUENT audit write starts failing — including this request's own
	// pre-dispatch write.
	installFailAuditTrigger(t, setup.storeDir)

	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (ADR-024 decision 11's fail-closed default: startPlaylist is not a member of "+
			"the blackout/stop/power-off safety class); body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	detail, _ := m["detail"].(string)
	if !strings.Contains(strings.ToLower(detail), "audit") {
		t.Errorf("problem detail = %q, want it to name the audit store as the refusal's cause", detail)
	}
	if !strings.Contains(detail, "startPlaylist") {
		t.Errorf("problem detail = %q, want it to name the refused action (startPlaylist)", detail)
	}

	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("commands rows = %d, want 0 — a fail-closed refusal must not create a commands row (the whole "+
			"transaction already rolled back per identity.ErrAuditWrite's own guarantee)", len(rows))
	}

	desired, err := setup.st.ListDesiredState(context.Background(), store.DesiredStateFilter{ResourceID: "bench-fpp"})
	if err != nil {
		t.Fatalf("list desired state: %v", err)
	}
	if len(desired) != 0 {
		t.Errorf("desired_state rows = %d, want 0 — a fail-closed refusal must not record desired state either", len(desired))
	}
}

// auditWriteAlwaysFailsSpy wraps a real identity.Service, forcing every
// [identity.Service.WriteAudit] call to fail while leaving
// [identity.Service.AuditedWrite] (the pre-dispatch, transactional write)
// delegated unchanged to the real implementation. This isolates "the
// POST-dispatch outcome/replay audit write failed" from "the PRE-dispatch
// write failed": [installFailAuditTrigger]'s real SQLite trigger cannot
// make that distinction on its own, since it fails every audit_log insert
// unconditionally — including the one inside AuditedWrite's own
// transaction — which would make a non-safety-class primitive fail closed
// before ever reaching dispatch, the wrong scenario for a test whose whole
// point is to prove the OUTCOME entry degrades once a command has already
// been dispatched. [identity.Service.AuditedWrite] never calls
// [identity.Service.WriteAudit] internally (it appends the audit row
// directly inside its own store.Store.InTx transaction — see
// internal/coordinator/identity/audit.go), so overriding only WriteAudit
// here is sufficient to leave the pre-dispatch path genuinely healthy.
type auditWriteAlwaysFailsSpy struct {
	identity.Service
}

func (s *auditWriteAlwaysFailsSpy) WriteAudit(context.Context, identity.AuditEntry) error {
	return errors.New("injected post-dispatch audit failure")
}

// TestFPPCommandPostDispatchOutcomeAuditDegradesForNonSafetyClassPrimitive
// is this task's own point 4: once a command has been dispatched,
// refusing to answer cannot undo the dispatch — it can only hide the
// record of it from the operator, which ADR-024 treats as "you cannot
// see" and never accepts, unlike a pre-dispatch refusal ("you cannot
// act"), which is fine. So the post-dispatch OUTCOME audit entry must
// degrade rather than refuse even for startPlaylist — a
// fppSafetyClassNotExempt primitive that DOES fail closed on its
// PRE-dispatch write, per
// TestFPPCommandNonSafetyClassPrimitiveFailsClosedWithAuditFailing right
// above. Proving both halves against the SAME primitive is the point:
// which rule applies depends on WHEN the audit store fails relative to
// dispatch, never on the primitive's [fppPrimitive.SafetyClass] alone.
func TestFPPCommandPostDispatchOutcomeAuditDegradesForNonSafetyClassPrimitive(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	deps := setup.deps()
	deps.Identity = &auditWriteAlwaysFailsSpy{Service: setup.svc}

	go func() {
		time.Sleep(60 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{
			fppStatusObs("bench-fpp", "playing", testNow, testNow),
			fppPlaylistNameObs("bench-fpp", "showmesh-test", testNow, testNow),
		})
	}()

	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 2 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	body := fppCommandBody("startPlaylist", "key-1", `{"playlist":"showmesh-test"}`)
	req := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — the PRE-dispatch write already succeeded (AuditedWrite is untouched by "+
			"this spy); an outcome entry that degrades must never turn into a refused response; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Errorf("outcome = %v, want \"confirmed\"", cmd["outcome"])
	}
	if cmd["attributionDegraded"] != true {
		t.Errorf("attributionDegraded = %v, want true — the post-dispatch outcome audit entry failed to write", cmd["attributionDegraded"])
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1 — the command must actually dispatch, exactly as its "+
			"PRE-dispatch write (which succeeded) already committed to", srv.hitCount())
	}
}

// --- Acceptance criterion 4 & 5: a replayed idempotency key dispatches
// nothing and returns the original result; dispatch and outcome are
// separate, correlated audit entries. ---

func TestFPPCommandReplayDispatchesNothingAndReturnsOriginalResult(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Deliberately UNCONFIRMED, not confirmed: fpp.status stays "playing"
	// for the whole confirmation window, so the original command's own
	// outcome carries a distinctive, non-constant reason
	// ("observed fpp.status = playing ..."). A prior version of this test
	// seeded an always-confirmed original, so hardcoding the replay path's
	// decode to the fabricated constants confirmed/current/"" stayed green
	// — this shape makes hardcoding those three constants fail instead,
	// because "confirmed"/"current"/"" is not what the original actually
	// produced.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 120 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
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
	if cmd1["outcome"] != "unconfirmed" {
		t.Fatalf("original outcome = %v, want \"unconfirmed\" (this test's own setup: fpp.status never reaches \"idle\")", cmd1["outcome"])
	}
	origReason, _ := cmd1["outcomeReason"].(string)
	if origReason == "" || !strings.Contains(origReason, "playing") {
		t.Fatalf("original outcomeReason = %q, want a distinctive reason naming the observed \"playing\" value", origReason)
	}

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
	if cmd2["outcomeState"] != cmd1["outcomeState"] {
		t.Errorf("replay outcomeState = %v, want the original result %v", cmd2["outcomeState"], cmd1["outcomeState"])
	}
	replayReason, _ := cmd2["outcomeReason"].(string)
	if replayReason != origReason {
		t.Errorf("replay outcomeReason = %q, want it to round-trip the original's reason %q verbatim", replayReason, origReason)
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

// TestFPPCommandFreshAndReplayedTimestampsRenderIdentically is Step 8
// review finding 14's own reproduction, fixed: a fresh dispatch's
// dispatchedAt/resolvedAt come from h.now() (fixedClock(testNow) here,
// whose own testNow — fixtures_test.go — is deliberately anchored at a
// -05:00 offset, matching contract section 6.3's own pinned example),
// while a REPLAY's dispatchedAt/resolvedAt are decoded from the STORE's
// own round trip — store/queries.go's timeToDB/dbToTime deliberately
// normalize every persisted timestamp to UTC ("this package owns the
// format itself"). Before this fix, formatTimePtr(dispatchedAt) and
// formatTime(resolvedAt) on the FRESH path rendered testNow's own -05:00
// offset verbatim, while the REPLAY path — reading the same underlying
// instant back out of the store — rendered "...Z". Both are valid RFC
// 3339 individually, but a client comparing the two strings for the SAME
// underlying instant saw a difference for no reason connected to the
// command itself. This asserts the fresh response's own dispatchedAt/
// resolvedAt already render with a "Z" suffix (matching what a replay of
// the SAME command will render), rather than testNow's own configured
// -05:00 offset.
func TestFPPCommandFreshAndReplayedTimestampsRenderIdentically(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 60 * time.Millisecond, FPPCommandPollInterval: 5 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	first := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("ts-key"), token)
	resp1, body1 := doRawRequest(t, api.Handler, first)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests, want 1", srv.hitCount())
	}
	m1 := decodeMap(t, body1)
	cmd1, _ := m1["command"].(map[string]any)
	freshDispatchedAt, _ := cmd1["dispatchedAt"].(string)
	freshResolvedAt, _ := cmd1["resolvedAt"].(string)
	if freshDispatchedAt == "" || freshResolvedAt == "" {
		t.Fatalf("fresh dispatchedAt/resolvedAt unexpectedly empty: %q / %q", freshDispatchedAt, freshResolvedAt)
	}
	if !strings.HasSuffix(freshDispatchedAt, "Z") {
		t.Errorf("fresh dispatchedAt = %q, want a \"Z\" (UTC) suffix, matching what this SAME command's own replay "+
			"will render from the store's round trip — testNow's own -05:00 offset leaking through here is finding "+
			"14's own reproduction", freshDispatchedAt)
	}
	if !strings.HasSuffix(freshResolvedAt, "Z") {
		t.Errorf("fresh resolvedAt = %q, want a \"Z\" (UTC) suffix, for the identical reason", freshResolvedAt)
	}

	second := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("ts-key"), token)
	resp2, body2 := doRawRequest(t, api.Handler, second)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	cmd2, _ := m2["command"].(map[string]any)
	replayDispatchedAt, _ := cmd2["dispatchedAt"].(string)
	replayResolvedAt, _ := cmd2["resolvedAt"].(string)

	if replayDispatchedAt != freshDispatchedAt {
		t.Errorf("replay dispatchedAt = %q, want it to render BYTE-IDENTICAL to the original response's own %q for "+
			"the same underlying instant", replayDispatchedAt, freshDispatchedAt)
	}
	if replayResolvedAt != freshResolvedAt {
		t.Errorf("replay resolvedAt = %q, want it to render BYTE-IDENTICAL to the original response's own %q for "+
			"the same underlying instant", replayResolvedAt, freshResolvedAt)
	}
}

// TestFPPCommandReplayConflictWhenTargetDiffers is Step 7 seam C review
// defect 6's own reproduction, fixed: replaying "key" (originally
// dispatched against fpp-garage) against a DIFFERENT instanceId
// (fpp-roof) used to answer instanceId: "fpp-roof" (the REQUEST's own
// path value) with fpp-garage's stored command id and outcome — a false
// statement that fpp-roof was ever touched. Now refused as a 409
// conflict, naming both the original and the requested target, and
// dispatching nothing either way.
func TestFPPCommandReplayConflictWhenTargetDiffers(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{
		{InstanceID: "fpp-garage", Endpoint: fppSrv.URL},
		{InstanceID: "fpp-roof", Endpoint: fppSrv.URL},
	}
	setup.obs.setObs([]observation.Observation{fppStatusObs("fpp-garage", "idle", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	first := newFPPCommandRequest(t, "fpp-garage", stopPlaylistBody("shared-key"), token)
	resp1, body1 := doRawRequest(t, api.Handler, first)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the first dispatch, want 1", srv.hitCount())
	}
	m1 := decodeMap(t, body1)
	cmd1, _ := m1["command"].(map[string]any)

	second := newFPPCommandRequest(t, "fpp-roof", stopPlaylistBody("shared-key"), token)
	resp2, body2 := doRawRequest(t, api.Handler, second)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("replay-against-different-target status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the conflicting replay, want still 1 — a conflict must dispatch nothing", srv.hitCount())
	}
	m2 := decodeMap(t, body2)
	detail, _ := m2["detail"].(string)
	if !strings.Contains(detail, "fpp-garage") || !strings.Contains(detail, "fpp-roof") {
		t.Errorf("problem detail = %q, want it to name both the original target (fpp-garage) and the requested one (fpp-roof)", detail)
	}
	if !strings.Contains(detail, cmd1["id"].(string)) {
		t.Errorf("problem detail = %q, want it to name the original command id %v", detail, cmd1["id"])
	}
	// And nothing about fpp-roof was ever claimed confirmed/dispatched —
	// the response must not even superficially resemble a successful
	// command result for fpp-roof.
	if _, ok := m2["command"]; ok {
		t.Errorf("conflict response unexpectedly carries a \"command\" object: %v", m2["command"])
	}
}

// TestFPPCommandReplayRecognizedBeforePreDispatchGuardRuns is Step 8
// review finding 4's own reproduction, fixed: dispatching
// `startPlaylist holiday-show` with key `key-replay` while idle
// dispatches once (an FPP hit, 200) and inserts a commands row under that
// key; if FPP's own scheduler then moves on to a DIFFERENT show before
// the SAME key is resent with the SAME body, startPlaylist's own ifBusy
// guard (the default, "refuse") would refuse a genuinely NEW request in
// that situation — but this is not a new request, it is a replay, and a
// replay must dispatch nothing and return the ORIGINAL result regardless
// of what the guard would decide about the CURRENT state. Before this
// fix, [handlers.handleFPPCommand] ran the guard (old step 3) before
// recognizing the replay (old step 4's insert-based detection), so the
// resend answered 409 (fpp-start-playlist-busy) instead of the documented
// replay — proved live against a real dispatch (see this task's report).
func TestFPPCommandReplayRecognizedBeforePreDispatchGuardRuns(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Starting")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Idle at dispatch time: startPlaylist's own ifBusy guard is "not
	// busy" here, so the FIRST request dispatches normally.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 30 * time.Millisecond, FPPCommandPollInterval: 5 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := fppCommandBody("startPlaylist", "key-replay", `{"playlist":"holiday-show"}`)

	first := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp1, body1 := doRawRequest(t, api.Handler, first)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the first dispatch, want 1", srv.hitCount())
	}
	m1 := decodeMap(t, body1)
	cmd1, _ := m1["command"].(map[string]any)

	// FPP's own scheduler moves on to a DIFFERENT show — exactly what the
	// client never seeing the first response, then FPP itself advancing,
	// looks like from the coordinator's own evidence.
	setup.obs.setObs([]observation.Observation{
		fppStatusObs("bench-fpp", "playing", testNow, testNow),
		fppPlaylistNameObs("bench-fpp", "some-other-show", testNow, testNow),
	})

	// Resend the BYTE-IDENTICAL body and key.
	second := newFPPCommandRequest(t, "bench-fpp", body, token)
	resp2, body2 := doRawRequest(t, api.Handler, second)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (a replay, never a fresh 409 from the ifBusy guard); body: %s", resp2.StatusCode, body2)
	}
	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests after the replay, want still 1 — a replay must dispatch nothing, and the "+
			"guard must never even run for it", srv.hitCount())
	}
	m2 := decodeMap(t, body2)
	cmd2, _ := m2["command"].(map[string]any)
	if cmd2["replay"] != true {
		t.Errorf("replay = %v, want true", cmd2["replay"])
	}
	if cmd2["id"] != cmd1["id"] {
		t.Errorf("replay id = %v, want the original command's id %v", cmd2["id"], cmd1["id"])
	}
}

// TestFPPCommandDispatchNeverAttemptedLeavesDispatchedAtNull is Step 7
// seam C review defect 9's own reproduction, fixed: when
// internal/coordinator/fppcommand.New itself fails (never an HTTP
// request), dispatchedAt must be null, honoring
// v1.FPPCommandResult.DispatchedAt's own documented "null only if dispatch
// itself could not be attempted." Forced here via a configured Endpoint
// fppcommand.New rejects outright (a URL with a path component).
func TestFPPCommandDispatchNeverAttemptedLeavesDispatchedAtNull(t *testing.T) {
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	// fppcommand.New refuses any URL carrying a path beyond a bare
	// trailing slash — see that package's own New doc comment — so this
	// never reaches the network at all.
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: "http://127.0.0.1:1/some/path"}}
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 100 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the REQUEST succeeded; the COMMAND's dispatch is what never happened); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["dispatchedAt"] != nil {
		t.Errorf("dispatchedAt = %v, want null — dispatch was never attempted (fppcommand.New itself failed)", cmd["dispatchedAt"])
	}
	if cmd["outcomeState"] != string(observation.StateCollectionFailed) {
		t.Errorf("outcomeState = %v, want %q", cmd["outcomeState"], observation.StateCollectionFailed)
	}
}

// TestFPPCommandOutcomeSurvivesClientDisconnect is Step 7 seam C review
// defect 4's own reproduction, fixed: this handler's post-dispatch
// bookkeeping (recording dispatch, confirming by evidence, recording the
// outcome, and writing the outcome audit entry) must not be cancellable by
// a client that walks away mid-request. Before this fix, all four were
// bound to r.Context(): UpdateCommandOutcome failed with "context
// canceled" (logged only, leaving the commands row permanently
// state='dispatched' with no outcome), and the outcome audit entry was
// lost outright. This test cancels the REQUEST's own context shortly
// after dispatch (simulating an abandoned browser tab) and, despite that,
// asserts the command still resolves to "confirmed" in the store, with a
// real outcome audit entry — the fix's whole point.
func TestFPPCommandOutcomeSurvivesClientDisconnect(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// Evidence arrives AFTER the client disconnects — proving the
	// confirmation wait itself keeps running server-side, not merely that
	// an already-decided outcome gets recorded.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "playing", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 1 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	go func() {
		time.Sleep(150 * time.Millisecond)
		setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
	}()

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	ctx, cancel := context.WithCancel(context.Background())
	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token).WithContext(ctx)

	// Cancel the REQUEST's own context shortly after dispatch (the fake
	// FPP server answers instantly) but well before the 150ms fresh
	// evidence arrives — simulating the client closing its tab mid-wait.
	go func() {
		time.Sleep(40 * time.Millisecond)
		cancel()
	}()

	rec := httptest.NewRecorder()
	api.Handler.ServeHTTP(rec, req)
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (ServeHTTP still completes and writes a response even though the REQUEST's own "+
			"context was canceled mid-wait); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Fatalf("outcome = %v, want \"confirmed\" — the client's own disconnect must not abort the server-side "+
			"confirmation wait; body: %s", cmd["outcome"], body)
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}

	// The store itself, independent of what the (possibly-abandoned)
	// response says, must show a fully resolved command — never stuck at
	// state='dispatched' the way defect 4 left it.
	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d commands, want 1", len(rows))
	}
	if rows[0].State != "resolved" {
		t.Errorf("commands row state = %q, want \"resolved\" (not stuck at \"dispatched\" — defect 4's exact failure mode)", rows[0].State)
	}
	if rows[0].ResolvedAt == nil {
		t.Errorf("commands row resolved_at is nil, want it set")
	}

	// And the outcome audit entry itself was not lost — defect 4's other
	// half.
	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var outcomeCount int
	for _, e := range entries {
		if e.CommandID == rows[0].ID && e.Kind == identity.AuditOutcome {
			outcomeCount++
		}
	}
	if outcomeCount != 1 {
		t.Errorf("outcome audit entries = %d, want exactly 1 (defect 4: this used to be silently lost on a canceled context)", outcomeCount)
	}
}

// TestFPPCommandDispatchSurvivesClientDisconnect is Step 8 review finding
// 14's own reproduction, fixed: defect 4's original fix severed
// r.Context()'s cancellation from POST-dispatch bookkeeping only, but left
// the DISPATCH ATTEMPT ITSELF (primitive.Dispatch, and CaptureBaseline)
// bound to r.Context() — so a client that disconnected while FPP was slow
// could abort the outbound command mid-flight, in the worst case a stop.
//
// The fake FPP server here blocks on a channel after recording the hit but
// before writing its response, and the client's own context is canceled
// only once that channel confirms the request has genuinely reached FPP
// (never a sleep racing real wall-clock time to REACH that point — the
// same channel-ordering discipline newFakeFPPCommandServerWithSideEffect
// uses one file over, for the identical "do not race a kernel" reason).
// This also guarantees the cancellation lands AFTER authentication and
// every pre-dispatch step have already run (all of which also read
// r.Context()), so this test isolates exactly the one thing finding 14 is
// about: the dispatch attempt itself.
//
// The assertion itself is deliberately a BOUNDED "did not finish yet"
// check, not a hit-count check taken immediately after canceling: closing
// a local context's Done channel is near-instantaneous, so if dispatch
// were still bound to r.Context() (the pre-fix shape), Go's own
// http.Transport tears down the pending round trip as soon as it observes
// the cancellation — well before this test chooses to unblock the fake
// server — and ServeHTTP returns almost immediately with a transport
// error, with NO further wait for FPP. Racing "cancel, then immediately
// release and see what comes back" against that teardown would be exactly
// the kernel-scheduling coin flip CLAUDE.md's own Step 4 lesson warns
// against (a response that already started arriving can beat a
// cancellation propagating through the transport, or not, depending on
// how the two goroutines happen to be scheduled). Waiting up to
// dispatchStillInFlightWindow for `done` to fire BEFORE releasing the
// server sidesteps that race entirely: under the fix, ServeHTTP is
// GENUINELY still blocked waiting on FPP's response (bgCtx is immune to
// ctx's cancellation, so nothing tears the round trip down), and stays
// blocked for the whole window, deterministically; only the broken,
// pre-fix shape can make `done` fire during it. This was verified against
// the pre-fix code (ctx instead of bgCtx in both dispatch-path calls),
// which fails this test's "still in flight" assertion consistently — see
// this task's report.
func TestFPPCommandDispatchSurvivesClientDisconnect(t *testing.T) {
	const dispatchStillInFlightWindow = 200 * time.Millisecond

	received := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	// releaseFPP unblocks the fake server's handler goroutine below.
	// Guarded by sync.Once and deferred unconditionally (not just on the
	// success path) so a t.Fatalf on the "already returned too early"
	// branch below — which halts THIS goroutine via runtime.Goexit,
	// running deferred calls first — still releases it, rather than
	// leaving that goroutine permanently blocked on <-release and
	// deadlocking t.Cleanup(fppSrv.Close) (Close waits for every
	// outstanding request to finish).
	releaseFPP := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseFPP()

	var mu sync.Mutex
	hitCount := 0
	fppSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hitCount++
		mu.Unlock()
		close(received)
		<-release
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("Stopped"))
	}))
	t.Cleanup(fppSrv.Close)

	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
	api := New(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 1 * time.Second, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	ctx, cancel := context.WithCancel(context.Background())
	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token).WithContext(ctx)

	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		api.Handler.ServeHTTP(rec, req)
		close(done)
	}()

	<-received // the dispatch attempt has genuinely reached the fake FPP server
	cancel()   // simulate the client disconnecting WHILE FPP is slow to answer

	select {
	case <-done:
		t.Fatalf("ServeHTTP returned within %s of the client disconnecting, before FPP was ever allowed to answer — "+
			"the dispatch attempt itself was aborted by the client's own cancellation (finding 14)", dispatchStillInFlightWindow)
	case <-time.After(dispatchStillInFlightWindow):
		// Still in flight, as the fix requires — now let FPP answer.
	}

	releaseFPP()
	<-done
	resp := rec.Result()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a client disconnect mid-dispatch must not abort the dispatch attempt "+
			"itself, or the response built from it); body: %s", resp.StatusCode, body)
	}
	mu.Lock()
	got := hitCount
	mu.Unlock()
	if got != 1 {
		t.Fatalf("FPP received %d requests, want exactly 1 — the in-flight dispatch must be allowed to run to its "+
			"own conclusion despite the client disconnect (finding 14)", got)
	}
}

// auditedWriteTimingSpy wraps a real identity.Service and records the real
// wall-clock instant ([time.Now], never h.now()) at which AuditedWrite
// RETURNS — i.e. once its transaction (the commands row, desired_state row,
// and dispatch audit entry) has actually committed. Comparing this against
// [fakeFPPCommandServer.firstRequestTime] (also real wall-clock) is what
// lets TestFPPCommandWritesDesiredStateAndDispatchAuditBeforeDispatchingToFPP
// prove ACTUAL execution order rather than comparing a data field
// ([identity.AuditEntry.Timestamp]) that is stamped once at the very top
// of the handler regardless of what order the rest of the function later
// executes in — a first attempt at this test compared that field and kept
// passing even after the handler's dispatch and write steps were
// physically reordered in the source, because the field's VALUE never
// moved even though the WRITE that persists it did.
type auditedWriteTimingSpy struct {
	identity.Service
	mu         sync.Mutex
	returnedAt time.Time
}

func (s *auditedWriteTimingSpy) AuditedWrite(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error)) error {
	err := s.Service.AuditedWrite(ctx, fn)
	s.mu.Lock()
	s.returnedAt = time.Now()
	s.mu.Unlock()
	return err
}

func (s *auditedWriteTimingSpy) auditedWriteReturnedAt() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.returnedAt
}

// TestFPPCommandWritesDesiredStateAndDispatchAuditBeforeDispatchingToFPP is
// finding 10's own regression test for ADR-024 decision 11's
// write-before-dispatch rule: insert the commands row, record desired
// state, and write the DISPATCH audit entry BEFORE dispatching to FPP —
// never after. TestFPPCommandDispatchAndAuditAreAtomic already proves all
// three artifacts exist together once a request completes, but proves
// nothing about ORDER relative to the actual network call: a handler that
// dispatched to FPP first and only recorded these three afterward would
// still pass that test, since by the time the response is inspected every
// write has already happened either way.
func TestFPPCommandWritesDesiredStateAndDispatchAuditBeforeDispatchingToFPP(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, time.Now)
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	spy := &auditedWriteTimingSpy{Service: setup.svc}
	deps := setup.deps()
	deps.Identity = spy
	// No observation seeded: the confirmation loop resolves unconfirmed
	// quickly via the no-observation path.
	api := New(deps, Options{
		FPPCommandConfirmDeadline: 80 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
		Logger: testLogger(),
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("order-key"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	cmdID, _ := cmd["id"].(string)

	if srv.hitCount() != 1 {
		t.Fatalf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
	requestTime := srv.firstRequestTime()
	if requestTime.IsZero() {
		t.Fatalf("fake FPP server recorded no request time despite hitCount() == 1")
	}
	writeReturnedAt := spy.auditedWriteReturnedAt()
	if writeReturnedAt.IsZero() {
		t.Fatalf("AuditedWrite spy recorded no return time")
	}

	// 1. desired_state holds the expected row, correlated by command id.
	desired, err := setup.st.GetDesiredState(context.Background(), "fpp", "bench-fpp", fppStatusSignal)
	if err != nil {
		t.Fatalf("get desired state: %v", err)
	}
	if desired.CommandID != cmdID {
		t.Errorf("desired_state.command_id = %q, want %q", desired.CommandID, cmdID)
	}

	// 2. AuditedWrite (which commits the commands row, desired_state row,
	// and the dispatch audit entry together) must have RETURNED before FPP
	// was ever contacted — never after.
	if writeReturnedAt.After(requestTime) {
		t.Errorf("AuditedWrite returned at %s, AFTER the FPP request at %s — want it to precede dispatch "+
			"(ADR-024 decision 11: write before dispatch, never dispatch before write)", writeReturnedAt, requestTime)
	}
}

// TestFPPCommandDispatchAndAuditAreAtomic is Step 7 seam C review defect
// 8's own proof: on a HEALTHY audit store, the commands row, its
// desired_state row, and its DISPATCH audit entry all come into existence
// together, via store.Tx.InsertCommand/SetDesiredState and one
// AuditedWrite transaction — never the pre-fix shape (a separate
// Store.InsertCommand followed by a separate, non-transactional
// WriteAudit) where a crash between the two could leave a commands row
// with no audit entry at all. This does not (and structurally cannot, in
// a unit test) prove atomicity under an actual crash — that guarantee
// comes from store.Store.InTx itself, already covered by
// store/tx_test.go's TestInTxRollsBackOnError/TestInTxRollsBackOnPanic —
// this test proves this HANDLER actually routes through that mechanism
// for all three writes, rather than the old separate-calls shape, by
// checking every artifact a healthy dispatch should leave behind.
func TestFPPCommandDispatchAndAuditAreAtomic(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
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
	if cmd["attributionDegraded"] != false {
		t.Fatalf("attributionDegraded = %v, want false (the audit store is healthy in this test)", cmd["attributionDegraded"])
	}

	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d commands, want 1", len(rows))
	}

	desired, err := setup.st.GetDesiredState(context.Background(), "fpp", "bench-fpp", fppStatusSignal)
	if err != nil {
		t.Fatalf("get desired state: %v", err)
	}
	if desired.CommandID != rows[0].ID {
		t.Errorf("desired_state.command_id = %q, want %q", desired.CommandID, rows[0].ID)
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var dispatchCount, outcomeCount int
	for _, e := range entries {
		if e.CommandID != rows[0].ID {
			continue
		}
		switch e.Kind {
		case identity.AuditDispatch:
			dispatchCount++
			// Assert the dispatch entry's own content, not merely that one
			// exists: a garbage action string, a wrong target, or a dropped
			// principal/form/credential/client address would all leave
			// dispatchCount == 1 while the entry itself is unattributed.
			if e.Action != "fpp.stop_playlist" {
				t.Errorf("dispatch entry Action = %q, want %q", e.Action, "fpp.stop_playlist")
			}
			if e.Target != "bench-fpp" {
				t.Errorf("dispatch entry Target = %q, want %q", e.Target, "bench-fpp")
			}
			if e.PrincipalID != operator.ID {
				t.Errorf("dispatch entry PrincipalID = %q, want %q", e.PrincipalID, operator.ID)
			}
			if e.PrincipalName != operator.Name {
				t.Errorf("dispatch entry PrincipalName = %q, want %q", e.PrincipalName, operator.Name)
			}
			if e.Form != identity.FormToken {
				t.Errorf("dispatch entry Form = %q, want %q", e.Form, identity.FormToken)
			}
			if e.CredentialID == "" {
				t.Errorf("dispatch entry CredentialID is empty, want the token's credential id recorded")
			}
		case identity.AuditOutcome:
			outcomeCount++
			if e.Action != "fpp.stop_playlist" {
				t.Errorf("outcome entry Action = %q, want %q", e.Action, "fpp.stop_playlist")
			}
			if e.Target != "bench-fpp" {
				t.Errorf("outcome entry Target = %q, want %q", e.Target, "bench-fpp")
			}
			if e.PrincipalID != operator.ID {
				t.Errorf("outcome entry PrincipalID = %q, want %q", e.PrincipalID, operator.ID)
			}
			if e.PrincipalName != operator.Name {
				t.Errorf("outcome entry PrincipalName = %q, want %q", e.PrincipalName, operator.Name)
			}
			if e.Form != identity.FormToken {
				t.Errorf("outcome entry Form = %q, want %q", e.Form, identity.FormToken)
			}
			if e.CredentialID == "" {
				t.Errorf("outcome entry CredentialID is empty, want the token's credential id recorded")
			}
		}
	}
	if dispatchCount != 1 {
		t.Errorf("dispatch audit entries = %d, want exactly 1 (inserted atomically with the commands row via AuditedWrite)", dispatchCount)
	}
	if outcomeCount != 1 {
		t.Errorf("outcome audit entries = %d, want exactly 1", outcomeCount)
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
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})
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

// --- The owner's 2026-08-13 post-dispatch poll nudge
// (h.deps.Nudger.NudgePoll, called from handleFPPCommand right before
// confirmFPPCommand — see that call site's own comment). These tests use a
// fake [FPPPollNudger] rather than the real
// internal/coordinator/collector.Runner: the real mechanism (scoping,
// rate-limiting, never blocking) is already proven directly by that
// package's own tests
// (TestRunnerNudgeTriggersPollForItsOwnCollectorOnly,
// TestRunnerNudgeRateLimitedPerID,
// TestRunnerNudgeReturnsImmediatelyWhileCollectorPollIsInFlight). What
// THESE tests prove is this package's own contract with whatever Nudger it
// is handed: called once, for the right instance, with its return value
// never consulted — confirmation always resolves purely from
// [ObservationLister] evidence through the unchanged notBefore fence,
// regardless of what NudgePoll answers. ---

// recordingNudger is a test-only [FPPPollNudger] that records every call
// (in order) and always answers accept, whatever that is configured to be.
// It has no connection to setup.obs at all — deliberately: a test proving
// the nudge's ACCEPTANCE never substitutes for evidence must not have any
// code path, even in the test double, that could make accepting a nudge
// change what [ObservationLister] reports.
type recordingNudger struct {
	mu     sync.Mutex
	calls  []string
	accept bool
}

func (n *recordingNudger) NudgePoll(instanceID string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, instanceID)
	return n.accept
}

func (n *recordingNudger) callsFor(instanceID string) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	count := 0
	for _, id := range n.calls {
		if id == instanceID {
			count++
		}
	}
	return count
}

func (n *recordingNudger) totalCalls() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.calls)
}

// TestFPPCommandDispatchNudgesOnlyItsOwnInstance is this task's own first
// required test: a dispatched command must trigger exactly one nudged poll
// for its own instance, and — with a second, entirely unrelated FPP
// instance configured on the same coordinator — none for that other one.
func TestFPPCommandDispatchNudgesOnlyItsOwnInstance(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	// Two configured instances; this request only ever names "bench-fpp".
	setup.fppLister.views = []FPPInstanceView{
		{InstanceID: "bench-fpp", Endpoint: fppSrv.URL},
		{InstanceID: "other-fpp", Endpoint: fppSrv.URL},
	}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	nudger := &recordingNudger{accept: true}
	deps := setup.deps()
	deps.Nudger = nudger
	api := New(deps, Options{
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

	if got := nudger.callsFor("bench-fpp"); got != 1 {
		t.Errorf("NudgePoll(\"bench-fpp\") called %d times, want exactly 1", got)
	}
	if got := nudger.callsFor("other-fpp"); got != 0 {
		t.Errorf("NudgePoll(\"other-fpp\") called %d times, want 0 — a nudge for one instance must never reach another", got)
	}
	if got := nudger.totalCalls(); got != 1 {
		t.Errorf("NudgePoll called %d times in total, want exactly 1", got)
	}
}

// TestFPPCommandNudgeRejectionDoesNotFailOrFalselyConfirmCommand is this
// task's own second and third required tests together: a nudge that the
// Nudger declines (modeling both "suppressed by the rate limit" and
// "errored" — [FPPPollNudger]'s own doc comment treats every non-accepted
// outcome identically) must neither fail the request nor prevent it from
// confirming normally through ordinary evidence. The seeded observation
// already reads the desired value, collected at/after dispatch — exactly
// TestFPPCommandAcceptedForOperator's own positive case — proving
// confirmation here comes entirely from [ObservationLister], never from
// the nudge's own return value.
func TestFPPCommandNudgeRejectionDoesNotFailOrFalselyConfirmCommand(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	nudger := &recordingNudger{accept: false} // every call declined
	deps := setup.deps()
	deps.Nudger = nudger
	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-1"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a declined nudge must never fail the request); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "confirmed" {
		t.Errorf("outcome = %v, want \"confirmed\" — a declined nudge must not stop the command confirming normally off ordinary evidence", cmd["outcome"])
	}
	if got := nudger.totalCalls(); got != 1 {
		t.Errorf("NudgePoll called %d times, want exactly 1 (the handler must still ATTEMPT the nudge even though this fake always declines it)", got)
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}

// TestFPPCommandNudgeAcceptedButEffectNeverOccurredStaysUnconfirmed is this
// task's own LOAD-BEARING test: docs/bench/fpp-command-vocabulary.md
// section 2 measured "Resume Playlist" against an already-idle fppd as a
// 200 ("Playlist Restarted") that changes nothing at all. This dispatches
// resumePlaylist with the nudge ACCEPTED (nudger.accept = true) — modeling
// "the nudge fired" — while [ObservationLister] never reports fpp.status
// moving to "playing" for the whole confirmation window, exactly as a real
// idle bench fppd would report it: the nudge changes WHEN the collector
// polls, never WHAT it finds. If accepting the nudge ever became a
// shortcut for evidence, this would report "confirmed" for an effect that
// never happened — precisely Step 7's 179-microsecond defect, rebuilt
// deliberately, and precisely what this test exists to catch.
func TestFPPCommandNudgeAcceptedButEffectNeverOccurredStaysUnconfirmed(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Playlist Restarted")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	// fpp.status stays "idle" for the entire confirmation window — capture
	// section 2/3.4's own measured behavior: Resume Playlist against an
	// idle host is a no-op 200.
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	nudger := &recordingNudger{accept: true}
	deps := setup.deps()
	deps.Nudger = nudger
	api := New(deps, Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 150 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	start := time.Now()
	req := newFPPCommandRequest(t, "bench-fpp", `{"action":"resumePlaylist","idempotencyKey":"key-1"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	elapsed := time.Since(start)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	cmd, _ := m["command"].(map[string]any)
	if cmd["outcome"] != "unconfirmed" {
		t.Fatalf("outcome = %v, want \"unconfirmed\" — FPP answered 200 (\"Playlist Restarted\") but fpp.status never "+
			"left \"idle\"; accepting the nudge must NEVER stand in for evidence the collector never actually produced", cmd["outcome"])
	}
	if got := nudger.totalCalls(); got != 1 {
		t.Errorf("NudgePoll called %d times, want exactly 1 (the nudge must still be requested even though this scenario never confirms)", got)
	}
	if elapsed < 100*time.Millisecond {
		t.Errorf("handler returned after %v, want it to have waited out the ~150ms confirm deadline rather than confirming instantly off the accepted nudge", elapsed)
	}
	if srv.hitCount() != 1 {
		t.Errorf("FPP received %d requests, want exactly 1", srv.hitCount())
	}
}
