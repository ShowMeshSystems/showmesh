package v1

// Wire types for GET /config/show.playlist/{id}/definition-move-preview:
// the read-only preview of moving an FPP-runner playlist to the newest
// captured copy of its FPP playlist.

// ShowPlaylistMoveSlot is one section and zero-based position of an FPP
// playlist.
type ShowPlaylistMoveSlot struct {
	Section  string `json:"section"`
	Position int    `json:"position"`
}

// ShowPlaylistMoveDefinition names one captured copy of an FPP playlist.
type ShowPlaylistMoveDefinition struct {
	Hash       string `json:"hash"`
	CapturedAt string `json:"capturedAt"`
	EntryCount int    `json:"entryCount"`
}

// ShowPlaylistMoveEntry is what happens to one saved entry. Outcome is
// kept, moved or dropped; MatchedBy is filename or position. To is null
// when the entry is dropped. Summary is the sentence an operator reads.
type ShowPlaylistMoveEntry struct {
	EntryID           string                `json:"entryId"`
	Cue               string                `json:"cue"`
	Filename          string                `json:"filename"`
	Outcome           string                `json:"outcome"`
	MatchedBy         string                `json:"matchedBy"`
	From              ShowPlaylistMoveSlot  `json:"from"`
	To                *ShowPlaylistMoveSlot `json:"to"`
	DuplicateFilename bool                  `json:"duplicateFilename"`
	Summary           string                `json:"summary"`
}

// ShowPlaylistMoveNewEntry is an entry of the newest FPP playlist that
// would have no cue after the move.
type ShowPlaylistMoveNewEntry struct {
	Section           string `json:"section"`
	Position          int    `json:"position"`
	Name              string `json:"name"`
	DuplicateFilename bool   `json:"duplicateFilename"`
	Summary           string `json:"summary"`
}

// ShowPlaylistMovePreviewResponse is the body of GET
// /config/show.playlist/{id}/definition-move-preview. Revision is the
// playlist revision the preview was computed against: send it as If-Match
// with Proposed to PUT /config/show.playlist/{id}. When NewerAvailable is
// false, Newest and Proposed are null and the lists are empty. Proposed is
// also null when CanConfirm is false.
type ShowPlaylistMovePreviewResponse struct {
	ServerTime     string                      `json:"serverTime"`
	PlaylistID     string                      `json:"playlistId"`
	Revision       int64                       `json:"revision"`
	NewerAvailable bool                        `json:"newerAvailable"`
	CanConfirm     bool                        `json:"canConfirm"`
	Summary        string                      `json:"summary"`
	Current        ShowPlaylistMoveDefinition  `json:"current"`
	Newest         *ShowPlaylistMoveDefinition `json:"newest"`
	Entries        []ShowPlaylistMoveEntry     `json:"entries"`
	NewEntries     []ShowPlaylistMoveNewEntry  `json:"newEntries"`
	Proposed       *ConfigShowPlaylist         `json:"proposed"`
}
