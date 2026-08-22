package main

import "time"

// This file is Track F seam F1's own transcription of
// internal/coordinator/api/v1/nightsession.go into showmeshctl's
// independent wire-decoding layer, matching types_macro.go's own standing
// rule (see that file's doc comment and doc.go/importgraph_test.go):
// nothing here reuses that package's types directly, even where the shape
// is identical field-for-field, so a server-side JSON tag rename cannot
// silently rename both sides of this program's own decode at once.

// nightSessionFPPPlaylist names an FPP-owned playlist: referenced, never
// created.
type nightSessionFPPPlaylist struct {
	FPPInstanceID string `json:"fppInstanceId"`
	Playlist      string `json:"playlist"`
}

// nightSessionAssetRef is ADR-028's asset identity as a night.session
// needs to name one.
type nightSessionAssetRef struct {
	Show     string `json:"show"`
	Sequence string `json:"sequence"`
	Target   string `json:"target"`
}

// nightSessionBackgroundAudioItem is one entry of
// resting.backgroundAudio.items.
type nightSessionBackgroundAudioItem struct {
	ItemID   string `json:"itemId"`
	Show     string `json:"show"`
	Sequence string `json:"sequence"`
	Target   string `json:"target"`
}

// nightSessionBackgroundAudio is night.session.resting.backgroundAudio,
// present only when the deployment configures background audio.
type nightSessionBackgroundAudio struct {
	Items          []nightSessionBackgroundAudioItem `json:"items"`
	Repeat         string                            `json:"repeat"`
	Resume         string                            `json:"resume"`
	ItemTransition string                            `json:"itemTransition"`
	CrossfadeMs    *int                              `json:"crossfadeMs,omitempty"`
	MaxGainDb      float64                           `json:"maxGainDb"`
}

// nightSessionResting is night.session.resting.
type nightSessionResting struct {
	FPPInstanceID      string                       `json:"fppInstanceId"`
	Playlist           string                       `json:"playlist"`
	EndOfNightPlaylist string                       `json:"endOfNightPlaylist"`
	TimelineAsset      nightSessionAssetRef         `json:"timelineAsset"`
	EndOfNightRepeat   bool                         `json:"endOfNightRepeat"`
	BackgroundAudio    *nightSessionBackgroundAudio `json:"backgroundAudio,omitempty"`
}

// nightSessionCue is one entry of enterShow.cues or enterResting.cues.
type nightSessionCue struct {
	Name               string  `json:"name"`
	Role               string  `json:"role"`
	Action             string  `json:"action"`
	OffsetMs           int     `json:"offsetMs"`
	FadeDurationMs     *int    `json:"fadeDurationMs,omitempty"`
	Barrier            bool    `json:"barrier"`
	OnFailure          string  `json:"onFailure"`
	AnnouncementPolicy *string `json:"announcementPolicy,omitempty"`
}

// nightSessionEnterShow is night.session.enterShow.
type nightSessionEnterShow struct {
	Cues           []nightSessionCue `json:"cues"`
	BlackoutHoldMs int               `json:"blackoutHoldMs"`
}

// nightSessionEnterResting is night.session.enterResting.
type nightSessionEnterResting struct {
	Cues                []nightSessionCue `json:"cues"`
	BlackoutAfterShowMs int               `json:"blackoutAfterShowMs"`
}

// nightSession is the "night.session" configuration kind's decoded
// payload: the "payload" member of GET/PUT /config/night.session/{id}'s
// response.
type nightSession struct {
	Show                      string                   `json:"show"`
	Label                     string                   `json:"label"`
	ShowPlaylist              nightSessionFPPPlaylist  `json:"showPlaylist"`
	Resting                   nightSessionResting      `json:"resting"`
	EnterShow                 nightSessionEnterShow    `json:"enterShow"`
	EnterResting              nightSessionEnterResting `json:"enterResting"`
	AnnouncementDefaultPolicy string                   `json:"announcementDefaultPolicy"`
}

// nightSessionConfigResponse is the body of GET and PUT
// /config/night.session/{id}, and of GET
// /config/night.session/{id}/revisions/{revision}.
type nightSessionConfigResponse struct {
	ServerTime             time.Time    `json:"serverTime"`
	Kind                   string       `json:"kind"`
	ID                     string       `json:"id"`
	Revision               int64        `json:"revision"`
	Payload                nightSession `json:"payload"`
	UpdatedAt              time.Time    `json:"updatedAt"`
	CreatedByPrincipalID   *string      `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string      `json:"createdByPrincipalName"`
	Source                 string       `json:"source"`
}

// nightSessionActive is the "night.session.active" singleton
// configuration kind's decoded payload. Session is "" to mean explicitly
// unset.
type nightSessionActive struct {
	Session string `json:"session"`
}

// nightSessionActiveConfigResponse is the body of GET and PUT
// /config/night.session.active.
type nightSessionActiveConfigResponse struct {
	ServerTime             time.Time          `json:"serverTime"`
	Kind                   string             `json:"kind"`
	ID                     string             `json:"id"`
	Revision               int64              `json:"revision"`
	Payload                nightSessionActive `json:"payload"`
	UpdatedAt              time.Time          `json:"updatedAt"`
	CreatedByPrincipalID   *string            `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string            `json:"createdByPrincipalName"`
	Source                 string             `json:"source"`
}
