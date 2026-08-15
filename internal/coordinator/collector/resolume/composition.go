package resolume

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
)

// This file used to decode the full body of GET /composition. That call is
// now forbidden at runtime — see this package's own doc comment and
// guardfullcomposition_test.go, which enforces it mechanically — so the
// top-level decode tree (what used to be Composition, Layer, LayerGroup,
// Column, Deck, and their helper types) is gone with it.
//
// # What survives here, and why
//
// Every Resolume object this package will ever need to read again is
// addressed by [ObjectID], and read by id (`/composition/clips/by-id/{id}`
// and its siblings), never by enumerating the whole document. A future
// by-id decode (Track D seam D-2) still has to solve the identical
// problem this file already solved once: every leaf in a Resolume REST
// response is an envelope object, never a bare value, and encoding/json
// collapses an ABSENT key and an explicit JSON `null` into the same Go
// zero value for nearly every ordinary destination — the "ma": null
// defect (CLAUDE.md) reproduced in Resolume's own vocabulary. So the
// pieces kept here are exactly the ones that solve THAT problem, decoupled
// from the full-document tree they used to be decoded inside of:
//
//   - [ParamBoolean], [ParamRange], [ParamString], [ParamChoice], and
//     [ParamState]: the parameter-envelope leaf types. A by-id read of a
//     single clip, layer, column, or deck still returns these same
//     envelope shapes for whichever fields it carries — only the
//     surrounding document structure they were nested inside is gone.
//   - [Presence], and the null-vs-absent [ActiveClipField] /
//     [ClipTransportControls] types built on it: the pattern a targeted
//     decode will need again the moment it reads a field where absent and
//     null must be told apart (this file's own [Presence] doc comment
//     names which two leaves that was ever load-bearing for: active_clip
//     and transport.controls).
//   - [ObjectID] and [ParameterID]: the two identifier types every
//     envelope and every by-id path is addressed by. Kept here rather
//     than reintroducing them from scratch, since [ParameterID]'s
//     MarshalJSON-refusal is structural enforcement a future seam should
//     not have to remember to re-derive — see its own doc comment.
//
// Nothing else survives. [Clip], [Layer], [LayerGroup], [Column], [Deck],
// and [Composition] (the full document tree) are deleted outright, not
// merely unused — the goal is that nothing in this package can construct
// GET /composition's shape again by accident, only rebuild a narrower
// by-id decode from the primitives above.

// ObjectID is a Resolume object identifier: persisted in the composition
// file, and confirmed stable across a restart and across a reorder. Safe
// to hold in memory; whether it is ever safe to persist elsewhere (SQLite,
// a config revision, an export bundle) is a later seam's decision, not
// this package's.
type ObjectID int64

func (o ObjectID) String() string { return strconv.FormatInt(int64(o), 10) }

// ParameterID is a Resolume parameter identifier: minted fresh every time
// Arena loads a composition — including on every restart — and never
// persisted anywhere by this package. See this package's doc comment for
// the full lifecycle rule and why [ParameterID.MarshalJSON] always fails.
type ParameterID int64

func (p ParameterID) String() string { return strconv.FormatInt(int64(p), 10) }

// MarshalJSON always returns an error. A ParameterID must never reach any
// JSON this project produces — an API payload, a config revision, an
// export bundle — because it is meaningless the moment Arena next
// restarts. This is the structural enforcement of that rule: a mistaken
// write fails loudly, at the exact call site that made it, instead of
// shipping a value that quietly stops meaning anything. [ParameterID.String]
// is still available for a log line; a log is not a persistence or wire
// boundary.
func (p ParameterID) MarshalJSON() ([]byte, error) {
	return nil, fmt.Errorf("resolume: ParameterID(%d) must never be marshaled to JSON: parameter ids are minted per Arena session and do not survive a restart", int64(p))
}

// --- The parameter envelope (capture section 4.3) --------------------------
//
// Every leaf in a Resolume composition is an object, never a bare value.
// The five types below are this package's minimal model of that envelope
// — just ID and Value, dropping min/max/in/out (ParamRange) and index
// (ParamChoice/ParamState) that nothing here has a use for yet. ParamChoice
// and ParamState carry an identical wire shape but are kept as distinct Go
// types deliberately: merging them would make it easy to write a
// `== "Connected"` comparison against something that started life as a
// generic ParamChoice, which is exactly the mistake the capture warns
// about for the five-state `connected` value.

// ParamBoolean models a `ParamBoolean` leaf.
type ParamBoolean struct {
	ID    ParameterID `json:"id"`
	Value bool        `json:"value"`
}

// ParamRange models a `ParamRange` leaf. Only Value and ID are decoded;
// min/max/in/out exist in the capture but nothing in this seam reads
// them.
type ParamRange struct {
	ID    ParameterID `json:"id"`
	Value float64     `json:"value"`
}

// ParamString models a `ParamString` leaf.
type ParamString struct {
	ID    ParameterID `json:"id"`
	Value string      `json:"value"`
}

// ParamChoice models a `ParamChoice` leaf, e.g. a clip's `transporttype`.
// Options is carried inline and is NOT constant across objects of the
// same kind (capture: two clips in one composition advertise different
// transporttype option lists), so nothing in this package hard-codes an
// enum from it.
type ParamChoice struct {
	ID      ParameterID `json:"id"`
	Value   string      `json:"value"`
	Options []string    `json:"options"`
}

// ParamState models a `ParamState` leaf, e.g. `connected`. The capture is
// explicit that this is a five-state value for a clip
// (Empty|Disconnected|Previewing|Connected|Connected & previewing) and a
// DIFFERENT three-state value for a column
// (Empty|Disconnected|Connected) — Options is read from the wire on every
// object rather than assumed constant, and Value is carried as the raw
// string rather than reduced to a bool anywhere in this package.
type ParamState struct {
	ID      ParameterID `json:"id"`
	Value   string      `json:"value"`
	Options []string    `json:"options"`
}

// --- Presence: the null-is-not-absent pattern -------------------------------

// Presence distinguishes three JSON encodings of one key: absent from the
// document entirely, present with value null, and present with a real
// value. encoding/json collapses the first two into "zero value, no
// error" for nearly every ordinary Go destination — the exact "ma": null
// defect this project has already shipped once, on FPP, per CLAUDE.md.
// Every field in this file where that distinction is load-bearing
// (active_clip, transport.controls) is decoded through a type built on
// Presence instead of a bare pointer or value.
type Presence int

const (
	// PresenceAbsent is the zero value: the key did not appear in the
	// document at all. encoding/json never calls UnmarshalJSON for an
	// absent key, so a field left at its zero value naturally reports
	// PresenceAbsent with no special-casing required — see
	// ActiveClipField.UnmarshalJSON's doc comment.
	PresenceAbsent Presence = iota
	// PresenceNull means the key was present with an explicit JSON null.
	PresenceNull
	// PresencePresent means the key was present with a real value.
	PresencePresent
)

func (p Presence) String() string {
	switch p {
	case PresenceAbsent:
		return "absent"
	case PresenceNull:
		return "null"
	case PresencePresent:
		return "present"
	default:
		return fmt.Sprintf("presence(%d)", int(p))
	}
}

// --- The null-vs-absent pattern, applied to the two leaves it was ever
// load-bearing for --------------------------------------------------------

// ActiveClip is a layer's active_clip value when present. Deliberately
// minimal: the only field a by-id confirmation ever needs from it is the
// clip's own object id, so that is the only field decoded here. No other
// property of this object was observed in the capture.
type ActiveClip struct {
	ID ObjectID `json:"id"`
}

// ActiveClipField is a layer's active_clip field, decoded so that
// PresenceAbsent (key missing — never observed in the capture, but not
// ruled out either), PresenceNull ("active_clip": null — nothing playing
// on this layer, capture section 4.4), and PresencePresent (an active
// clip) are three distinguishable outcomes rather than collapsing null
// and absent into the same Go zero value.
type ActiveClipField struct {
	Presence Presence
	Clip     *ActiveClip // non-nil only when Presence == PresencePresent
}

// UnmarshalJSON is invoked by encoding/json only when the "active_clip"
// key IS present in the document — an absent key never calls this method
// at all, leaving the field at its Go zero value, which is exactly
// {Presence: PresenceAbsent, Clip: nil} since PresenceAbsent is iota 0.
// That is what makes all three outcomes representable without a
// two-pass raw-map decode: this method only ever has to distinguish null
// from a real value.
func (f *ActiveClipField) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		f.Presence = PresenceNull
		f.Clip = nil
		return nil
	}
	var c ActiveClip
	if err := json.Unmarshal(data, &c); err != nil {
		return fmt.Errorf("active_clip: %w", err)
	}
	f.Presence = PresencePresent
	f.Clip = &c
	return nil
}

// ClipTransportControls is a clip's transport.controls field: an object
// under Timeline/BPM Sync transport, and JSON null under SMPTE transport
// (capture section 11.3) — never observed absent in the capture, but
// modeled with the same Presence pattern as ActiveClipField for the
// identical reason, and because "never observed absent" is not the same
// claim as "cannot be absent." Content is not decoded: the capture only
// ever observed this field as null, so there is nothing evidenced to
// decode when it is present. RawJSON keeps whatever bytes were there
// without inventing structure this package has no basis for.
type ClipTransportControls struct {
	Presence Presence
	RawJSON  json.RawMessage // non-nil only when Presence == PresencePresent
}

// UnmarshalJSON follows the identical shape as
// ActiveClipField.UnmarshalJSON — see its doc comment for why an absent
// key never reaches this method at all.
func (c *ClipTransportControls) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		c.Presence = PresenceNull
		c.RawJSON = nil
		return nil
	}
	raw := make(json.RawMessage, len(data))
	copy(raw, data)
	c.Presence = PresencePresent
	c.RawJSON = raw
	return nil
}

// ClipTransport is a clip's transport field: readable under every
// transport type (capture section 11.3 — position remains readable even
// under SMPTE). Kept as a pairing of Position and Controls because a
// by-id read of a single clip will decode both from the same "transport"
// object a full composition read used to nest this inside a layer's clip
// grid.
type ClipTransport struct {
	Position ParamRange            `json:"position"`
	Controls ClipTransportControls `json:"controls"`
}
