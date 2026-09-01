package v1

// Wire types for the "show.cue" configuration kind (TRACK-H-H1-SPEC.md
// section 2). Follows showobjects.go's precedent: the wire layer never
// reuses internal/coordinator/config's Go structs directly (ADR-020).

// ConfigShowCueRenderOutput is show.cue.outputs.render.
type ConfigShowCueRenderOutput struct {
	Sequence string `json:"sequence"`
}

// ConfigShowCueAudioOutput is show.cue.outputs.audio. Target is ADR-045's
// optional target node, mirroring show.surface's "node" field: absent
// resolves later to the installation's single program+ltc audio.node.
type ConfigShowCueAudioOutput struct {
	Asset             string `json:"asset"`
	StartOffsetMillis int    `json:"startOffsetMillis"`
	Target            string `json:"target,omitempty"`
}

// ConfigShowCueLTCOutput is show.cue.outputs.ltc. Target is ADR-045's
// optional target node — see [ConfigShowCueAudioOutput.Target].
type ConfigShowCueLTCOutput struct {
	StartOffsetMillis int    `json:"startOffsetMillis"`
	Target            string `json:"target,omitempty"`
}

// ConfigShowCueAnnouncementOutput is show.cue.outputs.announcement.
// DuckGainDb is present only when Policy is "duck". Target is ADR-045's
// optional target node — see [ConfigShowCueAudioOutput.Target].
type ConfigShowCueAnnouncementOutput struct {
	Policy     string   `json:"policy"`
	DuckGainDb *float64 `json:"duckGainDb,omitempty"`
	FadeMillis int      `json:"fadeMillis"`
	Target     string   `json:"target,omitempty"`
}

// ConfigShowCueOutputs is show.cue.outputs. At least one member is
// non-nil on any valid payload.
type ConfigShowCueOutputs struct {
	Render       *ConfigShowCueRenderOutput       `json:"render,omitempty"`
	Audio        *ConfigShowCueAudioOutput        `json:"audio,omitempty"`
	LTC          *ConfigShowCueLTCOutput          `json:"ltc,omitempty"`
	Announcement *ConfigShowCueAnnouncementOutput `json:"announcement,omitempty"`
}

// ConfigShowCue is the "show.cue" configuration kind's decoded payload
// (TRACK-H-H1-SPEC.md section 2): the body PUT /config/show.cue/{id}
// accepts, and the "payload" member of GET /config/show.cue/{id}'s
// response.
type ConfigShowCue struct {
	Show    string               `json:"show"`
	Name    string               `json:"name"`
	Outputs ConfigShowCueOutputs `json:"outputs"`
}

// ShowCueConfigResponse is the body of GET and PUT /config/show.cue/{id}.
type ShowCueConfigResponse struct {
	ServerTime             string        `json:"serverTime"`
	Kind                   string        `json:"kind"`
	ID                     string        `json:"id"`
	Revision               int64         `json:"revision"`
	Payload                ConfigShowCue `json:"payload"`
	UpdatedAt              string        `json:"updatedAt"`
	CreatedByPrincipalID   *string       `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string       `json:"createdByPrincipalName"`
	Source                 string        `json:"source"`
}
