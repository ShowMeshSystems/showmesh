package fppreconcile

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/internal/version"
)

var (
	sentenceEnd   = regexp.MustCompile(`[.!?](\s|$)`)
	longHexRun    = regexp.MustCompile(`[0-9a-fA-F]{16,}`)
	bannedWording = regexp.MustCompile(`(?i)\b(interlock|attestation|provenance|epoch|revision|rev|boundary|armed|evidence|desired|observed|dispatch|dispatched|reconcile|resolver|collector|signal|payload|idempotency|binding|principal|posture|pinned|barrier|outbox|decode|instance|endpoint)\b|show\.cue|audio\.node|night\.session|outputs\.|"`)
)

// assertOneMessage fails when msg is not one complete operator message.
func assertOneMessage(t *testing.T, msg string) {
	t.Helper()
	if msg == "" || strings.ToUpper(msg[:1]) != msg[:1] || !strings.HasSuffix(msg, ".") {
		t.Errorf("%q: want a capital first letter and a closing full stop", msg)
	}
	if n := len(sentenceEnd.FindAllString(msg, -1)); n > 2 {
		t.Errorf("%q: %d sentences, want at most two", msg, n)
	}
	if len(msg) > 160 {
		t.Errorf("%q: %d characters, want at most 160", msg, len(msg))
	}
	if strings.Contains(msg, "; ") {
		t.Errorf("%q: two messages joined into one", msg)
	}
	if run := longHexRun.FindString(msg); run != "" {
		t.Errorf("%q: carries the hash or id %q", msg, run)
	}
	if word := bannedWording.FindString(msg); word != "" {
		t.Errorf("%q: uses %q, which an operator cannot act on", msg, word)
	}
}

// TestPlaylistReadinessReturnsEachWarningAsItsOwnMessage holds every warning to one message and each per-node fact to one line.
func TestPlaylistReadinessReturnsEachWarningAsItsOwnMessage(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	orig := version.Version
	t.Cleanup(func() { version.Version = orig })
	version.Version = "main-8de657f6"
	both := []string{"showmesh-node-01", "pi-audio-01"}
	main, pairs := seedAcceptedPairsState(t, st, both)

	report, err := PlaylistReadiness(ctx, st, nil, nil, "playlist-main", 1, main)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready {
		t.Fatalf("Ready = false (%s: %s), want ready with warnings", report.FailingCondition, report.Reason)
	}
	for _, w := range report.Warnings {
		assertOneMessage(t, w)
	}

	count := func(parts ...string) int {
		n := 0
		for _, w := range report.Warnings {
			if containsAll(w, parts...) {
				n++
			}
		}
		return n
	}
	for _, node := range both {
		if count("Node "+node+" has ", " cue pairs sharing", "Accepted by admin") != 1 {
			t.Errorf("want one accepted-pairs line for %s in %q", node, report.Warnings)
		}
	}
	if pairs["showmesh-node-01"] != 4 || count("Node showmesh-node-01 has 4 cue pairs") != 1 {
		t.Errorf("want showmesh-node-01's 4 pairs counted, got %d pairs in %q", pairs["showmesh-node-01"], report.Warnings)
	}
	// Three cues play on each node, and neither node is reporting.
	for _, node := range both {
		if count("Node "+node+" is not reporting") != 1 || count("Node "+node+" is not reporting, so cues may start out of sync") != 1 {
			t.Errorf("want one clock line for %s naming no cue in %q", node, report.Warnings)
		}
	}
	if count("dracula", "default node") != 1 {
		t.Errorf("want one line for the cue on the default node in %q", report.Warnings)
	}
	if count("no FPP sequence") != 1 {
		t.Errorf("want one line for the cue with no FPP sequence in %q", report.Warnings)
	}
	if count("main-82a9da62") != 1 || count("pi-audio-01", "showmesh-node-01", "main-82a9da62", "main-8de657f6") != 1 {
		t.Errorf("want the version difference stated once, naming both nodes, in %q", report.Warnings)
	}
}

// seedAcceptedPairsState stores a show whose two nodes each carry accepted
// cue pairs and report an older version, and returns the Main playlist with
// the number of pairs on each node.
func seedAcceptedPairsState(t *testing.T, st *store.Store, both []string) (config.ShowPlaylistPayload, map[string]int) {
	t.Helper()
	ctx := context.Background()
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "showmesh-node-01")
	putProgramOnlyAudioNode(t, st, "pi-audio-01")
	for _, node := range both {
		declareNode(t, st, node)
		putNodeVersion(t, st, node, "main-82a9da62")
	}
	for _, cue := range []string{"bg1", "kpop-audio", "wake-up", "finale"} {
		putCueWithOutputs(t, st, cue, "show-1", config.ShowCueOutputs{
			Audio: &config.ShowCueAudioOutput{Asset: "asset-" + cue, Targets: both},
		})
	}
	putCueWithAudio(t, st, "dracula", "show-1")

	hash := hash64("a1")
	main := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-1", Cue: "kpop-audio", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0}},
			{ID: "entry-2", Cue: "wake-up", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1}},
			{ID: "entry-3", Cue: "dracula", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 2}},
			{ID: "entry-4", Cue: "finale", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 3}},
		},
	}
	if _, err := st.PutFPPPlaylistDefinition(ctx, store.FPPPlaylistDefinitionRecord{
		InstanceUUID: "inst-1", PlaylistHash: hash, PlaylistName: "Main",
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[{"type":"sequence"},{"type":"sequence"},{"type":"sequence"},{"type":"sequence"}],"leadOut":[]}`,
		CapturedAt:     time.Unix(1000, 0).UTC(), ReceivedAt: time.Unix(1000, 0).UTC(),
	}); err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
	putPlaylist(t, st, "playlist-main", main)
	putPlaylist(t, st, "playlist-bed", config.ShowPlaylistPayload{
		Show: "show-1", Name: "Bed", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Bed", PlaylistHash: hash64("b2")},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: "bg1", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
		}},
	})

	active, err := assetsync.ResolveActiveShow(ctx, st)
	if err != nil || !active.Configured {
		t.Fatalf("ResolveActiveShow: configured=%v err=%v", active.Configured, err)
	}
	pairs := map[string]int{}
	for _, node := range both {
		catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, node)
		if err != nil {
			t.Fatalf("ResolveCueCatalog(%s): %v", node, err)
		}
		if len(catalog.Conflicts) < 2 {
			t.Fatalf("%s has %d cue pairs, want at least 2 (fixture setup)", node, len(catalog.Conflicts))
		}
		pairs[node] = len(catalog.Conflicts)
		accepted := make([]store.StoredCatalogConflict, 0, len(catalog.Conflicts))
		for _, c := range catalog.Conflicts {
			accepted = append(accepted, store.StoredCatalogConflict{CueA: c.CueA, CueB: c.CueB, Claim: c.Claim.String()})
		}
		if err := st.PutNodeCueCatalogOverride(ctx, store.NodeCueCatalogOverrideRecord{
			NodeID: node, Revision: catalog.Revision, ShowID: "show-1", Generation: active.Generation,
			Conflicts: accepted, OverriddenByPrincipalID: "admin-1", OverriddenByPrincipalName: "admin",
		}); err != nil {
			t.Fatalf("PutNodeCueCatalogOverride(%s): %v", node, err)
		}
		if err := st.PutNodeCueCatalogAck(ctx, store.NodeCueCatalogAckRecord{
			NodeID: node, Revision: catalog.Revision, ShowID: "show-1", Generation: active.Generation,
		}); err != nil {
			t.Fatalf("PutNodeCueCatalogAck(%s): %v", node, err)
		}
	}
	return main, pairs
}

// TestPlaylistReadinessFailingReasonsAreOneMessageEach holds every fixed
// failing reason to the same rule as a warning.
func TestPlaylistReadinessFailingReasonsAreOneMessageEach(t *testing.T) {
	audioClaim := config.ShowCueClaim{Kind: config.ShowCueClaimKindProgramAudioRoute, Node: "pi-audio-01", Resource: "alsa_output.usb-0123456789abcdef0123"}
	for _, reason := range []string{
		playlistChangedReason,
		filenameMismatchReason(2, "sequence", "wake-up.fseq", "wake.fseq"),
		filenameMismatchReason(2, "media", "wake-up.mp3", ""),
		conflictReason(assetsync.CatalogConflict{CueA: "bg1", CueB: "kpop-audio", Claim: audioClaim}),
		CueCatalogOverrideWarning("pi-audio-01", assetsync.Catalog{Conflicts: []assetsync.CatalogConflict{{CueA: "bg1", CueB: "wake-up", Claim: audioClaim}}}, store.NodeCueCatalogOverrideRecord{OverriddenByPrincipalID: "0123456789abcdef0123"}),
	} {
		assertOneMessage(t, reason)
	}
}
