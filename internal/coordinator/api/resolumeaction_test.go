package api

import (
	"context"
	"errors"
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
	"github.com/showmeshsystems/showmesh/pkg/command"
	"github.com/showmeshsystems/showmesh/pkg/observation"
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
// were made — never more than what actually happened. ctx is captured too
// (Review fix 4, 2026-08-15) so a test can assert Dispatch was actually
// called on a BOUNDED context, not merely that it was called.
type fakeResolumeDispatchCall struct {
	ctx    context.Context
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

func (f *fakeResolumeActionDispatcher) Dispatch(ctx context.Context, action string, params map[string]any, _ time.Time) (ResolumeActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fakeResolumeDispatchCall{ctx: ctx, action: action, params: params})
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

// lastCtxDeadline reports the deadline carried by the most recent Dispatch
// call's own context, or ok=false if there was no call or that call's
// context carried no deadline at all.
func (f *fakeResolumeActionDispatcher) lastCtxDeadline() (deadline time.Time, ok bool) {
	call, hasCall := f.lastCall()
	if !hasCall || call.ctx == nil {
		return time.Time{}, false
	}
	return call.ctx.Deadline()
}

// standardResolumeActionDescriptors is the ADR-037 reference vocabulary
// (superseding the earlier raw "id") and its safety-class table — this
// package's own tests do not depend on D-3/A's real registry to exist;
// they fix this vocabulary themselves, matching it against the spec
// directly.
func standardResolumeActionDescriptors() []ResolumeActionDescriptor {
	clipParam := ResolumeActionParam{Name: "clip", Kind: ResolumeActionParamString, Required: true}
	deckOptional := ResolumeActionParam{Name: "deck", Kind: ResolumeActionParamString, Required: false}
	deckRequired := ResolumeActionParam{Name: "deck", Kind: ResolumeActionParamString, Required: true}
	layerOptional := ResolumeActionParam{Name: "layer", Kind: ResolumeActionParamString, Required: false}
	layerRequired := ResolumeActionParam{Name: "layer", Kind: ResolumeActionParamString, Required: true}
	persistentParam := ResolumeActionParam{Name: "persistent", Kind: ResolumeActionParamBool, Required: false}
	columnParam := ResolumeActionParam{Name: "column", Kind: ResolumeActionParamString, Required: true}
	return []ResolumeActionDescriptor{
		{Name: "launchClip", Params: []ResolumeActionParam{clipParam, deckOptional, layerOptional, persistentParam}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "clearLayer", Params: []ResolumeActionParam{layerRequired}, AuditExempt: true, CoordinatorRequired: true},
		{Name: "blackout", Params: nil, AuditExempt: true, CoordinatorRequired: true},
		{Name: "launchColumn", Params: []ResolumeActionParam{columnParam, deckRequired}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "selectDeck", Params: []ResolumeActionParam{deckRequired}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "setLayerBypass", Params: []ResolumeActionParam{layerRequired, {Name: "bypassed", Kind: ResolumeActionParamBool, Required: true}}, AuditExempt: false, CoordinatorRequired: true},
		{Name: "setLayerMaster", Params: []ResolumeActionParam{layerRequired, {Name: "master", Kind: ResolumeActionParamNumber, Required: true}}, AuditExempt: false, CoordinatorRequired: true},
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

			req := newResolumeActionRequest(t, resolumeActionBody("launchColumn", "key-"+string(tc.outcome), `{"column":"col-1","deck":"deck-1"}`), token)
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
		{"required param null", resolumeActionBody("launchClip", "key-1", `{"clip":null}`)},
		{"required string param empty", resolumeActionBody("launchClip", "key-1", `{"clip":""}`)},
		{"params object itself null", `{"action":"launchClip","idempotencyKey":"key-1","params":null}`},
		{"unknown key", resolumeActionBody("launchClip", "key-1", `{"clip":"clip-1","bogus":true}`)},
		{"zero-param action given params", resolumeActionBody("blackout", "key-1", `{"clip":"whatever"}`)},
		{"wrong type for bool param", resolumeActionBody("setLayerBypass", "key-1", `{"layer":"layer-1","bypassed":"yes"}`)},
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

	body := resolumeActionBody("launchColumn", "key-replay", `{"column":"col-1","deck":"deck-1"}`)
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

	req1 := newResolumeActionRequest(t, resolumeActionBody("launchColumn", "key-conflict", `{"column":"col-1","deck":"deck-1"}`), token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, resolumeActionBody("selectDeck", "key-conflict", `{"deck":"deck-1"}`), token)
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

	req1 := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-params", `{"clip":"clip-1","deck":"deck-1"}`), token)
	if resp1, body1 := doRawRequest(t, api.Handler, req1); resp1.StatusCode != http.StatusOK {
		t.Fatalf("first request status = %d, want 200; body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-params", `{"clip":"clip-2","deck":"deck-1"}`), token)
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

	req := newResolumeActionRequest(t, resolumeActionBody("launchClip", "key-non-exempt", `{"clip":"clip-1","deck":"deck-1"}`), token)
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

// TestStandardResolumeActionDescriptorsFixtureSafetyClassMatchesSpec is a
// regression guard on THIS FILE'S OWN TEST FIXTURE, standardResolumeActionDescriptors
// — NOT on D-3/A's production registry (Review fix 5, 2026-08-15: the
// previous name, TestResolumeActionSafetyClassMembershipMatchesSpec,
// claimed to check "the spec" while walking a fixture this same file
// defines two functions above, so it could never fail for a real registry
// defect — only for this file disagreeing with itself). The production
// property — resolume.actionRegistry's REAL safety class, read through
// resolumeActionDispatcherAdapter.Actions(), survives translation — is
// TestAdapterActionsMatchesADR024Decision11ExactlyThroughTheRealRegistry
// (internal/coordinator/resolumeactionwiring_test.go), against the real
// registry. What THIS test still guards is real, just narrower than its
// old name claimed: this package's own tests (TestResolumeActionExemptDispatchesWhenAuditFails
// and friends) all key off standardResolumeActionDescriptors, so a future
// edit that silently widens ITS exempt set would make every one of those
// tests exercise the wrong safety class without any of them failing.
func TestStandardResolumeActionDescriptorsFixtureSafetyClassMatchesSpec(t *testing.T) {
	wantExempt := map[string]bool{"blackout": true, "clearLayer": true}
	for _, d := range standardResolumeActionDescriptors() {
		if d.AuditExempt != wantExempt[d.Name] {
			t.Errorf("action %q: AuditExempt = %v, want %v (spec section 5.2: only blackout and clearLayer are exempt)",
				d.Name, d.AuditExempt, wantExempt[d.Name])
		}
	}
}

// TestResolumeActionMaxDispatchDurationEqualsRegistryMax is the fix for the
// defect CLAUDE.md names explicitly for this task: resolumeActionMaxDispatchDuration
// is a duplicated literal, not read from resolume.MaxDispatchDuration
// (internal/coordinator/collector/resolume, action.go) directly — this
// package's own production code does not import that package (see
// resolumeActionMaxDispatchDuration's own doc comment for why). This is a
// TEST file, and a test importing the producer to check a literal against
// it creates no production coupling at all — the same "test that fails if
// a client budget is ever set below the server's maximum" CLAUDE.md asks
// for, applied to the api<->resolume boundary specifically
// (TestResolumeActionHTTPWriteDeadlineFitsWithinCLIClientBudget below is
// the SEPARATE test for the api<->showmeshctl boundary, which cannot do
// this because that program is genuinely forbidden from importing either
// package).
//
// Before trusting this test: temporarily changed resolumeActionMaxDispatchDuration
// to 39*time.Second (one second below resolume.MaxDispatchDuration) and
// reran — failed immediately, naming both values. Reverted afterward.
func TestResolumeActionMaxDispatchDurationEqualsRegistryMax(t *testing.T) {
	if resolumeActionMaxDispatchDuration != resolume.MaxDispatchDuration {
		t.Fatalf("resolumeActionMaxDispatchDuration (%s) != resolume.MaxDispatchDuration (%s) — this package's "+
			"own dispatch-budget sizing has drifted from D-3/A's real, structurally-enforced deadline sum; "+
			"raise or lower resolumeActionMaxDispatchDuration (resolumeaction.go) to match",
			resolumeActionMaxDispatchDuration, resolume.MaxDispatchDuration)
	}
}

// TestResolumeRecoveryMaxLayersEqualsProducerBound is
// TestResolumeActionMaxDispatchDurationEqualsRegistryMax's own D-3a
// sibling: resolumeRecoveryMaxLayers (resolumerecovery.go) duplicates
// resolume.MaxRestoreLayers by value, and this is a TEST-only import
// checking the two never drift apart.
func TestResolumeRecoveryMaxLayersEqualsProducerBound(t *testing.T) {
	if resolumeRecoveryMaxLayers != resolume.MaxRestoreLayers {
		t.Fatalf("resolumeRecoveryMaxLayers (%d) != resolume.MaxRestoreLayers (%d) — this package's own "+
			"write-deadline clamp has drifted from what a restore itself ever attempts; raise or lower "+
			"resolumeRecoveryMaxLayers (resolumerecovery.go) to match",
			resolumeRecoveryMaxLayers, resolume.MaxRestoreLayers)
	}
}

// TestResolumeRecoveryRestoreDeadlineScalesAndClamps: the write deadline
// scales with the composition's own layer count (never a fixed number
// unrelated to what a restore actually needs) and is clamped at
// resolumeRecoveryMaxLayers rather than growing without limit for an
// unusually large composition. Breaking: the clamp `if layerCount >
// resolumeRecoveryMaxLayers { layerCount = resolumeRecoveryMaxLayers }`
// removed — confirmed this test's clamp assertion goes red (the deadline
// for 500 extra layers grew unbounded instead of matching the deadline at
// exactly resolumeRecoveryMaxLayers), then restored.
func TestResolumeRecoveryRestoreDeadlineScalesAndClamps(t *testing.T) {
	one := resolumeRecoveryRestoreDeadline(1)
	two := resolumeRecoveryRestoreDeadline(2)
	if two <= one {
		t.Fatalf("resolumeRecoveryRestoreDeadline(2) = %s, want more than resolumeRecoveryRestoreDeadline(1) = %s — the deadline must scale with layer count", two, one)
	}
	atCeiling := resolumeRecoveryRestoreDeadline(resolumeRecoveryMaxLayers)
	beyondCeiling := resolumeRecoveryRestoreDeadline(resolumeRecoveryMaxLayers + 500)
	if beyondCeiling != atCeiling {
		t.Fatalf("resolumeRecoveryRestoreDeadline(%d+500) = %s, want it clamped to resolumeRecoveryRestoreDeadline(%d) = %s",
			resolumeRecoveryMaxLayers, beyondCeiling, resolumeRecoveryMaxLayers, atCeiling)
	}
}

// TestResolumeActionHTTPWriteDeadlineFitsWithinCLIClientBudget is the
// server-side half of the client-timeout-derived-from-server-deadline
// reconciliation CLAUDE.md requires ("a test that fails if one is ever set
// below it"). cmd/showmeshctl does not import this package
// (importgraph_test.go forbids it), so the CLI's own
// minResolumeActionClientTimeout (cmd_resolume_action.go) is a SECOND,
// independently chosen literal — this hardcodes the number THAT literal
// must be raised to at least, and fails if resolumeActionHTTPWriteDeadline
// is ever raised past what it assumes.
//
// Rewritten by Review fix 4 (2026-08-15): the PREVIOUS version of this
// test reconciled resolumeActionMaxConfirmDeadline (30s, the confirm-poll
// clamp alone — never this handler's real write deadline, which also
// covers the baseline phase and post-dispatch bookkeeping) against a
// 45-second CLI floor using a "30-second round-trip margin" whose own
// comment disagreed with the 15-second literal on the next line, and it
// passed at EXACTLY that 45-second boundary with ZERO slack — a test that
// cannot distinguish "correct" from "wrong by a coincidence." This version
// reconciles the WRITE DEADLINE (the pair that actually matters) with real
// slack, and its own comment matches its own constant.
func TestResolumeActionHTTPWriteDeadlineFitsWithinCLIClientBudget(t *testing.T) {
	// requiredCLIMinClientTimeout is the number the CLI wave must raise
	// cmd/showmeshctl/cmd_resolume_action.go's own
	// minResolumeActionClientTimeout to (currently 45s, stale as of this
	// fix — see this task's own report). cmd_resolume_action_test.go
	// reconciles the REAL literal against a hardcoded copy of this number
	// in the opposite direction.
	const requiredCLIMinClientTimeout = 80 * time.Second
	// slack is real headroom over the computed floor below, never a
	// boundary equality — the exact property the pre-fix version of this
	// test lacked.
	const slack = 10 * time.Second

	// command.ClientTimeoutMargin is reused rather than a second,
	// possibly-drifting literal: the same margin minFPPCommandClientTimeout's
	// own reconciliation applies for the identical reason (a client
	// timeout equal to the server's own write deadline is already too
	// tight — the response still has to round-trip).
	need := resolumeActionHTTPWriteDeadline + command.ClientTimeoutMargin
	if need > requiredCLIMinClientTimeout {
		t.Fatalf("resolumeActionHTTPWriteDeadline (%s) + command.ClientTimeoutMargin (%s) = %s, which exceeds "+
			"requiredCLIMinClientTimeout (%s) — the CLI wave must raise minResolumeActionClientTimeout "+
			"(cmd_resolume_action.go) to at least %s",
			resolumeActionHTTPWriteDeadline, command.ClientTimeoutMargin, need, requiredCLIMinClientTimeout, need)
	}
	if got := requiredCLIMinClientTimeout - need; got < slack {
		t.Fatalf("requiredCLIMinClientTimeout (%s) leaves only %s of slack over the computed floor (%s), want at least %s",
			requiredCLIMinClientTimeout, got, need, slack)
	}
}

// TestResolumeActionDispatchContextCarriesTheDispatchBudget is Review fix
// 4's other half: before this fix, Dispatch was called on
// context.WithoutCancel(ctx) with NO deadline of any kind — correct for
// surviving a client abort, wrong for being bounded (both review passes
// independently found this: a stalled Dispatch call could run forever).
// This proves the context Dispatch actually receives now carries a
// deadline no more than resolumeActionMaxDispatchDuration away.
func TestResolumeActionDispatchContextCarriesTheDispatchBudget(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["blackout"] = confirmedResult("every tracked layer's active_clip reported absent")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	req := newResolumeActionRequest(t, resolumeActionBody("blackout", "key-ctx-budget", ""), token)
	doRawRequest(t, api.Handler, req)

	deadline, ok := setup.dispatcher.lastCtxDeadline()
	if !ok {
		t.Fatal("Dispatch's own context carried no deadline at all — it must be bounded by " +
			"resolumeActionMaxDispatchDuration, not context.WithoutCancel(ctx) alone")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		t.Fatalf("Dispatch's own context deadline already elapsed (%s remaining)", remaining)
	}
	if remaining > resolumeActionMaxDispatchDuration {
		t.Errorf("Dispatch's own context deadline leaves %s remaining, want <= resolumeActionMaxDispatchDuration (%s)",
			remaining, resolumeActionMaxDispatchDuration)
	}
}

// --- Review fix 3: OutcomeState carries pkg/observation's vocabulary, not
// this endpoint's own five-word outcome. ---

// TestResolumeActionOutcomeStateCarriesObservationVocabularyNotOutcomeWord
// dispatches one confirmed and one non-confirmed (refused) action and
// reads back BOTH places this handler writes OutcomeState — the commands
// row (store.CommandRecord.OutcomeState) and the outcome audit entry
// (identity.AuditEntry.OutcomeState) — asserting neither ever carries this
// endpoint's own outcome word ("confirmed", "refused", ...), which is not
// a member of pkg/observation's vocabulary at all.
func TestResolumeActionOutcomeStateCarriesObservationVocabularyNotOutcomeWord(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.results["launchColumn"] = confirmedResult("column connected")
	setup.dispatcher.results["selectDeck"] = ResolumeActionResult{
		Outcome: ResolumeOutcomeRefused, Reason: "test refusal", Dispatched: false,
	}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// confirmed: the one outcome this package can honestly state a real
	// pkg/observation state for (StateCurrent — the confirming evidence was
	// read strictly after dispatch, TRACK-D-D3-SPEC.md section 4.1).
	req := newResolumeActionRequest(t, resolumeActionBody("launchColumn", "key-state-confirmed", `{"column":"col-1","deck":"deck-1"}`), token)
	doRawRequest(t, api.Handler, req)

	rec, err := setup.st.GetCommand(context.Background(), mustLookUpResolumeCommandID(t, setup.st, "key-state-confirmed"))
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if rec.OutcomeState != string(observation.StateCurrent) {
		t.Errorf("confirmed command row OutcomeState = %q, want %q (pkg/observation's StateCurrent, not the outcome word)",
			rec.OutcomeState, string(observation.StateCurrent))
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 20)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.CommandID == rec.ID && e.Kind == identity.AuditOutcome {
			found = true
			if e.OutcomeState != string(observation.StateCurrent) {
				t.Errorf("confirmed outcome audit entry OutcomeState = %q, want %q", e.OutcomeState, string(observation.StateCurrent))
			}
		}
	}
	if !found {
		t.Fatal("no AuditOutcome entry found for the confirmed command")
	}

	// refused: this package has no per-observation evidence-state signal
	// to back ANY word for this outcome — OutcomeState must be genuinely
	// absent, never the outcome word "refused" (not a pkg/observation
	// state at all).
	req2 := newResolumeActionRequest(t, resolumeActionBody("selectDeck", "key-state-refused", `{"deck":"deck-1"}`), token)
	doRawRequest(t, api.Handler, req2)

	rec2, err := setup.st.GetCommand(context.Background(), mustLookUpResolumeCommandID(t, setup.st, "key-state-refused"))
	if err != nil {
		t.Fatalf("get command: %v", err)
	}
	if rec2.OutcomeState != "" {
		t.Errorf("refused command row OutcomeState = %q, want empty (no evidence-state signal is available for this outcome)", rec2.OutcomeState)
	}
	entries2, err := setup.svc.ListAudit(context.Background(), 0, 20)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, e := range entries2 {
		if e.CommandID == rec2.ID && e.Kind == identity.AuditOutcome {
			if e.OutcomeState != "" {
				t.Errorf("refused outcome audit entry OutcomeState = %q, want empty", e.OutcomeState)
			}
			if e.OutcomeState == string(ResolumeOutcomeRefused) {
				t.Error("refused outcome audit entry OutcomeState carries the outcome word itself — this is exactly the bug this test guards against")
			}
		}
	}
}

// mustLookUpResolumeCommandID is a small helper: this file's own tests key
// on idempotencyKey, but store.CommandRecord.GetCommand needs the row's
// ID — GetCommandByIdempotencyKey bridges the two.
func mustLookUpResolumeCommandID(t *testing.T, st *store.Store, idempotencyKey string) string {
	t.Helper()
	rec, err := st.GetCommandByIdempotencyKey(context.Background(), idempotencyKey)
	if err != nil {
		t.Fatalf("look up command by idempotency key %q: %v", idempotencyKey, err)
	}
	return rec.ID
}

// --- Review fix 1: a replay observed mid-flight emits an outcome the
// endpoint's own schema now honestly documents, and it can never be
// PERMANENT for a Resolume row — see resolumeaction_reconcile_test.go for
// the startup-reconciliation half of this fix. ---

// TestResolumeActionReplayOfADeadDispatchExposesTheAcceptedBlankOutcome
// reproduces the exact scenario Review fix 1 was filed against: force
// Dispatch to fail (this handler answers 500 and, by construction, writes
// NOTHING further to the row — see handleDispatchResolumeAction's own
// dispatchErr branch), then replay the SAME idempotency key. The row is
// still genuinely unresolved, so the replay honestly reports outcome="" —
// the identical narrow, accepted race FPPCommandResult.outcome's own
// description names — and, critically, this must be a normal 200 that
// validates against the schema, not a value the schema rejects.
func TestResolumeActionReplayOfADeadDispatchExposesTheAcceptedBlankOutcome(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	setup.dispatcher.err = errors.New("simulated internal dispatcher failure")
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	body := resolumeActionBody("blackout", "key-dead-dispatch", "")
	req1 := newResolumeActionRequest(t, body, token)
	resp1, body1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first request status = %d, want 500 (Dispatch was made to fail); body: %s", resp1.StatusCode, body1)
	}

	req2 := newResolumeActionRequest(t, body, token)
	resp2, body2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("replay status = %d, want 200 (a replay of an honestly-unresolved row is not itself an error); body: %s", resp2.StatusCode, body2)
	}
	m := decodeMap(t, body2)
	result, _ := m["result"].(map[string]any)
	if result["outcome"] != "" {
		t.Errorf("outcome = %v, want \"\" (the row genuinely never resolved — Dispatch failed before anything else was written)", result["outcome"])
	}
	if result["outcomeReason"] != "" {
		t.Errorf("outcomeReason = %v, want \"\" for the identical reason", result["outcomeReason"])
	}
	if result["replay"] != true {
		t.Errorf("replay = %v, want true", result["replay"])
	}
}

// --- Review fix 5: a body over the size limit is reported as a size
// refusal, not a syntax error. ---

func TestResolumeActionRequestBodyOverLimitReportsSizeNotSyntax(t *testing.T) {
	setup := newResolumeActionTestSetup(t, fixedClock(testNow))
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// A syntactically valid JSON object, padded with an oversized string
	// value past maxResolumeActionRequestBodyBytes (4 KiB) — this would
	// decode cleanly if it were not for the size limit, isolating the size
	// refusal from any genuine syntax error.
	padding := strings.Repeat("x", maxResolumeActionRequestBodyBytes+256)
	body := `{"action":"blackout","idempotencyKey":"key-too-large","padding":"` + padding + `"}`

	req := newResolumeActionRequest(t, body, token)
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body: %s", resp.StatusCode, respBody)
	}
	m := decodeMap(t, respBody)
	if m["type"] != ProblemTypeResolumeCompositionTooLarge {
		t.Errorf("type = %v, want %v (the shared payload-too-large class, not invalid-parameter)", m["type"], ProblemTypeResolumeCompositionTooLarge)
	}
	detail, _ := m["detail"].(string)
	if strings.Contains(detail, "JSON object matching") {
		t.Errorf("detail = %q, still reads as a syntax-error message rather than a size refusal", detail)
	}
	if setup.dispatcher.callCount() != 0 {
		t.Errorf("dispatcher received %d calls, want 0 — an oversized body must never reach Dispatch", setup.dispatcher.callCount())
	}
}
