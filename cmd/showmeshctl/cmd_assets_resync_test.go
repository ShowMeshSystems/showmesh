package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file is "assets resync" (POST /nodes/{nodeId}/assets/resync)'s own
// showmeshctl coverage.

func TestCmdAssetsResyncHappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/render-01/assets/resync" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusAccepted)
		_, _ = fmt.Fprint(w, `{
			"serverTime":"2026-08-10T21:00:00Z",
			"resync":{"node":"render-01","acceptedAt":"2026-08-10T21:00:00Z"}
		}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"resync", "--server", ts.URL, "render-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"render-01", "resync accepted"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdAssetsResyncDisabledSyncPrintsReason proves the "asset sync
// disabled" error path reaches the CLI as a stated failure, never a silent
// exitOK.
func TestCmdAssetsResyncDisabledSyncPrintsReason(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/invalid-parameter","title":"Invalid parameter","status":400,"detail":"asset sync is disabled (assets.settings' contentBaseUrl is not set): this coordinator will never deliver these assets to the node over the network","serverTime":"2026-08-10T21:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"resync", "--server", ts.URL, "render-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code == exitOK {
		t.Fatalf("exit code = exitOK, want a failure exit for a disabled-sync refusal; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "contentBaseUrl") {
		t.Errorf("stderr does not name the disabled setting:\n%s", stderr.String())
	}
}

func TestCmdAssetsResyncMissingArgsIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"resync"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}
