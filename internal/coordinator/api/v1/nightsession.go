package v1

import "encoding/json"

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
// configures background audio. MediaPlaylist and Items are mutually
// exclusive: MediaPlaylist names a media.playlist object whose own
// items/repeat/resume/gain/fade fields govern the bed; Items is the
// original inline form. FadeOutMs and FadeInMs are the show-boundary fade
// pair (config.NightSessionBackgroundAudio's own doc comment): both nil
// means an instant cut, unchanged from before the pair existed.
type ConfigNightSessionBackgroundAudio struct {
	MediaPlaylist  string
	Items          []ConfigNightSessionBackgroundAudioItem
	Repeat         string
	Resume         string
	ItemTransition string
	CrossfadeMs    *int
	MaxGainDb      float64
	FadeOutMs      *int
	FadeInMs       *int
}

// MarshalJSON emits the reference form ({"mediaPlaylist"} alone) or the
// inline form's original fixed shape - the same two mutually exclusive
// wire shapes config.NightSessionBackgroundAudio's own MarshalJSON emits,
// so an operator's GET response PUTs back unchanged either way.
func (b ConfigNightSessionBackgroundAudio) MarshalJSON() ([]byte, error) {
	if b.MediaPlaylist != "" {
		return json.Marshal(struct {
			MediaPlaylist string `json:"mediaPlaylist"`
		}{MediaPlaylist: b.MediaPlaylist})
	}
	return json.Marshal(struct {
		Items          []ConfigNightSessionBackgroundAudioItem `json:"items"`
		Repeat         string                                  `json:"repeat"`
		Resume         string                                  `json:"resume"`
		ItemTransition string                                  `json:"itemTransition"`
		CrossfadeMs    *int                                    `json:"crossfadeMs,omitempty"`
		MaxGainDb      float64                                 `json:"maxGainDb"`
		FadeOutMs      *int                                    `json:"fadeOutMs,omitempty"`
		FadeInMs       *int                                    `json:"fadeInMs,omitempty"`
	}{
		Items: b.Items, Repeat: b.Repeat, Resume: b.Resume, ItemTransition: b.ItemTransition,
		CrossfadeMs: b.CrossfadeMs, MaxGainDb: b.MaxGainDb, FadeOutMs: b.FadeOutMs, FadeInMs: b.FadeInMs,
	})
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
	Name               string  `json:"name"`
	Role               string  `json:"role"`
	Action             string  `json:"action"`
	OffsetMs           int     `json:"offsetMs"`
	FadeDurationMs     *int    `json:"fadeDurationMs,omitempty"`
	Barrier            bool    `json:"barrier"`
	OnFailure          string  `json:"onFailure"`
	AnnouncementPolicy *string `json:"announcementPolicy,omitempty"`
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

// ConfigNightSessionPowerBinding is a configured power binding under
// night.session.siteControl (RESTING-MODE.md §10.2, Track F seam F6).
type ConfigNightSessionPowerBinding struct {
	Action           string `json:"action"`
	PowerDomain      string `json:"powerDomain"`
	DomainProvenance string `json:"domainProvenance"`
}

// ConfigNightSessionPrerequisite is one entry of
// siteControl.presentationPowerOff.prerequisites.
type ConfigNightSessionPrerequisite struct {
	Kind                string `json:"kind"`
	Action              string `json:"action,omitempty"`
	RequireConfirmation bool   `json:"requireConfirmation,omitempty"`
	DelayMs             int    `json:"delayMs,omitempty"`
}

// ConfigNightSessionPresentationPowerOff is
// night.session.siteControl.presentationPowerOff.
type ConfigNightSessionPresentationPowerOff struct {
	Action                   string                           `json:"action"`
	PowerDomain              string                           `json:"powerDomain"`
	DomainProvenance         string                           `json:"domainProvenance"`
	RemovalPolicy            string                           `json:"removalPolicy"`
	ImmediateSafeAttestation bool                             `json:"immediateSafeAttestation,omitempty"`
	Prerequisites            []ConfigNightSessionPrerequisite `json:"prerequisites,omitempty"`
}

// ConfigNightSessionSiteControl is night.session.siteControl
// (RESTING-MODE.md §10.2/§10.4, Track F seam F6): entirely optional, and
// every field within it is independently optional too.
type ConfigNightSessionSiteControl struct {
	RequestThermalProfile string                                  `json:"requestThermalProfile,omitempty"`
	PresentationPowerOn   *ConfigNightSessionPowerBinding         `json:"presentationPowerOn,omitempty"`
	PresentationPowerOff  *ConfigNightSessionPresentationPowerOff `json:"presentationPowerOff,omitempty"`
}

// ConfigNightSessionInterlock is one entry of night.session.interlocks
// (RESTING-MODE.md §10.1, Track F seam F6). OnUnavailable and
// OverridePolicy are "" for posture "observe" or "disabled", matching
// config.NightInterlockRule's own precedent.
type ConfigNightSessionInterlock struct {
	Name             string `json:"name"`
	Phase            string `json:"phase"`
	Posture          string `json:"posture"`
	Signal           string `json:"signal,omitempty"`
	FreshnessSeconds *int   `json:"freshnessSeconds,omitempty"`
	FailureText      string `json:"failureText,omitempty"`
	OnUnavailable    string `json:"onUnavailable,omitempty"`
	OverridePolicy   string `json:"overridePolicy,omitempty"`
}

// ConfigNightSession is the "night.session" configuration kind's decoded
// payload: the body PUT /config/night.session/{id} accepts and GET
// returns. SiteControl and Interlocks are Track F seam F6's own optional
// blocks, nil/empty on a deployment that configures neither.
type ConfigNightSession struct {
	Show                      string                         `json:"show"`
	Label                     string                         `json:"label"`
	ShowPlaylist              ConfigNightSessionFPPPlaylist  `json:"showPlaylist"`
	Resting                   ConfigNightSessionResting      `json:"resting"`
	EnterShow                 ConfigNightSessionEnterShow    `json:"enterShow"`
	EnterResting              ConfigNightSessionEnterResting `json:"enterResting"`
	AnnouncementDefaultPolicy string                         `json:"announcementDefaultPolicy"`
	SiteControl               *ConfigNightSessionSiteControl `json:"siteControl,omitempty"`
	Interlocks                []ConfigNightSessionInterlock  `json:"interlocks,omitempty"`
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
