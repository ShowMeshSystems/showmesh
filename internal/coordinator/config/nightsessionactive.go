package config

import (
	"encoding/json"
	"fmt"
)

// NightSessionActiveConfigKind is config_objects.kind and
// config_revisions.kind for the night.session.active singleton (Track F
// seam F1, reserved identifier list in the seam spec).
const NightSessionActiveConfigKind = "night.session.active"

// NightSessionActiveObjectID is the config_objects id the active-session
// pointer is ALWAYS stored under — a fixed constant, never derived from
// any configuration value, mirroring show.active's ShowActiveObjectID
// (showactive.go) for the identical "a renamed value must never orphan a
// stored revision" reason. The seam spec reserves this exact literal
// ("default").
const NightSessionActiveObjectID = "default"

// nightSessionActiveTopLevelKeys is the complete set of keys
// DecodeNightSessionActivePayload recognizes at the top level.
var nightSessionActiveTopLevelKeys = map[string]bool{"session": true}

// NightSessionActivePayload is config_revisions.payload_json's decoded,
// VALIDATED shape for [NightSessionActiveConfigKind]. Session is "" to
// mean explicitly unset (cleared) — see DecodeNightSessionActivePayload's
// own doc comment for why an empty string is a real, distinct value here
// rather than a stand-in for "not provided" (ADR-039 rule 4's
// zero-to-one-and-back-to-zero transition).
type NightSessionActivePayload struct {
	Session string `json:"session"`
}

// EncodeNightSessionActivePayload marshals p into
// config_revisions.payload_json's column shape. p is assumed already
// valid (the product of DecodeNightSessionActivePayload); this function
// does not re-validate.
func EncodeNightSessionActivePayload(p NightSessionActivePayload) (string, error) {
	b, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("config: encode night.session.active payload: %w", err)
	}
	return string(b), nil
}

// NightSessionExists reports whether id names an existing night.session
// object with an active revision. Caller-supplied, mirroring show.active's
// showExists callback.
type NightSessionExists func(id string) bool

// DecodeNightSessionActivePayload parses and validates raw. "session" is
// REQUIRED as a key (an omitted key is rejected, never treated as "leave
// it as it was" — this kind has no read-modify-write path, matching every
// other full-replacement kind in this package) but its value may be the
// empty string, which explicitly clears the pointer back to unset. A
// non-empty value must resolve against sessionExists. This is the one
// path in this package that deliberately allows an empty string past
// decodeRequiredString's ordinary "empty is invalid" rule — ADR-039 rule
// 4 requires the zero-to-one-and-back-to-zero transition to work, and a
// pointer kind with no way to express "nothing" cannot ever be tested for
// the "back to zero" half of that.
func DecodeNightSessionActivePayload(raw string, sessionExists NightSessionExists) (NightSessionActivePayload, *ValidationError) {
	top, verr := decodeTopLevelObject(raw)
	if verr != nil {
		return NightSessionActivePayload{}, verr
	}
	if verr := rejectUnknownTopLevelKeys(top, nightSessionActiveTopLevelKeys); verr != nil {
		return NightSessionActivePayload{}, verr
	}

	session, verr := decodeRequiredStringAllowEmpty(top, "session", "session")
	if verr != nil {
		return NightSessionActivePayload{}, verr
	}
	if session != "" && !sessionExists(session) {
		return NightSessionActivePayload{}, &ValidationError{
			Code: ValidationCodeFieldUnknownReference, Field: "session",
			Detail: fmt.Sprintf("session %q is not a configured night.session object", session),
		}
	}

	return NightSessionActivePayload{Session: session}, nil
}
