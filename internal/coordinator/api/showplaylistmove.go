package api

import (
	"fmt"
	"net/http"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// newestFPPPlaylistDefinition returns the most recently captured stored
// definition of one FPP playlist. Ties on capture time fall to the later
// receipt, then to the hash, so the answer never depends on list order.
func newestFPPPlaylistDefinition(recs []store.FPPPlaylistDefinitionRecord, instanceUUID, playlistName string) (store.FPPPlaylistDefinitionRecord, bool) {
	var newest store.FPPPlaylistDefinitionRecord
	found := false
	for _, rec := range recs {
		if rec.InstanceUUID != instanceUUID || rec.PlaylistName != playlistName {
			continue
		}
		switch {
		case !found,
			rec.CapturedAt.After(newest.CapturedAt),
			rec.CapturedAt.Equal(newest.CapturedAt) && rec.ReceivedAt.After(newest.ReceivedAt),
			rec.CapturedAt.Equal(newest.CapturedAt) && rec.ReceivedAt.Equal(newest.ReceivedAt) && rec.PlaylistHash > newest.PlaylistHash:
			newest, found = rec, true
		}
	}
	return newest, found
}

func playlistMovePreviewProblem(status int, detail string) v1.Problem {
	if status == http.StatusNotFound {
		return resourceNotFoundProblem(detail)
	}
	return v1.Problem{Type: ProblemTypeConflict, Title: "Cannot preview the move", Status: status, Detail: detail}
}

// handleGetShowPlaylistDefinitionMovePreview serves
// GET /api/v1/config/show.playlist/{id}/definition-move-preview. It writes
// nothing: the operator saves the proposed payload with the ordinary
// PUT /config/show.playlist/{id}, If-Match on the revision named here.
func (h *handlers) handleGetShowPlaylistDefinitionMovePreview(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	id := r.PathValue("id")
	rev, _, problem, err := h.getActiveShowConfigRevision(ctx, config.ShowPlaylistConfigKind, id)
	if err != nil {
		h.writeInternalError(w, now, "get active show.playlist config revision", err)
		return
	}
	if problem != nil {
		writeProblem(w, h.logger, now, playlistMovePreviewProblem(http.StatusNotFound, "This playlist does not exist. Pick a playlist from the list and try again."))
		return
	}
	var payload config.ShowPlaylistPayload
	if err := jsonUnmarshalStrict(rev.PayloadJSON, &payload); err != nil {
		h.writeInternalError(w, now, "decode show.playlist config payload", err)
		return
	}
	if payload.Runner != config.ShowPlaylistRunnerFPP || payload.FPP == nil {
		writeProblem(w, h.logger, now, playlistMovePreviewProblem(http.StatusConflict, "This playlist is not run by FPP, so it does not follow an FPP playlist. Open an FPP playlist to move it."))
		return
	}

	recs, err := h.deps.FPPPlaylistDefinitions.ListFPPPlaylistDefinitions(ctx)
	if err != nil {
		h.writeInternalError(w, now, "list fpp playlist definitions", err)
		return
	}
	var current *store.FPPPlaylistDefinitionRecord
	for i := range recs {
		if recs[i].InstanceUUID == payload.FPP.InstanceUUID && recs[i].PlaylistName == payload.FPP.PlaylistName && recs[i].PlaylistHash == payload.FPP.PlaylistHash {
			current = &recs[i]
		}
	}
	if current == nil {
		writeProblem(w, h.logger, now, playlistMovePreviewProblem(http.StatusConflict, "ShowMesh no longer holds the copy of FPP's playlist this playlist was made from. Ask FPP to send its playlists again, then try once more."))
		return
	}
	currentEntries, err := fppidentity.ParseDefinitionEntries(current.DefinitionJSON)
	if err != nil {
		h.writeInternalError(w, now, "parse current fpp playlist definition entries", err)
		return
	}

	resp := v1.ShowPlaylistMovePreviewResponse{
		ServerTime: formatTime(now), PlaylistID: id, Revision: rev.Revision,
		Current:    v1.ShowPlaylistMoveDefinition{Hash: current.PlaylistHash, CapturedAt: formatTime(current.CapturedAt), EntryCount: len(currentEntries)},
		Entries:    []v1.ShowPlaylistMoveEntry{},
		NewEntries: []v1.ShowPlaylistMoveNewEntry{},
	}
	newest, _ := newestFPPPlaylistDefinition(recs, payload.FPP.InstanceUUID, payload.FPP.PlaylistName)
	if newest.PlaylistHash == current.PlaylistHash || !newest.CapturedAt.After(current.CapturedAt) {
		resp.Summary = "This playlist already follows the newest copy of FPP's playlist. There is nothing to move."
		jsonWrite(w, resp)
		return
	}
	newEntries, err := fppidentity.ParseDefinitionEntries(newest.DefinitionJSON)
	if err != nil {
		h.writeInternalError(w, now, "parse newest fpp playlist definition entries", err)
		return
	}

	plan := config.PlanPlaylistMove(payload, newest.PlaylistHash, newEntries)
	resp.NewerAvailable = true
	resp.Newest = &v1.ShowPlaylistMoveDefinition{Hash: newest.PlaylistHash, CapturedAt: formatTime(newest.CapturedAt), EntryCount: len(newEntries)}
	for _, e := range plan.Entries {
		resp.Entries = append(resp.Entries, mapPlaylistMoveEntry(e))
	}
	for _, e := range plan.Unassigned {
		resp.NewEntries = append(resp.NewEntries, mapPlaylistMoveNewEntry(e))
	}
	if plan.CanSave() {
		resp.CanConfirm = true
		proposed := mapConfigShowPlaylist(plan.Proposed)
		resp.Proposed = &proposed
		resp.Summary = "FPP's playlist changed. Review the changes to keep your cues on the right sequences."
	} else {
		resp.Summary = "None of this playlist's sequences are in FPP's playlist any more, so no cue can be kept. Add them back in FPP, or make a new playlist."
	}
	jsonWrite(w, resp)
}

func moveSlotText(s config.PlaylistMoveSlot, withSection bool) string {
	if withSection {
		return fmt.Sprintf("%s position %d", s.Section, s.Position)
	}
	return fmt.Sprintf("position %d", s.Position)
}

func mapPlaylistMoveEntry(e config.PlaylistMoveEntry) v1.ShowPlaylistMoveEntry {
	out := v1.ShowPlaylistMoveEntry{
		EntryID: e.EntryID, Cue: e.Cue, Filename: e.Filename,
		Outcome: string(e.Outcome), MatchedBy: string(e.MatchedBy),
		From:              v1.ShowPlaylistMoveSlot{Section: e.From.Section, Position: e.From.Position},
		DuplicateFilename: e.DuplicateFilename,
	}
	var sentences []string
	switch {
	case e.Outcome == config.PlaylistMoveDropped && e.MatchedBy == config.PlaylistMoveByPosition && e.PositionTaken:
		sentences = append(sentences, fmt.Sprintf("%s now belongs to a sequence that kept its own cue. This cue will be removed from this playlist.", capitalize(moveSlotText(e.From, false))))
	case e.Outcome == config.PlaylistMoveDropped && e.MatchedBy == config.PlaylistMoveByPosition:
		sentences = append(sentences, fmt.Sprintf("FPP's playlist has nothing at %s any more. This cue will be removed from this playlist.", moveSlotText(e.From, false)))
	case e.Outcome == config.PlaylistMoveDropped && e.DuplicateFilename:
		sentences = append(sentences, "This sequence appears more than once, and FPP's playlist has fewer copies than before. This cue will be removed from this playlist.")
	case e.Outcome == config.PlaylistMoveDropped:
		sentences = append(sentences, "This sequence is no longer in FPP's playlist. Its cue will be removed from this playlist.")
	}
	if e.To != nil {
		out.To = &v1.ShowPlaylistMoveSlot{Section: e.To.Section, Position: e.To.Position}
		if e.Outcome == config.PlaylistMoveKept {
			sentences = append(sentences, fmt.Sprintf("Stays at %s.", moveSlotText(*e.To, false)))
		} else {
			withSection := e.From.Section != e.To.Section
			sentences = append(sentences, fmt.Sprintf("Moved from %s to %s.", moveSlotText(e.From, withSection), moveSlotText(*e.To, withSection)))
		}
		if e.MatchedBy == config.PlaylistMoveByPosition {
			sentences = append(sentences, "No sequence name was saved for this cue, so it was matched by position; check that it is still the right sequence.")
		}
	}
	if e.DuplicateFilename && e.Outcome != config.PlaylistMoveDropped {
		sentences = append(sentences, "This sequence appears more than once, so cues were matched in order; check them.")
	}
	out.Summary = strings.Join(sentences, " ")
	return out
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func mapPlaylistMoveNewEntry(e config.PlaylistMoveNewEntry) v1.ShowPlaylistMoveNewEntry {
	name := e.SequenceName
	if name == "" {
		name = e.MediaName
	}
	text := fmt.Sprintf("New at %s in FPP's playlist. It has no cue yet; give it one after you confirm.", moveSlotText(e.Slot, false))
	if e.DuplicateFilename {
		text = fmt.Sprintf("New at %s in FPP's playlist, and this sequence appears more than once. It has no cue yet; give it one after you confirm.", moveSlotText(e.Slot, false))
	}
	return v1.ShowPlaylistMoveNewEntry{Section: e.Slot.Section, Position: e.Slot.Position, Name: name, DuplicateFilename: e.DuplicateFilename, Summary: text}
}
