package config

import (
	"slices"

	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// PlaylistMoveOutcome is what happens to one saved entry when a playlist
// moves to a newer FPP playlist.
type PlaylistMoveOutcome string

const (
	PlaylistMoveKept    PlaylistMoveOutcome = "kept"
	PlaylistMoveMoved   PlaylistMoveOutcome = "moved"
	PlaylistMoveDropped PlaylistMoveOutcome = "dropped"
)

// PlaylistMoveMatch says how a saved entry was matched: by its saved
// sequence filename, or by section and position because it had none.
type PlaylistMoveMatch string

const (
	PlaylistMoveByFilename PlaylistMoveMatch = "filename"
	PlaylistMoveByPosition PlaylistMoveMatch = "position"
)

// PlaylistMoveSlot is one section and position in an FPP playlist.
type PlaylistMoveSlot struct {
	Section  string
	Position int
}

// PlaylistMoveEntry is the outcome for one saved entry. To is nil when the
// entry is dropped. DuplicateFilename is set when the entry's filename
// occurs more than once in the saved playlist or in the newer one.
type PlaylistMoveEntry struct {
	EntryID           string
	Cue               string
	Filename          string
	Outcome           PlaylistMoveOutcome
	MatchedBy         PlaylistMoveMatch
	From              PlaylistMoveSlot
	To                *PlaylistMoveSlot
	DuplicateFilename bool
	// PositionTaken is set on a dropped entry matched by position whose
	// position still exists but now belongs to an entry matched by filename.
	PositionTaken bool
	// PreviousSequence is the sequence name the entry's saved position held
	// in the copy the playlist follows now, when that copy is known and the
	// entry was matched by position. NewSequence is the name at the position
	// it lands on. NeedsCheck is set when a position-matched entry is carried
	// and the two names differ or the earlier one is not known.
	PreviousSequence      string
	PreviousSequenceKnown bool
	NewSequence           string
	NeedsCheck            bool
}

// PlaylistMoveNewEntry is an entry of the newer FPP playlist that no saved
// entry carried over to, so it has no cue.
type PlaylistMoveNewEntry struct {
	Slot              PlaylistMoveSlot
	SequenceName      string
	MediaName         string
	DuplicateFilename bool
}

// PlaylistMovePlan is the whole result of moving a playlist. Proposed is
// only meaningful when at least one entry is carried over: a playlist
// cannot be saved with no entries.
type PlaylistMovePlan struct {
	Entries    []PlaylistMoveEntry
	Unassigned []PlaylistMoveNewEntry
	Proposed   ShowPlaylistPayload
}

// CanSave reports whether Proposed would be accepted by the playlist write.
func (p PlaylistMovePlan) CanSave() bool { return len(p.Proposed.Entries) > 0 }

// PlanPlaylistMove computes how current's cue assignments carry over to the
// FPP playlist whose hash and entries are newHash and newEntries. Saved
// entries with an expected sequence filename are matched to the newer entry
// with that sequence name, in section then position order when a name
// repeats; entries without one are matched by section and position, from
// the slots the filename matches left free. oldEntries are the entries of the
// copy current follows now, or nil when ShowMesh no longer holds it; they only
// name what a position-matched entry used to sit on. It only reads its inputs.
func PlanPlaylistMove(current ShowPlaylistPayload, newHash string, newEntries, oldEntries []fppidentity.DefinitionEntry) PlaylistMovePlan {
	saved := make([]ShowPlaylistEntry, len(current.Entries))
	copy(saved, current.Entries)
	slices.SortStableFunc(saved, func(a, b ShowPlaylistEntry) int { return compareSlots(savedSlot(a), savedSlot(b)) })

	news := make([]fppidentity.DefinitionEntry, len(newEntries))
	copy(news, newEntries)
	slices.SortStableFunc(news, func(a, b fppidentity.DefinitionEntry) int {
		return compareSlots(PlaylistMoveSlot{a.Section, a.Position}, PlaylistMoveSlot{b.Section, b.Position})
	})

	savedNames := map[string]int{}
	for _, e := range saved {
		if name := savedFilename(e); name != "" {
			savedNames[name]++
		}
	}
	newNames := map[string]int{}
	queues := map[string][]int{}
	for i, e := range news {
		if e.SequenceName != "" {
			newNames[e.SequenceName]++
			queues[e.SequenceName] = append(queues[e.SequenceName], i)
		}
	}
	repeated := func(name string) bool { return savedNames[name] > 1 || newNames[name] > 1 }

	claimed := make([]bool, len(news))
	entries := make([]PlaylistMoveEntry, len(saved))
	carried := map[string]*PlaylistMoveSlot{}

	for i, e := range saved {
		out := PlaylistMoveEntry{
			EntryID: e.ID, Cue: e.Cue, Filename: savedFilename(e),
			Outcome: PlaylistMoveDropped, From: savedSlot(e),
		}
		entries[i] = out
		if out.Filename == "" {
			continue
		}
		entries[i].MatchedBy = PlaylistMoveByFilename
		entries[i].DuplicateFilename = repeated(out.Filename)
		if q := queues[out.Filename]; len(q) > 0 {
			claimed[q[0]] = true
			queues[out.Filename] = q[1:]
			setMoveTarget(&entries[i], PlaylistMoveSlot{news[q[0]].Section, news[q[0]].Position})
		}
	}

	oldNames := map[PlaylistMoveSlot]string{}
	for _, e := range oldEntries {
		oldNames[PlaylistMoveSlot{e.Section, e.Position}] = definitionEntryName(e)
	}
	slotIndex := map[PlaylistMoveSlot]int{}
	for i, e := range news {
		slotIndex[PlaylistMoveSlot{e.Section, e.Position}] = i
	}
	for i := range entries {
		if entries[i].Filename != "" {
			continue
		}
		entries[i].MatchedBy = PlaylistMoveByPosition
		entries[i].PreviousSequence, entries[i].PreviousSequenceKnown = oldNames[entries[i].From]
		idx, ok := slotIndex[entries[i].From]
		switch {
		case ok && !claimed[idx]:
			claimed[idx] = true
			setMoveTarget(&entries[i], entries[i].From)
			entries[i].NewSequence = definitionEntryName(news[idx])
			entries[i].NeedsCheck = !entries[i].PreviousSequenceKnown || entries[i].PreviousSequence != entries[i].NewSequence
		case ok:
			entries[i].PositionTaken = true
		}
	}

	var unassigned []PlaylistMoveNewEntry
	for i, e := range news {
		if claimed[i] {
			continue
		}
		unassigned = append(unassigned, PlaylistMoveNewEntry{
			Slot:              PlaylistMoveSlot{e.Section, e.Position},
			SequenceName:      e.SequenceName,
			MediaName:         e.MediaName,
			DuplicateFilename: e.SequenceName != "" && repeated(e.SequenceName),
		})
	}

	for i, e := range saved {
		if entries[i].To != nil {
			carried[e.ID] = entries[i].To
		}
	}
	return PlaylistMovePlan{Entries: entries, Unassigned: unassigned, Proposed: proposedPayload(current, newHash, carried)}
}

func definitionEntryName(e fppidentity.DefinitionEntry) string {
	if e.SequenceName != "" {
		return e.SequenceName
	}
	return e.MediaName
}

func setMoveTarget(e *PlaylistMoveEntry, to PlaylistMoveSlot) {
	e.To = &to
	e.Outcome = PlaylistMoveMoved
	if to == e.From {
		e.Outcome = PlaylistMoveKept
	}
}

// proposedPayload copies current with the new hash and only the carried
// entries, in the newer playlist's order. Carried entries keep every stored
// field; only section and position change.
func proposedPayload(current ShowPlaylistPayload, newHash string, carried map[string]*PlaylistMoveSlot) ShowPlaylistPayload {
	out := current
	if current.FPP != nil {
		binding := *current.FPP
		binding.PlaylistHash = newHash
		out.FPP = &binding
	}
	out.Entries = []ShowPlaylistEntry{}
	for _, e := range current.Entries {
		to, ok := carried[e.ID]
		if !ok {
			continue
		}
		moved := e
		fpp := ShowPlaylistEntryFPP{}
		if e.FPP != nil {
			fpp = *e.FPP
		}
		fpp.Section, fpp.Position = to.Section, to.Position
		moved.FPP = &fpp
		out.Entries = append(out.Entries, moved)
	}
	slices.SortStableFunc(out.Entries, func(a, b ShowPlaylistEntry) int { return compareSlots(savedSlot(a), savedSlot(b)) })
	return out
}

func savedSlot(e ShowPlaylistEntry) PlaylistMoveSlot {
	if e.FPP == nil {
		return PlaylistMoveSlot{}
	}
	return PlaylistMoveSlot{e.FPP.Section, e.FPP.Position}
}

func savedFilename(e ShowPlaylistEntry) string {
	if e.FPP == nil {
		return ""
	}
	return e.FPP.ExpectedSequenceFilename
}

// compareSlots orders slots leadIn, mainPlaylist, leadOut, then any other
// section by name, then by position.
func compareSlots(a, b PlaylistMoveSlot) int {
	if ra, rb := sectionRank(a.Section), sectionRank(b.Section); ra != rb {
		return ra - rb
	}
	if a.Section != b.Section {
		if a.Section < b.Section {
			return -1
		}
		return 1
	}
	return a.Position - b.Position
}

func sectionRank(section string) int {
	if i := slices.Index(fppidentity.DefinitionSections, section); i >= 0 {
		return i
	}
	return len(fppidentity.DefinitionSections)
}
