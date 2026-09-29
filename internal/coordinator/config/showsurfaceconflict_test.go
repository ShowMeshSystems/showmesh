package config

import (
	"strings"
	"testing"
)

func conflictSurface(show, node string, start, count int, ndiName string) ShowSurfacePayload {
	out := ShowSurfaceOutput{Transport: ShowSurfaceTransportHDMI, HDMI: &ShowSurfaceHDMI{Display: "HDMI-1"}}
	if ndiName != "" {
		out = ShowSurfaceOutput{Transport: ShowSurfaceTransportNDI, NDI: &ShowSurfaceNDIOutput{SourceName: ndiName}}
	}
	return ShowSurfacePayload{
		Show: show, Name: "Matrix Left", Node: node,
		ChannelRange: ShowSurfaceChannelRange{StartChannel: start, ChannelCount: count},
		Output:       out,
	}
}

func TestCheckShowSurfaceConflict(t *testing.T) {
	existing := func(show, node string, start, count int, ndi string) []OtherShowSurface {
		return []OtherShowSurface{{ID: "left", Payload: conflictSurface(show, node, start, count, ndi)}}
	}
	tests := []struct {
		name      string
		id        string
		candidate ShowSurfacePayload
		others    []OtherShowSurface
		wantField string
		wantText  string
	}{
		{"no overlap", "right", conflictSurface("s", "n", 500, 100, "B"), existing("s", "n", 1, 100, "A"), "", ""},
		{"touching ranges pass", "right", conflictSurface("s", "n", 101, 100, "B"), existing("s", "n", 1, 100, "A"), "", ""},
		{"overlap at one channel", "right", conflictSurface("s", "n", 100, 100, "B"), existing("s", "n", 1, 100, "A"), "channelRange",
			`The surface "Matrix Left" already uses channels 1 to 100 on this node. Choose channels outside that range or move one surface to another node.`},
		{"candidate inside other", "right", conflictSurface("s", "n", 10, 5, "B"), existing("s", "n", 1, 100, "A"), "channelRange", ""},
		{"other inside candidate", "right", conflictSurface("s", "n", 1, 1000, "B"), existing("s", "n", 10, 5, "A"), "channelRange", ""},
		{"same ndi name", "right", conflictSurface("s", "n", 500, 100, "A"), existing("s", "n", 1, 100, "A"), "output.ndi.sourceName",
			`The NDI name "A" is already used by the surface "Matrix Left" on this node. Choose a different NDI name.`},
		{"ndi name differing by case conflicts", "right", conflictSurface("s", "n", 500, 100, "a"), existing("s", "n", 1, 100, "A"), "output.ndi.sourceName", ""},
		{"ndi name with trailing space conflicts", "right", conflictSurface("s", "n", 500, 100, "A "), existing("s", "n", 1, 100, "A"), "output.ndi.sourceName", ""},
		{"other above candidate", "left", conflictSurface("s", "n", 1, 4000, "B"), []OtherShowSurface{{ID: "right", Payload: conflictSurface("s", "n", 3601, 3600, "A")}}, "channelRange", ""},
		{"same values in another show pass", "right", conflictSurface("t", "n", 1, 100, "A"), existing("s", "n", 1, 100, "A"), "", ""},
		{"same values on another node pass", "right", conflictSurface("s", "m", 1, 100, "A"), existing("s", "n", 1, 100, "A"), "", ""},
		{"own id ignored on re-save", "left", conflictSurface("s", "n", 1, 100, "A"), existing("s", "n", 1, 100, "A"), "", ""},
		{"own id ignored when moving within node", "left", conflictSurface("s", "n", 50, 100, "A"), existing("s", "n", 1, 100, "A"), "", ""},
		{"hdmi ignores name rule", "right", conflictSurface("s", "n", 500, 100, ""), existing("s", "n", 1, 100, "A"), "", ""},
		{"hdmi on both sides", "right", conflictSurface("s", "n", 500, 100, ""), existing("s", "n", 1, 100, ""), "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			verr := CheckShowSurfaceConflict(tt.id, tt.candidate, tt.others)
			if tt.wantField == "" {
				if verr != nil {
					t.Fatalf("unexpected refusal: %+v", verr)
				}
				return
			}
			if verr == nil {
				t.Fatalf("want refusal on %s, got none", tt.wantField)
			}
			if verr.Code != ValidationCodeFieldInvalid || verr.Field != tt.wantField {
				t.Fatalf("got code %q field %q, want field %q", verr.Code, verr.Field, tt.wantField)
			}
			if tt.wantText != "" && verr.Detail != tt.wantText {
				t.Fatalf("detail = %q, want %q", verr.Detail, tt.wantText)
			}
			if strings.Contains(verr.Detail, "—") {
				t.Fatalf("detail contains an em-dash: %q", verr.Detail)
			}
		})
	}
}
