package fallbackcompile

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// TestCompileRefusesAmbiguousEntryKey covers TRACK-J-fpp-fallback.md J1's
// first named refusal. A second, independent show.playlist object bound
// to the IDENTICAL FPP instance/playlist name/playlist hash as the base
// fixture's "main" playlist, claiming the same (section, position) slot
// for a different Cue, derives the identical deterministic entry key by
// construction, an authoring mistake config's own single-playlist
// uniqueness validation cannot see, because each playlist is internally
// unambiguous; only the compiler's own cross-playlist check catches it.
func TestCompileRefusesAmbiguousEntryKey(t *testing.T) {
	f := newBaseFixture(t)
	ctx := context.Background()

	createAsset(t, f.st, f.showID, "safe", strings.Repeat("b", 64), "safe.fseq")
	putCue(t, f.st, "safe", f.showID, config.ShowCuePayload{
		Name:    "Safe",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "safe"}},
	})
	putPlaylist(t, f.st, "second", config.ShowPlaylistPayload{
		Show: f.showID, Name: "Second", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "safe", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	// Adding the "safe" Cue and its referencing playlist changed render-01's
	// resolved catalog (a new Cue is now referenced); re-acknowledge so the
	// test reaches the ambiguity check rather than a stale-catalog refusal.
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	result, err := Compile(ctx, f.st, fakeSigner{}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeAmbiguousEntryKey {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeAmbiguousEntryKey, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesCrossShowReference covers the second named refusal: a
// Cue an active-show playlist entry references does not itself belong to
// the active show. Authoring-time validation already forbids this
// through the normal API; this fixture writes directly to the store
// (bypassing that validation, exactly as every other test helper in this
// package does) to exercise the compiler's own defensive check.
func TestCompileRefusesCrossShowReference(t *testing.T) {
	f := newBaseFixture(t)
	ctx := context.Background()

	putShow(t, f.st, "christmas", "Christmas")
	putCue(t, f.st, "wrong-show-cue", "christmas", config.ShowCuePayload{
		Name:    "Wrong Show",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "wrong-show"}},
	})

	// Rebind the halloween playlist's only entry to the christmas Cue.
	putPlaylist(t, f.st, "main", config.ShowPlaylistPayload{
		Show: f.showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "wrong-show-cue", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})

	result, err := Compile(ctx, f.st, fakeSigner{}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeCrossShowReference {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeCrossShowReference, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesMissingNodeCatalogAcknowledgement covers the third
// named refusal: a node the program would name as a target has never
// acknowledged the coordinator's currently resolved catalog revision for
// it.
func TestCompileRefusesMissingNodeCatalogAcknowledgement(t *testing.T) {
	f := newBaseFixture(t)
	ctx := context.Background()

	// Undo the base fixture's own acknowledgement.
	if _, err := f.st.GetNodeCueCatalogAck(ctx, f.nodeID); err != nil {
		t.Fatalf("precondition: base fixture should have acknowledged %q: %v", f.nodeID, err)
	}
	if err := f.st.PutNodeCueCatalogAck(ctx, store.NodeCueCatalogAckRecord{
		NodeID: f.nodeID, Revision: "a-stale-revision-nothing-resolves-to", ShowID: f.showID,
		Generation: f.resolveActive(t).Generation, AcknowledgedAt: f.now,
	}); err != nil {
		t.Fatalf("overwrite ack with a stale revision: %v", err)
	}

	result, err := Compile(ctx, f.st, fakeSigner{}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeMissingCatalogAcknowledgement {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeMissingCatalogAcknowledgement, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesUnresolvableTarget covers the fourth named refusal: a
// Cue's resolved render output names a sequence with no asset uploaded,
// so there is no runtime filename for the activation to point at.
func TestCompileRefusesUnresolvableTarget(t *testing.T) {
	st := openTestStore(t)
	now := testNow()
	showID, nodeID := "halloween", "render-01"

	putShow(t, st, showID, "Halloween")
	declareNode(t, st, nodeID)
	putSurface(t, st, "garage", showID, nodeID)
	// Deliberately no createAsset call: the Cue's declared sequence has
	// nothing uploaded for it.
	putCue(t, st, "thriller", showID, config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putDefinition(t, st, testInstanceUUID, testPlaylistHash, "thriller.fseq")
	putActiveShow(t, st, showID)

	result, err := Compile(context.Background(), st, fakeSigner{}, testInstanceUUID, now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeUnresolvableTarget {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeUnresolvableTarget, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesUnsupportedOutput covers the fifth named refusal: a
// Cue reachable from this host's playlist declares an announcement
// output for a node target, which a pre-authorized offline activation
// cannot arbitrate against whatever else is playing.
func TestCompileRefusesUnsupportedOutput(t *testing.T) {
	f := newBaseFixture(t)
	putAudioNode(t, f.st, f.nodeID) // announcement output is scoped by audio.node presence

	createAsset(t, f.st, f.showID, "thriller-audio", strings.Repeat("c", 64), "thriller.mp3")
	duckGainDb := -12.0
	putCue(t, f.st, "thriller", f.showID, config.ShowCuePayload{
		Name: "Thriller",
		Outputs: config.ShowCueOutputs{
			Render:       &config.ShowCueRenderOutput{Sequence: "thriller"},
			Audio:        &config.ShowCueAudioOutput{Asset: "thriller-audio"},
			Announcement: &config.ShowCueAnnouncementOutput{Policy: config.ShowCueAnnouncementPolicyDuck, DuckGainDb: &duckGainDb},
		},
	})
	ackNodeCatalog(t, f.st, f.resolveActive(t), f.nodeID, f.now)

	result, err := Compile(context.Background(), f.st, fakeSigner{}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeUnsupportedOutput {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeUnsupportedOutput, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesMissingPlaylistDefinition covers ADR-048's own
// "never a silently smaller program" rule for a case
// OutcomeUnresolvableTarget also covers: a playlist has authored FPP
// entries, but the coordinator has never received the stored FPP
// playlist definition those entries need to derive entry keys from.
//
// The fixture needs TWO playlists: with only one, a refusal and a
// silently skipped playlist both land on OutcomeUnresolvableTarget, so
// a single-playlist fixture cannot tell them apart.
func TestCompileRefusesMissingPlaylistDefinition(t *testing.T) {
	st := openTestStore(t)
	now := testNow()
	showID, nodeID := "halloween", "render-01"
	secondPlaylistHash := strings.Repeat("2", 64)

	putShow(t, st, showID, "Halloween")
	declareNode(t, st, nodeID)
	putSurface(t, st, "garage", showID, nodeID)
	createAsset(t, st, showID, "thriller", strings.Repeat("a", 64), "thriller.fseq")
	putCue(t, st, "thriller", showID, config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putDefinition(t, st, testInstanceUUID, testPlaylistHash, "thriller.fseq")
	// "second" has an authored FPP entry too, but deliberately no
	// putDefinition call for its own hash: the coordinator has never
	// stored a definition for it.
	putPlaylist(t, st, "second", config.ShowPlaylistPayload{
		Show: showID, Name: "Second", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Second", PlaylistHash: secondPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putActiveShow(t, st, showID)

	active, err := assetsync.ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	ackNodeCatalog(t, st, active, nodeID, now)

	result, err := Compile(context.Background(), st, fakeSigner{}, testInstanceUUID, now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeUnresolvableTarget {
		t.Fatalf("Compile outcome = %q, want %q (the \"main\" playlist alone resolves fine; a program must not silently publish it while skipping \"second\", which has no stored definition); reason: %s",
			result.Outcome, OutcomeUnresolvableTarget, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesWhenNoEntryResolvesToAnyTarget covers the same
// "never a silently smaller program" rule for the other path to an empty
// program: every authored entry resolves to zero node targets (the
// referenced Cue declares no output any declared node can render or
// play). A buggy compiler could publish a validly signed, empty
// program; this test proves it refuses instead.
func TestCompileRefusesWhenNoEntryResolvesToAnyTarget(t *testing.T) {
	st := openTestStore(t)
	now := testNow()
	showID, nodeID := "halloween", "render-01"

	putShow(t, st, showID, "Halloween")
	declareNode(t, st, nodeID)
	// Deliberately no putSurface and no putAudioNode: render-01 holds no
	// surface and no audio.node, so the Cue below resolves to no output
	// for it, and no other node is declared at all.
	putCue(t, st, "thriller", showID, config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
		},
	})
	putDefinition(t, st, testInstanceUUID, testPlaylistHash, "thriller.fseq")
	putActiveShow(t, st, showID)

	result, err := Compile(context.Background(), st, fakeSigner{}, testInstanceUUID, now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeUnresolvableTarget {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeUnresolvableTarget, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesPartialUnresolvableEntry needs TWO entries: with
// one, a refusal and a silently dropped entry both land on
// OutcomeUnresolvableTarget via the all-empty guard, indistinguishably.
func TestCompileRefusesPartialUnresolvableEntry(t *testing.T) {
	st := openTestStore(t)
	now := testNow()
	showID, nodeID := "halloween", "render-01"

	putShow(t, st, showID, "Halloween")
	declareNode(t, st, nodeID)
	putSurface(t, st, "garage", showID, nodeID)
	// Deliberately no putAudioNode: render-01 has render capability only.
	createAsset(t, st, showID, "thriller", strings.Repeat("a", 64), "thriller.fseq")
	putCue(t, st, "thriller", showID, config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "thriller"}},
	})
	putCue(t, st, "spooky-audio", showID, config.ShowCuePayload{
		Name:    "Spooky Audio",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "spooky"}},
	})
	putPlaylist(t, st, "main", config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP: &config.ShowPlaylistFPPBinding{
			InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash,
		},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-0", Cue: "thriller", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
			{ID: "entry-1", Cue: "spooky-audio", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1}},
		},
	})
	if _, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: testInstanceUUID, PlaylistHash: testPlaylistHash, PlaylistName: "Main",
		DefinitionJSON: `{"mainPlaylist":[{"type":"sequence","sequenceName":"thriller.fseq"},{"type":"media","mediaName":"spooky.mp3"}]}`,
		CapturedAt:     now,
	}); err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
	putActiveShow(t, st, showID)

	active, err := assetsync.ResolveActiveShow(context.Background(), st)
	if err != nil {
		t.Fatalf("resolve active show: %v", err)
	}
	ackNodeCatalog(t, st, active, nodeID, now)

	result, err := Compile(context.Background(), st, fakeSigner{}, testInstanceUUID, now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeUnresolvableTarget {
		t.Fatalf("Compile outcome = %q, want %q (a program with a resolvable entry-0 and an unresolvable entry-1 must refuse, not publish just entry-0); reason: %s",
			result.Outcome, OutcomeUnresolvableTarget, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}

// TestCompileRefusesUnsignedResult covers the sixth named refusal: every
// other check passes, but the coordinator's own signing step fails.
func TestCompileRefusesUnsignedResult(t *testing.T) {
	f := newBaseFixture(t)
	result, err := Compile(context.Background(), f.st, fakeSigner{err: errors.New("signing key unavailable")}, testInstanceUUID, f.now)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	if result.Outcome != OutcomeUnsigned {
		t.Fatalf("Compile outcome = %q, want %q; reason: %s", result.Outcome, OutcomeUnsigned, result.Reason)
	}
	if result.Program != nil {
		t.Fatalf("a refused compile must never carry a Program")
	}
}
