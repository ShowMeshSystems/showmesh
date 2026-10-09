package main

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const movePreviewBody = `{"serverTime":"2026-08-16T21:00:00Z","playlistId":"quick","revision":4,"newerAvailable":true,"canConfirm":true,
	"summary":"FPP's playlist changed. Review the changes to keep your cues on the right sequences.",
	"current":{"hash":"aa","receivedAt":"2026-08-16T19:00:00Z","entryCount":2},
	"newest":{"hash":"bb","receivedAt":"2026-08-16T20:20:00Z","entryCount":3},
	"entries":[
		{"entryId":"a","cue":"wake-up","filename":"Wake Up.fseq","outcome":"moved","matchedBy":"filename","from":{"section":"mainPlaylist","position":0},"to":{"section":"mainPlaylist","position":1},"duplicateFilename":false,"previousSequence":"","newSequence":"","needsCheck":false,"summary":"Moved from position 0 to position 1."},
		{"entryId":"b","cue":"old","filename":"","outcome":"dropped","matchedBy":"position","from":{"section":"mainPlaylist","position":5},"to":null,"duplicateFilename":false,"previousSequence":"Old.fseq","newSequence":"","needsCheck":true,"summary":"FPP's playlist has no entry at position 5 any more. This cue will be removed from this playlist."}],
	"newEntries":[{"section":"mainPlaylist","position":0,"name":"Opener.fseq","duplicateFilename":false,"summary":"New at position 0 in FPP's playlist. It has no cue yet; give it one after you confirm."}],
	"proposed":{"show":"halloween-2026","name":"Quick","runner":"fpp","mismatchPolicy":"hold",
		"fpp":{"instanceUuid":"u","playlistName":"p","playlistHash":"bb"},
		"entries":[{"id":"a","cue":"wake-up","fpp":{"section":"mainPlaylist","position":1,"expectedSequenceFilename":"Wake Up.fseq"}}]}}`

const movePutBody = `{"serverTime":"2026-08-16T21:00:00Z","kind":"show.playlist","id":"quick","revision":5,
	"payload":{"show":"halloween-2026","name":"Quick","runner":"fpp","entries":[]},
	"updatedAt":"2026-08-16T21:00:00Z","createdByPrincipalId":"p1","createdByPrincipalName":"admin","source":"api"}`

type moveServer struct {
	*httptest.Server
	methods []string
	ifMatch string
	putBody string
}

func newMoveServer(t *testing.T, previewBody string) *moveServer {
	t.Helper()
	ms := &moveServer{}
	ms.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ms.methods = append(ms.methods, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("ShowMesh-API-Version", "1")
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			ms.putBody, ms.ifMatch = string(b), r.Header.Get("If-Match")
			_, _ = fmt.Fprint(w, movePutBody)
			return
		}
		_, _ = fmt.Fprint(w, previewBody)
	}))
	t.Cleanup(ms.Close)
	return ms
}

func TestCmdPlaylistMovePreviewWritesNothing(t *testing.T) {
	ms := newMoveServer(t, movePreviewBody)
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"move-to-newer-fpp", "--server", ms.URL, "quick"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if len(ms.methods) != 1 || ms.methods[0] != "GET /api/v1/config/show.playlist/quick/definition-move-preview" {
		t.Fatalf("requests = %v", ms.methods)
	}
	out := stdout.String()
	for _, want := range []string{"moved", "Moved from position 0 to position 1.", "dropped", "no entry at position 5", "(no sequence name saved)", "Opener.fseq", "Nothing was changed"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
}

func TestCmdPlaylistMoveConfirmSendsProposedWithIfMatch(t *testing.T) {
	ms := newMoveServer(t, movePreviewBody)
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"move-to-newer-fpp", "--confirm", "--server", ms.URL, "quick"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	if len(ms.methods) != 2 || ms.methods[1] != "PUT /api/v1/config/show.playlist/quick" {
		t.Fatalf("requests = %v", ms.methods)
	}
	if ms.ifMatch != `"4"` {
		t.Errorf("If-Match = %q, want the previewed revision", ms.ifMatch)
	}
	if !strings.Contains(ms.putBody, `"playlistHash":"bb"`) || !strings.Contains(ms.putBody, `"position":1`) {
		t.Errorf("PUT body is not the proposed payload: %s", ms.putBody)
	}
	if !strings.Contains(stdout.String(), "Saved.") {
		t.Errorf("stdout = %s", stdout.String())
	}
}

func TestCmdPlaylistMoveNothingNewerWritesNothingEvenWithConfirm(t *testing.T) {
	body := `{"serverTime":"2026-08-16T21:00:00Z","playlistId":"quick","revision":4,"newerAvailable":false,"canConfirm":false,
		"summary":"This playlist already follows the newest copy of FPP's playlist. There is nothing to move.",
		"current":{"hash":"aa","receivedAt":"2026-08-16T19:00:00Z","entryCount":2},"newest":null,"entries":[],"newEntries":[],"proposed":null}`
	ms := newMoveServer(t, body)
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"move-to-newer-fpp", "--confirm", "--server", ms.URL, "quick"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK || len(ms.methods) != 1 {
		t.Fatalf("exit = %d, requests = %v", code, ms.methods)
	}
	if !strings.Contains(stdout.String(), "nothing to move") {
		t.Errorf("stdout = %s", stdout.String())
	}
}

func TestCmdPlaylistMoveConfirmRefusedWhenEveryCueWouldBeDropped(t *testing.T) {
	body := strings.Replace(strings.Replace(movePreviewBody, `"canConfirm":true`, `"canConfirm":false`, 1), `"proposed":{`, `"proposed":null,"x":{`, 1)
	ms := newMoveServer(t, body)
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"move-to-newer-fpp", "--confirm", "--server", ms.URL, "quick"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitAPIError || len(ms.methods) != 1 {
		t.Fatalf("exit = %d, requests = %v", code, ms.methods)
	}
	if !strings.Contains(stdout.String(), "CHECK") {
		t.Errorf("a flagged row has no marker:\n%s", stdout.String())
	}
}

func TestCmdPlaylistMoveConfirmWithJSONPrintsThePreviewWhenNothingIsSaved(t *testing.T) {
	none := `{"serverTime":"2026-08-16T21:00:00Z","playlistId":"quick","revision":4,"newerAvailable":false,"canConfirm":false,"summary":"x",
		"current":null,"newest":null,"entries":[],"newEntries":[],"proposed":null}`
	for name, body := range map[string]string{"nothing newer": none, "cannot confirm": strings.Replace(movePreviewBody, `"canConfirm":true`, `"canConfirm":false`, 1)} {
		ms := newMoveServer(t, body)
		var stdout, stderr bytes.Buffer
		cmdPlaylist([]string{"move-to-newer-fpp", "--confirm", "--output", "json", "--server", ms.URL, "quick"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
		if len(ms.methods) != 1 || !strings.Contains(stdout.String(), `"newerAvailable"`) {
			t.Errorf("%s: requests = %v, stdout = %q", name, ms.methods, stdout.String())
		}
	}
}

func TestCmdPlaylistMoveJSONPassesTheServerAnswerThrough(t *testing.T) {
	ms := newMoveServer(t, movePreviewBody)
	var stdout, stderr bytes.Buffer
	code := cmdPlaylist([]string{"move-to-newer-fpp", "--server", ms.URL, "--output", "json", "quick"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z")))
	if code != exitOK {
		t.Fatalf("exit = %d; stderr=%s", code, stderr.String())
	}
	for _, want := range []string{`"newerAvailable": true`, `"revision": 4`, `"outcome": "moved"`, `"proposed"`} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
}

func TestCmdPlaylistMoveRequiresOnePlaylistID(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := cmdPlaylist([]string{"move-to-newer-fpp"}, &stdout, &stderr, fixedClock(mustParse(t, "2026-08-16T21:00:00Z"))); code != exitUsage {
		t.Fatalf("exit = %d", code)
	}
}
