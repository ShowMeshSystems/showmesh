package store

import (
	"context"
	"testing"
	"time"
)

// TestTimestampOrderingAcrossListFunctions is the narrow reproduction of
// the reported defect: two rows written within the same second, whose
// nanosecond values trim to DIFFERENT fraction lengths under the old
// time.RFC3339Nano on-disk format (t1 trims to ".122", from .122000000;
// t2 does not trim at all, ".122183"), must still come back in true time
// order from every timestamp-ordered list or get-latest query this
// package exposes. Table-driven over those functions rather than one
// near-identical test per file, per the files the original report and
// this migration's own sweep touch: assets.go, audiosessions.go,
// commands.go, discovery.go, fppplaylistdefinitions.go, macro_runs.go,
// identity.go, and nightsession.go.
func TestTimestampOrderingAcrossListFunctions(t *testing.T) {
	ctx := context.Background()
	t1 := time.Date(2026, 9, 5, 10, 0, 2, 122_000_000, time.UTC)
	t2 := t1.Add(183 * time.Microsecond) // 122183000ns: t1 trims to ".122", t2 does not trim at all.

	type orderedIDs struct {
		ids  []string
		want []string
	}

	cases := []struct {
		name string
		run  func(t *testing.T) orderedIDs
	}{
		{
			name: "ListTokens (created_at ASC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				pid := mustPrincipal(t, st)
				if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-1", PrincipalID: pid, Digest: "d1"}); err != nil {
					t.Fatalf("create token t-1: %v", err)
				}
				clock.t = t2
				if _, err := st.CreateToken(ctx, TokenRecord{ID: "t-2", PrincipalID: pid, Digest: "d2"}); err != nil {
					t.Fatalf("create token t-2: %v", err)
				}
				got, err := st.ListTokens(ctx, pid)
				if err != nil {
					t.Fatalf("list tokens: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r TokenRecord) string { return r.ID }), want: []string{"t-1", "t-2"}}
			},
		},
		{
			name: "ListSessions (created_at ASC)",
			run: func(t *testing.T) orderedIDs {
				st := openTestStore(t, nil)
				pid := mustPrincipal(t, st)
				if _, err := st.CreateSession(ctx, SessionRecord{ID: "s-1", PrincipalID: pid, Digest: "d1"}, t1); err != nil {
					t.Fatalf("create session s-1: %v", err)
				}
				if _, err := st.CreateSession(ctx, SessionRecord{ID: "s-2", PrincipalID: pid, Digest: "d2"}, t2); err != nil {
					t.Fatalf("create session s-2: %v", err)
				}
				got, err := st.ListSessions(ctx, pid)
				if err != nil {
					t.Fatalf("list sessions: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r SessionRecord) string { return r.ID }), want: []string{"s-1", "s-2"}}
			},
		},
		{
			name: "ListCommands (created_at DESC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				if _, err := st.InsertCommand(ctx, CommandRecord{ID: "c-1", IdempotencyKey: "k1", Action: "a", State: "pending"}); err != nil {
					t.Fatalf("insert command c-1: %v", err)
				}
				clock.t = t2
				if _, err := st.InsertCommand(ctx, CommandRecord{ID: "c-2", IdempotencyKey: "k2", Action: "a", State: "pending"}); err != nil {
					t.Fatalf("insert command c-2: %v", err)
				}
				got, err := st.ListCommands(ctx, 10)
				if err != nil {
					t.Fatalf("list commands: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r CommandRecord) string { return r.ID }), want: []string{"c-2", "c-1"}}
			},
		},
		{
			name: "ListUnresolvedCommands (created_at ASC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				if _, err := st.InsertCommand(ctx, CommandRecord{ID: "c-1", IdempotencyKey: "k1", Action: "a", State: "pending"}); err != nil {
					t.Fatalf("insert command c-1: %v", err)
				}
				clock.t = t2
				if _, err := st.InsertCommand(ctx, CommandRecord{ID: "c-2", IdempotencyKey: "k2", Action: "a", State: "pending"}); err != nil {
					t.Fatalf("insert command c-2: %v", err)
				}
				got, err := st.ListUnresolvedCommands(ctx)
				if err != nil {
					t.Fatalf("list unresolved commands: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r CommandRecord) string { return r.ID }), want: []string{"c-1", "c-2"}}
			},
		},
		{
			name: "GetLatestCommandByTargetAction (created_at DESC LIMIT 1)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				if _, err := st.InsertCommand(ctx, CommandRecord{ID: "c-1", IdempotencyKey: "k1", Action: "cue.activate", TargetKind: "node", TargetID: "n-1", State: "pending"}); err != nil {
					t.Fatalf("insert command c-1: %v", err)
				}
				clock.t = t2
				if _, err := st.InsertCommand(ctx, CommandRecord{ID: "c-2", IdempotencyKey: "k2", Action: "cue.activate", TargetKind: "node", TargetID: "n-1", State: "pending"}); err != nil {
					t.Fatalf("insert command c-2: %v", err)
				}
				got, err := st.GetLatestCommandByTargetAction(ctx, "node", "n-1", "cue.activate")
				if err != nil {
					t.Fatalf("get latest command by target/action: %v", err)
				}
				return orderedIDs{ids: []string{got.ID}, want: []string{"c-2"}}
			},
		},
		{
			name: "ListMacroRuns (created_at DESC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				run1, steps1 := testMacroRun("m-1", "mk1", "macro-obj")
				run1.State = "finished" // avoid ADR-031 decision 6's same-macro overlap refusal
				if _, _, err := st.CreateMacroRun(ctx, run1, steps1); err != nil {
					t.Fatalf("create macro run m-1: %v", err)
				}
				clock.t = t2
				run2, steps2 := testMacroRun("m-2", "mk2", "macro-obj")
				if _, _, err := st.CreateMacroRun(ctx, run2, steps2); err != nil {
					t.Fatalf("create macro run m-2: %v", err)
				}
				got, err := st.ListMacroRuns(ctx, "macro-obj", 10)
				if err != nil {
					t.Fatalf("list macro runs: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r MacroRunRecord) string { return r.ID }), want: []string{"m-2", "m-1"}}
			},
		},
		{
			name: "ListRunningMacroRuns (created_at ASC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				run1, steps1 := testMacroRun("m-1", "mk1", "macro-obj-1")
				if _, _, err := st.CreateMacroRun(ctx, run1, steps1); err != nil {
					t.Fatalf("create macro run m-1: %v", err)
				}
				clock.t = t2
				run2, steps2 := testMacroRun("m-2", "mk2", "macro-obj-2")
				if _, _, err := st.CreateMacroRun(ctx, run2, steps2); err != nil {
					t.Fatalf("create macro run m-2: %v", err)
				}
				got, err := st.ListRunningMacroRuns(ctx)
				if err != nil {
					t.Fatalf("list running macro runs: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r MacroRunRecord) string { return r.ID }), want: []string{"m-1", "m-2"}}
			},
		},
		{
			name: "ListDiscoveryRuns (started_at DESC, rowid DESC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "r-1"}); err != nil {
					t.Fatalf("start discovery run r-1: %v", err)
				}
				clock.t = t2
				if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "r-2"}); err != nil {
					t.Fatalf("start discovery run r-2: %v", err)
				}
				got, err := st.ListDiscoveryRuns(ctx, 10)
				if err != nil {
					t.Fatalf("list discovery runs: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r DiscoveryRunRecord) string { return r.ID }), want: []string{"r-2", "r-1"}}
			},
		},
		{
			name: "ListFPPPlaylistDefinitionsByInstance (received_at DESC)",
			run: func(t *testing.T) orderedIDs {
				st := openTestStore(t, nil)
				if _, err := st.PutFPPPlaylistDefinition(ctx, FPPPlaylistDefinitionRecord{
					InstanceUUID: "inst-1", PlaylistHash: "hash-1", PlaylistName: "one", DefinitionJSON: "{}", ReceivedAt: t1,
				}); err != nil {
					t.Fatalf("put fpp playlist definition hash-1: %v", err)
				}
				if _, err := st.PutFPPPlaylistDefinition(ctx, FPPPlaylistDefinitionRecord{
					InstanceUUID: "inst-1", PlaylistHash: "hash-2", PlaylistName: "two", DefinitionJSON: "{}", ReceivedAt: t2,
				}); err != nil {
					t.Fatalf("put fpp playlist definition hash-2: %v", err)
				}
				got, err := st.ListFPPPlaylistDefinitionsByInstance(ctx, "inst-1")
				if err != nil {
					t.Fatalf("list fpp playlist definitions by instance: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r FPPPlaylistDefinitionRecord) string { return r.PlaylistHash }), want: []string{"hash-2", "hash-1"}}
			},
		},
		{
			name: "GetCurrentNightSession (created_at DESC, rowid DESC LIMIT 1)",
			run: func(t *testing.T) orderedIDs {
				st := openTestStore(t, nil)
				if err := st.CreateNightSession(ctx, NightSessionRecord{ID: "ns-1", State: "preparing", StateEnteredAt: t1}, t1); err != nil {
					t.Fatalf("create night session ns-1: %v", err)
				}
				if err := st.CreateNightSession(ctx, NightSessionRecord{ID: "ns-2", State: "preparing", StateEnteredAt: t2}, t2); err != nil {
					t.Fatalf("create night session ns-2: %v", err)
				}
				got, _, err := st.GetCurrentNightSession(ctx)
				if err != nil {
					t.Fatalf("get current night session: %v", err)
				}
				return orderedIDs{ids: []string{got.ID}, want: []string{"ns-2"}}
			},
		},
		{
			name: "GetLatestNightReadiness (completed_at DESC, rowid DESC LIMIT 1)",
			run: func(t *testing.T) orderedIDs {
				st := openTestStore(t, nil)
				if err := st.CreateNightReadiness(ctx, NightReadinessRecord{ID: "nr-1", SessionID: "sess-1", EpochID: "e1", CompletedAt: t1, Outcome: "pass", ChecksJSON: "{}"}); err != nil {
					t.Fatalf("create night readiness nr-1: %v", err)
				}
				if err := st.CreateNightReadiness(ctx, NightReadinessRecord{ID: "nr-2", SessionID: "sess-1", EpochID: "e1", CompletedAt: t2, Outcome: "pass", ChecksJSON: "{}"}); err != nil {
					t.Fatalf("create night readiness nr-2: %v", err)
				}
				got, err := st.GetLatestNightReadiness(ctx, "sess-1")
				if err != nil {
					t.Fatalf("get latest night readiness: %v", err)
				}
				return orderedIDs{ids: []string{got.ID}, want: []string{"nr-2"}}
			},
		},
		{
			name: "ListAudioSessionsByNode (updated_at DESC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "as-1", NodeID: "node-1", DesiredJSON: "{}", Revision: 1}); err != nil {
					t.Fatalf("put audio session as-1: %v", err)
				}
				clock.t = t2
				if err := st.PutAudioSession(ctx, AudioSessionRecord{ID: "as-2", NodeID: "node-1", DesiredJSON: "{}", Revision: 1}); err != nil {
					t.Fatalf("put audio session as-2: %v", err)
				}
				got, err := st.ListAudioSessionsByNode(ctx, "node-1")
				if err != nil {
					t.Fatalf("list audio sessions by node: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r AudioSessionRecord) string { return r.ID }), want: []string{"as-2", "as-1"}}
			},
		},
		{
			name: "ListAssets same identity, superseded (created_at tail tiebreak, ASC)",
			run: func(t *testing.T) orderedIDs {
				clock := &fakeClock{t: t1}
				st := openTestStore(t, clock)
				if _, _, err := st.CreateAsset(ctx, AssetRecord{
					ID: "a-1", ShowID: "show-1", SequenceID: "seq-1", TargetKind: AssetTargetKindShow,
					MediaType: "fseq", ContentHash: "sha256:aaa", RuntimeFilename: "f.fseq", Backend: "volume", StorageKey: "k1",
				}); err != nil {
					t.Fatalf("create asset a-1: %v", err)
				}
				clock.t = t2
				if _, _, err := st.CreateAsset(ctx, AssetRecord{
					ID: "a-2", ShowID: "show-1", SequenceID: "seq-1", TargetKind: AssetTargetKindShow,
					MediaType: "fseq", ContentHash: "sha256:bbb", RuntimeFilename: "f.fseq", Backend: "volume", StorageKey: "k2",
				}); err != nil {
					t.Fatalf("create asset a-2 (supersedes a-1): %v", err)
				}
				got, err := st.ListAssets(ctx, AssetFilter{ShowID: "show-1", SequenceID: "seq-1"})
				if err != nil {
					t.Fatalf("list assets: %v", err)
				}
				return orderedIDs{ids: recordIDs(got, func(r AssetRecord) string { return r.ID }), want: []string{"a-1", "a-2"}}
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.run(t)
			if !sameOrder(got.ids, got.want) {
				t.Fatalf("order = %v, want %v", got.ids, got.want)
			}
		})
	}
}

// recordIDs maps a slice of store records to the IDs id extracts, in the
// same order the store returned them.
func recordIDs[T any](records []T, id func(T) string) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = id(r)
	}
	return out
}

// sameOrder reports whether want's IDs appear in got in exactly the same
// relative order, ignoring any other IDs got may also contain.
func sameOrder(got, want []string) bool {
	var filtered []string
	wantSet := make(map[string]bool, len(want))
	for _, w := range want {
		wantSet[w] = true
	}
	for _, g := range got {
		if wantSet[g] {
			filtered = append(filtered, g)
		}
	}
	if len(filtered) != len(want) {
		return false
	}
	for i := range want {
		if filtered[i] != want[i] {
			return false
		}
	}
	return true
}
