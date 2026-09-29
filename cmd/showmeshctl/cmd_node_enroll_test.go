package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNodeInstallCommand(t *testing.T) {
	cases := map[string]string{
		"0.2.0":       "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.2.0/get-showmesh.sh | sudo bash -s -- --coordinator http://c:8080 --code ABCD-2345",
		"0.2.0-rc.1":  "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.2.0-rc.1/get-showmesh.sh | sudo bash -s -- --coordinator http://c:8080 --code ABCD-2345",
		"dev":         "sudo showmesh-install --coordinator http://c:8080 --code ABCD-2345",
		"":            "sudo showmesh-install --coordinator http://c:8080 --code ABCD-2345",
		"0.2.0-dirty": "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.2.0-dirty/get-showmesh.sh | sudo bash -s -- --coordinator http://c:8080 --code ABCD-2345",
	}
	for version, want := range cases {
		if got := nodeInstallCommand(version, "http://c:8080", "ABCD-2345"); got != want {
			t.Errorf("version %q:\n got %s\nwant %s", version, got, want)
		}
	}
}

func enrollTestServer(t *testing.T, publicURL, version string, gotBody *createNodeEnrollmentRequest) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/node-enrollments":
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, gotBody)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"serverTime":"2026-09-23T10:00:00Z","id":"e1","nodeId":"render-01","code":"ABCD-2345","reenroll":false,"expiresAt":"2026-09-23T10:15:00Z","coordinatorUrl":"`+publicURL+`"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/":
			_, _ = io.WriteString(w, `{"serverTime":"2026-09-23T10:00:00Z","apiVersion":1,"supportedVersions":[1],"coordinator":{"version":"`+version+`"}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/node-enrollments/redeem":
			t.Error("the CLI must never call redeem")
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestNodeEnrollPrintsCodeAndCommand(t *testing.T) {
	var body createNodeEnrollmentRequest
	srv := enrollTestServer(t, "", "dev", &body)
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	code := run([]string{"node", "enroll", "--server", srv.URL, "--expires", "30m", "render-01"}, &stdout, &stderr,
		func() time.Time { return time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC) })
	if code != exitOK {
		t.Fatalf("exit %d; stderr %s", code, stderr.String())
	}
	if body.NodeID != "render-01" || body.ExpiresInSeconds != 1800 || body.Reenroll {
		t.Fatalf("request body = %+v", body)
	}
	out := stdout.String()
	for _, want := range []string{"Code:        ABCD-2345", "Node:        render-01", "(in 15m0s)",
		"sudo showmesh-install --coordinator http://COORDINATOR-ADDRESS:" + srv.URL[strings.LastIndex(srv.URL, ":")+1:] + " --code ABCD-2345",
		"COORDINATOR-ADDRESS is a placeholder", "Set SHOWMESH_PUBLIC_URL on the coordinator, or run this command with --server",
		"shown only now"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "127.0.0.1") {
		t.Errorf("output prints a loopback address a node cannot reach:\n%s", out)
	}
}

func TestReplaceLoopbackHost(t *testing.T) {
	cases := map[string]string{
		"http://localhost:8080":    "http://COORDINATOR-ADDRESS:8080",
		"http://127.0.0.1:8080":    "http://COORDINATOR-ADDRESS:8080",
		"http://127.1.2.3":         "http://COORDINATOR-ADDRESS",
		"https://[::1]:8443":       "https://COORDINATOR-ADDRESS:8443",
		"http://LocalHost:80/":     "http://COORDINATOR-ADDRESS:80/",
		"http://showmesh.lan:8080": "http://showmesh.lan:8080",
		"http://192.168.1.10:8080": "http://192.168.1.10:8080",
	}
	for in, want := range cases {
		got, replaced := replaceLoopbackHost(in)
		if got != want || replaced != (in != want) {
			t.Errorf("replaceLoopbackHost(%q) = %q, %v; want %q", in, got, replaced, want)
		}
	}
}

func TestNodeEnrollUsesPublicURLAndReleaseCommand(t *testing.T) {
	var body createNodeEnrollmentRequest
	srv := enrollTestServer(t, "http://showmesh.lan:8080", "0.2.0", &body)
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"node", "enroll", "--server", srv.URL, "--reenroll", "render-01"}, &stdout, &stderr, time.Now); code != exitOK {
		t.Fatalf("exit %d; stderr %s", code, stderr.String())
	}
	if !body.Reenroll || body.ExpiresInSeconds != 900 {
		t.Fatalf("request body = %+v", body)
	}
	want := "curl -fsSL https://github.com/ShowMeshSystems/showmesh/releases/download/v0.2.0/get-showmesh.sh | sudo bash -s -- --coordinator http://showmesh.lan:8080 --code ABCD-2345"
	if !strings.Contains(stdout.String(), want) {
		t.Fatalf("output lacks %q:\n%s", want, stdout.String())
	}
}

func TestNodeEnrollJSONAndBadExpiry(t *testing.T) {
	var body createNodeEnrollmentRequest
	srv := enrollTestServer(t, "", "dev", &body)
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"node", "enroll", "--server", srv.URL, "--json", "render-01"}, &stdout, &stderr, time.Now); code != exitOK {
		t.Fatalf("exit %d; stderr %s", code, stderr.String())
	}
	var got createNodeEnrollmentResponse
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil || got.Code != "ABCD-2345" {
		t.Fatalf("--json output %q: %v", stdout.String(), err)
	}
	stdout.Reset()
	if code := run([]string{"node", "enroll", "--server", srv.URL, "--expires", "25h", "render-01"}, &stdout, &stderr, time.Now); code != exitUsage {
		t.Fatalf("--expires 25h exit = %d, want %d", code, exitUsage)
	}
}

func TestNodeEnrollConflictUsesExitConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(w, `{"type":"https://showmesh.dev/problems/conflict","title":"Conflict","status":409,"detail":"Node \"render-01\" is already enrolled. Mint a re-enrollment code to replace its credentials.","serverTime":"2026-09-23T10:00:00Z"}`)
	}))
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"node", "enroll", "--server", srv.URL, "render-01"}, &stdout, &stderr, time.Now); code != exitConflict {
		t.Fatalf("exit = %d, want exitConflict; stderr %s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "already enrolled") {
		t.Fatalf("stderr lacks the coordinator's message: %s", stderr.String())
	}
}
