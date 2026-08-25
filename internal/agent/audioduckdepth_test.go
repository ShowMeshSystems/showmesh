package agent

import (
	"testing"

	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// validAudioSettingsParams is one audio.settings.configure params map
// with every required key present, so a case below can remove or spoil
// exactly one of them.
func validAudioSettingsParams() map[string]any {
	return map[string]any{
		"driftIgnoreThresholdMs":   float64(50),
		"defaultFadeCurve":         "linear",
		"defaultFadeDurationMs":    float64(500),
		"defaultMaxBackgroundGain": 0.8,
		"duckTargetGain":           0.2,
		"ltcFrameRate":             "30",
		"ltcDefaultStartOffset":    "00:00:00:00",
		"revision":                 float64(7),
	}
}

// mutation target: decodeAudioSettingsConfig's duckTargetGain entries in
// the known-keys map, the required-field list, and its range check.
// The node decodes the duck depth at its own wire boundary rather than
// trusting the coordinator's validation.
func TestDecodeAudioSettingsConfigValidatesDuckTargetGain(t *testing.T) {
	params := validAudioSettingsParams()
	p, err := decodeAudioSettingsConfig(params)
	if err != nil {
		t.Fatalf("valid params: unexpected error: %v", err)
	}
	if p.DuckTargetGain != 0.2 {
		t.Fatalf("DuckTargetGain = %v, want 0.2", p.DuckTargetGain)
	}

	absent := validAudioSettingsParams()
	delete(absent, "duckTargetGain")
	if _, err := decodeAudioSettingsConfig(absent); err == nil {
		t.Fatal("an absent duckTargetGain was accepted, want it refused by name")
	}

	for _, bad := range []float64{1, 1.5, -0.1} {
		spoiled := validAudioSettingsParams()
		spoiled["duckTargetGain"] = bad
		if _, err := decodeAudioSettingsConfig(spoiled); err == nil {
			t.Fatalf("duckTargetGain %v was accepted, want it refused", bad)
		}
	}

	// A silent duck is a legitimate operator choice, not an error.
	silent := validAudioSettingsParams()
	silent["duckTargetGain"] = 0.0
	if _, err := decodeAudioSettingsConfig(silent); err != nil {
		t.Fatalf("duckTargetGain 0 refused: %v", err)
	}
}

// mutation target: audioSettingsFromWire's DuckTargetGain assignment.
// Without it the operator's configured depth never reaches the session
// logic that reads it.
func TestAudioSettingsFromWireCarriesDuckTargetGain(t *testing.T) {
	p, err := decodeAudioSettingsConfig(validAudioSettingsParams())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := audioSettingsFromWire(p).DuckTargetGain; got != pkgaudio.Gain(0.2) {
		t.Fatalf("DuckTargetGain reaching audio.Settings = %v, want 0.2", got)
	}
}
