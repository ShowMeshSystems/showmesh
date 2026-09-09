package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file drives POST /api/v1/fpp/{instanceId}/brightness/transition-gain
// through a real [API] against a real HTTP fake standing in for the FPP
// plugin - never a hand-built wire struct asserted against a mock, and
// never a substituted writer, so the real fppcommand.Client and its real
// decode are what these tests exercise.

// fakeTransitionGainPlugin stands in for the resident FPP plugin's own
// POST /api/plugin-apis/showmesh/brightness/transition-gain. Every request
// is recorded so a refused write can be proven to have dispatched nothing.
type fakeTransitionGainPlugin struct {
	mu       sync.Mutex
	requests []string
	status   int
	body     string
}

func newFakeTransitionGainPlugin(t *testing.T, status int, body string) (*httptest.Server, *fakeTransitionGainPlugin) {
	t.Helper()
	f := &fakeTransitionGainPlugin{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		f.mu.Lock()
		f.requests = append(f.requests, string(raw[:n]))
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeTransitionGainPlugin) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeTransitionGainPlugin) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return ""
	}
	return f.requests[len(f.requests)-1]
}

// transitionGainAPI wires a real coordinator whose single configured FPP
// endpoint is pluginURL, plus an operator token for it.
func transitionGainAPI(t *testing.T, pluginURL string) (*API, *fppCommandTestSetup, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: pluginURL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	return api, setup, mustIssueToken(t, setup.svc, operator.ID)
}

func transitionGainRequest(t *testing.T, instanceID, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fpp/"+instanceID+"/brightness/transition-gain", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

type transitionGainResultForTest struct {
	InstanceID      string `json:"instanceId"`
	RequestID       string `json:"requestId"`
	Applied         bool   `json:"applied"`
	GainStart       int    `json:"gainStart"`
	GainTarget      int    `json:"gainTarget"`
	FadeSeconds     int    `json:"fadeSeconds"`
	Ceiling         int    `json:"ceiling"`
	EffectiveOutput int    `json:"effectiveOutput"`
}

type transitionGainResponseForTest struct {
	ServerTime     string                      `json:"serverTime"`
	TransitionGain transitionGainResultForTest `json:"transitionGain"`
}

func decodeTransitionGain(t *testing.T, body []byte) transitionGainResultForTest {
	t.Helper()
	var resp transitionGainResponseForTest
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode transition gain response: %v; body: %s", err, body)
	}
	return resp.TransitionGain
}

// The plugin's own section 2.2 response, applied.
const appliedGainBody = `{"schemaVersion":1,"applied":true,"gainStart":100,"gainTarget":75,"fadeSeconds":30,"ceiling":60,"effectiveOutput":45}`

// TestFPPTransitionGainReportsTheAppliedState is this route's whole
// point: the response carries what the host is now doing, not "the
// request did not error".
func TestFPPTransitionGainReportsTheAppliedState(t *testing.T) {
	srv, plugin := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	api, _, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeTransitionGain(t, body)
	want := transitionGainResultForTest{
		InstanceID: "bench-fpp", RequestID: "req-1", Applied: true,
		GainStart: 100, GainTarget: 75, FadeSeconds: 30, Ceiling: 60, EffectiveOutput: 45,
	}
	if got != want {
		t.Errorf("transitionGain = %+v, want %+v", got, want)
	}
	if plugin.hitCount() != 1 {
		t.Fatalf("plugin received %d requests, want exactly 1", plugin.hitCount())
	}
	if sent := plugin.lastBody(); !strings.Contains(sent, `"requestId":"req-1"`) || !strings.Contains(sent, `"targetPercent":75`) {
		t.Errorf("dispatched body = %s, want the caller's own requestId and targetPercent", sent)
	}
}

// TestFPPTransitionGainAppliedFalseIsASuccess is the single most likely
// thing to get wrong: a repeated requestId comes back applied=false with
// the gain unchanged, which is the idempotency key working. Answering an
// error there would tell a caller to retry a write that already took.
func TestFPPTransitionGainAppliedFalseIsASuccess(t *testing.T) {
	const repeatBody = `{"schemaVersion":1,"applied":false,"gainStart":75,"gainTarget":75,"fadeSeconds":0,"ceiling":60,"effectiveOutput":45}`
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusOK, repeatBody)
	api, _, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 - applied=false is a success, not a failure; body: %s", resp.StatusCode, body)
	}
	got := decodeTransitionGain(t, body)
	if got.Applied {
		t.Fatalf("Applied = true, want false")
	}
	if got.GainTarget != 75 || got.Ceiling != 60 || got.EffectiveOutput != 45 {
		t.Errorf("applied=false response = %+v, want the gain as it stands reported, not zeroes", got)
	}
}

// TestFPPTransitionGainRefusesOutOfRangeAndDispatchesNothing: refused,
// never clamped, and refused before a request is spent.
func TestFPPTransitionGainRefusesOutOfRangeAndDispatchesNothing(t *testing.T) {
	for _, tt := range []struct {
		name, body, wantIn string
	}{
		{"percent above 100", `{"targetPercent":101,"fadeSeconds":0}`, "targetPercent 101"},
		{"percent below 0", `{"targetPercent":-1,"fadeSeconds":0}`, "targetPercent -1"},
		{"fade above the bound", `{"targetPercent":50,"fadeSeconds":86401}`, "fadeSeconds 86401"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, plugin := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
			api, _, token := transitionGainAPI(t, srv.URL)

			resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, "bench-fpp", tt.body, token))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			detail, _ := decodeMap(t, body)["detail"].(string)
			if !strings.Contains(detail, tt.wantIn) {
				t.Errorf("problem detail = %q, want it to name %q so the mistyped value stays visible", detail, tt.wantIn)
			}
			if plugin.hitCount() != 0 {
				t.Errorf("plugin received %d requests, want 0 - a refused value must never be clamped and sent", plugin.hitCount())
			}
		})
	}
}

// TestFPPTransitionGainUnknownInstanceIsACleanProblem: an instance id
// naming no configured endpoint answers a 404 problem, never a panic and
// never a write to some other host.
func TestFPPTransitionGainUnknownInstanceIsACleanProblem(t *testing.T) {
	srv, plugin := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	api, _, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "no-such-fpp", `{"targetPercent":50,"fadeSeconds":0}`, token))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeResourceNotFound {
		t.Errorf("problem type = %v, want %v", m["type"], ProblemTypeResourceNotFound)
	}
	if detail, _ := m["detail"].(string); !strings.Contains(detail, "no-such-fpp") {
		t.Errorf("problem detail = %q, want it to name the instance id", detail)
	}
	if plugin.hitCount() != 0 {
		t.Errorf("plugin received %d requests, want 0", plugin.hitCount())
	}
}

// TestFPPTransitionGainMintsARequestIDWhenAbsent: a caller may omit the
// key, and the value actually used is reported back so that caller can
// still retry with it.
func TestFPPTransitionGainMintsARequestIDWhenAbsent(t *testing.T) {
	srv, plugin := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	api, _, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":75,"fadeSeconds":30}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeTransitionGain(t, body)
	if got.RequestID == "" {
		t.Fatal("RequestID = \"\", want the minted key reported back")
	}
	if !strings.Contains(plugin.lastBody(), `"requestId":"`+got.RequestID+`"`) {
		t.Errorf("dispatched body = %s, want the same requestId the response reports (%s)", plugin.lastBody(), got.RequestID)
	}
}

// TestFPPTransitionGainRefusedForbiddenViewerNamesScope pins the scope
// this route actually requires.
func TestFPPTransitionGainRefusedForbiddenViewerNamesScope(t *testing.T) {
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: srv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":50,"fadeSeconds":0}`, token))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if detail, _ := decodeMap(t, body)["detail"].(string); !strings.Contains(detail, "fpp:command") {
		t.Errorf("problem detail = %q, want it to name the missing scope fpp:command", detail)
	}
}

// TestFPPTransitionGainWriteFailureIsItsOwnProblem: a write that reaches a
// configured host and does not take is reported as an upstream failure
// carrying the host's own words, never as a success.
func TestFPPTransitionGainWriteFailureIsItsOwnProblem(t *testing.T) {
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusInternalServerError, `{"error":"brightness engine unavailable"}`)
	api, _, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":50,"fadeSeconds":0}`, token))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", resp.StatusCode, body)
	}
	if got := decodeMap(t, body)["type"]; got != ProblemTypeFPPTransitionGainWriteFailed {
		t.Errorf("problem type = %v, want %v", got, ProblemTypeFPPTransitionGainWriteFailed)
	}
}

// TestFPPTransitionGainRejectsAbsentAndNullRequiredFields keeps absent,
// null and zero three different things: 0 is a real gain (blackout), so a
// forgotten field must never be read as one.
func TestFPPTransitionGainRejectsAbsentAndNullRequiredFields(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"targetPercent absent", `{"fadeSeconds":0}`},
		{"targetPercent null", `{"targetPercent":null,"fadeSeconds":0}`},
		{"fadeSeconds absent", `{"targetPercent":50}`},
		{"fadeSeconds null", `{"targetPercent":50,"fadeSeconds":null}`},
		{"unrecognized field", `{"targetPercent":50,"fadeSeconds":0,"ceiling":80}`},
		{"empty requestId", `{"targetPercent":50,"fadeSeconds":0,"requestId":""}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, plugin := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
			api, _, token := transitionGainAPI(t, srv.URL)
			resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, "bench-fpp", tt.body, token))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			if plugin.hitCount() != 0 {
				t.Errorf("plugin received %d requests, want 0", plugin.hitCount())
			}
		})
	}
}

// TestFPPTransitionGainAuditsAnIdempotentRepeatAsChangingNothing: the
// audit trail must show that a repeat changed nothing, which is exactly
// the case where an operator did not get their answer the first time.
func TestFPPTransitionGainAuditsAnIdempotentRepeatAsChangingNothing(t *testing.T) {
	const repeatBody = `{"schemaVersion":1,"applied":false,"gainStart":75,"gainTarget":75,"fadeSeconds":0,"ceiling":60,"effectiveOutput":45}`
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusOK, repeatBody)
	api, setup, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, "bench-fpp", `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var dispatch, outcome *identity.AuditEntry
	for i := range entries {
		if entries[i].Action != auditActionFPPTransitionGain {
			continue
		}
		switch entries[i].Kind {
		case identity.AuditDispatch:
			dispatch = &entries[i]
		case identity.AuditOutcome:
			outcome = &entries[i]
		}
	}
	if dispatch == nil || outcome == nil {
		t.Fatalf("audit entries for %s: dispatch = %v, outcome = %v, want both", auditActionFPPTransitionGain, dispatch, outcome)
	}
	if dispatch.IdempotencyKey != "req-1" || dispatch.Target != "bench-fpp" {
		t.Errorf("dispatch entry = %+v, want it to record the requestId and the instance", dispatch)
	}
	if !strings.Contains(outcome.OutcomeReason, "already applied") {
		t.Errorf("outcome reason = %q, want it to say the requestId was already applied and nothing changed", outcome.OutcomeReason)
	}
	if dispatch.CommandID == "" || dispatch.CommandID != outcome.CommandID {
		t.Errorf("commandId dispatch = %q, outcome = %q, want one non-empty value correlating them", dispatch.CommandID, outcome.CommandID)
	}
}
