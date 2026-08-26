package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/fppconnectpush"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is this fix's own conformance coverage, following
// openapi_noderender_test.go's exact pattern one push surface over: a
// dropped channel range — previously visible only as a coordinator log
// line (internal/coordinator/fppconnectpush.resolveForNode's own
// logger.Warn call) — now reaches GET /api/v1/nodes/{nodeId} through the
// real write path (PUT /api/v1/config/show.surface/{id}, twenty times,
// the same "too long for the 120-byte ping field" refusal
// TestToNodeFormattingFailureRecordsDroppedStatus proves at the
// fppconnectpush package level) and the real read path, with no log
// inspection anywhere in this test.

// putNonContiguousSurface PUTs one show.surface config object onto
// nodeID: 150 channels (rgb, width 1 x height 50 — 1*50*3 = 150, satisfying
// showsurface.go's geometry cross-check), starting at startChannel. Twenty
// of these, spread 200 channels apart, push the formatted, comma-joined
// channelRanges string past multisync.MaxPingRangesLength (120 bytes) —
// the SAME construction internal/coordinator/fppconnectpush/push_test.go's
// own TestToNodeFormattingFailureRecordsDroppedStatus uses one layer down,
// exercised here through the real HTTP config-write path instead of a
// fake ConfigStore.
func putNonContiguousSurface(t *testing.T, api *API, token, id, nodeID string, startChannel int) {
	t.Helper()
	body := fmt.Sprintf(`{
		"show": "halloween-2026",
		"name": %q,
		"node": %q,
		"channelRange": {"startChannel": %d, "channelCount": 150},
		"geometry": {"width": 1, "height": 50, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": %q}}
	}`, id, nodeID, startChannel, id)
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/"+id, body, map[string]string{"Authorization": "Bearer " + token})
	if resp, respBody := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface/%s status = %d, want 200; body: %s", id, resp.StatusCode, respBody)
	}
}

// TestDroppedChannelRangeReachesRealAPIWithoutReadingLogs is the
// reproduction this issue asked for, proven as a passing test instead of
// a one-off log capture: twenty non-contiguous show.surface writes on one
// node, over the real config-write HTTP path, drive a real
// fppconnect.configure push whose channel range fails to format. Before
// this fix, the ONLY trace of that was a logger.Warn line in
// fppconnectpush.resolveForNode; this test asserts the SAME fact is now
// visible in GET /api/v1/nodes/{nodeId}'s "fppConnect" field, with a
// reason naming the actual refusal, with nothing here reading a log.
func TestDroppedChannelRangeReachesRealAPIWithoutReadingLogs(t *testing.T) {
	c := newOpenAPICompiler(t)

	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	statusStore := fppconnectpush.NewStatusStore()
	deps := showObjectsTestDeps(svc, st)
	pub := &fakeRenderPublisher{}
	deps.RenderPublisher = pub
	deps.FPPConnectStatus = statusStore
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	for i := 0; i < 20; i++ {
		putNonContiguousSurface(t, api, token, fmt.Sprintf("surface-%d", i), "render-01", 1+i*200)
	}
	waitForPublishCount(t, pub, "fppconnect.configure", 20)

	// --- the published fppconnect.configure command itself carries an
	// empty range, indistinguishable from a node with no surfaces at all
	// — this IS the pre-fix defect: nothing in the wire command names why.
	pub.mu.Lock()
	var lastChannelRanges any = "not published"
	var sawFPPConnectConfigure bool
	for _, env := range pub.payload {
		if env.Payload.Action == "fppconnect.configure" {
			sawFPPConnectConfigure = true
			lastChannelRanges = env.Payload.Params["channelRanges"]
		}
	}
	pub.mu.Unlock()
	if !sawFPPConnectConfigure {
		t.Fatal("no fppconnect.configure command was published")
	}
	if lastChannelRanges != "" {
		t.Fatalf("channelRanges = %v, want empty string on a formatting failure", lastChannelRanges)
	}

	// --- GET /api/v1/nodes/render-01 carries the drop as evidence, not a
	// log line: this is the acceptance criterion, proven end to end.
	resp, nodeBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /nodes/render-01: status = %d, want 200; body: %s", resp.StatusCode, nodeBody)
	}
	assertMatchesSchema(t, c, "NodeResponse", nodeBody)

	body := string(nodeBody)
	if !strings.Contains(body, `"node.fppconnect.channel_range.state"`) {
		t.Fatalf("GET /nodes/render-01 body does not carry node.fppconnect.channel_range.state: %s", body)
	}
	if !strings.Contains(body, `"value":"dropped"`) {
		t.Fatalf("GET /nodes/render-01 body does not report state \"dropped\": %s", body)
	}
	if !strings.Contains(body, `"node.fppconnect.channel_range.reason"`) {
		t.Fatalf("GET /nodes/render-01 body does not carry node.fppconnect.channel_range.reason: %s", body)
	}
	if !strings.Contains(body, "120-byte") {
		t.Fatalf("GET /nodes/render-01 body does not name the actual refusal (120-byte ping field): %s", body)
	}

	// --- a node that never had a push resolved renders fppConnect: [],
	// never omitted, matching render/audio's identical rule.
	mustDeclareNode(t, st, "quiet-01")
	quietResp, quietBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/quiet-01", nil)
	if quietResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /nodes/quiet-01: status = %d, want 200; body: %s", quietResp.StatusCode, quietBody)
	}
	assertMatchesSchema(t, c, "NodeResponse", quietBody)
	if !strings.Contains(string(quietBody), `"fppConnect":[]`) {
		t.Errorf("GET /nodes/quiet-01 (no fppconnect push ever resolved) body = %s, want an empty fppConnect array, never omitted", quietBody)
	}
}

// TestFormattedChannelRangeNeverReportsDropped is
// TestDroppedChannelRangeReachesRealAPIWithoutReadingLogs' negative case:
// a node whose single surface formats cleanly must never read as
// "dropped" — proving this test suite would actually fail if the state
// were reported unconditionally rather than from the real outcome.
func TestFormattedChannelRangeNeverReportsDropped(t *testing.T) {
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)

	statusStore := fppconnectpush.NewStatusStore()
	deps := showObjectsTestDeps(svc, st)
	pub := &fakeRenderPublisher{}
	deps.RenderPublisher = pub
	deps.FPPConnectStatus = statusStore
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	mustDeclareNode(t, st, "render-01")
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)

	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.surface/garage-door", validSurfaceBodyNDI, map[string]string{"Authorization": "Bearer " + token})
	if resp, body := doRawRequest(t, api.Handler, req); resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.surface status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	waitForPublishCount(t, pub, "fppconnect.configure", 1)

	_, nodeBody := doRequest(t, api.Handler, "GET", "/api/v1/nodes/render-01", nil)
	body := string(nodeBody)
	if !strings.Contains(body, `"value":"formatted"`) {
		t.Fatalf("GET /nodes/render-01 body does not report state \"formatted\": %s", body)
	}
	if strings.Contains(body, `"value":"dropped"`) {
		t.Fatalf("GET /nodes/render-01 body reports \"dropped\" for a surface that formatted cleanly: %s", body)
	}
}
