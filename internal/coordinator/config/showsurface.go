package config

import (
	"encoding/json"
	"fmt"
	"unicode/utf8"
)

// ShowSurfaceConfigKind is config_objects.kind and config_revisions.kind
// for a show.surface object (ADR-026 decision 1, ADR-027 decision 2). Like
// show.action, this is a collection: each object id is the surface's own
// identifier, chosen by the caller.
const ShowSurfaceConfigKind = "show.surface"

// The two members of show.surface.geometry.pixelFormat and the channels
// each contributes per pixel.
const (
	ShowSurfacePixelFormatRGB  = "rgb"
	ShowSurfacePixelFormatRGBW = "rgbw"
)

var showSurfacePixelFormatChannels = map[string]int{
	ShowSurfacePixelFormatRGB:  3,
	ShowSurfacePixelFormatRGBW: 4,
}

// The two members of show.surface.output.transport (ADR-026: NDI is the
// reference transport, HDMI a supported alternate — support for one is
// never evidence for the other, so nothing here defaults a transport).
const (
	ShowSurfaceTransportNDI  = "ndi"
	ShowSurfaceTransportHDMI = "hdmi"
)

var showSurfaceTransports = map[string]bool{
	ShowSurfaceTransportNDI:  true,
	ShowSurfaceTransportHDMI: true,
}

// maxSurfaceChannelNumber bounds startChannel+channelCount-1. This is a
// sanity bound this project has not verified against FPP's own real
// ceiling — it exists only to catch a typo'd channelCount, not to encode a
// protocol limit.
const maxSurfaceChannelNumber = 8388608

// maxSurfaceDimension bounds geometry.width and geometry.height. Without
// it, width*height*channelsPerPixel can overflow int64 and wrap to a value
// that happens to equal channelCount, which would pass the cross-field
// check below on a payload that is nonsense.
const maxSurfaceDimension = maxSurfaceChannelNumber

// maxSurfaceNameRunes bounds show.surface.name, matching maxShowNameRunes.
const maxSurfaceNameRunes = 200

// showSurfaceTopLevelKeys is the complete set of keys
// DecodeShowSurfacePayload recognizes at the top level of the request body.
var showSurfaceTopLevelKeys = map[string]bool{
	"show": true, "name": true, "node": true, "channelRange": true,
	"geometry": true, "frameRate": true, "output": true,
}

// The recognized key sets for each nested object. A typo inside a nested
// object is refused for the same reason a typo at the top level is: an
// ignored key reads as an applied one.
var (
	showSurfaceChannelRangeKeys = map[string]bool{"startChannel": true, "channelCount": true}
	showSurfaceGeometryKeys     = map[string]bool{"width": true, "height": true, "pixelFormat": true}
	showSurfaceOutputKeys       = map[string]bool{"transport": true, "ndi": true, "hdmi": true}
	showSurfaceNDIKeys          = map[string]bool{"sourceName": true}
	showSurfaceHDMIKeys         = map[string]bool{"display": true}
)

// rejectUnknownKeysUnder is rejectUnknownTopLevelKeys for a nested object,
// naming the path so the operator knows which object the typo is in.
func rejectUnknownKeysUnder(fields map[string]json.RawMessage, known map[string]bool, path string) *ValidationError {
	verr := rejectUnknownTopLevelKeys(fields, known)
	if verr == nil {
		return nil
	}
	verr.Field = path
	verr.Detail = path + ": " + verr.Detail
	return verr
}

// ShowSurfacePayload is config_revisions.payload_json's decoded, VALIDATED
// shape for [ShowSurfaceConfigKind]. A second surface assigned to the same
// node is a valid payload on its own terms — ADR-026's N=1 is a scope
// limit on the renderer, not a schema rule, and nothing in this file
// checks for a collision with any other stored surface.
type ShowSurfacePayload struct {
	Show         string                  `json:"show"`
	Name         string                  `json:"name"`
	Node         string                  `json:"node"`
	ChannelRange ShowSurfaceChannelRange `json:"channelRange"`
	Geometry     ShowSurfaceGeometry     `json:"geometry"`
	// FrameRate carries no default. ADR-026's day-0 profile of one surface
	// at 40 fps over NDI on OptiPlex 7040-class hardware is L0 design
	// intent, a target to validate, not a supported profile.
	FrameRate int               `json:"frameRate"`
	Output    ShowSurfaceOutput `json:"output"`
}

// ShowSurfaceChannelRange is show.surface.channelRange. An empty range
// (channelCount 0) is refused at write time: RES-003 found that an empty
// xLights channelRanges makes it render a full, non-sparse FSEQ, which is
// the gigabytes-per-song case ADR-028's asset store exists to avoid.
type ShowSurfaceChannelRange struct {
	StartChannel int `json:"startChannel"`
	ChannelCount int `json:"channelCount"`
}

// ShowSurfaceGeometry is show.surface.geometry. Width*Height*channels-per-
// pixel(PixelFormat) must equal the sibling ChannelRange.ChannelCount
// exactly — checked in DecodeShowSurfacePayload, since it is a rule across
// two sibling objects rather than a property of either alone.
type ShowSurfaceGeometry struct {
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	PixelFormat string `json:"pixelFormat"`
}

// ShowSurfaceOutput is show.surface.output. Exactly one of NDI/HDMI is
// populated, matching Transport; the other is nil.
type ShowSurfaceOutput struct {
	Transport string                `json:"transport"`
	NDI       *ShowSurfaceNDIOutput `json:"ndi,omitempty"`
	HDMI      *ShowSurfaceHDMI      `json:"hdmi,omitempty"`
}

// ShowSurfaceNDIOutput is show.surface.output.ndi.
type ShowSurfaceNDIOutput struct {
	SourceName string `json:"sourceName"`
}

// ShowSurfaceHDMI is show.surface.output.hdmi. Display names which local
// display the node drives. Nothing consumes this yet: Track B's renderer is
// the first reader, and it may need a different shape.
type ShowSurfaceHDMI struct {
	Display string `json:"display"`
}

// EncodeShowSurfacePayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeShowSurfacePayload); this function does not re-validate.
func EncodeShowSurfacePayload(p ShowSurfacePayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode show.surface payload: %w", err)
	}
	return string(b), nil
}

// DecodeShowSurfacePayload parses and validates raw. showExists reports
// whether a "show" reference names an existing show config object;
// nodeDeclared reports whether a "node" reference names a declared node.
// Neither is fetched by this package — both are caller-supplied, matching
// showaction.go's endpoints/brokers/registry parameter shape, because this
// package has no store access.
//
// This function deliberately never consults a node's advertised
// capabilities to decide whether it can drive output.Transport.
// Advertisement is observed state and is absent whenever a node is
// offline; refusing a configuration write on absent advertisement would
// manufacture absence from a node that may simply not have checked in yet.
func DecodeShowSurfacePayload(raw string, showExists func(string) bool, nodeDeclared func(string) bool) (ShowSurfacePayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, showSurfaceTopLevelKeys); verr != nil {
		return ShowSurfacePayload{}, verr
	}

	show, verr := decodeRequiredString(top, "show", "show")
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}
	if verr := validateShowRef(show); verr != nil {
		return ShowSurfacePayload{}, verr
	}
	if !showExists(show) {
		return ShowSurfacePayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "show",
			Detail: fmt.Sprintf("show %q is not a configured show", show),
		}
	}

	name, verr := decodeRequiredString(top, "name", "name")
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}
	if utf8.RuneCountInString(name) > maxSurfaceNameRunes {
		return ShowSurfacePayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "name",
			Detail: fmt.Sprintf("name must be %d characters or fewer", maxSurfaceNameRunes),
		}
	}

	node, verr := decodeRequiredString(top, "node", "node")
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}
	if !nodeDeclared(node) {
		return ShowSurfacePayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "node",
			Detail: fmt.Sprintf("node %q is not a declared node", node),
		}
	}

	channelRange, verr := decodeShowSurfaceChannelRange(top)
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}

	geometry, verr := decodeShowSurfaceGeometry(top)
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}

	channelsPerPixel := showSurfacePixelFormatChannels[geometry.PixelFormat]
	computed := geometry.Width * geometry.Height * channelsPerPixel
	if computed != channelRange.ChannelCount {
		return ShowSurfacePayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "geometry",
			Detail: fmt.Sprintf(
				"geometry %dx%d at %d channels per pixel (%s) computes to %d channels, which does not equal channelRange.channelCount %d",
				geometry.Width, geometry.Height, channelsPerPixel, geometry.PixelFormat, computed, channelRange.ChannelCount),
		}
	}

	frameRate, verr := decodeRequiredInt(top, "frameRate", "frameRate")
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}
	if frameRate < 1 || frameRate > 120 {
		return ShowSurfacePayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "frameRate",
			Detail: "frameRate must be between 1 and 120",
		}
	}

	output, verr := decodeShowSurfaceOutput(top)
	if verr != nil {
		return ShowSurfacePayload{}, verr
	}

	return ShowSurfacePayload{
		Show: show, Name: name, Node: node,
		ChannelRange: channelRange, Geometry: geometry,
		FrameRate: frameRate, Output: output,
	}, nil
}

// decodeShowSurfaceChannelRange decodes and validates the required
// "channelRange" field. Absent, explicit null, and an explicitly empty
// range ({"startChannel":1,"channelCount":0}) are three distinct refusals
// with three distinct messages — see decodeRequiredObject for the first
// two and this function's own channelCount check for the third.
func decodeShowSurfaceChannelRange(top map[string]json.RawMessage) (ShowSurfaceChannelRange, *ValidationError) {
	fields, verr := decodeRequiredObject(top, "channelRange", "channelRange")
	if verr != nil {
		return ShowSurfaceChannelRange{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showSurfaceChannelRangeKeys, "channelRange"); verr != nil {
		return ShowSurfaceChannelRange{}, verr
	}

	startChannel, verr := decodeRequiredInt(fields, "startChannel", "channelRange.startChannel")
	if verr != nil {
		return ShowSurfaceChannelRange{}, verr
	}
	if startChannel < 1 {
		return ShowSurfaceChannelRange{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "channelRange.startChannel",
			Detail: "startChannel must be at least 1",
		}
	}

	channelCount, verr := decodeRequiredInt(fields, "channelCount", "channelRange.channelCount")
	if verr != nil {
		return ShowSurfaceChannelRange{}, verr
	}
	if channelCount < 1 {
		return ShowSurfaceChannelRange{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "channelRange.channelCount",
			Detail: "channelCount must be at least 1; a zero-length channel range is refused rather than silently accepted",
		}
	}

	if startChannel+channelCount-1 > maxSurfaceChannelNumber {
		return ShowSurfaceChannelRange{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "channelRange",
			Detail: fmt.Sprintf(
				"channelRange startChannel %d plus channelCount %d exceeds the maximum channel number %d",
				startChannel, channelCount, maxSurfaceChannelNumber),
		}
	}

	return ShowSurfaceChannelRange{StartChannel: startChannel, ChannelCount: channelCount}, nil
}

// decodeShowSurfaceGeometry decodes and validates the required "geometry"
// field, including that pixelFormat is a member of
// showSurfacePixelFormatChannels. It does not check the cross-field
// channel-count rule; DecodeShowSurfacePayload does that once both
// geometry and channelRange are decoded.
func decodeShowSurfaceGeometry(top map[string]json.RawMessage) (ShowSurfaceGeometry, *ValidationError) {
	fields, verr := decodeRequiredObject(top, "geometry", "geometry")
	if verr != nil {
		return ShowSurfaceGeometry{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showSurfaceGeometryKeys, "geometry"); verr != nil {
		return ShowSurfaceGeometry{}, verr
	}

	width, verr := decodeRequiredInt(fields, "width", "geometry.width")
	if verr != nil {
		return ShowSurfaceGeometry{}, verr
	}
	if width < 1 || width > maxSurfaceDimension {
		return ShowSurfaceGeometry{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "geometry.width",
			Detail: fmt.Sprintf("width must be between 1 and %d", maxSurfaceDimension),
		}
	}

	height, verr := decodeRequiredInt(fields, "height", "geometry.height")
	if verr != nil {
		return ShowSurfaceGeometry{}, verr
	}
	if height < 1 || height > maxSurfaceDimension {
		return ShowSurfaceGeometry{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "geometry.height",
			Detail: fmt.Sprintf("height must be between 1 and %d", maxSurfaceDimension),
		}
	}

	pixelFormat, verr := decodeRequiredString(fields, "pixelFormat", "geometry.pixelFormat")
	if verr != nil {
		return ShowSurfaceGeometry{}, verr
	}
	if _, ok := showSurfacePixelFormatChannels[pixelFormat]; !ok {
		return ShowSurfaceGeometry{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "geometry.pixelFormat",
			Detail: "pixelFormat must be one of rgb or rgbw",
		}
	}

	return ShowSurfaceGeometry{Width: width, Height: height, PixelFormat: pixelFormat}, nil
}

// decodeShowSurfaceOutput decodes and validates the required "output"
// field. Exactly one of "ndi"/"hdmi" must be present, and it must be the
// one named by "transport" — support for one transport is never evidence
// for the other, so this never defaults or infers a transport from
// whichever sub-object happens to be present.
func decodeShowSurfaceOutput(top map[string]json.RawMessage) (ShowSurfaceOutput, *ValidationError) {
	fields, verr := decodeRequiredObject(top, "output", "output")
	if verr != nil {
		return ShowSurfaceOutput{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, showSurfaceOutputKeys, "output"); verr != nil {
		return ShowSurfaceOutput{}, verr
	}

	transport, verr := decodeRequiredString(fields, "transport", "output.transport")
	if verr != nil {
		return ShowSurfaceOutput{}, verr
	}
	if !showSurfaceTransports[transport] {
		return ShowSurfaceOutput{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "output.transport",
			Detail: "transport must be one of ndi or hdmi",
		}
	}

	_, hasNDI := fields["ndi"]
	_, hasHDMI := fields["hdmi"]

	switch transport {
	case ShowSurfaceTransportNDI:
		if hasHDMI {
			return ShowSurfaceOutput{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "output.hdmi",
				Detail: "output.hdmi must not be present when transport is \"ndi\"",
			}
		}
		ndiFields, verr := decodeRequiredObject(fields, "ndi", "output.ndi")
		if verr != nil {
			return ShowSurfaceOutput{}, verr
		}
		if verr := rejectUnknownKeysUnder(ndiFields, showSurfaceNDIKeys, "output.ndi"); verr != nil {
			return ShowSurfaceOutput{}, verr
		}
		sourceName, verr := decodeRequiredString(ndiFields, "sourceName", "output.ndi.sourceName")
		if verr != nil {
			return ShowSurfaceOutput{}, verr
		}
		return ShowSurfaceOutput{Transport: transport, NDI: &ShowSurfaceNDIOutput{SourceName: sourceName}}, nil

	case ShowSurfaceTransportHDMI:
		if hasNDI {
			return ShowSurfaceOutput{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: "output.ndi",
				Detail: "output.ndi must not be present when transport is \"hdmi\"",
			}
		}
		hdmiFields, verr := decodeRequiredObject(fields, "hdmi", "output.hdmi")
		if verr != nil {
			return ShowSurfaceOutput{}, verr
		}
		if verr := rejectUnknownKeysUnder(hdmiFields, showSurfaceHDMIKeys, "output.hdmi"); verr != nil {
			return ShowSurfaceOutput{}, verr
		}
		display, verr := decodeRequiredString(hdmiFields, "display", "output.hdmi.display")
		if verr != nil {
			return ShowSurfaceOutput{}, verr
		}
		return ShowSurfaceOutput{Transport: transport, HDMI: &ShowSurfaceHDMI{Display: display}}, nil
	}

	// Unreachable: transport already validated against showSurfaceTransports.
	return ShowSurfaceOutput{}, &ValidationError{
		Code: ValidationCodeFieldInvalid, Field: "output.transport",
		Detail: "transport must be one of ndi or hdmi",
	}
}
