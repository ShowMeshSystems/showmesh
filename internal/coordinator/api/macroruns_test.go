package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 9 wave 2's own test suite for the run surface
// (STEP-9-SPEC.md section 6.6): POST /macros/{id}/runs, GET /macro-runs,
// GET /macro-runs/{runId}. Driven against [fakeMacroRunner]
// (fakes_test.go) — this package cannot import internal/coordinator/macro
// (the import direction is forced the other way, see macro_seam.go and
// importgraph_test.go), so a real *macro.Executor cannot appear in this
// package's own test suite; the executor's own behavior is proven in its
// own package, and end to end against a running coordinator per this
// builder's own report.

func macroRunTestDeps(svc identity.Service, macros *fakeMacroRunner, commands *fakeCommandStore) Dependencies {
	return Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Identity: svc, Macros: macros, Commands: commands,
	}
}

// TestSubmitMacroRunIs202AndScoped proves acceptance criterion 21's run
// half (an operator can run a macro) and STEP-9-SPEC.md section 6.6's
// status code.
//
// Broken and confirmed to fail: changed handleSubmitMacroRun's
// w.WriteHeader(http.StatusAccepted) to http.StatusOK and reran — the
// "status is 202" assertion below failed as expected (got 200). Restored
// afterward.
func TestSubmitMacroRunIs202AndScoped(t *testing.T) {
	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	operatorToken := mustIssueToken(t, svc, operator.ID)
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	viewerToken := mustIssueToken(t, svc, viewer.ID)

	macros := &fakeMacroRunner{submitResult: MacroRunResult{
		Run: store.MacroRunRecord{ID: "run-1", MacroObjectID: "begin-set", State: "running", CreatedAt: testNow},
	}}
	api := New(macroRunTestDeps(svc, macros, newFakeCommandStore()), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	t.Run("viewer lacks show:macro:run", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPost, "/api/v1/macros/begin-set/runs",
			`{"idempotencyKey":"key-1","trigger":"ui"}`, map[string]string{"Authorization": "Bearer " + viewerToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
		}
	})

	t.Run("operator holds show:macro:run", func(t *testing.T) {
		req := newJSONRequest(t, http.MethodPost, "/api/v1/macros/begin-set/runs",
			`{"idempotencyKey":"key-1","trigger":"ui"}`, map[string]string{"Authorization": "Bearer " + operatorToken})
		resp, body := doRawRequest(t, api.Handler, req)
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body: %s", resp.StatusCode, body)
		}
		got := decodeMap(t, body)
		run := got["run"].(map[string]any)
		if run["id"] != "run-1" {
			t.Errorf("run.id = %v, want run-1", run["id"])
		}
		if macros.gotSubmit.Trigger != "ui" || macros.gotSubmit.IdempotencyKey != "key-1" {
			t.Errorf("executor received Trigger=%q IdempotencyKey=%q, want ui/key-1", macros.gotSubmit.Trigger, macros.gotSubmit.IdempotencyKey)
		}
		if macros.gotSubmit.Issuer.PrincipalName != "operator-1" {
			t.Errorf("executor received issuer %q, want operator-1", macros.gotSubmit.Issuer.PrincipalName)
		}
	})
}

// TestSubmitMacroRunRejectsBadTriggerAndMissingKey.
func TestSubmitMacroRunRejectsBadTriggerAndMissingKey(t *testing.T) {
	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)
	macros := &fakeMacroRunner{}
	api := New(macroRunTestDeps(svc, macros, newFakeCommandStore()), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	cases := []struct {
		name, body string
	}{
		{"missing idempotency key", `{"trigger":"ui"}`},
		{"bad trigger", `{"idempotencyKey":"k","trigger":"cron"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := newJSONRequest(t, http.MethodPost, "/api/v1/macros/begin-set/runs", tc.body, map[string]string{"Authorization": "Bearer " + token})
			resp, body := doRawRequest(t, api.Handler, req)
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
		})
	}
}

// TestSubmitMacroRunBoundsPriorFailures: wave 2 shared contract section 2,
// "a caller sending ten thousand entries must be refused, not absorbed."
func TestSubmitMacroRunBoundsPriorFailures(t *testing.T) {
	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)
	macros := &fakeMacroRunner{}
	api := New(macroRunTestDeps(svc, macros, newFakeCommandStore()), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	var body strings.Builder
	body.WriteString(`{"idempotencyKey":"k","trigger":"plugin","priorFailures":[`)
	for i := 0; i < maxPriorFailuresInRequest+1; i++ {
		if i > 0 {
			body.WriteString(",")
		}
		body.WriteString(`{"macroObjectId":"begin-set","class":"refused","httpStatus":403,"at":"2026-08-14T00:00:00Z"}`)
	}
	body.WriteString(`]}`)

	req := newJSONRequest(t, http.MethodPost, "/api/v1/macros/begin-set/runs", body.String(), map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (oversized priorFailures must be refused, not absorbed); body: %s", resp.StatusCode, respBody)
	}
}

// TestListMacroRunsValidatesState.
func TestListMacroRunsValidatesState(t *testing.T) {
	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)
	macros := &fakeMacroRunner{listResult: []store.MacroRunRecord{
		{ID: "run-1", MacroObjectID: "begin-set", State: "finished", CreatedAt: testNow},
	}}
	api := New(macroRunTestDeps(svc, macros, newFakeCommandStore()), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/macro-runs?state=bogus", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}

	resp, body = doRequest(t, api.Handler, "GET", "/api/v1/macro-runs?state=finished&macroId=begin-set", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if macros.gotFilter.State != "finished" || macros.gotFilter.MacroObjectID != "begin-set" {
		t.Errorf("executor received filter %+v, want state=finished macroId=begin-set", macros.gotFilter)
	}
	got := decodeMap(t, body)
	runs := got["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1; body: %s", len(runs), body)
	}
}

// TestGetMacroRunNotFound.
func TestGetMacroRunNotFound(t *testing.T) {
	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)
	macros := &fakeMacroRunner{getErr: fmtErrMacroRunNotFound("run-x")}
	api := New(macroRunTestDeps(svc, macros, newFakeCommandStore()), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/macro-runs/run-x", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func fmtErrMacroRunNotFound(id string) error {
	return &wrappedNotFound{id: id}
}

type wrappedNotFound struct{ id string }

func (w *wrappedNotFound) Error() string { return "not found: " + w.id }
func (w *wrappedNotFound) Unwrap() error { return ErrMacroRunNotFound }

// TestGetMacroRunRendersCommandDetail proves acceptance criterion 22's
// route-layer half: a step's dispatched command renders "retained" when
// the commands table still holds it, "not_retained" (never blank) when it
// does not, and "none" when the step never had one at all — see
// [v1.MacroRunStepCommand]'s own doc comment for the three states.
func TestGetMacroRunRendersCommandDetail(t *testing.T) {
	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, svc, operator.ID)

	commands := newFakeCommandStore()
	commands.setCommand(store.CommandRecord{
		ID: "cmd-retained", IdempotencyKey: "run-1/0", Action: "startPlaylist", TargetID: "player-01",
		ParamsJSON: `{"playlist":"Halloween Main"}`, ResultJSON: `{"outcome":"confirmed"}`,
		OutcomeState: "current", OutcomeReason: "matched", State: "resolved",
	})
	retainedID, prunedID := "cmd-retained", "cmd-pruned"

	macros := &fakeMacroRunner{getResult: MacroRunResult{
		Run: store.MacroRunRecord{ID: "run-1", MacroObjectID: "begin-set", State: "finished", CreatedAt: testNow},
		Steps: []store.MacroRunStepRecord{
			{RunID: "run-1", StepIndex: 0, StepID: "s0", ActionObjectID: "a0", Integration: "fpp", State: "resolved", Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "ok", CommandID: &retainedID},
			{RunID: "run-1", StepIndex: 1, StepID: "s1", ActionObjectID: "a1", Integration: "fpp", State: "resolved", Outcome: "confirmed", OutcomeState: "current", OutcomeReason: "ok", CommandID: &prunedID},
			{RunID: "run-1", StepIndex: 2, StepID: "s2", ActionObjectID: "a2", Integration: "mqtt", State: "resolved", Outcome: "unconfirmable", OutcomeState: "unconfirmable_declared", OutcomeReason: "no expected response"},
		},
	}}
	api := New(macroRunTestDeps(svc, macros, commands), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/macro-runs/run-1", map[string]string{"Authorization": "Bearer " + token})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeMap(t, body)
	steps := got["run"].(map[string]any)["steps"].([]any)
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(steps))
	}

	cmd0 := steps[0].(map[string]any)["command"].(map[string]any)
	if cmd0["state"] != "retained" || cmd0["detail"] == nil {
		t.Errorf("step 0 command = %v, want state=retained with detail", cmd0)
	}
	cmd1 := steps[1].(map[string]any)["command"].(map[string]any)
	if cmd1["state"] != "not_retained" || cmd1["reason"] == "" || cmd1["reason"] == nil {
		t.Errorf("step 1 command = %v, want state=not_retained with a stated reason (never blank)", cmd1)
	}
	cmd2 := steps[2].(map[string]any)["command"].(map[string]any)
	if cmd2["state"] != "none" {
		t.Errorf("step 2 (mqtt) command = %v, want state=none", cmd2)
	}
}

// TestSnapshotIncludesInFlightMacroRuns is acceptance criterion 16's
// route-layer proof: a run [MacroRunner.SnapshotRuns] reports as in-flight
// appears in GET /api/v1/snapshot's macroRuns array.
func TestSnapshotIncludesInFlightMacroRuns(t *testing.T) {
	svc, _, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	macros := &fakeMacroRunner{}
	macros.setSnapshot([]store.MacroRunRecord{
		{ID: "run-live", MacroObjectID: "begin-set", State: "running", CreatedAt: testNow},
	})
	deps := macroRunTestDeps(svc, macros, newFakeCommandStore())
	deps.Nodes = &fakeNodeLister{}
	deps.FPP = &fakeFPPLister{}
	deps.Events = &fakeEventReader{}
	deps.Collectors = &fakeCollectorStatusLister{}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeMap(t, body)
	runs, ok := got["macroRuns"].([]any)
	if !ok || len(runs) != 1 {
		t.Fatalf("snapshot.macroRuns = %v, want exactly one in-flight run", got["macroRuns"])
	}
	run := runs[0].(map[string]any)
	if run["id"] != "run-live" || run["state"] != "running" {
		t.Errorf("snapshot run = %v, want id=run-live state=running", run)
	}
}

// TestHubEmitsMacroRunChangedEvent proves STEP-9-SPEC.md section 6.6's own
// change-stream addition: a run state transition between two render passes
// produces a "macroRun.changed" frame.
func TestHubEmitsMacroRunChangedEvent(t *testing.T) {
	macros := &fakeMacroRunner{}
	macros.setSnapshot([]store.MacroRunRecord{
		{ID: "run-1", MacroObjectID: "begin-set", State: "running", CreatedAt: testNow},
	})
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{}, Macros: macros,
	}
	opts := Options{Clock: fixedClock(testNow), Logger: testLogger()}.withDefaults()
	hub := newHub(deps.withDefaults(), opts, opts.Logger)

	_, sub := hub.subscribe(false, nil)

	hub.render(t.Context())
	drain(sub.frames) // the first render pass's own frame (the run first appearing)

	confirmed := true
	macros.setSnapshot([]store.MacroRunRecord{
		{ID: "run-1", MacroObjectID: "begin-set", State: "finished", CreatedAt: testNow, Confirmed: &confirmed},
	})
	hub.render(t.Context())

	select {
	case pf := <-sub.frames:
		if pf.event != "macroRun.changed" {
			t.Fatalf("event = %q, want macroRun.changed", pf.event)
		}
		if pf.macroRun == nil || pf.macroRun.RunID != "run-1" || pf.macroRun.State != "finished" {
			t.Fatalf("frame = %+v, want run-1 finished", pf.macroRun)
		}
	default:
		t.Fatal("expected a macroRun.changed frame after the run transitioned, got none")
	}
}

func drain(ch chan pendingFrame) {
	select {
	case <-ch:
	default:
	}
}
