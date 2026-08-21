package fppidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// SchemaVersion is the current playlist-entry observation schema version,
// contract section 1.2. A request carrying any other value is refused.
const SchemaVersion = 1

// EntryIdentity is the five fields that determine a playlist entry's
// identity, contract section 1.3. Position is zero-based within Section.
type EntryIdentity struct {
	InstanceUUID string
	PlaylistName string
	PlaylistHash string
	Section      string
	Position     int
}

// DeriveEntryKey hashes a canonical JSON object of the five identifying
// fields rather than a delimited string, so no playlist name or section
// containing a separator character can collide with a different entry.
// The five members are built directly in their JCS-sorted order
// (instanceUuid, playlistHash, playlistName, position, section, per
// contract section 1.3) rather than round-tripped through Canonicalize,
// but the resulting bytes are byte-for-byte what Canonicalize would
// produce for the same object, since these member names have a fixed
// UTF-16 order. Mirrors deriveEntryKey in
// native/src/playlist_identity.cpp.
func DeriveEntryKey(identity EntryIdentity) (string, error) {
	obj := makeObject([]member{
		{name: "instanceUuid", v: makeString(identity.InstanceUUID)},
		{name: "playlistHash", v: makeString(identity.PlaylistHash)},
		{name: "playlistName", v: makeString(identity.PlaylistName)},
		{name: "position", v: makeNumber(float64(identity.Position))},
		{name: "section", v: makeString(identity.Section)},
	})
	var out strings.Builder
	if err := writeValue(obj, &out); err != nil {
		return "", fmt.Errorf("fppidentity: could not derive entry key: %w", err)
	}
	sum := sha256.Sum256([]byte(out.String()))
	return hex.EncodeToString(sum[:]), nil
}

// Action is the wire vocabulary for the FPP playlist callback action,
// contract section 1.2. A repeated Playing carrying a new section or
// position is item advancement, not a duplicate.
type Action string

const (
	ActionStart     Action = "start"
	ActionPlaying   Action = "playing"
	ActionStop      Action = "stop"
	ActionQueryNext Action = "query_next"
	ActionUnknown   Action = "unknown"
)

// ParseAction validates s against the fixed action vocabulary. Any other
// value is refused per contract section 1.6 step 7; it is not mapped to
// ActionUnknown, since that wire value is a report from the plugin, not a
// parse fallback.
func ParseAction(s string) (Action, error) {
	switch Action(s) {
	case ActionStart, ActionPlaying, ActionStop, ActionQueryNext, ActionUnknown:
		return Action(s), nil
	default:
		return "", fmt.Errorf("fppidentity: %q is not a recognized action", s)
	}
}

// Unavailable is the wire vocabulary for why an observation could not
// carry identity, contract section 1.4. The zero value, "", means
// identity is available.
type Unavailable string

const (
	UnavailableNone                   Unavailable = ""
	UnavailableMissingInstanceUUID    Unavailable = "missing_instance_uuid"
	UnavailableMissingPlaylistName    Unavailable = "missing_playlist_name"
	UnavailableMissingDefinition      Unavailable = "missing_definition"
	UnavailableUnsupportedDefShape    Unavailable = "unsupported_definition_shape"
	UnavailableNegativePosition       Unavailable = "negative_position"
	UnavailableTruncatedIdentityField Unavailable = "truncated_identity_field"
)

// ParseUnavailable validates s against the fixed §1.4 vocabulary. The
// empty string is accepted as UnavailableNone: an absent `unavailable`
// field means identity is available, and callers should not have to
// special-case that separately from parsing it.
func ParseUnavailable(s string) (Unavailable, error) {
	switch Unavailable(s) {
	case UnavailableNone,
		UnavailableMissingInstanceUUID,
		UnavailableMissingPlaylistName,
		UnavailableMissingDefinition,
		UnavailableUnsupportedDefShape,
		UnavailableNegativePosition,
		UnavailableTruncatedIdentityField:
		return Unavailable(s), nil
	default:
		return "", fmt.Errorf("fppidentity: %q is not a recognized unavailable reason", s)
	}
}

// IsHash64 reports whether s is exactly 64 lowercase hex characters, the
// shape contract section 1.6 step 7 requires for playlistHash and
// entryKey.
func IsHash64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
