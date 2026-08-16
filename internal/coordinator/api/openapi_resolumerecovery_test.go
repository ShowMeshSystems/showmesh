package api

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file is Track D seam D-3a's own OpenAPI conformance suite (review
// finding 5): the five HTTP routes shipped conformance-untested, mirroring
// openapi_resolumeaction_test.go's pattern of driving a REAL [API] and
// validating its actual response body against api/openapi.yaml's own
// schema for that endpoint.

// TestOpenAPIResolumeRecoveryResponseMatchesRealResponse covers GET
// /resolume/recovery, including a non-nil lastRestore (the only way to
// exercise ResolumeRecoveryRestoreReport's own required fields, including
// omittedLayerCount, inside this response's schema).
func TestOpenAPIResolumeRecoveryResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	setup.rec.setRecord([]ResolumeRecoveryRecordEntryView{{Layer: "Whole House 1", State: "dark"}})
	setup.rec.setLastReport(&ResolumeRecoveryRestoreReportView{
		StartedAt: "2026-08-16T00:00:00Z", FinishedAt: "2026-08-16T00:00:01Z",
		Trigger: "manual", Outcome: "restored",
		Layers:            []ResolumeRecoveryRestoreLayerView{{Layer: "Whole House 1", Result: "restored"}},
		OmittedLayerCount: 1,
	})
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/resolume/recovery", nil)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "ResolumeRecoveryResponse", body)
}

// TestOpenAPIResolumeRecoveryRestoreResponseMatchesRealResponse covers
// POST /resolume/recovery/restore's success body against
// ResolumeRecoveryRestoreResponse, driven with a real principal holding
// resolume:action.
func TestOpenAPIResolumeRecoveryRestoreResponseMatchesRealResponse(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	setup.rec.setRestoreResult(ResolumeRecoveryRestoreReportView{
		StartedAt: "2026-08-16T00:00:00Z", FinishedAt: "2026-08-16T00:00:01Z",
		Trigger: "manual", Outcome: "restored",
		Layers: []ResolumeRecoveryRestoreLayerView{{Layer: "Whole House 1", Result: "restored"}},
	})
	operator := mustCreatePrincipal(t, setup.svc, "operator-1", identity.RoleOperator)
	token := mustIssueToken(t, setup.svc, operator.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/resolume/recovery/restore", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, body := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	assertMatchesSchema(t, c, "ResolumeRecoveryRestoreResponse", body)
}

// TestOpenAPIResolumeRecoveryConfigResponsesMatchRealResponses covers PUT
// /config/resolume.recovery's success body, GET /config/resolume.recovery's
// body, and GET /config/resolume.recovery/revisions' body, each validated
// against a REAL response — mirroring
// TestOpenAPIConfigResponsesMatchRealResponses (openapi_test.go) for
// fpp.endpoints.
func TestOpenAPIResolumeRecoveryConfigResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	setup := newResolumeRecoveryTestSetup(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, setup.svc, admin.ID)
	api := New(setup.deps(), Options{Clock: fixedClock(testNow), Logger: testLogger()})
	authHeader := map[string]string{"Authorization": "Bearer " + token}

	putReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/resolume.recovery", `{"autoRestoreEnabled":false}`, authHeader)
	putResp, putBody := doRawRequest(t, api.Handler, putReq)
	if putResp.StatusCode != http.StatusOK {
		t.Fatalf("PUT: status = %d, want 200; body: %s", putResp.StatusCode, putBody)
	}
	assertMatchesSchema(t, c, "ResolumeRecoveryConfigResponse", putBody)

	_, getBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery", authHeader)
	assertMatchesSchema(t, c, "ResolumeRecoveryConfigResponse", getBody)

	_, revBody := doRequest(t, api.Handler, "GET", "/api/v1/config/resolume.recovery/revisions", authHeader)
	assertMatchesSchema(t, c, "ConfigRevisionsResponse", revBody)
}

// TestOpenAPIResolumeRecoveryChangedEventMatchesRealFrame is
// [TestOpenAPIStreamEventSchemasMatchRealFrames]'s own sibling for
// resolumeRecovery.changed: a real frame, obtained over a live stream
// connection from stream_resolumerecovery_test.go's own fakes, validated
// against ResolumeRecoveryChangedEvent.
func TestOpenAPIResolumeRecoveryChangedEventMatchesRealFrame(t *testing.T) {
	c := newOpenAPICompiler(t)

	cs := &mutableResolumeRecoveryConfigStore{}
	cs.setEnabled(true)
	rec := &mutableResolumeRecoveryProvider{}

	testAPI := newStreamTestAPI(Dependencies{
		Nodes: &fakeNodeLister{}, FPP: &fakeFPPLister{}, Observations: &fakeObservationLister{},
		Events: &fakeEventReader{}, Collectors: &fakeCollectorStatusLister{},
		Config: cs, ResolumeRecovery: rec, ResolumeRecoverySettleSeconds: 8,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go testAPI.Hub.Run(ctx)

	srv := httptest.NewServer(testAPI.Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/stream")
	if err != nil {
		t.Fatalf("GET /api/v1/stream: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	r := bufio.NewReader(resp.Body)

	if event, _ := readEventWithTimeout(t, r, 5*time.Second); event != "stream.start" {
		t.Fatalf("first event = %q, want stream.start", event)
	}

	testAPI.Hub.Notify()
	event, data := readEventWithTimeout(t, r, 5*time.Second)
	if event != "resolumeRecovery.changed" {
		t.Fatalf("event = %q, want resolumeRecovery.changed", event)
	}
	assertMatchesSchema(t, c, "ResolumeRecoveryChangedEvent", []byte(data))
}
