package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// excludedRangesServer answers one write with body and records the method
// and request body it was sent.
func excludedRangesServer(t *testing.T, status int, body string) (*httptest.Server, *string, *string) {
	t.Helper()
	var method, sent string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		method, sent = r.Method+" "+r.URL.Path, string(raw)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)
	return ts, &method, &sent
}

const excludedRangesCommand = `"command":{"id":"c1","idempotencyKey":"req-1","action":"setBrightnessExcludedRanges","instanceId":"bench-fpp","outcome":"%s","outcomeReason":"%s"}`

func TestCmdFPPSetBrightnessExcludedRangesSendsTheListAndPrintsTheReadBack(t *testing.T) {
	ts, method, sent := excludedRangesServer(t, http.StatusOK, `{"serverTime":"2026-08-13T22:00:00Z",`+
		strings.NewReplacer("%s", "confirmed").Replace(excludedRangesCommand)+
		`,"excludedRanges":[{"startChannel":1,"channelCount":512},{"startChannel":1025,"channelCount":512}]}`)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-brightness-excluded-ranges", "--server", ts.URL, "--request-id", "req-1", "bench-fpp", "1-512, 1025-512"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if *method != "PUT /api/v1/fpp/bench-fpp/brightness/excluded-ranges" {
		t.Errorf("request = %s, want a PUT to the excluded ranges route", *method)
	}
	if want := `{"ranges":[{"startChannel":1,"channelCount":512},{"startChannel":1025,"channelCount":512}],"requestId":"req-1"}`; strings.TrimSpace(*sent) != want {
		t.Errorf("request body = %s, want %s", *sent, want)
	}
	if out := stdout.String(); !strings.Contains(out, "confirmed: bench-fpp leaves these channels alone: 1-512, 1025-512") {
		t.Errorf("stdout = %q, want the list read back", out)
	}
}

func TestCmdFPPSetBrightnessExcludedRangesNoneClearsTheList(t *testing.T) {
	ts, _, sent := excludedRangesServer(t, http.StatusOK, `{"serverTime":"2026-08-13T22:00:00Z",`+
		strings.NewReplacer("%s", "confirmed").Replace(excludedRangesCommand)+`,"excludedRanges":[]}`)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-brightness-excluded-ranges", "--server", ts.URL, "bench-fpp", "none"}, &stdout, &stderr, time.Now)
	if code != exitOK {
		t.Fatalf("exit code = %d, want exitOK; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(*sent, `"ranges":[]`) {
		t.Errorf("request body = %s, want an empty ranges list", *sent)
	}
	if out := stdout.String(); !strings.Contains(out, "leaves these channels alone: none") {
		t.Errorf("stdout = %q, want it to say none are excluded", out)
	}
}

func TestCmdFPPSetBrightnessExcludedRangesUnconfirmedExitsNonZero(t *testing.T) {
	ts, _, _ := excludedRangesServer(t, http.StatusOK, `{"serverTime":"2026-08-13T22:00:00Z","command":{"id":"c1","idempotencyKey":"req-1",`+
		`"outcome":"unconfirmed","outcomeReason":"The player took 1-512 and has not reported it back yet. Check again shortly."}}`)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-brightness-excluded-ranges", "--server", ts.URL, "bench-fpp", "1-512"}, &stdout, &stderr, time.Now)
	if code != exitCommandUnconfirmed {
		t.Fatalf("exit code = %d, want exitCommandUnconfirmed; stdout=%s", code, stdout.String())
	}
	if out := stdout.String(); !strings.Contains(out, "unconfirmed: The player took 1-512") {
		t.Errorf("stdout = %q, want the coordinator's own reason", out)
	}
}

func TestCmdFPPSetBrightnessExcludedRangesRefusesABadFormBeforeSending(t *testing.T) {
	for _, bad := range []string{"1-512,abc", "512", "0-10", "5-0", "1-512,,7-2", "1:512"} {
		ts, method, _ := excludedRangesServer(t, http.StatusOK, `{}`)
		var stdout, stderr bytes.Buffer
		code := cmdFPP([]string{"set-brightness-excluded-ranges", "--server", ts.URL, "bench-fpp", bad}, &stdout, &stderr, time.Now)
		if code != exitUsage {
			t.Errorf("%q exit code = %d, want exitUsage", bad, code)
		}
		if !strings.Contains(stderr.String(), "Enter start-count pairs, like 1-512, 1025-512.") {
			t.Errorf("%q stderr = %q, want one line showing the form", bad, stderr.String())
		}
		if *method != "" {
			t.Errorf("%q reached the server: %s", bad, *method)
		}
	}
}

func TestCmdFPPSetBrightnessExcludedRangesPrintsTheCoordinatorsRefusal(t *testing.T) {
	ts, _, _ := excludedRangesServer(t, http.StatusBadRequest, `{"type":"https://showmesh.dev/problems/invalid-parameter","title":"Invalid parameter","status":400,`+
		`"detail":"Range 600-100 overlaps 1-1000. Use separate ranges, like 1-512, 1025-512.","serverTime":"2026-08-13T22:00:00Z"}`)

	var stdout, stderr bytes.Buffer
	code := cmdFPP([]string{"set-brightness-excluded-ranges", "--server", ts.URL, "bench-fpp", "1-1000,600-100"}, &stdout, &stderr, time.Now)
	if code == exitOK {
		t.Fatal("exit code = exitOK, want a failure")
	}
	if !strings.Contains(stderr.String(), "Range 600-100 overlaps 1-1000.") {
		t.Errorf("stderr = %q, want the coordinator's own sentence", stderr.String())
	}
}

// The status table shows the signal's value as the same text the write takes.
func TestFPPStatusShowsExcludedRangesBesideTheCeiling(t *testing.T) {
	var out bytes.Buffer
	printObservations(&out, []evidence{
		{Signal: "fpp.brightness.ceiling", Value: float64(60), State: "current"},
		{Signal: "fpp.brightness.excluded_ranges", Value: "1-512, 1025-512", State: "current"},
	}, time.Now())
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[1], "fpp.brightness.ceiling") || !strings.Contains(lines[2], "1-512, 1025-512") {
		t.Errorf("status output = %q, want the excluded ranges on the row after the ceiling", out.String())
	}
}
