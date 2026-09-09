package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// This file is the evidence for the owner ruling this branch implements:
// -output json for nodes (list and single get), snapshot, night status,
// and resolume recovery status must emit the coordinator's response body
// unmodified, never a re-serialization of this package's own decoded
// struct. Every JSON body below is a hand-written literal, matching this
// package's own established rule (client_test.go): marshaling a struct
// and unmarshaling it back into the same struct proves nothing about the
// wire contract.

// --- printJSONBody's own two clauses (the only new branching logic this
// change adds) ---

func TestPrintJSONBodyEmptyRawStillNewlineTerminates(t *testing.T) {
	var out bytes.Buffer
	if err := printJSONBody(&out, []byte{}); err != nil {
		t.Fatalf("printJSONBody: %v", err)
	}
	if out.String() != "\n" {
		t.Errorf("output = %q, want a bare newline for empty raw input", out.String())
	}
}

func TestPrintJSONBodyAlreadyNewlineTerminatedIsUnchanged(t *testing.T) {
	var out bytes.Buffer
	raw := []byte("{\"a\":1}\n")
	if err := printJSONBody(&out, raw); err != nil {
		t.Fatalf("printJSONBody: %v", err)
	}
	if out.String() != string(raw) {
		t.Errorf("output = %q, want the input echoed with no second newline appended", out.String())
	}
}

func TestPrintJSONBodyMissingTrailingNewlineGetsOneAppended(t *testing.T) {
	var out bytes.Buffer
	if err := printJSONBody(&out, []byte(`{"a":1}`)); err != nil {
		t.Fatalf("printJSONBody: %v", err)
	}
	if out.String() != "{\"a\":1}\n" {
		t.Errorf("output = %q, want a trailing newline appended", out.String())
	}
}

// --- The central positive proof: a field NO CLI struct declares must
// reach -output json, and the SAME body decoded the old way (getJSON into
// a struct, then printJSON(struct)) must NOT show it. ---

func TestSnapshotJSONPassthroughSurfacesFieldNoStructDeclares(t *testing.T) {
	const body = `{"serverTime":"2026-08-10T21:00:00Z","latestEventSeq":0,"nodes":[],` +
		`"fpp":{"instances":[]},"collectors":[],"macroRuns":[],"resolume":[],` +
		`"auditStore":{"state":"usable","reason":null},` +
		`"futureFieldNoCLIStructDeclares":"surprise"}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSnapshot([]string{"--server", ts.URL, "--output", "json"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"futureFieldNoCLIStructDeclares":"surprise"`) {
		t.Errorf("passthrough JSON output dropped a field the snapshot struct does not declare; got:\n%s", stdout.String())
	}

	// The positive control: the OLD mechanism (getJSON into the struct,
	// then printJSON the struct) against the IDENTICAL body must NOT show
	// it -- proving this test would have failed before this change, not
	// merely that the new one passes.
	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	})
	var snap snapshot
	if err := c.getJSON(context.Background(), "/api/v1/snapshot", nil, &snap); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	var oldOut bytes.Buffer
	if err := printJSON(&oldOut, snap); err != nil {
		t.Fatalf("printJSON: %v", err)
	}
	if strings.Contains(oldOut.String(), "futureFieldNoCLIStructDeclares") {
		t.Fatalf("positive control failed: the OLD struct round-trip unexpectedly preserved the undeclared field, so this test proves nothing; got:\n%s", oldOut.String())
	}
}

func TestNodeJSONPassthroughSurfacesFieldNoStructDeclaresAndRenests(t *testing.T) {
	const body = `{"serverTime":"2026-08-27T00:00:00Z","node":{"nodeId":"media-03","label":null,"platform":null,` +
		`"agentVersion":null,"bootId":null,"startedAt":null,"firstSeenAt":"2026-08-27T00:00:00Z",` +
		`"updatedAt":"2026-08-27T00:00:00Z","capabilities":[],"controlPlane":{"state":"unknown","reason":"x"},` +
		`"evidence":{"hello":` + validEvidenceJSONForFPPConnectTest + `,"lastWill":` + validEvidenceJSONForFPPConnectTest +
		`,"heartbeat":` + validEvidenceJSONForFPPConnectTest + `},"declaration":` + validDeclarationJSONForFPPConnectTest +
		`,"render":[],"audio":[],"fppConnect":[],"futureFieldNoCLIStructDeclares":"surprise"}}`

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNode([]string{"--server", ts.URL, "--output", "json", "media-03"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"futureFieldNoCLIStructDeclares":"surprise"`) {
		t.Errorf("passthrough JSON output dropped a field the node struct does not declare; got:\n%s", stdout.String())
	}
	// The re-nesting: the response's own top-level "node" wrapper key must
	// survive into the output, proving this now emits the WRAPPED body
	// (contract section 6.10's own shape) rather than the unwrapped node
	// object this command used to print.
	if !strings.Contains(stdout.String(), `"node":{`) {
		t.Errorf("passthrough JSON output is not wrapped in the response's own \"node\" key; got:\n%s", stdout.String())
	}

	// Positive control: the OLD mechanism printed the UNWRAPPED node (no
	// "node" key, no top-level "serverTime") and dropped the undeclared
	// field. Reproduce it against the identical body via decodeSingleNode
	// (still this package's own real decode path, not a reimplementation).
	n, _, err := decodeSingleNode([]byte(body))
	if err != nil {
		t.Fatalf("decodeSingleNode: %v", err)
	}
	var oldOut bytes.Buffer
	if err := printJSON(&oldOut, n); err != nil {
		t.Fatalf("printJSON: %v", err)
	}
	if strings.Contains(oldOut.String(), "futureFieldNoCLIStructDeclares") {
		t.Fatalf("positive control failed: the OLD struct round-trip unexpectedly preserved the undeclared field; got:\n%s", oldOut.String())
	}
	if strings.Contains(oldOut.String(), `"node":`) {
		t.Fatalf("positive control failed: the OLD output was unexpectedly already wrapped; got:\n%s", oldOut.String())
	}
}

// --- Absent, null, empty and populated are four distinct wire inputs;
// passthrough must reproduce each exactly, never substitute a CLI-side
// default. resolumeConfigured is the sharpest case (Finding 1 from the
// prior review round on this same field). ---

func resolumeRecoveryServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
}

func TestResolumeRecoveryJSONPassthroughDistinguishesAbsentNullEmptyPopulated(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantSubstr string
		wantAbsent string
	}{
		{
			name: "absent",
			body: `{"serverTime":"2026-08-16T00:00:00Z","autoRestoreEnabled":true,"autoRestoreConfigured":true,` +
				`"settleDelaySeconds":8,"record":[],"lastRestore":null}`,
			wantAbsent: `"resolumeConfigured"`,
		},
		{
			name: "null",
			body: `{"serverTime":"2026-08-16T00:00:00Z","resolumeConfigured":null,"autoRestoreEnabled":true,` +
				`"autoRestoreConfigured":true,"settleDelaySeconds":8,"record":[],"lastRestore":null}`,
			wantSubstr: `"resolumeConfigured":null`,
		},
		{
			name: "populated false",
			body: `{"serverTime":"2026-08-16T00:00:00Z","resolumeConfigured":false,"autoRestoreEnabled":true,` +
				`"autoRestoreConfigured":true,"settleDelaySeconds":8,"record":[],"lastRestore":null}`,
			wantSubstr: `"resolumeConfigured":false`,
		},
		{
			name: "populated true, empty record",
			body: `{"serverTime":"2026-08-16T00:00:00Z","resolumeConfigured":true,"autoRestoreEnabled":true,` +
				`"autoRestoreConfigured":true,"settleDelaySeconds":8,"record":[],"lastRestore":null}`,
			wantSubstr: `"resolumeConfigured":true,"autoRestoreEnabled":true,"autoRestoreConfigured":true,"settleDelaySeconds":8,"record":[]`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := resolumeRecoveryServer(t, tc.body)
			defer ts.Close()

			var stdout, stderr bytes.Buffer
			code := cmdResolumeRecovery([]string{"status", "--server", ts.URL, "--output", "json"}, &stdout, &stderr, time.Now)
			if code != exitOK {
				t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
			}
			if tc.wantSubstr != "" && !strings.Contains(stdout.String(), tc.wantSubstr) {
				t.Errorf("output missing %q; got:\n%s", tc.wantSubstr, stdout.String())
			}
			if tc.wantAbsent != "" && strings.Contains(stdout.String(), tc.wantAbsent) {
				t.Errorf("output unexpectedly contains %q for the absent case; got:\n%s", tc.wantAbsent, stdout.String())
			}
			// Byte-exact passthrough: the body this program received IS the
			// body it printed, up to the trailing newline printJSONBody
			// guarantees.
			if strings.TrimRight(stdout.String(), "\n") != tc.body {
				t.Errorf("output is not byte-identical to the server's body\ngot:  %s\nwant: %s", strings.TrimRight(stdout.String(), "\n"), tc.body)
			}
		})
	}
}

// --- No re-indenting: the coordinator's own encoder never sets Indent, so
// passthrough output for a multi-field body must be single-line, not the
// two-space-indented multi-line shape printJSON produces. ---

func TestNodesListJSONPassthroughIsSingleLineNotIndented(t *testing.T) {
	const body = `{"serverTime":"2026-08-10T21:00:00Z","nodes":[]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNodes([]string{"--server", ts.URL, "--output", "json"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if got := strings.Count(stdout.String(), "\n"); got != 1 {
		t.Errorf("output has %d newline(s), want exactly 1 (single-line body plus its own trailing newline); got:\n%q", got, stdout.String())
	}
	if strings.Contains(stdout.String(), "  ") {
		t.Errorf("output contains two-space indentation, want the coordinator's own compact encoding preserved verbatim; got:\n%s", stdout.String())
	}
}

// --- Text mode is untouched: every converted command's TEXT rendering
// still comes from the same print function fed the same decoded struct,
// so its output must equal calling that print function directly. ---

func TestSnapshotTextModeUnchanged(t *testing.T) {
	const body = `{"serverTime":"2026-08-10T21:00:00Z","latestEventSeq":5,"nodes":[],` +
		`"fpp":{"instances":[]},"collectors":[],"macroRuns":[],"resolume":[],` +
		`"auditStore":{"state":"usable","reason":null}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdSnapshot([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	})
	var snap snapshot
	if err := c.getJSON(context.Background(), "/api/v1/snapshot", nil, &snap); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	var want bytes.Buffer
	printSnapshotDetail(&want, snap)

	if stdout.String() != want.String() {
		t.Errorf("text-mode output changed\ngot:\n%s\nwant:\n%s", stdout.String(), want.String())
	}
}

func TestNodeTextModeUnchanged(t *testing.T) {
	const body = `{"serverTime":"2026-08-27T00:00:00Z","node":{"nodeId":"media-03","label":"Garage","platform":null,` +
		`"agentVersion":null,"bootId":null,"startedAt":null,"firstSeenAt":"2026-08-27T00:00:00Z",` +
		`"updatedAt":"2026-08-27T00:00:00Z","capabilities":[],"controlPlane":{"state":"online","reason":null},` +
		`"evidence":{"hello":` + validEvidenceJSONForFPPConnectTest + `,"lastWill":` + validEvidenceJSONForFPPConnectTest +
		`,"heartbeat":` + validEvidenceJSONForFPPConnectTest + `},"declaration":` + validDeclarationJSONForFPPConnectTest +
		`,"render":[],"audio":[],"fppConnect":[]}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNode([]string{"--server", ts.URL, "media-03"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	n, serverTime, err := decodeSingleNode([]byte(body))
	if err != nil {
		t.Fatalf("decodeSingleNode: %v", err)
	}
	var want bytes.Buffer
	printNodeDetail(&want, n, serverTime)

	if stdout.String() != want.String() {
		t.Errorf("text-mode output changed\ngot:\n%s\nwant:\n%s", stdout.String(), want.String())
	}
}

func TestNodesListTextModeUnchanged(t *testing.T) {
	const body = `{"serverTime":"2026-08-10T21:00:00Z","nodes":[]}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNodes([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	var want bytes.Buffer
	printNodesTable(&want, nodesResponse{})
	if stdout.String() != want.String() {
		t.Errorf("text-mode output changed\ngot:\n%s\nwant:\n%s", stdout.String(), want.String())
	}
}

func TestNightStatusTextModeUnchanged(t *testing.T) {
	const body = `{"serverTime":"2026-08-18T22:00:00Z",
		"session":{"id":"s1","configObjectId":"halloween-main","configRevision":1,"state":"live",
		"stateEnteredAt":"2026-08-18T22:00:00Z","cycle":0,"finalShowRequested":false,"finalShowRequestedAt":null,
		"admissionClosed":false,"admissionClosedAt":null,"shutdownIntent":"","armedShowId":"","showCommitted":true,
		"readiness":{"state":"unknown","reason":"no readiness result recorded","sameEpoch":false,"fresh":false,"checks":[]},
		"powerPhase":{"state":"unknown","reason":""},"transition":{"state":"not_available","reason":""},
		"degraded":false,"updatedAt":"2026-08-18T22:00:00Z"}}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNight([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	})
	var resp nightSessionLifecycleResponse
	if err := c.getJSON(context.Background(), "/api/v1/night/session", nil, &resp); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	var want bytes.Buffer
	printNightSessionStateDetail(&want, resp.Session)

	if stdout.String() != want.String() {
		t.Errorf("text-mode output changed\ngot:\n%s\nwant:\n%s", stdout.String(), want.String())
	}
}

func TestResolumeRecoveryStatusTextModeUnchanged(t *testing.T) {
	const body = `{"serverTime":"2026-08-16T00:00:00Z","resolumeConfigured":true,"autoRestoreEnabled":true,"autoRestoreConfigured":false,
		"settleDelaySeconds":8,"record":[
			{"layer":"Whole House 1","layerNameGenerated":false,"state":"clip","clip":"Green screen snowstorm",
			 "clipNameGenerated":false,"deck":"Main","establishedAt":"2026-08-16T00:00:00Z","source":"action"}
		],"lastRestore":null}`
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdResolumeRecovery([]string{"status", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	c, _ := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, body)
	})
	var resp resolumeRecoveryResponse
	if err := c.getJSON(context.Background(), "/api/v1/resolume/recovery", nil, &resp); err != nil {
		t.Fatalf("getJSON: %v", err)
	}
	var want bytes.Buffer
	printResolumeRecoveryStatus(&want, resp)

	if stdout.String() != want.String() {
		t.Errorf("text-mode output changed\ngot:\n%s\nwant:\n%s", stdout.String(), want.String())
	}
}
