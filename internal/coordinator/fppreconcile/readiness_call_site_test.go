package fppreconcile

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/assetsync"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file guards PlaylistReadiness's CALL SITE, not any one condition's
// internal logic (each condition already has its own focused tests
// elsewhere in this package). The two things a merge of two branches that
// each add conditions to the same call site can silently break, with a
// clean build and a green run of every OTHER test, are:
//
//  1. one side's invocation block is dropped entirely while the other
//     side's replaces it wholesale -- the condition's own resolver still
//     exists and compiles, it is simply never called, so it can never
//     fire again; and
//  2. the surviving invocations end up in the wrong relative order,
//     which changes which failure an operator is shown first without
//     changing whether Ready is true or false.
//
// TestPlaylistReadinessEveryClosedVocabularyConditionFires proves (1) by
// exercising every member of ReadinessCondition's closed vocabulary and
// checking PlaylistReadiness actually reports it; the order tests below
// prove (2) for every adjacent pair in that vocabulary.

// TestPlaylistReadinessEveryClosedVocabularyConditionFires exercises the
// nine conditions this table names. If a future merge (or a hand
// resolution of one) drops an invocation from PlaylistReadiness's body,
// the resolver it should have called still compiles and its own unit
// tests (if any survive) may still pass in isolation -- but the specific
// scenario below, driven through PlaylistReadiness itself, stops
// reporting that condition and this test catches it. The closing
// assertion pins this table's own vocabulary: a tenth entry landing in
// `cases` without a matching name in `want` (or vice versa) is a gap this
// file no longer closes, so the count check exists to make that omission
// loud rather than silent.
func TestPlaylistReadinessEveryClosedVocabularyConditionFires(t *testing.T) {
	cases := []struct {
		name string
		want ReadinessCondition
		run  func(t *testing.T) Report
	}{
		{
			name: "definition-missing",
			want: ReadinessDefinitionMissing,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash64("h1"), "cue-1", "mainPlaylist", 0, "", "")
				// Deliberately no definition stored.
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "entry-not-in-definition",
			want: ReadinessEntryNotInDefinition,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				hash := hash64("h1")
				p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 5, "", "")
				putDefinitionWithEntries(t, st, "inst-1", hash, "Thriller.fseq", "")
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "entry-filename-mismatch",
			want: ReadinessEntryFilenameMismatch,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				hash := hash64("h1")
				p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
				putDefinitionWithEntries(t, st, "inst-1", hash, "SomethingElse.fseq", "")
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "cue-not-ready",
			want: ReadinessCueNotReady,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				hash := hash64("h1")
				p := config.ShowPlaylistPayload{
					Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
					MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
					FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash},
					Entries: []config.ShowPlaylistEntry{{
						ID: "entry-1", Cue: "does-not-exist",
						FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0},
					}},
				}
				putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "observation-hash-mismatch",
			want: ReadinessObservationHashMismatch,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				hash := hash64("h1")
				p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
				putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
				obs := baseObservation("inst-1")
				obs.PlaylistName, obs.PlaylistHash, obs.Section, obs.Position = "Main", hash64("different"), "mainPlaylist", 0
				obs.EntryKey = entryKeyFor(t, p, "entry-1")
				putObservation(t, st, obs)
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "node-render-unassigned",
			want: ReadinessNodeRenderUnassigned,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				hash := hash64("h1")
				p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
				putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
				putSurface(t, st, "wall-1", "show-1", "node-1")
				// Deliberately no surface.pipeline.state evidence for node-1/wall-1.
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "exclusive-claim-conflict",
			want: ReadinessExclusiveClaimConflict,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				putActiveShow(t, st, "show-1")
				putAudioNode(t, st, "node-1")
				declareNode(t, st, "node-1")
				hash := hash64("a1")
				putCueWithAudio(t, st, "cue-1", "show-1")
				putCueWithAudio(t, st, "cue-2", "show-1")
				putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
				p := simpleFPPPlaylist("show-1", "inst-1", hash, "cue-1")
				putPlaylist(t, st, "playlist-1", p)
				p2 := simpleFPPPlaylist("show-1", "inst-2", hash64("a2"), "cue-2")
				putPlaylist(t, st, "playlist-2", p2)
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "node-catalog-stale",
			want: ReadinessNodeCatalogStale,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				putActiveShow(t, st, "show-1")
				hash := hash64("a1")
				p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
				putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
				putPlaylist(t, st, "playlist-1", p)
				putAudioNode(t, st, "node-1")
				declareNode(t, st, "node-1")
				putSurface(t, st, "surface-1", "show-1", "node-1")
				putSurfacePipelineState(t, st, "surface-1", "node-1", "running", time.Unix(1000, 0).UTC())
				// node-1 never acknowledges any cue-catalog revision.
				return mustReadiness(t, st, p)
			},
		},
		{
			name: "assets-missing",
			want: ReadinessAssetsMissing,
			run: func(t *testing.T) Report {
				st := openTestStore(t)
				putShow(t, st, "show-1", "Show One")
				putActiveShow(t, st, "show-1")
				hash := hash64("a1")
				// cue-1 (from singleEntryPlaylist) declares only render, for
				// sequence "seq-cue-1" (putCue's default).
				p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
				putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
				putPlaylist(t, st, "playlist-1", p)
				declareNode(t, st, "node-1")
				putSurface(t, st, "surface-1", "show-1", "node-1")
				putSurfacePipelineState(t, st, "surface-1", "node-1", "running", time.Unix(1000, 0).UTC())

				ctx := context.Background()

				// An asset for cue-1's sequence genuinely exists in the
				// store -- created BEFORE the catalog is resolved and
				// acknowledged below, since the resolved catalog revision
				// folds in each entry's own asset hashes and creating the
				// asset afterward would silently invalidate the ack this
				// case just wrote.
				if _, _, err := st.CreateAsset(ctx, store.AssetRecord{
					ID: "sha256:missing-node-node-1", ShowID: "show-1", SequenceID: "seq-cue-1",
					TargetKind: store.AssetTargetKindNode, TargetID: "node-1", MediaType: "fseq",
					ContentHash: "sha256:missing", RuntimeFilename: "Opening.fseq", SizeBytes: 1024,
					Backend: "volume", StorageKey: "sha256:missing",
				}); err != nil {
					t.Fatalf("create asset: %v", err)
				}

				active, err := assetsync.ResolveActiveShow(ctx, st)
				if err != nil {
					t.Fatalf("ResolveActiveShow: %v", err)
				}
				catalog, err := assetsync.ResolveCueCatalog(ctx, st, active, "node-1")
				if err != nil {
					t.Fatalf("ResolveCueCatalog: %v", err)
				}
				if err := st.PutNodeCueCatalogAck(ctx, store.NodeCueCatalogAckRecord{
					NodeID: "node-1", Revision: catalog.Revision, ShowID: "show-1", Generation: active.Generation,
				}); err != nil {
					t.Fatalf("put node cue catalog ack: %v", err)
				}

				// node-1's own reported inventory does not hold the asset
				// above: this is Missing, never Gap (a sequence with no
				// asset ever registered at all is a separate, separately
				// tracked defect).
				if err := st.ReplaceNodeAssetInventory(ctx, "node-1", nil, store.NodeAssetReportRecord{
					ReportedAt: time.Now(), Complete: true,
				}); err != nil {
					t.Fatalf("replace node asset inventory: %v", err)
				}

				return mustReadiness(t, st, p)
			},
		},
	}

	seen := make(map[ReadinessCondition]bool, len(cases))
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			report := c.run(t)
			if report.Ready {
				t.Fatalf("Ready = true, want false (failing condition %q)", c.want)
			}
			if report.FailingCondition != c.want {
				t.Fatalf("FailingCondition = %q, want %q (reason: %s)", report.FailingCondition, c.want, report.Reason)
			}
		})
		seen[c.want] = true
	}

	want := []ReadinessCondition{
		ReadinessDefinitionMissing, ReadinessEntryNotInDefinition, ReadinessEntryFilenameMismatch,
		ReadinessCueNotReady, ReadinessObservationHashMismatch, ReadinessNodeRenderUnassigned,
		ReadinessExclusiveClaimConflict, ReadinessNodeCatalogStale, ReadinessAssetsMissing,
	}
	if len(seen) != len(want) {
		t.Fatalf("exercised %d distinct conditions, want exactly the %d ReadinessCondition currently declared", len(seen), len(want))
	}
	for _, w := range want {
		if !seen[w] {
			t.Fatalf("%q was never exercised by this table", w)
		}
	}
}

// mustReadiness is a thin, panic-on-error wrapper so the table above can
// stay one expression per case.
func mustReadiness(t *testing.T, st *store.Store, p config.ShowPlaylistPayload) Report {
	t.Helper()
	report, err := PlaylistReadiness(context.Background(), st, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	return report
}

// The tests below prove PlaylistReadiness's ORDER, not merely that each
// condition can fire in isolation: for every adjacent pair in the closed
// vocabulary, a scenario where BOTH conditions would independently fail
// must report only the earlier one, since PlaylistReadiness stops at the
// first failing condition. A merge that keeps every invocation but
// reorders them would pass every test above (each scenario only ever
// triggers its own single condition) while still changing which failure
// an operator is shown here.

// TestPlaylistReadinessOrderObservationBeforeNodeRender proves condition 5
// (observation-hash-mismatch, binding-specific) is checked before condition
// 6 (node-render-unassigned): see PlaylistReadiness's own doc comment on
// why a binding-specific problem must never be masked by a fleet/node-state
// one.
func TestPlaylistReadinessOrderObservationBeforeNodeRender(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	// cue-1 declares render (putCue's default); give it a surface with no
	// render assignment at all, so node-render-unassigned would ALSO fail
	// if it were ever reached.
	putSurface(t, st, "wall-1", "show-1", "node-1")

	obs := baseObservation("inst-1")
	obs.PlaylistName, obs.PlaylistHash, obs.Section, obs.Position = "Main", hash64("different"), "mainPlaylist", 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")
	putObservation(t, st, obs)

	report := mustReadiness(t, st, p)
	if report.FailingCondition != ReadinessObservationHashMismatch {
		t.Fatalf("FailingCondition = %q, want %q: a binding-specific mismatch must be reported before node-render-unassigned even though both would independently fail", report.FailingCondition, ReadinessObservationHashMismatch)
	}
}

// TestPlaylistReadinessOrderNodeRenderBeforeExclusiveClaim proves condition
// 6 (node-render-unassigned) is checked before condition 7
// (exclusive-claim-conflict).
func TestPlaylistReadinessOrderNodeRenderBeforeExclusiveClaim(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")

	hash := hash64("a1")
	putCueRenderAndAudio(t, st, "cue-1", "show-1")
	putCueWithAudio(t, st, "cue-2", "show-1")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	p := simpleFPPPlaylist("show-1", "inst-1", hash, "cue-1")
	putPlaylist(t, st, "playlist-1", p)
	p2 := simpleFPPPlaylist("show-1", "inst-2", hash64("a2"), "cue-2")
	putPlaylist(t, st, "playlist-2", p2)

	// cue-1 declares render; give it a surface with no render assignment,
	// so node-render-unassigned would ALSO fail if it were ever reached.
	// cue-1 and cue-2 collide on their shared audio route regardless, so
	// exclusive-claim-conflict would ALSO fail if node-render-unassigned
	// did not stop readiness first.
	putSurface(t, st, "wall-1", "show-1", "node-1")

	report := mustReadiness(t, st, p)
	if report.FailingCondition != ReadinessNodeRenderUnassigned {
		t.Fatalf("FailingCondition = %q, want %q: node-render-unassigned must be reported before exclusive-claim-conflict even though both would independently fail", report.FailingCondition, ReadinessNodeRenderUnassigned)
	}
}

// TestPlaylistReadinessOrderExclusiveClaimBeforeNodeCatalog proves condition
// 7 (exclusive-claim-conflict) is checked before condition 8
// (node-catalog-stale).
func TestPlaylistReadinessOrderExclusiveClaimBeforeNodeCatalog(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putAudioNode(t, st, "node-1")
	declareNode(t, st, "node-1")

	hash := hash64("a1")
	putCueWithAudio(t, st, "cue-1", "show-1")
	putCueWithAudio(t, st, "cue-2", "show-1")
	putDefinitionWithEntries(t, st, "inst-1", hash, "", "")
	p := simpleFPPPlaylist("show-1", "inst-1", hash, "cue-1")
	putPlaylist(t, st, "playlist-1", p)
	p2 := simpleFPPPlaylist("show-1", "inst-2", hash64("a2"), "cue-2")
	putPlaylist(t, st, "playlist-2", p2)
	// node-1 never acknowledges any cue-catalog revision, so
	// node-catalog-stale would ALSO fail if it were ever reached.

	report := mustReadiness(t, st, p)
	if report.FailingCondition != ReadinessExclusiveClaimConflict {
		t.Fatalf("FailingCondition = %q, want %q: exclusive-claim-conflict must be reported before node-catalog-stale even though both would independently fail", report.FailingCondition, ReadinessExclusiveClaimConflict)
	}
}

// putCueRenderAndAudio stores a Cue declaring BOTH a render and an audio
// output, for order tests that need one Cue to be able to trip
// node-render-unassigned and participate in an exclusive-claim-conflict at
// the same time.
func putCueRenderAndAudio(t *testing.T, st *store.Store, cueID, showID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: cueID,
		Outputs: config.ShowCueOutputs{
			Render: &config.ShowCueRenderOutput{Sequence: "seq-" + cueID},
			Audio:  &config.ShowCueAudioOutput{Asset: "asset-" + cueID},
		},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, cueID, payload)
}
