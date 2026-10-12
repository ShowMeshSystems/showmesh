package fallbackcompile

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

func TestBaseFixturePublishes(t *testing.T) {
	f := newBaseFixture(t)
	result := f.compile(t)
	f.requirePublished(t, result)

	if len(result.Program.Program.Entries) != 1 {
		t.Fatalf("Entries = %+v, want exactly one", result.Program.Program.Entries)
	}
	entry := result.Program.Program.Entries[0]
	if entry.CueID != "thriller" {
		t.Fatalf("Entries[0].CueID = %q, want %q", entry.CueID, "thriller")
	}
	if len(entry.Targets) != 1 || entry.Targets[0].NodeID != f.nodeID {
		t.Fatalf("Entries[0].Targets = %+v, want exactly one target on %q", entry.Targets, f.nodeID)
	}
	if entry.Targets[0].Render == nil || entry.Targets[0].Render.Filename != "thriller.fseq" {
		t.Fatalf("Entries[0].Targets[0].Render = %+v, want filename thriller.fseq", entry.Targets[0].Render)
	}
}

func TestAnEmptyPlaylistOwesNoProgram(t *testing.T) {
	f := newBaseFixture(t)
	putPlaylist(t, f.st, "main", config.ShowPlaylistPayload{
		Show: f.showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		FPP:     &config.ShowPlaylistFPPBinding{InstanceUUID: testInstanceUUID, PlaylistName: "Main", PlaylistHash: testPlaylistHash},
		Entries: []config.ShowPlaylistEntry{},
	})
	hosts, err := ParticipatingFPPHosts(context.Background(), f.st)
	if err != nil {
		t.Fatalf("ParticipatingFPPHosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("hosts = %v, want none: a playlist with no cues bound owes no program", hosts)
	}
}
