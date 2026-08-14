package resolume

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// ObjectKind labels which Resolume object family an object or parameter
// belongs to, for [Resolution.ParameterID]'s lookup key.
type ObjectKind string

const (
	ObjectKindClip        ObjectKind = "clip"
	ObjectKindLayer       ObjectKind = "layer"
	ObjectKindLayerGroup  ObjectKind = "layergroup"
	ObjectKindColumn      ObjectKind = "column"
	ObjectKindDeck        ObjectKind = "deck"
	ObjectKindComposition ObjectKind = "composition"
)

// ObjectID is a Resolume object identifier: persisted in the composition
// file, and confirmed stable across a restart and across a reorder. Safe
// to hold in memory for the life of a held [Resolution]; whether it is
// ever safe to persist elsewhere (SQLite, a config revision, an export
// bundle) is a later seam's decision, not this package's.
type ObjectID int64

func (o ObjectID) String() string { return strconv.FormatInt(int64(o), 10) }

// compositionObjectID is the reserved stand-in [ObjectID] used to index
// the composition's own parameters (master, bypassed, name), because the
// composition itself carries no object id at all over REST — only a
// mutable `name`. Every object id actually observed in the capture is a
// 13-digit creation timestamp (the smallest is in the 1.6e12 range), so
// zero can never collide with a real one.
const compositionObjectID ObjectID = 0

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

// parameterKey is the lookup key for one indexed parameter: which kind of
// object, which specific object (compositionObjectID for the one
// composition-scoped case), and the parameter's dotted name as used in
// this package's closed index (see Resolve).
type parameterKey struct {
	kind ObjectKind
	obj  ObjectID
	name string
}

// ClipScope states which deck a [Resolution]'s clip ids and per-clip
// parameter index actually cover. GET /composition returns only the
// currently selected deck's clip grid — there is no way to read a deck
// that is not selected — so a Resolution's clip-related data is never a
// complete enumeration of every clip in the composition, only of the one
// deck that was selected at the moment it was resolved. This type exists
// so that scope is carried as data a caller can inspect, not only stated
// in a doc comment a caller might not read.
type ClipScope struct {
	DeckID   ObjectID
	DeckName string
}

// Resolution is one point-in-time resolution of a fetched [Composition]:
// every object id this package tracks, by kind, and a closed set of
// parameter ids reachable only through [Resolution.ParameterID].
//
// A Resolution is never diffed, merged, or updated in place. [Resolve]
// always builds a complete new one from a freshly fetched Composition,
// and [Adapter] always replaces its held Resolution wholesale — see
// Adapter's doc comment for why, and this package's doc comment for the
// parameter-id lifecycle rule that makes partial updates unsafe.
//
// The parameter index is a CLOSED set, not every parameter the
// composition carries. Exactly these are indexed, because each is named
// as load-bearing by the adapter specification and nothing else is:
//
//   - composition: master, bypassed, name
//   - each layer: master, bypassed, video.opacity, transition.duration
//   - each layergroup: master, bypassed
//   - each clip (in the selected deck's grid only — see ClipScope):
//     connected, transporttype
//   - each column: connected
//   - each deck: selected
type Resolution struct {
	ResolvedAt time.Time

	clipIDs       []ObjectID
	layerIDs      []ObjectID
	layerGroupIDs []ObjectID
	columnIDs     []ObjectID
	deckIDs       []ObjectID

	SelectedDeckID   ObjectID
	SelectedDeckName string

	params map[parameterKey]ParameterID
}

// ClipIDs returns the object ids of every clip slot found in the
// SELECTED deck's grid only — see [ClipScope] and [Resolution.ClipScope].
// The returned slice is a copy; mutating it does not affect the
// Resolution.
func (r *Resolution) ClipIDs() []ObjectID { return append([]ObjectID(nil), r.clipIDs...) }

// LayerIDs returns every layer object id found in the composition. Layer
// identity is deck-independent (capture section 9.4: the same 18 layers
// exist under every deck), so unlike ClipIDs this is not scoped to the
// selected deck.
func (r *Resolution) LayerIDs() []ObjectID { return append([]ObjectID(nil), r.layerIDs...) }

// LayerGroupIDs returns every layer group object id found in the
// composition.
func (r *Resolution) LayerGroupIDs() []ObjectID {
	return append([]ObjectID(nil), r.layerGroupIDs...)
}

// ColumnIDs returns the object ids of every column in the SELECTED deck's
// grid only — see [ClipScope]. The column count itself differs per deck
// (capture section 9.4: 14, 9, and 9 on three different decks of one real
// composition).
func (r *Resolution) ColumnIDs() []ObjectID { return append([]ObjectID(nil), r.columnIDs...) }

// DeckIDs returns every deck object id found in the composition.
func (r *Resolution) DeckIDs() []ObjectID { return append([]ObjectID(nil), r.deckIDs...) }

// ClipScope reports which deck this Resolution's clip ids and per-clip
// parameter index actually cover — see [ClipScope]'s own doc comment.
func (r *Resolution) ClipScope() ClipScope {
	return ClipScope{DeckID: r.SelectedDeckID, DeckName: r.SelectedDeckName}
}

// ParameterID looks up the parameter id for one indexed parameter. ok is
// false for anything outside the closed set this package's doc comment
// enumerates, including a syntactically plausible name this package
// simply never indexed (e.g. a layer's "solo") and an object id this
// Resolution never saw.
func (r *Resolution) ParameterID(kind ObjectKind, obj ObjectID, name string) (ParameterID, bool) {
	id, ok := r.params[parameterKey{kind: kind, obj: obj, name: name}]
	return id, ok
}

// index records one parameter id under its lookup key — UNLESS id is the
// zero value, in which case nothing is recorded and the key stays
// unresolved (review finding C, 2026-08-14).
//
// A zero ParameterID is never a real Resolume parameter id: every
// parameter id this package has ever decoded off the wire is a large
// per-session-minted number (the bench capture's own examples are all in
// the 1.78e12 range — docs/bench/resolume-control-surface.md section 3.2
// — see this package's doc comment for why that citation belongs only in
// a comment, never in an operator-facing string), the same reasoning
// compositionObjectID's own doc comment already relies on for object ids.
// encoding/json leaves a struct field's ParameterID at its Go zero value
// BOTH when the parameter's own JSON key is absent from its envelope
// entirely and when the envelope is present but an explicit JSON null
// (capture sections 4.4 and 11.3) — composition.go's Presence type solves
// this for the two leaves this package actually reads (active_clip,
// transport.controls), but every OTHER envelope this file indexes
// (master, bypassed, name, video.opacity, transition.duration, connected,
// transporttype, selected) is still a bare struct with no Presence
// tracking (see composition.go's SCOPE NOTE). Indexing id 0 anyway would
// make [Resolution.ParameterID] report ok=true for a parameter Resolume
// never actually sent — CLAUDE.md's "ma": null defect reproduced here as
// a fabricated RESOLVED ID rather than a fabricated reading. Skipping it
// is what makes the lookup answer ok=false instead, which is what
// [Resolution.ParameterID]'s own doc comment already promises for
// "an object id this Resolution never saw" — a zero id is exactly that
// case, restated.
//
// Unexported: the closed set this file builds in Resolve is the only
// place new entries are ever added, per this type's own "closed set" doc
// comment — a caller outside this file has no way to widen the index.
func (r *Resolution) index(kind ObjectKind, obj ObjectID, name string, id ParameterID) {
	if id == 0 {
		return
	}
	r.params[parameterKey{kind: kind, obj: obj, name: name}] = id
}

// Resolve builds a [Resolution] from an already-fetched [Composition].
// Pure: it reads comp and now, and returns a new Resolution or an error;
// it performs no I/O and holds no state of its own.
//
// now is stamped as ResolvedAt; it is the caller's clock, not time.Now,
// so this stays deterministic under test — the same discipline
// internal/coordinator/collector/fpp.Collector uses its own injected Now
// for.
func Resolve(comp *Composition, now time.Time) (*Resolution, error) {
	if comp == nil {
		return nil, fmt.Errorf("resolume: Resolve: composition is nil")
	}

	r := &Resolution{
		ResolvedAt: now,
		params:     make(map[parameterKey]ParameterID),
	}

	// Composition-level parameters. See compositionObjectID's doc comment
	// for why the composition itself has no real object id to index
	// under.
	r.index(ObjectKindComposition, compositionObjectID, "master", comp.Master.ID)
	r.index(ObjectKindComposition, compositionObjectID, "bypassed", comp.Bypassed.ID)
	r.index(ObjectKindComposition, compositionObjectID, "name", comp.Name.ID)

	for _, l := range comp.Layers {
		r.layerIDs = append(r.layerIDs, l.ID)
		r.index(ObjectKindLayer, l.ID, "master", l.Master.ID)
		r.index(ObjectKindLayer, l.ID, "bypassed", l.Bypassed.ID)
		r.index(ObjectKindLayer, l.ID, "video.opacity", l.Video.Opacity.ID)
		r.index(ObjectKindLayer, l.ID, "transition.duration", l.Transition.Duration.ID)

		// Clips live only inside a layer's own clip grid — see
		// Composition's doc comment: there is no flat top-level clips
		// array. This range, across every layer of the top-level (never
		// the duplicate nested) layers array, is what populates clipIDs
		// and the clip parameter index, and it is scoped to whichever
		// deck was selected when comp was fetched — see ClipScope.
		for _, c := range l.Clips {
			r.clipIDs = append(r.clipIDs, c.ID)
			r.index(ObjectKindClip, c.ID, "connected", c.Connected.ID)
			r.index(ObjectKindClip, c.ID, "transporttype", c.TransportType.ID)
		}
	}

	for _, g := range comp.LayerGroups {
		r.layerGroupIDs = append(r.layerGroupIDs, g.ID)
		r.index(ObjectKindLayerGroup, g.ID, "master", g.Master.ID)
		r.index(ObjectKindLayerGroup, g.ID, "bypassed", g.Bypassed.ID)
	}

	for _, col := range comp.Columns {
		r.columnIDs = append(r.columnIDs, col.ID)
		r.index(ObjectKindColumn, col.ID, "connected", col.Connected.ID)
	}

	var foundSelected bool
	for _, d := range comp.Decks {
		r.deckIDs = append(r.deckIDs, d.ID)
		r.index(ObjectKindDeck, d.ID, "selected", d.Selected.ID)
		if d.Selected.Value {
			r.SelectedDeckID = d.ID
			r.SelectedDeckName = d.Name.Value
			foundSelected = true
		}
	}
	if !foundSelected {
		return nil, fmt.Errorf("resolume: Resolve: no deck reported selected=true among %d deck(s)", len(comp.Decks))
	}

	return r, nil
}

// ObjectFingerprint is a short, stable hex digest over every DECK-
// INDEPENDENT object id this Resolution holds: layers, layer groups, and
// decks. Two Resolutions built from compositions carrying the same
// objects — even across an Arena restart, where object ids survive but
// parameter ids do not — produce the same ObjectFingerprint. It is not a
// parameter id and may be logged.
//
// CORRECTED (review finding B, 2026-08-14): this fingerprint used to also
// fold in clipIDs and columnIDs. That was wrong, and it was wrong in a way
// that falsified the exact claim this fingerprint exists to let a human
// check: GET /composition returns only the SELECTED deck's clip and
// column grid (see ClipScope), so clip and column ids are not a property
// of the composition as a whole, they are a property of which deck
// happened to be selected at resolve time. Verified live against the
// operator's own composition: with "Main" selected, ObjectFingerprint
// (old, clip/column-inclusive form) read 9aae306b; with "Rest Staging"
// selected on the SAME running composition, it read d1e71589 — a fully
// deck-independent value changing on nothing but a deck switch, which is
// neither a restart nor evidence that any object was replaced. Layer
// identity is deck-independent (capture section 9.4: the same 18 layers
// exist under every deck) — layers, layer groups, and decks are therefore
// the only ids this fingerprint covers. See
// [Resolution.SelectedDeckObjectFingerprint] for the clip/column-scoped
// counterpart this correction split out.
func (r *Resolution) ObjectFingerprint() string {
	return fingerprint(r.deckIndependentObjectIDs())
}

// SelectedDeckObjectFingerprint is a short, stable hex digest over every
// clip and column object id in THIS Resolution's selected deck ONLY (see
// ClipScope and [Resolution.ClipScope]) — precisely the ids
// [Resolution.ObjectFingerprint] deliberately excludes, per its own doc
// comment (review finding B, 2026-08-14).
//
// Two Resolutions differ here whenever a different deck was selected at
// resolve time, EVEN IF nothing about the composition itself changed.
// That is expected, not a defect — it is the reason this fingerprint is
// named and documented separately from ObjectFingerprint rather than
// folded into it: a caller comparing this value across two Resolutions
// must already know, or not care, whether the selected deck was the same
// both times. It is not a parameter id and may be logged.
func (r *Resolution) SelectedDeckObjectFingerprint() string {
	ids := make([]int64, 0, len(r.clipIDs)+len(r.columnIDs))
	for _, id := range r.clipIDs {
		ids = append(ids, int64(id))
	}
	for _, id := range r.columnIDs {
		ids = append(ids, int64(id))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return fingerprint(ids)
}

// ParameterFingerprint is the parameter-id counterpart to
// ObjectFingerprint: a short, stable hex digest over every parameter id
// this Resolution indexed. Two Resolutions with identical object ids but
// different parameter ids — the exact shape of an Arena restart — produce
// the SAME ObjectFingerprint and a DIFFERENT ParameterFingerprint. This
// exists so a human can prove at the console, from two short strings in a
// log line, that object ids survived a restart while parameter ids did
// not, without ever logging a parameter id itself. It is not a parameter
// id and may be logged.
func (r *Resolution) ParameterFingerprint() string {
	ids := make([]int64, 0, len(r.params))
	for _, id := range r.params {
		ids = append(ids, int64(id))
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return fingerprint(ids)
}

// deckIndependentObjectIDs returns every object id this Resolution holds
// EXCEPT clip and column ids (see [Resolution.ObjectFingerprint]'s doc
// comment for why those two are deck-scoped, not composition-scoped),
// sorted for a stable fingerprint regardless of composition array order.
func (r *Resolution) deckIndependentObjectIDs() []int64 {
	all := make([]int64, 0, len(r.layerIDs)+len(r.layerGroupIDs)+len(r.deckIDs))
	for _, id := range r.layerIDs {
		all = append(all, int64(id))
	}
	for _, id := range r.layerGroupIDs {
		all = append(all, int64(id))
	}
	for _, id := range r.deckIDs {
		all = append(all, int64(id))
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	return all
}

// fingerprint hashes a sorted slice of ids into a short, stable, 8-hex-
// character digest. Deterministic under repeated calls with the same ids
// in the same order — callers are responsible for sorting first, exactly
// as allObjectIDs and ParameterFingerprint both already do.
func fingerprint(sortedIDs []int64) string {
	h := sha256.New()
	for _, id := range sortedIDs {
		fmt.Fprintf(h, "%d\n", id)
	}
	return hex.EncodeToString(h.Sum(nil))[:8]
}
