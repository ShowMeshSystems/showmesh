package config

import (
	"encoding/json"
	"fmt"
)

// This file is Track B seam B2c's config kind (ADR-039, TRACK-B-BUILD-CONTRACT.md
// §"render.settings"): the operator-settable idle-output behaviour (ruling 3
// of TRACK-B-BUILD-CONTRACT.md — sync loss changes the picture, never the
// sender) and the render pipeline supervisor's bounded restart backoff.
// Singleton, mirroring resolumerecovery.go's shape: one config object id,
// a well-defined default so GET never 404s, PUT is a full replacement.

const (
	// RenderSettingsConfigKind is config_objects.kind and
	// config_revisions.kind for this object, and the second path segment of
	// GET/PUT /api/v1/config/render.settings.
	RenderSettingsConfigKind = "render.settings"

	// RenderSettingsConfigObjectID is the single config_objects.id this
	// kind ever uses — one settings object per coordinator.
	RenderSettingsConfigObjectID = "default"

	// RenderSettingsSourceAPI is this kind's only config_revisions.source
	// value: no environment variable ever backed this object (ADR-039
	// decision 2 — a render node's idle behaviour and restart backoff are
	// show state, not a start-time value), so there is nothing to migrate.
	RenderSettingsSourceAPI = "api"
)

// The three members of render.settings.idleOutput (TRACK-B-BUILD-CONTRACT.md
// ruling 3): what a surface draws while the timeline is stopped, opened, or
// unknown.
const (
	RenderIdleOutputBlack      = "black"
	RenderIdleOutputHold       = "hold"
	RenderIdleOutputDiagnostic = "diagnostic"
)

var renderIdleOutputs = map[string]bool{
	RenderIdleOutputBlack:      true,
	RenderIdleOutputHold:       true,
	RenderIdleOutputDiagnostic: true,
}

// RenderIdleOutputDefault is the value reported when nothing has ever been
// written. Black, deliberately (ruling 3): a frozen frame on a house is
// indistinguishable from a crash and stays that way all night, which is the
// failure the operator cannot diagnose from the driveway.
const RenderIdleOutputDefault = RenderIdleOutputBlack

// Bounds on render.settings.restartPolicy's three fields. Sanity bounds on
// a supervisor backoff, not a measured ceiling — wide enough to cover any
// plausible tuning, narrow enough to catch a typo (e.g. a delay entered in
// milliseconds instead of seconds).
const (
	minInitialDelaySeconds = 1
	maxInitialDelaySeconds = 60

	minMaxDelaySeconds = 1
	maxMaxDelaySeconds = 300

	minConsecutiveFastFailures = 1
	maxConsecutiveFastFailures = 20
)

// Defaults for render.settings.restartPolicy, applied only when nothing has
// ever been written (RenderSettingsDefaultPayload) — never as a per-field
// fallback inside a PUT, which is a full replacement (see
// DecodeRenderSettingsPayload's doc comment).
const (
	RenderRestartInitialDelaySecondsDefault        = 1
	RenderRestartMaxDelaySecondsDefault            = 30
	RenderRestartMaxConsecutiveFastFailuresDefault = 5
)

// RenderRestartPolicy is render.settings.restartPolicy: the render pipeline
// supervisor's bounded exponential backoff (TRACK-B-BUILD-CONTRACT.md seam
// B2 — "must not restart a pipeline that fails identically and immediately
// forever").
type RenderRestartPolicy struct {
	InitialDelaySeconds        int `json:"initialDelaySeconds"`
	MaxDelaySeconds            int `json:"maxDelaySeconds"`
	MaxConsecutiveFastFailures int `json:"maxConsecutiveFastFailures"`
}

// RenderSettingsPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [RenderSettingsConfigKind].
type RenderSettingsPayload struct {
	IdleOutput    string              `json:"idleOutput"`
	RestartPolicy RenderRestartPolicy `json:"restartPolicy"`
}

// RenderSettingsDefaultPayload is the value reported when nothing has ever
// been written for this kind — mirrors ResolumeRecoveryDefaultEnabled's own
// "the default when nothing is stored" posture, extended to a whole payload
// since this kind carries more than one field.
var RenderSettingsDefaultPayload = RenderSettingsPayload{
	IdleOutput: RenderIdleOutputDefault,
	RestartPolicy: RenderRestartPolicy{
		InitialDelaySeconds:        RenderRestartInitialDelaySecondsDefault,
		MaxDelaySeconds:            RenderRestartMaxDelaySecondsDefault,
		MaxConsecutiveFastFailures: RenderRestartMaxConsecutiveFastFailuresDefault,
	},
}

var renderSettingsTopLevelKeys = map[string]bool{"idleOutput": true, "restartPolicy": true}
var renderRestartPolicyKeys = map[string]bool{
	"initialDelaySeconds": true, "maxDelaySeconds": true, "maxConsecutiveFastFailures": true,
}

// EncodeRenderSettingsPayload marshals p into config_revisions.payload_json's
// column shape. p is assumed already valid (the product of
// DecodeRenderSettingsPayload); this function does not re-validate.
func EncodeRenderSettingsPayload(p RenderSettingsPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode render.settings payload: %w", err)
	}
	return string(b), nil
}

// DecodeRenderSettingsPayload parses and validates raw. PUT is a full
// replacement (show.surface's house style, not fpp.endpoints'
// merge-by-absence): every field is required on every write, so an absent
// key is refused by name rather than silently defaulted — the "a config PUT
// with no endpoints key wiped every endpoint" defect class, applied here by
// simply never treating "absent" as "keep the old value" or "use the
// default" for a REQUIRED field. A reader of the STORED value (GET on an
// object nothing has ever configured) gets RenderSettingsDefaultPayload
// instead; that is a distinct code path (handleGetRenderSettingsConfig),
// never this function.
func DecodeRenderSettingsPayload(raw string) (RenderSettingsPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return RenderSettingsPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, renderSettingsTopLevelKeys); verr != nil {
		return RenderSettingsPayload{}, verr
	}

	idleOutput, verr := decodeRequiredString(top, "idleOutput", "idleOutput")
	if verr != nil {
		return RenderSettingsPayload{}, verr
	}
	if !renderIdleOutputs[idleOutput] {
		return RenderSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "idleOutput",
			Detail: "idleOutput must be one of black, hold, or diagnostic",
		}
	}

	restartPolicy, verr := decodeRenderRestartPolicy(top)
	if verr != nil {
		return RenderSettingsPayload{}, verr
	}

	return RenderSettingsPayload{IdleOutput: idleOutput, RestartPolicy: restartPolicy}, nil
}

// decodeRenderRestartPolicy decodes and validates the required
// "restartPolicy" field: all three members required, each range-checked,
// plus the cross-field rule that maxDelaySeconds must not be smaller than
// initialDelaySeconds — a backoff that shrinks makes no sense and is almost
// certainly a swapped pair of values, so this names both numbers rather
// than accepting it as two independently-valid fields.
func decodeRenderRestartPolicy(top map[string]json.RawMessage) (RenderRestartPolicy, *ValidationError) {
	fields, verr := decodeRequiredObject(top, "restartPolicy", "restartPolicy")
	if verr != nil {
		return RenderRestartPolicy{}, verr
	}
	if verr := rejectUnknownKeysUnder(fields, renderRestartPolicyKeys, "restartPolicy"); verr != nil {
		return RenderRestartPolicy{}, verr
	}

	initialDelay, verr := decodeRequiredInt(fields, "initialDelaySeconds", "restartPolicy.initialDelaySeconds")
	if verr != nil {
		return RenderRestartPolicy{}, verr
	}
	if initialDelay < minInitialDelaySeconds || initialDelay > maxInitialDelaySeconds {
		return RenderRestartPolicy{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "restartPolicy.initialDelaySeconds",
			Detail: fmt.Sprintf("initialDelaySeconds must be between %d and %d", minInitialDelaySeconds, maxInitialDelaySeconds),
		}
	}

	maxDelay, verr := decodeRequiredInt(fields, "maxDelaySeconds", "restartPolicy.maxDelaySeconds")
	if verr != nil {
		return RenderRestartPolicy{}, verr
	}
	if maxDelay < minMaxDelaySeconds || maxDelay > maxMaxDelaySeconds {
		return RenderRestartPolicy{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "restartPolicy.maxDelaySeconds",
			Detail: fmt.Sprintf("maxDelaySeconds must be between %d and %d", minMaxDelaySeconds, maxMaxDelaySeconds),
		}
	}
	if maxDelay < initialDelay {
		return RenderRestartPolicy{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "restartPolicy.maxDelaySeconds",
			Detail: fmt.Sprintf(
				"maxDelaySeconds (%d) must not be less than initialDelaySeconds (%d)", maxDelay, initialDelay),
		}
	}

	fastFailures, verr := decodeRequiredInt(fields, "maxConsecutiveFastFailures", "restartPolicy.maxConsecutiveFastFailures")
	if verr != nil {
		return RenderRestartPolicy{}, verr
	}
	if fastFailures < minConsecutiveFastFailures || fastFailures > maxConsecutiveFastFailures {
		return RenderRestartPolicy{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "restartPolicy.maxConsecutiveFastFailures",
			Detail: fmt.Sprintf("maxConsecutiveFastFailures must be between %d and %d", minConsecutiveFastFailures, maxConsecutiveFastFailures),
		}
	}

	return RenderRestartPolicy{
		InitialDelaySeconds:        initialDelay,
		MaxDelaySeconds:            maxDelay,
		MaxConsecutiveFastFailures: fastFailures,
	}, nil
}
