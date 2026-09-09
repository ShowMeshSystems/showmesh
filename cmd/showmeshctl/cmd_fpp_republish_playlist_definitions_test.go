package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// republishServer answers one republish with the plugin evidence the
// coordinator carries through, and records the request body so a minted
// key can be proven to have been sent.
func republishServer(t *testing.T, applied bool, cleared, held, refused int, sweepPending bool) (*httptest.Server, *[]byte) {
	t.Helper()
	var raw []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ = io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		_, _ = fmt.Fprintf(w, `{"serverTime":"2026-08-13T22:00:00Z","republish":{
			"instanceId":"bench-fpp","requestId":%q,"applied":%t,"definitionsCleared":%d,
			"definitionsHeld":%d,"definitionsRefusedTerminally":%d,"sweepPending":%t}}`,
			fmt.Sprint(body["requestId"]), applied, cleared, held, refused, sweepPending)
	}))
	t.Cleanup(ts.Close)
	return ts, &raw
}

// TestCmdFPPRepublishPlaylistDefinitionsReportsAcceptanceNotArrival is
// this command's whole point. The counts printed are the plugin's own
// state, and the output must say in words that the resend is owed rather
// than done, because an operator who reads "6 definitions" as six
// definitions imported will stop looking at exactly the wrong moment.
func TestCmdFPPRepublishPlaylistDefinitionsReportsAcceptanceNotArrival(t *testing.T) {
	ts, raw := republishServer(t, true, 6, 0, 1, true)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"republish-playlist-definitions", "--server", ts.URL, "bench-fpp"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"the plugin agreed to resend",
		"cleared 6 definition(s) for resend",
		"0 still held",
		"1 refused terminally and will not be re-sent",
		"the resend is owed, not done",
		"showmeshctl fpp playlist-definitions list",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q, want it to contain %q", out, want)
		}
	}
	// The words that would blur an accepted request into a completed
	// import. None of them can honestly appear here.
	for _, forbidden := range []string{"imported", "definitions arrived", "definitions received"} {
		if strings.Contains(out, forbidden) {
			t.Errorf("stdout = %q, want it never to say %q: nothing has been imported when the plugin answers", out, forbidden)
		}
	}
	if !strings.Contains(string(*raw), `"requestId":"`) {
		t.Errorf("request body = %s, want a minted requestId", string(*raw))
	}
}

// TestCmdFPPRepublishPlaylistDefinitionsAppliedFalseIsASuccess: a repeated
// --request-id clears nothing and exits 0. It is also how an operator
// learns the plugin finished sending, so it must not read as a failure.
func TestCmdFPPRepublishPlaylistDefinitionsAppliedFalseIsASuccess(t *testing.T) {
	ts, _ := republishServer(t, false, 0, 4, 1, false)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"republish-playlist-definitions", "--server", ts.URL, "--request-id", "req-1", "bench-fpp"},
		&stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK - applied=false is a success; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "already applied") || !strings.Contains(out, "nothing cleared") {
		t.Errorf("stdout = %q, want it to say this requestId was already applied and nothing was cleared", out)
	}
	// Sending finished is still not arrival, and the wording must keep
	// those apart.
	if !strings.Contains(out, "finished sending") || !strings.Contains(out, "not the same as the coordinator having stored") {
		t.Errorf("stdout = %q, want it to distinguish the plugin finishing its send from the coordinator having stored it", out)
	}
}

// TestCmdFPPRepublishPlaylistDefinitionsJSONCarriesTheWholeResult: the
// json output is the response verbatim, so a script gets sweepPending and
// the terminal-refusal count rather than a prose summary.
func TestCmdFPPRepublishPlaylistDefinitionsJSONCarriesTheWholeResult(t *testing.T) {
	ts, _ := republishServer(t, true, 6, 0, 1, true)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"republish-playlist-definitions", "--server", ts.URL, "--output", "json", "bench-fpp"},
		&stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	var got struct {
		Republish struct {
			Applied                      bool `json:"applied"`
			DefinitionsCleared           int  `json:"definitionsCleared"`
			DefinitionsRefusedTerminally int  `json:"definitionsRefusedTerminally"`
			SweepPending                 bool `json:"sweepPending"`
		} `json:"republish"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("decode json output: %v; stdout=%s", err, stdout.String())
	}
	if !got.Republish.Applied || got.Republish.DefinitionsCleared != 6 ||
		got.Republish.DefinitionsRefusedTerminally != 1 || !got.Republish.SweepPending {
		t.Errorf("json republish = %+v, want applied, 6 cleared, 1 refused terminally, sweep pending", got.Republish)
	}
}

// TestCmdFPPRepublishPlaylistDefinitionsRequiresAnInstance keeps a
// mistyped invocation from posting to a host at all.
func TestCmdFPPRepublishPlaylistDefinitionsRequiresAnInstance(t *testing.T) {
	ts, raw := republishServer(t, true, 0, 0, 0, true)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"republish-playlist-definitions", "--server", ts.URL}, &stdout, &stderr, time.Now)
	if code != exitUsage {
		t.Fatalf("exit code = %d, want exitUsage; stderr=%s", code, stderr.String())
	}
	if len(*raw) != 0 {
		t.Errorf("a usage error still sent %s", string(*raw))
	}
}
