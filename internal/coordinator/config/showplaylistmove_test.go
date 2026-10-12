package config

import (
	"encoding/json"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

const moveTestHash = "15e7000000000000000000000000000000000000000000000000000000000000"

func moveEntry(id, cue, section string, position int, filename string) ShowPlaylistEntry {
	return ShowPlaylistEntry{ID: id, Cue: cue, FPP: &ShowPlaylistEntryFPP{Section: section, Position: position, ExpectedSequenceFilename: filename}}
}

func movePlaylist(entries ...ShowPlaylistEntry) ShowPlaylistPayload {
	return ShowPlaylistPayload{
		Show: "halloween", Name: "Quick check", Runner: ShowPlaylistRunnerFPP, MismatchPolicy: ShowPlaylistMismatchPolicyHold,
		FPP:     &ShowPlaylistFPPBinding{InstanceUUID: "inst", PlaylistName: "halloween-quick-check", PlaylistHash: "old"},
		Entries: entries,
	}
}

func seq(section string, position int, name string) fppidentity.DefinitionEntry {
	return fppidentity.DefinitionEntry{Section: section, Position: position, Type: "sequence", SequenceName: name}
}

func planMove(current ShowPlaylistPayload, hash string, news []fppidentity.DefinitionEntry) PlaylistMovePlan {
	return PlanPlaylistMove(current, hash, news, nil)
}

func outcomeOf(t *testing.T, plan PlaylistMovePlan, id string) PlaylistMoveEntry {
	t.Helper()
	for _, e := range plan.Entries {
		if e.EntryID == id {
			return e
		}
	}
	t.Fatalf("no outcome for entry %q", id)
	return PlaylistMoveEntry{}
}

func wantMoved(t *testing.T, plan PlaylistMovePlan, id string, outcome PlaylistMoveOutcome, section string, position int) {
	t.Helper()
	e := outcomeOf(t, plan, id)
	if e.Outcome != outcome || e.To == nil || e.To.Section != section || e.To.Position != position {
		t.Fatalf("entry %q = %+v to %v, want %s at %s %d", id, e, e.To, outcome, section, position)
	}
}

func wantDropped(t *testing.T, plan PlaylistMovePlan, id string) PlaylistMoveEntry {
	t.Helper()
	e := outcomeOf(t, plan, id)
	if e.Outcome != PlaylistMoveDropped || e.To != nil {
		t.Fatalf("entry %q = %+v, want dropped", id, e)
	}
	return e
}

func TestPlanPlaylistMoveEntryInsertedAtTop(t *testing.T) {
	saved := movePlaylist(
		moveEntry("a823", "wake-up", "mainPlaylist", 0, "Wake Up MH Test.fseq"),
		moveEntry("c080", "kpop-audio", "mainPlaylist", 1, "kpop 2026 MH Test.fseq"),
	)
	plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{
		seq("mainPlaylist", 0, "New Opener.fseq"),
		seq("mainPlaylist", 1, "Wake Up MH Test.fseq"),
		seq("mainPlaylist", 2, "kpop 2026 MH Test.fseq"),
	})
	wantMoved(t, plan, "a823", PlaylistMoveMoved, "mainPlaylist", 1)
	wantMoved(t, plan, "c080", PlaylistMoveMoved, "mainPlaylist", 2)
	if len(plan.Unassigned) != 1 || plan.Unassigned[0].SequenceName != "New Opener.fseq" || plan.Unassigned[0].Slot.Position != 0 {
		t.Fatalf("unassigned = %+v, want only the new opener", plan.Unassigned)
	}
	if got := outcomeOf(t, plan, "a823"); got.MatchedBy != PlaylistMoveByFilename || got.DuplicateFilename {
		t.Fatalf("a823 = %+v", got)
	}
	if plan.Proposed.FPP.PlaylistHash != moveTestHash || len(plan.Proposed.Entries) != 2 {
		t.Fatalf("proposed = %+v", plan.Proposed)
	}
	if got := plan.Proposed.Entries[0]; got.ID != "a823" || got.Cue != "wake-up" || got.FPP.Position != 1 || got.FPP.ExpectedSequenceFilename != "Wake Up MH Test.fseq" {
		t.Fatalf("first proposed entry = %+v %+v", got, got.FPP)
	}
	if saved.FPP.PlaylistHash != "old" || saved.Entries[0].FPP.Position != 0 {
		t.Fatal("the input playlist was changed")
	}
}

func TestPlanPlaylistMoveEntryRemoved(t *testing.T) {
	saved := movePlaylist(
		moveEntry("a", "ca", "mainPlaylist", 0, "A.fseq"),
		moveEntry("b", "cb", "mainPlaylist", 1, "B.fseq"),
		moveEntry("c", "cc", "mainPlaylist", 2, "C.fseq"),
	)
	plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "A.fseq"), seq("mainPlaylist", 1, "C.fseq")})
	wantMoved(t, plan, "a", PlaylistMoveKept, "mainPlaylist", 0)
	wantDropped(t, plan, "b")
	wantMoved(t, plan, "c", PlaylistMoveMoved, "mainPlaylist", 1)
	if len(plan.Unassigned) != 0 {
		t.Fatalf("unassigned = %+v", plan.Unassigned)
	}
}

func TestPlanPlaylistMoveEntryRenamed(t *testing.T) {
	saved := movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, "Old Name.fseq"), moveEntry("b", "cb", "mainPlaylist", 1, "B.fseq"))
	plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "New Name.fseq"), seq("mainPlaylist", 1, "B.fseq")})
	wantDropped(t, plan, "a")
	wantMoved(t, plan, "b", PlaylistMoveKept, "mainPlaylist", 1)
	if len(plan.Unassigned) != 1 || plan.Unassigned[0].SequenceName != "New Name.fseq" {
		t.Fatalf("unassigned = %+v", plan.Unassigned)
	}
}

func TestPlanPlaylistMoveSavedEntryWithoutFilename(t *testing.T) {
	t.Run("position still exists", func(t *testing.T) {
		plan := planMove(movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, "")), moveTestHash,
			[]fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Anything.fseq")})
		wantMoved(t, plan, "a", PlaylistMoveKept, "mainPlaylist", 0)
		if got := outcomeOf(t, plan, "a"); got.MatchedBy != PlaylistMoveByPosition {
			t.Fatalf("matched by %q", got.MatchedBy)
		}
		if got := plan.Proposed.Entries[0].FPP.ExpectedSequenceFilename; got != "" {
			t.Fatalf("a filename was added: %q", got)
		}
	})
	t.Run("position is gone", func(t *testing.T) {
		plan := planMove(movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, "A.fseq"), moveEntry("b", "cb", "mainPlaylist", 5, "")), moveTestHash,
			[]fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "A.fseq")})
		if got := wantDropped(t, plan, "b"); got.MatchedBy != PlaylistMoveByPosition {
			t.Fatalf("matched by %q", got.MatchedBy)
		}
	})
	t.Run("a filename match already took the position", func(t *testing.T) {
		saved := movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, "A.fseq"), moveEntry("b", "cb", "mainPlaylist", 1, ""))
		plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "New.fseq"), seq("mainPlaylist", 1, "A.fseq")})
		wantMoved(t, plan, "a", PlaylistMoveMoved, "mainPlaylist", 1)
		wantDropped(t, plan, "b")
		if len(plan.Unassigned) != 1 || plan.Unassigned[0].SequenceName != "New.fseq" {
			t.Fatalf("unassigned = %+v", plan.Unassigned)
		}
	})
}

func TestPlanPlaylistMoveDuplicateFilenames(t *testing.T) {
	t.Run("matched in order and flagged", func(t *testing.T) {
		saved := movePlaylist(moveEntry("first", "c1", "mainPlaylist", 0, "Loop.fseq"), moveEntry("second", "c2", "mainPlaylist", 2, "Loop.fseq"), moveEntry("solo", "c3", "mainPlaylist", 1, "Solo.fseq"))
		plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{
			seq("mainPlaylist", 0, "Intro.fseq"), seq("mainPlaylist", 1, "Loop.fseq"), seq("mainPlaylist", 2, "Solo.fseq"), seq("mainPlaylist", 3, "Loop.fseq"),
		})
		wantMoved(t, plan, "first", PlaylistMoveMoved, "mainPlaylist", 1)
		wantMoved(t, plan, "second", PlaylistMoveMoved, "mainPlaylist", 3)
		wantMoved(t, plan, "solo", PlaylistMoveMoved, "mainPlaylist", 2)
		if !outcomeOf(t, plan, "first").DuplicateFilename || !outcomeOf(t, plan, "second").DuplicateFilename || outcomeOf(t, plan, "solo").DuplicateFilename {
			t.Fatalf("flags wrong: %+v", plan.Entries)
		}
	})
	t.Run("more saved than new: the extra is dropped and flagged", func(t *testing.T) {
		saved := movePlaylist(moveEntry("first", "c1", "mainPlaylist", 0, "Loop.fseq"), moveEntry("second", "c2", "mainPlaylist", 1, "Loop.fseq"))
		plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Loop.fseq")})
		wantMoved(t, plan, "first", PlaylistMoveKept, "mainPlaylist", 0)
		if got := wantDropped(t, plan, "second"); !got.DuplicateFilename {
			t.Fatal("dropped duplicate is not flagged")
		}
	})
	t.Run("more new than saved: the extra new entry is listed and flagged", func(t *testing.T) {
		saved := movePlaylist(moveEntry("only", "c1", "mainPlaylist", 0, "Loop.fseq"))
		plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Loop.fseq"), seq("mainPlaylist", 1, "Loop.fseq")})
		if !outcomeOf(t, plan, "only").DuplicateFilename {
			t.Fatal("matched entry is not flagged")
		}
		if len(plan.Unassigned) != 1 || plan.Unassigned[0].Slot.Position != 1 || !plan.Unassigned[0].DuplicateFilename {
			t.Fatalf("unassigned = %+v", plan.Unassigned)
		}
	})
}

func TestPlanPlaylistMoveAllDropped(t *testing.T) {
	plan := planMove(movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, "A.fseq")), moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Other.fseq")})
	wantDropped(t, plan, "a")
	if plan.CanSave() || len(plan.Proposed.Entries) != 0 {
		t.Fatalf("a playlist with every entry dropped must not be saveable: %+v", plan.Proposed)
	}
}

func TestPlanPlaylistMoveLeadInAndLeadOut(t *testing.T) {
	saved := movePlaylist(
		moveEntry("out", "c3", "leadOut", 0, "Out.fseq"),
		moveEntry("in", "c1", "leadIn", 0, "In.fseq"),
		moveEntry("main", "c2", "mainPlaylist", 0, "Main.fseq"),
	)
	plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{
		seq("leadIn", 0, "Pre.fseq"), seq("leadIn", 1, "In.fseq"), seq("mainPlaylist", 0, "Main.fseq"), seq("leadOut", 0, "Out.fseq"),
	})
	wantMoved(t, plan, "in", PlaylistMoveMoved, "leadIn", 1)
	wantMoved(t, plan, "main", PlaylistMoveKept, "mainPlaylist", 0)
	wantMoved(t, plan, "out", PlaylistMoveKept, "leadOut", 0)
	if got := []string{plan.Proposed.Entries[0].ID, plan.Proposed.Entries[1].ID, plan.Proposed.Entries[2].ID}; got[0] != "in" || got[1] != "main" || got[2] != "out" {
		t.Fatalf("proposed order = %v", got)
	}
	if len(plan.Unassigned) != 1 || plan.Unassigned[0].Slot.Section != "leadIn" {
		t.Fatalf("unassigned = %+v", plan.Unassigned)
	}
}

func TestPlanPlaylistMoveSameEntriesKeepsEverything(t *testing.T) {
	saved := movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, "A.fseq"))
	plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "A.fseq")})
	wantMoved(t, plan, "a", PlaylistMoveKept, "mainPlaylist", 0)
	if len(plan.Unassigned) != 0 {
		t.Fatalf("unassigned = %+v", plan.Unassigned)
	}
}

func TestPlanPlaylistMoveProposedPayloadDecodes(t *testing.T) {
	saved := movePlaylist(
		moveEntry("a823", "wake-up", "mainPlaylist", 0, "Wake Up MH Test.fseq"),
		moveEntry("c080", "kpop-audio", "mainPlaylist", 1, "kpop 2026 MH Test.fseq"),
	)
	saved.Entries[1].FPP.ExpectedMediaFilename = "kpop.mp3"
	plan := planMove(saved, moveTestHash, []fppidentity.DefinitionEntry{
		seq("mainPlaylist", 0, "New Opener.fseq"), seq("mainPlaylist", 1, "Wake Up MH Test.fseq"), seq("mainPlaylist", 2, "kpop 2026 MH Test.fseq"),
	})
	raw, err := EncodeShowPlaylistPayload(plan.Proposed)
	if err != nil {
		t.Fatal(err)
	}
	decoded, verr := DecodeShowPlaylistPayload(raw,
		func(show string) bool { return show == "halloween" },
		func(cue string) (string, bool) { return "halloween", true })
	if verr != nil {
		t.Fatalf("proposed payload refused: %v", verr)
	}
	want, _ := json.Marshal(plan.Proposed)
	got, _ := json.Marshal(decoded)
	if string(want) != string(got) {
		t.Fatalf("round trip changed the payload:\n%s\n%s", want, got)
	}
	if decoded.Entries[1].FPP.ExpectedMediaFilename != "kpop.mp3" || decoded.Name != saved.Name || decoded.MismatchPolicy != saved.MismatchPolicy {
		t.Fatalf("a field other than hash and entries changed: %+v", decoded)
	}
}

func TestPlanPlaylistMoveNoFilenameFollowsTheSequenceItsPositionHeld(t *testing.T) {
	saved := movePlaylist(moveEntry("a", "wake-up", "mainPlaylist", 0, ""), moveEntry("b", "kpop-audio", "mainPlaylist", 1, ""))
	saved.Entries[1].FPP.ExpectedMediaFilename = "kpop.mp3"
	old := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Wake Up.fseq"), seq("mainPlaylist", 1, "kpop.fseq")}
	news := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Opener.fseq"), seq("mainPlaylist", 1, "Wake Up.fseq"), seq("mainPlaylist", 2, "kpop.fseq")}
	plan := PlanPlaylistMove(saved, moveTestHash, news, old)
	wantMoved(t, plan, "a", PlaylistMoveMoved, "mainPlaylist", 1)
	wantMoved(t, plan, "b", PlaylistMoveMoved, "mainPlaylist", 2)
	for id, name := range map[string]string{"a": "Wake Up.fseq", "b": "kpop.fseq"} {
		e := outcomeOf(t, plan, id)
		if e.MatchedBy != PlaylistMoveByPreviousSequence || e.NeedsCheck || e.DuplicateFilename || !e.PreviousSequenceKnown || e.PreviousSequence != name || e.NewSequence != name || e.Filename != "" {
			t.Fatalf("entry %s = %+v", id, e)
		}
	}
	if len(plan.Unassigned) != 1 || plan.Unassigned[0].Slot.Position != 0 {
		t.Fatalf("unassigned = %+v", plan.Unassigned)
	}
	for _, e := range plan.Proposed.Entries {
		if e.FPP.ExpectedSequenceFilename != "" {
			t.Fatalf("a filename was written onto %s: %+v", e.ID, e.FPP)
		}
	}
	if b := plan.Proposed.Entries[1]; b.FPP.ExpectedMediaFilename != "kpop.mp3" || b.FPP.Position != 2 {
		t.Fatalf("proposed b = %+v", b.FPP)
	}
}

func TestPlanPlaylistMoveNoFilenameEntryRemovedOrRenamed(t *testing.T) {
	saved := movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, ""), moveEntry("b", "cb", "mainPlaylist", 1, ""), moveEntry("c", "cc", "mainPlaylist", 2, ""))
	old := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "A.fseq"), seq("mainPlaylist", 1, "B.fseq"), seq("mainPlaylist", 2, "C.fseq")}
	t.Run("removed", func(t *testing.T) {
		plan := PlanPlaylistMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "A.fseq"), seq("mainPlaylist", 1, "C.fseq")}, old)
		wantMoved(t, plan, "a", PlaylistMoveKept, "mainPlaylist", 0)
		got := wantDropped(t, plan, "b")
		if got.MatchedBy != PlaylistMoveByPreviousSequence || got.PreviousSequence != "B.fseq" || got.PositionTaken {
			t.Fatalf("b = %+v", got)
		}
		wantMoved(t, plan, "c", PlaylistMoveMoved, "mainPlaylist", 1)
	})
	t.Run("renamed", func(t *testing.T) {
		plan := PlanPlaylistMove(saved, moveTestHash, []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "A.fseq"), seq("mainPlaylist", 1, "B2.fseq"), seq("mainPlaylist", 2, "C.fseq")}, old)
		if got := wantDropped(t, plan, "b"); got.PreviousSequence != "B.fseq" || got.MatchedBy != PlaylistMoveByPreviousSequence {
			t.Fatalf("b = %+v", got)
		}
		if len(plan.Unassigned) != 1 || plan.Unassigned[0].SequenceName != "B2.fseq" {
			t.Fatalf("unassigned = %+v", plan.Unassigned)
		}
	})
}

func TestPlanPlaylistMoveSavedFilenameAndNoFilenameShareARepeatedName(t *testing.T) {
	saved := movePlaylist(moveEntry("first", "c1", "mainPlaylist", 0, "Loop.fseq"), moveEntry("second", "c2", "mainPlaylist", 1, ""))
	old := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Loop.fseq"), seq("mainPlaylist", 1, "Loop.fseq")}
	news := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Intro.fseq"), seq("mainPlaylist", 1, "Loop.fseq"), seq("mainPlaylist", 2, "Loop.fseq")}
	plan := PlanPlaylistMove(saved, moveTestHash, news, old)
	wantMoved(t, plan, "first", PlaylistMoveMoved, "mainPlaylist", 1)
	wantMoved(t, plan, "second", PlaylistMoveMoved, "mainPlaylist", 2)
	if first := outcomeOf(t, plan, "first"); first.MatchedBy != PlaylistMoveByFilename || !first.DuplicateFilename {
		t.Fatalf("first = %+v", first)
	}
	if second := outcomeOf(t, plan, "second"); second.MatchedBy != PlaylistMoveByPreviousSequence || !second.DuplicateFilename || second.NeedsCheck {
		t.Fatalf("second = %+v", second)
	}
}

func TestPlanPlaylistMoveRepeatedSequenceWithNoSavedFilenames(t *testing.T) {
	saved := movePlaylist(moveEntry("first", "loop", "mainPlaylist", 0, ""), moveEntry("second", "loop", "mainPlaylist", 1, ""))
	old := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Loop.fseq"), seq("mainPlaylist", 1, "Loop.fseq")}
	news := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Intro.fseq"), seq("mainPlaylist", 1, "Loop.fseq"), seq("mainPlaylist", 2, "Loop.fseq")}
	plan := PlanPlaylistMove(saved, moveTestHash, news, old)
	wantMoved(t, plan, "first", PlaylistMoveMoved, "mainPlaylist", 1)
	wantMoved(t, plan, "second", PlaylistMoveMoved, "mainPlaylist", 2)
	if !outcomeOf(t, plan, "first").DuplicateFilename || !outcomeOf(t, plan, "second").DuplicateFilename {
		t.Fatalf("entries not flagged: %+v", plan.Entries)
	}
}

func TestPlanPlaylistMoveFallsBackToPositionWhenNoNameIsKnown(t *testing.T) {
	saved := movePlaylist(moveEntry("a", "wake-up", "mainPlaylist", 0, ""), moveEntry("b", "kpop-audio", "mainPlaylist", 1, ""))
	news := []fppidentity.DefinitionEntry{seq("mainPlaylist", 0, "Opener.fseq"), seq("mainPlaylist", 1, "Wake Up.fseq"), seq("mainPlaylist", 2, "kpop.fseq")}
	check := func(t *testing.T, plan PlaylistMovePlan, known bool, previous map[string]string) {
		t.Helper()
		wantMoved(t, plan, "a", PlaylistMoveKept, "mainPlaylist", 0)
		wantMoved(t, plan, "b", PlaylistMoveKept, "mainPlaylist", 1)
		for id, want := range map[string]string{"a": "Opener.fseq", "b": "Wake Up.fseq"} {
			e := outcomeOf(t, plan, id)
			if e.MatchedBy != PlaylistMoveByPosition || !e.NeedsCheck || e.PreviousSequenceKnown != known || e.PreviousSequence != previous[id] || e.NewSequence != want {
				t.Fatalf("entry %s = %+v", id, e)
			}
		}
	}
	t.Run("the current copy is not held", func(t *testing.T) {
		check(t, PlanPlaylistMove(saved, moveTestHash, news, nil), false, map[string]string{})
	})
	t.Run("the old slot has no sequence name", func(t *testing.T) {
		old := []fppidentity.DefinitionEntry{
			{Section: "mainPlaylist", Position: 0, Type: "media", MediaName: "wake.mp3"},
			{Section: "mainPlaylist", Position: 1, Type: "pause"},
		}
		check(t, PlanPlaylistMove(saved, moveTestHash, news, old), true, map[string]string{"a": "wake.mp3"})
	})
	t.Run("the old copy has no entry at the slot", func(t *testing.T) {
		old := []fppidentity.DefinitionEntry{seq("mainPlaylist", 7, "Elsewhere.fseq")}
		check(t, PlanPlaylistMove(saved, moveTestHash, news, old), false, map[string]string{})
	})
}

func TestPlanPlaylistMovePositionMatchOnTheSameSequenceIsNotFlagged(t *testing.T) {
	saved := movePlaylist(moveEntry("a", "ca", "mainPlaylist", 0, ""))
	old := []fppidentity.DefinitionEntry{{Section: "mainPlaylist", Position: 0, Type: "media", MediaName: "a.mp3"}}
	plan := PlanPlaylistMove(saved, moveTestHash, []fppidentity.DefinitionEntry{{Section: "mainPlaylist", Position: 0, Type: "media", MediaName: "a.mp3"}, seq("mainPlaylist", 1, "B.fseq")}, old)
	if e := outcomeOf(t, plan, "a"); e.NeedsCheck || e.NewSequence != "a.mp3" || e.MatchedBy != PlaylistMoveByPosition {
		t.Fatalf("entry = %+v", e)
	}
}
