package resolume

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam D-2/B: the bridge between pkg/resolumecomp's
// parsed .avc model (what an operator uploaded) and the in-memory shape
// D-2/C's poll cycle and composition-identity check actually query against
// (TRACK-D-D2-SPEC.md §3, §6). It has no HTTP client, no WebSocket, and no
// awareness of Resolume's REST API at all — everything here is either a
// pure transform of an already-parsed [resolumecomp.Composition], or an
// in-memory cache of the result keyed by config revision number. Nothing
// in this file performs, or could perform, a network call: ADR-032
// decision 2 is a rule about runtime reads of Resolume, and this file sits
// entirely upstream of any such read.
//
// # Why "no composition uploaded yet" is a type, not a bool
//
// CLAUDE.md records the same defect four times over: an absent value that
// decodes, or defaults, to something that LOOKS like a valid empty result
// is read as "checked, and there was nothing" rather than "never checked
// at all." A coordinator that has never had a composition uploaded to it,
// and a coordinator whose uploaded composition genuinely has zero layers,
// must not collapse into the same observable state — the first is
// "nothing to report yet," the second is a real (if strange) show. If
// [CompositionStore.Current] returned an empty *TrackedComposition for
// both, D-2/C's readiness conjunction would read the former as "every
// layer's terms vacuously agree, report ready," exactly the shape of
// defect this file exists to rule out before it can happen. See
// [ErrCompositionNotUploaded] and [CompositionStore.Current]'s own doc
// comments for how that is enforced structurally rather than by
// convention.

// ObjectID conversion note: pkg/resolumecomp deliberately keeps every id
// as a bare string (it is a pure XML parser with no opinion on what a
// caller does with an id — see that package's own doc comment). This
// package's [ObjectID] (composition.go) is the type every REST by-id path
// and every future action addresses Resolume objects by, so
// [BuildTrackedComposition] is the one place a stored id map's ids are
// parsed into that type. Every id in a real .avc file measured so far is a
// base-10 integer (the bench capture's own §15.1 table), so a non-numeric
// id is treated as a build-time error rather than something this package
// tries to route around — half-importing a composition whose ids this
// package cannot even address would be worse than refusing it outright,
// matching pkg/resolumecomp's own ErrMissingDeckID reasoning for the
// identical judgment call.

// ErrInvalidObjectID is returned by [BuildTrackedComposition] when a
// stored composition carries an id this package cannot parse as an
// [ObjectID] (a base-10 integer). Every id observed in a real Resolume
// composition file has been numeric (bench capture §15.1); this exists as
// a build-time refusal rather than a silent skip because an id this
// package cannot address is not a clip, layer, group, or deck this
// package can ever look up over REST — carrying it into
// [TrackedComposition] as a mangled or zero value would manufacture a
// reference that resolves to nothing, or worse, to the wrong object.
var ErrInvalidObjectID = errors.New("resolume: composition id map: id is not a valid Resolume object id (base-10 integer)")

// TrackedDeck is one deck from the uploaded composition file.
type TrackedDeck struct {
	ID   ObjectID
	Name string
}

// TrackedLayerGroup is one layer group from the uploaded composition file.
type TrackedLayerGroup struct {
	ID ObjectID
}

// TrackedColumn is one column from the uploaded composition file — Track D
// seam D-3's own addition, not read by D-2. DeckID names the deck this
// column belongs to (pkg/resolumecomp.Column carries the identical field,
// itself derived from which <Deck> element the file's <Column> was nested
// inside). Unlike [TrackedClip], D-3's own specification (TRACK-D-D3-SPEC.md
// §3.4) states the deck-refusal rule for a CLIP action only — column
// deck-scoping over Resolume's REST API has never been measured the way
// ADR-032 decision 6 measured it for clips (30/30 selected-deck ids
// resolved, 0/10 non-selected-deck ids did), so this package does not
// invent an unmeasured refusal rule for launchColumn from DeckID's mere
// presence here. DeckID is carried anyway because it is already part of
// the stored id map's own shape and a later, evidence-backed seam may need
// it; nothing today reads it to refuse a dispatch.
type TrackedColumn struct {
	ID     ObjectID
	DeckID ObjectID

	// Index is the column's own columnIndex attribute (0-based), exactly as
	// resolumecomp.Column.Index reports it — ADR-037's ColumnLabel generates
	// "Column <n>" from this, never from this slice's own position.
	Index int
}

// TrackedLayer is one layer, deck-independent (bench capture §16.1: all 18
// layer ids in the operator's composition resolved regardless of which
// deck was selected — nothing about that measurement was specific to that
// composition's own layer count).
type TrackedLayer struct {
	ID ObjectID

	// Index is the layer's own layerIndex attribute (0-based), exactly as
	// resolumecomp.Layer.Index reports it — ADR-037's LayerLabel generates
	// "Layer <n>" from this, never from this slice's own position, so a
	// layer that is reordered in a future upload keeps the label its own
	// file states rather than one this package invented from list order.
	Index int

	// Name is the layer's own display name (ADR-037 decision 7), read
	// from resolumecomp.Layer.Name exactly as parsed: empty when the
	// layer's own composition file entry carried no Name param at all.
	// This package invents no positional label for that case — that is a
	// display decision for a caller rendering an operator-facing surface
	// (see internal/coordinator/api's composition mapper), not a fact
	// about the composition itself.
	Name string

	// LayerGroupID is the id of the layer group this layer belongs to, or
	// nil when the composition has no layer groups at all, or when the
	// layer's own layerGroup index (as the file states it) does not
	// resolve against [TrackedComposition.LayerGroups]. resolumecomp.Layer's
	// own doc comment is explicit that it reports that index exactly as
	// the file states it, unchecked — [BuildTrackedComposition] performs
	// the one bounds check that value needs, here, once, rather than
	// leaving every caller of this field to re-derive it or risk indexing
	// past the end of a slice with an unvalidated value.
	LayerGroupID *ObjectID
}

// TrackedClip is one non-empty clip that lives on a deck. DeckID is always
// set: ADR-032 decision 6 measured that a clip id resolves over Resolume's
// REST API only while its own deck is selected (30/30 selected vs 0/10
// non-selected, both against real ids), so a stored clip reference without
// its deck cannot tell "this clip was replaced" from "this clip's deck
// simply is not showing" — every [TrackedComposition.Clips] entry carries
// the deck it needs to make that distinction.
type TrackedClip struct {
	ID     ObjectID
	DeckID ObjectID
	Name   string

	// LayerIndex and ColumnIndex are this clip's own layerIndex/columnIndex
	// attributes (0-based), exactly as resolumecomp.Clip reports them —
	// ADR-037's ClipLabel generates "Clip L<n>C<m>" from these when Name is
	// empty. Kept separately from LayerID (below): LayerID is this
	// package's own RESOLVED reference (nil when the file's layerIndex does
	// not resolve to a known layer), while LayerIndex is the raw value the
	// file states regardless of whether it resolves, which a label must
	// still be able to render from.
	LayerIndex  int
	ColumnIndex int

	// LayerID is the id of the layer this clip's own layerIndex resolves
	// to, or nil when it does not (the file's layerIndex names a layer
	// this composition has no matching Layer.Index for) — Track D seam
	// D-3's own addition, not read by D-2. TRACK-D-D3-SPEC.md §2's
	// launchClip confirming predicate is "clip connected... AND the
	// owning layer's active_clip.id == id", and that second term needs
	// to know which layer to re-read WITHOUT scanning every tracked
	// layer, which is exactly the "1 to 3 targeted by-id reads" budget
	// §4.3 sets and the "reads every tracked layer" exception it
	// reserves for blackout alone. Resolved the same way
	// [TrackedLayer.LayerGroupID] is: a clip's own "layerIndex" attribute
	// and a Layer's own "layerIndex" attribute share the identical name
	// in the .avc format, so [BuildTrackedComposition] treats them as the
	// same index space and performs the one lookup this field needs, once,
	// here — left nil (never a zero ObjectID standing in for "unknown")
	// when that lookup does not resolve, matching LayerGroupID's own
	// "left unresolved rather than rejecting" rule.
	LayerID *ObjectID
}

// TrackedPersistentClip is one of the composition's PersistentClips: it
// lives outside any deck and resolves by id regardless of which deck is
// selected (bench capture §16.2 — four such clips in the operator's own
// composition, none of them subject to the deck-selection rule
// [TrackedClip] exists for). This type deliberately has NO DeckID field at
// all, not a zero-valued or empty-string one: ADR-032 decision 6 calls out
// persistent clips as "the exception" to the deck-carrying rule and asks
// that they be "modelled separately and explicitly," and a struct that
// cannot syntactically hold a deck id is what makes it impossible for a
// future caller to accidentally attach one — the same structural-over-
// conventional judgment this package's own [ParameterID.MarshalJSON]
// already makes for a different rule.
type TrackedPersistentClip struct {
	ID   ObjectID
	Name string

	// LayerID: see [TrackedClip.LayerID]'s own doc comment — identical
	// meaning and identical resolution rule, applied to a persistent
	// clip's own layerIndex attribute (present on the enclosing
	// <PersistentClip> element, not on this file's inner <Clip> — see
	// pkg/resolumecomp's own rawPersistentClip doc comment).
	LayerID *ObjectID
}

// TrackedComposition is the collector's in-memory tracked-object set: what
// D-2/C's poll cycle and composition-identity check (TRACK-D-D2-SPEC.md
// §3, §6) query against instead of ever enumerating Resolume itself. It is
// built once, by [BuildTrackedComposition], from a [resolumecomp.Composition]
// already parsed out of an uploaded .avc file, and is immutable after
// that: every accessor below returns a value or a slice built at
// construction time, and nothing on this type ever mutates that state in
// place. Immutability is what makes [CompositionStore]'s swap-by-pointer
// concurrency story correct without any locking inside this type itself
// — see that type's own doc comment.
//
// The zero value, *TrackedComposition(nil), is never handed to a caller by
// this package's own API ([CompositionStore.Current] returns
// [ErrCompositionNotUploaded] instead of a nil or zero-valued pointer) —
// but a caller that somehow obtains one anyway (e.g. a bare
// `var tc *TrackedComposition`) gets a value whose accessors all return
// nil/empty, which is exactly the "empty composition" reading this
// package's own doc comment says must never be confused with "nothing
// uploaded." Callers MUST reach a *TrackedComposition only through
// [CompositionStore.Current] or [BuildTrackedComposition]'s own returned
// error being nil, never by constructing one directly.
type TrackedComposition struct {
	name      string
	writtenBy resolumecomp.WrittenBy

	layers          []TrackedLayer
	layerGroups     []TrackedLayerGroup
	decks           []TrackedDeck
	columns         []TrackedColumn
	clips           []TrackedClip
	persistentClips []TrackedPersistentClip
}

// Name is the composition's own display name (CompositionInfo/@name in the
// file). Per TRACK-D-D2-SPEC.md §3.8/§5 this is explicitly NOT an
// identity — it is mutable and not unique across shows — and callers
// must never use it as one; see [TrackedComposition.IdentitySample] for
// the actual identity check this package supports.
func (t *TrackedComposition) Name() string { return t.name }

// WrittenBy is the Arena product/version that wrote the uploaded file
// (ADR-032 decision 7): the tripwire to check first when a parse, or a
// downstream reading built from it, looks wrong. It is a fact about the
// Arena that wrote the file, never a guarantee that any other version
// parses the same way.
func (t *TrackedComposition) WrittenBy() resolumecomp.WrittenBy { return t.writtenBy }

// Layers returns every layer in the uploaded composition, in the order
// [BuildTrackedComposition] encountered them (the file's own document
// order). The caller must not mutate the returned slice; it is the same
// backing array this TrackedComposition holds internally.
func (t *TrackedComposition) Layers() []TrackedLayer { return t.layers }

// LayerGroups returns every layer group in the uploaded composition, in
// document order. Same no-mutation contract as [TrackedComposition.Layers].
func (t *TrackedComposition) LayerGroups() []TrackedLayerGroup { return t.layerGroups }

// Decks returns every deck in the uploaded composition, in document order.
// Same no-mutation contract as [TrackedComposition.Layers].
func (t *TrackedComposition) Decks() []TrackedDeck { return t.decks }

// Columns returns every column in the uploaded composition, in document
// order — Track D seam D-3's own addition, not read by D-2. Same
// no-mutation contract as [TrackedComposition.Layers].
func (t *TrackedComposition) Columns() []TrackedColumn { return t.columns }

// Clips returns every non-empty deck clip in the uploaded composition,
// each carrying its own deck (ADR-032 decision 6). Persistent clips are
// NOT included here — see [TrackedComposition.PersistentClips]. Same
// no-mutation contract as [TrackedComposition.Layers].
func (t *TrackedComposition) Clips() []TrackedClip { return t.clips }

// PersistentClips returns every clip from the composition's
// <PersistentClips> section — the ones that live outside any deck and
// resolve regardless of deck selection (bench capture §16.2). Same
// no-mutation contract as [TrackedComposition.Layers].
func (t *TrackedComposition) PersistentClips() []TrackedPersistentClip { return t.persistentClips }

// ClipsOnDeck returns the subset of [TrackedComposition.Clips] whose
// DeckID equals deck, in document order. A deck with no non-empty clips
// (or a deck id this composition does not contain at all) returns an
// empty, non-nil slice — this is an ordinary "nothing here" answer about
// a deck this method was explicitly asked about, not the "nothing
// uploaded at all" state [CompositionStore] guards against, so it is fine
// for it to be an empty slice rather than an error.
func (t *TrackedComposition) ClipsOnDeck(deck ObjectID) []TrackedClip {
	out := make([]TrackedClip, 0)
	for _, c := range t.clips {
		if c.DeckID == deck {
			out = append(out, c)
		}
	}
	return out
}

// --- Track D seam D-3: by-id lookups against the stored id map -----------
//
// TRACK-D-D3-SPEC.md §2's own opening rule is that every action takes a
// "ShowMesh reference resolving through the stored id map" — never a raw
// Resolume object id an operator or a macro author typed by hand. These
// five lookups are that resolution step: action.go calls exactly one of
// them per action target, and a miss (ok == false) is a REFUSAL before any
// HTTP request reaches Resolume, because an id this composition does not
// contain is not a stored reference at all, whatever else it might be. All
// five are a linear scan: a real composition's own object counts (dozens
// of layers/decks/clips, not thousands) make a map index unnecessary
// machinery for what is, per action, exactly one lookup.

// LayerByID returns the tracked layer with this id, or ok=false if the
// uploaded composition does not contain it.
func (t *TrackedComposition) LayerByID(id ObjectID) (layer TrackedLayer, ok bool) {
	for _, l := range t.layers {
		if l.ID == id {
			return l, true
		}
	}
	return TrackedLayer{}, false
}

// DeckByID returns the tracked deck with this id, or ok=false if the
// uploaded composition does not contain it.
func (t *TrackedComposition) DeckByID(id ObjectID) (deck TrackedDeck, ok bool) {
	for _, d := range t.decks {
		if d.ID == id {
			return d, true
		}
	}
	return TrackedDeck{}, false
}

// ColumnByID returns the tracked column with this id, or ok=false if the
// uploaded composition does not contain it.
func (t *TrackedComposition) ColumnByID(id ObjectID) (column TrackedColumn, ok bool) {
	for _, c := range t.columns {
		if c.ID == id {
			return c, true
		}
	}
	return TrackedColumn{}, false
}

// ClipByID returns the tracked DECK clip with this id — never a persistent
// clip; see [TrackedComposition.PersistentClipByID] for that — or ok=false
// if the uploaded composition does not contain it among its deck clips.
func (t *TrackedComposition) ClipByID(id ObjectID) (clip TrackedClip, ok bool) {
	for _, c := range t.clips {
		if c.ID == id {
			return c, true
		}
	}
	return TrackedClip{}, false
}

// PersistentClipByID returns the tracked persistent clip with this id, or
// ok=false if the uploaded composition does not contain it among its
// persistent clips. A caller resolving an arbitrary clip reference (Track D
// seam D-3's launchClip) must try both this and
// [TrackedComposition.ClipByID], since a stored reference does not itself
// say which kind it is — see ADR-032 decision 6's own "PersistentClips ...
// modelled separately and explicitly."
func (t *TrackedComposition) PersistentClipByID(id ObjectID) (clip TrackedPersistentClip, ok bool) {
	for _, c := range t.persistentClips {
		if c.ID == id {
			return c, true
		}
	}
	return TrackedPersistentClip{}, false
}

// identitySampleMaxDeckClips is TRACK-D-D2-SPEC.md §6's cap: "up to 8
// non-empty clips of the currently selected deck." Persistent clips are
// never subject to this cap — they are cheap (four in the operator's own
// composition) and are always fully included, per that section's own
// wording ("plus all PersistentClips").
const identitySampleMaxDeckClips = 8

// IdentitySampleClip is one clip named by an [IdentitySample], carrying
// just enough to both drive a by-id resolve and render an operator-facing
// message naming which clip failed to resolve (TRACK-D-D2-SPEC.md §6:
// "some 404 ... with the ids named").
type IdentitySampleClip struct {
	ID   ObjectID
	Name string
}

// IdentitySample is TRACK-D-D2-SPEC.md §6's composition-identity sample:
// up to [identitySampleMaxDeckClips] non-empty clips of one selected deck,
// plus every persistent clip. DeckClips and PersistentClips are reported
// separately, never merged into one slice, because ADR-032 decision 6
// gives them different 404 semantics: a 404 on a DeckClips entry can be a
// stale reference OR a deck mismatch depending on whether SelectedDeck is
// still actually selected in the running Arena (this package does not
// observe that; the caller does), while a 404 on a PersistentClips entry
// is unconditionally meaningful, because persistent clips resolve
// regardless of deck selection.
type IdentitySample struct {
	// SelectedDeck is the deck DeckClips was drawn from — an echo of the
	// caller's own input, kept here so a caller building an operator
	// message from an IdentitySample value alone (without holding onto
	// its own copy of which deck it asked for) still has it.
	SelectedDeck ObjectID

	DeckClips       []IdentitySampleClip
	PersistentClips []IdentitySampleClip
}

// IdentitySample builds TRACK-D-D2-SPEC.md §6's composition-identity
// sample for selectedDeck. selectedDeck is a parameter, not something this
// method reads off Resolume itself: which deck is currently selected is
// live state (the deck's own `selected` parameter, read by id), and this
// package's tracked-object set is built once from an uploaded file and
// knows nothing about the running Arena's current state — the caller
// (D-2/C) is the one that has, or will have, read that live value.
//
// DeckClips is drawn from [TrackedComposition.Clips] in document order,
// capped at [identitySampleMaxDeckClips]; every entry in
// [TrackedComposition.PersistentClips] is always included with no cap.
// selectedDeck naming a deck this composition does not contain (or the
// zero [ObjectID]) is not an error here — it simply produces zero
// DeckClips, which TRACK-D-D2-SPEC.md §6's own "nothing resolves and
// Resolume is reachable -> unknown" rung is designed to absorb; this
// method reports what it found, not whether that is enough to draw a
// conclusion from.
func (t *TrackedComposition) IdentitySample(selectedDeck ObjectID) IdentitySample {
	sample := IdentitySample{
		SelectedDeck:    selectedDeck,
		DeckClips:       make([]IdentitySampleClip, 0, identitySampleMaxDeckClips),
		PersistentClips: make([]IdentitySampleClip, 0, len(t.persistentClips)),
	}

	for _, c := range t.clips {
		if len(sample.DeckClips) >= identitySampleMaxDeckClips {
			break
		}
		if c.DeckID != selectedDeck {
			continue
		}
		sample.DeckClips = append(sample.DeckClips, IdentitySampleClip{ID: c.ID, Name: c.Name})
	}

	for _, c := range t.persistentClips {
		sample.PersistentClips = append(sample.PersistentClips, IdentitySampleClip{ID: c.ID, Name: c.Name})
	}

	return sample
}

// BuildTrackedComposition transforms an already-parsed
// [resolumecomp.Composition] into the tracked-object set D-2/C queries
// against. comp must be non-nil; a nil comp is a caller error (this
// package's own caller, resolumewiring.go, only ever calls this with a
// freshly-decoded value or does not call it at all — see
// [CompositionStore.Refresh]).
//
// An error here means comp carries an id this package cannot parse as an
// [ObjectID] ([ErrInvalidObjectID]) — see that error's own doc comment
// for why this rejects the whole build rather than skipping the offending
// object. No partially-built *TrackedComposition is ever returned
// alongside a non-nil error, mirroring pkg/resolumecomp's own
// [resolumecomp.Parse] contract ("a rejected file changes nothing," ADR-032
// decision 7) one layer up.
func BuildTrackedComposition(comp *resolumecomp.Composition) (*TrackedComposition, error) {
	if comp == nil {
		return nil, fmt.Errorf("resolume: build tracked composition: comp is nil")
	}

	decks := make([]TrackedDeck, 0, len(comp.Decks))
	for _, d := range comp.Decks {
		id, err := parseObjectID(d.ID, "deck")
		if err != nil {
			return nil, err
		}
		decks = append(decks, TrackedDeck{ID: id, Name: d.Name})
	}

	layerGroups := make([]TrackedLayerGroup, 0, len(comp.LayerGroups))
	for _, g := range comp.LayerGroups {
		id, err := parseObjectID(g.ID, "layer group")
		if err != nil {
			return nil, err
		}
		layerGroups = append(layerGroups, TrackedLayerGroup{ID: id})
	}

	// layerIndexToID resolves a Clip's own "layerIndex" attribute to the
	// Layer object it names — see [TrackedClip.LayerID]'s own doc comment
	// for why this is keyed by Layer.Index (the file's own semantic
	// "layerIndex" value) rather than by slice position, and why the two
	// attributes sharing one name is the basis for treating them as one
	// index space.
	layerIndexToID := make(map[int]ObjectID, len(comp.Layers))

	layers := make([]TrackedLayer, 0, len(comp.Layers))
	for _, l := range comp.Layers {
		id, err := parseObjectID(l.ID, "layer")
		if err != nil {
			return nil, err
		}

		var groupID *ObjectID
		if l.LayerGroupIndex != nil {
			if idx := *l.LayerGroupIndex; idx >= 0 && idx < len(layerGroups) {
				g := layerGroups[idx].ID
				groupID = &g
			}
			// An out-of-range layerGroup index is left unresolved
			// (groupID stays nil) rather than rejecting the whole
			// composition — see [TrackedLayer.LayerGroupID]'s own doc
			// comment. resolumecomp.Layer.LayerGroupIndex's own doc
			// comment already establishes that this package does not
			// invent a group that is not there.
		}

		layers = append(layers, TrackedLayer{ID: id, Index: l.Index, Name: l.Name, LayerGroupID: groupID})
		layerIndexToID[l.Index] = id
	}

	columns := make([]TrackedColumn, 0, len(comp.Columns))
	for _, col := range comp.Columns {
		id, err := parseObjectID(col.ID, "column")
		if err != nil {
			return nil, err
		}
		deckID, err := parseObjectID(col.DeckID, "column deck")
		if err != nil {
			return nil, err
		}
		columns = append(columns, TrackedColumn{ID: id, DeckID: deckID, Index: col.Index})
	}

	// resolveLayerID looks up layerIndexToID for one clip's own layerIndex
	// — shared between the deck-clip and persistent-clip loops below,
	// since [TrackedClip.LayerID] and [TrackedPersistentClip.LayerID]
	// resolve identically. A miss leaves the pointer nil rather than a
	// zero ObjectID, matching [TrackedLayer.LayerGroupID]'s own
	// "left unresolved rather than rejecting" rule.
	resolveLayerID := func(layerIndex int) *ObjectID {
		if id, ok := layerIndexToID[layerIndex]; ok {
			return &id
		}
		return nil
	}

	clips := make([]TrackedClip, 0, len(comp.Clips))
	for _, c := range comp.Clips {
		id, err := parseObjectID(c.ID, "clip")
		if err != nil {
			return nil, err
		}
		// c.DeckID is always non-empty for a [resolumecomp.Composition]'s
		// Clips entries (that package's own invariant, enforced by
		// ErrMissingDeckID at parse time) — parsed as an ObjectID here
		// the same way any other id is, not defaulted or special-cased.
		deckID, err := parseObjectID(c.DeckID, "clip deck")
		if err != nil {
			return nil, err
		}
		clips = append(clips, TrackedClip{
			ID: id, DeckID: deckID, Name: c.Name,
			LayerIndex: c.LayerIndex, ColumnIndex: c.ColumnIndex,
			LayerID: resolveLayerID(c.LayerIndex),
		})
	}

	persistentClips := make([]TrackedPersistentClip, 0, len(comp.PersistentClips))
	for _, c := range comp.PersistentClips {
		id, err := parseObjectID(c.ID, "persistent clip")
		if err != nil {
			return nil, err
		}
		persistentClips = append(persistentClips, TrackedPersistentClip{ID: id, Name: c.Name, LayerID: resolveLayerID(c.LayerIndex)})
	}

	return &TrackedComposition{
		name:            comp.Name,
		writtenBy:       comp.WrittenBy,
		layers:          layers,
		layerGroups:     layerGroups,
		decks:           decks,
		columns:         columns,
		clips:           clips,
		persistentClips: persistentClips,
	}, nil
}

// parseObjectID parses a resolumecomp string id as an [ObjectID]. what
// names the kind of object the id belongs to, for [ErrInvalidObjectID]'s
// wrapped message.
func parseObjectID(s, what string) (ObjectID, error) {
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s id %q", ErrInvalidObjectID, what, s)
	}
	return ObjectID(n), nil
}

// --- CompositionStore: the concurrency-safe, restart-free holder ---------

// ErrCompositionNotUploaded is what [CompositionStore.Current] returns
// when no composition has ever been uploaded to this coordinator (or none
// has been loaded into this particular store yet). It exists so "nothing
// uploaded" is a distinguishable outcome a caller must handle explicitly
// — an error return, checked with errors.Is — rather than an empty
// *TrackedComposition a caller could read as "an uploaded composition
// that happens to have zero of everything." See this file's own top
// comment for the CLAUDE.md defect class this rules out.
var ErrCompositionNotUploaded = errors.New("resolume: no composition has been uploaded to this coordinator yet")

// CompositionStore holds this coordinator's current tracked-object set,
// safe for concurrent use: [CompositionStore.Current] is read by a
// collector's poll goroutine, and [CompositionStore.Refresh] is called
// (today, by resolumewiring.go's own periodic loop; once D-2/C's collector
// exists, potentially also from its own poll cycle — Refresh is cheap and
// idempotent when nothing changed, so either caller, or both, is safe)
// whenever a fresh upload may have landed. The two can run concurrently:
// Refresh builds an entirely new *TrackedComposition off to the side and
// installs it with a single atomic pointer store, so a concurrent
// Current() call always observes either the complete old set or the
// complete new one, never a partially-built value in between
// (TRACK-D-D2-SPEC.md §9's own D-2/B row: "make the swap atomic — a poll
// must never observe a half-installed map").
//
// The zero value is a ready-to-use CompositionStore already in the "not
// uploaded" state — its internal pointer is nil, and
// [CompositionStore.Current] reports [ErrCompositionNotUploaded] for that
// nil pointer directly, with no separate "loaded" bool that some future
// change could add and then forget to check everywhere. There is
// structurally no path through this type that hands back a
// *TrackedComposition nobody actually built from a stored revision.
type CompositionStore struct {
	current  atomic.Pointer[TrackedComposition]
	revision atomic.Int64 // 0 == "no revision loaded yet" — store.ConfigObjectRecord's own "0 means nothing activated" convention
}

// Current returns the coordinator's current tracked-object set, or
// [ErrCompositionNotUploaded]. Cheap: a single atomic load, safe to call
// from any number of goroutines concurrently with each other and with
// [CompositionStore.Refresh].
func (s *CompositionStore) Current() (*TrackedComposition, error) {
	tc := s.current.Load()
	if tc == nil {
		return nil, ErrCompositionNotUploaded
	}
	return tc, nil
}

// LoadedRevision reports the config_revisions revision number the
// currently held [TrackedComposition] was built from, or 0 if none has
// ever been loaded (matching [CompositionStore]'s own "0 == not loaded"
// convention, itself matching store.ConfigObjectRecord.CurrentRevision).
// This is a diagnostic accessor — ADR-032 decision 7's own tripwire is
// [TrackedComposition.WrittenBy], not this — but knowing which revision
// is actually live is the first fact worth logging alongside it.
func (s *CompositionStore) LoadedRevision() int64 {
	return s.revision.Load()
}

// CompositionConfigReader is what [CompositionStore.Refresh] needs to
// check for, and read, the currently active resolume.composition config
// revision. Declared here, at the consumer, per this project's own
// standing convention (Step 3 contract §5: "declare interfaces at the
// consumer, not the producer") — this package does not import
// internal/coordinator/store, and does not need to know the
// config_objects/config_revisions kind and object id constants that
// internal/coordinator/api/resolumecomposition.go owns privately.
// resolumewiring.go builds the adapter that knows both, the same way
// apiwiring.go's own adapters make *store.Store satisfy interfaces the api
// package declares for itself.
type CompositionConfigReader interface {
	// CurrentCompositionRevision returns the resolume.composition config
	// object's currently active revision number, and the raw JSON bytes
	// of the stored [resolumecomp.Composition] value itself — already
	// unwrapped from whatever envelope the config store's own
	// payload_json carries it inside; Refresh never needs to know that
	// shape.
	//
	// ok is false, with revision 0, compositionJSON nil, and err nil,
	// when no composition has ever been uploaded: no config object at all
	// for this kind, or one that exists but whose CurrentRevision is
	// still 0 (store/config.go's own "declared, nothing active yet"
	// state, unreachable in practice for this kind since its only writer
	// activates a revision in the same transaction that creates the
	// object — but not assumed unreachable here either). This is a
	// STATE, never an error: [CompositionStore.Refresh] treats ok==false
	// exactly like [ErrCompositionNotUploaded], not like a read failure.
	CurrentCompositionRevision(ctx context.Context) (revision int64, compositionJSON []byte, ok bool, err error)
}

// Refresh re-reads reader's currently active resolume.composition
// revision and, only if it differs from the revision this store already
// holds, decodes and rebuilds a fresh [TrackedComposition] and installs it
// with one atomic pointer store. This is what lets an uploaded composition
// reach a running coordinator's tracked-object set without a restart
// (TRACK-D-D2-SPEC.md §9's D-2/B row) — unlike SHOWMESH_FPP_ENDPOINTS,
// which this coordinator reads once at startup and never again (see
// api/config.go's own restartRequiredReason), the resolume.composition
// config surface is read fresh here specifically so an upload takes
// effect on its own, not on the coordinator's next restart.
//
// Cheap when nothing changed: reader.CurrentCompositionRevision is meant
// to cost one small indexed store read (the config_objects pointer row),
// and this method only decodes and rebuilds when the revision number
// reader returns has moved past [CompositionStore.LoadedRevision] —
// calling this once per collector poll cycle (D-2/C, ~10s) or on a
// dedicated short interval (resolumewiring.go's own loop) costs one extra
// small read per call in the common case of no new upload.
//
// If reader reports ok == false, Refresh clears this store back to the
// "not uploaded" state — reachable in practice only if the composition's
// config object were somehow removed outright, which nothing in this
// codebase does today, but Refresh does not assume that can never happen
// (matching this project's own "absent evidence is stated, never
// assumed impossible" habit).
func (s *CompositionStore) Refresh(ctx context.Context, reader CompositionConfigReader) error {
	revision, compositionJSON, ok, err := reader.CurrentCompositionRevision(ctx)
	if err != nil {
		return fmt.Errorf("resolume: refresh tracked composition: %w", err)
	}
	if !ok {
		s.current.Store(nil)
		s.revision.Store(0)
		return nil
	}
	if revision == s.revision.Load() {
		// No change since the last successful refresh (or since this
		// store was first populated at this revision) — keep the
		// currently installed *TrackedComposition rather than rebuilding
		// an identical one.
		return nil
	}

	comp, err := decodeStoredComposition(compositionJSON)
	if err != nil {
		return fmt.Errorf("resolume: decode stored composition (revision %d): %w", revision, err)
	}
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		return fmt.Errorf("resolume: build tracked composition (revision %d): %w", revision, err)
	}

	s.current.Store(tc)
	s.revision.Store(revision)
	return nil
}

// decodeStoredComposition unmarshals compositionJSON — the raw bytes
// [CompositionConfigReader.CurrentCompositionRevision] returned — into a
// [resolumecomp.Composition]. [resolumecomp.Composition]'s own json tags
// (see that package's doc comment: every exported field carries one) are
// exactly the shape internal/coordinator/api/resolumecomposition.go
// marshaled the "composition" member of its stored payload from in the
// first place, so a plain json.Unmarshal here is the exact inverse of that
// write with nothing this package needs to know about the surrounding
// envelope (sourceFilename, contentHash, sizeBytes) that envelope also
// carries — the adapter that reads the store already stripped that off,
// per [CompositionConfigReader]'s own doc comment.
func decodeStoredComposition(compositionJSON []byte) (*resolumecomp.Composition, error) {
	var comp resolumecomp.Composition
	if err := json.Unmarshal(compositionJSON, &comp); err != nil {
		return nil, err
	}
	return &comp, nil
}
