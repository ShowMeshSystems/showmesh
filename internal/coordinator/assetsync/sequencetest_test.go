package assetsync

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

func TestCueCatalogCarriesTestTriggersWhetherOrNotANightIsOpen(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	declareNode(t, st, "audio-01")
	advertiseCaps(t, st, "audio-01", capability.Set{{ID: capability.CueTestTriggers, Version: 1}})
	putShow(t, st, "halloween-2026", "Halloween 2026")
	putAudioNode(t, st, "audio-01")
	putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
		Name:    "Thriller",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"}, LTC: &config.ShowCueLTCOutput{}},
	})
	putCue(t, st, "new-song", "halloween-2026", config.ShowCuePayload{
		Name:    "New song",
		Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "new-song-audio"}, LTC: &config.ShowCueLTCOutput{}},
	})
	putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
	putActiveShow(t, st, "halloween-2026")
	active, err := ResolveActiveShow(ctx, st)
	if err != nil {
		t.Fatalf("ResolveActiveShow: %v", err)
	}
	before, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}

	payload, err := config.EncodeSequenceTestPayload(config.SequenceTestPayload{
		Show: "halloween-2026", Entries: []config.SequenceTestEntry{{SequenceFilename: "NewSong.fseq", Cue: "new-song"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	putConfigRev(t, st, config.ShowSequenceTestConfigKind, "halloween-2026", 1, payload)

	day, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	idx := slices.IndexFunc(day.Entries, func(e cuecatalog.Entry) bool { return e.CueID == "new-song" })
	if idx < 0 {
		t.Fatalf("entries = %+v, want new-song included by its test mapping", day.Entries)
	}
	if got := day.Entries[idx].TestTriggers; !slices.Equal(got, []string{"NewSong.fseq"}) {
		t.Fatalf("new-song TestTriggers = %v, want [NewSong.fseq]", got)
	}
	if slices.Contains(day.Entries[idx].Triggers, "NewSong.fseq") {
		t.Fatalf("new-song Triggers = %v, want the test filename kept out of Triggers", day.Entries[idx].Triggers)
	}
	if len(day.Conflicts) != 0 {
		t.Fatalf("Conflicts = %+v, want none: a test cue plays instead of a playlist entry", day.Conflicts)
	}
	if day.Revision == before.Revision {
		t.Fatal("catalog revision did not change when a test mapping was added")
	}

	if err := st.CreateNightSession(ctx, store.NightSessionRecord{ID: "night-1", State: "preshow", StateEnteredAt: time.Unix(2000, 0).UTC()}, time.Unix(2000, 0).UTC()); err != nil {
		t.Fatalf("create night session: %v", err)
	}
	night, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	if night.Revision != day.Revision {
		t.Fatal("catalog revision changed when the night session opened, want it unchanged")
	}

	if err := st.UpdateNightSession(ctx, store.NightSessionRecord{ID: "night-1", State: "stopped", StateEnteredAt: time.Unix(3000, 0).UTC()}, time.Unix(3000, 0).UTC()); err != nil {
		t.Fatalf("stop night session: %v", err)
	}
	stopped, err := ResolveCueCatalog(ctx, st, active, "audio-01")
	if err != nil {
		t.Fatalf("ResolveCueCatalog: %v", err)
	}
	if stopped.Revision != day.Revision {
		t.Fatal("catalog revision changed when the night session stopped, want it unchanged")
	}
}

func TestCueCatalogWithoutTestMappingsOmitsTestTriggersOnTheWire(t *testing.T) {
	raw, err := json.Marshal(cuecatalog.Entry{CueID: "c", Triggers: []string{}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "testTriggers") {
		t.Fatalf("entry JSON = %s, want no testTriggers key so older agents compute the same revision", raw)
	}
}

func advertiseCaps(t *testing.T, st *store.Store, nodeID string, caps capability.Set) {
	t.Helper()
	if err := st.UpsertHello(context.Background(), nodeID, store.HelloRecord{Capabilities: caps, Provenance: store.ProvenanceAgentReport}); err != nil {
		t.Fatalf("upsert hello %q: %v", nodeID, err)
	}
}

func TestCueCatalogForANodeWithoutTheCapabilityIgnoresTestMappings(t *testing.T) {
	for _, tc := range []struct {
		name  string
		hello bool
	}{{"no hello", false}, {"old agent hello", true}} {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t)
			ctx := context.Background()
			declareNode(t, st, "audio-01")
			if tc.hello {
				advertiseCaps(t, st, "audio-01", capability.Set{{ID: "process.supervise", Version: 1}})
			}
			putShow(t, st, "halloween-2026", "Halloween 2026")
			putAudioNode(t, st, "audio-01")
			putCue(t, st, "thriller", "halloween-2026", config.ShowCuePayload{
				Name:    "Thriller",
				Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "thriller-audio"}, LTC: &config.ShowCueLTCOutput{}},
			})
			putCue(t, st, "new-song", "halloween-2026", config.ShowCuePayload{
				Name:    "New song",
				Outputs: config.ShowCueOutputs{Audio: &config.ShowCueAudioOutput{Asset: "new-song-audio"}, LTC: &config.ShowCueLTCOutput{}},
			})
			putPlaylist(t, st, "main", simplePlaylist("halloween-2026", "thriller"))
			putActiveShow(t, st, "halloween-2026")
			active, err := ResolveActiveShow(ctx, st)
			if err != nil {
				t.Fatalf("ResolveActiveShow: %v", err)
			}
			before, err := ResolveCueCatalog(ctx, st, active, "audio-01")
			if err != nil {
				t.Fatalf("ResolveCueCatalog: %v", err)
			}

			payload, err := config.EncodeSequenceTestPayload(config.SequenceTestPayload{
				Show: "halloween-2026", Entries: []config.SequenceTestEntry{
					{SequenceFilename: "NewSong.fseq", Cue: "new-song"},
					{SequenceFilename: "ThrillerAlt.fseq", Cue: "thriller"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			putConfigRev(t, st, config.ShowSequenceTestConfigKind, "halloween-2026", 1, payload)

			after, err := ResolveCueCatalog(ctx, st, active, "audio-01")
			if err != nil {
				t.Fatalf("ResolveCueCatalog: %v", err)
			}
			if after.Revision != before.Revision {
				t.Fatalf("revision = %s, want %s: a node without cue.testtriggers must get the catalog it had before", after.Revision, before.Revision)
			}
			for _, e := range after.Entries {
				if e.CueID == "new-song" || len(e.TestTriggers) != 0 {
					t.Fatalf("entries = %+v, want no test-only Cue and no testTriggers", after.Entries)
				}
			}
		})
	}
}
