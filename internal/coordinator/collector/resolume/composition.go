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
// and its siblings), never by enumerating the whole document. Track D seam
// D-2 is the by-id decode this comment used to describe as future work; it
// solves the identical problem this file already solved once: every leaf
// in a Resolume REST response is an envelope object, never a bare value,
// and encoding/json collapses an ABSENT key and an explicit JSON `null`
// into the same Go zero value for nearly every ordinary destination — the
// "ma": null defect (CLAUDE.md) reproduced in Resolume's own vocabulary.
// So the pieces kept here from the deleted full-document decode are
// exactly the ones that solve THAT problem, decoupled from the full-document
// tree they used to be decoded inside of:
//
//   - [ParamBoolean], [ParamRange], [ParamString], [ParamChoice], and
//     [ParamState]: the parameter-envelope leaf types. A by-id read of a
//     single clip, layer, column, or deck still returns these same
//     envelope shapes for whichever fields it carries — only the
//     surrounding document structure they were nested inside is gone.
//   - [Presence], and the null-vs-absent [ActiveClipField] /
//     [ClipTransportControls] types built on it: the pattern D-2's own
//     *Field types below ([ParamBooleanField] and its four siblings)
//     generalize to every other leaf where absent and null must be told
//     apart, which D-1 only ever needed for two of them (active_clip and
//     transport.controls — see this file's own [Presence] doc comment).
//   - [ObjectID] and [ParameterID]: the two identifier types every
//     envelope and every by-id path is addressed by. Kept here rather
//     than reintroducing them from scratch, since [ParameterID]'s
//     MarshalJSON-refusal is structural enforcement a later seam should
//     not have to remember to re-derive — see its own doc comment.
//
// [Column] and [Composition] (the full-document container and the one
// object type D-2 does not read by id) stay deleted outright. [Clip],
// [Layer], [LayerGroup], and [Deck] are reintroduced below, but as narrow
// by-id decodes only — see each type's own doc comment for exactly which
// leaves it reads and why nothing enumerates or reconstructs the full
// document from them.

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
// The five types below are this package's minimal model of that envelope.
// ParamChoice and ParamState carry an identical wire shape but are kept as
// distinct Go types deliberately: merging them would make it easy to write
// a `== "Connected"` comparison against something that started life as a
// generic ParamChoice, which is exactly the mistake the capture warns
// about for the five-state `connected` value.
//
// # The "value" key gets the identical absent/null/present treatment the envelope already gets
//
// Arena's own OpenAPI specification (docs/bench/resolume-control-surface.md
// section 17) settled a question the capture alone could not: NO schema in
// the whole specification carries a `required` list, so `{"id": 123}` with
// no "value" key at all is a contract-legal `BooleanParameter`. Every
// *Field type in this file already wraps the ENVELOPE in [Presence] — a
// missing or null "bypassed" key cannot be mistaken for a real value — but
// until this fix, the "value" key ONE LEVEL DEEPER was still a bare Go
// field (bool/float64/string), so an envelope that answered with no
// "value" at all decoded that field to Go's own zero value, indistinguishable
// from a real, measured false/zero/empty. The consequence was specific and
// dangerous: a confirmation predicate reading `.Param.Value` off a
// value-less `bypassed` or `master` envelope would see `false`/`0.0` — the
// exact values `setLayerBypass`/`setLayerMaster` dispatch toward the
// darkening direction — and report a false CONFIRMED.
//
// Every Param* type below now decodes "value" (and, for [ParamRange],
// "min"/"max") through its own UnmarshalJSON, classifying each the same
// three ways [Presence] already classifies an envelope, via
// [presenceOfRaw]. A consumer must go through the corresponding *Field
// type's own accessor (ParamBooleanField.Bool, ParamRangeField.Float,
// ParamStringField.String, ParamChoiceField.String, ParamStateField.String)
// rather than reading .Param.Value directly — every accessor's second
// return value is false whenever EITHER level of presence — the envelope
// or the value inside it — was anything other than a real, present value.

// presenceOfRaw classifies a possibly-nil json.RawMessage the same three
// ways [Presence] already classifies an envelope: nil (the key was absent
// from the enclosing JSON object, so encoding/json never populated this
// RawMessage field at all), the JSON literal null, or a real value.
func presenceOfRaw(raw json.RawMessage) Presence {
	if raw == nil {
		return PresenceAbsent
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return PresenceNull
	}
	return PresencePresent
}

// ParamBoolean models a `ParamBoolean` leaf. ValuePresence is this file's
// own top-comment fix: PresencePresent is the ONLY case Value is safe to
// trust as a real, measured value — see [ParamBooleanField.Bool].
type ParamBoolean struct {
	ID            ParameterID
	Value         bool
	ValuePresence Presence
}

// UnmarshalJSON decodes "id" and "value" (the latter through
// [presenceOfRaw] before attempting to decode it as a bool) rather than
// relying on struct tags, so an absent or null "value" key is classified
// instead of silently decoding to Go's false zero value — see this file's
// own top comment.
func (p *ParamBoolean) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID    ParameterID     `json:"id"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("resolume: decoding ParamBoolean: %w", err)
	}
	p.ID = raw.ID
	p.ValuePresence = presenceOfRaw(raw.Value)
	p.Value = false
	if p.ValuePresence != PresencePresent {
		return nil
	}
	if err := json.Unmarshal(raw.Value, &p.Value); err != nil {
		return fmt.Errorf("resolume: decoding ParamBoolean value: %w", err)
	}
	return nil
}

// ParamRange models a `ParamRange` leaf. Value, Min, and Max each carry
// their own [Presence] — see this file's own top comment for Value; Min
// and Max get the identical treatment because Track D seam D-3's own
// setLayerMaster range validation (TRACK-D-D3-SPEC.md, "reject out-of-range
// with a stated reason rather than clamping silently") reads Arena's own
// declared bounds for the SPECIFIC parameter being written, rather than
// assuming the bench capture's observed [0, 1] holds for every layer on
// every composition. `in`/`out` (the clamped range) exist on the wire but
// nothing in this package reads them.
type ParamRange struct {
	ID    ParameterID
	Value float64
	Min   float64
	Max   float64

	ValuePresence Presence
	MinPresence   Presence
	MaxPresence   Presence
}

// UnmarshalJSON follows [ParamBoolean.UnmarshalJSON]'s identical shape for
// "value", extended to "min" and "max".
func (p *ParamRange) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID    ParameterID     `json:"id"`
		Value json.RawMessage `json:"value"`
		Min   json.RawMessage `json:"min"`
		Max   json.RawMessage `json:"max"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("resolume: decoding ParamRange: %w", err)
	}
	p.ID = raw.ID

	p.ValuePresence = presenceOfRaw(raw.Value)
	p.Value = 0
	if p.ValuePresence == PresencePresent {
		if err := json.Unmarshal(raw.Value, &p.Value); err != nil {
			return fmt.Errorf("resolume: decoding ParamRange value: %w", err)
		}
	}

	p.MinPresence = presenceOfRaw(raw.Min)
	p.Min = 0
	if p.MinPresence == PresencePresent {
		if err := json.Unmarshal(raw.Min, &p.Min); err != nil {
			return fmt.Errorf("resolume: decoding ParamRange min: %w", err)
		}
	}

	p.MaxPresence = presenceOfRaw(raw.Max)
	p.Max = 0
	if p.MaxPresence == PresencePresent {
		if err := json.Unmarshal(raw.Max, &p.Max); err != nil {
			return fmt.Errorf("resolume: decoding ParamRange max: %w", err)
		}
	}
	return nil
}

// ParamString models a `ParamString` leaf. See [ParamBoolean]'s own doc
// comment; the shape and the reasoning for ValuePresence are identical.
type ParamString struct {
	ID            ParameterID
	Value         string
	ValuePresence Presence
}

func (p *ParamString) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID    ParameterID     `json:"id"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("resolume: decoding ParamString: %w", err)
	}
	p.ID = raw.ID
	p.ValuePresence = presenceOfRaw(raw.Value)
	p.Value = ""
	if p.ValuePresence != PresencePresent {
		return nil
	}
	if err := json.Unmarshal(raw.Value, &p.Value); err != nil {
		return fmt.Errorf("resolume: decoding ParamString value: %w", err)
	}
	return nil
}

// ParamChoice models a `ParamChoice` leaf, e.g. a clip's `transporttype`.
// Options is carried inline and is NOT constant across objects of the
// same kind (capture: two clips in one composition advertise different
// transporttype option lists), so nothing in this package hard-codes an
// enum from it. See [ParamBoolean]'s own doc comment for ValuePresence.
type ParamChoice struct {
	ID            ParameterID
	Value         string
	Options       []string
	ValuePresence Presence
}

func (p *ParamChoice) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID      ParameterID     `json:"id"`
		Value   json.RawMessage `json:"value"`
		Options []string        `json:"options"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("resolume: decoding ParamChoice: %w", err)
	}
	p.ID = raw.ID
	p.Options = raw.Options
	p.ValuePresence = presenceOfRaw(raw.Value)
	p.Value = ""
	if p.ValuePresence != PresencePresent {
		return nil
	}
	if err := json.Unmarshal(raw.Value, &p.Value); err != nil {
		return fmt.Errorf("resolume: decoding ParamChoice value: %w", err)
	}
	return nil
}

// ParamState models a `ParamState` leaf, e.g. `connected`. The capture is
// explicit that this is a five-state value for a clip
// (Empty|Disconnected|Previewing|Connected|Connected & previewing) and a
// DIFFERENT three-state value for a column
// (Empty|Disconnected|Connected) — Options is read from the wire on every
// object rather than assumed constant, and Value is carried as the raw
// string rather than reduced to a bool anywhere in this package. See
// [ParamBoolean]'s own doc comment for ValuePresence.
type ParamState struct {
	ID            ParameterID
	Value         string
	Options       []string
	ValuePresence Presence
}

func (p *ParamState) UnmarshalJSON(data []byte) error {
	var raw struct {
		ID      ParameterID     `json:"id"`
		Value   json.RawMessage `json:"value"`
		Options []string        `json:"options"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("resolume: decoding ParamState: %w", err)
	}
	p.ID = raw.ID
	p.Options = raw.Options
	p.ValuePresence = presenceOfRaw(raw.Value)
	p.Value = ""
	if p.ValuePresence != PresencePresent {
		return nil
	}
	if err := json.Unmarshal(raw.Value, &p.Value); err != nil {
		return fmt.Errorf("resolume: decoding ParamState value: %w", err)
	}
	return nil
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

// --- Presence-wrapped parameter-envelope fields (Track D seam D-2) ---------
//
// D-1 only ever needed the null-vs-absent distinction for two leaves
// (active_clip, transport.controls), so ActiveClipField and
// ClipTransportControls were hand-written one at a time. D-2 reads
// `bypassed`, `master`, `video.opacity`, `selected`, `connected`,
// `transporttype`, and every object's `name` — every one of them a
// ParamBoolean/ParamRange/ParamString/ParamChoice/ParamState leaf where
// the identical "ma": null defect applies: a bare (non-pointer,
// non-Unmarshaler) struct field left untouched by encoding/json when the
// key's value is the JSON literal null, indistinguishable from the key
// never having appeared at all. `bypassed` is TRACK-D-D2-SPEC.md §2's
// named worst case — "bypassed": null decoding to false would report a
// dark layer as ready — but the same defect is equally live in the other
// six.
//
// presenceFieldValue is the one decode body all five *Field types below
// call, rather than each hand-copying ActiveClipField.UnmarshalJSON's
// logic. That duplication was the more "obvious" choice and was rejected
// specifically because five independent copies of the same three-line
// null check is how one of the five quietly drifts from the others —
// which is exactly the kind of defect this package's own guard tests
// exist to catch mechanically instead of by re-reading five methods every
// review. The five callers stay distinct named Go types on purpose (see
// ParamChoice/ParamState's own doc comment on why they are not merged
// despite an identical wire shape): sharing a decode function is not the
// same as sharing an identity, and nothing here makes it possible to
// compare a ParamBooleanField to a ParamStateField by accident.
func presenceFieldValue[T any](data []byte) (Presence, *T, error) {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return PresenceNull, nil, nil
	}
	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return PresenceAbsent, nil, err
	}
	return PresencePresent, &v, nil
}

// ParamBooleanField decodes a `ParamBoolean` leaf (e.g. `bypassed`,
// `selected`) so PresenceAbsent, PresenceNull, and PresencePresent are
// three distinguishable outcomes — see this file's own Presence doc
// comment and, for why this matters most here, the doc comment on this
// section above. Param is non-nil only when Presence == PresencePresent,
// matching ActiveClipField's own shape.
type ParamBooleanField struct {
	Presence Presence
	Param    *ParamBoolean
}

// UnmarshalJSON is invoked by encoding/json only when this field's key IS
// present in the document — see ActiveClipField.UnmarshalJSON's doc
// comment for why an absent key never reaches this method at all, leaving
// the field at its Go zero value {PresenceAbsent, nil}.
func (f *ParamBooleanField) UnmarshalJSON(data []byte) error {
	p, v, err := presenceFieldValue[ParamBoolean](data)
	if err != nil {
		return fmt.Errorf("resolume: decoding ParamBoolean field: %w", err)
	}
	f.Presence, f.Param = p, v
	return nil
}

// Bool returns f's boolean value together with whether it is safe to trust
// as a real, measured value — the ONLY accessor this package's own
// consumers (readiness.go, action_dispatch.go, collector.go) may use, never
// f.Param.Value directly. ok is true only when BOTH levels of presence are
// PresencePresent: the envelope itself (f.Presence) AND, per this file's
// own top comment, the "value" key inside it (f.Param.ValuePresence). Any
// other combination — envelope absent, envelope null, or an envelope that
// arrived with no "value" key or an explicit "value": null — returns
// ok=false, never a value a caller could mistake for a measured false.
func (f ParamBooleanField) Bool() (value bool, ok bool) {
	if f.Presence != PresencePresent || f.Param == nil || f.Param.ValuePresence != PresencePresent {
		return false, false
	}
	return f.Param.Value, true
}

// ParamRangeField decodes a `ParamRange` leaf (e.g. `master`,
// `video.opacity`). See ParamBooleanField's doc comment; the shape and the
// reasoning are identical.
type ParamRangeField struct {
	Presence Presence
	Param    *ParamRange
}

func (f *ParamRangeField) UnmarshalJSON(data []byte) error {
	p, v, err := presenceFieldValue[ParamRange](data)
	if err != nil {
		return fmt.Errorf("resolume: decoding ParamRange field: %w", err)
	}
	f.Presence, f.Param = p, v
	return nil
}

// Float returns f's numeric value together with whether it is safe to
// trust as a real, measured value. See [ParamBooleanField.Bool]'s doc
// comment; the shape and the reasoning are identical, applied to the
// "value" key's own presence rather than min/max's — [Range] is the
// accessor for a parameter's own declared bounds.
func (f ParamRangeField) Float() (value float64, ok bool) {
	if f.Presence != PresencePresent || f.Param == nil || f.Param.ValuePresence != PresencePresent {
		return 0, false
	}
	return f.Param.Value, true
}

// Range returns f's declared [min, max] bound together with whether BOTH
// ends are safe to trust as real, measured values — Track D seam D-3's own
// setLayerMaster range-validation accessor (TRACK-D-D3-SPEC.md). ok is
// false whenever the envelope itself is not present, or when either min or
// max individually was absent or null: a caller must never validate
// against a half-known bound (e.g. a known min paired with an unknown, and
// therefore assumed-zero, max) — see [ParamBooleanField.Bool]'s doc
// comment for the identical reasoning applied to "value".
func (f ParamRangeField) Range() (min, max float64, ok bool) {
	if f.Presence != PresencePresent || f.Param == nil {
		return 0, 0, false
	}
	if f.Param.MinPresence != PresencePresent || f.Param.MaxPresence != PresencePresent {
		return 0, 0, false
	}
	return f.Param.Min, f.Param.Max, true
}

// ParamStringField decodes a `ParamString` leaf (e.g. an object's `name`).
// See ParamBooleanField's doc comment; the shape and the reasoning are
// identical.
type ParamStringField struct {
	Presence Presence
	Param    *ParamString
}

func (f *ParamStringField) UnmarshalJSON(data []byte) error {
	p, v, err := presenceFieldValue[ParamString](data)
	if err != nil {
		return fmt.Errorf("resolume: decoding ParamString field: %w", err)
	}
	f.Presence, f.Param = p, v
	return nil
}

// String returns f's string value together with whether it is safe to
// trust as a real, measured value. See [ParamBooleanField.Bool]'s doc
// comment; the shape and the reasoning are identical.
func (f ParamStringField) String() (value string, ok bool) {
	if f.Presence != PresencePresent || f.Param == nil || f.Param.ValuePresence != PresencePresent {
		return "", false
	}
	return f.Param.Value, true
}

// ParamChoiceField decodes a `ParamChoice` leaf (e.g. a clip's
// `transporttype`). See ParamBooleanField's doc comment; the shape and the
// reasoning are identical. Options is carried on the decoded ParamChoice
// verbatim, per this file's own ParamChoice doc comment: it is not
// constant across objects of the same kind.
type ParamChoiceField struct {
	Presence Presence
	Param    *ParamChoice
}

func (f *ParamChoiceField) UnmarshalJSON(data []byte) error {
	p, v, err := presenceFieldValue[ParamChoice](data)
	if err != nil {
		return fmt.Errorf("resolume: decoding ParamChoice field: %w", err)
	}
	f.Presence, f.Param = p, v
	return nil
}

// String returns f's selected-option string together with whether it is
// safe to trust as a real, measured value. See [ParamBooleanField.Bool]'s
// doc comment; the shape and the reasoning are identical. Deliberately
// named the same as [ParamStateField.String] despite the two never being
// comparable types (this file's own ParamChoice/ParamState doc comment).
func (f ParamChoiceField) String() (value string, ok bool) {
	if f.Presence != PresencePresent || f.Param == nil || f.Param.ValuePresence != PresencePresent {
		return "", false
	}
	return f.Param.Value, true
}

// ParamStateField decodes a `ParamState` leaf — this package's only use of
// it today is a clip's `connected`, the five-state value capture section
// 4.3 and section 8.1 are both explicit must never be reduced to a bool.
// See ParamBooleanField's doc comment; the shape and the reasoning are
// identical.
type ParamStateField struct {
	Presence Presence
	Param    *ParamState
}

func (f *ParamStateField) UnmarshalJSON(data []byte) error {
	p, v, err := presenceFieldValue[ParamState](data)
	if err != nil {
		return fmt.Errorf("resolume: decoding ParamState field: %w", err)
	}
	f.Presence, f.Param = p, v
	return nil
}

// String returns f's five-state (or, for a column, three-state) string
// value together with whether it is safe to trust as a real, measured
// value. See [ParamBooleanField.Bool]'s doc comment; the shape and the
// reasoning are identical. Still never reduced to a bool — this file's own
// ParamState doc comment.
func (f ParamStateField) String() (value string, ok bool) {
	if f.Presence != PresencePresent || f.Param == nil || f.Param.ValuePresence != PresencePresent {
		return "", false
	}
	return f.Param.Value, true
}

// --- Targeted by-id decode types (Track D seam D-2) -------------------------
//
// Each type below decodes exactly the leaves TRACK-D-D2-SPEC.md §5's
// signal table and §3.7's readiness conjunction need, out of a `by-id`
// response body — never the full composition document (see this file's
// own top-of-file doc comment and guardfullcomposition_test.go). Every
// leaf that matters to a conjunction, a displayed value, or an identity
// check is one of the *Field types above, never a bare Param* type,
// precisely so a Resolume-side null cannot silently read as this package's
// zero value.

// layerVideo is a layer's `video` object, holding only `opacity` — the
// single nested leaf D-2 reads. Kept as its own unexported type, addressed
// through Layer.VideoOpacity, rather than flattening "video.opacity" onto
// Layer directly, because encoding/json has no dotted-path tag syntax and
// the response genuinely nests it this way.
type layerVideo struct {
	Opacity ParamRangeField `json:"opacity"`
}

// Layer is `GET /composition/layers/by-id/{id}`'s targeted decode: capture
// section 4.1's cheap-reads table measured this endpoint's real body at
// 62,795 bytes — large because a layer object carries its whole clip grid
// alongside the handful of fields this type actually reads — but there is
// no partial or projected read (capture section 4.1), so the full body is
// still read and this type simply ignores everything it does not name.
// Layer ids are deck-independent (capture section 16.1, 18/18): a caller
// of [Client.Layer] need not know or check which deck is selected first.
type Layer struct {
	ID ObjectID `json:"id"`

	// Bypassed and Master are two of TRACK-D-D2-SPEC.md §3.7's / capture
	// section 8.1's readiness-conjunction terms. Capture section 8.1
	// measured directly that a clip on a bypassed layer, or a layer at
	// master 0, still reports `connected: "Connected"` with an active
	// clip present — bypassed/master are the ONLY fields that reveal that
	// failure; nothing on the clip does.
	Bypassed ParamBooleanField `json:"bypassed"`
	Master   ParamRangeField   `json:"master"`

	// Solo is readable on both Layer and LayerGroup (Arena's own schema).
	// It is deliberately NOT folded into TRACK-D-D3-SPEC.md §3.7's seven-term
	// conjunction as an eighth term: standard mixer semantics would have a
	// solo ANYWHERE silence every non-soloed layer, but Arena's own
	// specification is silent on the mechanism, so this package has no
	// confirmed rule to encode. See [ApplySoloOverride] (readiness.go) for
	// the honest version this package DOES implement: a survey already
	// reads every layer, so the data is cheap to have in hand even though
	// the conjunction itself does not consume it directly.
	Solo ParamBooleanField `json:"solo"`

	// Video carries video.opacity, the third per-layer conjunction term.
	// Unexported and reached through VideoOpacity() rather than exposed
	// directly, because the "video" object is itself the nesting point —
	// see VideoOpacity's own doc comment for what "video" being absent or
	// null does to the leaf's reported Presence.
	Video *layerVideo `json:"video"`

	// Transition carries transition.duration — Track D seam D-3's own
	// addition, not read by anything in D-2. Bench capture §7.2 measured
	// this ParamRange directly: driving it from 0.0s to 5.0s moved the
	// observed time to reach a disconnected clip from 75ms to 4,068ms, a
	// 35x spread that is TRACK-D-D3-SPEC.md §3.3's own evidence that a
	// clearLayer/blackout confirmation deadline must be DERIVED from this
	// field, never a constant. Unexported and reached through
	// TransitionDuration() for the identical reason Video/VideoOpacity()
	// is: "transition" is itself the nesting point, and a null or absent
	// "transition" object must report the same PresenceAbsent a caller
	// would get from a genuinely absent leaf, not a decode error.
	Transition *layerTransition `json:"transition"`

	// ActiveClip reuses D-1's ActiveClipField unchanged: what is playing
	// on this layer, or the explicit absence of anything (capture section
	// 4.4 — JSON null, never an absent key, in every case the capture
	// observed).
	ActiveClip ActiveClipField `json:"active_clip"`

	Name ParamStringField `json:"name"`
}

// VideoOpacity returns this layer's video.opacity leaf. A pointer field
// (Video) already makes encoding/json treat a null "video" object the
// same as an absent one — both leave the Go pointer nil — so both
// collapse to PresenceAbsent here. That is a real (if minor) loss of
// distinction between "video was absent" and "video was null", but the
// capture never lists the "video" container itself as a signal anything
// reads; the leaf this package's callers actually need is video.opacity,
// and PresenceAbsent is the correct, honest answer for it in both of
// those outer cases: opacity's value is not known either way. When
// "video" is present as an object, this returns exactly what its own
// "opacity" key decoded to, including PresenceNull if Resolume sent
// "opacity": null.
func (l Layer) VideoOpacity() ParamRangeField {
	if l.Video == nil {
		return ParamRangeField{}
	}
	return l.Video.Opacity
}

// layerTransition is a layer's `transition` object, holding only
// `duration` — the single nested leaf Track D seam D-3 reads. See
// [layerVideo]'s own doc comment; the shape and the reasoning for keeping
// this as its own unexported type are identical.
type layerTransition struct {
	Duration ParamRangeField `json:"duration"`
}

// TransitionDuration returns this layer's transition.duration leaf. See
// [Layer.VideoOpacity]'s own doc comment: a nil Transition (the "transition"
// object absent or null) and a present Transition whose own "duration" key
// is absent or null both collapse to PresenceAbsent/PresenceNull exactly as
// that method's identical case does, for the identical reason — a pointer
// field already makes encoding/json treat a null "transition" object the
// same as an absent one.
func (l Layer) TransitionDuration() ParamRangeField {
	if l.Transition == nil {
		return ParamRangeField{}
	}
	return l.Transition.Duration
}

// LayerGroup is `GET /composition/layergroups/by-id/{id}`'s targeted
// decode: the containing group's own two readiness-conjunction terms
// (capture section 8, "layergroups[g].bypassed, .master, .solo … the
// containing group").
type LayerGroup struct {
	ID       ObjectID          `json:"id"`
	Bypassed ParamBooleanField `json:"bypassed"`
	Master   ParamRangeField   `json:"master"`

	// Solo: see [Layer.Solo]'s own doc comment; the reasoning is identical.
	Solo ParamBooleanField `json:"solo"`
}

// Deck is `GET /composition/decks/by-id/{id}`'s targeted decode. Selected
// is what TRACK-D-D2-SPEC.md §6.4's deck term and §6's identity check both
// depend on (capture section 9.2's composition.decks array carries the
// identical id/name/selected shape; this reads the same fields by-id
// instead).
type Deck struct {
	ID       ObjectID          `json:"id"`
	Selected ParamBooleanField `json:"selected"`
	Name     ParamStringField  `json:"name"`
}

// Clip is `GET /composition/clips/by-id/{id}`'s targeted decode. Capture
// section 16.1 is the load-bearing warning for every caller of
// [Client.Clip]: a clip id resolves only while its OWN deck is selected
// (30/30 selected-deck ids resolved; 0/10 non-selected-deck ids did, all
// 404) — a caller must know which deck a stored clip id belongs to before
// treating this method's 404 as anything other than a possible deck
// mismatch. PersistentClips (capture section 16.2) are the sole exception
// and resolve regardless of selected deck.
type Clip struct {
	ID ObjectID `json:"id"`

	// Connected is the five-state value capture sections 4.3 and 8.1 both
	// warn must never be reduced to a bool — "Connected & previewing" is
	// real and distinct from "Connected", and capture 8.1 measured that
	// EITHER one is reported while a clip's own layer is bypassed or at
	// master 0. Connected alone is not evidence anything reached the
	// wall; TRACK-D-D2-SPEC.md §3.7's conjunction is what this package's
	// layer/layergroup reads exist to complete.
	Connected ParamStateField `json:"connected"`

	// Transport carries transport.position and transport.controls,
	// reusing D-1's ClipTransport/ClipTransportControls unchanged (capture
	// section 11.3: position stays readable under every transport type,
	// controls is null under SMPTE).
	Transport ClipTransport `json:"transport"`

	// TransportType is the DIFFERENT top-level `transporttype` field
	// (capture section 8/9.4: a first-class ParamChoice, options
	// Timeline/BPM Sync/SMPTE 1/SMPTE 2/Denon DJ/Pioneer DJ, confirmed
	// both readable and writable — this package only ever reads it) that
	// TRACK-D-D2-SPEC.md §5 assigns to resolume.clip.<id>.transporttype,
	// feeding the parent specification's §8 drift check. Not to be
	// confused with Transport above, a different field with a similar
	// name.
	TransportType ParamChoiceField `json:"transporttype"`

	Name ParamStringField `json:"name"`
}
