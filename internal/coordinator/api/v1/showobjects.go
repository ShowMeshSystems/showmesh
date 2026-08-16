package v1

// Wire types for the show, show.surface, and show.active config kinds.
// Nothing here reuses internal/coordinator/config's Go structs directly
// (ADR-020: the wire layer is separate from the domain layer), matching
// showmacros.go's precedent.

// ConfigShow is the "show" kind's decoded payload: the body PUT
// /config/show/{id} accepts and GET returns. A PUT is a full replacement
// — an absent "notes" means notes becomes empty, never "leave it as is".
type ConfigShow struct {
	Name  string `json:"name"`
	Notes string `json:"notes"`
}

// ShowConfigResponse is the body of GET and PUT /config/show/{id}.
type ShowConfigResponse struct {
	ServerTime             string     `json:"serverTime"`
	Kind                   string     `json:"kind"`
	ID                     string     `json:"id"`
	Revision               int64      `json:"revision"`
	Payload                ConfigShow `json:"payload"`
	UpdatedAt              string     `json:"updatedAt"`
	CreatedByPrincipalID   *string    `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string    `json:"createdByPrincipalName"`
	Source                 string     `json:"source"`
}

// ConfigShowSurfaceChannelRange is show.surface.channelRange.
type ConfigShowSurfaceChannelRange struct {
	StartChannel int `json:"startChannel"`
	ChannelCount int `json:"channelCount"`
}

// ConfigShowSurfaceGeometry is show.surface.geometry.
type ConfigShowSurfaceGeometry struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelFormat string `json:"pixelFormat"`
}

// ConfigShowSurfaceNDIOutput is show.surface.output.ndi.
type ConfigShowSurfaceNDIOutput struct {
	SourceName string `json:"sourceName"`
}

// ConfigShowSurfaceHDMI is show.surface.output.hdmi.
type ConfigShowSurfaceHDMI struct {
	Display string `json:"display"`
}

// ConfigShowSurfaceOutput is show.surface.output. Exactly one of NDI/HDMI
// is populated, matching Transport (ADR-026: support for one transport is
// never evidence for the other).
type ConfigShowSurfaceOutput struct {
	Transport string                      `json:"transport"`
	NDI       *ConfigShowSurfaceNDIOutput `json:"ndi,omitempty"`
	HDMI      *ConfigShowSurfaceHDMI      `json:"hdmi,omitempty"`
}

// ConfigShowSurface is the "show.surface" configuration kind's decoded
// payload (TRACK-E-SESSION-SPEC.md section 2.2): the body PUT
// /config/show.surface/{id} accepts, and the "payload" member of GET
// /config/show.surface/{id}'s response. Every field is required on write —
// unlike ConfigShow, this payload has no optional/defaulted key, so a
// single shape serves both the read and write side.
type ConfigShowSurface struct {
	Show         string                        `json:"show"`
	Name         string                        `json:"name"`
	Node         string                        `json:"node"`
	ChannelRange ConfigShowSurfaceChannelRange `json:"channelRange"`
	Geometry     ConfigShowSurfaceGeometry     `json:"geometry"`
	FrameRate    int                           `json:"frameRate"`
	Output       ConfigShowSurfaceOutput       `json:"output"`
}

// ShowSurfaceConfigResponse is the body of GET and PUT
// /config/show.surface/{id}.
type ShowSurfaceConfigResponse struct {
	ServerTime             string            `json:"serverTime"`
	Kind                   string            `json:"kind"`
	ID                     string            `json:"id"`
	Revision               int64             `json:"revision"`
	Payload                ConfigShowSurface `json:"payload"`
	UpdatedAt              string            `json:"updatedAt"`
	CreatedByPrincipalID   *string           `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string           `json:"createdByPrincipalName"`
	Source                 string            `json:"source"`
}

// ConfigShowActive is the "show.active" singleton configuration kind's
// decoded payload (TRACK-E-SESSION-SPEC.md section 2.3): the body PUT
// /config/show.active accepts, and the "payload" member of GET
// /config/show.active's response.
type ConfigShowActive struct {
	Show string `json:"show"`
}

// ShowActiveConfigResponse is the body of GET and PUT /config/show.active.
type ShowActiveConfigResponse struct {
	ServerTime             string           `json:"serverTime"`
	Kind                   string           `json:"kind"`
	ID                     string           `json:"id"`
	Revision               int64            `json:"revision"`
	Payload                ConfigShowActive `json:"payload"`
	UpdatedAt              string           `json:"updatedAt"`
	CreatedByPrincipalID   *string          `json:"createdByPrincipalId"`
	CreatedByPrincipalName *string          `json:"createdByPrincipalName"`
	Source                 string           `json:"source"`
}
