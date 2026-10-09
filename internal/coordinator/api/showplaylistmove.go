package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// newestFPPPlaylistDefinition returns the stored copy of one FPP playlist
// that ShowMesh received last, the same ordering the playlist readiness check
// uses: the capture time is a clock the FPP host supplies, so it is not
// trusted for this. Ties fall to the capture time, then to the hash.
func newestFPPPlaylistDefinition(recs []store.FPPPlaylistDefinitionRecord, instanceUUID, playlistName string) (store.FPPPlaylistDefinitionRecord, bool) {
	var newest store.FPPPlaylistDefinitionRecord
	found := false
	for _, rec := range recs {
		if rec.InstanceUUID != instanceUUID || rec.PlaylistName != playlistName {
			continue
		}
		switch {
		case !found,
			rec.ReceivedAt.After(newest.ReceivedAt),
			rec.ReceivedAt.Equal(newest.ReceivedAt) && rec.CapturedAt.After(newest.CapturedAt),
			rec.ReceivedAt.Equal(newest.ReceivedAt) && rec.CapturedAt.Equal(newest.CapturedAt) && rec.PlaylistHash > newest.PlaylistHash:
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

const (
	moveSummaryChanged     = "FPP's playlist changed. Review the changes to keep your cues on the right sequences."
	moveSummaryCopyMissing = "ShowMesh no longer has the copy of FPP's playlist this playlist was made from. Review the changes to keep your cues on the right sequences."
	moveSummaryAllDropped  = "None of this playlist's cues can be kept in FPP's changed playlist. Add the sequences back in FPP, or make a new playlist."
	moveSummaryCueGone     = "A cue this playlist uses no longer exists. Pick a different cue for it and save, then review the changes again."
	moveSummaryCannotSave  = "This playlist would not pass the save checks after the move. Fix the playlist and save, then review the changes again."
)

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
	newest, anyHeld := newestFPPPlaylistDefinition(recs, payload.FPP.InstanceUUID, payload.FPP.PlaylistName)
	if !anyHeld {
		writeProblem(w, h.logger, now, playlistMovePreviewProblem(http.StatusConflict, "ShowMesh holds no copy of this FPP playlist. Ask FPP to send its playlists again, then review the changes."))
		return
	}

	resp := v1.ShowPlaylistMovePreviewResponse{
		ServerTime: formatTime(now), PlaylistID: id, Revision: rev.Revision,
		Entries:    []v1.ShowPlaylistMoveEntry{},
		NewEntries: []v1.ShowPlaylistMoveNewEntry{},
	}
	var currentEntries []fppidentity.DefinitionEntry
	if current != nil {
		currentEntries, err = fppidentity.ParseDefinitionEntries(current.DefinitionJSON)
		if err != nil {
			h.writeInternalError(w, now, "parse current fpp playlist definition entries", err)
			return
		}
		resp.Current = &v1.ShowPlaylistMoveDefinition{Hash: current.PlaylistHash, ReceivedAt: formatTime(current.ReceivedAt), EntryCount: len(currentEntries)}
		if newest.PlaylistHash == current.PlaylistHash {
			resp.Summary = "This playlist already follows the newest copy of FPP's playlist. There is nothing to move."
			jsonWrite(w, resp)
			return
		}
	}
	newEntries, err := fppidentity.ParseDefinitionEntries(newest.DefinitionJSON)
	if err != nil {
		h.writeInternalError(w, now, "parse newest fpp playlist definition entries", err)
		return
	}

	plan := config.PlanPlaylistMove(payload, newest.PlaylistHash, newEntries, currentEntries)
	resp.NewerAvailable = true
	resp.Newest = &v1.ShowPlaylistMoveDefinition{Hash: newest.PlaylistHash, ReceivedAt: formatTime(newest.ReceivedAt), EntryCount: len(newEntries)}
	for _, e := range plan.Entries {
		resp.Entries = append(resp.Entries, mapPlaylistMoveEntry(e))
	}
	for _, e := range plan.Unassigned {
		resp.NewEntries = append(resp.NewEntries, mapPlaylistMoveNewEntry(e))
	}
	resp.Summary = moveSummaryChanged
	if current == nil {
		resp.Summary = moveSummaryCopyMissing
	}
	if !plan.CanSave() {
		resp.Summary = moveSummaryAllDropped
		jsonWrite(w, resp)
		return
	}

	refusal, err := h.refusalForMovedPlaylist(ctx, id, plan.Proposed)
	if err != nil {
		h.writeInternalError(w, now, "check the moved show.playlist payload", err)
		return
	}
	if refusal != "" {
		resp.Summary = refusal
		jsonWrite(w, resp)
		return
	}
	resp.CanConfirm = true
	proposed := mapConfigShowPlaylist(plan.Proposed)
	resp.Proposed = &proposed
	jsonWrite(w, resp)
}

// refusalForMovedPlaylist runs the proposed payload through the checks the
// playlist write applies, and returns the operator sentence for the first one
// it would fail, or "" when the write would accept it.
func (h *handlers) refusalForMovedPlaylist(ctx context.Context, id string, proposed config.ShowPlaylistPayload) (string, error) {
	raw, err := config.EncodeShowPlaylistPayload(proposed)
	if err != nil {
		return "", err
	}
	resolveCue, cueErr := h.cueLookup(ctx)
	decoded, verr := config.DecodeShowPlaylistPayload(raw, h.showExists(ctx), resolveCue)
	if *cueErr != nil {
		return "", *cueErr
	}
	if verr != nil {
		if verr.Code == config.ValidationCodeFieldUnknownReference && (verr.Field == "safeCueRef" || strings.HasSuffix(verr.Field, ".cue")) {
			return moveSummaryCueGone, nil
		}
		return moveSummaryCannotSave, nil
	}
	claim, err := h.checkSequenceFilenameClaims(ctx, id, decoded)
	if err != nil {
		return "", err
	}
	if claim != nil {
		return moveSummaryCannotSave, nil
	}
	return "", nil
}

// sectionLabel is how an operator sentence names an FPP playlist section.
func sectionLabel(section string) string {
	switch section {
	case "leadIn":
		return "lead-in"
	case "mainPlaylist":
		return "main playlist"
	case "leadOut":
		return "lead-out"
	}
	return section
}

// moveSlotText names a slot. The main playlist is the default and is only
// named when withSection is set or the slot is in another section.
func moveSlotText(s config.PlaylistMoveSlot, withSection bool) string {
	label := sectionLabel(s.Section)
	if label == "" || (s.Section == "mainPlaylist" && !withSection) {
		return fmt.Sprintf("position %d", s.Position)
	}
	return fmt.Sprintf("%s position %d", label, s.Position)
}

func sequenceText(name string) string {
	if name == "" {
		return "an entry with no sequence name"
	}
	return name
}

func mapPlaylistMoveEntry(e config.PlaylistMoveEntry) v1.ShowPlaylistMoveEntry {
	out := v1.ShowPlaylistMoveEntry{
		EntryID: e.EntryID, Cue: e.Cue, Filename: e.Filename,
		Outcome: string(e.Outcome), MatchedBy: string(e.MatchedBy),
		From:              v1.ShowPlaylistMoveSlot{Section: e.From.Section, Position: e.From.Position},
		DuplicateFilename: e.DuplicateFilename,
		PreviousSequence:  e.PreviousSequence, NewSequence: e.NewSequence, NeedsCheck: e.NeedsCheck,
	}
	var sentences []string
	switch {
	case e.Outcome == config.PlaylistMoveDropped && e.MatchedBy == config.PlaylistMoveByPosition && e.PositionTaken:
		sentences = append(sentences, fmt.Sprintf("%s now belongs to a sequence that kept its own cue. This cue will be removed from this playlist.", capitalize(moveSlotText(e.From, false))))
	case e.Outcome == config.PlaylistMoveDropped && e.MatchedBy == config.PlaylistMoveByPosition:
		sentences = append(sentences, fmt.Sprintf("FPP's playlist has no entry at %s any more. This cue will be removed from this playlist.", moveSlotText(e.From, true)))
	case e.Outcome == config.PlaylistMoveDropped && e.MatchedBy == config.PlaylistMoveByPreviousSequence && e.DuplicateFilename:
		sentences = append(sentences, fmt.Sprintf("No sequence name was saved for this cue. Its position held %s, which appears more than once, and FPP's playlist has fewer copies than before. This cue will be removed from this playlist.", e.PreviousSequence))
	case e.Outcome == config.PlaylistMoveDropped && e.MatchedBy == config.PlaylistMoveByPreviousSequence:
		sentences = append(sentences, fmt.Sprintf("No sequence name was saved for this cue. Its position held %s, which is no longer in FPP's playlist. This cue will be removed from this playlist.", e.PreviousSequence))
	case e.Outcome == config.PlaylistMoveDropped && e.DuplicateFilename:
		sentences = append(sentences, "This sequence appears more than once, and FPP's playlist has fewer copies than before. This cue will be removed from this playlist.")
	case e.Outcome == config.PlaylistMoveDropped:
		sentences = append(sentences, "This sequence is no longer in FPP's playlist. Its cue will be removed from this playlist.")
	}
	if e.To != nil {
		out.To = &v1.ShowPlaylistMoveSlot{Section: e.To.Section, Position: e.To.Position}
		switch {
		case e.MatchedBy == config.PlaylistMoveByPosition && !e.NeedsCheck:
			sentences = append(sentences, fmt.Sprintf("No sequence name was saved for this cue, so it stays at %s, which still holds %s.", moveSlotText(*e.To, false), sequenceText(e.NewSequence)))
		case e.MatchedBy == config.PlaylistMoveByPosition && e.PreviousSequenceKnown:
			sentences = append(sentences,
				fmt.Sprintf("No sequence name was saved for this cue, so it stays at %s, which held %s and now holds %s.", moveSlotText(*e.To, false), sequenceText(e.PreviousSequence), sequenceText(e.NewSequence)),
				"If that is wrong, cancel and assign this cue again after moving.")
		case e.MatchedBy == config.PlaylistMoveByPosition:
			sentences = append(sentences,
				fmt.Sprintf("No sequence name was saved for this cue, so it stays at %s, which now holds %s. The earlier sequence name is not known.", moveSlotText(*e.To, false), sequenceText(e.NewSequence)),
				"If that is wrong, cancel and assign this cue again after moving.")
		case e.MatchedBy == config.PlaylistMoveByPreviousSequence && e.Outcome == config.PlaylistMoveKept:
			sentences = append(sentences, fmt.Sprintf("No sequence name was saved for this cue, so it follows the sequence its position held, %s. Stays at %s.", e.PreviousSequence, moveSlotText(*e.To, false)))
		case e.MatchedBy == config.PlaylistMoveByPreviousSequence:
			withSection := e.From.Section != e.To.Section
			sentences = append(sentences, fmt.Sprintf("No sequence name was saved for this cue, so it follows the sequence its position held, %s. Moved from %s to %s.", e.PreviousSequence, moveSlotText(e.From, withSection), moveSlotText(*e.To, withSection)))
		case e.Outcome == config.PlaylistMoveKept:
			sentences = append(sentences, fmt.Sprintf("Stays at %s.", moveSlotText(*e.To, false)))
		default:
			withSection := e.From.Section != e.To.Section
			sentences = append(sentences, fmt.Sprintf("Moved from %s to %s.", moveSlotText(e.From, withSection), moveSlotText(*e.To, withSection)))
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
