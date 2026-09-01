package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file is the showmeshctl test suite for "resolume recovery", over
// GET /resolume/recovery, GET/PUT /config/resolume.recovery(/revisions),
// and POST /resolume/recovery/restore (Track D seam D-3a).

func TestCmdResolumeRecoveryStatusPrintsToggleAndRecord(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/resolume/recovery" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T00:00:00Z","resolumeConfigured":true,"autoRestoreEnabled":true,"autoRestoreConfigured":false,
			"settleDelaySeconds":8,"record":[
				{"layer":"Whole House 1","layerNameGenerated":false,"state":"clip","clip":"Green screen snowstorm",
				 "clipNameGenerated":false,"deck":"Main","establishedAt":"2026-08-16T00:00:00Z","source":"action"},
				{"layer":"Whole House 2","layerNameGenerated":false,"state":"unknown","reason":"no record has ever been established for this layer"}
			],"lastRestore":null}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeRecovery([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"auto-restore: true", "default", "Whole House 1", "Green screen snowstorm", "Whole House 2", "unknown", "no restore has run yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestCmdResolumeRecoveryEnablePUTsTrue(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/api/v1/config/resolume.recovery" {
			t.Errorf("unexpected request %s %q", r.Method, r.URL.Path)
		}
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if !strings.Contains(string(body), `"autoRestoreEnabled":true`) {
			t.Errorf("request body = %s, want autoRestoreEnabled:true", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T00:00:00Z","kind":"resolume.recovery","revision":1,
			"payload":{"autoRestoreEnabled":true},"updatedAt":"2026-08-16T00:00:00Z",
			"createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeRecovery([]string{"enable", "--server", ts.URL, "--token", "sometoken"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "revision 1 is now active") {
		t.Errorf("output = %q, want it to confirm the new active revision", stdout.String())
	}
}

func TestCmdResolumeRecoveryDisablePUTsFalse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		if !strings.Contains(string(body), `"autoRestoreEnabled":false`) {
			t.Errorf("request body = %s, want autoRestoreEnabled:false", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T00:00:00Z","kind":"resolume.recovery","revision":2,
			"payload":{"autoRestoreEnabled":false},"updatedAt":"2026-08-16T00:00:00Z",
			"createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeRecovery([]string{"disable", "--server", ts.URL, "--token", "sometoken"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
}

func TestCmdResolumeRecoveryRestoreExitCodesMatchOutcome(t *testing.T) {
	tests := []struct {
		name     string
		outcome  string
		wantCode int
	}{
		{"restored", "restored", exitOK},
		{"nothing_to_do", "nothing_to_do", exitOK},
		{"partial", "partial", exitRestoreIncomplete},
		{"failed", "failed", exitActionFailed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/v1/resolume/recovery/restore" {
					t.Errorf("unexpected request %s %q", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("ShowMesh-API-Version", "1")
				_, _ = fmt.Fprintf(w, `{"serverTime":"2026-08-16T00:00:00Z","restore":{
					"startedAt":"2026-08-16T00:00:00Z","finishedAt":"2026-08-16T00:00:01Z","trigger":"manual",
					"outcome":%q,"principal":"admin","layers":[
						{"layer":"Whole House 1","layerNameGenerated":false,"result":"restored"}
					]}}`, tt.outcome)
			}))
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdResolumeRecovery([]string{"restore", "--server", ts.URL, "--token", "sometoken"}, &stdout, &stderr, time.Now)
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d (%s); stdout=%s stderr=%s", code, tt.wantCode, tt.name, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.outcome) {
				t.Errorf("stdout = %q, want it to name the outcome %q", stdout.String(), tt.outcome)
			}
		})
	}
}

func TestCmdResolumeRecoveryRevisionsListsHistory(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/config/resolume.recovery/revisions" {
			t.Errorf("unexpected request path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T00:00:00Z","kind":"resolume.recovery","revisions":[
			{"revision":1,"createdAt":"2026-08-16T00:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api","note":"","active":true}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeRecovery([]string{"revisions", "--server", ts.URL, "--token", "sometoken"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
}

// TestCmdResolumeDispatchesRecovery proves "showmeshctl resolume recovery"
// is reachable through the real top-level dispatcher, mirroring
// TestCmdResolumeDispatchesStatus's identical reasoning (CLAUDE.md's
// "Step 6's own lesson": a capability nothing calls is not shipped).
func TestCmdResolumeDispatchesRecovery(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T00:00:00Z","autoRestoreEnabled":true,"autoRestoreConfigured":false,
			"settleDelaySeconds":8,"record":[],"lastRestore":null}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{"resolume", "recovery", "status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
}

// TestMinResolumeRecoveryRestoreClientTimeoutExceedsServerBound reconciles
// this program's own client-timeout floor against the server's write
// deadline it is sized from — see cmd_resolume_recovery.go's own doc
// comment on why this is two independently-chosen literals rather than
// one shared constant (this program cannot import the coordinator's api
// package; its own importgraph_test.go forbids the reverse). Asserts
// STRICT inequality: a client budget merely equal to the server's deadline
// is the Step 7 defect (CLAUDE.md), since the server ALSO spends time past
// its own deadline computation (the post-restore audit write) before it
// can answer.
func TestMinResolumeRecoveryRestoreClientTimeoutExceedsServerBound(t *testing.T) {
	// This is the server's own resolumeRecoveryRestoreDeadline formula
	// (internal/coordinator/api/resolumerecovery.go), duplicated by value:
	// resolumeRecoveryMaxLayers * 40s (matching resolume.MaxDispatchDuration)
	// plus its own two rounds of bookkeeping and its own margin.
	serverBound := time.Duration(resolumeRecoveryMaxLayers)*40*time.Second + 2*resolumeRecoveryBookkeepingBudget + resolumeRecoveryDeadlineMargin
	if minResolumeRecoveryRestoreClientTimeout <= serverBound {
		t.Fatalf("minResolumeRecoveryRestoreClientTimeout = %s, want STRICTLY more than the server's own worst-case bound %s",
			minResolumeRecoveryRestoreClientTimeout, serverBound)
	}
}
