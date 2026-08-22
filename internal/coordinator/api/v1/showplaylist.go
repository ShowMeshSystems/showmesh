package v1

// Wire types for the "show.playlist" configuration kind
// (TRACK-H-H1-SPEC.md section 3). Follows showcue.go's precedent.

// ConfigShowPlaylistFPPBinding is show.playlist.fpp.
type ConfigShowPlaylistFPPBinding struct {
	InstanceUUID string `json:"instanceUuid"`
	PlaylistName string `json:"playlistName"`
	PlaylistHash string `json:"playlistHash"`
}

// ConfigShowPlaylistShowmeshAudio is show.playlist.showmeshAudio.
type ConfigShowPlaylistShowmeshAudio struct {
	Repeat string `json:"repeat"`
}

// ConfigShowPlaylistEntryFPP is one entry's fpp binding.
type ConfigShowPlaylistEntryFPP struct {
	Section                  string `json:"section"`
	Position                 int    `json:"position"`
	ExpectedSequenceFilename string `json:"expectedSequenceFilename,omitempty"`
	ExpectedMediaFilename    string `json:"expectedMediaFilename,omitempty"`
}

// ConfigShowPlaylistEntry is one element of show.playlist.entries.
type ConfigShowPlaylistEntry struct {
	ID  string                      `json:"id"`
	Cue string                      `json:"cue"`
	FPP *ConfigShowPlaylistEntryFPP `json:"fpp,omitempty"`
}

// ConfigShowPlaylist is the "show.playlist" configuration kind's decoded
// payload (TRACK-H-H1-SPEC.md section 3): the body PUT
// /config/show.playlist/{id} accepts, and the "payload" member of GET
// /config/show.playlist/{id}'s response.
type ConfigShowPlaylist struct {
	Show           string                           `json:"show"`
	Name           string                           `json:"name"`
	Runner         string                           `json:"runner"`
	MismatchPolicy string                           `json:"mismatchPolicy,omitempty"`
	SafeCueRef     string                           `json:"safeCueRef,omitempty"`
	FPP            *ConfigShowPlaylistFPPBinding    `json:"fpp,omitempty"`
	ShowmeshAudio  *ConfigShowPlaylistShowmeshAudio `json:"showmeshAudio,omitempty"`
	Entries        []ConfigShowPlaylistEntry        `json:"entries"`
}

// ShowPlaylistConfigResponse is the body of GET and PUT
// /config/show.playlist/{id}.
type ShowPlaylistConfigResponse struct {
	ServerTime             string             `json:"serverTime"`
	Kind                   string             `json:"kind"`
	ID                     string             `json:"id"`
	Revision               int64              `json:"revision"`
	Payload                ConfigShowPlaylist `json:"payload"`
	UpdatedAt              string             `json:"updatedAt"`
	CreatedByPrincipalID   *string            `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string            `json:"createdByPrincipalName"`
	Source                 string             `json:"source"`
}
