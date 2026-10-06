package config

import (
	"strings"
	"testing"
)

func decodeWithLightsFade(t *testing.T, extra string) (NightSessionPayload, *ValidationError) {
	t.Helper()
	raw := strings.Replace(validNightSessionJSON, `"enterShow"`, extra+`"enterShow"`, 1)
	return DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
}

func TestNightSessionLightsFadesAreOptionalAndIndependent(t *testing.T) {
	p, verr := decodeWithLightsFade(t, "")
	if verr != nil || p.LightsFadeOutMs != nil || p.LightsFadeInMs != nil {
		t.Fatalf("omitted fades must stay nil: %+v %+v", p, verr)
	}
	p, verr = decodeWithLightsFade(t, `"lightsFadeOutMs": 20000, `)
	if verr != nil || p.LightsFadeOutMs == nil || *p.LightsFadeOutMs != 20000 || p.LightsFadeInMs != nil {
		t.Fatalf("fade-out alone: %+v %+v", p, verr)
	}
	p, verr = decodeWithLightsFade(t, `"lightsFadeInMs": 8000, `)
	if verr != nil || p.LightsFadeInMs == nil || *p.LightsFadeInMs != 8000 || p.LightsFadeOutMs != nil {
		t.Fatalf("fade-in alone: %+v %+v", p, verr)
	}
}

func TestNightSessionLightsFadesRoundTrip(t *testing.T) {
	p, verr := decodeWithLightsFade(t, `"lightsFadeOutMs": 20000, "lightsFadeInMs": 8000, `)
	if verr != nil {
		t.Fatalf("%+v", verr)
	}
	raw, err := EncodeNightSessionPayload(p)
	if err != nil || !strings.Contains(raw, `"lightsFadeOutMs":20000`) || !strings.Contains(raw, `"lightsFadeInMs":8000`) {
		t.Fatalf("encoded payload lost a fade: %s (%v)", raw, err)
	}
	plain := decodeValidNightSession(t)
	raw, _ = EncodeNightSessionPayload(plain)
	if strings.Contains(raw, "lightsFade") {
		t.Fatalf("an unset fade must not appear on the wire: %s", raw)
	}
}

func TestNightSessionLightsFadesRejectValuesTheFadeCannotUse(t *testing.T) {
	for _, tc := range []struct{ name, extra, field string }{
		{"zero out", `"lightsFadeOutMs": 0, `, "lightsFadeOutMs"},
		{"negative in", `"lightsFadeInMs": -1, `, "lightsFadeInMs"},
		{"too long", `"lightsFadeOutMs": 86400001, `, "lightsFadeOutMs"},
		{"not a number", `"lightsFadeInMs": "soon", `, "lightsFadeInMs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, verr := decodeWithLightsFade(t, tc.extra)
			if verr == nil || verr.Field != tc.field {
				t.Fatalf("expected a rejection naming %s, got %+v", tc.field, verr)
			}
		})
	}
}

func TestNightSessionLightsFadesRefuseUnderOneSecond(t *testing.T) {
	_, verr := decodeWithLightsFade(t, `"lightsFadeOutMs": 499, `)
	if verr == nil || verr.Field != "lightsFadeOutMs" || !strings.Contains(verr.Detail, "at least 1000") {
		t.Fatalf("a sub-second fade must be refused with its minimum named: %+v", verr)
	}
	if _, verr := decodeWithLightsFade(t, `"lightsFadeInMs": 1000, `); verr != nil {
		t.Fatalf("one second is the shortest allowed fade: %+v", verr)
	}
}

func TestNightSessionLightsFadeRefusesALightingCueFadingTheSameWay(t *testing.T) {
	for _, tc := range []struct{ name, phase, field, extra string }{
		{"enterShow", "enterShow", "lightsFadeOutMs", `"lightsFadeOutMs": 5000, `},
		{"enterResting", "enterResting", "lightsFadeInMs", `"lightsFadeInMs": 5000, `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := validNightSessionJSON
			raw = strings.Replace(raw, `"enterShow"`, tc.extra+`"enterShow"`, 1)
			raw = strings.ReplaceAll(raw, `"role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "barrier": true`, `"role": "lighting", "action": "lighting-fade-out", "offsetMs": -20000, "fadeDurationMs": 3000, "barrier": true`)
			raw = strings.ReplaceAll(raw, `"role": "lighting", "action": "lighting-fade-in", "offsetMs": 0`, `"role": "lighting", "action": "lighting-fade-in", "offsetMs": 0, "fadeDurationMs": 3000`)
			_, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil)
			if verr == nil || verr.Field != tc.field || !strings.Contains(verr.Detail, "fadeDurationMs") || !strings.Contains(verr.Detail, tc.field) {
				t.Fatalf("expected a refusal naming both settings, got %+v", verr)
			}
			// Either setting alone keeps working.
			raw = strings.Replace(raw, tc.extra, "", 1)
			if _, verr := DecodeNightSessionPayload(raw, nightSessionTestEndpoints, alwaysTrueAssetCurrent, alwaysTrueActionResolver, alwaysTrueInterlockSignalResolver, alwaysTrueMediaPlaylistCurrent, alwaysTrueAudioNodeExists, nil); verr != nil {
				t.Fatalf("the cue fade alone must still decode: %+v", verr)
			}
		})
	}
}
