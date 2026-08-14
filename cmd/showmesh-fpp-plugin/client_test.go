package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestClassifyHTTPStatus(t *testing.T) {
	cases := []struct {
		status int
		want   string
	}{
		{200, classOK},
		{202, classOK},
		{299, classOK},
		{401, classRefused},
		{403, classRefused},
		{400, classRejected},
		{404, classRejected},
		{409, classRejected},
		{429, classRejected},
		{499, classRejected},
		{500, classUnreachable},
		{502, classUnreachable},
		{503, classUnreachable},
		{599, classUnreachable},
		{600, classUnreachable}, // anything this build does not specifically recognize
	}
	for _, tc := range cases {
		if got := classifyHTTPStatus(tc.status); got != tc.want {
			t.Errorf("classifyHTTPStatus(%d) = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSubmitMacroRunOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/macros/my-macro/runs" {
			t.Errorf("path = %s, want /api/v1/macros/my-macro/runs", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization header = %q, want Bearer secret-token", got)
		}
		var body createMacroRunRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Trigger != "plugin" {
			t.Errorf("trigger = %q, want plugin", body.Trigger)
		}
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(macroRunSubmitResponse{
			Run: macroRun{ID: "run-1", MacroObjectID: "my-macro", MacroRevision: 3},
		})
	}))
	defer srv.Close()

	result := submitMacroRun(context.Background(), srv.Client(), mustParseURL(t, srv.URL), "secret-token", "my-macro",
		createMacroRunRequest{IdempotencyKey: "k1", Trigger: "plugin"})

	if result.Class != classOK {
		t.Fatalf("class = %q, want %q", result.Class, classOK)
	}
	if result.HTTPStatus != http.StatusAccepted {
		t.Errorf("httpStatus = %d, want 202", result.HTTPStatus)
	}
	if result.Run == nil || result.Run.Run.ID != "run-1" {
		t.Errorf("decoded run = %+v, want ID run-1", result.Run)
	}
}

func TestSubmitMacroRunRefusedAndRejectedAndUnreachable(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantClass  string
		wantStatus int
	}{
		{"401 no credential", http.StatusUnauthorized, `{"type":"t","title":"unauthorized","status":401,"detail":"no credential"}`, classRefused, 401},
		{"403 missing scope", http.StatusForbidden, `{"type":"t","title":"forbidden","status":403,"detail":"missing scope show:macro:run"}`, classRefused, 403},
		{"404 unknown macro", http.StatusNotFound, `{"type":"t","title":"not found","status":404,"detail":"unknown macro"}`, classRejected, 404},
		{"409 in flight", http.StatusConflict, `{"type":"t","title":"conflict","status":409,"detail":"already running"}`, classRejected, 409},
		{"500 server error", http.StatusInternalServerError, `{"type":"t","title":"internal error","status":500,"detail":"unexpected panic"}`, classUnreachable, 500},
		{"503 audit unavailable", http.StatusServiceUnavailable, `{"type":"t","title":"unavailable","status":503,"detail":"audit store unwritable"}`, classUnreachable, 503},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/problem+json")
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			result := submitMacroRun(context.Background(), srv.Client(), mustParseURL(t, srv.URL), "tok", "my-macro",
				createMacroRunRequest{IdempotencyKey: "k1", Trigger: "plugin"})

			if result.Class != tc.wantClass {
				t.Errorf("class = %q, want %q", result.Class, tc.wantClass)
			}
			if result.HTTPStatus != tc.wantStatus {
				t.Errorf("httpStatus = %d, want %d", result.HTTPStatus, tc.wantStatus)
			}
			if result.Problem == nil || result.Problem.Detail == "" {
				t.Errorf("expected a decoded problem with a detail, got %+v", result.Problem)
			}
		})
	}
}

func TestSubmitMacroRunTransportFailureIsUnreachableWithZeroStatus(t *testing.T) {
	// Point at a closed port: bind a listener to get a genuinely free
	// port, then close it immediately so that port deterministically
	// refuses the next connection, rather than relying on an arbitrary
	// unassigned port number that might coincidentally be in use.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	closedURL := "http://" + addr
	result := submitMacroRun(context.Background(), &http.Client{Timeout: 2 * time.Second}, mustParseURL(t, closedURL), "tok", "my-macro",
		createMacroRunRequest{IdempotencyKey: "k1", Trigger: "plugin"})

	if result.Class != classUnreachable {
		t.Errorf("class = %q, want %q", result.Class, classUnreachable)
	}
	if result.HTTPStatus != 0 {
		t.Errorf("httpStatus = %d, want 0 (no response was ever received)", result.HTTPStatus)
	}
	if result.TransportErr == nil {
		t.Error("expected a non-nil TransportErr for a connection failure")
	}
}

func TestFetchMacroConfigOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if r.URL.Path != "/api/v1/config/show.macro/my-macro" {
			t.Errorf("path = %s, want /api/v1/config/show.macro/my-macro", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer secret-token" {
			t.Errorf("Authorization header = %q, want Bearer secret-token", got)
		}
		_ = json.NewEncoder(w).Encode(showMacroConfigResponse{
			ID: "my-macro", Revision: 5,
			Payload: configShowMacro{
				Label: "Begin Set",
				Steps: []configShowMacroStep{
					{ID: "s1", LocalFallback: configShowMacroLocalFallback{Class: "coordinator-required", Reason: "r1"}},
				},
			},
		})
	}))
	defer srv.Close()

	cfg, err := fetchMacroConfig(context.Background(), srv.Client(), mustParseURL(t, srv.URL), "secret-token", "my-macro")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Revision != 5 || cfg.Payload.Label != "Begin Set" || len(cfg.Payload.Steps) != 1 {
		t.Errorf("cfg = %+v, want revision 5, label Begin Set, one step", cfg)
	}
}

func TestFetchMacroConfigNon2xxIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"t","title":"forbidden","status":403,"detail":"missing scope"}`))
	}))
	defer srv.Close()

	_, err := fetchMacroConfig(context.Background(), srv.Client(), mustParseURL(t, srv.URL), "tok", "my-macro")
	if err == nil {
		t.Fatal("expected an error for a 403 response")
	}
}
