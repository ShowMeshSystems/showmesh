package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file drives POST /api/v1/fpp/brightness/transition-gain
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

// transitionGainAPI wires a real coordinator whose configured FPP endpoints
// are pluginURLs, named bench-fpp, bench-fpp-2 and on, plus an operator token.
func transitionGainAPI(t *testing.T, pluginURLs ...string) (*API, *fppCommandTestSetup, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	for i, u := range pluginURLs {
		id := "bench-fpp"
		if i > 0 {
			id = fmt.Sprintf("bench-fpp-%d", i+1)
		}
		setup.fppLister.views = append(setup.fppLister.views, FPPInstanceView{InstanceID: id, Endpoint: u})
	}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	return api, setup, mustIssueToken(t, setup.svc, operator.ID)
}

func transitionGainRequest(t *testing.T, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fpp/brightness/transition-gain", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

type transitionGainResultForTest struct {
	InstanceID      string `json:"instanceId"`
	RequestID       string `json:"requestId"`
	Outcome         string `json:"outcome"`
	Reason          string `json:"reason"`
	GainStart       *int   `json:"gainStart"`
	GainTarget      *int   `json:"gainTarget"`
	FadeSeconds     *int   `json:"fadeSeconds"`
	Ceiling         *int   `json:"ceiling"`
	EffectiveOutput *int   `json:"effectiveOutput"`
}

func decodeTransitionGain(t *testing.T, body []byte) []transitionGainResultForTest {
	t.Helper()
	var resp struct {
		Players []transitionGainResultForTest `json:"players"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode transition gain response: %v; body: %s", err, body)
	}
	return resp.Players
}

// The plugin's own section 2.2 response, applied.
const appliedGainBody = `{"schemaVersion":1,"applied":true,"gainStart":100,"gainTarget":75,"fadeSeconds":30,"ceiling":60,"effectiveOutput":45}`

func TestFPPTransitionGainWritesEveryFPPWithItsOwnRequestID(t *testing.T) {
	srvA, a := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	srvB, b := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	api, _, token := transitionGainAPI(t, srvA.URL, srvB.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeTransitionGain(t, body)
	if len(got) != 2 {
		t.Fatalf("players = %+v, want 2", got)
	}
	for i, p := range got {
		if p.Outcome != "applied" || *p.GainTarget != 75 || *p.Ceiling != 60 || *p.EffectiveOutput != 45 || p.Reason != "" {
			t.Errorf("player %d = %+v, want applied with the plugin's state", i, p)
		}
	}
	if got[0].RequestID == got[1].RequestID {
		t.Errorf("both FPPs carried requestId %q, want one each", got[0].RequestID)
	}
	for i, f := range []*fakeTransitionGainPlugin{a, b} {
		if f.hitCount() != 1 {
			t.Fatalf("plugin %d received %d requests, want 1", i, f.hitCount())
		}
		if sent := f.lastBody(); !strings.Contains(sent, `"requestId":"`+got[i].RequestID+`"`) || !strings.Contains(sent, `"targetPercent":75`) {
			t.Errorf("plugin %d body = %s, want requestId %s and targetPercent 75", i, sent, got[i].RequestID)
		}
	}
}

func TestFPPTransitionGainOneFailureDoesNotStopTheOthers(t *testing.T) {
	bad, _ := newFakeTransitionGainPlugin(t, http.StatusInternalServerError, `{"error":"brightness engine unavailable"}`)
	good, g := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	api, _, token := transitionGainAPI(t, bad.URL, good.URL)

	resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, `{"targetPercent":50,"fadeSeconds":0}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 with each FPP's outcome; body: %s", resp.StatusCode, body)
	}
	got := decodeTransitionGain(t, body)
	if got[0].Outcome != "failed" || got[0].Reason == "" || got[0].GainTarget != nil {
		t.Errorf("failed FPP = %+v, want failed with a reason and no numbers", got[0])
	}
	if !strings.Contains(got[0].Reason, "bench-fpp") {
		t.Errorf("reason = %q, want it to name the player", got[0].Reason)
	}
	if got[1].Outcome != "applied" || g.hitCount() != 1 {
		t.Errorf("healthy FPP = %+v (hits %d), want applied", got[1], g.hitCount())
	}
}

// TestFPPTransitionGainAppliedFalseIsASuccess: a repeated requestId is the
// idempotency key working, not a failure.
func TestFPPTransitionGainAppliedFalseIsASuccess(t *testing.T) {
	const repeatBody = `{"schemaVersion":1,"applied":false,"gainStart":75,"gainTarget":75,"fadeSeconds":0,"ceiling":60,"effectiveOutput":45}`
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusOK, repeatBody)
	api, _, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeTransitionGain(t, body)[0]
	if got.Outcome != "alreadyApplied" || *got.GainTarget != 75 || *got.Ceiling != 60 {
		t.Errorf("repeat = %+v, want alreadyApplied with the gain as it stands", got)
	}
}

func TestFPPTransitionGainRefusesOutOfRangeAndDispatchesNothing(t *testing.T) {
	for _, tt := range []struct {
		name, body, wantIn string
	}{
		{"percent above 100", `{"targetPercent":101,"fadeSeconds":0}`, "101%"},
		{"percent below 0", `{"targetPercent":-1,"fadeSeconds":0}`, "-1%"},
		{"fade above the bound", `{"targetPercent":50,"fadeSeconds":86401}`, "86401 seconds"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, plugin := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
			api, _, token := transitionGainAPI(t, srv.URL)

			resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, tt.body, token))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			detail, _ := decodeMap(t, body)["detail"].(string)
			if !strings.Contains(detail, tt.wantIn) {
				t.Errorf("problem detail = %q, want it to name %q", detail, tt.wantIn)
			}
			if plugin.hitCount() != 0 {
				t.Errorf("plugin received %d requests, want 0", plugin.hitCount())
			}
		})
	}
}

func TestFPPTransitionGainWithNoFPPsIsAConflict(t *testing.T) {
	api, _, token := transitionGainAPI(t)
	resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, `{"targetPercent":50,"fadeSeconds":0}`, token))
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", resp.StatusCode, body)
	}
}

func TestFPPTransitionGainMintsARequestIDWhenAbsent(t *testing.T) {
	srv, plugin := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	api, _, token := transitionGainAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, `{"targetPercent":75,"fadeSeconds":30}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeTransitionGain(t, body)[0]
	if got.RequestID == "" || !strings.Contains(plugin.lastBody(), `"requestId":"`+got.RequestID+`"`) {
		t.Errorf("requestId %q, dispatched %s; want the minted key sent and reported", got.RequestID, plugin.lastBody())
	}
}

func TestFPPTransitionGainRefusedForbiddenViewerNamesScope(t *testing.T) {
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusOK, appliedGainBody)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: srv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)

	resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, `{"targetPercent":50,"fadeSeconds":0}`, token))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if detail, _ := decodeMap(t, body)["detail"].(string); !strings.Contains(detail, "fpp:command") {
		t.Errorf("problem detail = %q, want it to name the missing scope fpp:command", detail)
	}
}

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
			resp, body := doRawRequest(t, api.Handler, transitionGainRequest(t, tt.body, token))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			if plugin.hitCount() != 0 {
				t.Errorf("plugin received %d requests, want 0", plugin.hitCount())
			}
		})
	}
}

// TestFPPTransitionGainAuditsEachFPPsOutcome: one dispatch entry for the
// display, one outcome per FPP, a repeat recorded as changing nothing.
func TestFPPTransitionGainAuditsEachFPPsOutcome(t *testing.T) {
	const repeatBody = `{"schemaVersion":1,"applied":false,"gainStart":75,"gainTarget":75,"fadeSeconds":0,"ceiling":60,"effectiveOutput":45}`
	srv, _ := newFakeTransitionGainPlugin(t, http.StatusOK, repeatBody)
	bad, _ := newFakeTransitionGainPlugin(t, http.StatusInternalServerError, `{"error":"nope"}`)
	api, setup, token := transitionGainAPI(t, srv.URL, bad.URL)

	resp, body := doRawRequest(t, api.Handler,
		transitionGainRequest(t, `{"targetPercent":75,"fadeSeconds":30,"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var dispatch *identity.AuditEntry
	outcomes := map[string]identity.AuditEntry{}
	for i := range entries {
		if entries[i].Action != auditActionFPPTransitionGain {
			continue
		}
		switch entries[i].Kind {
		case identity.AuditDispatch:
			dispatch = &entries[i]
		case identity.AuditOutcome:
			outcomes[entries[i].Target] = entries[i]
		}
	}
	if dispatch == nil || dispatch.IdempotencyKey != "req-1" || dispatch.Target != "display" {
		t.Fatalf("dispatch entry = %+v, want one for the display carrying req-1", dispatch)
	}
	if o := outcomes["bench-fpp"]; !strings.Contains(o.OutcomeReason, "already applied") || o.CommandID != dispatch.CommandID {
		t.Errorf("bench-fpp outcome = %+v, want an already-applied entry correlated to the dispatch", o)
	}
	if o := outcomes["bench-fpp-2"]; o.Outcome != outcomeWordFailed {
		t.Errorf("bench-fpp-2 outcome = %+v, want failed", o)
	}
}
