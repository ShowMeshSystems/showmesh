package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Every JSON body in this file is a hand-written literal string, never
// produced by marshalling one of this package's own structs. Per contract
// §1: a test that marshals a struct and unmarshals it back into the same
// struct proves nothing about the wire contract, since a JSON tag rename
// would still round-trip. These strings are what makes this file an actual
// test of the decode path.

func testServer(t *testing.T, handler http.HandlerFunc) (*client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(handler)
	t.Cleanup(ts.Close)
	c, err := newClient(ts.URL, "", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	return c, ts
}

func TestGetJSONDecodesLiteralBody(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("ShowMesh-API-Version"); got != "1" {
			t.Errorf("request ShowMesh-API-Version header = %q, want 1", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:14:22.481-05:00","nodes":[
			{"nodeId":"media-03","label":"Garage media node","platform":"linux-amd64",
			 "agentVersion":"dev","bootId":"boot-1","startedAt":"2026-08-10T20:00:00-05:00",
			 "firstSeenAt":"2026-08-10T19:00:00-05:00","updatedAt":"2026-08-10T21:14:00-05:00",
			 "capabilities":[{"id":"matrix.render","version":1,"attributes":{"rows":8}}],
			 "controlPlane":{"state":"online","reason":null},
			 "evidence":{
			   "hello":{"signal":"node.hello","value":true,"unit":null,"state":"current","reason":null,
			             "observedAt":"2026-08-10T20:00:00-05:00","collectedAt":"2026-08-10T20:00:00-05:00",
			             "source":"mqtt-inventory","quality":"direct","validForSeconds":null},
			   "lastWill":{"signal":"node.lastWill","value":null,"unit":null,"state":"not_collected","reason":"no LWT observed yet",
			               "observedAt":null,"collectedAt":"2026-08-10T20:00:00-05:00","source":"mqtt-inventory","quality":"direct","validForSeconds":null},
			   "heartbeat":{"signal":"node.heartbeat","value":true,"unit":null,"state":"current","reason":null,
			                "observedAt":"2026-08-10T21:14:15-05:00","collectedAt":"2026-08-10T21:14:15-05:00",
			                "source":"mqtt-inventory","quality":"direct","validForSeconds":15}
			 }}
		]}`)
	})

	var resp nodesResponse
	if err := c.getJSON(context.Background(), "/api/v1/nodes", nil, &resp); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	if len(resp.Nodes) != 1 {
		t.Fatalf("len(Nodes) = %d, want 1", len(resp.Nodes))
	}
	n := resp.Nodes[0]
	if n.NodeID != "media-03" {
		t.Errorf("NodeID = %q, want media-03", n.NodeID)
	}
	if n.Label == nil || *n.Label != "Garage media node" {
		t.Errorf("Label = %v, want \"Garage media node\"", n.Label)
	}
	if n.ControlPlane.State != "online" {
		t.Errorf("ControlPlane.State = %q, want online", n.ControlPlane.State)
	}
	if n.Evidence.LastWill.ObservedAt != nil {
		t.Errorf("LastWill.ObservedAt = %v, want nil (contract §3.3: never fabricated)", n.Evidence.LastWill.ObservedAt)
	}
	if n.Evidence.LastWill.Reason == nil || *n.Evidence.LastWill.Reason != "no LWT observed yet" {
		t.Errorf("LastWill.Reason = %v, want set", n.Evidence.LastWill.Reason)
	}
	if len(n.Capabilities) != 1 || n.Capabilities[0].ID != "matrix.render" {
		t.Errorf("Capabilities = %+v, want one matrix.render entry", n.Capabilities)
	}
}

// TestDecodeIgnoresUnknownFields proves the "additive-only" tolerance
// contract §6.2 requires: a coordinator newer than this CLI can add fields
// anywhere in the payload without breaking it. Uses a hand-written body
// with fields this package's structs do not declare, at both the top
// level and inside the evidence envelope.
func TestDecodeIgnoresUnknownFields(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","futureTopLevelField":"surprise","nodes":[
			{"nodeId":"media-03","label":null,"platform":null,"agentVersion":null,"bootId":null,
			 "startedAt":null,"firstSeenAt":null,"updatedAt":null,"capabilities":[],
			 "controlPlane":{"state":"unknown","reason":"never seen","futureField":42},
			 "evidence":{
			   "hello":{"signal":"node.hello","value":null,"unit":null,"state":"not_collected","reason":"x","observedAt":null,"collectedAt":"2026-08-10T21:00:00Z","source":"s","quality":"direct","aBrandNewField":{"nested":true}},
			   "lastWill":{"signal":"node.lastWill","value":null,"unit":null,"state":"not_collected","reason":"x","observedAt":null,"collectedAt":"2026-08-10T21:00:00Z","source":"s","quality":"direct"},
			   "heartbeat":{"signal":"node.heartbeat","value":null,"unit":null,"state":"not_collected","reason":"x","observedAt":null,"collectedAt":"2026-08-10T21:00:00Z","source":"s","quality":"direct"}
			 }}
		]}`)
	})

	var resp nodesResponse
	if err := c.getJSON(context.Background(), "/api/v1/nodes", nil, &resp); err != nil {
		t.Fatalf("getJSON returned an error on a body with unknown fields (must not — contract §6.2): %v", err)
	}
	if len(resp.Nodes) != 1 || resp.Nodes[0].NodeID != "media-03" {
		t.Fatalf("decoding failed to recover the known fields around the unknown ones: %+v", resp)
	}
}

func TestGetJSONUnauthorized(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("WWW-Authenticate", "Bearer")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/unauthorized","title":"Unauthorized","status":401,"detail":"missing or invalid bearer token"}`)
	})

	var resp nodesResponse
	err := c.getJSON(context.Background(), "/api/v1/nodes", nil, &resp)
	ce := requireCLIError(t, err)
	if ce.code != exitUnauthorized {
		t.Errorf("exit code = %d, want exitUnauthorized (%d)", ce.code, exitUnauthorized)
	}
	if !containsAll(ce.Error(), "Unauthorized", "missing or invalid bearer token") {
		t.Errorf("error message = %q, want it to surface the problem's title and detail", ce.Error())
	}
}

func TestGetJSONNotFound(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no node with id media-99"}`)
	})

	var resp nodeResponse
	err := c.getJSON(context.Background(), "/api/v1/nodes/media-99", nil, &resp)
	ce := requireCLIError(t, err)
	if ce.code != exitNotFound {
		t.Errorf("exit code = %d, want exitNotFound (%d)", ce.code, exitNotFound)
	}
}

func TestGetJSONVersionIncompatible(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/unsupported-api-version","title":"Unsupported API version","status":400,"detail":"This coordinator serves API version 2.","supportedVersions":[2]}`)
	})

	var resp serviceDescriptor
	err := c.getJSON(context.Background(), "/api/v1/", nil, &resp)
	ce := requireCLIError(t, err)
	if ce.code != exitVersionIncompatible {
		t.Errorf("exit code = %d, want exitVersionIncompatible (%d)", ce.code, exitVersionIncompatible)
	}
	if !containsAll(ce.Error(), "2") {
		t.Errorf("error message = %q, want it to name the coordinator's supported version", ce.Error())
	}
}

// TestSuccessfulResponseWithMismatchedVersionHeaderIsRejected covers the
// defensive check in checkAPIVersionHeader: contract §6.6 says a version
// mismatch is always a 400, but this CLI refuses to render a 2xx body
// anyway if the header disagrees, rather than trust the body blindly.
func TestSuccessfulResponseWithMismatchedVersionHeaderIsRejected(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "2")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","nodes":[]}`)
	})

	var resp nodesResponse
	err := c.getJSON(context.Background(), "/api/v1/nodes", nil, &resp)
	ce := requireCLIError(t, err)
	if ce.code != exitVersionIncompatible {
		t.Errorf("exit code = %d, want exitVersionIncompatible (%d)", ce.code, exitVersionIncompatible)
	}
}

func TestGetJSONNonProblemErrorBody(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = fmt.Fprint(w, `<html><body>502 Bad Gateway</body></html>`)
	})

	var resp nodesResponse
	err := c.getJSON(context.Background(), "/api/v1/nodes", nil, &resp)
	ce := requireCLIError(t, err)
	if ce.code != exitAPIError {
		t.Errorf("exit code = %d, want exitAPIError (%d) for a non-problem+json error body", ce.code, exitAPIError)
	}
}

// TestGetRawRejectsOversizedBody pins the bound added for Step 3 review
// finding 4.7 ("Unbounded io.ReadAll of the response body, where the FPP
// collector deliberately bounds its reads"): a body larger than
// maxResponseBytes must be rejected outright, never silently truncated
// and handed to the JSON decoder as if it were a short, valid document.
func TestGetRawRejectsOversizedBody(t *testing.T) {
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusOK)
		chunk := make([]byte, 1<<20)
		for i := range chunk {
			chunk[i] = 'a'
		}
		var written int64
		for written <= maxResponseBytes {
			n, _ := w.Write(chunk)
			written += int64(n)
		}
	})

	var resp nodesResponse
	err := c.getJSON(context.Background(), "/api/v1/nodes", nil, &resp)
	ce := requireCLIError(t, err)
	if ce.code != exitAPIError {
		t.Errorf("exit code = %d, want exitAPIError (%d) for an oversized body", ce.code, exitAPIError)
	}
	if !strings.Contains(ce.Error(), "byte limit") {
		t.Errorf("error message = %q, want it to mention the byte limit", ce.Error())
	}
}

func TestClassifyRequestErrorUnreachable(t *testing.T) {
	c, err := newClient("http://127.0.0.1:1", "", &http.Client{Timeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	var resp nodesResponse
	getErr := c.getJSON(context.Background(), "/api/v1/nodes", nil, &resp)
	ce := requireCLIError(t, getErr)
	if ce.code != exitUnreachable {
		t.Errorf("exit code = %d, want exitUnreachable (%d)", ce.code, exitUnreachable)
	}
}

func TestNewClientRejectsInvalidServer(t *testing.T) {
	for _, bad := range []string{"", "not-a-url", "ftp://example.com", "http://"} {
		if _, err := newClient(bad, "", &http.Client{}); err == nil {
			t.Errorf("newClient(%q) = nil error, want a usage error", bad)
		}
	}
}

func TestApplyHeadersSetsBearerTokenWhenConfigured(t *testing.T) {
	c, err := newClient("http://example.invalid", "s3cret", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/api/v1/nodes", nil)
	c.applyHeaders(req)
	if got := req.Header.Get("Authorization"); got != "Bearer s3cret" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer s3cret")
	}
}

func TestApplyHeadersOmitsAuthorizationWhenTokenUnset(t *testing.T) {
	c, err := newClient("http://example.invalid", "", &http.Client{})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://example.invalid/api/v1/nodes", nil)
	c.applyHeaders(req)
	if got := req.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization header = %q, want empty when no token is configured", got)
	}
}

func requireCLIError(t *testing.T, err error) *cliError {
	t.Helper()
	if err == nil {
		t.Fatal("got nil error, want a *cliError")
	}
	ce, ok := err.(*cliError)
	if !ok {
		t.Fatalf("err = %T (%v), want *cliError", err, err)
	}
	return ce
}

func containsAll(s string, substrs ...string) bool {
	for _, sub := range substrs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
