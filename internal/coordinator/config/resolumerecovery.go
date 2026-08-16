package config

import (
	"encoding/json"
	"fmt"
)

// This file is Track D seam D-3a's config kind: the "resolume.recovery"
// config_objects.kind / config_revisions.payload_json shape for the
// crash-recovery auto-restore toggle (TRACK-D-D3A-BUILD-CONTRACT.md §1.1).
// Mirrors fppendpoints.go's shape exactly — one boolean rather than a list,
// same singleton object id, same "api" source constant.

const (
	// ResolumeRecoveryConfigKind is config_objects.kind and
	// config_revisions.kind for the auto-restore toggle, and the second
	// path segment of GET/PUT /api/v1/config/resolume.recovery.
	ResolumeRecoveryConfigKind = "resolume.recovery"

	// ResolumeRecoveryConfigObjectID is the single config_objects.id this
	// kind ever uses — one toggle per coordinator.
	ResolumeRecoveryConfigObjectID = "default"

	// ResolumeRecoverySourceAPI is this kind's only config_revisions.source
	// value: every revision is written through PUT
	// /api/v1/config/resolume.recovery, never migrated from an environment
	// variable (there is no env var for this toggle — see
	// [Config.ResolumeRecoverySettle]'s doc comment for the one env var
	// this seam DOES have, a tuning knob rather than show state).
	ResolumeRecoverySourceAPI = "api"

	// ResolumeRecoveryDefaultEnabled is the value reported when nothing
	// has ever been written for this kind — the default is ON, deliberately
	// (build contract §1.1): an operator who believes recovery is armed and
	// discovers otherwise mid-show is worse off than one who knows it is
	// off, and the restore's own skip rules are what keep an armed restore
	// from fighting a human.
	ResolumeRecoveryDefaultEnabled = true
)

// ResolumeRecoveryPayload is config_revisions.payload_json's decoded shape
// for [ResolumeRecoveryConfigKind]: one boolean, no other keys.
type ResolumeRecoveryPayload struct {
	AutoRestoreEnabled bool `json:"autoRestoreEnabled"`
}

// EncodeResolumeRecoveryPayload marshals enabled into config_revisions'
// payload_json column shape.
func EncodeResolumeRecoveryPayload(enabled bool) (string, error) {
	b, err := json.Marshal(ResolumeRecoveryPayload{AutoRestoreEnabled: enabled})
	if err != nil {
		return "", fmt.Errorf("config: encode resolume.recovery payload: %w", err)
	}
	return string(b), nil
}

// DecodeResolumeRecoveryPayload is [EncodeResolumeRecoveryPayload]'s
// inverse. A decode failure means a config_revisions row this package never
// wrote in this shape — every writer of this kind goes through
// EncodeResolumeRecoveryPayload — so callers treat it as a store-integrity
// error, not a validation outcome to recover from.
func DecodeResolumeRecoveryPayload(raw string) (bool, error) {
	var payload ResolumeRecoveryPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return false, fmt.Errorf("config: decode resolume.recovery payload: %w", err)
	}
	return payload.AutoRestoreEnabled, nil
}
