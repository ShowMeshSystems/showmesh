package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/fppcommand"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// These tests drive PUT /api/v1/fpp/{instanceId}/brightness/excluded-ranges
// through a real [API] against an HTTP fake standing in for the plugin, so
// the real client and the real read-back are what they exercise.

// fakeExcludedRangesPlugin answers the plugin's excluded ranges write and
// its brightness read. holds is the list the read reports; keep true
// makes an accepted write change it.
type fakeExcludedRangesPlugin struct {
	mu     sync.Mutex
	writes []string
	holds  string
	keep   bool
	status int
	answer string
}

func newFakeExcludedRangesPlugin(t *testing.T, f *fakeExcludedRangesPlugin) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPut && r.URL.Path == fppcommand.ExcludedRangesPath:
			raw, _ := io.ReadAll(r.Body)
			f.writes = append(f.writes, string(raw))
			if f.status != 0 {
				w.WriteHeader(f.status)
				_, _ = w.Write([]byte(f.answer))
				return
			}
			var req struct {
				ExcludedRanges json.RawMessage `json:"excludedRanges"`
			}
			_ = json.Unmarshal(raw, &req)
			if f.keep {
				f.holds = string(req.ExcludedRanges)
			}
			_, _ = w.Write([]byte(`{"schemaVersion":1,"excludedRanges":` + string(req.ExcludedRanges) + `}`))
		case r.Method == http.MethodGet && r.URL.Path == fppPluginBrightnessPath:
			_, _ = w.Write([]byte(`{"schemaVersion":1,"ceiling":60,"transitionGain":100,"effectiveOutput":60,"fadeActive":false,"updatedAtMillis":1,"excludedRanges":` + f.holds + `}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (f *fakeExcludedRangesPlugin) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

func excludedRangesRequest(t *testing.T, instanceID, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/fpp/"+instanceID+"/brightness/excluded-ranges", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	return req
}

type excludedRangesResponseForTest struct {
	Command struct {
		Action        string `json:"action"`
		Outcome       string `json:"outcome"`
		OutcomeReason string `json:"outcomeReason"`
	} `json:"command"`
	ExcludedRanges *[]struct {
		StartChannel int `json:"startChannel"`
		ChannelCount int `json:"channelCount"`
	} `json:"excludedRanges"`
}

func putExcludedRanges(t *testing.T, plugin *fakeExcludedRangesPlugin, instanceID, body string) (*http.Response, []byte, *fppCommandTestSetup) {
	t.Helper()
	srv := newFakeExcludedRangesPlugin(t, plugin)
	api, setup, token := ceilingAPI(t, srv.URL)
	resp, got := doRawRequest(t, api.Handler, excludedRangesRequest(t, instanceID, body, token))
	return resp, got, setup
}

func TestExcludedRangesWritesThePluginRouteAndReportsWhatItHolds(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[]`, keep: true}
	resp, body, setup := putExcludedRanges(t, plugin, "bench-fpp",
		`{"ranges":[{"startChannel":1,"channelCount":512},{"startChannel":1025,"channelCount":512}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if want := `{"excludedRanges":[{"startChannel":1,"channelCount":512},{"startChannel":1025,"channelCount":512}]}`; plugin.writes[0] != want {
		t.Fatalf("plugin received %s, want %s", plugin.writes[0], want)
	}
	var got excludedRangesResponseForTest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v; body: %s", err, body)
	}
	if got.ExcludedRanges == nil || len(*got.ExcludedRanges) != 2 || (*got.ExcludedRanges)[1].StartChannel != 1025 {
		t.Fatalf("excludedRanges = %+v, want the two ranges read back", got.ExcludedRanges)
	}
	if got.Command.Outcome != outcomeWordConfirmed || got.Command.Action != fppBrightnessExcludedRangesAction {
		t.Fatalf("command = %+v, want a confirmed setBrightnessExcludedRanges", got.Command)
	}

	entries, err := setup.svc.ListAuditNewestFirst(t.Context(), 0, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	kinds := map[identity.AuditKind]bool{}
	for _, e := range entries {
		if e.Action == auditActionFPPSetBrightnessExcludedRanges {
			kinds[e.Kind] = true
		}
	}
	if !kinds[identity.AuditDispatch] || !kinds[identity.AuditOutcome] {
		t.Fatalf("audit kinds = %v, want a dispatch and an outcome entry", kinds)
	}
}

func TestExcludedRangesAnEmptyListClearsAndReadsBackEmpty(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[{"startChannel":1,"channelCount":8}]`, keep: true}
	resp, body, _ := putExcludedRanges(t, plugin, "bench-fpp", `{"ranges":[]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), `"excludedRanges":[]`) {
		t.Fatalf("body = %s, want an empty excludedRanges list, present", body)
	}
}

// A plugin that joins touching ranges still holds the channels asked for.
func TestExcludedRangesConfirmsAPluginThatJoinsTouchingRanges(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[{"startChannel":1,"channelCount":20}]`}
	resp, body, _ := putExcludedRanges(t, plugin, "bench-fpp",
		`{"ranges":[{"startChannel":11,"channelCount":10},{"startChannel":1,"channelCount":10}]}`)
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"outcome":"confirmed"`) {
		t.Fatalf("status = %d body = %s, want a confirmed 200", resp.StatusCode, body)
	}
}

func TestExcludedRangesIsUnconfirmedWhenThePluginReportsADifferentList(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[]`}
	resp, body, _ := putExcludedRanges(t, plugin, "bench-fpp", `{"ranges":[{"startChannel":1,"channelCount":512}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var got excludedRangesResponseForTest
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ExcludedRanges != nil || got.Command.Outcome != outcomeWordUnconfirmed {
		t.Fatalf("got %+v, want unconfirmed with no excludedRanges", got)
	}
}

func TestExcludedRangesRefusesBadRangesInOneLineAndWritesNothing(t *testing.T) {
	for body, want := range map[string]string{
		`{"ranges":[{"startChannel":1,"channelCount":1000},{"startChannel":600,"channelCount":100}]}`: "Range 600-100 overlaps 1-1000. Use separate ranges, like 1-512, 1025-512.",
		`{"ranges":[{"startChannel":0,"channelCount":10}]}`:                                           "Range 0-10 starts below channel 1. Enter start-count, like 1-512.",
		`{"ranges":[{"startChannel":5,"channelCount":0}]}`:                                            "Range 5-0 has no channels. Enter start-count, like 1-512.",
		`{"ranges":[{"startChannel":8388000,"channelCount":1000}]}`:                                   "Range 8388000-1000 ends past channel 8388608. Shorten it or remove it.",
		`{"ranges":[{"startChannel":9000000,"channelCount":1}]}`:                                      "Range 9000000-1 ends past channel 8388608. Shorten it or remove it.",
		`{}`:                              "The ranges list is missing.",
		`{"ranges":null}`:                 "The ranges list is missing.",
		`{"ranges":"1-512"}`:              "The ranges value is not a list of ranges.",
		`{"ranges":[{"startChannel":1}]}`: "Range 1 needs a whole-number startChannel and channelCount only.",
		`{"ranges":[{"startChannel":1.5,"channelCount":2}]}`: "Range 1 needs a whole-number startChannel and channelCount only.",
		`{"ranges":[],"nope":1}`:                             "The field nope is not recognized.",
		`{"ranges":[],"requestId":""}`:                       "The requestId is not a non-empty string.",
	} {
		plugin := &fakeExcludedRangesPlugin{holds: `[]`, keep: true}
		resp, got, _ := putExcludedRanges(t, plugin, "bench-fpp", body)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("body %s status = %d, want 400; response: %s", body, resp.StatusCode, got)
		}
		var problem struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(got, &problem)
		if !strings.HasPrefix(problem.Detail, want) || strings.Contains(problem.Detail, "\n") {
			t.Errorf("body %s detail = %q, want one line starting %q", body, problem.Detail, want)
		}
		if plugin.writeCount() != 0 {
			t.Errorf("body %s reached the plugin %d times, want none", body, plugin.writeCount())
		}
	}
}

func TestExcludedRangesAcceptsARangeEndingOnTheLastChannel(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[]`, keep: true}
	resp, body, _ := putExcludedRanges(t, plugin, "bench-fpp", `{"ranges":[{"startChannel":8388608,"channelCount":1}]}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
}

func TestExcludedRangesCarriesThePluginsOwnRefusal(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[]`, status: http.StatusBadRequest, answer: `{"error":"range 2 is past the last output channel"}`}
	resp, body, setup := putExcludedRanges(t, plugin, "bench-fpp", `{"ranges":[{"startChannel":1,"channelCount":512}]}`)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "range 2 is past the last output channel") || !strings.Contains(string(body), "fpp-brightness-excluded-ranges-write-failed") {
		t.Fatalf("body = %s, want the plugin's own words under this route's problem type", body)
	}
	entries, err := setup.svc.ListAuditNewestFirst(t.Context(), 0, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	for _, e := range entries {
		if e.Action == auditActionFPPSetBrightnessExcludedRanges && e.Kind == identity.AuditOutcome {
			if e.Outcome != outcomeWordFailed || !strings.Contains(e.OutcomeReason, "past the last output channel") {
				t.Errorf("audited outcome = %q (%q), want failed with the plugin's words", e.Outcome, e.OutcomeReason)
			}
			return
		}
	}
	t.Error("no outcome audit entry was written for a refused write")
}

// A plugin too old to have the route answers 404, which is its own fact.
func TestExcludedRangesSaysToUpdateAPluginWithoutTheRoute(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[]`, status: http.StatusNotFound}
	resp, body, _ := putExcludedRanges(t, plugin, "bench-fpp", `{"ranges":[]}`)
	if resp.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "Update the ShowMesh plugin.") {
		t.Fatalf("status = %d body = %s, want a 502 that says to update the plugin", resp.StatusCode, body)
	}
}

func TestExcludedRangesRefusesAnUnconfiguredInstanceAndAViewer(t *testing.T) {
	plugin := &fakeExcludedRangesPlugin{holds: `[]`, keep: true}
	resp, body, setup := putExcludedRanges(t, plugin, "no-such-fpp", `{"ranges":[]}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unconfigured instance status = %d, want 404; body: %s", resp.StatusCode, body)
	}

	srv := newFakeExcludedRangesPlugin(t, plugin)
	setup.fppLister.views = []FPPInstanceView{{InstanceID: "bench-fpp", Endpoint: srv.URL}}
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	viewer := mustCreatePrincipal(t, setup.svc, "viewer-1", identity.RoleViewer)
	resp, body = doRawRequest(t, api.Handler, excludedRangesRequest(t, "bench-fpp", `{"ranges":[]}`, mustIssueToken(t, setup.svc, viewer.ID)))
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer status = %d, want 403; body: %s", resp.StatusCode, body)
	}
	if plugin.writeCount() != 0 {
		t.Fatalf("the plugin was written %d times, want none", plugin.writeCount())
	}
}

func TestOpenAPIFPPBrightnessExcludedRangesMatchesRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{"FPPChannelRange", "FPPBrightnessExcludedRangesRequest", "FPPBrightnessExcludedRangesResponse"} {
		compileSchema(t, c, name)
	}
	if got := requestBodySchemaRef(t, "put", "/fpp/{instanceId}/brightness/excluded-ranges"); got != "FPPBrightnessExcludedRangesRequest" {
		t.Errorf("request body schema = %q, want FPPBrightnessExcludedRangesRequest", got)
	}

	_, confirmed, _ := putExcludedRanges(t, &fakeExcludedRangesPlugin{holds: `[]`, keep: true}, "bench-fpp", `{"ranges":[{"startChannel":1,"channelCount":512}]}`)
	assertMatchesSchema(t, c, "FPPBrightnessExcludedRangesResponse", confirmed)
	_, cleared, _ := putExcludedRanges(t, &fakeExcludedRangesPlugin{holds: `[]`, keep: true}, "bench-fpp", `{"ranges":[]}`)
	assertMatchesSchema(t, c, "FPPBrightnessExcludedRangesResponse", cleared)
	_, refused, _ := putExcludedRanges(t, &fakeExcludedRangesPlugin{holds: `[]`}, "bench-fpp", `{"ranges":[{"startChannel":0,"channelCount":1}]}`)
	assertMatchesSchema(t, c, "Problem", refused)
	_, upstream, _ := putExcludedRanges(t, &fakeExcludedRangesPlugin{holds: `[]`, status: http.StatusBadRequest, answer: `{"error":"no"}`}, "bench-fpp", `{"ranges":[]}`)
	assertMatchesSchema(t, c, "Problem", upstream)
}
