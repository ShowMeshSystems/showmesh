package config

import (
	"encoding/json"
	"testing"
)

func alwaysTrueShowExists(string) bool   { return true }
func alwaysTrueNodeDeclared(string) bool { return true }
func alwaysFalse(string) bool            { return false }

func validSurfaceJSON() string {
	return `{
		"show": "halloween-2026",
		"name": "Garage Door",
		"node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "ShowMesh Garage"}}
	}`
}

func TestDecodeShowSurfacePayloadValid(t *testing.T) {
	p, verr := DecodeShowSurfacePayload(validSurfaceJSON(), alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Show != "halloween-2026" || p.Node != "render-01" || p.Name != "Garage Door" {
		t.Fatalf("unexpected payload: %+v", p)
	}
	if p.ChannelRange.StartChannel != 1 || p.ChannelRange.ChannelCount != 3600 {
		t.Fatalf("unexpected channel range: %+v", p.ChannelRange)
	}
	if p.Geometry.Width != 40 || p.Geometry.Height != 30 || p.Geometry.PixelFormat != "rgb" {
		t.Fatalf("unexpected geometry: %+v", p.Geometry)
	}
	if p.FrameRate != 40 {
		t.Fatalf("unexpected frameRate: %d", p.FrameRate)
	}
	if p.Output.Transport != "ndi" || p.Output.NDI == nil || p.Output.NDI.SourceName != "ShowMesh Garage" || p.Output.HDMI != nil {
		t.Fatalf("unexpected output: %+v", p.Output)
	}
}

func TestEncodeShowSurfacePayloadRoundTrips(t *testing.T) {
	p, verr := DecodeShowSurfacePayload(validSurfaceJSON(), alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	raw, err := EncodeShowSurfacePayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(raw), &back); err != nil {
		t.Fatalf("re-decode: %v", err)
	}
	if back["node"] != "render-01" {
		t.Fatalf("node did not round trip: %v", back["node"])
	}
}

func TestDecodeShowSurfacePayloadHDMIValid(t *testing.T) {
	j := `{
		"show": "halloween-2026",
		"name": "Front Yard",
		"node": "render-02",
		"channelRange": {"startChannel": 1, "channelCount": 1600},
		"geometry": {"width": 20, "height": 20, "pixelFormat": "rgbw"},
		"frameRate": 30,
		"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1"}}
	}`
	p, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.Output.Transport != "hdmi" || p.Output.HDMI == nil || p.Output.HDMI.Display != "HDMI-1" || p.Output.NDI != nil {
		t.Fatalf("unexpected output: %+v", p.Output)
	}
}

// --- show reference ---

func TestDecodeShowSurfacePayloadShowUnknown(t *testing.T) {
	_, verr := DecodeShowSurfacePayload(validSurfaceJSON(), alwaysFalse, alwaysTrueNodeDeclared)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "show" {
		t.Fatalf("expected field-unknown-reference on show, got %+v", verr)
	}
}

// --- node reference ---

func TestDecodeShowSurfacePayloadNodeUnknown(t *testing.T) {
	_, verr := DecodeShowSurfacePayload(validSurfaceJSON(), alwaysTrueShowExists, alwaysFalse)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownReference || verr.Field != "node" {
		t.Fatalf("expected field-unknown-reference on node, got %+v", verr)
	}
}

// TestDecodeShowSurfacePayloadAllowsSecondSurfaceOnSameNode proves ADR-026's
// N=1 is a scope limit and never reaches this schema: nothing in
// DecodeShowSurfacePayload checks for a collision with any other stored
// surface, so two independent decodes naming the same node both succeed.
func TestDecodeShowSurfacePayloadAllowsSecondSurfaceOnSameNode(t *testing.T) {
	first, verr := DecodeShowSurfacePayload(validSurfaceJSON(), alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr != nil {
		t.Fatalf("unexpected error on first surface: %+v", verr)
	}
	second, verr := DecodeShowSurfacePayload(validSurfaceJSON(), alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr != nil {
		t.Fatalf("unexpected error on second surface targeting the same node: %+v", verr)
	}
	if first.Node != second.Node {
		t.Fatalf("test setup error: expected both surfaces to target the same node")
	}
}

// --- channelRange: absent / null / empty are three distinct refusals ---

func TestDecodeShowSurfacePayloadChannelRangeAbsent(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Code != ValidationCodeFieldRequired || verr.Field != "channelRange" {
		t.Fatalf("expected field-required on channelRange, got %+v", verr)
	}
}

func TestDecodeShowSurfacePayloadChannelRangeNull(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": null,
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Code != ValidationCodeFieldNull || verr.Field != "channelRange" {
		t.Fatalf("expected field-null on channelRange, got %+v", verr)
	}
}

func TestDecodeShowSurfacePayloadChannelCountZero(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 0},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "channelRange.channelCount" {
		t.Fatalf("expected a refusal naming channelRange.channelCount, got %+v", verr)
	}
}

// TestDecodeShowSurfacePayloadChannelRangeThreeDistinctRefusals confirms
// the three channelRange failure modes above produce three DIFFERENT
// (Code, Field) pairs, not the same generic error three times.
func TestDecodeShowSurfacePayloadChannelRangeThreeDistinctRefusals(t *testing.T) {
	base := func(channelRangeFragment string) string {
		return `{
			"show": "halloween-2026", "name": "x", "node": "render-01",` +
			channelRangeFragment + `
			"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
			"frameRate": 40,
			"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
		}`
	}
	_, absentErr := DecodeShowSurfacePayload(base(""), alwaysTrueShowExists, alwaysTrueNodeDeclared)
	_, nullErr := DecodeShowSurfacePayload(base(`"channelRange": null,`), alwaysTrueShowExists, alwaysTrueNodeDeclared)
	_, zeroErr := DecodeShowSurfacePayload(base(`"channelRange": {"startChannel":1,"channelCount":0},`), alwaysTrueShowExists, alwaysTrueNodeDeclared)

	if absentErr == nil || nullErr == nil || zeroErr == nil {
		t.Fatalf("expected all three to be refused: absent=%+v null=%+v zero=%+v", absentErr, nullErr, zeroErr)
	}
	if absentErr.Detail == nullErr.Detail || absentErr.Detail == zeroErr.Detail || nullErr.Detail == zeroErr.Detail {
		t.Fatalf("expected three distinct messages, got absent=%q null=%q zero=%q", absentErr.Detail, nullErr.Detail, zeroErr.Detail)
	}
}

func TestDecodeShowSurfacePayloadStartChannelZero(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 0, "channelCount": 100},
		"geometry": {"width": 10, "height": 10, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "channelRange.startChannel" {
		t.Fatalf("expected a refusal naming channelRange.startChannel, got %+v", verr)
	}
}

func TestDecodeShowSurfacePayloadChannelRangeExceedsMax(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 8388600, "channelCount": 100},
		"geometry": {"width": 10, "height": 10, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "channelRange" {
		t.Fatalf("expected a refusal naming channelRange, got %+v", verr)
	}
}

// --- geometry x channelCount must match exactly ---

func TestDecodeShowSurfacePayloadRGBChannelMathMismatch(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 100},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Code != ValidationCodeFieldInvalid || verr.Field != "geometry" {
		t.Fatalf("expected field-invalid on geometry, got %+v", verr)
	}
	if !containsAll(verr.Detail, "3600", "100") {
		t.Fatalf("expected message to name both numbers (3600 and 100), got %q", verr.Detail)
	}
}

func TestDecodeShowSurfacePayloadRGBWChannelMathMatches(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 1600},
		"geometry": {"width": 20, "height": 20, "pixelFormat": "rgbw"},
		"frameRate": 30,
		"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1"}}
	}`
	p, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	if p.ChannelRange.ChannelCount != 1600 {
		t.Fatalf("unexpected channel count: %d", p.ChannelRange.ChannelCount)
	}
}

func TestDecodeShowSurfacePayloadPixelFormatInvalid(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 100},
		"geometry": {"width": 10, "height": 10, "pixelFormat": "cmyk"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "geometry.pixelFormat" {
		t.Fatalf("expected a refusal naming geometry.pixelFormat, got %+v", verr)
	}
}

// --- frameRate ---

func TestDecodeShowSurfacePayloadFrameRateOutOfRange(t *testing.T) {
	for _, fr := range []int{0, -1, 121} {
		j := frameRateSurfaceJSON(fr)
		_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
		if verr == nil || verr.Field != "frameRate" {
			t.Fatalf("frameRate %d: expected a refusal naming frameRate, got %+v", fr, verr)
		}
	}
}

func frameRateSurfaceJSON(frameRate int) string {
	return `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": ` + itoa(frameRate) + `,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}}
	}`
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// --- output.transport ---

func TestDecodeShowSurfacePayloadTransportMismatchHDMIWithNDITransport(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}, "hdmi": {"display": "HDMI-1"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "output.hdmi" {
		t.Fatalf("expected a refusal naming output.hdmi, got %+v", verr)
	}
}

func TestDecodeShowSurfacePayloadTransportMismatchNDIWithHDMITransport(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1"}, "ndi": {"sourceName": "s"}}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "output.ndi" {
		t.Fatalf("expected a refusal naming output.ndi, got %+v", verr)
	}
}

func TestDecodeShowSurfacePayloadTransportInvalid(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "dmx"}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "output.transport" {
		t.Fatalf("expected a refusal naming output.transport, got %+v", verr)
	}
}

func TestDecodeShowSurfacePayloadNDIMissingWhenTransportNDI(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi"}
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "output.ndi" {
		t.Fatalf("expected a refusal naming output.ndi, got %+v", verr)
	}
}

// --- unknown top-level key / not an object ---

func TestDecodeShowSurfacePayloadUnknownTopLevelKey(t *testing.T) {
	j := `{
		"show": "halloween-2026", "name": "x", "node": "render-01",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "s"}},
		"extra": true
	}`
	_, verr := DecodeShowSurfacePayload(j, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Code != ValidationCodeFieldUnknownKey {
		t.Fatalf("expected field-unknown-key, got %+v", verr)
	}
}

func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// A typo inside a nested object is refused, not ignored. Without this an
// unrecognized key reads to the operator as a key that was applied.
func TestDecodeShowSurfacePayloadRejectsUnknownNestedKeys(t *testing.T) {
	cases := map[string]string{
		"channelRange": `{
			"show": "s", "name": "n", "node": "d",
			"channelRange": {"startChannel": 1, "channelCount": 3600, "startchannel": 2},
			"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
			"frameRate": 40,
			"output": {"transport": "ndi", "ndi": {"sourceName": "x"}}}`,
		"geometry": `{
			"show": "s", "name": "n", "node": "d",
			"channelRange": {"startChannel": 1, "channelCount": 3600},
			"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb", "depth": 1},
			"frameRate": 40,
			"output": {"transport": "ndi", "ndi": {"sourceName": "x"}}}`,
		"output": `{
			"show": "s", "name": "n", "node": "d",
			"channelRange": {"startChannel": 1, "channelCount": 3600},
			"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
			"frameRate": 40,
			"output": {"transport": "ndi", "ndi": {"sourceName": "x"}, "srt": {}}}`,
		"output.ndi": `{
			"show": "s", "name": "n", "node": "d",
			"channelRange": {"startChannel": 1, "channelCount": 3600},
			"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
			"frameRate": 40,
			"output": {"transport": "ndi", "ndi": {"sourceName": "x", "groups": "y"}}}`,
		"output.hdmi": `{
			"show": "s", "name": "n", "node": "d",
			"channelRange": {"startChannel": 1, "channelCount": 3600},
			"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
			"frameRate": 40,
			"output": {"transport": "hdmi", "hdmi": {"display": "HDMI-1", "edid": "x"}}}`,
	}
	for path, raw := range cases {
		t.Run(path, func(t *testing.T) {
			_, verr := DecodeShowSurfacePayload(raw, alwaysTrueShowExists, alwaysTrueNodeDeclared)
			if verr == nil {
				t.Fatalf("an unrecognized key under %s was accepted", path)
			}
			if verr.Code != ValidationCodeFieldUnknownKey {
				t.Fatalf("code = %q, want %q", verr.Code, ValidationCodeFieldUnknownKey)
			}
			if verr.Field != path {
				t.Fatalf("field = %q, want %q", verr.Field, path)
			}
		})
	}
}

// A width or height large enough to overflow the cross-field product is
// refused before the product is computed, so a nonsense payload cannot wrap
// to a value that happens to equal channelCount.
func TestDecodeShowSurfacePayloadBoundsGeometryDimensions(t *testing.T) {
	for _, tc := range []struct{ name, geometry string }{
		{"width", `{"width": 999999999999, "height": 1, "pixelFormat": "rgb"}`},
		{"height", `{"width": 1, "height": 999999999999, "pixelFormat": "rgb"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := `{
				"show": "s", "name": "n", "node": "d",
				"channelRange": {"startChannel": 1, "channelCount": 3600},
				"geometry": ` + tc.geometry + `,
				"frameRate": 40,
				"output": {"transport": "ndi", "ndi": {"sourceName": "x"}}}`
			_, verr := DecodeShowSurfacePayload(raw, alwaysTrueShowExists, alwaysTrueNodeDeclared)
			if verr == nil {
				t.Fatal("an out-of-range dimension was accepted")
			}
			if verr.Field != "geometry."+tc.name {
				t.Fatalf("field = %q, want geometry.%s", verr.Field, tc.name)
			}
		})
	}
}

func TestDecodeShowSurfacePayloadBoundsName(t *testing.T) {
	long := make([]rune, maxSurfaceNameRunes+1)
	for i := range long {
		long[i] = 'a'
	}
	raw := `{
		"show": "s", "name": "` + string(long) + `", "node": "d",
		"channelRange": {"startChannel": 1, "channelCount": 3600},
		"geometry": {"width": 40, "height": 30, "pixelFormat": "rgb"},
		"frameRate": 40,
		"output": {"transport": "ndi", "ndi": {"sourceName": "x"}}}`
	_, verr := DecodeShowSurfacePayload(raw, alwaysTrueShowExists, alwaysTrueNodeDeclared)
	if verr == nil || verr.Field != "name" {
		t.Fatalf("an over-long name was accepted or misreported: %+v", verr)
	}
}
