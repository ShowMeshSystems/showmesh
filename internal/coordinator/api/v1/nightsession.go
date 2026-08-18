package v1

// Wire types for the night.session and night.session.active config kinds
// (Track F seam F1, RESTING-MODE.md §6-8/§12/§13, ADR-038, ADR-039).
// Nothing here reuses internal/coordinator/config's Go structs directly
// (ADR-020: the wire layer is separate from the domain layer), matching
// showobjects.go's precedent one seam over.

// ConfigNightSessionFPPPlaylist names an FPP-owned playlist: referenced,
// never created.
type ConfigNightSessionFPPPlaylist struct {
	FPPInstanceID string `json:"fppInstanceId"`
	Playlist      string `json:"playlist"`
}

// ConfigNightSessionAssetRef is ADR-028's asset identity as a night.session
// needs to name one.
type ConfigNightSessionAssetRef struct {
	Show     string `json:"show"`
	Sequence string `json:"sequence"`
	Target   string `json:"target"`
}

// ConfigNightSessionBackgroundAudioItem is one entry of
// resting.backgroundAudio.items.
type ConfigNightSessionBackgroundAudioItem struct {
	ItemID   string `json:"itemId"`
	Show     string `json:"show"`
	Sequence string `json:"sequence"`
	Target   string `json:"target"`
}

// ConfigNightSessionBackgroundAudio is
// night.session.resting.backgroundAudio, present only when the deployment
// configures background audio.
type ConfigNightSessionBackgroundAudio struct {
	Items          []ConfigNightSessionBackgroundAudioItem `json:"items"`
	Repeat         string                                  `json:"repeat"`
	Resume         string                                  `json:"resume"`
	ItemTransition string                                  `json:"itemTransition"`
	CrossfadeMs    *int                                    `json:"crossfadeMs,omitempty"`
	MaxGainDb      float64                                 `json:"maxGainDb"`
}

// ConfigNightSessionResting is night.session.resting.
type ConfigNightSessionResting struct {
	FPPInstanceID      string                             `json:"fppInstanceId"`
	Playlist           string                             `json:"playlist"`
	EndOfNightPlaylist string                             `json:"endOfNightPlaylist"`
	TimelineAsset      ConfigNightSessionAssetRef         `json:"timelineAsset"`
	EndOfNightRepeat   bool                               `json:"endOfNightRepeat"`
	BackgroundAudio    *ConfigNightSessionBackgroundAudio `json:"backgroundAudio,omitempty"`
}

// ConfigNightSessionCue is one entry of enterShow.cues or
// enterResting.cues.
type ConfigNightSessionCue struct {
	Name           string `json:"name"`
	Role           string `json:"role"`
	Action         string `json:"action"`
	OffsetMs       int    `json:"offsetMs"`
	FadeDurationMs *int   `json:"fadeDurationMs,omitempty"`
	Barrier        bool   `json:"barrier"`
	OnFailure      string `json:"onFailure"`
}

// ConfigNightSessionEnterShow is night.session.enterShow.
type ConfigNightSessionEnterShow struct {
	Cues           []ConfigNightSessionCue `json:"cues"`
	BlackoutHoldMs int                     `json:"blackoutHoldMs"`
}

// ConfigNightSessionEnterResting is night.session.enterResting.
type ConfigNightSessionEnterResting struct {
	Cues                []ConfigNightSessionCue `json:"cues"`
	BlackoutAfterShowMs int                     `json:"blackoutAfterShowMs"`
}

// ConfigNightSession is the "night.session" configuration kind's decoded
// payload: the body PUT /config/night.session/{id} accepts and GET
// returns.
type ConfigNightSession struct {
	Show         string                         `json:"show"`
	Label        string                         `json:"label"`
	ShowPlaylist ConfigNightSessionFPPPlaylist  `json:"showPlaylist"`
	Resting      ConfigNightSessionResting      `json:"resting"`
	EnterShow    ConfigNightSessionEnterShow    `json:"enterShow"`
	EnterResting ConfigNightSessionEnterResting `json:"enterResting"`
}

// NightSessionConfigResponse is the body of GET and PUT
// /config/night.session/{id}.
type NightSessionConfigResponse struct {
	ServerTime             string             `json:"serverTime"`
	Kind                   string             `json:"kind"`
	ID                     string             `json:"id"`
	Revision               int64              `json:"revision"`
	Payload                ConfigNightSession `json:"payload"`
	UpdatedAt              string             `json:"updatedAt"`
	CreatedByPrincipalID   *string            `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string            `json:"createdByPrincipalName"`
	Source                 string             `json:"source"`
}

// ConfigNightSessionActive is the "night.session.active" singleton
// configuration kind's decoded payload: the body PUT
// /config/night.session.active accepts, and the "payload" member of GET
// /config/night.session.active's response. Session is "" to mean
// explicitly unset — see config.DecodeNightSessionActivePayload's own doc
// comment.
type ConfigNightSessionActive struct {
	Session string `json:"session"`
}

// NightSessionActiveConfigResponse is the body of GET and PUT
// /config/night.session.active.
type NightSessionActiveConfigResponse struct {
	ServerTime             string                   `json:"serverTime"`
	Kind                   string                   `json:"kind"`
	ID                     string                   `json:"id"`
	Revision               int64                    `json:"revision"`
	Payload                ConfigNightSessionActive `json:"payload"`
	UpdatedAt              string                   `json:"updatedAt"`
	CreatedByPrincipalID   *string                  `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string                  `json:"createdByPrincipalName"`
	Source                 string                   `json:"source"`
}
