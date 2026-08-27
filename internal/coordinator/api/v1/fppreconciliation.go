package v1

// This file is TRACK-H-H2-SPEC.md section 5's read route wire type: what
// the coordinator currently makes of one FPP instance's latest accepted
// playlist-entry observation. It is a read-only projection of
// internal/coordinator/fppreconcile.Result — this package holds no
// reconciliation logic of its own, matching every other v1 file's "wire
// shape only" posture.

// FPPPlaylistEntryReconciliationResponse is GET
// /integrations/fpp/playlist-entry-observations/{instanceUuid}/reconciliation's
// body. Outcome is one of "identity-unavailable", "unbound",
// "stale-import", "unknown-entry", "evidence-mismatch", "cross-show", or
// "resolved" (fppreconcile.Outcome's wire spellings). Every Observed*
// field mirrors the underlying observation's own evidence; every other
// field is populated only as fppreconcile.Result's own doc comment
// describes for that Outcome, and is the JSON zero value otherwise.
type FPPPlaylistEntryReconciliationResponse struct {
	InstanceUUID string `json:"instanceUuid"`
	Outcome      string `json:"outcome"`
	Reason       string `json:"reason"`

	ObservedPlaylistHash string `json:"observedPlaylistHash,omitempty"`
	ObservedEntryKey     string `json:"observedEntryKey,omitempty"`
	// ObservedSection is a pointer, not omitempty string: the empty
	// string is a real FPP section (the common default one), so it must
	// render distinguishably from "no section reported" (a nil/absent
	// member). See ObservedPosition's identical reasoning.
	ObservedSection          *string `json:"observedSection,omitempty"`
	ObservedPosition         *int    `json:"observedPosition,omitempty"`
	ObservedSequenceFilename string  `json:"observedSequenceFilename,omitempty"`
	ObservedMediaFilename    string  `json:"observedMediaFilename,omitempty"`
	ObservedAction           string  `json:"observedAction,omitempty"`
	ObservedUnavailable      string  `json:"observedUnavailable,omitempty"`

	PlaylistID          string `json:"playlistId,omitempty"`
	PlaylistRevision    int64  `json:"playlistRevision,omitempty"`
	Show                string `json:"show,omitempty"`
	BindingPlaylistHash string `json:"bindingPlaylistHash,omitempty"`
	BindingPlaylistName string `json:"bindingPlaylistName,omitempty"`

	EntryID     string `json:"entryId,omitempty"`
	CueID       string `json:"cueId,omitempty"`
	CueRevision int64  `json:"cueRevision,omitempty"`

	DefinitionAvailable bool `json:"definitionAvailable"`

	ServerTime string `json:"serverTime"`
}

// FPPPlaylistReadinessResponse is GET
// /integrations/fpp/playlists/{playlistId}/readiness's body,
// TRACK-H-H2-SPEC.md §6 plus the later extensions of the same
// vocabulary (docs/build/IDENTIFIER-REGISTER.md's "Playlist readiness
// conditions"): whether one FPP-backed Playlist is ready, and which
// condition fails first when it is not. A read-only projection of
// internal/coordinator/fppreconcile.Report. FailingCondition is one of
// "definition-missing", "entry-not-in-definition",
// "entry-filename-mismatch", "cue-not-ready", "observation-hash-mismatch",
// or "node-render-unassigned" (fppreconcile.ReadinessCondition's wire
// spellings), empty when Ready. Warning is set only for the non-fatal
// form of the observation-hash condition (§6's own "the normal afternoon
// state, not a fault" case), never alongside a non-empty FailingCondition.
type FPPPlaylistReadinessResponse struct {
	PlaylistID       string `json:"playlistId"`
	Ready            bool   `json:"ready"`
	FailingCondition string `json:"failingCondition,omitempty"`
	Reason           string `json:"reason,omitempty"`
	Warning          string `json:"warning,omitempty"`
	ServerTime       string `json:"serverTime"`
}
