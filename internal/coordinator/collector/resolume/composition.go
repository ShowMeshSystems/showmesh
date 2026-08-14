package resolume

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// This file decodes ONLY what seam D-1 needs from GET /composition:
// object ids and the closed set of parameters resolve.go indexes, plus
// two fields (layers[].active_clip and clip.transport.controls) that
// exist here purely so this package never repeats the "ma": null defect
// (CLAUDE.md) in a second vendor's API — see ActiveClipField and
// ClipTransportControls below. Nothing about effects, dashboards, audio,
// or the crossfader is modeled. layergroups[].layers is decoded down to
// member layer ids only — never the full duplicate layer objects the
// capture found nested there (49% of the whole payload, the same layer
// set already available from the top-level layers array).
//
// Every field below is traceable to something the bench capture actually
// observed (docs/bench/resolume-control-surface.md, sections 4, 8, 9, 11
// — see this package's doc comment for why that citation belongs in a
// comment, never in an operator-facing string): "by-id" addressing for
// clips, layers, layergroups, columns and decks implies each carries a
// top-level object "id" the way the capture shows explicitly for decks;
// every other field name here (bypassed, master, video.opacity,
// transition.duration, connected, transporttype, active_clip,
// transport.position, transport.controls, closed, selected) is named
// directly in the capture's own text or JSON excerpts.

// --- The parameter envelope (capture section 4.3) --------------------------
//
// Every leaf in a Resolume composition is an object, never a bare value.
// The four types below are this package's minimal model of that envelope
// — just ID and Value, dropping min/max/in/out (ParamRange) and index
// (ParamChoice/ParamState) that D-1 has no use for yet. ParamChoice and
// ParamState carry an identical wire shape but are kept as distinct Go
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
//
// SCOPE NOTE (review finding C, 2026-08-14): active_clip and
// transport.controls are the ONLY two leaves modelled through Presence.
// Every other leaf in this file — every ParamBoolean, ParamRange,
// ParamString, ParamChoice, and ParamState field — still collapses a null
// envelope and an absent key to that struct's Go zero value, exactly the
// pattern this type exists to avoid. That is harmless in D-1 today only
// because resolve.go's parameter index separately refuses to index a zero
// ParameterID (see index()'s own doc comment) — nothing in THIS seam ever
// reads one of those zero-valued leaves as a boolean, range, or string. It
// stops being harmless in D-2, where a leaf like a layer's `bypassed`
// would be READ, not merely indexed: a null-envelope `"bypassed": null`
// collapsing to `false` would make the §3.7 readiness conjunction report a
// layer healthy when Resolume never actually told this package whether it
// is bypassed. Do not model every leaf through Presence pre-emptively here
// — that is D-2's decision, made against D-2's actual read sites — but
// whoever writes D-2's readiness conjunction must revisit this.
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

// --- Clips -------------------------------------------------------------------

// ActiveClip is layers[i].active_clip's value when present. Deliberately
// minimal: the only field the capture and the adapter spec's confirmation
// rule ("the owning layer's active_clip.id == id") name is the clip's
// object id, so that is the only field decoded here. No other property of
// this object was observed.
type ActiveClip struct {
	ID ObjectID `json:"id"`
}

// ActiveClipField is layers[i].active_clip, decoded so that
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

// ClipTransportControls is clip.transport.controls: an object under
// Timeline/BPM Sync transport, and JSON null under SMPTE transport
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

// ClipTransport is clip.transport: readable under every transport type
// (capture section 11.3 — position remains readable even under SMPTE).
type ClipTransport struct {
	Position ParamRange            `json:"position"`
	Controls ClipTransportControls `json:"controls"`
}

// Clip is one cell of a layer's clip grid (layers[i].clips[j]). Only the
// two parameters resolve.go's closed index names are decoded as indexed
// fields; Transport is decoded alongside them purely for the null-safety
// this file exists to demonstrate (see ClipTransportControls), not
// because D-1 indexes it.
type Clip struct {
	ID            ObjectID      `json:"id"`
	Connected     ParamState    `json:"connected"`
	TransportType ParamChoice   `json:"transporttype"`
	Transport     ClipTransport `json:"transport"`
}

// --- Layers, layer groups, columns, decks -----------------------------------

// layerVideo is layers[i].video, narrowed to the one field D-1 indexes.
type layerVideo struct {
	Opacity ParamRange `json:"opacity"`
}

// layerTransition is layers[i].transition, narrowed to the one field D-1
// indexes.
type layerTransition struct {
	Duration ParamRange `json:"duration"`
}

// Layer is one entry of the top-level composition.layers array (never the
// duplicate nested inside layergroups[].layers — see layerGroupMember).
type Layer struct {
	ID         ObjectID        `json:"id"`
	Bypassed   ParamBoolean    `json:"bypassed"`
	Master     ParamRange      `json:"master"`
	Video      layerVideo      `json:"video"`
	Transition layerTransition `json:"transition"`
	ActiveClip ActiveClipField `json:"active_clip"`
	Clips      []Clip          `json:"clips"`
}

// layerGroupMember decodes one entry of layergroups[].layers down to just
// its object id. This is deliberate data loss: the capture found that
// array to be a byte-for-byte duplicate of the top-level layers array
// (same id set, 49% of the whole ~2.26 MB payload), so decoding it in
// full would parse the entire show twice for zero new information.
// encoding/json only allocates for fields present in the destination
// struct, so this type is what keeps that duplication from costing
// anything beyond the unavoidable cost of skipping past the bytes on the
// wire.
type layerGroupMember struct {
	ID ObjectID `json:"id"`
}

// LayerGroup is one entry of composition.layergroups, narrowed to the
// group's own id/name/master/bypassed and its member layer ids — see
// layerGroupMember.
type LayerGroup struct {
	ID       ObjectID           `json:"id"`
	Name     ParamString        `json:"name"`
	Bypassed ParamBoolean       `json:"bypassed"`
	Master   ParamRange         `json:"master"`
	Layers   []layerGroupMember `json:"layers"`
}

// Column is one entry of composition.columns, narrowed to the one
// parameter D-1 indexes. `connected` here is the column's own
// three-state ParamState (Empty|Disconnected|Connected) — a DIFFERENT
// option set from a clip's five-state `connected` (capture section 4.3)
// even though both decode through the identical ParamState Go type.
type Column struct {
	ID        ObjectID   `json:"id"`
	Connected ParamState `json:"connected"`
}

// Deck is one entry of composition.decks (capture section 9.2).
type Deck struct {
	ID       ObjectID     `json:"id"`
	Closed   bool         `json:"closed"`
	Name     ParamString  `json:"name"`
	Selected ParamBoolean `json:"selected"`
}

// --- The composition itself ---------------------------------------------

// Composition is the minimal decode of GET /composition — see this file's
// package-level comment for the full "what is and is not decoded" list.
// The real object also carries audio, clipbeatsnap, cliptarget,
// cliptriggerstyle, crossfader, dashboard, selected, speed, and
// tempocontroller top-level keys (capture section 12.3's key inventory);
// none of them is named by anything D-1 needs, so none is modeled here.
type Composition struct {
	Name        ParamString  `json:"name"`
	Bypassed    ParamBoolean `json:"bypassed"`
	Master      ParamRange   `json:"master"`
	Layers      []Layer      `json:"layers"`
	LayerGroups []LayerGroup `json:"layergroups"`
	Columns     []Column     `json:"columns"`
	Decks       []Deck       `json:"decks"`
}
