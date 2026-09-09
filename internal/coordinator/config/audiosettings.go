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
// percentage instead of decibels), not tuned ceilings. The
// drift threshold in particular has never been measured (see
// AudioSettingsDefaultPayload's own doc comment).
const (
	minDriftIgnoreThresholdMs = 0
	maxDriftIgnoreThresholdMs = 5000

	minFadeDurationMs = 1
	maxFadeDurationMs = 600000

	// maxDefaultMaxBackgroundGainDb is [audio.MaxOperatorGainDb], the
	// typo guard every operator-facing gain shares.
	maxDefaultMaxBackgroundGainDb = audio.MaxOperatorGainDb

	// minDefaultMaxBackgroundGainDb is audio.SilenceFloorDb, the same
	// floor duckTargetGainDb already bounds against: without it an
	// operator reaching for the same number as the duck field gets a
	// ceiling low enough that every background bed goes inaudible.
	minDefaultMaxBackgroundGainDb = audio.SilenceFloorDb

	// maxDuckTargetGainDb is exclusive: a duck lowers a session, so unity
	// (0 dB) or louder is not a duck at all. The silence floor remains
	// expressible and means the bed goes fully silent under an
	// announcement.
	maxDuckTargetGainDb = 0.0

	// minDuckTargetGainDb is audio.SilenceFloorDb, the same floor
	// show.cue's outputs.announcement duckGainDb already bounds against.
	minDuckTargetGainDb = audio.SilenceFloorDb
)

// Bounds on scheduledStartDeliveryBoundMs and scheduledStartMarginMs.
// Typo guards only, not tuned ceilings: zero is admissible for both (an
// operator who has measured a negligible path, or who wants no pad at
// all, may say so), and the ceiling is a minute, far past any plausible
// value, so a duration typed in microseconds is caught while a genuinely
// slow path is not refused.
const (
	minScheduledStartOffsetMs = 0
	maxScheduledStartOffsetMs = 60000
)

// Bounds on duckFadeDurationMs and duckRestoreFadeDurationMs share
// defaultFadeDurationMs's own typo-guard range: neither is a fade a
// node can ever run outside it either.
const (
	minDuckFadeDurationMs = minFadeDurationMs
	maxDuckFadeDurationMs = maxFadeDurationMs
)

// audioSettingsRenamedGainFields maps every pre-dB field name to its
// replacement. A payload still carrying an old name is refused by name
// rather than reinterpreted: the numbers meant a linear multiplier and
// mean decibels now, so silently accepting 0.5 would turn a halving into
// a barely audible half-decibel lift.
var audioSettingsRenamedGainFields = map[string]string{
	"defaultMaxBackgroundGain": "defaultMaxBackgroundGainDb",
	"duckTargetGain":           "duckTargetGainDb",
}

// AudioSettingsPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [AudioSettingsConfigKind].
type AudioSettingsPayload struct {
	// DriftIgnoreThresholdMs is how far a node's audio playback may drift
	// from its track-boundary correction point before ShowMesh treats it
	// as a problem (ADR-017: audio corrects discretely at track
	// boundaries, never by continuous rate manipulation). Its default is
	// now derived from a measurement rather than guessed: see
	// AudioSettingsDefaultPayload.
	DriftIgnoreThresholdMs int `json:"driftIgnoreThresholdMs"`

	// DefaultFadeCurve is the fade shape a session uses when a macro step
	// or the audio engine does not name one explicitly. Validated against
	// [audio.FadeCurve]'s own closed vocabulary — only "linear" ships
	// there today, so this can never carry a curve the engine cannot
	// execute.
	DefaultFadeCurve string `json:"defaultFadeCurve"`

	// DefaultFadeDurationMs is the fade duration used the same way.
	DefaultFadeDurationMs int `json:"defaultFadeDurationMs"`

	// DefaultMaxBackgroundGainDb is the default ceiling applied to a
	// background bed, in decibels: 0 dB is unity gain, and the
	// coordinator converts it to [audio.Ceiling]'s linear multiplier once,
	// at its own boundary, before anything reaches a node.
	DefaultMaxBackgroundGainDb float64 `json:"defaultMaxBackgroundGainDb"`

	// DuckTargetGainDb is how far a node lowers a session while a
	// higher-priority session ducks it (an announcement over a resting
	// background bed), in decibels: it must be negative (0 dB would be no
	// duck at all) and at or above [audio.SilenceFloorDb], where the bed
	// goes fully silent. PROVISIONAL, NOT MEASURED: the shipped value
	// has never been heard on the installation's speakers (RES-007). A muted session is unaffected: mute silences
	// unconditionally.
	DuckTargetGainDb float64 `json:"duckTargetGainDb"`

	// DuckFadeDurationMs is how long a session takes to fade DOWN to
	// DuckTargetGainDb once a higher-priority session starts ducking it.
	// Shorter than DuckRestoreFadeDurationMs by design (the broadcast
	// "fast attack, slow release" shape): an announcement is already
	// talking over the bed by the time the duck starts, so the drop
	// should be quick, not lingering.
	DuckFadeDurationMs int `json:"duckFadeDurationMs"`

	// DuckRestoreFadeDurationMs is how long a session takes to fade back
	// UP once the last session ducking it stops. Longer than
	// DuckFadeDurationMs: nothing is talking over the bed anymore, so an
	// abrupt return to full level reads as jarring in a way the initial
	// duck-down does not.
	DuckRestoreFadeDurationMs int `json:"duckRestoreFadeDurationMs"`

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

	// ScheduledStartDeliveryBoundMs and ScheduledStartMarginMs are read by
	// the COORDINATOR, not by a node: every other field in this object is
	// a default a node applies to its own playback, so the consumer is
	// worth stating rather than inferring. They are the two terms
	// RES-019 section 6 adds to a scheduled start's T0, which the
	// coordinator computes as max(node ready times) + bound + margin and
	// sends identically to every target.
	//
	// DeliveryBound is how long the coordinator assumes a start command
	// takes to reach the slowest target and finish its preroll. It is a
	// property of the path and a bench can eventually replace it with a
	// measurement.
	//
	// Margin is the pad held back beyond that bound. It is a judgement
	// about how much slack a show should carry, and stays a judgement even
	// after the bound is measured -- which is why these are two fields and
	// not one sum: collapsing them would make the measurable half
	// unmeasurable.
	//
	// BOTH SHIPPED VALUES ARE GUESSES, NOT MEASUREMENTS. Nothing in this
	// repository has timed a command from dispatch to a node's completed
	// preroll. See AudioSettingsDefaultPayload.
	ScheduledStartDeliveryBoundMs int `json:"scheduledStartDeliveryBoundMs"`
	ScheduledStartMarginMs        int `json:"scheduledStartMarginMs"`
}

// AudioSettingsDefaultPayload is the value reported when nothing has ever
// been written. Most numbers here are starting points, not tuned values.
//
// DriftIgnoreThresholdMs is the exception: it is now derived from a
// measurement. A real sink was observed making two routine skew
// corrections of exactly 960 samples, 20.0 ms at 48 kHz, about 21 minutes
// apart, each landing within 0.3 microseconds of 20 ms. The previous
// default of 20 was therefore the SAME NUMBER as the ordinary correction
// it had to be distinguished from, with no margin for the jitter two
// samples cannot characterise. 40 is that sink's own documented
// drift-tolerance, twice the correction it actually makes: an error above
// it is one the sink's own policy says should already have been handled,
// so it is genuinely anomalous rather than routine.
//
// This does not by itself fix a spurious resync, and is not claimed to:
// internal/agent/audio/timeline.go consults this threshold only INSIDE an
// established discontinuity, and a sink skew correction is none of the
// three causes that establish one. What it fixes is the case where a
// genuine cause (a PTP step) coincides with a routine correction, where a
// threshold of 20 decided "seek or do not seek" on 0.3 microseconds of
// noise.
//
// An operator whose stored revision carries an explicit 20 keeps 20: this
// default governs only a coordinator that has never written the object.
//
// The two scheduled-start numbers are guesses of the same kind. Together
// they put a scheduled start three seconds after the last target reports
// ready, which is invisible for a cue the show schedules and is three
// seconds of waiting for an operator pressing go. If that is felt as
// latency, the delivery bound is the half to measure and reduce: the
// margin is deliberate slack, and the bound is a placeholder standing in
// for a measurement nobody has taken.
var AudioSettingsDefaultPayload = AudioSettingsPayload{
	DriftIgnoreThresholdMs:     40,
	DefaultFadeCurve:           string(audio.FadeCurveLinear),
	DefaultFadeDurationMs:      1000,
	DefaultMaxBackgroundGainDb: -4.44,
	DuckTargetGainDb:           -12.04,
	DuckFadeDurationMs:         200,
	DuckRestoreFadeDurationMs:  800,
	LTCFrameRate:               string(audio.LTCFrameRate30),
	LTCDefaultStartOffset:      "00:00:00:00",

	ScheduledStartDeliveryBoundMs: 2000,
	ScheduledStartMarginMs:        1000,
}

var audioSettingsTopLevelKeys = map[string]bool{
	"driftIgnoreThresholdMs": true, "defaultFadeCurve": true,
	"defaultFadeDurationMs": true, "defaultMaxBackgroundGainDb": true,
	"duckTargetGainDb": true, "duckFadeDurationMs": true, "duckRestoreFadeDurationMs": true,
	"ltcFrameRate": true, "ltcDefaultStartOffset": true,
	"scheduledStartDeliveryBoundMs": true, "scheduledStartMarginMs": true,
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
	for old, replacement := range audioSettingsRenamedGainFields {
		if _, present := top[old]; present {
			return AudioSettingsPayload{}, &ValidationError{
				Code: ValidationCodeFieldInvalid, Field: old,
				Detail: fmt.Sprintf("%s was a linear amplitude multiplier and no longer exists; use %s, which is in decibels (0 dB is unity)", old, replacement),
			}
		}
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

	maxGainDb, verr := decodeRequiredFloat(top, "defaultMaxBackgroundGainDb", "defaultMaxBackgroundGainDb")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if err := audio.CeilingFromDb(maxGainDb).Validate(); err != nil {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "defaultMaxBackgroundGainDb",
			Detail: err.Error(),
		}
	}
	if maxGainDb > maxDefaultMaxBackgroundGainDb {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "defaultMaxBackgroundGainDb",
			Detail: fmt.Sprintf("defaultMaxBackgroundGainDb is in decibels (0 dB is unity) and must not exceed %v dB", maxDefaultMaxBackgroundGainDb),
		}
	}
	if maxGainDb < minDefaultMaxBackgroundGainDb {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "defaultMaxBackgroundGainDb",
			Detail: fmt.Sprintf("defaultMaxBackgroundGainDb is in decibels (0 dB is unity) and must not be below %v dB", minDefaultMaxBackgroundGainDb),
		}
	}

	duckGainDb, verr := decodeRequiredFloat(top, "duckTargetGainDb", "duckTargetGainDb")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if duckGainDb >= maxDuckTargetGainDb || duckGainDb < minDuckTargetGainDb {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "duckTargetGainDb",
			Detail: fmt.Sprintf("duckTargetGainDb is in decibels and must be negative and at least %v dB: 0 dB or louder does not duck anything, and %v dB is already silence", minDuckTargetGainDb, minDuckTargetGainDb),
		}
	}

	duckFadeMs, verr := decodeRequiredInt(top, "duckFadeDurationMs", "duckFadeDurationMs")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if duckFadeMs < minDuckFadeDurationMs || duckFadeMs > maxDuckFadeDurationMs {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "duckFadeDurationMs",
			Detail: fmt.Sprintf("duckFadeDurationMs must be between %d and %d", minDuckFadeDurationMs, maxDuckFadeDurationMs),
		}
	}

	duckRestoreFadeMs, verr := decodeRequiredInt(top, "duckRestoreFadeDurationMs", "duckRestoreFadeDurationMs")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if duckRestoreFadeMs < minDuckFadeDurationMs || duckRestoreFadeMs > maxDuckFadeDurationMs {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "duckRestoreFadeDurationMs",
			Detail: fmt.Sprintf("duckRestoreFadeDurationMs must be between %d and %d", minDuckFadeDurationMs, maxDuckFadeDurationMs),
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

	deliveryBoundMs, verr := decodeRequiredInt(top, "scheduledStartDeliveryBoundMs", "scheduledStartDeliveryBoundMs")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if deliveryBoundMs < minScheduledStartOffsetMs || deliveryBoundMs > maxScheduledStartOffsetMs {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "scheduledStartDeliveryBoundMs",
			Detail: fmt.Sprintf("scheduledStartDeliveryBoundMs must be between %d and %d", minScheduledStartOffsetMs, maxScheduledStartOffsetMs),
		}
	}

	marginMs, verr := decodeRequiredInt(top, "scheduledStartMarginMs", "scheduledStartMarginMs")
	if verr != nil {
		return AudioSettingsPayload{}, verr
	}
	if marginMs < minScheduledStartOffsetMs || marginMs > maxScheduledStartOffsetMs {
		return AudioSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "scheduledStartMarginMs",
			Detail: fmt.Sprintf("scheduledStartMarginMs must be between %d and %d", minScheduledStartOffsetMs, maxScheduledStartOffsetMs),
		}
	}

	return AudioSettingsPayload{
		DriftIgnoreThresholdMs:     driftMs,
		DefaultFadeCurve:           fadeCurve,
		DefaultFadeDurationMs:      fadeDurationMs,
		DefaultMaxBackgroundGainDb: maxGainDb,
		DuckTargetGainDb:           duckGainDb,
		DuckFadeDurationMs:         duckFadeMs,
		DuckRestoreFadeDurationMs:  duckRestoreFadeMs,
		LTCFrameRate:               ltcFrameRate,
		LTCDefaultStartOffset:      ltcOffset,

		ScheduledStartDeliveryBoundMs: deliveryBoundMs,
		ScheduledStartMarginMs:        marginMs,
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
