package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file proves this seam's own fix: dispatchOneCueActivation must
// confirm from the NODE'S OWN result, never from a bare successful
// publish (ADR-003), reusing audiodispatch_test.go's real-store-plus-
// fakeAudioPublisher pattern (newAudioDispatchTestSetup) since
// dispatchOneCueActivation now awaits a result the identical way
// executeAudioSessionDispatch does.

// declareNodeForTest gives nodeID a row in the "nodes" table itself —
// mirrors internal/coordinator/cueactivate's own declareNode test helper
// (decide_test.go), independently reproduced here per this codebase's
// standing convention for test fixtures that live in different packages.
func declareNodeForTest(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	if err := st.UpsertHello(context.Background(), nodeID, store.HelloRecord{Label: nodeID}); err != nil {
		t.Fatalf("declare node %q: %v", nodeID, err)
	}
}

// putFreshReportForTest marks nodeID's asset inventory report complete
// and dated now, with nothing expected and nothing held — mirrors
// cueactivate's own putFreshReport, the simplest path to a
// [assetsync.ManifestReady] node for a Cue that names no real asset.
func putFreshReportForTest(t *testing.T, st *store.Store, nodeID string, now time.Time) {
	t.Helper()
	if err := st.ReplaceNodeAssetInventory(context.Background(), nodeID, nil, store.NodeAssetReportRecord{
		NodeID: nodeID, ReportedAt: now, Complete: true,
	}); err != nil {
		t.Fatalf("replace node asset inventory for %q: %v", nodeID, err)
	}
}

// putAudioOnlyCueForTest writes a show.cue declaring only an audio output,
// naming an asset nothing ever uploads — so a test node can be resolved
// "asset ready" (assetsync.ManifestReady) without uploading anything,
// mirroring cueactivate's own putLTCCue one field narrower (no LTC).
func putAudioOnlyCueForTest(t *testing.T, st *store.Store, id, showID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: id,
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "asset-" + id}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowCueConfigKind, id, payload)
}

func putPlaylistForTest(t *testing.T, st *store.Store, id string, p config.ShowPlaylistPayload) {
	t.Helper()
	payload, err := config.EncodeShowPlaylistPayload(p)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfigForTest(t, st, config.ShowPlaylistConfigKind, id, payload)
}

// hash64ForTest mirrors cueactivate's own hash64: a syntactically valid
// 64-lowercase-hex hash from a short label.
func hash64ForTest(label string) string {
	h := strings.Repeat("0", 64-len(label)) + label
	return h[len(h)-64:]
}

// cueActivationDispatchTestFixture builds a fully authorized cue.activate
// scenario: an active show, one audio-only Cue (asset-ready with nothing
// uploaded), one fpp-runner Playlist entry naming it, and a declared,
// asset-ready node — exactly what [cueactivate.Authorize] requires to
// pass, so dispatchOneCueActivation reaches its own publish-and-await
// path rather than refusing before ever getting there.
func cueActivationDispatchTestFixture(t *testing.T, setup *audioDispatchTestSetup, now time.Time) (nodeID string, act cueactivation.Activation) {
	t.Helper()
	const showID, cueID, playlistID, instanceUUID = "halloween-2026", "cue-1", "playlist-1", "inst-1"
	nodeID = "audio-01"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putAudioNodeForTest(t, setup.st, nodeID)
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)
	putAudioOnlyCueForTest(t, setup.st, cueID, showID)
	putPlaylistForTest(t, setup.st, playlistID, config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	})
	putActiveShowForTest(t, setup.st, showID)

	act = cueactivation.Activation{
		Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-test-1",
		Show: showID, Generation: 1, CatalogRevision: "", // filled in below
		Playlist: playlistID, PlaylistRevision: 1, EntryID: "entry-1",
		CueID: cueID, CueRevision: 1, PositionMS: 0,
		EvidenceAt: now,
	}
	return nodeID, act
}

// cueActivationNodeResultPayload builds the exact evidence shape
// internal/agent/cueactivationops.go's activate produces, for a fake
// node's own result.
func cueActivationNodeResultPayload(confirmed bool, nodeOutcome string) mqttproto.ResultPayload {
	outcome := mqttproto.OutcomeUnconfirmed
	reason := "not authorized"
	if confirmed {
		outcome = mqttproto.OutcomeConfirmed
		reason = ""
	}
	return mqttproto.ResultPayload{
		Outcome: outcome, Reason: reason,
		Evidence: &mqttproto.ResultEvidence{
			Signal: "node.cue_activation.outcome",
			Value:  map[string]any{"activationId": "cueact-test-1", "cueId": "cue-1", "cueRevision": int64(1), "outcome": nodeOutcome},
		},
	}
}

// TestDispatchOneCueActivationConfirmedFromNodeResult proves confirmation
// comes from the node's own result: a bare successful publish is not, by
// itself, enough — the fake publisher's canned result must actually say
// "authorized" for Confirmed to become true.
func TestDispatchOneCueActivationConfirmedFromNodeResult(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)

	// CatalogRevision must be the coordinator's OWN resolution to pass
	// Authorize's stale-catalog check — resolve it the same way
	// cueactivate.Decide would, via a real Authorize dry run is overkill
	// here; instead read it back off the resolved catalog directly.
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)

	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	outcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	if outcome.Err != nil {
		t.Fatalf("dispatchOneCueActivation: %v", outcome.Err)
	}
	if outcome.AuthorizeOutcome != "" {
		t.Fatalf("AuthorizeOutcome = %q, want empty (this fixture is authorized)", outcome.AuthorizeOutcome)
	}
	if !outcome.Dispatched {
		t.Fatalf("Dispatched = false, want true")
	}
	if !outcome.Confirmed {
		t.Fatalf("Confirmed = false, want true (the node's own result reported authorized)")
	}
	if outcome.NodeOutcome != cueActivationNodeOutcomeAuthorized {
		t.Fatalf("NodeOutcome = %q, want %q", outcome.NodeOutcome, cueActivationNodeOutcomeAuthorized)
	}
}

// TestDispatchOneCueActivationSetsWireDeadline proves
// dispatchOneCueActivation populates the dispatched command's
// CmdPayload.Deadline, anchored to the dispatch clock by exactly
// cueActivationWireDeadline, not merely non-nil.
func TestDispatchOneCueActivationSetsWireDeadline(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)

	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	outcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	if outcome.Err != nil {
		t.Fatalf("dispatchOneCueActivation: %v", outcome.Err)
	}
	if len(setup.pub.dispatched) != 1 {
		t.Fatalf("dispatched count = %d, want 1", len(setup.pub.dispatched))
	}
	got := setup.pub.dispatched[0].Deadline
	if got == nil {
		t.Fatalf("Deadline = nil, want set")
	}
	want := now.Add(cueActivationWireDeadline)
	if !got.Equal(want) {
		t.Fatalf("Deadline = %v, want %v (now + cueActivationWireDeadline)", got, want)
	}
}

// TestDispatchOneCueActivationRecordsNodeRefusalNotDispatchedSuccess
// proves this seam's own defect fix directly: a node that refuses (e.g.
// cross-show, or a stale catalog it independently detected) must NOT be
// recorded as a successful dispatch — Confirmed stays false and
// NodeOutcome carries the node's own refusal reason, evidence a bare
// "the publish succeeded" outcome could never distinguish from a real
// acceptance.
func TestDispatchOneCueActivationRecordsNodeRefusalNotDispatchedSuccess(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)

	// The fake publisher replies with the PUBLISH succeeding (Publish
	// itself never errors) but the node's own result reporting a refusal
	// — exactly the case a bare "publish succeeded" dispatch would have
	// mis-recorded as accepted before this fix.
	setup.pub.result = cueActivationNodeResultPayload(false, "stale-catalog")

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	outcome := h.dispatchOneCueActivation(context.Background(), now, nodeID, act, issuer, nil)
	if outcome.Err != nil {
		t.Fatalf("dispatchOneCueActivation: %v", outcome.Err)
	}
	if !outcome.Dispatched {
		t.Fatalf("Dispatched = false, want true (the publish itself succeeded)")
	}
	if outcome.Confirmed {
		t.Fatalf("Confirmed = true, want false: the node's own result refused this activation (stale-catalog)")
	}
	if outcome.NodeOutcome != "stale-catalog" {
		t.Fatalf("NodeOutcome = %q, want %q (the node's own reported refusal, not silently dropped)", outcome.NodeOutcome, "stale-catalog")
	}

	// The refusal must also be durably recorded on the command row, not
	// only returned in memory — an operator reading the command history
	// later must see the same refusal.
	cmd, err := setup.st.GetCommandByIdempotencyKey(context.Background(), act.ActivationID)
	if err != nil {
		t.Fatalf("get command by idempotency key: %v", err)
	}
	if cmd.OutcomeState == mqttproto.OutcomeConfirmed {
		t.Fatalf("persisted command OutcomeState = %q, must not read as confirmed", cmd.OutcomeState)
	}
}

// putAuthorizedAudioAssetForTest creates a real asset record for the
// sequence putAudioOnlyCueForTest's cueID names ("asset-"+cueID), targets
// it at nodeID, and marks nodeID's own reported inventory as holding it —
// turning cueActivationDispatchTestFixture's Cue, which otherwise names a
// sequence nothing has ever uploaded (see putAudioOnlyCueForTest's own doc
// comment), into one [cueactivate.Authorize] actually finds present. Before
// this coordinator started refusing a never-uploaded sequence, that vacuous
// "nothing uploaded" state passed Authorize on its own; a test
// that needs a genuinely authorized activation must now build one for
// real. Callers must call this BEFORE resolving CatalogRevision — the
// asset's own hash is part of what the catalog revision covers
// (cuecatalog.RevisionInput's own doc comment).
func putAuthorizedAudioAssetForTest(t *testing.T, st *store.Store, showID, cueID, nodeID string, now time.Time) {
	t.Helper()
	contentHash := "sha256:authorized-" + cueID
	filename := "Authorized-" + cueID + ".wav"
	if _, _, err := st.CreateAsset(context.Background(), store.AssetRecord{
		ID: contentHash + "-node-" + nodeID, ShowID: showID, SequenceID: "asset-" + cueID,
		TargetKind: store.AssetTargetKindNode, TargetID: nodeID, MediaType: "audio",
		ContentHash: contentHash, RuntimeFilename: filename,
		SizeBytes: 1024, Backend: "volume", StorageKey: contentHash,
	}); err != nil {
		t.Fatalf("create authorized asset for %q: %v", cueID, err)
	}
	if err := st.ReplaceNodeAssetInventory(context.Background(), nodeID,
		[]store.NodeAssetInventoryRecord{{NodeID: nodeID, ContentHash: contentHash, RuntimeFilename: filename, SizeBytes: 1024, VerifiedAt: now}},
		store.NodeAssetReportRecord{NodeID: nodeID, ReportedAt: now, Complete: true},
	); err != nil {
		t.Fatalf("replace node asset inventory for %q: %v", nodeID, err)
	}
}

// cuePrepareAheadTestFixture builds a two-entry Playlist (cue-1 then
// cue-2, both audio-only, both with a real node-inventoried asset) and an
// Activation for cue-1 — the minimum dispatchPrepareAheadAudio needs to
// find cue-2 as the next entry and stage its audio. Independent of
// cueActivationDispatchTestFixture (rather than extending it) because
// putConfigForTest always writes config revision 1: a second call against
// the SAME playlist id to add cue-2's entry would collide with the first.
func cuePrepareAheadTestFixture(t *testing.T, setup *audioDispatchTestSetup, now time.Time) (nodeID string, act cueactivation.Activation) {
	t.Helper()
	const showID, cue1ID, cue2ID, playlistID, instanceUUID = "halloween-2026", "cue-1", "cue-2", "playlist-1", "inst-1"
	nodeID = "audio-01"

	putShowForTest(t, setup.st, showID, "Halloween 2026")
	putAudioNodeForTest(t, setup.st, nodeID)
	declareNodeForTest(t, setup.st, nodeID)
	putFreshReportForTest(t, setup.st, nodeID, now)
	putAudioOnlyCueForTest(t, setup.st, cue1ID, showID)
	putAudioOnlyCueForTest(t, setup.st, cue2ID, showID)
	putPlaylistForTest(t, setup.st, playlistID, config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: "Main", PlaylistHash: hash64ForTest("a1")},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-1", Cue: cue1ID, FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
			{ID: "entry-2", Cue: cue2ID, FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1}},
		},
	})
	putActiveShowForTest(t, setup.st, showID)
	putAuthorizedAudioAssetForTest(t, setup.st, showID, cue1ID, nodeID, now)
	putAuthorizedAudioAssetForTest(t, setup.st, showID, cue2ID, nodeID, now)

	act = cueactivation.Activation{
		Runner: "fpp", RunnerInstance: instanceUUID, ActivationID: "cueact-prepare-ahead-1",
		Show: showID, Generation: 1, CatalogRevision: resolvedCatalogRevisionForTest(t, setup.st, showID, nodeID),
		Playlist: playlistID, PlaylistRevision: 1, EntryID: "entry-1",
		CueID: cue1ID, CueRevision: 1, PositionMS: 0,
		EvidenceAt: now,
	}
	return nodeID, act
}

// dispatchedActionsToSession filters setup's recorded dispatches down to
// those against sessionID, in dispatch order.
func dispatchedActionsToSession(setup *audioDispatchTestSetup, sessionID string) []dispatchedAudioCommand {
	setup.pub.mu.Lock()
	defer setup.pub.mu.Unlock()
	var out []dispatchedAudioCommand
	for _, d := range setup.pub.dispatched {
		if s, _ := d.Params["sessionId"].(string); s == sessionID {
			out = append(out, d)
		}
	}
	return out
}

// TestDispatchPrepareAheadAudioStagesNextCue proves the coordinator's own
// half of the video-leads-audio fix: at cue-1's activation, it must stage
// cue-2's own audio (the ordered Playlist's next entry) under
// [cueactivation.PrepareStagingSessionID] via audio.session.apply followed
// by audio.session.prepare — and never against the playing show session
// id, which staging on would tear down whatever is actually playing (see
// [audio.Manager.Promote]'s own doc comment for why a separate id exists
// at all).
func TestDispatchPrepareAheadAudioStagesNextCue(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cuePrepareAheadTestFixture(t, setup, now)
	setup.pub.result = cueActivationNodeResultPayload(true, cueActivationNodeOutcomeAuthorized)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.dispatchPrepareAheadAudio(context.Background(), now, nodeID, act, issuer)

	staged := dispatchedActionsToSession(setup, cueactivation.PrepareStagingSessionID)
	if len(staged) != 2 {
		t.Fatalf("dispatched %d commands against the staging session, want 2 (apply, prepare); got %+v", len(staged), staged)
	}
	if staged[0].Action != "audio.session.apply" {
		t.Fatalf("first staging dispatch action = %q, want audio.session.apply", staged[0].Action)
	}
	media, ok := staged[0].Params["media"].(map[string]any)
	if !ok {
		t.Fatalf("audio.session.apply params carried no media object: %+v", staged[0].Params)
	}
	if got := media["assetId"]; got != "asset-cue-2" {
		t.Fatalf("staged media assetId = %v, want %q (cue-2's own, never cue-1's)", got, "asset-cue-2")
	}
	if got := media["contentHash"]; got != "sha256:authorized-cue-2" {
		t.Fatalf("staged media contentHash = %v, want cue-2's own authorized asset hash", got)
	}
	if staged[1].Action != "audio.session.prepare" {
		t.Fatalf("second staging dispatch action = %q, want audio.session.prepare", staged[1].Action)
	}

	// The staging id must never equal the playing show session id: staging
	// there would tear down whatever cue-1 itself is actually playing.
	if cueactivation.PrepareStagingSessionID == cueactivation.AudioSessionID {
		t.Fatalf("PrepareStagingSessionID == AudioSessionID (%q); a prepare-ahead dispatch would target the playing show session", cueactivation.AudioSessionID)
	}
	for _, d := range setup.pub.dispatched {
		if s, _ := d.Params["sessionId"].(string); s == cueactivation.AudioSessionID {
			t.Fatalf("dispatchPrepareAheadAudio published against the playing show session id %q: %+v", cueactivation.AudioSessionID, d)
		}
	}
}

// TestDispatchPrepareAheadAudioSkipsOnLastPlaylistEntry proves
// nextPlaylistEntryCueID's own "nothing to stage" case reaches
// dispatchPrepareAheadAudio correctly: cue-1's own entry is the
// Playlist's last, so there is no next Cue to guess at, and nothing must
// be dispatched.
func TestDispatchPrepareAheadAudioSkipsOnLastPlaylistEntry(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cueActivationDispatchTestFixture(t, setup, now)
	putAuthorizedAudioAssetForTest(t, setup.st, act.Show, act.CueID, nodeID, now)
	act.CatalogRevision = resolvedCatalogRevisionForTest(t, setup.st, act.Show, nodeID)

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.dispatchPrepareAheadAudio(context.Background(), now, nodeID, act, issuer)

	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (entry-1 is this Playlist's own last entry; there is nothing to prepare ahead)", setup.pub.count())
	}
}

// TestDispatchPrepareAheadAudioSkipsWhenNextCueHasNoAudioOutput proves
// dispatchPrepareAheadAudio never stages a Cue that declares no audio
// output on this node — nothing for Promote to ever move, and a wasted
// Apply/Prepare round trip this seam's own doc comment says to avoid.
func TestDispatchPrepareAheadAudioSkipsWhenNextCueHasNoAudioOutput(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cuePrepareAheadTestFixture(t, setup, now)

	// Overwrite cue-2 as render-only: this creates config revision 2 on
	// the SAME cue-2 object, which putConfigForTest cannot do (it always
	// writes revision 1) — so cue-2 is rebuilt as a fresh object instead,
	// and the Playlist's own entry-2 is repointed at it.
	putRenderOnlyCueForTest(t, setup.st, "cue-2-render-only", act.Show)
	putPlaylistForTest(t, setup.st, "playlist-2", config.ShowPlaylistPayload{
		Show: act.Show, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: act.RunnerInstance, PlaylistName: "Main2", PlaylistHash: hash64ForTest("a2")},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-1", Cue: act.CueID, FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
			{ID: "entry-2", Cue: "cue-2-render-only", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1}},
		},
	})
	act.Playlist = "playlist-2"

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.dispatchPrepareAheadAudio(context.Background(), now, nodeID, act, issuer)

	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (cue-2-render-only declares no audio output; nothing to stage)", setup.pub.count())
	}
}

// TestDispatchPrepareAheadAudioSkipsWhenEntryIDEmpty proves a directly-
// activated announcement or a safeCue mismatch fallback (neither of which
// advances through an ordered Playlist, so act.EntryID is always empty
// for both — cueactivate/decide.go's own resolveActivationsForCue) never
// triggers a prepare-ahead dispatch.
func TestDispatchPrepareAheadAudioSkipsWhenEntryIDEmpty(t *testing.T) {
	now := testNow
	setup := newAudioDispatchTestSetup(t, fixedClock(now))
	nodeID, act := cuePrepareAheadTestFixture(t, setup, now)
	act.EntryID = ""

	deps := setup.deps()
	deps.AssetManifests = setup.st
	h := &handlers{deps: deps.withDefaults(), clock: fixedClock(now), logger: testLogger()}
	issuer := cueActivationIssuer{PrincipalID: "system:cue-activation-loop:test"}

	h.dispatchPrepareAheadAudio(context.Background(), now, nodeID, act, issuer)

	if setup.pub.count() != 0 {
		t.Fatalf("publish count = %d, want 0 (an empty EntryID names no Playlist position to advance from)", setup.pub.count())
	}
}

// resolvedCatalogRevisionForTest resolves showID's Cue catalog for nodeID
// exactly the way [cueactivate.Authorize] does (it calls the identical two
// functions), so a test can build an Activation whose CatalogRevision
// passes the stale-catalog check without duplicating that logic.
func resolvedCatalogRevisionForTest(t *testing.T, st *store.Store, showID, nodeID string) string {
	t.Helper()
	ctx := context.Background()
	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	if !active.Configured || active.ShowID != showID {
		t.Fatalf("resolve active show: Configured=%v ShowID=%q, want Configured=true ShowID=%q", active.Configured, active.ShowID, showID)
	}
	catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, nodeID)
	if err != nil {
		t.Fatalf("resolve cue catalog: %v", err)
	}
	return catalog.Revision
}
