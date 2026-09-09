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

// This file drives POST
// /api/v1/fpp/{instanceId}/playlist-definitions/republish through a real
// [API] against a real HTTP fake standing in for the FPP plugin, never a
// hand-built wire struct asserted against a mock and never a substituted
// republisher, so the real fppcommand.Client and its real decode are what
// these tests exercise.

// fakeRepublishPlugin stands in for the resident FPP plugin's own
// POST /api/plugin-apis/showmesh/playlists/republish. Every request is
// recorded so a refused one can be proven to have dispatched nothing.
type fakeRepublishPlugin struct {
	mu       sync.Mutex
	requests []string
	paths    []string
	status   int
	body     string
}

func newFakeRepublishPlugin(t *testing.T, status int, body string) (*httptest.Server, *fakeRepublishPlugin) {
	t.Helper()
	f := &fakeRepublishPlugin{status: status, body: body}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw := make([]byte, 4096)
		n, _ := r.Body.Read(raw)
		f.mu.Lock()
		f.requests = append(f.requests, string(raw[:n]))
		f.paths = append(f.paths, r.URL.Path)
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.body))
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func (f *fakeRepublishPlugin) hitCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *fakeRepublishPlugin) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return ""
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeRepublishPlugin) lastPath() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.paths) == 0 {
		return ""
	}
	return f.paths[len(f.paths)-1]
}

// definitionRepublishAPI wires a real coordinator whose single configured
// FPP endpoint is pluginURL, plus an operator token for it.
func definitionRepublishAPI(t *testing.T, pluginURL string) (*API, *fppCommandTestSetup, string) {
	t.Helper()
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: pluginURL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	return api, setup, mustIssueToken(t, setup.svc, operator.ID)
}

func definitionRepublishRequest(t *testing.T, instanceID, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		"/api/v1/fpp/"+instanceID+"/playlist-definitions/republish", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

type definitionRepublishResultForTest struct {
	InstanceID                   string `json:"instanceId"`
	RequestID                    string `json:"requestId"`
	Applied                      bool   `json:"applied"`
	DefinitionsCleared           int    `json:"definitionsCleared"`
	DefinitionsHeld              int    `json:"definitionsHeld"`
	DefinitionsRefusedTerminally int    `json:"definitionsRefusedTerminally"`
	SweepPending                 bool   `json:"sweepPending"`
}

func decodeDefinitionRepublish(t *testing.T, body []byte) definitionRepublishResultForTest {
	t.Helper()
	var resp struct {
		ServerTime string                           `json:"serverTime"`
		Republish  definitionRepublishResultForTest `json:"republish"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode definition republish response: %v; body: %s", err, body)
	}
	return resp.Republish
}

// The plugin's own section 3.9 response, applied.
const appliedRepublishPluginBody = `{"schemaVersion":1,"applied":true,"definitionsCleared":6,` +
	`"definitionsHeld":0,"definitionsRefusedTerminally":1,"sweepPending":true}`

// TestFPPDefinitionRepublishRelaysThePluginsEvidence: the response carries
// what the plugin did to its own state, at the plugin-apis address, never
// a bare "the request did not error".
func TestFPPDefinitionRepublishRelaysThePluginsEvidence(t *testing.T) {
	srv, plugin := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
	api, _, token := definitionRepublishAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	got := decodeDefinitionRepublish(t, body)
	want := definitionRepublishResultForTest{
		InstanceID: "bench-fpp", RequestID: "req-1", Applied: true,
		DefinitionsCleared: 6, DefinitionsHeld: 0, DefinitionsRefusedTerminally: 1, SweepPending: true,
	}
	if got != want {
		t.Errorf("republish = %+v, want %+v", got, want)
	}
	if plugin.hitCount() != 1 {
		t.Fatalf("plugin received %d requests, want exactly 1", plugin.hitCount())
	}
	if p := plugin.lastPath(); p != "/api/plugin-apis/showmesh/playlists/republish" {
		t.Errorf("posted to %q, want the plugin-apis address", p)
	}
	if sent := plugin.lastBody(); !strings.Contains(sent, `"requestId":"req-1"`) || !strings.Contains(sent, `"schemaVersion":1`) {
		t.Errorf("dispatched body = %s, want the caller's own requestId and schemaVersion 1", sent)
	}
}

// TestFPPDefinitionRepublishDoesNotReportDefinitionsAsArrived is the
// requirement this route is most likely to get wrong. When the plugin
// answers, not one post of the sweep has been attempted, so the response
// must report the request as accepted and the sweep as OWED. It must
// carry no field claiming definitions were imported, received, or stored,
// and sweepPending must be true on an applied answer.
func TestFPPDefinitionRepublishDoesNotReportDefinitionsAsArrived(t *testing.T) {
	srv, _ := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
	api, _, token := definitionRepublishAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v; body: %s", err, body)
	}
	var result map[string]any
	if err := json.Unmarshal(envelope["republish"], &result); err != nil {
		t.Fatalf("decode republish: %v; body: %s", err, body)
	}

	// The whole set of fields, pinned. An added field claiming arrival
	// would fail here before anyone read its description.
	wantFields := map[string]bool{
		"instanceId": true, "requestId": true, "applied": true,
		"definitionsCleared": true, "definitionsHeld": true,
		"definitionsRefusedTerminally": true, "sweepPending": true,
	}
	for name := range result {
		if !wantFields[name] {
			t.Errorf("republish carries an undocumented field %q; this route reports acceptance, not arrival", name)
		}
	}
	for name := range wantFields {
		if _, ok := result[name]; !ok {
			t.Errorf("republish is missing field %q", name)
		}
	}
	for _, forbidden := range []string{
		"definitionsImported", "definitionsReceived", "definitionsStored",
		"definitionsAccepted", "definitionsPublished", "imported", "received",
	} {
		if _, ok := result[forbidden]; ok {
			t.Errorf("republish carries %q, which would claim an import that has not been attempted", forbidden)
		}
	}
	if pending, _ := result["sweepPending"].(bool); !pending {
		t.Error("sweepPending = false on an applied answer; the sweep is owed, not done")
	}
}

// TestFPPDefinitionRepublishAppliedFalseIsASuccess: a repeated requestId
// clears nothing and reports the state as it stands. That is the
// idempotency key working, and it is also the only way a caller learns
// the sweep finished, so answering an error would remove both.
func TestFPPDefinitionRepublishAppliedFalseIsASuccess(t *testing.T) {
	const repeatBody = `{"schemaVersion":1,"applied":false,"definitionsCleared":0,` +
		`"definitionsHeld":4,"definitionsRefusedTerminally":1,"sweepPending":false}`
	srv, _ := newFakeRepublishPlugin(t, http.StatusOK, repeatBody)
	api, _, token := definitionRepublishAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 - applied=false is a success, not a failure; body: %s", resp.StatusCode, body)
	}
	got := decodeDefinitionRepublish(t, body)
	if got.Applied {
		t.Fatal("Applied = true, want false")
	}
	if got.DefinitionsHeld != 4 || got.SweepPending {
		t.Errorf("repeat = %+v, want 4 held and sweepPending false, the state as it stands", got)
	}
}

// TestFPPDefinitionRepublishRefusesAnUndecodable200: a 200 whose body does
// not decode must never become a zero-valued result, which would read as
// "cleared nothing, holds nothing, no sweep owed" and is the most
// reassuring answer this shape can produce.
func TestFPPDefinitionRepublishRefusesAnUndecodable200(t *testing.T) {
	srv, _ := newFakeRepublishPlugin(t, http.StatusOK, "not json")
	api, _, token := definitionRepublishAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", resp.StatusCode, body)
	}
	if got := decodeMap(t, body)["type"]; got != ProblemTypeFPPDefinitionRepublishFailed {
		t.Errorf("problem type = %v, want %v", got, ProblemTypeFPPDefinitionRepublishFailed)
	}
}

// TestFPPDefinitionRepublishUnknownInstanceIsACleanProblem: an instance id
// naming no configured endpoint answers a 404 problem, never a panic and
// never a request to some other host.
func TestFPPDefinitionRepublishUnknownInstanceIsACleanProblem(t *testing.T) {
	srv, plugin := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
	api, _, token := definitionRepublishAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "no-such-fpp", `{}`, token))
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

// TestFPPDefinitionRepublishMintsARequestIDWhenAbsent: the body is
// optional entirely, and the key actually used comes back so the caller
// can retry with it or poll the sweep with it.
func TestFPPDefinitionRepublishMintsARequestIDWhenAbsent(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"empty object", `{}`},
		{"no body at all", ``},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, plugin := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
			api, _, token := definitionRepublishAPI(t, srv.URL)

			resp, body := doRawRequest(t, api.Handler, definitionRepublishRequest(t, "bench-fpp", tt.body, token))
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
			}
			got := decodeDefinitionRepublish(t, body)
			if got.RequestID == "" {
				t.Fatal("RequestID = \"\", want the minted key reported back")
			}
			if !strings.Contains(plugin.lastBody(), `"requestId":"`+got.RequestID+`"`) {
				t.Errorf("dispatched body = %s, want the same requestId the response reports (%s)", plugin.lastBody(), got.RequestID)
			}
		})
	}
}

// TestFPPDefinitionRepublishRefusedForbiddenViewerNamesScope pins the
// scope this route actually requires.
func TestFPPDefinitionRepublishRefusedForbiddenViewerNamesScope(t *testing.T) {
	srv, plugin := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
	setup := newFPPCommandTestSetup(t, fixedClock(testNow))
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: srv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	token := mustIssueToken(t, setup.svc, viewer.ID)

	resp, body := doRawRequest(t, api.Handler, definitionRepublishRequest(t, "bench-fpp", `{}`, token))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if detail, _ := decodeMap(t, body)["detail"].(string); !strings.Contains(detail, "fpp:command") {
		t.Errorf("problem detail = %q, want it to name the missing scope fpp:command", detail)
	}
	if plugin.hitCount() != 0 {
		t.Errorf("plugin received %d requests, want 0", plugin.hitCount())
	}
}

// TestFPPDefinitionRepublishFailureIsItsOwnProblem: a plugin that does not
// agree to resend is an upstream failure carrying the host's own words, a
// 502 rather than a 500, because the operator can act on it.
func TestFPPDefinitionRepublishFailureIsItsOwnProblem(t *testing.T) {
	srv, _ := newFakeRepublishPlugin(t, http.StatusBadRequest,
		`{"schemaVersion":1,"applied":false,"error":"definition publication is not configured"}`)
	api, _, token := definitionRepublishAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler, definitionRepublishRequest(t, "bench-fpp", `{}`, token))
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", resp.StatusCode, body)
	}
	if got := decodeMap(t, body)["type"]; got != ProblemTypeFPPDefinitionRepublishFailed {
		t.Errorf("problem type = %v, want %v", got, ProblemTypeFPPDefinitionRepublishFailed)
	}
}

// TestFPPDefinitionRepublishRejectsMalformedBodies: an unrecognized key is
// a 400 naming it rather than a silently ignored typo, and an empty
// requestId is refused rather than quietly replaced, because a caller that
// meant to supply a key and sent an empty one would silently lose both its
// retry safety and its way to poll.
func TestFPPDefinitionRepublishRejectsMalformedBodies(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"unrecognized field", `{"instanceId":"bench-fpp"}`},
		{"empty requestId", `{"requestId":""}`},
		{"requestId not a string", `{"requestId":7}`},
		{"body is not an object", `[1,2]`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, plugin := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
			api, _, token := definitionRepublishAPI(t, srv.URL)
			resp, body := doRawRequest(t, api.Handler, definitionRepublishRequest(t, "bench-fpp", tt.body, token))
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
			}
			if plugin.hitCount() != 0 {
				t.Errorf("plugin received %d requests, want 0", plugin.hitCount())
			}
		})
	}
}

// TestFPPDefinitionRepublishAuditsAcceptanceRatherThanAnImport: the audit
// trail must not let a later reader take "6 definitions" for six
// definitions stored. It records that the plugin agreed and that the sweep
// is owed.
func TestFPPDefinitionRepublishAuditsAcceptanceRatherThanAnImport(t *testing.T) {
	srv, _ := newFakeRepublishPlugin(t, http.StatusOK, appliedRepublishPluginBody)
	api, setup, token := definitionRepublishAPI(t, srv.URL)

	resp, body := doRawRequest(t, api.Handler,
		definitionRepublishRequest(t, "bench-fpp", `{"requestId":"req-1"}`, token))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}

	entries, err := setup.svc.ListAudit(context.Background(), 0, 100)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var dispatch, outcome *identity.AuditEntry
	for i := range entries {
		if entries[i].Action != auditActionFPPDefinitionRepublish {
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
		t.Fatalf("audit entries for %s: dispatch = %v, outcome = %v, want both", auditActionFPPDefinitionRepublish, dispatch, outcome)
	}
	if dispatch.IdempotencyKey != "req-1" || dispatch.Target != "bench-fpp" {
		t.Errorf("dispatch entry = %+v, want it to record the requestId and the instance", dispatch)
	}
	if !strings.Contains(outcome.OutcomeReason, "agreed to resend") || !strings.Contains(outcome.OutcomeReason, "owed, not done") {
		t.Errorf("outcome reason = %q, want it to say the plugin agreed to resend and the sweep is owed rather than done", outcome.OutcomeReason)
	}
	if dispatch.CommandID == "" || dispatch.CommandID != outcome.CommandID {
		t.Errorf("commandId dispatch = %q, outcome = %q, want one non-empty value correlating them", dispatch.CommandID, outcome.CommandID)
	}
}
