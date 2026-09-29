package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func cueActionsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		switch r.URL.Path {
		case "/api/v1/config/show.cue":
			_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.cue","objects":[
				{"id":"song-one","label":"Song one","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"},
				{"id":"thriller","label":"Thriller","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"},
				{"id":"broken","label":"Broken","show":"halloween-2026","currentRevision":1,"updatedAt":"2026-08-16T20:00:00Z"}
			]}`)
		case "/api/v1/config/show.cue/song-one":
			_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.cue","id":"song-one","revision":1,
				"payload":{"show":"halloween-2026","name":"Song one","outputs":{"render":{"sequence":"song-one"},"actions":["column-1","blackout-now"]}},
				"updatedAt":"2026-08-16T20:00:00Z","source":"api"}`)
		case "/api/v1/config/show.cue/broken":
			http.Error(w, `{"title":"Internal error"}`, http.StatusInternalServerError)
		case "/api/v1/config/show.cue/thriller":
			_, _ = fmt.Fprint(w, `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.cue","id":"thriller","revision":1,
				"payload":{"show":"halloween-2026","name":"Thriller","outputs":{"render":{"sequence":"thriller"}}},
				"updatedAt":"2026-08-16T20:00:00Z","source":"api"}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestCmdCueGetPrintsActionsInOrder(t *testing.T) {
	ts := cueActionsTestServer(t)
	defer ts.Close()
	var stdout, stderr bytes.Buffer
	if code := cmdCue([]string{"get", "--server", ts.URL, "song-one"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z"))); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "Actions:      column-1, blackout-now\n") {
		t.Fatalf("stdout = %q, want the actions line in firing order", stdout.String())
	}
}

func TestCmdCueListPrintsActionsColumn(t *testing.T) {
	ts := cueActionsTestServer(t)
	defer ts.Close()
	var stdout, stderr bytes.Buffer
	if code := cmdCue([]string{"list", "--server", ts.URL}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z"))); code != exitOK {
		t.Fatalf("exit code = %d; stderr=%s", code, stderr.String())
	}
	lines := strings.Split(stdout.String(), "\n")
	if !strings.Contains(lines[0], "ACTIONS") {
		t.Fatalf("header = %q, want an ACTIONS column", lines[0])
	}
	if !strings.Contains(lines[1], "song-one") || !strings.HasSuffix(strings.TrimSpace(lines[1]), "column-1, blackout-now") {
		t.Fatalf("song-one row = %q, want its actions listed", lines[1])
	}
	if !strings.Contains(lines[2], "thriller") || !strings.HasSuffix(strings.TrimSpace(lines[2]), "-") {
		t.Fatalf("thriller row = %q, want - for no actions", lines[2])
	}
	if !strings.Contains(lines[3], "broken") || !strings.HasSuffix(strings.TrimSpace(lines[3]), "?") {
		t.Fatalf("broken row = %q, want ? for a cue that could not be read", lines[3])
	}
}

func TestReportCueActivateResponsePrintsActionOutcomes(t *testing.T) {
	resp := cueActivateResponse{
		CueID: "song-one", Aligned: true,
		Nodes: []cueActivationNodeOutcome{{NodeID: "audio-01", Dispatched: true, Confirmed: true, Outcome: "confirmed"}},
		Actions: []cueActionOutcome{
			{ActionID: "column-1", Outcome: "refused", OutcomeReason: "No composition is loaded."},
			{ActionID: "blackout-now", Outcome: "confirmed"},
		},
	}
	var stdout bytes.Buffer
	if code := reportCueActivateResponse(&stdout, resp); code != exitOK {
		t.Fatalf("exit code = %d, want exitOK: an action failure does not fail the cue's node outputs", code)
	}
	got := stdout.String()
	if !strings.Contains(got, "refused: song-one action column-1: No composition is loaded.\n") || !strings.Contains(got, "confirmed: song-one action blackout-now: \n") {
		t.Fatalf("output = %q, want one line per action outcome", got)
	}
}
