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

	ObservedPlaylistHash     string `json:"observedPlaylistHash,omitempty"`
	ObservedEntryKey         string `json:"observedEntryKey,omitempty"`
	ObservedSection          string `json:"observedSection,omitempty"`
	ObservedPosition         *int   `json:"observedPosition,omitempty"`
	ObservedSequenceFilename string `json:"observedSequenceFilename,omitempty"`
	ObservedMediaFilename    string `json:"observedMediaFilename,omitempty"`
	ObservedAction           string `json:"observedAction,omitempty"`
	ObservedUnavailable      string `json:"observedUnavailable,omitempty"`

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
