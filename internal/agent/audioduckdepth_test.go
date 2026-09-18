package agent

import (
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// validAudioSettingsParams is one audio.settings.configure params map
// with every required key present, so a case below can remove or spoil
// exactly one of them.
func validAudioSettingsParams() map[string]any {
	return map[string]any{
		"driftIgnoreThresholdMs":    float64(50),
		"defaultFadeCurve":          "linear",
		"defaultFadeDurationMs":     float64(500),
		"defaultMaxBackgroundGain":  0.8,
		"duckTargetGain":            0.2,
		"duckFadeDurationMs":        float64(150),
		"duckRestoreFadeDurationMs": float64(700),
		"ltcFrameRate":              "30",
		"ltcDefaultStartOffset":     "00:00:00:00",
		"revision":                  float64(7),
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

// TestDecodeAudioSettingsConfigMultisyncStartLeadMsIsOptional proves
// ADR-051 decision 1's own wire boundary rule: multisyncStartLeadMs is
// optional with no migration, so an existing coordinator that has never
// sent it must keep working (absent decodes with no error and no value),
// a negative one is refused, and a valid one decodes as given.
func TestDecodeAudioSettingsConfigMultisyncStartLeadMsIsOptional(t *testing.T) {
	absent := validAudioSettingsParams()
	p, err := decodeAudioSettingsConfig(absent)
	if err != nil {
		t.Fatalf("params with no multisyncStartLeadMs: unexpected error: %v", err)
	}
	if p.MultisyncStartLeadMs != nil {
		t.Fatalf("MultisyncStartLeadMs = %v, want nil (absent means no value pushed)", *p.MultisyncStartLeadMs)
	}
	if got := audioSettingsFromWire(p).MultisyncStartLeadMs; got != audio.DefaultSettings.MultisyncStartLeadMs {
		t.Fatalf("audioSettingsFromWire with an absent lead = %d, want the package default %d", got, audio.DefaultSettings.MultisyncStartLeadMs)
	}

	negative := validAudioSettingsParams()
	negative["multisyncStartLeadMs"] = float64(-1)
	if _, err := decodeAudioSettingsConfig(negative); err == nil {
		t.Fatal("multisyncStartLeadMs -1 was accepted, want it refused")
	}

	given := validAudioSettingsParams()
	given["multisyncStartLeadMs"] = float64(150)
	p, err = decodeAudioSettingsConfig(given)
	if err != nil {
		t.Fatalf("params with multisyncStartLeadMs=150: unexpected error: %v", err)
	}
	if p.MultisyncStartLeadMs == nil || *p.MultisyncStartLeadMs != 150 {
		t.Fatalf("MultisyncStartLeadMs = %v, want 150", p.MultisyncStartLeadMs)
	}
	if got := audioSettingsFromWire(p).MultisyncStartLeadMs; got != 150 {
		t.Fatalf("audioSettingsFromWire with lead=150 = %d, want 150", got)
	}
}
