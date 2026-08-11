package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
)

// buildTestAPI wires a full [API] over the fixtures in fixtures_test.go,
// with a fixed clock so every golden file is deterministic.
func buildTestAPI(t *testing.T) *API {
	t.Helper()
	deps := Dependencies{
		Nodes: &fakeNodeLister{views: []inventory.NodeView{
			onlineNodeFixture(t), retainedOnlyNodeFixture(t),
		}},
		FPP:          &fakeFPPLister{views: []FPPInstanceView{fppInstanceFixture(t)}},
		Observations: &fakeObservationLister{},
		Events: &fakeEventReader{
			records: []EventRecord{eventFixture()},
			latest:  37, oldest: 1, hasOld: true,
		},
		Collectors: &fakeCollectorStatusLister{statuses: []CollectorState{
			{ID: "fpp-rest", State: "running"},
		}},
	}
	return New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
}

func TestGoldenServiceDescriptor(t *testing.T) {
	// goVersion legitimately differs by toolchain; pin it so this golden
	// file is stable across a Go upgrade, per goVersion's own doc comment.
	orig := goVersion
	goVersion = func() string { return "go1.99.0" }
	t.Cleanup(func() { goVersion = orig })

	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertGolden(t, "service_descriptor", body)
}

func TestGoldenNodes(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertGolden(t, "nodes", body)
}

func TestGoldenNodeByID(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/media-03", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertGolden(t, "node_media-03", body)
}

func TestNodeByIDNotFound(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/nonexistent", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeResourceNotFound {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeResourceNotFound)
	}
	if resp.Header.Get("Content-Type") != "application/problem+json" {
		t.Errorf("Content-Type = %q, want application/problem+json", resp.Header.Get("Content-Type"))
	}
	if m["serverTime"] == nil || m["serverTime"] == "" {
		t.Errorf("serverTime = %v, want a non-empty RFC 3339 timestamp on a problem document too (contract section 6.2 has no exception for errors)", m["serverTime"])
	}
}

// TestEveryProblemClassCarriesServerTime is a single sweep across every
// problem-producing path this package has, checking the one orchestrator
// correction that is easy to silently regress on a fifth: contract section
// 6.2's serverTime requirement has no carve-out for an error response,
// and [writeProblem] is the only thing that actually enforces it.
func TestEveryProblemClassCarriesServerTime(t *testing.T) {
	api := buildTestAPI(t)
	tokenAPI := New(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}, Options{AuthToken: "s3cret", Clock: fixedClock(testNow), Logger: testLogger()})

	cases := []struct {
		name    string
		handler http.Handler
		method  string
		target  string
		headers map[string]string
	}{
		{"resource-not-found", api.Handler, "GET", "/api/v1/nodes/nonexistent", nil},
		{"invalid-parameter", api.Handler, "GET", "/api/v1/nodes/Not_Valid!", nil},
		{"unsupported-api-version (header)", api.Handler, "GET", "/api/v1/", map[string]string{apiVersionHeaderName: "2"}},
		{"unsupported-api-version (path)", api.Handler, "GET", "/api/v2/nodes", nil},
		{"unauthorized", tokenAPI.Handler, "GET", "/api/v1/", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, body := doRequest(t, tc.handler, tc.method, tc.target, tc.headers)
			m := decodeMap(t, body)
			st, ok := m["serverTime"].(string)
			if !ok || st == "" {
				t.Fatalf("%s: serverTime = %v, want a non-empty string; body: %s", tc.name, m["serverTime"], body)
			}
			if _, err := time.Parse(time.RFC3339, st); err != nil {
				t.Errorf("%s: serverTime %q is not RFC 3339: %v", tc.name, st, err)
			}
		})
	}
}

func TestNodeByIDInvalidSyntax(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/Not_Valid!", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

func TestGoldenFPPList(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/fpp", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertGolden(t, "fpp_list", body)

	// The endpoint's userinfo must never reach the wire (contract section
	// 6.10). A golden-file byte comparison already proves this, but the
	// credential is sensitive enough to warrant its own explicit,
	// impossible-to-miss assertion independent of the golden file's
	// content.
	if strings.Contains(string(body), "user:pass") {
		t.Fatalf("response body contains endpoint credentials: %s", body)
	}
}

func TestGoldenFPPInstanceByID(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/fpp/player-01", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertGolden(t, "fpp_instance_player-01", body)
}

func TestFPPInstanceByIDNotFound(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/fpp/nonexistent", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

func TestGoldenSnapshot(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertGolden(t, "snapshot", body)
}

func TestGoldenEvents(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/events", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertGolden(t, "events", body)
}

func TestEventsQueryParametersForwarded(t *testing.T) {
	events := &fakeEventReader{records: nil, latest: 100, oldest: 1, hasOld: true}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/events?since=20&limit=5", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if events.gotSince != 20 {
		t.Errorf("since forwarded = %d, want 20", events.gotSince)
	}
	if events.gotLimit != 5 {
		t.Errorf("limit forwarded = %d, want 5", events.gotLimit)
	}
}

func TestEventsLimitClampedToMaximum(t *testing.T) {
	events := &fakeEventReader{latest: 1, oldest: 1, hasOld: true}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/events?limit=999999", nil)
	if events.gotLimit != maxEventsLimit {
		t.Errorf("limit forwarded = %d, want it clamped to %d; body: %s", events.gotLimit, maxEventsLimit, body)
	}
}

func TestEventsInvalidSince(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/events?since=not-a-number", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

// TestEventsGapIsReportedNotErrored is the direct test for the
// orchestrator's contract addition: a pruned interval is a successful,
// descriptive 200 response, never a 4xx that would push a client into
// retrying against history that can never come back.
func TestEventsGapIsReportedNotErrored(t *testing.T) {
	events := &fakeEventReader{
		records: []EventRecord{eventFixture()},
		gap:     true,
		latest:  37, oldest: 30, hasOld: true,
	}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/events?since=1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when gap is true; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["gap"] != true {
		t.Errorf("gap = %v, want true; body: %s", m["gap"], body)
	}
	if got, want := m["oldestRetainedSeq"], float64(30); got != want {
		t.Errorf("oldestRetainedSeq = %v, want %v; body: %s", got, want, body)
	}
}

// TestEventsNoGapReportsFalseAndNullOldest proves the "gap" field is
// always present (never omitted, per contract section 6.2) and that
// oldestRetainedSeq is a literal JSON null, not merely a Go zero value,
// when the store holds no events at all.
func TestEventsNoGapReportsFalseAndNullOldest(t *testing.T) {
	events := &fakeEventReader{records: nil, gap: false, latest: 0, hasOld: false}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: events, Collectors: &fakeCollectorStatusLister{},
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	_, body := doRequest(t, api.Handler, "GET", "/api/v1/events", nil)
	m := decodeMap(t, body)
	if _, ok := m["gap"]; !ok {
		t.Fatalf("gap key is missing from response entirely, want it present and false; body: %s", body)
	}
	if m["gap"] != false {
		t.Errorf("gap = %v, want false", m["gap"])
	}
	if _, ok := m["oldestRetainedSeq"]; !ok {
		t.Fatalf("oldestRetainedSeq key is missing entirely, want it present and null; body: %s", body)
	}
	if m["oldestRetainedSeq"] != nil {
		t.Errorf("oldestRetainedSeq = %v, want literal null", m["oldestRetainedSeq"])
	}
}

func TestObservationsFilterParsedAndForwarded(t *testing.T) {
	obsLister := &fakeObservationLister{}
	deps := Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: obsLister,
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
	}
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/observations?resourceKind=fpp&resourceId=player-01&signal=fpp.multisync.enabled", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	if obsLister.gotFilter.ResourceKind == nil || *obsLister.gotFilter.ResourceKind != "fpp" {
		t.Errorf("ResourceKind filter = %v, want \"fpp\"", obsLister.gotFilter.ResourceKind)
	}
	if obsLister.gotFilter.ResourceID == nil || *obsLister.gotFilter.ResourceID != "player-01" {
		t.Errorf("ResourceID filter = %v, want \"player-01\"", obsLister.gotFilter.ResourceID)
	}
	if obsLister.gotFilter.Signal == nil || *obsLister.gotFilter.Signal != "fpp.multisync.enabled" {
		t.Errorf("Signal filter = %v, want \"fpp.multisync.enabled\"", obsLister.gotFilter.Signal)
	}
}

func TestObservationsInvalidResourceKind(t *testing.T) {
	api := buildTestAPI(t)
	resp, body := doRequest(t, api.Handler, "GET", "/api/v1/observations?resourceKind=projector", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", resp.StatusCode, body)
	}
	m := decodeMap(t, body)
	if m["type"] != ProblemTypeInvalidParameter {
		t.Errorf("type = %v, want %v", m["type"], ProblemTypeInvalidParameter)
	}
}

// --- Raw-JSON assertions the contract's standing rule requires (section 1
// and section 7 of Task D's spec): these check literal bytes/keys, never a
// struct round-trip, so a renamed or dropped JSON tag would actually fail
// them.

// TestRetainedEvidenceRendersNullObservedAtAndUnknownAge is the single
// most important test in this package per contract section 3.3: a
// retained MQTT delivery must never be rendered with a fabricated
// observation time. It asserts on the decoded map's actual key values,
// not on a re-decoded Evidence struct.
func TestRetainedEvidenceRendersNullObservedAtAndUnknownAge(t *testing.T) {
	api := buildTestAPI(t)
	_, body := doRequest(t, api.Handler, "GET", "/api/v1/nodes/shed-01", nil)
	resp := decodeMap(t, body)
	if resp["serverTime"] == nil {
		t.Fatalf("response is missing serverTime; body: %s", body)
	}
	m, ok := resp["node"].(map[string]any)
	if !ok {
		t.Fatalf("response node is not an object; body: %s", body)
	}

	evidence, ok := m["evidence"].(map[string]any)
	if !ok {
		t.Fatalf("evidence is not an object; body: %s", body)
	}
	lastWill, ok := evidence["lastWill"].(map[string]any)
	if !ok {
		t.Fatalf("evidence.lastWill is not an object; body: %s", body)
	}

	if v, ok := lastWill["observedAt"]; !ok {
		t.Fatalf("evidence.lastWill.observedAt key is missing entirely, want it present and null; body: %s", body)
	} else if v != nil {
		t.Errorf("evidence.lastWill.observedAt = %v, want literal null for a retained delivery", v)
	}
	if lastWill["state"] != "unknown_age" {
		t.Errorf("evidence.lastWill.state = %v, want \"unknown_age\"", lastWill["state"])
	}
	if lastWill["reason"] == nil {
		t.Errorf("evidence.lastWill.reason = nil, want a non-null reason when state is not current")
	}

	// controlPlane.state must never be "node.state"/"node.online" — the
	// structural defence contract section 3.2 requires is the field's own
	// name; this test also nails down that a client reading the raw JSON
	// finds "state" nested under "controlPlane", never at the top level.
	if _, ok := m["state"]; ok {
		t.Errorf("a top-level \"state\" field exists on Node; contract section 3.2 forbids it (must be controlPlane.state only)")
	}
	if _, ok := m["online"]; ok {
		t.Errorf("a top-level \"online\" field exists on Node; contract section 3.2 forbids it")
	}
	cp, ok := m["controlPlane"].(map[string]any)
	if !ok {
		t.Fatalf("controlPlane is not an object; body: %s", body)
	}
	if cp["state"] != "unknown" {
		t.Errorf("controlPlane.state = %v, want \"unknown\"", cp["state"])
	}
}

// TestNoPrecomputedAgeField asserts contract section 6.2's "payloads carry
// absolute timestamps only. Never a precomputed age or secondsAgo" across
// every resource this package renders, by scanning the raw snapshot body
// (which includes nodes, fpp instances, and — via one of the fpp
// observations — a stale evidence entry, the exact case most tempting to
// render as a precomputed age) for a set of key-name substrings that would
// indicate one snuck in.
func TestNoPrecomputedAgeField(t *testing.T) {
	api := buildTestAPI(t)
	_, body := doRequest(t, api.Handler, "GET", "/api/v1/snapshot", nil)

	forbidden := []string{"secondsAgo", "ageSeconds", "\"age\"", "observedAgeSecs"}
	for _, f := range forbidden {
		if strings.Contains(string(body), f) {
			t.Errorf("snapshot response contains forbidden precomputed-age field %q; body: %s", f, body)
		}
	}
}

// TestStaleObservationNeverReportsCurrent pins pkg/observation's own
// invariant one layer up, on the actual wire body: the fpp instance
// fixture's third observation ages out its ValidFor well before testNow,
// and this test proves that shows up as "stale" on the rendered JSON, not
// "current".
func TestStaleObservationNeverReportsCurrent(t *testing.T) {
	api := buildTestAPI(t)
	_, body := doRequest(t, api.Handler, "GET", "/api/v1/fpp/player-01", nil)
	resp := decodeMap(t, body)
	m, ok := resp["instance"].(map[string]any)
	if !ok {
		t.Fatalf("response instance is not an object; body: %s", body)
	}

	observations, ok := m["observations"].([]any)
	if !ok {
		t.Fatalf("observations is not an array; body: %s", body)
	}
	found := false
	for _, raw := range observations {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if entry["signal"] == "fpp.uptime.seconds" {
			found = true
			if entry["state"] != "stale" {
				t.Errorf("fpp.uptime.seconds state = %v, want \"stale\"", entry["state"])
			}
			if entry["reason"] == nil {
				t.Errorf("fpp.uptime.seconds reason = nil, want a non-null synthesized reason")
			}
		}
	}
	if !found {
		t.Fatalf("fpp.uptime.seconds observation not found in response; body: %s", body)
	}
}
