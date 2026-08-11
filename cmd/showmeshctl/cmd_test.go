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

func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func TestRunUnknownCommandIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
	if !strings.Contains(stderr.String(), "bogus") {
		t.Errorf("stderr = %q, want it to name the unrecognized command", stderr.String())
	}
}

func TestRunNoArgsIsUsageError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestRunHelpIsSuccess(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"help"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Errorf("exit code = %d, want exitOK", code)
	}
	if !strings.Contains(stdout.String(), "Exit codes:") {
		t.Error("help output does not document exit codes (task spec §3: \"Document them in --help\")")
	}
}

func TestCmdNodesTextOutputMarksStaleEvidence(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","nodes":[
			{"nodeId":"media-03","label":"Garage","platform":null,"agentVersion":null,"bootId":null,
			 "startedAt":null,"firstSeenAt":null,"updatedAt":null,"capabilities":[],
			 "controlPlane":{"state":"offline","reason":"no heartbeat within the staleness window"},
			 "evidence":{
			   "hello":{"signal":"node.hello","value":true,"unit":null,"state":"stale","reason":"aged out","observedAt":"2026-08-10T20:00:00Z","collectedAt":"2026-08-10T20:00:00Z","source":"s","quality":"direct"},
			   "lastWill":{"signal":"node.lastWill","value":null,"unit":null,"state":"not_collected","reason":"none observed","observedAt":null,"collectedAt":"2026-08-10T20:00:00Z","source":"s","quality":"direct"},
			   "heartbeat":{"signal":"node.heartbeat","value":null,"unit":null,"state":"unknown_age","reason":"retained delivery","observedAt":null,"collectedAt":"2026-08-10T20:00:00Z","source":"s","quality":"direct"}
			 }}
		]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNodes([]string{"--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-10T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "media-03") {
		t.Errorf("output missing node id:\n%s", out)
	}
	if strings.Contains(out, "OFFLINE") && !strings.Contains(out, "control-plane") {
		t.Errorf("output renders offline without the control-plane qualifier:\n%s", out)
	}
	for _, want := range []string{"STALE", "NOT-COLLECTED", "AGE-UNKNOWN"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q; non-current evidence states must be visibly distinguishable:\n%s", want, out)
		}
	}
}

func TestCmdNodesJSONOutput(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","nodes":[]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNodes([]string{"--server", ts.URL, "--output", "json"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"nodes"`) {
		t.Errorf("json output = %q, want it to contain a \"nodes\" key", stdout.String())
	}
}

func TestCmdNodesRejectsInvalidOutputFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdNodes([]string{"--output", "xml"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdNodeNotFound(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/resource-not-found","title":"Resource not found","status":404,"detail":"no node with id media-99"}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNode([]string{"--server", ts.URL, "media-99"}, &stdout, &stderr, time.Now)
	if code != exitNotFound {
		t.Errorf("exit code = %d, want exitNotFound (%d); stderr=%s", code, exitNotFound, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Resource not found") {
		t.Errorf("stderr = %q, want the problem's title surfaced", stderr.String())
	}
}

// TestCmdNodeRejectsBareNodeShapeWithNoServerTime is the inverse of what
// this test used to assert: an earlier version of decodeSingleNode
// tolerated a bare, unwrapped Node object with no serverTime at all (the
// shape the parallel Task D implementation shipped before this session's
// wiring pass fixed it to always wrap per contract §6.10) by falling back
// to rendering it anyway with an "ages are approximate" note. That
// tolerance has been removed: the API side is fixed now, and a client that
// quietly tolerates a server contract violation is how the contract stops
// being a contract. This body is a literal transcription of the shape
// that must now be REJECTED, loudly, not rendered.
func TestCmdNodeRejectsBareNodeShapeWithNoServerTime(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"nodeId":"media-03","label":null,"platform":null,"agentVersion":null,"bootId":null,
			"startedAt":null,"firstSeenAt":"2026-08-10T19:00:00Z","updatedAt":"2026-08-10T21:00:00Z",
			"capabilities":[],
			"controlPlane":{"state":"unknown","reason":"no evidence yet"},
			"evidence":{
			  "hello":{"signal":"node.hello","value":null,"unit":null,"state":"not_collected","reason":"x","observedAt":null,"collectedAt":"2026-08-10T21:00:00Z","source":"s","quality":"direct","validForSeconds":null},
			  "lastWill":{"signal":"node.lastWill","value":null,"unit":null,"state":"not_collected","reason":"x","observedAt":null,"collectedAt":"2026-08-10T21:00:00Z","source":"s","quality":"direct","validForSeconds":null},
			  "heartbeat":{"signal":"node.heartbeat","value":null,"unit":null,"state":"not_collected","reason":"x","observedAt":null,"collectedAt":"2026-08-10T21:00:00Z","source":"s","quality":"direct","validForSeconds":null}
			}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdNode([]string{"--server", ts.URL, "media-03"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatalf("exit code = exitOK, want a failure: a bare, serverTime-less node object violates contract section 6.2 and must be rejected, not rendered. stdout=%s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "serverTime") {
		t.Errorf("stderr = %q, want it to name the missing serverTime", stderr.String())
	}
}

func TestCmdNodeRequiresExactlyOneArgument(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cmdNode(nil, &stdout, &stderr, time.Now); code != exitUsage {
		t.Errorf("with no args: exit code = %d, want exitUsage", code)
	}

	stdout.Reset()
	stderr.Reset()
	if code := cmdNode([]string{"a", "b"}, &stdout, &stderr, time.Now); code != exitUsage {
		t.Errorf("with two args: exit code = %d, want exitUsage", code)
	}
}

func TestCmdVersionReportsIncompatibleAPIVersion(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = fmt.Fprint(w, `{"type":"https://showmesh.dev/problems/unsupported-api-version","title":"Unsupported API version","status":400,"detail":"This coordinator serves API version 2.","supportedVersions":[2]}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdVersion([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitVersionIncompatible {
		t.Errorf("exit code = %d, want exitVersionIncompatible (%d); stdout=%s stderr=%s", code, exitVersionIncompatible, stdout.String(), stderr.String())
	}
	if !strings.Contains(strings.ToLower(stdout.String()), "incompatible") {
		t.Errorf("stdout = %q, want it to say the versions are incompatible rather than attempt a partial render", stdout.String())
	}
	if strings.Contains(stdout.String(), "coordinator:  dev") {
		t.Errorf("stdout = %q, must not render coordinator fields when negotiation failed (no partial render)", stdout.String())
	}
}

func TestCmdVersionCompatible(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","apiVersion":1,"supportedVersions":[1],
			"coordinator":{"version":"dev","commit":"abc1234","buildDate":"2026-08-10T00:00:00Z","goVersion":"go1.25"}}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdVersion([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "negotiation: compatible") {
		t.Errorf("stdout = %q, want it to report a compatible negotiation", stdout.String())
	}
}

func TestCmdEventsRejectsInvalidSince(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := cmdEvents([]string{"--since", "not-a-number"}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Errorf("exit code = %d, want exitUsage (%d)", code, exitUsage)
	}
}

func TestCmdEventsPassesSinceAndLimitAsQueryParams(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","events":[],"latestSeq":0}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdEvents([]string{"--server", ts.URL, "--since", "10", "--limit", "5"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(gotQuery, "since=10") || !strings.Contains(gotQuery, "limit=5") {
		t.Errorf("query = %q, want since=10 and limit=5", gotQuery)
	}
}

// TestCmdEventsSurfacesGap covers a field the pinned contract §6.10 shape
// does not mention at all (gap/oldestRetainedSeq) but the real handler
// under parallel development sends — see types.go's comment on
// eventsResponse. A CLI that silently dropped it would hide the one thing
// that matters most about a pruned page: that it is not a complete
// history.
func TestCmdEventsSurfacesGap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","events":[],"latestSeq":50,"gap":true,"oldestRetainedSeq":37}`)
	}))
	defer ts.Close()

	var stdout, stderr bytes.Buffer
	code := cmdEvents([]string{"--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "GAP") || !strings.Contains(stdout.String(), "37") {
		t.Errorf("stdout = %q, want it to surface the gap and the oldest retained seq", stdout.String())
	}
}

// TestCmdEventsDecodesSeqBeyondInt64Range pins Step 3 review finding 4.7's
// seq type divergence: contract §6.10 and internal/coordinator/api/v1
// declare seq (and latestSeq/oldestRetainedSeq) as unsigned. A value that is
// valid uint64 but out of int64's range is the one input that actually
// distinguishes the two types — every seq value either type has ever
// realistically carried in this project's own lifetime decodes identically
// as either, which is exactly how this divergence went unnoticed. If Seq
// were still declared int64, decoding this body would fail outright.
func TestCmdEventsDecodesSeqBeyondInt64Range(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","events":[
			{"seq":18446744073709551615,"recordedAt":"2026-08-10T21:00:00Z","occurredAt":null,
			 "source":"s","resource":{"kind":"node","id":"n"},"category":"control_plane",
			 "severity":"informational","summary":"x","details":{},"correlationId":null}
		],"latestSeq":18446744073709551615,"gap":false,"oldestRetainedSeq":null}`)
	}))
	defer ts.Close()

	c, err := newClient(ts.URL, "", &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("newClient: %v", err)
	}
	var resp eventsResponse
	if err := c.getJSON(context.Background(), "/api/v1/events", nil, &resp); err != nil {
		t.Fatalf("getJSON: %v (seq beyond int64 range must decode cleanly into a uint64 field)", err)
	}
	const maxUint64 = ^uint64(0)
	if resp.LatestSeq != maxUint64 {
		t.Errorf("LatestSeq = %d, want %d", resp.LatestSeq, maxUint64)
	}
	if len(resp.Events) != 1 || resp.Events[0].Seq != maxUint64 {
		t.Errorf("Events[0].Seq = %+v, want %d", resp.Events, maxUint64)
	}
}

func TestCmdNodesClockSkewWarningPrintedToStderrOnly(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-10T21:00:00Z","nodes":[]}`)
	}))
	defer ts.Close()

	skewedLocal := mustParse(t, "2026-08-10T21:05:00Z") // 5 minutes off
	var stdout, stderr bytes.Buffer
	code := cmdNodes([]string{"--server", ts.URL}, &stdout, &stderr, fixedClock(skewedLocal))
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "clock") {
		t.Errorf("stderr = %q, want a clock-skew warning", stderr.String())
	}
	if strings.Contains(stdout.String(), "clock") {
		t.Errorf("stdout = %q, clock-skew warnings must go to stderr, not contaminate stdout data output", stdout.String())
	}
}
