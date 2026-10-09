package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

const (
	moveInstance = "11111111-1111-1111-1111-111111111111"
	moveListName = "halloween-quick-check"
	moveOldHash  = "1111111111111111111111111111111111111111111111111111111111111111"
	moveNewHash  = "2222222222222222222222222222222222222222222222222222222222222222"
)

type movePreviewSetup struct {
	api   *API
	st    *store.Store
	auth  map[string]string
	token string
}

func newMovePreviewSetup(t *testing.T) movePreviewSetup {
	t.Helper()
	svc, st, _ := newTestIdentityServiceWithStore(t, fixedClock(testNow))
	admin := mustCreatePrincipal(t, svc, "admin-1", identity.RoleAdmin)
	token := mustIssueToken(t, svc, admin.ID)
	deps := showObjectsTestDeps(svc, st)
	deps.FPPPlaylistDefinitions = st
	api := New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})
	auth := map[string]string{"Authorization": "Bearer " + token}
	mustPutShow(t, api, token, "halloween-2026", `{"name":"Halloween 2026"}`)
	for _, id := range []string{"wake-up", "kpop-audio"} {
		mustPutCue(t, api, token, id, validCueBody)
	}
	return movePreviewSetup{api: api, st: st, auth: auth, token: token}
}

func (s movePreviewSetup) putDefinition(t *testing.T, hash string, capturedAt time.Time, body string) {
	t.Helper()
	if _, err := s.st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: moveInstance, PlaylistHash: hash, PlaylistName: moveListName,
		DefinitionJSON: body, CapturedAt: capturedAt, ReceivedAt: capturedAt,
	}); err != nil {
		t.Fatalf("put definition: %v", err)
	}
}

const movePlaylistTemplate = `{
	"show": "halloween-2026", "name": "Quick check", "runner": "fpp", "mismatchPolicy": "hold",
	"fpp": {"instanceUuid": "` + moveInstance + `", "playlistName": "` + moveListName + `", "playlistHash": "` + moveOldHash + `"},
	"entries": [
		{"id": "a823", "cue": "wake-up", "fpp": {"section": "mainPlaylist", "position": 0, "expectedSequenceFilename": "Wake Up.fseq"}},
		{"id": "c080", "cue": "kpop-audio", "fpp": {"section": "mainPlaylist", "position": 1, "expectedSequenceFilename": "kpop.fseq"}}
	]
}`

const (
	moveOldDefinition = `{"mainPlaylist":[{"type":"sequence","sequenceName":"Wake Up.fseq"},{"type":"sequence","sequenceName":"kpop.fseq"}]}`
	moveNewDefinition = `{"mainPlaylist":[{"type":"sequence","sequenceName":"Opener.fseq"},{"type":"sequence","sequenceName":"Wake Up.fseq"},{"type":"sequence","sequenceName":"kpop.fseq"}]}`
)

func (s movePreviewSetup) putPlaylist(t *testing.T, id, body string) {
	t.Helper()
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/"+id, body, s.auth)
	resp, respBody := doRawRequest(t, s.api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist/%s: status = %d; body: %s", id, resp.StatusCode, respBody)
	}
}

func (s movePreviewSetup) preview(t *testing.T, id string) (int, []byte) {
	t.Helper()
	resp, body := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/"+id+"/definition-move-preview", s.auth)
	return resp.StatusCode, body
}

func (s movePreviewSetup) seededWithNewerDefinition(t *testing.T) {
	t.Helper()
	s.putDefinition(t, moveOldHash, testNow.Add(-time.Hour), moveOldDefinition)
	s.putPlaylist(t, "quick", movePlaylistTemplate)
	s.putDefinition(t, moveNewHash, testNow, moveNewDefinition)
}

func TestShowPlaylistDefinitionMovePreviewReportsMovedEntriesAndTheProposedWrite(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.seededWithNewerDefinition(t)
	status, body := s.preview(t, "quick")
	if status != http.StatusOK {
		t.Fatalf("status = %d; body: %s", status, body)
	}
	var got v1.ShowPlaylistMovePreviewResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if !got.NewerAvailable || !got.CanConfirm || got.Revision != 1 || got.Newest == nil || got.Newest.Hash != moveNewHash || got.Newest.EntryCount != 3 || got.Current.Hash != moveOldHash || got.Current.EntryCount != 2 {
		t.Fatalf("unexpected preview: %s", body)
	}
	if len(got.Entries) != 2 || got.Entries[0].Outcome != "moved" || got.Entries[0].To.Position != 1 || got.Entries[1].To.Position != 2 {
		t.Fatalf("entries: %+v", got.Entries)
	}
	if got.Entries[0].Summary != "Moved from position 0 to position 1." {
		t.Fatalf("summary = %q", got.Entries[0].Summary)
	}
	if len(got.NewEntries) != 1 || got.NewEntries[0].Name != "Opener.fseq" || got.NewEntries[0].Position != 0 {
		t.Fatalf("new entries: %+v", got.NewEntries)
	}
	if got.Proposed == nil || got.Proposed.FPP.PlaylistHash != moveNewHash || len(got.Proposed.Entries) != 2 {
		t.Fatalf("proposed: %+v", got.Proposed)
	}

	// The proposed body is accepted unchanged by the existing write.
	proposedJSON, _ := json.Marshal(got.Proposed)
	hdr := map[string]string{"Authorization": "Bearer " + s.token, "If-Match": `"1"`}
	resp, putBody := doRawRequest(t, s.api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/quick", string(proposedJSON), hdr))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT of the proposed payload: status = %d; body: %s", resp.StatusCode, putBody)
	}
	// Nothing newer remains, and the answer does not point back at the older copy.
	status, body = s.preview(t, "quick")
	var after v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &after)
	if status != http.StatusOK || after.NewerAvailable || after.Newest != nil || after.Proposed != nil || after.CanConfirm || after.Revision != 2 {
		t.Fatalf("after the move: %s", body)
	}
}

func TestShowPlaylistDefinitionMovePreviewWritesNothing(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.seededWithNewerDefinition(t)
	_, before := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/quick", s.auth)
	if status, body := s.preview(t, "quick"); status != http.StatusOK {
		t.Fatalf("status = %d; body: %s", status, body)
	}
	_, after := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/quick", s.auth)
	if string(stripServerTime(before)) != string(stripServerTime(after)) {
		t.Fatalf("playlist changed:\n%s\n%s", before, after)
	}
	_, revisions := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/quick/revisions", s.auth)
	if strings.Count(string(revisions), `"revision"`) != 1 {
		t.Fatalf("a revision was written: %s", revisions)
	}
}

func stripServerTime(b []byte) []byte {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	delete(m, "serverTime")
	out, _ := json.Marshal(m)
	return out
}

func TestShowPlaylistDefinitionMovePreviewNothingNewer(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.putDefinition(t, moveOldHash, testNow, moveOldDefinition)
	s.putPlaylist(t, "quick", movePlaylistTemplate)
	// An older capture of the same FPP playlist must never be offered.
	s.putDefinition(t, moveNewHash, testNow.Add(-time.Hour), moveNewDefinition)
	status, body := s.preview(t, "quick")
	var got v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &got)
	if status != http.StatusOK || got.NewerAvailable || got.CanConfirm || got.Newest != nil || got.Proposed != nil || len(got.Entries) != 0 || got.Summary == "" {
		t.Fatalf("status = %d; body: %s", status, body)
	}
}

func TestShowPlaylistDefinitionMovePreviewEveryEntryDropped(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.putDefinition(t, moveOldHash, testNow.Add(-time.Hour), moveOldDefinition)
	s.putPlaylist(t, "quick", movePlaylistTemplate)
	s.putDefinition(t, moveNewHash, testNow, `{"mainPlaylist":[{"type":"sequence","sequenceName":"Other.fseq"}]}`)
	status, body := s.preview(t, "quick")
	var got v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &got)
	if status != http.StatusOK || !got.NewerAvailable || got.CanConfirm || got.Proposed != nil || len(got.Entries) != 2 || got.Entries[0].Outcome != "dropped" || len(got.NewEntries) != 1 {
		t.Fatalf("status = %d; body: %s", status, body)
	}
}

func TestShowPlaylistDefinitionMovePreviewRefusals(t *testing.T) {
	s := newMovePreviewSetup(t)

	status, body := s.preview(t, "missing")
	if status != http.StatusNotFound || !strings.Contains(string(body), "This playlist does not exist") {
		t.Fatalf("unknown playlist: status = %d; body: %s", status, body)
	}

	s.putPlaylist(t, "audio", `{"show":"halloween-2026","name":"Audio","runner":"showmesh-audio","entries":[{"id":"e1","cue":"wake-up"}]}`)
	status, body = s.preview(t, "audio")
	if status != http.StatusConflict || !strings.Contains(string(body), "not run by FPP") {
		t.Fatalf("not an FPP playlist: status = %d; body: %s", status, body)
	}

	s.putPlaylist(t, "quick", movePlaylistTemplate)
	status, body = s.preview(t, "quick")
	if status != http.StatusConflict || !strings.Contains(string(body), "no longer holds the copy") {
		t.Fatalf("no stored copy: status = %d; body: %s", status, body)
	}
}

func TestShowPlaylistDefinitionMovePreviewNeedsReadScope(t *testing.T) {
	s := newMovePreviewSetup(t)
	resp, _ := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/quick/definition-move-preview", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no credential: status = %d", resp.StatusCode)
	}
}

func TestNewestFPPPlaylistDefinition(t *testing.T) {
	rec := func(hash, name string, captured, received int) store.FPPPlaylistDefinitionRecord {
		return store.FPPPlaylistDefinitionRecord{
			InstanceUUID: "i", PlaylistName: name, PlaylistHash: hash,
			CapturedAt: testNow.Add(time.Duration(captured) * time.Minute), ReceivedAt: testNow.Add(time.Duration(received) * time.Minute),
		}
	}
	recs := []store.FPPPlaylistDefinitionRecord{rec("a", "p", 1, 9), rec("b", "p", 5, 1), rec("c", "p", 5, 2), rec("z", "other", 99, 99)}
	got, ok := newestFPPPlaylistDefinition(recs, "i", "p")
	if !ok || got.PlaylistHash != "c" {
		t.Fatalf("newest = %+v ok=%v", got, ok)
	}
	if _, ok := newestFPPPlaylistDefinition(recs, "i", "none"); ok {
		t.Fatal("found a definition for an unknown playlist name")
	}
}
