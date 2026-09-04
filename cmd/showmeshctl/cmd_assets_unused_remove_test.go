package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file is this seam's own showmeshctl coverage: "assets unused" (GET
// /nodes/{nodeId}/assets/unused) and "assets remove" (POST
// /nodes/{nodeId}/assets/remove), mirroring cmd_assets_test.go's existing
// "assets manifest" test shapes one command over.

func TestCmdAssetsUnusedPrintsTable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/render-01/assets/unused" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{
			"serverTime":"2026-08-10T21:00:00Z",
			"node":"render-01",
			"state":"ready",
			"observedAt":"2026-08-10T20:00:00Z",
			"unused":[{"contentHash":"sha256:stale","filename":"Opening-old.fseq","sizeBytes":512,"sequence":"opening"}]
		}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"unused", "--server", ts.URL, "render-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"render-01", "sha256:stale", "opening", "Opening-old.fseq"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdAssetsUnusedUnknownWithholdsList proves the CLI never prints a
// fabricated "no unused assets" line for a node whose state is unknown  -
// it must state the reason instead, matching the API's own withhold rule.
func TestCmdAssetsUnusedUnknownWithholdsList(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{
			"serverTime":"2026-08-10T21:00:00Z",
			"node":"render-01",
			"state":"unknown",
			"reason":"no inventory report has ever been received from this node",
			"unused":[]
		}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"unused", "--server", ts.URL, "render-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "unknown") || !strings.Contains(out, "no inventory report has ever been received") {
		t.Errorf("output does not state the unknown reason:\n%s", out)
	}
	if strings.Contains(out, "no unused assets") {
		t.Errorf("output claims \"no unused assets\" on an unknown verdict, want the reason withheld instead:\n%s", out)
	}
}

func TestCmdAssetsRemoveHappyPath(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/nodes/render-01/assets/remove" || r.Method != http.MethodPost {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{
			"serverTime":"2026-08-10T21:00:00Z",
			"command":{
				"commandId":"cmd-1","idempotencyKey":"idem-1","node":"render-01",
				"contentHash":"sha256:stale","replay":false,"outcome":"confirmed",
				"dispatchedAt":"2026-08-10T20:59:00Z","resolvedAt":"2026-08-10T20:59:01Z"
			}
		}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"remove", "--server", ts.URL, "render-01", "sha256:stale"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"render-01", "confirmed", "sha256:stale"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// TestCmdAssetsRemoveConflictPrintsCueName proves acceptance criterion 3
// reaches the CLI: a 409 refusal's Detail (naming the referencing Cue) is
// printed, not swallowed.
func TestCmdAssetsRemoveConflictPrintsCueName(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(http.StatusConflict)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/conflict","title":"Asset removal refused: still referenced by a Cue","status":409,"detail":"content hash \"sha256:thriller\" on node \"render-01\" is referenced by the following Cue(s) in this node's current Cue catalog and cannot be removed: \"thriller\"","serverTime":"2026-08-10T21:00:00Z"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"remove", "--server", ts.URL, "render-01", "sha256:thriller"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code == exitOK {
		t.Fatalf("exit code = exitOK, want a failure exit for a 409 refusal; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), `"thriller"`) {
		t.Errorf("stderr does not name the referencing cue:\n%s", stderr.String())
	}
}

func TestCmdAssetsRemoveMissingArgsIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdAssets([]string{"remove", "render-01"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
}
