package api

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// This file proves fppcommand_dispatch.go's own exported surface — the
// seam Step 9's macro executor (a different package, built in a later
// wave) is expected to call in-process — using the SAME real-store,
// real-identity-service harness fppcommand_handler_test.go already
// builds ([newFPPCommandTestSetup]), never a hand-built fake standing in
// for the dispatch/confirm/audit core itself. It does NOT re-prove any
// Step 7/Step 8 acceptance criterion fppcommand_handler_test.go already
// covers over HTTP — this refactor's whole claim is that
// [handlers.dispatchFPPCommand] is the SAME code either way, so re-running
// every one of those scenarios a second time here would only prove Go
// functions are deterministic. What is new here, and untested until this
// file, is the exported surface itself: [FPPCommandDispatcher.Dispatch] and
// [FPPCommandSafetyClassForAction].

// TestFPPCommandDispatcherMatchesHTTPPathForSuccess dispatches the
// identical stopPlaylist command twice against one fake bench fppd — once
// through the real HTTP handler ([New].Handler), once through
// [FPPCommandDispatcher.Dispatch] built from the SAME Dependencies/Options
// — and asserts both produce the same confirmed outcome against the same
// evidence. Different idempotency keys (the three-way replay rule would
// otherwise turn the second call into a replay of the first, proving
// nothing about the in-process path specifically).
func TestFPPCommandDispatcherMatchesHTTPPathForSuccess(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	opts := Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	}
	deps := setup.deps()
	api := New(deps, opts)

	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)

	// --- HTTP path ---
	req := newFPPCommandRequest(t, "bench-fpp", stopPlaylistBody("key-http"), token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("HTTP path: status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	httpOutcome := decodeMap(t, body)["command"].(map[string]any)["outcome"]

	// --- In-process path, same deps/opts, a DIFFERENT idempotency key. ---
	dispatcher := NewFPPCommandDispatcher(deps, opts)
	outcome, problem, err := dispatcher.Dispatch(context.Background(), FPPCommandInput{
		InstanceID:     "bench-fpp",
		Action:         "stopPlaylist",
		IdempotencyKey: "key-inprocess",
		Issuer:         FPPCommandIssuer{PrincipalID: operator.ID, PrincipalName: operator.Name},
	})
	if err != nil {
		t.Fatalf("Dispatch returned an internal error: %v", err)
	}
	if problem != nil {
		t.Fatalf("Dispatch refused: %+v", problem)
	}
	if outcome.Outcome != "confirmed" {
		t.Errorf("in-process outcome = %q, want %q (matching the HTTP path's %v)", outcome.Outcome, "confirmed", httpOutcome)
	}
	if httpOutcome != "confirmed" {
		t.Fatalf("HTTP outcome = %v, want \"confirmed\" — test setup is wrong if this fails", httpOutcome)
	}
	if srv.hitCount() != 2 {
		t.Errorf("FPP received %d requests, want exactly 2 (one per path, no duplicate dispatch)", srv.hitCount())
	}
	if outcome.CommandID == "" {
		t.Error("CommandID is empty on a successful dispatch")
	}
	if outcome.AttributionDegraded {
		t.Error("AttributionDegraded = true with a healthy audit store")
	}
	if outcome.DispatchedAt == nil || outcome.ResolvedAt == nil {
		t.Error("DispatchedAt/ResolvedAt must both be set on a resolved dispatch")
	}
}

// TestFPPCommandDispatcherReplayViaDispatch proves the in-process entry
// point honors the SAME idempotency-first replay rule the HTTP handler
// does: a second Dispatch call with the same key, action, target, and
// params returns the FIRST call's own result and dispatches nothing a
// second time.
func TestFPPCommandDispatcherReplayViaDispatch(t *testing.T) {
	fppSrv, srv := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	opts := Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	}
	dispatcher := NewFPPCommandDispatcher(setup.deps(), opts)

	in := FPPCommandInput{
		InstanceID:     "bench-fpp",
		Action:         "stopPlaylist",
		IdempotencyKey: "key-replay-inprocess",
		Issuer:         FPPCommandIssuer{PrincipalID: "op-1", PrincipalName: "operator-1"},
	}

	first, problem, err := dispatcher.Dispatch(context.Background(), in)
	if err != nil || problem != nil {
		t.Fatalf("first dispatch: err=%v problem=%+v", err, problem)
	}
	if first.Replay {
		t.Fatal("first dispatch reported Replay=true")
	}
	if srv.hitCount() != 1 {
		t.Fatalf("after first dispatch, FPP received %d requests, want 1", srv.hitCount())
	}

	second, problem, err := dispatcher.Dispatch(context.Background(), in)
	if err != nil || problem != nil {
		t.Fatalf("second (replay) dispatch: err=%v problem=%+v", err, problem)
	}
	if !second.Replay {
		t.Error("second dispatch with the same key/action/target/params did not report Replay=true")
	}
	if second.CommandID != first.CommandID {
		t.Errorf("replay CommandID = %q, want the original %q", second.CommandID, first.CommandID)
	}
	if srv.hitCount() != 1 {
		t.Errorf("after the replay, FPP received %d requests, want still 1 — a replay must dispatch nothing", srv.hitCount())
	}
}

// TestFPPCommandDispatcherUnsupportedAction proves Dispatch refuses an
// unrecognized wire action the same way the HTTP handler does (a
// caller-facing problem, not an internal error), without ever reaching
// the store or FPP.
func TestFPPCommandDispatcherUnsupportedAction(t *testing.T) {
	srv := newFailIfHitFPPCommandServer(t)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: srv.URL}}
	dispatcher := NewFPPCommandDispatcher(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	_, problem, err := dispatcher.Dispatch(context.Background(), FPPCommandInput{
		InstanceID:     "bench-fpp",
		Action:         "reticulateSplines",
		IdempotencyKey: "key-1",
		Issuer:         FPPCommandIssuer{PrincipalID: "op-1", PrincipalName: "operator-1"},
	})
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem == nil {
		t.Fatal("Dispatch did not refuse an unsupported action")
	}
	if problem.Status != http.StatusBadRequest {
		t.Errorf("problem.Status = %d, want 400", problem.Status)
	}
}

// TestFPPCommandSafetyClassForAction proves the exported safety-class
// lookup Step 9's macro executor needs for ADR-031 decision 5 as accepted:
// "A step's own action decides whether that step is exempt." The lookup is
// per step and must never be aggregated into one run-wide class. Decision
// 5's own record quotes and rejects the run-wide draft, because a run that
// inherits an exemption from any one of its steps turns a stop step into a
// way to launder an unattributable start. This comment carried that
// rejected draft as though it were the decision until 2026-08-14, which is
// worth noting here rather than silently correcting: the reviewer who
// reads a test to learn what a function is for reads this. Proved without
// reaching into [fppCommandPrimitives] itself. stopPlaylist and
// stopPlaylistGracefully are the only two members of ADR-024 decision 11's
// named safety class; every other registered primitive is not; an
// unregistered action reports ok=false rather than a guessed class.
func TestFPPCommandSafetyClassForAction(t *testing.T) {
	cases := []struct {
		action string
		want   FPPCommandSafetyClass
		wantOK bool
	}{
		{"stopPlaylist", FPPCommandSafetyClassExempt, true},
		{"stopPlaylistGracefully", FPPCommandSafetyClassExempt, true},
		{"startPlaylist", FPPCommandSafetyClassNotExempt, true},
		{"pausePlaylist", FPPCommandSafetyClassNotExempt, true},
		{"resumePlaylist", FPPCommandSafetyClassNotExempt, true},
		{"nextPlaylistItem", FPPCommandSafetyClassNotExempt, true},
		{"prevPlaylistItem", FPPCommandSafetyClassNotExempt, true},
		{"setVolume", FPPCommandSafetyClassNotExempt, true},
		{"notARealAction", 0, false},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			got, ok := FPPCommandSafetyClassForAction(c.action)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("class = %v, want %v", got, c.want)
			}
		})
	}
}

// TestFPPCommandDispatcherThreadsRequestedRevisionAndRunID proves
// [FPPCommandInput.RequestedRevision] reaches the dispatched command's own
// store row and [FPPCommandIssuer.RunID] reaches every audit entry that
// row's dispatch writes (STEP-9-SPEC.md section 6.1's "commands.
// requested_revision carries the pinned macro revision" and section 2.9's
// "each step's commands row records the issuing principal and the run
// id" - the run id, absent a dedicated column, travels in the audit
// entry's own Params instead; see [FPPCommandIssuer.RunID]'s doc comment).
func TestFPPCommandDispatcherThreadsRequestedRevisionAndRunID(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	dispatcher := NewFPPCommandDispatcher(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	outcome, problem, err := dispatcher.Dispatch(context.Background(), FPPCommandInput{
		InstanceID:        "bench-fpp",
		Action:            "stopPlaylist",
		IdempotencyKey:    "key-revision-runid",
		RequestedRevision: "macro:begin-set@3",
		Issuer:            FPPCommandIssuer{PrincipalID: "op-1", PrincipalName: "operator-1", RunID: "run-abc123"},
	})
	if err != nil || problem != nil {
		t.Fatalf("Dispatch: err=%v problem=%+v", err, problem)
	}

	rec, err := setup.st.GetCommand(context.Background(), outcome.CommandID)
	if err != nil {
		t.Fatalf("GetCommand: %v", err)
	}
	if rec.RequestedRevision != "macro:begin-set@3" {
		t.Errorf("commands.requested_revision = %q, want %q", rec.RequestedRevision, "macro:begin-set@3")
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var dispatchEntry, outcomeEntry *identity.AuditEntry
	for i := range entries {
		if entries[i].CommandID != outcome.CommandID {
			continue
		}
		switch entries[i].Kind {
		case identity.AuditDispatch:
			dispatchEntry = &entries[i]
		case identity.AuditOutcome:
			outcomeEntry = &entries[i]
		}
	}
	if dispatchEntry == nil {
		t.Fatal("no dispatch audit entry found for this command")
	}
	if dispatchEntry.Params["runId"] != "run-abc123" {
		t.Errorf("dispatch audit entry Params[runId] = %v, want %q", dispatchEntry.Params["runId"], "run-abc123")
	}
	if outcomeEntry == nil {
		t.Fatal("no outcome audit entry found for this command")
	}
	if outcomeEntry.Params["runId"] != "run-abc123" {
		t.Errorf("outcome audit entry Params[runId] = %v, want %q", outcomeEntry.Params["runId"], "run-abc123")
	}
}

// TestFPPCommandDispatcherOmitsRunIDParamsWhenAbsent is the negative
// control: an ordinary caller that never sets
// [FPPCommandIssuer.RunID] (every HTTP-dispatched command, and every
// pre-Step-9 test in this package) must write audit entries with no
// "runId" key at all, not a present-but-empty one - matching this
// package's own absent/empty distinction applied to evidence.
func TestFPPCommandDispatcherOmitsRunIDParamsWhenAbsent(t *testing.T) {
	fppSrv, _ := newFakeFPPCommandServer(t, http.StatusOK, "Stopped")
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppSrv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	dispatcher := NewFPPCommandDispatcher(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	outcome, problem, err := dispatcher.Dispatch(context.Background(), FPPCommandInput{
		InstanceID:     "bench-fpp",
		Action:         "stopPlaylist",
		IdempotencyKey: "key-no-run-id",
		Issuer:         FPPCommandIssuer{PrincipalID: "op-1", PrincipalName: "operator-1"},
	})
	if err != nil || problem != nil {
		t.Fatalf("Dispatch: err=%v problem=%+v", err, problem)
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.CommandID != outcome.CommandID {
			continue
		}
		found = true
		if _, ok := e.Params["runId"]; ok {
			t.Errorf("audit entry (kind %v) carries a runId param with no RunID set on the issuer: %v", e.Kind, e.Params)
		}
	}
	if !found {
		t.Fatal("no audit entry found for this command")
	}
}

// TestFPPCommandDecision11ClassForAction proves the STEP-9-SPEC.md section
// 5.3 wire-vocabulary lookup a config layer needs to check that a
// show.action's declared safetyClass agrees with its bound FPP
// primitive's own registered class, in the same four-member enum that
// field itself uses.
func TestFPPCommandDecision11ClassForAction(t *testing.T) {
	cases := []struct {
		action string
		want   FPPCommandDecision11Class
		wantOK bool
	}{
		{"stopPlaylist", FPPCommandDecision11ClassStop, true},
		{"stopPlaylistGracefully", FPPCommandDecision11ClassStop, true},
		{"startPlaylist", FPPCommandDecision11ClassNone, true},
		{"pausePlaylist", FPPCommandDecision11ClassNone, true},
		{"resumePlaylist", FPPCommandDecision11ClassNone, true},
		{"nextPlaylistItem", FPPCommandDecision11ClassNone, true},
		{"prevPlaylistItem", FPPCommandDecision11ClassNone, true},
		{"setVolume", FPPCommandDecision11ClassNone, true},
		{"notARealAction", "", false},
	}
	for _, c := range cases {
		t.Run(c.action, func(t *testing.T) {
			got, ok := FPPCommandDecision11ClassForAction(c.action)
			if ok != c.wantOK {
				t.Fatalf("ok = %v, want %v", ok, c.wantOK)
			}
			if ok && got != c.want {
				t.Errorf("class = %v, want %v", got, c.want)
			}
		})
	}
}

// TestFPPCommandAllStepsExempt proves the submission-time "is this set of
// steps all exempt" combinator STEP-9-SPEC.md section 2.5 requires: every
// step exempt is the only true case, an unrecognized value fails closed
// (not exempt), and a zero-step run is vacuously true (STEP-9-SPEC.md
// section 5.4 makes that case unreachable in practice; this proves the
// function does not special-case it to something else).
func TestFPPCommandAllStepsExempt(t *testing.T) {
	cases := []struct {
		name    string
		classes []FPPCommandDecision11Class
		want    bool
	}{
		{"empty", nil, true},
		{"single stop", []FPPCommandDecision11Class{FPPCommandDecision11ClassStop}, true},
		{"single none", []FPPCommandDecision11Class{FPPCommandDecision11ClassNone}, false},
		{"blackout and powerOff", []FPPCommandDecision11Class{FPPCommandDecision11ClassBlackout, FPPCommandDecision11ClassPowerOff}, true},
		{"stop then none", []FPPCommandDecision11Class{FPPCommandDecision11ClassStop, FPPCommandDecision11ClassNone}, false},
		{"unrecognized value fails closed", []FPPCommandDecision11Class{"reticulateSplines"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FPPCommandAllStepsExempt(c.classes...); got != c.want {
				t.Errorf("FPPCommandAllStepsExempt(%v) = %v, want %v", c.classes, got, c.want)
			}
		})
	}
}

// TestFPPCommandAuditUnavailableProblemMatchesRealRefusal proves the
// exported [FPPCommandAuditUnavailableProblem] is not a second,
// independently worded refusal: dispatching a non-exempt primitive
// (startPlaylist) against a real, genuinely failing audit_log
// ([installFailAuditTrigger]) is refused with exactly the Type and Status
// the exported constructor itself would build - Step 9's macro executor
// needs this for the identical submission-time refusal STEP-9-SPEC.md
// section 2.5 requires, and a drifted second copy would defeat the point
// of exposing one.
func TestFPPCommandAuditUnavailableProblemMatchesRealRefusal(t *testing.T) {
	srv := newFailIfHitFPPCommandServer(t)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: srv.URL}}
	setup.obs.setObs([]observation.Observation{fppStatusObs("bench-fpp", "idle", testNow, testNow)})

	installFailAuditTrigger(t, setup.storeDir)

	dispatcher := NewFPPCommandDispatcher(setup.deps(), Options{
		Clock: fixedClock(testNow), Logger: testLogger(),
		FPPCommandConfirmDeadline: 200 * time.Millisecond, FPPCommandPollInterval: 10 * time.Millisecond,
	})

	_, problem, err := dispatcher.Dispatch(context.Background(), FPPCommandInput{
		InstanceID: "bench-fpp",
		Action:     "startPlaylist",
		// ifBusy is required here even though it is optional on the wire:
		// the wire decoder's own default (decodeFPPCommandParams,
		// fppcommand_params.go) never runs on this in-process path - see
		// [FPPCommandInput.Params]'s own doc comment, "already-normalized,
		// natively-typed values" - so a caller is responsible for
		// supplying it already resolved, exactly as Step 9's macro
		// executor's own write-time [fppPrimitive.ValidateParams] check
		// (STEP-9-SPEC.md section 5.3) will have required of it already.
		Params:         map[string]any{"playlist": "showmesh-test", "ifBusy": fppIfBusyRefuse},
		IdempotencyKey: "key-audit-unavailable",
		Issuer:         FPPCommandIssuer{PrincipalID: "op-1", PrincipalName: "operator-1"},
	})
	if err != nil {
		t.Fatalf("unexpected internal error: %v", err)
	}
	if problem == nil {
		t.Fatal("Dispatch did not refuse a non-exempt primitive with the audit store failing")
	}

	want := FPPCommandAuditUnavailableProblem("begin-set", identity.ErrAuditWrite)
	if problem.Type != want.Type {
		t.Errorf("problem.Type = %q, want %q (the exported constructor's own type)", problem.Type, want.Type)
	}
	if problem.Status != want.Status {
		t.Errorf("problem.Status = %d, want %d", problem.Status, want.Status)
	}
}
