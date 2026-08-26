package config

import (
	"encoding/json"
	"fmt"
)

// This file is the "fppconnect.settings" singleton (ADR-039, ADR-044
// decision 5, IDENTIFIER-REGISTER.md): the enable flag and the two byte
// caps that bound the node's unauthenticated xLights ingestion listener
// (ADR-044 decision 4's three bounds — the directory allowlist is the
// listener's own code, not configuration). Singleton, mirroring
// audiosettings.go's shape exactly: one config object id, a well-defined
// default so GET never 404s, PUT is a full replacement.

const (
	// FPPConnectSettingsConfigKind is config_objects.kind and
	// config_revisions.kind for this object, and the second path segment
	// of GET/PUT /api/v1/config/fppconnect.settings.
	FPPConnectSettingsConfigKind = "fppconnect.settings"

	// FPPConnectSettingsConfigObjectID is the single config_objects.id
	// this kind ever uses — one settings object per coordinator.
	FPPConnectSettingsConfigObjectID = "default"

	// FPPConnectSettingsSourceAPI is this kind's only config_revisions.
	// source value: no environment variable ever backed this object
	// (IDENTIFIER-REGISTER.md: the only environment variable Track E
	// phase 2 adds is SHOWMESH_FPPCONNECT_LISTEN_ADDR, a start-time bind
	// address under ADR-039 decision 9, unrelated to this kind).
	FPPConnectSettingsSourceAPI = "api"
)

const bytesPerGiB = 1 << 30

// fppConnectSettingsDefaultMaxFileBytes and
// fppConnectSettingsDefaultMaxAssetDirBytes are IDENTIFIER-REGISTER.md's
// stated defaults (2 GiB, 20 GiB) — sanity starting points, not measured
// against any real xLights upload.
const (
	fppConnectSettingsDefaultMaxFileBytes     = 2 * bytesPerGiB
	fppConnectSettingsDefaultMaxAssetDirBytes = 20 * bytesPerGiB
)

// FPPConnectSettingsPayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [FPPConnectSettingsConfigKind].
type FPPConnectSettingsPayload struct {
	// Enabled gates the node's xLights ingestion listener. BUILDER
	// DEFAULT: true (see FPPConnectSettingsDefaultPayload) — ADR-044 rules
	// on the byte caps and the enable flag's existence, not on which way
	// it defaults; flagged here for review rather than decided silently.
	Enabled bool `json:"enabled"`

	// MaxFileBytes is the per-file byte cap on one ingested upload
	// (ADR-044 decision 4's second bound). Always at least 1 byte.
	MaxFileBytes int64 `json:"maxFileBytes"`

	// MaxAssetDirBytes is the total byte cap on the node's asset directory
	// (ADR-044 decision 4's third bound). Always at least MaxFileBytes: a
	// cap smaller than a single allowed file would refuse every upload
	// unconditionally, which is not what an operator setting a total cap
	// intends.
	MaxAssetDirBytes int64 `json:"maxAssetDirBytes"`
}

// FPPConnectSettingsDefaultPayload is the value reported when nothing has
// ever been written. enabled:true is a BUILDER DEFAULT, not an owner
// ruling — ADR-044 decision 5 requires the kind to exist and carry these
// three fields; it does not rule on which way "enabled" defaults.
var FPPConnectSettingsDefaultPayload = FPPConnectSettingsPayload{
	Enabled:          true,
	MaxFileBytes:     fppConnectSettingsDefaultMaxFileBytes,
	MaxAssetDirBytes: fppConnectSettingsDefaultMaxAssetDirBytes,
}

var fppConnectSettingsTopLevelKeys = map[string]bool{
	"enabled": true, "maxFileBytes": true, "maxAssetDirBytes": true,
}

// EncodeFPPConnectSettingsPayload marshals p into config_revisions.
// payload_json's column shape. p is assumed already valid (the product of
// DecodeFPPConnectSettingsPayload); this function does not re-validate.
func EncodeFPPConnectSettingsPayload(p FPPConnectSettingsPayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode fppconnect.settings payload: %w", err)
	}
	return string(b), nil
}

// DecodeFPPConnectSettingsPayload parses and validates raw. PUT is a full
// replacement: every field is required on every write, so an absent key is
// refused by name rather than silently defaulted or carried forward from
// the previous revision. A reader of the STORED value (GET on an object
// nothing has ever configured) gets FPPConnectSettingsDefaultPayload
// instead, a distinct code path in package api, never this function.
func DecodeFPPConnectSettingsPayload(raw string) (FPPConnectSettingsPayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return FPPConnectSettingsPayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, fppConnectSettingsTopLevelKeys); verr != nil {
		return FPPConnectSettingsPayload{}, verr
	}

	enabled, verr := decodeRequiredBool(top, "enabled", "enabled")
	if verr != nil {
		return FPPConnectSettingsPayload{}, verr
	}

	maxFileBytes, verr := decodeRequiredInt(top, "maxFileBytes", "maxFileBytes")
	if verr != nil {
		return FPPConnectSettingsPayload{}, verr
	}
	if maxFileBytes < 1 {
		return FPPConnectSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "maxFileBytes",
			Detail: "maxFileBytes must be at least 1",
		}
	}

	maxAssetDirBytes, verr := decodeRequiredInt(top, "maxAssetDirBytes", "maxAssetDirBytes")
	if verr != nil {
		return FPPConnectSettingsPayload{}, verr
	}
	if maxAssetDirBytes < 1 {
		return FPPConnectSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "maxAssetDirBytes",
			Detail: "maxAssetDirBytes must be at least 1",
		}
	}
	if maxAssetDirBytes < maxFileBytes {
		return FPPConnectSettingsPayload{}, &ValidationError{
			Code: ValidationCodeFieldInvalid, Field: "maxAssetDirBytes",
			Detail: fmt.Sprintf("maxAssetDirBytes (%d) must be at least maxFileBytes (%d)", maxAssetDirBytes, maxFileBytes),
		}
	}

	return FPPConnectSettingsPayload{
		Enabled:          enabled,
		MaxFileBytes:     int64(maxFileBytes),
		MaxAssetDirBytes: int64(maxAssetDirBytes),
	}, nil
}
