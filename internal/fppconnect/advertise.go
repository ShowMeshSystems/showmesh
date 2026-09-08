// Package fppconnect holds the values a ShowMesh render node advertises to
// xLights' FPP Connect dialog, shared between the MultiSync discover-ping
// responder and the node's HTTP compatibility listener so both surfaces
// agree on what the node claims to be.
package fppconnect

const (
	// AdvertisedVersion is the FPP version string the node advertises. It
	// clears the 7.1 eligibility gate and the 7.0 and 9.3 FSEQ gates while
	// staying below 10.0, per RES-003 section 10.5.
	AdvertisedVersion = "9.5.0"

	// AdvertisedVersionMajor is AdvertisedVersion's major component,
	// advertised as an explicit integer because xLights' IsVersionAtLeast
	// prefers it over the parsed string when both are present. RES-003
	// section 10.5.
	AdvertisedVersionMajor = 9

	// AdvertisedVersionMinor is AdvertisedVersion's minor component, for the
	// same reason as AdvertisedVersionMajor. RES-003 section 10.5.
	AdvertisedVersionMinor = 5

	// AdvertisedMode is the Mode value the HTTP seam's GET /api/system/info
	// serves. xLights renders the playlist widget, and defaults to sparse
	// FSEQ rendering, only for a target whose mode is "player" or "master";
	// "player" is the only one that gets both. RES-003 section 10.6. The
	// MultiSync ping's own mode byte does not carry this value: it stays
	// PingModeRemote so FPP's own unicast-sync targeting still selects this
	// node, per RES-003 section 10.2's supportsUnicast finding.
	AdvertisedMode = "player"
)
