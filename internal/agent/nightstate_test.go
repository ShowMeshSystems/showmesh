package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

func setNightOpen(t *testing.T, h *NightStateHolder, open bool) {
	t.Helper()
	res, err := h.set(context.Background(), map[string]any{"open": open}, time.Now)
	if err != nil || !res.Confirmed {
		t.Fatalf("night.state.set open=%v: result %+v, err %v", open, res, err)
	}
}

func TestNightStateDefaultsOpenAndSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	if !NewNightStateHolder(dir, discardLogger()).Open() {
		t.Fatal("a node that never received a night state must treat the night as open")
	}
	setNightOpen(t, NewNightStateHolder(dir, discardLogger()), false)
	if NewNightStateHolder(dir, discardLogger()).Open() {
		t.Fatal("closed night state was not reloaded from disk after a restart")
	}
	if _, err := NewNightStateHolder(dir, discardLogger()).set(context.Background(), map[string]any{"open": "yes"}, time.Now); err == nil {
		t.Fatal("a non-boolean open param was accepted")
	}
}

func TestMultiSyncTestTriggersFollowTheNightFlag(t *testing.T) {
	dir := t.TempDir()
	catalogStore := heldcatalog.NewFileStore(dir)
	audioOut := &cuecatalog.AudioOutput{Asset: "a"}
	if err := catalogStore.Save(heldcatalog.HeldCatalog{
		Show: "s", Generation: 1, Node: "n", Revision: "r1", ReceivedAt: time.Now(),
		Entries: []cuecatalog.Entry{
			{CueID: "playlist-cue", Outputs: cuecatalog.Outputs{Audio: audioOut}, Triggers: []string{"Shared.fseq"}},
			{CueID: "test-cue", Outputs: cuecatalog.Outputs{Audio: audioOut}, Triggers: []string{}, TestTriggers: []string{"New.fseq", "Shared.fseq"}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	trigger := newMultiSyncCueAudioTrigger(discardLogger(), time.Now, nil)

	if _, ok := trigger.lookup("New.fseq", catalogStore); ok {
		t.Fatal("test trigger fired with no night state wired")
	}
	night := NewNightStateHolder(dir, discardLogger())
	trigger.SetNightState(night)
	if _, ok := trigger.lookup("New.fseq", catalogStore); ok {
		t.Fatal("test trigger fired while the night flag was absent")
	}
	setNightOpen(t, night, false)
	entry, ok := trigger.lookup("New.fseq", catalogStore)
	if !ok || entry.CueID != "test-cue" {
		t.Fatalf("lookup New.fseq with the night closed = %+v, %v; want test-cue", entry, ok)
	}
	if entry, _ := trigger.lookup("Shared.fseq", catalogStore); entry.CueID != "playlist-cue" {
		t.Fatalf("lookup Shared.fseq = %q, want the playlist trigger to win", entry.CueID)
	}
	setNightOpen(t, night, true)
	if _, ok := trigger.lookup("New.fseq", catalogStore); ok {
		t.Fatal("test trigger fired while the night was open")
	}
}
