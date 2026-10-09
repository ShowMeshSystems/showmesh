package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
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
	svc   identity.Service
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
	return movePreviewSetup{svc: svc, api: api, st: st, auth: auth, token: token}
}

func (s movePreviewSetup) putDefinition(t *testing.T, hash string, at time.Time, body string) {
	t.Helper()
	s.putDefinitionAt(t, hash, at, at, body)
}

func (s movePreviewSetup) putDefinitionAt(t *testing.T, hash string, capturedAt, receivedAt time.Time, body string) {
	t.Helper()
	if _, err := s.st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: moveInstance, PlaylistHash: hash, PlaylistName: moveListName,
		DefinitionJSON: body, CapturedAt: capturedAt, ReceivedAt: receivedAt,
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
	if status != http.StatusConflict || !strings.Contains(string(body), "holds no copy of this FPP playlist") {
		t.Fatalf("no copy held at all: status = %d; body: %s", status, body)
	}
}

func TestShowPlaylistDefinitionMovePreviewWhenTheCurrentCopyIsNotHeld(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.putPlaylist(t, "quick", movePlaylistTemplate)
	s.putDefinition(t, moveNewHash, testNow, moveNewDefinition)
	status, body := s.preview(t, "quick")
	var got v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &got)
	if status != http.StatusOK || !got.NewerAvailable || !got.CanConfirm || got.Current != nil || got.Newest == nil || !strings.Contains(got.Summary, "no longer has the copy") {
		t.Fatalf("status = %d; body: %s", status, body)
	}
	if got.Entries[0].Outcome != "moved" {
		t.Fatalf("filename entries should still match: %+v", got.Entries)
	}
}

func TestShowPlaylistDefinitionMovePreviewNewestIsDecidedByWhenShowMeshReceivedIt(t *testing.T) {
	s := newMovePreviewSetup(t)
	// The current copy was captured later by the plugin's clock but received
	// first; the other copy was received last, with an earlier capture time.
	s.putDefinitionAt(t, moveOldHash, testNow, testNow.Add(-time.Hour), moveOldDefinition)
	s.putPlaylist(t, "quick", movePlaylistTemplate)
	s.putDefinitionAt(t, moveNewHash, testNow.Add(-2*time.Hour), testNow, moveNewDefinition)
	status, body := s.preview(t, "quick")
	var got v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &got)
	if status != http.StatusOK || !got.NewerAvailable || got.Newest == nil || got.Newest.Hash != moveNewHash {
		t.Fatalf("status = %d; body: %s", status, body)
	}
}

const moveNoFilenamePlaylist = `{
	"show": "halloween-2026", "name": "Quick check", "runner": "fpp",
	"fpp": {"instanceUuid": "` + moveInstance + `", "playlistName": "` + moveListName + `", "playlistHash": "` + moveOldHash + `"},
	"entries": [
		{"id": "a", "cue": "wake-up", "fpp": {"section": "mainPlaylist", "position": 0}},
		{"id": "b", "cue": "kpop-audio", "fpp": {"section": "mainPlaylist", "position": 1}}
	]}`

func TestShowPlaylistDefinitionMovePreviewPositionMatchesNameBothSequences(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.putPlaylist(t, "quick", moveNoFilenamePlaylist)
	s.putDefinition(t, moveNewHash, testNow, moveNewDefinition)
	status, body := s.preview(t, "quick")
	var got v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &got)
	if status != http.StatusOK || !got.CanConfirm || len(got.Entries) != 2 {
		t.Fatalf("status = %d; body: %s", status, body)
	}
	a := got.Entries[0]
	if a.MatchedBy != "position" || a.Outcome != "kept" || !a.NeedsCheck || a.NewSequence != "Opener.fseq" {
		t.Fatalf("entry a = %+v", a)
	}
	if !strings.Contains(a.Summary, "which now holds Opener.fseq. The earlier sequence name is not known.") {
		t.Fatalf("summary = %q", a.Summary)
	}
}

func TestShowPlaylistDefinitionMovePreviewNoFilenameFollowsThePreviousSequence(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.putDefinition(t, moveOldHash, testNow.Add(-time.Hour), moveOldDefinition)
	s.putPlaylist(t, "quick", moveNoFilenamePlaylist)
	s.putDefinition(t, moveNewHash, testNow, moveNewDefinition)
	_, before := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/quick", s.auth)
	status, body := s.preview(t, "quick")
	var got v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &got)
	if status != http.StatusOK || !got.CanConfirm || len(got.Entries) != 2 {
		t.Fatalf("status = %d; body: %s", status, body)
	}
	a, b := got.Entries[0], got.Entries[1]
	if a.MatchedBy != "previousSequence" || a.Outcome != "moved" || a.To.Position != 1 || a.NeedsCheck || a.PreviousSequence != "Wake Up.fseq" || a.NewSequence != "Wake Up.fseq" || a.Filename != "" {
		t.Fatalf("entry a = %+v", a)
	}
	if b.MatchedBy != "previousSequence" || b.Outcome != "moved" || b.To.Position != 2 || b.NeedsCheck {
		t.Fatalf("entry b = %+v", b)
	}
	if want := "No sequence name was saved for this cue, so it follows the sequence its position held, Wake Up.fseq. Moved from position 0 to position 1."; a.Summary != want {
		t.Fatalf("summary = %q, want %q", a.Summary, want)
	}
	if len(got.NewEntries) != 1 || got.NewEntries[0].Position != 0 {
		t.Fatalf("new entries: %+v", got.NewEntries)
	}
	for _, e := range got.Proposed.Entries {
		if e.FPP.ExpectedSequenceFilename != "" {
			t.Fatalf("proposed entry %s gained a filename: %+v", e.ID, e.FPP)
		}
	}
	_, after := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/quick", s.auth)
	if string(stripServerTime(before)) != string(stripServerTime(after)) {
		t.Fatalf("the preview wrote:\n%s\n%s", before, after)
	}

	proposedJSON, _ := json.Marshal(got.Proposed)
	hdr := map[string]string{"Authorization": "Bearer " + s.token, "If-Match": `"1"`}
	resp, putBody := doRawRequest(t, s.api.Handler, newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/quick", string(proposedJSON), hdr))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT of the proposed payload: status = %d; body: %s", resp.StatusCode, putBody)
	}
}

func TestShowPlaylistDefinitionMovePreviewNoFilenameDroppedNamesTheSequence(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.putDefinition(t, moveOldHash, testNow.Add(-time.Hour), moveOldDefinition)
	s.putPlaylist(t, "quick", moveNoFilenamePlaylist)
	s.putDefinition(t, moveNewHash, testNow, `{"mainPlaylist":[{"type":"sequence","sequenceName":"Wake Up.fseq"},{"type":"sequence","sequenceName":"Renamed.fseq"}]}`)
	_, body := s.preview(t, "quick")
	var got v1.ShowPlaylistMovePreviewResponse
	_ = json.Unmarshal(body, &got)
	if len(got.Entries) != 2 || got.Entries[1].Outcome != "dropped" || got.Entries[1].To != nil || got.Entries[1].NeedsCheck {
		t.Fatalf("entries: %s", body)
	}
	if want := "No sequence name was saved for this cue. Its position held kpop.fseq, which is no longer in FPP's playlist. This cue will be removed from this playlist."; got.Entries[1].Summary != want {
		t.Fatalf("summary = %q", got.Entries[1].Summary)
	}
}

func TestShowPlaylistDefinitionMovePreviewRefusesAMoveTheWriteWouldRefuse(t *testing.T) {
	deleteConfig := func(s movePreviewSetup, kind, id string) {
		t.Helper()
		req := newJSONRequest(t, http.MethodDelete, "/api/v1/config/"+kind+"/"+id, `{"confirm":true}`, s.auth)
		if resp, body := doRawRequest(t, s.api.Handler, req); resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete %s/%s: status = %d; body: %s", kind, id, resp.StatusCode, body)
		}
	}
	check := func(t *testing.T, s movePreviewSetup) {
		t.Helper()
		status, body := s.preview(t, "quick")
		var got v1.ShowPlaylistMovePreviewResponse
		_ = json.Unmarshal(body, &got)
		if status != http.StatusOK || !got.NewerAvailable || got.CanConfirm || got.Proposed != nil || !strings.Contains(got.Summary, "no longer exists") {
			t.Fatalf("status = %d; body: %s", status, body)
		}
	}
	t.Run("a cue was deleted", func(t *testing.T) {
		s := newMovePreviewSetup(t)
		s.seededWithNewerDefinition(t)
		deleteConfig(s, "show.cue", "kpop-audio")
		check(t, s)
	})
	t.Run("the safe cue was deleted", func(t *testing.T) {
		s := newMovePreviewSetup(t)
		mustPutCue(t, s.api, s.token, "safe", validCueBody)
		s.putDefinition(t, moveOldHash, testNow.Add(-time.Hour), moveOldDefinition)
		s.putPlaylist(t, "quick", strings.Replace(movePlaylistTemplate, `"mismatchPolicy": "hold"`, `"mismatchPolicy": "safeCue", "safeCueRef": "safe"`, 1))
		s.putDefinition(t, moveNewHash, testNow, moveNewDefinition)
		deleteConfig(s, "show.cue", "safe")
		check(t, s)
	})
}

func TestShowPlaylistDefinitionMovePreviewReadScopes(t *testing.T) {
	s := newMovePreviewSetup(t)
	s.seededWithNewerDefinition(t)
	svc := s.svc
	viewer := mustCreatePrincipal(t, svc, "viewer-1", identity.RoleViewer)
	operator := mustCreatePrincipal(t, svc, "operator-1", identity.RoleOperator)
	get := func(token string) int {
		var hdr map[string]string
		if token != "" {
			hdr = map[string]string{"Authorization": "Bearer " + token}
		}
		resp, _ := doRequest(t, s.api.Handler, "GET", "/api/v1/config/show.playlist/quick/definition-move-preview", hdr)
		return resp.StatusCode
	}
	if got := get(""); got != http.StatusUnauthorized {
		t.Errorf("no credential: status = %d, want 401", got)
	}
	if got := get(mustIssueToken(t, svc, viewer.ID)); got != http.StatusForbidden {
		t.Errorf("principal with neither scope: status = %d, want 403", got)
	}
	if got := get(mustIssueToken(t, svc, operator.ID)); got != http.StatusOK {
		t.Errorf("principal with only show:macro:run: status = %d, want 200", got)
	}
}

func TestNewestFPPPlaylistDefinition(t *testing.T) {
	rec := func(hash, name string, captured, received int) store.FPPPlaylistDefinitionRecord {
		return store.FPPPlaylistDefinitionRecord{
			InstanceUUID: "i", PlaylistName: name, PlaylistHash: hash,
			CapturedAt: testNow.Add(time.Duration(captured) * time.Minute), ReceivedAt: testNow.Add(time.Duration(received) * time.Minute),
		}
	}
	// "late" was captured last by the plugin's clock but received first.
	recs := []store.FPPPlaylistDefinitionRecord{rec("late", "p", 99, 1), rec("b", "p", 5, 5), rec("c", "p", 6, 5), rec("z", "other", 99, 99)}
	got, ok := newestFPPPlaylistDefinition(recs, "i", "p")
	if !ok || got.PlaylistHash != "c" {
		t.Fatalf("newest = %+v ok=%v, want the last received, ties to the later capture", got, ok)
	}
	if _, ok := newestFPPPlaylistDefinition(recs, "i", "none"); ok {
		t.Fatal("found a definition for an unknown playlist name")
	}
}

func TestMovePreviewSentencesNameSectionsInPlainWords(t *testing.T) {
	moved := mapPlaylistMoveEntry(config.PlaylistMoveEntry{
		Outcome: config.PlaylistMoveMoved, MatchedBy: config.PlaylistMoveByFilename,
		From: config.PlaylistMoveSlot{Section: "leadIn", Position: 0}, To: &config.PlaylistMoveSlot{Section: "mainPlaylist", Position: 1},
	})
	if moved.Summary != "Moved from lead-in position 0 to main playlist position 1." {
		t.Errorf("summary = %q", moved.Summary)
	}
	empty := mapPlaylistMoveEntry(config.PlaylistMoveEntry{
		Outcome: config.PlaylistMoveMoved, MatchedBy: config.PlaylistMoveByFilename,
		From: config.PlaylistMoveSlot{Section: "", Position: 0}, To: &config.PlaylistMoveSlot{Section: "mainPlaylist", Position: 0},
	})
	if strings.Contains(empty.Summary, "  ") || !strings.HasPrefix(empty.Summary, "Moved from position 0 to main playlist position 0") {
		t.Errorf("summary = %q", empty.Summary)
	}
}
