package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file drives POST /api/v1/fpp/{instanceId}/brightness/ceiling
// through a real [API] against an HTTP fake standing in for FPP and its
// resident plugin, so the real command client and the real read-back are
// what these tests exercise.

// fakeCeilingFPP answers FPP's own POST /api/command and the plugin's
// GET brightness route. readBack is what the plugin reports; nil means
// the plugin is not installed and the GET is a 404.
type fakeCeilingFPP struct {
	mu        sync.Mutex
	commands  []string
	readBack  *int
	applyOnce bool
}

func newFakeCeilingFPP(t *testing.T, readBack *int, applyOnce bool) (*httptest.Server, *fakeCeilingFPP) {
	t.Helper()
	f := &fakeCeilingFPP{readBack: readBack, applyOnce: applyOnce}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/command":
			raw := make([]byte, 4096)
			n, _ := r.Body.Read(raw)
			f.mu.Lock()
			f.commands = append(f.commands, string(raw[:n]))
			if f.applyOnce {
				var req struct {
					Args []string `json:"args"`
				}
				if err := json.Unmarshal(raw[:n], &req); err == nil && len(req.Args) == 1 {
					var applied int
					if _, err := fmt.Sscanf(req.Args[0], "%d", &applied); err == nil {
						f.readBack = &applied
					}
				}
			}
			f.mu.Unlock()
			_, _ = w.Write([]byte("Brightness Ceiling Set"))
		case r.Method == http.MethodGet && r.URL.Path == "/api/plugin-apis/showmesh/brightness":
			f.mu.Lock()
			got := f.readBack
			f.mu.Unlock()
			if got == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"schemaVersion":1,"ceiling":%d,"transitionGain":100,"effectiveOutput":%d,"fadeActive":false,"updatedAtMillis":1}`, *got, *got)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeCeilingFPP) commandCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.commands)
}

func (f *fakeCeilingFPP) lastCommand() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.commands) == 0 {
		return ""
	}
	return f.commands[len(f.commands)-1]
}

func ceilingAPI(t *testing.T, fppURL string) (*API, *fppCommandTestSetup, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: fppURL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	return api, setup, mustIssueToken(t, setup.svc, operator.ID)
}

func ceilingRequest(t *testing.T, instanceID, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/fpp/"+instanceID+"/brightness/ceiling", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

type ceilingResponseForTest struct {
	Command struct {
		Action        string `json:"action"`
		Outcome       string `json:"outcome"`
		OutcomeState  string `json:"outcomeState"`
		OutcomeReason string `json:"outcomeReason"`
	} `json:"command"`
	Ceiling *int `json:"ceiling"`
}

func decodeCeiling(t *testing.T, body []byte) ceilingResponseForTest {
	t.Helper()
	var out ceilingResponseForTest
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode ceiling response: %v; body: %s", err, body)
	}
	return out
}

// TestBrightnessCeilingDispatchesFPPsOwnCommandAndReadsItBack.
func TestBrightnessCeilingDispatchesFPPsOwnCommandAndReadsItBack(t *testing.T) {
	srv, fpp := newFakeCeilingFPP(t, nil, true)
	api, _, token := ceilingAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":60}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ceiling write status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if got := fpp.lastCommand(); !strings.Contains(got, `"command":"ShowMesh: Set Brightness Ceiling"`) || !strings.Contains(got, `"args":["60"]`) {
		t.Fatalf("dispatched command = %s, want the plugin's own command with one argument", got)
	}
	got := decodeCeiling(t, body)
	if got.Ceiling == nil || *got.Ceiling != 60 {
		t.Fatalf("ceiling = %v, want 60 read back", got.Ceiling)
	}
	if got.Command.Outcome != outcomeWordConfirmed || got.Command.Action != fppBrightnessCeilingAction {
		t.Fatalf("command = %+v, want a confirmed setBrightnessCeiling", got.Command)
	}
}

// TestBrightnessCeilingIsUnconfirmedWhenThePluginNeverReportsIt: FPP
// answering 200 is not confirmation, and the response must not pretend it
// is.
func TestBrightnessCeilingIsUnconfirmedWhenThePluginNeverReportsIt(t *testing.T) {
	srv, _ := newFakeCeilingFPP(t, nil, false)
	api, _, token := ceilingAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":40}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ceiling write status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeCeiling(t, body)
	if got.Ceiling != nil {
		t.Fatalf("ceiling = %v, want absent when the plugin never reported it", *got.Ceiling)
	}
	if got.Command.Outcome != outcomeWordUnconfirmed {
		t.Fatalf("outcome = %q, want unconfirmed", got.Command.Outcome)
	}
}

// TestBrightnessCeilingRefusesOutOfRangeAndDispatchesNothing.
func TestBrightnessCeilingRefusesOutOfRangeAndDispatchesNothing(t *testing.T) {
	for _, body := range []string{`{"ceiling":101}`, `{"ceiling":-1}`, `{"ceiling":null}`, `{}`, `{"ceiling":"60"}`, `{"ceiling":60,"nope":1}`} {
		srv, fpp := newFakeCeilingFPP(t, nil, true)
		api, _, token := ceilingAPI(t, srv.URL)

		resp, got := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", body, token))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400; response: %s", body, resp.StatusCode, got)
		}
		if fpp.commandCount() != 0 {
			t.Fatalf("body %s dispatched %d commands, want none", body, fpp.commandCount())
		}
	}
}

// TestBrightnessCeilingRefusesAnUnconfiguredInstance.
func TestBrightnessCeilingRefusesAnUnconfiguredInstance(t *testing.T) {
	srv, fpp := newFakeCeilingFPP(t, nil, true)
	api, _, token := ceilingAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, "no-such-fpp", `{"ceiling":50}`, token))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured instance status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	if fpp.commandCount() != 0 {
		t.Fatalf("an unconfigured instance dispatched %d commands, want none", fpp.commandCount())
	}
}

// TestBrightnessCeilingReportsAHostThatRefusedTheCommand.
func TestBrightnessCeilingReportsAHostThatRefusedTheCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("No Command: ShowMesh: Set Brightness Ceiling"))
	}))
	t.Cleanup(srv.Close)
	api, _, token := ceilingAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":50}`, token))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("refused command status = %d, want 502; body: %s", resp.StatusCode, body)
	}
}

// TestBrightnessCeilingCarriesFPPsOwnRefusalText: an FPP without the
// plugin installed answers 500 "No Command", and an operator needs that
// sentence, not a coordinator paraphrase.
func TestBrightnessCeilingCarriesFPPsOwnRefusalText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("No Command: ShowMesh: Set Brightness Ceiling"))
	}))
	t.Cleanup(srv.Close)
	api, setup, token := ceilingAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":50}`, token))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "No Command") {
		t.Fatalf("the refusal did not carry FPP's own text: %s", body)
	}

	entries, err := setup.svc.ListAuditNewestFirst(t.Context(), 0, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	found := false
	for _, e := range entries {
		if e.Action == auditActionFPPSetBrightnessCeiling && e.Kind == identity.AuditOutcome {
			found = true
			if e.Outcome != outcomeWordFailed {
				t.Errorf("audited outcome = %q, want failed", e.Outcome)
			}
			if !strings.Contains(e.OutcomeReason, "No Command") {
				t.Errorf("audited reason = %q, want FPP's own text", e.OutcomeReason)
			}
		}
	}
	if !found {
		t.Error("no outcome audit entry was written for a refused ceiling write")
	}
}

// TestBrightnessCeilingReportsWhatFPPActuallyAnswered when the plugin
// never reports the value back: FPP's own answer is evidence the command
// ran, and it is not evidence the ceiling changed.
func TestBrightnessCeilingReportsWhatFPPActuallyAnswered(t *testing.T) {
	srv, _ := newFakeCeilingFPP(t, nil, false)
	api, _, token := ceilingAPI(t, srv.URL)

	_, body := doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":40}`, token))
	got := decodeCeiling(t, body)
	if !strings.Contains(got.Command.OutcomeReason, "Brightness Ceiling Set") {
		t.Fatalf("outcomeReason = %q, want FPP's own answer quoted", got.Command.OutcomeReason)
	}
	if !strings.Contains(got.Command.OutcomeReason, "40") {
		t.Fatalf("outcomeReason = %q, want the ceiling that was not read back", got.Command.OutcomeReason)
	}
}

// TestBrightnessCeilingNeverTouchesTheTransitionGainRoute: the two
// values have separate writers and this one must not reach the other.
func TestBrightnessCeilingNeverTouchesTheTransitionGainRoute(t *testing.T) {
	srv, fpp := newFakeCeilingFPP(t, nil, true)
	api, _, token := ceilingAPI(t, srv.URL)

	doRawRequest(t, api.Handler, ceilingRequest(t, "bench-fpp", `{"ceiling":25}`, token))
	if got := fpp.lastCommand(); strings.Contains(strings.ToLower(got), "gain") {
		t.Fatalf("the ceiling write reached the transition gain: %s", got)
	}
}
