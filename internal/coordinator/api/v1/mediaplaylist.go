package v1

// Wire types for the "media.playlist" configuration kind
// (internal/coordinator/config/mediaplaylist.go). Unlike show.playlist (a
// list of cues a runner steps through), media.playlist is a list of things
// the audio engine plays as a bed, and several may exist per show.

// ConfigMediaPlaylistItem is one element of media.playlist.items, matching
// config.MediaPlaylistItem's flattened wire shape.
type ConfigMediaPlaylistItem struct {
	Kind     string `json:"kind"`
	Show     string `json:"show"`
	Sequence string `json:"sequence"`
	Target   string `json:"target"`
}

// ConfigMediaPlaylist is the "media.playlist" configuration kind's decoded
// payload: the body PUT /config/media.playlist/{id} accepts, and the
// "payload" member of GET /config/media.playlist/{id}'s response.
type ConfigMediaPlaylist struct {
	Label          string                    `json:"label"`
	Show           string                    `json:"show"`
	Items          []ConfigMediaPlaylistItem `json:"items"`
	Repeat         string                    `json:"repeat"`
	Resume         string                    `json:"resume"`
	ItemTransition string                    `json:"itemTransition"`
	CrossfadeMs    *int                      `json:"crossfadeMs,omitempty"`
	MaxGainDb      float64                   `json:"maxGainDb"`
	FadeOutMs      *int                      `json:"fadeOutMs,omitempty"`
	FadeInMs       *int                      `json:"fadeInMs,omitempty"`
}

// MediaPlaylistConfigResponse is the body of GET and PUT
// /config/media.playlist/{id}.
type MediaPlaylistConfigResponse struct {
	ServerTime             string              `json:"serverTime"`
	Kind                   string              `json:"kind"`
	ID                     string              `json:"id"`
	Revision               int64               `json:"revision"`
	Payload                ConfigMediaPlaylist `json:"payload"`
	UpdatedAt              string              `json:"updatedAt"`
	CreatedByPrincipalID   *string             `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string             `json:"createdByPrincipalName"`
	Source                 string              `json:"source"`
}
