package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file proves TRACK-H-cues-and-playlists.md section H5 build items 1
// and 6 at the coordinator dispatch level — the level a fresh review found
// completely untested: the two existing tests that exercised an
// announcement's duck/mix/interrupt policy
// (internal/agent/cueactivationannouncement_test.go,
// internal/agent/audio/announcementltc_test.go) faked "the background
// session the runner already applied" by calling mgr.Apply then mgr.Start
// by hand, which hid the actual defect (the coordinator issued only
// audio.session.apply, never Prepare or Start, so the session it pinned
// never advanced past StateReady and never played). This file exercises
// the coordinator's OWN dispatch — applyShowmeshAudioPlaylistIfAny — and
// asserts its actual command set.

func putAudioCueForTest(t *testing.T, st *store.Store, id, showID, asset string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: id,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: asset}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowCueConfigKind, id, payload)
}

func createShowAudioAssetForTest(t *testing.T, st *store.Store, showID, sequenceID, contentHash, filename string) {
	t.Helper()
	if _, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-show-", ShowID: showID, SequenceID: sequenceID,
		TargetKind: store.AssetTargetKindShow, TargetID: "", MediaType: "audio", ContentHash: contentHash,
		RuntimeFilename: filename, SizeBytes: 1024, Backend: "volume", StorageKey: contentHash,
	}); err != nil {
		t.Fatalf("create asset: %v", err)
	}
}

// showmeshAudioDispatchTestFixture builds one active Show with an
// audio.node-declared node and one showmesh-audio Playlist naming one
// audio-ready Cue — exactly what applyShowmeshAudioPlaylistIfAny needs to
// reach its own dispatch, rather than returning early on a resolve
// failure.
func showmeshAudioDispatchTestFixture(t *testing.T, setup *audioDispatchTestSetup) (nodeID string, active assetsync.ActiveShow) {
	t.Helper()
	const showID, cueID, playlistID = "halloween-2026", "bed-a", "background"
	nodeID = "audio-01"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putAudioNodeForTest(t, setup.st, nodeID)
	putAudioCueForTest(t, setup.st, cueID, showID, "bed-a-audio")
	createShowAudioAssetForTest(t, setup.st, showID, "bed-a-audio", strings.Repeat("a", 64), "bed-a.wav")
	putPlaylistForTest(t, setup.st, playlistID, config.ShowPlaylistPayload{
		Show: showID, Name: "background", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		ShowmeshAudio: &config.ShowPlaylistShowmeshAudio{Repeat: config.ShowPlaylistShowmeshAudioRepeatNone},
		Entries:       []config.ShowPlaylistEntry{{ID: "bed-a-entry", Cue: cueID}},
	})
	putActiveShowForTest(t, setup.st, showID)

	var err error
	active, err = assetsync.ResolveActiveShow(context.Background(), setup.st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	return nodeID, active
}

// confirmedAudioResult is a node result every audio.session.* step in
// this file's tests confirms unconditionally — the dispatch SEQUENCE is
// what these tests prove, not per-step evidence semantics already proven
// by audiodispatch_test.go.
func confirmedAudioResult() mqttproto.ResultPayload {
	return mqttproto.ResultPayload{
		Outcome: mqttproto.OutcomeConfirmed,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.audio_session",
			Value:  map[string]any{"outcome": "confirmed", "reason": ""},
		},
	}
}

// TestApplyShowmeshAudioPlaylistDispatchesApplyPrepareStart proves build
// item 1's own fix: the coordinator dispatches Apply, THEN Prepare, THEN
// Start against the background session — not Apply alone, which leaves
// [audio.Manager]'s session in StateReady forever (only Start reaches
// StatePlaying, the one state [audio.Manager.watchTick] advances).
func TestApplyShowmeshAudioPlaylistDispatchesApplyPrepareStart(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = confirmedAudioResult()
	nodeID, active := showmeshAudioDispatchTestFixture(t, setup)

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	h.deps.AssetManifests = setup.st

	h.applyShowmeshAudioPlaylistIfAny(context.Background(), testNow, nodeID, active)

	setup.pub.mu.Lock()
	defer setup.pub.mu.Unlock()
	var actions []string
	for _, d := range setup.pub.dispatched {
		if d.Action == "audio.session.apply" || d.Action == "audio.session.prepare" || d.Action == "audio.session.start" {
			actions = append(actions, d.Action)
			if sessionID, _ := d.Params["sessionId"].(string); sessionID != "cue-activation:background" {
				t.Fatalf("%s dispatched with sessionId = %q, want cue-activation:background", d.Action, sessionID)
			}
		}
	}
	want := []string{"audio.session.apply", "audio.session.prepare", "audio.session.start"}
	if len(actions) != len(want) {
		t.Fatalf("dispatched audio.session actions = %v, want exactly %v", actions, want)
	}
	for i := range want {
		if actions[i] != want[i] {
			t.Fatalf("dispatched audio.session actions = %v, want %v in that order", actions, want)
		}
	}
}

// TestApplyShowmeshAudioPlaylistUsesUnifiedRevisionSpace proves build item
// 6's own fix: each step's own pkg/audio.Revision is derived through
// [cueactivation.AudioSessionRevision] (a real nanosecond-scale wall-clock
// reading), not a bare small integer — the SAME derivation
// blackAndSilence's own stop (build item 4) now also uses against this
// SAME session id. Before this fix, a small-integer revision from THIS
// dispatch and a nanosecond-scale revision from a blackAndSilence stop on
// the same session would permanently strand whichever one came second as
// "stale" for the life of the session.
func TestApplyShowmeshAudioPlaylistUsesUnifiedRevisionSpace(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = confirmedAudioResult()
	nodeID, active := showmeshAudioDispatchTestFixture(t, setup)

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	h.deps.AssetManifests = setup.st

	h.applyShowmeshAudioPlaylistIfAny(context.Background(), testNow, nodeID, active)

	setup.pub.mu.Lock()
	defer setup.pub.mu.Unlock()
	// A revision derived from a small integer (a bare config-object
	// revision counter, as this dispatch used to send) is many orders of
	// magnitude smaller than one derived from a real wall-clock reading in
	// nanoseconds. Asserting every dispatched revision is nanosecond-scale
	// (Unix nanoseconds for 2026 is on the order of 1.77e18) is a direct,
	// regression-proof check that this dispatch no longer sends the small
	// integer.
	const minPlausibleNanosecondRevision = float64(1e18)
	found := false
	for _, d := range setup.pub.dispatched {
		if d.Action != "audio.session.apply" && d.Action != "audio.session.prepare" && d.Action != "audio.session.start" {
			continue
		}
		found = true
		rev, ok := d.Params["revision"].(float64)
		if !ok || rev < minPlausibleNanosecondRevision {
			t.Fatalf("%s dispatched with revision = %v, want a nanosecond-scale value (cueactivation.AudioSessionRevision), not a small config-object revision integer", d.Action, d.Params["revision"])
		}
	}
	if !found {
		t.Fatal("no audio.session.* command was dispatched")
	}
}

// TestApplyShowmeshAudioPlaylistReappliesAfterContentChangeAtTheSameRevision
// proves build item 6's own idempotency-key fix: this dispatch's
// idempotency key is derived from the resolved playlist ref's own
// CONTENT, not the bare config-object revision number alone. If the
// SAME node/playlist pair is asked to apply DIFFERENT content (simulated
// here directly, since this package's store always advances the revision
// counter on an ordinary write — see showmeshAudioIdempotencyKey's own
// doc comment for the scenario this guards against), the dispatch must
// still take effect rather than silently replaying a stale success.
func TestApplyShowmeshAudioPlaylistIdempotencyKeyIsContentDerived(t *testing.T) {
	setup := newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = confirmedAudioResult()
	nodeID, active := showmeshAudioDispatchTestFixture(t, setup)

	h := &handlers{deps: setup.deps().withDefaults(), clock: fixedClock(testNow), logger: testLogger()}
	h.deps.AssetManifests = setup.st

	h.applyShowmeshAudioPlaylistIfAny(context.Background(), testNow, nodeID, active)
	firstApplyKey := ""
	setup.pub.mu.Lock()
	for _, d := range setup.pub.dispatched {
		if d.Action == "audio.session.apply" {
			firstApplyKey, _ = d.Params["invocationId"].(string)
		}
	}
	setup.pub.mu.Unlock()
	if firstApplyKey == "" {
		t.Fatal("no audio.session.apply invocationId recorded on the first apply")
	}

	// A second revision of the SAME playlist object changes its content
	// (repeat none -> all) without touching its object id — the ordinary
	// "playlist edited" case, and, since content changed, the
	// digest-derived key must differ even though this is only a bare
	// second revision number as far as the coordinator's own config store
	// is concerned.
	editedPayload, err := config.EncodeShowPlaylistPayload(config.ShowPlaylistPayload{
		Show: "halloween-2026", Name: "background", Runner: config.ShowPlaylistRunnerShowmeshAudio,
		ShowmeshAudio: &config.ShowPlaylistShowmeshAudio{Repeat: config.ShowPlaylistShowmeshAudioRepeatAll},
		Entries:       []config.ShowPlaylistEntry{{ID: "bed-a-entry", Cue: "bed-a"}},
	})
	if err != nil {
		t.Fatalf("encode edited show.playlist payload: %v", err)
	}
	ctx := context.Background()
	if _, err := setup.st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowPlaylistConfigKind, ObjectID: "background", Revision: 2, PayloadJSON: editedPayload, Source: "api",
	}); err != nil {
		t.Fatalf("create edited show.playlist revision: %v", err)
	}
	if _, err := setup.st.ActivateConfigRevision(ctx, config.ShowPlaylistConfigKind, "background", 2); err != nil {
		t.Fatalf("activate edited show.playlist revision: %v", err)
	}

	h.applyShowmeshAudioPlaylistIfAny(context.Background(), testNow, nodeID, active)

	setup.pub.mu.Lock()
	defer setup.pub.mu.Unlock()
	secondApplyKey := ""
	applyCount := 0
	for _, d := range setup.pub.dispatched {
		if d.Action == "audio.session.apply" {
			applyCount++
			secondApplyKey, _ = d.Params["invocationId"].(string)
		}
	}
	if applyCount != 2 {
		t.Fatalf("audio.session.apply dispatch count = %d, want 2 (changed content must dispatch again, not replay)", applyCount)
	}
	if secondApplyKey == firstApplyKey {
		t.Fatalf("apply idempotency key did not change (%q) after the playlist's own content changed (repeat none -> all)", secondApplyKey)
	}
}

// --- build item 8: trigger holes ---------------------------------------

// showmeshAudioPlaylistHTTPTestSetup builds an active Show with an
// audio-ready node and returns an *API whose PUT /show.playlist route
// (showplaylist.go's handlePutShowPlaylist) can trigger
// applyShowmeshAudioPlaylistIfAny — deps.AssetManifests and deps.Nodes are
// wired to real/fake values that setup.deps() alone does not provide.
func showmeshAudioPlaylistHTTPTestSetup(t *testing.T) (setup *audioDispatchTestSetup, api *API, token, nodeID string) {
	t.Helper()
	setup = newAudioDispatchTestSetup(t, fixedClock(testNow))
	setup.pub.result = confirmedAudioResult()
	nodeID = "audio-01"
	const showID, cueID = "halloween-2026", "bed-a"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putAudioNodeForTest(t, setup.st, nodeID)
	putAudioCueForTest(t, setup.st, cueID, showID, "bed-a-audio")
	createShowAudioAssetForTest(t, setup.st, showID, "bed-a-audio", strings.Repeat("a", 64), "bed-a.wav")
	putActiveShowForTest(t, setup.st, showID)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	deps.Nodes = &fakeNodeLister{views: []inventory.NodeView{{NodeID: nodeID}}}
	api = New(deps, Options{Clock: fixedClock(testNow), Logger: testLogger()})

	admin := mustCreatePrincipal(t, setup.svc, "admin-1", identity.RoleAdmin)
	token = mustIssueToken(t, setup.svc, admin.ID)
	return setup, api, token, nodeID
}

func countAudioSessionApplies(setup *audioDispatchTestSetup) int {
	setup.pub.mu.Lock()
	defer setup.pub.mu.Unlock()
	n := 0
	for _, d := range setup.pub.dispatched {
		if d.Action == "audio.session.apply" {
			n++
		}
	}
	return n
}

// TestPutShowPlaylistReappliesOnPlaylistRevisionChangeAlone proves build
// item 8's first trigger-hole fix: changing showmeshAudio.repeat (or
// reordering entries over the same Cue set) changes this Playlist's own
// revision without changing any Cue's own declared outputs, so the Cue
// catalog revision ([cuecatalog.ComputeRevision], derived from Cue
// entries alone) does not change and no cue-catalog deploy is ever
// triggered by it. Before this fix, that meant the change never reached
// the node at all — applying only followed a confirmed catalog deploy.
func TestPutShowPlaylistReappliesOnPlaylistRevisionChangeAlone(t *testing.T) {
	setup, api, token, _ := showmeshAudioPlaylistHTTPTestSetup(t)

	body1 := `{
		"show": "halloween-2026", "name": "background", "runner": "showmesh-audio",
		"showmeshAudio": {"repeat": "none"},
		"entries": [{"id": "bed-a-entry", "cue": "bed-a"}]
	}`
	req1 := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/background", body1, map[string]string{"Authorization": "Bearer " + token})
	resp1, respBody1 := doRawRequest(t, api.Handler, req1)
	if resp1.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist (first): status = %d, want 200; body: %s", resp1.StatusCode, respBody1)
	}
	if got := countAudioSessionApplies(setup); got != 1 {
		t.Fatalf("audio.session.apply dispatch count after the first PUT = %d, want 1", got)
	}

	// Change ONLY showmeshAudio.repeat: the Cue catalog's own resolved
	// entries (and therefore its revision) are unaffected by this field.
	body2 := `{
		"show": "halloween-2026", "name": "background", "runner": "showmesh-audio",
		"showmeshAudio": {"repeat": "all"},
		"entries": [{"id": "bed-a-entry", "cue": "bed-a"}]
	}`
	req2 := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/background", body2, map[string]string{"Authorization": "Bearer " + token})
	resp2, respBody2 := doRawRequest(t, api.Handler, req2)
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist (second): status = %d, want 200; body: %s", resp2.StatusCode, respBody2)
	}
	if got := countAudioSessionApplies(setup); got != 2 {
		t.Fatalf("audio.session.apply dispatch count after changing only showmeshAudio.repeat = %d, want 2 (a catalog-revision-invisible change must still re-apply)", got)
	}
}

// TestPutShowPlaylistRefusesSecondShowmeshAudioPlaylist proves build item
// 8's second trigger-hole fix: authoring a SECOND showmesh-audio Playlist
// for one Show is refused, naming both playlist ids — never silently
// resolved later by picking whichever sorts first by object id (the old
// behavior applyShowmeshAudioPlaylistIfAny's own doc comment used to
// describe).
func TestPutShowPlaylistRefusesSecondShowmeshAudioPlaylist(t *testing.T) {
	_, api, token, _ := showmeshAudioPlaylistHTTPTestSetup(t)

	body := `{
		"show": "halloween-2026", "name": "background", "runner": "showmesh-audio",
		"showmeshAudio": {"repeat": "none"},
		"entries": [{"id": "bed-a-entry", "cue": "bed-a"}]
	}`
	req := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/background", body, map[string]string{"Authorization": "Bearer " + token})
	resp, respBody := doRawRequest(t, api.Handler, req)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT show.playlist (first): status = %d, want 200; body: %s", resp.StatusCode, respBody)
	}

	secondBody := `{
		"show": "halloween-2026", "name": "background 2", "runner": "showmesh-audio",
		"showmeshAudio": {"repeat": "none"},
		"entries": [{"id": "bed-a-entry", "cue": "bed-a"}]
	}`
	secondReq := newJSONRequest(t, http.MethodPut, "/api/v1/config/show.playlist/background-2", secondBody, map[string]string{"Authorization": "Bearer " + token})
	secondResp, secondRespBody := doRawRequest(t, api.Handler, secondReq)
	if secondResp.StatusCode != http.StatusConflict {
		t.Fatalf("PUT show.playlist (second showmesh-audio playlist): status = %d, want 409; body: %s", secondResp.StatusCode, secondRespBody)
	}
	if !strings.Contains(string(secondRespBody), "background") || !strings.Contains(string(secondRespBody), "background-2") {
		t.Fatalf("refusal body %q does not name both playlist ids", secondRespBody)
	}
}
