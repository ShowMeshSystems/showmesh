package config

import (
	"encoding/json"
	"fmt"

	"github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file is the engine-wide kind (ADR-039,
// IDENTIFIER-REGISTER.md's "audio.settings" reservation): the operator
// settings [pkg/audio]'s vocabulary itself carries no default for —
// [audio.FadeCurve] validates a curve name but does not pick one,
// [audio.Gain]/[audio.Ceiling] validate a magnitude but do not pick one.
// Singleton, mirroring rendersettings.go's shape exactly: one config
// object id, a well-defined default so GET never 404s, PUT is a full
// replacement.

const (
	// AudioSettingsConfigKind is config_objects.kind and
	// config_revisions.kind for this object, and the second path segment
	// of GET/PUT /api/v1/config/audio.settings.
	AudioSettingsConfigKind = "audio.settings"

	// AudioSettingsConfigObjectID is the single config_objects.id this
	// kind ever uses — one settings object per coordinator.
	AudioSettingsConfigObjectID = "default"

	// AudioSettingsSourceAPI is this kind's only config_revisions.source
	// value: no environment variable ever backed this object.
	AudioSettingsSourceAPI = "api"
)

// Bounds on audio.settings' numeric fields. Sanity bounds catching a typo (a
// duration entered in seconds instead of milliseconds, a gain entered as a
// percentage instead of a linear multiplier), not tuned ceilings — the
// drift threshold in particular has never been measured (see
// AudioSettingsDefaultPayload's own doc comment).
const (
	minDriftIgnoreThresholdMs = 0
	maxDriftIgnoreThresholdMs = 5000

	minFadeDurationMs = 1
	maxFadeDurationMs = 600000

	// maxDefaultMaxBackgroundGain bounds defaultMaxBackgroundGain at 4.0
	// (12 dB of amplification above unity) purely to catch a typo; it is
	// not a tuned headroom figure.
	maxDefaultMaxBackgroundGain = 4.0

	// maxDuckTargetGain is exclusive: a duck lowers a session, so unity
	// (or more) is not a duck at all. Zero remains expressible and means
	// the bed goes fully silent under an announcement.
	maxDuckTargetGain = 1.0
)

// AudioSettingsPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [AudioSettingsConfigKind].
type AudioSettingsPayload struct {
	// DriftIgnoreThresholdMs is how far a node's audio playback may drift
	// from its track-boundary correction point before ShowMesh treats it
	// as a problem (ADR-017: audio corrects discretely at track
	// boundaries, never by continuous rate manipulation). HYPOTHESIS, NOT
	// MEASURED — see AudioSettingsDefaultPayload.
	DriftIgnoreThresholdMs int `json:"driftIgnoreThresholdMs"`

	// DefaultFadeCurve is the fade shape a session uses when a macro step
	// or the audio engine does not name one explicitly. Validated against
	// [audio.FadeCurve]'s own closed vocabulary — only "linear" ships
	// there today, so this can never carry a curve the engine cannot
	// execute.
	DefaultFadeCurve string `json:"defaultFadeCurve"`

	// DefaultFadeDurationMs is the fade duration used the same way.
	DefaultFadeDurationMs int `json:"defaultFadeDurationMs"`

	// DefaultMaxBackgroundGain is the default [audio.Ceiling] applied to a
	// background bed, in the same linear-multiplier unit as
	// [audio.Gain] — 1.0 is unity gain.
	DefaultMaxBackgroundGain float64 `json:"defaultMaxBackgroundGain"`

	// DuckTargetGain is how far a node lowers a session while a
	// higher-priority session ducks it (an announcement over a resting
	// background bed), in the same linear-multiplier unit as
	// DefaultMaxBackgroundGain: 0.0 is silence, 1.0 would be no duck at
	// all and is refused. PROVISIONAL, NOT MEASURED — the shipped value
	// has never been heard on the installation's speakers (RES-007). A muted session is unaffected: mute silences
	// unconditionally.
	DuckTargetGain float64 `json:"duckTargetGain"`

	// LTCFrameRate is the closed vocabulary Resolume's timecode input
	// supports ([audio.LTCFrameRate]): 24, 25, 29.97, or 30. This ships
	// non-drop-frame at every rate — RES-001 §9 leaves Resolume's
	// drop-frame expectation at 29.97 unresearched, so this answers the
	// question explicitly rather than picking silently; see
	// [audio.LTCTimecode]'s doc comment for the recorded limitation.
	LTCFrameRate string `json:"ltcFrameRate"`

	// LTCDefaultStartOffset is the HH:MM:SS:FF timecode a session's LTC
	// generator starts from when the session's own audio.session.apply
	// carries no ltcStartOffset override — the generating half of
	// RES-001 §54's per-clip Offset convention.
	LTCDefaultStartOffset string `json:"ltcDefaultStartOffset"`
}

// AudioSettingsDefaultPayload is the value reported when nothing has ever
// been written. Every number here is a starting point, not a tuned value:
// the drift ignore threshold in particular has never been measured against
// real playback (RES-007 is the work queue), so 20ms is a guess labelled
// as one, not a result.
var AudioSettingsDefaultPayload = AudioSettingsPayload{
	DriftIgnoreThresholdMs:   20,
	DefaultFadeCurve:         string(audio.FadeCurveLinear),
	DefaultFadeDurationMs:    1000,
	DefaultMaxBackgroundGain: 0.6,
	DuckTargetGain:           0.25,
	LTCFrameRate:             string(audio.LTCFrameRate30),
	LTCDefaultStartOffset:    "00:00:00:00",
}

var audioSettingsTopLevelKeys = map[string]bool{
	"driftIgnoreThresholdMs": true, "defaultFadeCurve": true,
	"defaultFadeDurationMs": true, "defaultMaxBackgroundGain": true,
	"duckTargetGain": true, "ltcFrameRate": true, "ltcDefaultStartOffset": true,
}

// EncodeAudioSettingsPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeAudioSettingsPayload); this function does not re-validate.
func EncodeAudioSettingsPayload(p AudioSettingsPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode audio.settings payload: %w", err)
	}
	return string(b), nil
}

// DecodeAudioSettingsPayload parses and validates raw. PUT is a full
// replacement: every field is required on every write, so an absent key is
// refused by name rather than silently defaulted or carried forward from
// the previous revision — the "a config PUT with an absent key silently
// wiped a value" defect class, applied here by never treating "absent" as
// "keep the old value" or "use the default" for a required field. A reader
// of the STORED value (GET on an object nothing has ever configured) gets
// AudioSettingsDefaultPayload instead, a distinct code path
// (handleGetAudioSettingsConfig in package api), never this function.
func DecodeAudioSettingsPayload(raw string) (AudioSettingsPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, audioSettingsTopLevelKeys); verr != nil {
		return AudioSettingsPayload{}, verr
	}

	driftMs, verr := decodeRequiredInt(top, "driftIgnoreThresholdMs", "driftIgnoreThresholdMs")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if driftMs < minDriftIgnoreThresholdMs || driftMs > maxDriftIgnoreThresholdMs {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "driftIgnoreThresholdMs",
			Detail: fmt.Sprintf("driftIgnoreThresholdMs must be between %d and %d", minDriftIgnoreThresholdMs, maxDriftIgnoreThresholdMs),
		}
	}

	fadeCurve, verr := decodeRequiredString(top, "defaultFadeCurve", "defaultFadeCurve")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if err := audio.FadeCurve(fadeCurve).Validate(); err != nil {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "defaultFadeCurve",
			Detail: err.Error(),
		}
	}

	fadeDurationMs, verr := decodeRequiredInt(top, "defaultFadeDurationMs", "defaultFadeDurationMs")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if fadeDurationMs < minFadeDurationMs || fadeDurationMs > maxFadeDurationMs {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "defaultFadeDurationMs",
			Detail: fmt.Sprintf("defaultFadeDurationMs must be between %d and %d", minFadeDurationMs, maxFadeDurationMs),
		}
	}

	maxGain, verr := decodeRequiredFloat(top, "defaultMaxBackgroundGain", "defaultMaxBackgroundGain")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if err := audio.Ceiling(maxGain).Validate(); err != nil {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "defaultMaxBackgroundGain",
			Detail: err.Error(),
		}
	}
	if maxGain > maxDefaultMaxBackgroundGain {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "defaultMaxBackgroundGain",
			Detail: fmt.Sprintf("defaultMaxBackgroundGain must not exceed %v", maxDefaultMaxBackgroundGain),
		}
	}

	duckGain, verr := decodeRequiredFloat(top, "duckTargetGain", "duckTargetGain")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if err := audio.Gain(duckGain).Validate(); err != nil {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "duckTargetGain",
			Detail: err.Error(),
		}
	}
	if duckGain >= maxDuckTargetGain {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "duckTargetGain",
			Detail: fmt.Sprintf("duckTargetGain must be below %v: a gain of %v or more does not duck anything", maxDuckTargetGain, maxDuckTargetGain),
		}
	}

	ltcFrameRate, verr := decodeRequiredString(top, "ltcFrameRate", "ltcFrameRate")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if err := audio.LTCFrameRate(ltcFrameRate).Validate(); err != nil {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "ltcFrameRate",
			Detail: err.Error(),
		}
	}

	ltcOffset, verr := decodeRequiredString(top, "ltcDefaultStartOffset", "ltcDefaultStartOffset")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if err := audio.LTCTimecode(ltcOffset).Validate(); err != nil {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "ltcDefaultStartOffset",
			Detail: err.Error(),
		}
	}

	return AudioSettingsPayload{
		DriftIgnoreThresholdMs:   driftMs,
		DefaultFadeCurve:         fadeCurve,
		DefaultFadeDurationMs:    fadeDurationMs,
		DefaultMaxBackgroundGain: maxGain,
		DuckTargetGain:           duckGain,
		LTCFrameRate:             ltcFrameRate,
		LTCDefaultStartOffset:    ltcOffset,
	}, nil
}

// decodeRequiredFloat reads key from top as a required, non-null JSON
// number. [decodeRequiredInt]'s sibling for a field that is not
// constrained to a whole number.
func decodeRequiredFloat(top map[string]json.RawMessage, key, field string) (float64, *ValidationError) {
	raw, present := top[key]
	if !present {
		return 0, &ValidationError{Code: ValidationCodeFieldRequired, Field: field, Detail: fmt.Sprintf("%s is required", field)}
	}
	if isJSONNull(raw) {
		return 0, &ValidationError{Code: ValidationCodeFieldNull, Field: field, Detail: fmt.Sprintf("%s must not be null", field)}
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil {
		return 0, &ValidationError{Code: ValidationCodeFieldInvalid, Field: field, Detail: fmt.Sprintf("%s must be a JSON number", field)}
	}
	return f, nil
}
