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

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track D seam D-3/B's own acceptance-criteria proof, driving
// a real [API] (real store.Store, real identity.Service) against a FAKE
// [ResolumeActionDispatcher] — this package's own consumer-side interface
// (resolumeaction_interfaces.go) — exactly the way fppcommand_handler_test.go
// drives a real [API] against a fake FPP endpoint. D-3/A's own real
// dispatch/confirm engine is a different, concurrently-built package this
// task deliberately does not import; this file proves the HTTP surface,
// the authorization gate, the idempotency/replay handling, and the
// ADR-024 decision 11 audit rule, all independent of whatever D-3/A's
// real implementation eventually does.

// fakeResolumeDispatchCall records one Dispatch invocation this file's own
// fake received, for a test asserting exactly which (or how many) calls
// were made — never more than what actually happened.
type fakeResolumeDispatchCall struct {
	action string
	params map[string]any
}

// fakeResolumeActionDispatcher implements [ResolumeActionDispatcher].
// results is consulted by action name; err, when set, is returned from
// EVERY call regardless of action (this file's own tests never need a
// per-action error).
type fakeResolumeActionDispatcher struct {
	mu          sync.Mutex
	descriptors []ResolumeActionDescriptor
	results     map[string]ResolumeActionResult
	err         error
	calls       []fakeResolumeDispatchCall
}

func (f *fakeResolumeActionDispatcher) Actions() []ResolumeActionDescriptor {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ResolumeActionDescriptor(nil), f.descriptors...)
}

func (f *fakeResolumeActionDispatcher) Dispatch(_ context.Context, action string, params map[string]any, _ time.Time) (ResolumeActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeResolumeDispatchCall{action: action, params: params})
	if f.err != nil {
		return ResolumeActionResult{}, f.err
	}
	return f.results[action], nil
}

func (f *fakeResolumeActionDispatcher) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeResolumeActionDispatcher) lastCall() (fakeResolumeDispatchCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return fakeResolumeDispatchCall{}, false
	}
	return f.calls[len(f.calls)-1], true
}

// standardResolumeActionDescriptors is TRACK-D-D3-SPEC.md section 2's
// seven-action vocabulary and section 5.2's safety-class table, exactly as
// this seam's own report describes them — this package's own tests do not
// depend on D-3/A's real registry to exist; they fix this vocabulary
// themselves, matching it against the spec directly.
func standardResolumeActionDescriptors() []ResolumeActionDescriptor {
	idParam := ResolumeActionParam{Name: "id", Kind: ResolumeActionParamString, Required: true}
	return []ResolumeActionDescriptor{
		{Name: "launchClip", Params: []ResolumeActionParam{idParam}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "clearLayer", Params: []ResolumeActionParam{idParam}, AuditExempt: true, CoordinatorRequired: true},
		{Name: "blackout", Params: nil, AuditExempt: true, CoordinatorRequired: true},
		{Name: "launchColumn", Params: []ResolumeActionParam{idParam}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "selectDeck", Params: []ResolumeActionParam{idParam}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "setLayerBypass", Params: []ResolumeActionParam{idParam, {Name: "bypassed", Kind: ResolumeActionParamBool, Required: true}}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "setLayerMaster", Params: []ResolumeActionParam{idParam, {Name: "master", Kind: ResolumeActionParamNumber, Required: true}}, AuditExempt: false, CoordinatorRequired: true},
	}
}

type resolumeActionTestSetup struct {
	st         *store.Store
	storeDir   string
	svc        identity.Service
	dispatcher *fakeResolumeActionDispatcher
}

func newResolumeActionTestSetup(t *testing.T, now func() time.Time) *resolumeActionTestSetup {
	t.Helper()
	dir := t.TempDir()
	storeDir := filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := identity.NewService(st, now, filepath.Join(dir, "data"), identity.WithLogger(testLogger()))
	return &resolumeActionTestSetup{
		st: st, storeDir: storeDir, svc: svc,
		dispatcher: &fakeResolumeActionDispatcher{
			descriptors: standardResolumeActionDescriptors(),
			results:     map[string]ResolumeActionResult{},
		},
	}
}

func (s *resolumeActionTestSetup) deps() Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: s.svc, Commands: s.st, ResolumeActions: s.dispatcher,
	}
}

func newResolumeActionRequest(t *testing.T, body, bearerToken string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/actions", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+bearerToken)
	}
	return req
}

func resolumeActionBody(action, idempotencyKey, paramsJSON string) string {
	if paramsJSON == "" {
		return `{"action":"` + action + `","idempotencyKey":"` + idempotencyKey + `"}`
	}
	return `{"action":"` + action + `","idempotencyKey":"` + idempotencyKey + `","params":` + paramsJSON + `}`
}

func confirmedResult(reason string) ResolumeActionResult {
	dispatchedAt := testNow
	resolvedAt := testNow
	return ResolumeActionResult{
		Outcome: ResolumeOutcomeConfirmed, Reason: reason, Dispatched: true,
		DispatchedAt: &dispatchedAt, ResolvedAt: &resolvedAt,
	}
}

// --- GET /resolume/actions: never gated. ---

func TestResolumeActionListIsOpenAndNamesEveryAction(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resolume/actions", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (no credential presented); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	actions, _ := m["actions"].([]any)
	if len(actions) != 7 {
		t.Fatalf("actions = %d, want 7; body: %s", len(actions), body)
	}
	names := map[string]bool{}
	for _, a := range actions {
		am, _ := a.(map[string]any)
		names[am["name"].(string)] = true
	}
	for _, want := range []string{"launchClip", "clearLayer", "blackout", "launchColumn", "selectDeck", "setLayerBypass", "setLayerMaster"} {
		if !names[want] {
			t.Errorf("action %q missing from GET /resolume/actions; body: %s", want, body)
		}
	}
}

// --- Acceptance criterion 7: 401 unauthenticated, 403 for a viewer naming
// resolume:action, and no HTTP request reaches Resolume in either case. ---

func TestResolumeActionDispatchRefusedUnauthenticated(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := newResolumeActionRequest(t, resolumeActionBody("blackout", "key-1", ""), "")
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body: %s", resp.StatusCode, body)
	}
	if setup.dispatcher.callCount() != 0 {
		t.Errorf("dispatcher received %d calls, want 0 — unauthenticated must never reach Dispatch", setup.dispatcher.callCount())
	}
}

func TestResolumeActionDispatchRefusedForbiddenViewerNamesScopeAndDispatchesNothing(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)

	req := newResolumeActionRequest(t, resolumeActionBody("blackout", "key-1", ""), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, "resolume:action") {
		t.Errorf("problem detail = %q, want it to name the missing scope resolume:action", detail)
	}
	if setup.dispatcher.callCount() != 0 {
		t.Errorf("dispatcher received %d calls, want 0 — a 403 must never reach Dispatch", setup.dispatcher.callCount())
	}
	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("commands rows = %d, want 0 — a 403 must not create a commands row either", len(rows))
	}
}

func TestResolumeActionDispatchAcceptedForOperator(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["blackout"] = confirmedResult("every tracked layer's active_clip reported absent")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newResolumeActionRequest(t, resolumeActionBody("blackout", "key-1", ""), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result, _ := m["result"].(map[string]any)
	if result["outcome"] != "confirmed" {
		t.Errorf("outcome = %v, want \"confirmed\"", result["outcome"])
	}
	if setup.dispatcher.callCount() != 1 {
		t.Errorf("dispatcher received %d calls, want exactly 1", setup.dispatcher.callCount())
	}
}

// --- Acceptance criterion (outcome vocabulary): every one of the five
// outcomes renders as a normal 200 body, never as an HTTP error — this is
// the "unconfirmable is not an error and must not render as one" rule
// (and, by the identical reasoning this file's own top comment names,
// applies to refused/failed too: they are honest reports, not transport or
// request failures). ---

func TestResolumeActionOutcomeVocabularyAlwaysRendersAsTwoHundred(t *testing.T) {
	cases := []struct {
		outcome ResolumeActionOutcome
	}{
		{ResolumeOutcomeConfirmed},
		{ResolumeOutcomeUnconfirmed},
		{ResolumeOutcomeUnconfirmable},
		{ResolumeOutcomeRefused},
		{ResolumeOutcomeFailed},
	}
	for _, tc := range cases {
		t.Run(string(tc.outcome), func(t *testing.T) {
			setup := newResolumeActionTestSetup(t, fixedClock(testNow))
			setup.dispatcher.results["launchColumn"] = ResolumeActionResult{
				Outcome: tc.outcome, Reason: "test reason for " + string(tc.outcome), Dispatched: tc.outcome != ResolumeOutcomeRefused,
			}
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
			token := mustIssueToken(t, setup.svc, operator.ID)

			req := newResolumeActionRequest(t, resolumeActionBody("launchColumn", "key-"+string(tc.outcome), `{"id":"col-1"}`), token)
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200 for outcome %q (never an HTTP error); body: %s", resp.StatusCode, tc.outcome, body)
			}
			m := decodeMap(t, body)
			if _, isProblem := m["type"]; isProblem {
				t.Errorf("response carries a Problem \"type\" field for outcome %q — an outcome must never render as an error; body: %s", tc.outcome, body)
			}
			result, _ := m["result"].(map[string]any)
			if result["outcome"] != string(tc.outcome) {
				t.Errorf("outcome = %v, want %q", result["outcome"], tc.outcome)
			}
			if result["outcomeReason"] == "" || result["outcomeReason"] == nil {
				t.Errorf("outcomeReason is empty for outcome %q, want a stated reason", tc.outcome)
			}
		})
	}
}

// --- Idempotency key required; malformed/missing body rejected before any
// commands row or Dispatch call. ---

func TestResolumeActionRejectsMissingIdempotencyKey(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newResolumeActionRequest(t, `{"action":"blackout"}`, token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no idempotencyKey supplied); body: %s", resp.StatusCode, body)
	}
	if setup.dispatcher.callCount() != 0 {
		t.Errorf("dispatcher received %d calls, want 0", setup.dispatcher.callCount())
	}
	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("commands rows = %d, want 0 — a rejected request must not create a commands row", len(rows))
	}
}

func TestResolumeActionRejectsUnsupportedAction(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newResolumeActionRequest(t, resolumeActionBody("deleteEverything", "key-1", ""), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	detail, _ := m["detail"].(string)
	if !strings.Contains(detail, "blackout") {
		t.Errorf("detail = %q, want it to name a supported action such as blackout", detail)
	}
}

// --- Params: absent/null/empty rule, matching decodeFPPCommandParams'
// identical contract (fppcommand_primitives.go). ---

func TestResolumeActionParamsAbsentNullEmptyRule(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"required param absent", resolumeActionBody("launchClip", "key-1", `{}`)},
		{"required param null", resolumeActionBody("launchClip", "key-1", `{"id":null}`)},
		{"required string param empty", resolumeActionBody("launchClip", "key-1", `{"id":""}`)},
		{"params object itself null", `{"action":"launchClip","idempotencyKey":"key-1","params":null}`},
		{"unknown key", resolumeActionBody("launchClip", "key-1", `{"id":"clip-1","bogus":true}`)},
		{"zero-param action given params", resolumeActionBody("blackout", "key-1", `{"id":"whatever"}`)},
		{"wrong type for bool param", resolumeActionBody("setLayerBypass", "key-1", `{"id":"layer-1","bypassed":"yes"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setup := newResolumeActionTestSetup(t, fixedClock(testNow))
			api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
			operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
			token := mustIssueToken(t, setup.svc, operator.ID)

			req := newResolumeActionRequest(t, tc.body, token)
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 for %s; body: %s", resp.StatusCode, tc.name, body)
			}
			if setup.dispatcher.callCount() != 0 {
				t.Errorf("dispatcher received %d calls for %s, want 0", setup.dispatcher.callCount(), tc.name)
			}
		})
	}
}

// --- Idempotency: replay and conflict, mirroring
// fppcommand_dispatch.go's resolveFPPCommandReplay contract exactly. ---

func TestResolumeActionReplaySameKeyDispatchesOnlyOnce(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["launchColumn"] = confirmedResult("column connected")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := resolumeActionBody("launchColumn", "key-replay", `{"id":"col-1"}`)
	req1 := newResolumeActionRequest(t, body, token)
	resp1, body1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, body, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200; body: %s", resp2.StatusCode, body2)
	}
	m2 := decodeMap(t, body2)
	result2, _ := m2["result"].(map[string]any)
	if result2["replay"] != true {
		t.Errorf("replay = %v, want true on the second request", result2["replay"])
	}
	if setup.dispatcher.callCount() != 1 {
		t.Errorf("dispatcher received %d calls, want exactly 1 — a replay must dispatch nothing", setup.dispatcher.callCount())
	}
}

func TestResolumeActionReplayDifferentActionIsConflict(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["launchColumn"] = confirmedResult("column connected")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req1 := newResolumeActionRequest(t, resolumeActionBody("launchColumn", "key-conflict", `{"id":"col-1"}`), token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, resolumeActionBody("selectDeck", "key-conflict", `{"id":"deck-1"}`), token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
	if setup.dispatcher.callCount() != 1 {
		t.Errorf("dispatcher received %d calls, want 1 (the conflicting second request dispatches nothing)", setup.dispatcher.callCount())
	}
}

func TestResolumeActionReplayDifferentParamsIsConflict(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["launchClip"] = confirmedResult("clip connected")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req1 := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-params", `{"id":"clip-1"}`), token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-params", `{"id":"clip-2"}`), token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp2.StatusCode, body2)
	}
}

// --- ADR-024 decision 11 safety class: blackout/clearLayer are exempt,
// every other action fails closed. Verified per this project's standing
// rule by installing the fail_audit trigger — a REAL SQLite trigger, not a
// mock. ---

func TestResolumeActionExemptDispatchesWhenAuditFails(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["blackout"] = confirmedResult("every tracked layer's active_clip reported absent")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	installFailAuditTrigger(t, setup.storeDir)

	req := newResolumeActionRequest(t, resolumeActionBody("blackout", "key-exempt", ""), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (blackout is exempt from ADR-024 decision 11's fail-closed rule); body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	result, _ := m["result"].(map[string]any)
	if result["attributionDegraded"] != true {
		t.Errorf("attributionDegraded = %v, want true (the audit write failed and this proceeded anyway)", result["attributionDegraded"])
	}
	if setup.dispatcher.callCount() != 1 {
		t.Errorf("dispatcher received %d calls, want exactly 1 — blackout must still dispatch", setup.dispatcher.callCount())
	}
}

func TestResolumeActionNonExemptFailsClosedWhenAuditFails(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["launchClip"] = confirmedResult("clip connected")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	installFailAuditTrigger(t, setup.storeDir)

	req := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-non-exempt", `{"id":"clip-1"}`), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (launchClip is NOT exempt; ADR-024 decision 11's default fail-closed rule); body: %s", resp.StatusCode, body)
	}
	if setup.dispatcher.callCount() != 0 {
		t.Errorf("dispatcher received %d calls, want 0 — a fail-closed refusal must dispatch nothing", setup.dispatcher.callCount())
	}
	rows, err := setup.st.ListCommands(context.Background(), 10)
	if err != nil {
		t.Fatalf("list commands: %v", err)
	}
	if len(rows) != 0 {
		t.Errorf("commands rows = %d, want 0 — a fail-closed refusal must not create a commands row either", len(rows))
	}
}

// TestResolumeActionSafetyClassMembershipMatchesSpec is the regression
// guard the spec's own section 5.2 warns about by name: "exempting a
// setLayerBypass/setLayerMaster-shaped action to protect the silencing
// direction would exempt the lighting direction with it" — Step 8's own
// documented defect (a doc comment claimed one exempt member while the
// code exempted all eight), reproduced here as an assertion against the
// FIXTURE this file's own tests are built on, so a future edit to
// standardResolumeActionDescriptors that widens the exempt set silently
// fails this test rather than only being caught by inspection.
func TestResolumeActionSafetyClassMembershipMatchesSpec(t *testing.T) {
	wantExempt := map[string]bool{"blackout": true, "clearLayer": true}
	for _, d := range standardResolumeActionDescriptors() {
		if d.AuditExempt != wantExempt[d.Name] {
			t.Errorf("action %q: AuditExempt = %v, want %v (spec section 5.2: only blackout and clearLayer are exempt)",
				d.Name, d.AuditExempt, wantExempt[d.Name])
		}
	}
}

// TestResolumeActionMaxConfirmDeadlineEqualsRegistryMax is the fix for the
// defect CLAUDE.md names explicitly for this task: resolumeActionMaxConfirmDeadline
// used to be "chosen only to comfortably exceed the spec's own named
// defaults" — a guess that happened to be generous, not a value derived
// from D-3/A's own deadline model. resolume.MaxActionConfirmDeadline
// (internal/coordinator/collector/resolume, action.go) is now the single,
// structurally-enforced source of truth: every deadline
// deriveClearDeadline/dispatchBlackout can ever produce is clamped to it,
// which is what makes it a true maximum rather than an asserted one. This
// package's own production code still does not import that package (see
// resolumeActionMaxConfirmDeadline's own doc comment for why), but this is
// a TEST file, and a test importing the producer to check a literal against
// it creates no production coupling at all — this is exactly the "test
// that fails if a client budget is ever set below the server's maximum"
// CLAUDE.md asks for, applied to the api<->resolume boundary specifically
// (TestResolumeActionMaxConfirmDeadlineFitsWithinCLIClientBudget below is
// the SEPARATE test for the api<->showmeshctl boundary, which cannot do
// this because that program is genuinely forbidden from importing either
// package).
//
// Before trusting this test: temporarily changed resolumeActionMaxConfirmDeadline
// to 29*time.Second (one second below resolume.MaxActionConfirmDeadline)
// and reran — failed immediately, naming both values. Reverted afterward.
func TestResolumeActionMaxConfirmDeadlineEqualsRegistryMax(t *testing.T) {
	if resolumeActionMaxConfirmDeadline != resolume.MaxActionConfirmDeadline {
		t.Fatalf("resolumeActionMaxConfirmDeadline (%s) != resolume.MaxActionConfirmDeadline (%s) — this package's "+
			"own HTTP write-deadline sizing has drifted from D-3/A's real, structurally-enforced deadline clamp; "+
			"raise or lower resolumeActionMaxConfirmDeadline (resolumeaction.go) to match",
			resolumeActionMaxConfirmDeadline, resolume.MaxActionConfirmDeadline)
	}
}

// TestResolumeActionMaxConfirmDeadlineFitsWithinCLIClientBudget is the
// server-side half of the client-timeout-derived-from-server-deadline
// reconciliation CLAUDE.md requires ("a test that fails if one is ever set
// below it"). cmd/showmeshctl does not import this package
// (importgraph_test.go forbids it), so the CLI's own
// minResolumeActionClientTimeout (cmd_resolume_action.go) is a SECOND,
// independently chosen literal — this hardcodes that literal's value here
// and fails if resolumeActionMaxConfirmDeadline is ever raised past what
// it assumes, mirroring the reasoning
// minFPPCommandClientTimeout's own doc comment (cmd/showmeshctl/
// cmd_fpp_command.go) documents for its own reconciliation. The mirror
// test in cmd/showmeshctl (cmd_resolume_action_test.go) does the same in
// the opposite direction.
func TestResolumeActionMaxConfirmDeadlineFitsWithinCLIClientBudget(t *testing.T) {
	// This MUST match cmd/showmeshctl/cmd_resolume_action.go's own
	// minResolumeActionClientTimeout literal exactly.
	const cliMinClientTimeout = 45 * time.Second
	// The 30-second round-trip margin below matches
	// pkg/command.ClientTimeoutMargin's own value and reasoning (a client
	// timeout equal to the server's own write deadline is already too
	// tight — the response still has to round-trip).
	const roundTripMargin = 15 * time.Second
	if resolumeActionMaxConfirmDeadline+roundTripMargin > cliMinClientTimeout {
		t.Fatalf("resolumeActionMaxConfirmDeadline (%s) + %s round-trip margin exceeds the CLI's own minimum client "+
			"timeout (%s) — a client dispatching a Resolume action could abort before this server's own write "+
			"deadline elapses, producing a false transport-timeout failure for a healthy, still-working "+
			"conversation. Raise cmd/showmeshctl/cmd_resolume_action.go's minResolumeActionClientTimeout to match.",
			resolumeActionMaxConfirmDeadline, roundTripMargin, cliMinClientTimeout)
	}
}
