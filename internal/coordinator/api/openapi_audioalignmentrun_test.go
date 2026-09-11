package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file follows openapi_audionodesilence_test.go's split: a
// compile-only pass over the added schemas, plus a real-handler pass
// against api/openapi.yaml's response schemas.

func TestOpenAPIAudioAlignmentRunSchemasCompile(t *testing.T) {
	c := newOpenAPICompiler(t)
	for _, name := range []string{
		"AudioAlignmentRun", "AudioAlignmentSample", "AudioAlignmentRunSummary",
		"AudioAlignmentRunStopRequest", "AudioAlignmentRunResponse",
		"AudioAlignmentRunListResponse", "AudioAlignmentRunDetailResponse",
	} {
		if _, err := c.Compile(openAPIDocumentURL + "#/components/schemas/" + name); err != nil {
			t.Errorf("compiling schema %s: %v", name, err)
		}
	}
}

func TestOpenAPIAudioAlignmentRunResponsesMatchRealResponses(t *testing.T) {
	c := newOpenAPICompiler(t)
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	start := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	start.Header.Set("Authorization", auth)
	startResp, startBody := doRawRequest(t, api.Handler, start)
	if startResp.StatusCode != http.StatusOK {
		t.Fatalf("start status = %d, want 200; body: %s", startResp.StatusCode, startBody)
	}
	assertMatchesSchema(t, c, "AudioAlignmentRunResponse", startBody)
	runID, _ := decodeMap(t, startBody)["run"].(map[string]any)["id"].(string)

	list := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	list.Header.Set("Authorization", auth)
	listResp, listBody := doRawRequest(t, api.Handler, list)
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body: %s", listResp.StatusCode, listBody)
	}
	assertMatchesSchema(t, c, "AudioAlignmentRunListResponse", listBody)

	get := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID, nil)
	get.Header.Set("Authorization", auth)
	getResp, getBody := doRawRequest(t, api.Handler, get)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("get status = %d, want 200; body: %s", getResp.StatusCode, getBody)
	}
	assertMatchesSchema(t, c, "AudioAlignmentRunDetailResponse", getBody)

	stop := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs/"+runID+"/stop",
		bytes.NewBufferString(`{"reason":"done"}`))
	stop.Header.Set("Authorization", auth)
	stopResp, stopBody := doRawRequest(t, api.Handler, stop)
	if stopResp.StatusCode != http.StatusOK {
		t.Fatalf("stop status = %d, want 200; body: %s", stopResp.StatusCode, stopBody)
	}
	assertMatchesSchema(t, c, "AudioAlignmentRunResponse", stopBody)
}

func TestOpenAPIAudioAlignmentRunConflictAndNotFoundMatchProblemSchema(t *testing.T) {
	c := newOpenAPICompiler(t)
	deps, _ := newTestAlignmentRunDeps(t, fixedClock(testNow))
	auth := adminAuthHeader(t, deps)
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	first := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	first.Header.Set("Authorization", auth)
	doRawRequest(t, api.Handler, first)

	second := httptest.NewRequest(http.MethodPost, "/api/v1/nodes/node-a/audio/alignment-runs", nil)
	second.Header.Set("Authorization", auth)
	conflictResp, conflictBody := doRawRequest(t, api.Handler, second)
	if conflictResp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", conflictResp.StatusCode, conflictBody)
	}
	assertMatchesSchema(t, c, "Problem", conflictBody)

	notFound := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/node-a/audio/alignment-runs/missing", nil)
	notFound.Header.Set("Authorization", auth)
	nfResp, nfBody := doRawRequest(t, api.Handler, notFound)
	if nfResp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", nfResp.StatusCode, nfBody)
	}
	assertMatchesSchema(t, c, "Problem", nfBody)
}

func TestOpenAPIAudioAlignmentRunStopRequestSchemaBindsToOperation(t *testing.T) {
	if got := requestBodySchemaRef(t, "post", "/nodes/{nodeId}/audio/alignment-runs/{runId}/stop"); got != "AudioAlignmentRunStopRequest" {
		t.Errorf("requestBody schema = %q, want AudioAlignmentRunStopRequest", got)
	}
}
