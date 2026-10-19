package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func decodeWithEndOfNight(block string) (NightSessionPayload, *ValidationError) {
	raw := strings.Replace(validNightSessionJSON, `"enterResting": {`, `"enterEndOfNight": `+block+`, "enterResting": {`, 1)
	return DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
}

const goodnightCue = `{"name": "goodnight", "role": "projection", "action": "goodnight-clip", "offsetMs": 0}`

func TestDecodeNightSessionEndOfNight_OmittedRunsTheRestingCues(t *testing.T) {
	p := decodeValidNightSession(t)
	if p.EnterEndOfNight != nil {
		t.Fatalf("EnterEndOfNight = %+v, want nil when the block is omitted", p.EnterEndOfNight)
	}
	if p.EndOfNightMode() != NightSessionEndOfNightModeResting {
		t.Fatalf("mode = %q, want resting", p.EndOfNightMode())
	}
	if cues := p.EndOfNightCues(); len(cues) != 1 || cues[0].Name != "lighting-fade-in" {
		t.Fatalf("EndOfNightCues = %+v, want the enterResting cues", cues)
	}
	if got := p.EndOfNightBlackoutAfterShowMs(); got != 6000 {
		t.Fatalf("blackout = %d, want enterResting's 6000", got)
	}
}

func TestDecodeNightSessionEndOfNight_Modes(t *testing.T) {
	cases := []struct {
		name      string
		block     string
		wantCues  []string
		wantHold  int
		wantField string
	}{
		{"resting", `{"mode": "resting"}`, []string{"lighting-fade-in"}, 6000, ""},
		{"resting with an empty list from a read", `{"mode": "resting", "cues": []}`, []string{"lighting-fade-in"}, 6000, ""},
		{"none", `{"mode": "none"}`, nil, 6000, ""},
		{"none with own blackout", `{"mode": "none", "cues": [], "blackoutAfterShowMs": 0}`, nil, 0, ""},
		{"custom", `{"mode": "custom", "cues": [` + goodnightCue + `]}`, []string{"goodnight"}, 6000, ""},
		{"custom without cues", `{"mode": "custom", "cues": []}`, nil, 0, "enterEndOfNight.cues"},
		{"custom with cues missing", `{"mode": "custom"}`, nil, 0, "enterEndOfNight.cues"},
		{"none with cues", `{"mode": "none", "cues": [` + goodnightCue + `]}`, nil, 0, "enterEndOfNight.cues"},
		{"mode missing", `{"cues": []}`, nil, 0, "enterEndOfNight.mode"},
		{"unknown mode", `{"mode": "skip"}`, nil, 0, "enterEndOfNight.mode"},
		{"negative blackout", `{"mode": "none", "blackoutAfterShowMs": -1}`, nil, 0, "enterEndOfNight.blackoutAfterShowMs"},
		{"unknown key", `{"mode": "none", "playlist": "x"}`, nil, 0, "enterEndOfNight"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, verr := decodeWithEndOfNight(tc.block)
			if tc.wantField != "" {
				if verr == nil || verr.Field != tc.wantField {
					t.Fatalf("got %+v, want a rejection on %s", verr, tc.wantField)
				}
				return
			}
			if verr != nil {
				t.Fatalf("unexpected rejection: %+v", verr)
			}
			var names []string
			for _, c := range p.EndOfNightCues() {
				names = append(names, c.Name)
			}
			if strings.Join(names, ",") != strings.Join(tc.wantCues, ",") {
				t.Fatalf("EndOfNightCues = %v, want %v", names, tc.wantCues)
			}
			if got := p.EndOfNightBlackoutAfterShowMs(); got != tc.wantHold {
				t.Fatalf("blackout = %d, want %d", got, tc.wantHold)
			}
		})
	}
}

func TestDecodeNightSessionEndOfNight_RoundTrips(t *testing.T) {
	p, verr := decodeWithEndOfNight(`{"mode": "custom", "cues": [` + goodnightCue + `]}`)
	if verr != nil {
		t.Fatalf("decode: %+v", verr)
	}
	raw, err := EncodeNightSessionPayload(p)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var stored struct {
		EnterEndOfNight map[string]json.RawMessage `json:"enterEndOfNight"`
	}
	if err := json.Unmarshal([]byte(raw), &stored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, has := stored.EnterEndOfNight["blackoutAfterShowMs"]; has {
		t.Fatalf("an omitted end-of-night blackout was stored as a value: %s", raw)
	}
	back, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr != nil {
		t.Fatalf("re-decode: %+v", verr)
	}
	if back.EndOfNightMode() != NightSessionEndOfNightModeCustom || len(back.EndOfNightCues()) != 1 {
		t.Fatalf("round trip = %+v", back.EnterEndOfNight)
	}
}

func TestDecodeNightSessionEndOfNight_LightsFadeInOverlapsLightingCue(t *testing.T) {
	raw := strings.Replace(validNightSessionJSON, `"show": "halloween-2026",`, `"show": "halloween-2026", "lightsFadeInMs": 5000,`, 1)
	raw = strings.Replace(raw, `{"name": "lighting-fade-in", "role": "lighting", "action": "lighting-fade-in", "offsetMs": 0}`, ``, 1)
	raw = strings.Replace(raw, `"enterResting": {`, `"enterEndOfNight": {"mode": "custom", "cues": [{"name": "fade", "role": "lighting", "action": "a", "offsetMs": 0, "fadeDurationMs": 3000}]}, "enterResting": {`, 1)
	_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
	if verr == nil || verr.Field != "lightsFadeInMs" || !strings.Contains(verr.Detail, "enterEndOfNight") {
		t.Fatalf("got %+v, want the lights fade-in overlap named on enterEndOfNight", verr)
	}
}
