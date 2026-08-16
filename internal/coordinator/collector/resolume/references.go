package resolume

import (
	"errors"
	"fmt"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam B (ADR-037 decisions 1-6): the label vocabulary
// every operator-facing surface must render an object as, and the pure
// resolve step that turns a named reference back into the [ObjectID] the
// wire to Resolume still addresses by. Nothing here issues an HTTP request
// or reads anything off a live Collector — every function takes only a
// *[TrackedComposition] and a reference, so a caller with no dispatcher, no
// client and no server (a macro's author-time validation path, ADR-037's
// own Open section) can call it exactly the way [ActionDispatcher.Dispatch]
// does.

// --- Labels (ADR-037 decision 4) -------------------------------------------
//
// One function per object kind, called from here and from
// internal/coordinator/api's composition read surface — never a second,
// independently written labeller for the same kind. Every label is
// compared byte-for-byte, case-sensitive, no trimming: the measured
// composition contains a layer authored as "Peak + Under " (a trailing
// space) and a clip name containing a full-width vertical bar. Names are
// operator-typed strings, not identifiers, and trimming one would make
// "Peak + Under " and a hypothetical "Peak + Under" the same reference —
// silently addressing whichever one this package happened to find first.

// LayerLabel returns a layer's operator-facing label from its own 0-based
// index and authored name: the name when non-empty, otherwise the
// generated "Layer <n>" form from its 1-based position. generated reports
// which case this is, so a caller never presents an invented label as one
// the operator chose.
func LayerLabel(index int, name string) (label string, generated bool) {
	if name != "" {
		return name, false
	}
	return fmt.Sprintf("Layer %d", index+1), true
}

// ColumnLabel returns a column's generated label from its own 0-based
// index. Columns never carry an authored name at all (resolumecomp.Column
// has no Name field — the .avc format does not give one), so this is
// always generated.
func ColumnLabel(index int) string {
	return fmt.Sprintf("Column %d", index+1)
}

// DeckLabel returns a deck's operator-facing label from its 1-based
// position among [TrackedComposition.Decks] and its authored name: the
// name when non-empty, otherwise the generated "Deck <n>" form. position is
// a caller-supplied ordinal, never recomputed from anything this function
// reads itself, so every caller ranking decks agrees on the same numbering.
func DeckLabel(position int, name string) (label string, generated bool) {
	if name != "" {
		return name, false
	}
	return fmt.Sprintf("Deck %d", position), true
}

// ClipLabel returns a deck clip's operator-facing label from its own
// 0-based layerIndex/columnIndex and authored name: the name when
// non-empty, otherwise the generated "Clip L<n>C<m>" form. These indices
// are the file's own semantic values (matching [TrackedClip.LayerIndex]'s
// own doc comment), never a slice position.
func ClipLabel(layerIndex, columnIndex int, name string) (label string, generated bool) {
	if name != "" {
		return name, false
	}
	return fmt.Sprintf("Clip L%dC%d", layerIndex+1, columnIndex+1), true
}

// PersistentClipLabel returns a persistent clip's operator-facing label
// from its 1-based position among [TrackedComposition.PersistentClips] and
// its authored name: the name when non-empty, otherwise the generated
// "Persistent clip <n>" form.
func PersistentClipLabel(position int, name string) (label string, generated bool) {
	if name != "" {
		return name, false
	}
	return fmt.Sprintf("Persistent clip %d", position), true
}

// LayerLabelByIndex resolves layerIndex (a clip's own layerIndex attribute,
// matching [resolumecomp.Layer.Index]) to that layer's own [LayerLabel]
// among layers, for a caller working directly against a parsed
// [resolumecomp.Composition] rather than a built [TrackedComposition] — the
// composition read surface (internal/coordinator/api) is exactly that
// caller: it has no ObjectID to resolve through and needs a clip's layer
// label purely to compute [ClipTripleKey]. Mirrors [clipLayerLabel]'s
// identical "unknown layer" fallback for an index that resolves to nothing.
func LayerLabelByIndex(layers []resolumecomp.Layer, layerIndex int) string {
	for _, l := range layers {
		if l.Index == layerIndex {
			label, _ := LayerLabel(l.Index, l.Name)
			return label
		}
	}
	return "an unknown layer"
}

// --- Ambiguity (ADR-037 amendment, 2026-08-16) ------------------------------
//
// Measured against the operator's real composition: seven (deck, layer,
// label) triples on one deck alone are shared by more than one clip,
// covering 16 of 36 clips, and two of the four persistent clips share a
// label too. A clip whose triple collides with another's can never be told
// apart by any [ClipReference] this package accepts — adding "layer" does
// not help, because the colliding clips already agree on their own layer
// too. ADR-037 decision 5 ("an error at bind time, not a coin flip at
// showtime") extends to this case as "flagged before anything is bound at
// all": the composition read surface marks it so an operator finds every
// such clip in one pass instead of discovering them one refused macro at a
// time. The only remedy is renaming one of them in Resolume and
// re-uploading; this package never invents a column term or an ordinal to
// break the tie, because either would be a positional reference wearing a
// name (ADR-037's own rejection of positional addressing).

// ClipTripleKey is the (deck, layer, label) tuple two distinct clips must
// not share. Deck is "" for a persistent clip (mirroring
// [TrackedPersistentClip]'s own "no DeckID field at all" rule) — a
// persistent clip's key is really (persistent, layer, label), and using ""
// rather than a real deck id is what keeps it from ever colliding with a
// deck clip's key, since a real deck id is never empty.
type ClipTripleKey struct {
	Deck  string
	Layer string
	Label string
}

// AmbiguousClipIDs reports, for every id in entries, whether its
// [ClipTripleKey] is shared by another entry. Both [resolveDeckClip] and
// the composition read surface build their own id/key pairs from their own
// data model (an [ObjectID] resolved against a [TrackedComposition], or a
// raw resolumecomp.Clip.ID string) and call this one function, rather than
// each independently deciding what "shares a triple" means.
func AmbiguousClipIDs[K comparable](entries map[K]ClipTripleKey) map[K]bool {
	counts := make(map[ClipTripleKey]int, len(entries))
	for _, k := range entries {
		counts[k]++
	}
	out := make(map[K]bool, len(entries))
	for id, k := range entries {
		out[id] = counts[k] > 1
	}
	return out
}

// labelEquals is every reference comparison in this file's one call site:
// exact, byte-for-byte, case-sensitive. See this file's own top comment for
// the trailing-space and full-width-character examples that rule out
// trimming or normalizing either side.
func labelEquals(reference, label string) bool {
	return reference == label
}

// --- Deck and layer: flat candidate sets -----------------------------------

// ResolveDeck resolves reference against every deck in tc by
// [DeckLabel], returning the one matching deck's [ObjectID]. Zero or more
// than one match is a refusal naming the label and, for an ambiguous
// match, every candidate's position (ADR-037 decision 5: ambiguity is an
// error, never a coin flip).
func ResolveDeck(tc *TrackedComposition, reference string) (ObjectID, error) {
	decks := tc.Decks()
	var matched []struct {
		id       ObjectID
		position int
	}
	for i, d := range decks {
		label, _ := DeckLabel(i+1, d.Name)
		if labelEquals(reference, label) {
			matched = append(matched, struct {
				id       ObjectID
				position int
			}{d.ID, i + 1})
		}
	}
	switch len(matched) {
	case 1:
		return matched[0].id, nil
	case 0:
		return 0, fmt.Errorf("no deck in the current composition is named %q", reference)
	default:
		positions := make([]string, len(matched))
		for i, m := range matched {
			positions[i] = fmt.Sprintf("position %d", m.position)
		}
		return 0, fmt.Errorf(
			"more than one deck in the current composition is named %q (at %s); rename one of them in Resolume to disambiguate",
			reference, strings.Join(positions, ", "))
	}
}

// ResolveLayer resolves reference against every layer in tc by
// [LayerLabel], returning the one matching layer's [ObjectID]. Zero or more
// than one match is a refusal naming the label and, for an ambiguous
// match, every candidate's position.
func ResolveLayer(tc *TrackedComposition, reference string) (ObjectID, error) {
	layers := tc.Layers()
	var matched []TrackedLayer
	for _, l := range layers {
		label, _ := LayerLabel(l.Index, l.Name)
		if labelEquals(reference, label) {
			matched = append(matched, l)
		}
	}
	switch len(matched) {
	case 1:
		return matched[0].ID, nil
	case 0:
		return 0, fmt.Errorf("no layer in the current composition is named %q", reference)
	default:
		positions := make([]string, len(matched))
		for i, l := range matched {
			positions[i] = fmt.Sprintf("layerIndex %d", l.Index)
		}
		return 0, fmt.Errorf(
			"more than one layer in the current composition is named %q (%s); rename one of them in Resolume to disambiguate",
			reference, strings.Join(positions, ", "))
	}
}

// --- Column: scoped to a resolved deck --------------------------------------

// ResolveColumn resolves deckReference against [ResolveDeck] first — a
// failure there is reported as a deck failure, never collapsed into "column
// not found" — then resolves columnReference against [ColumnLabel] within
// that deck alone.
func ResolveColumn(tc *TrackedComposition, deckReference, columnReference string) (ObjectID, error) {
	deckID, err := ResolveDeck(tc, deckReference)
	if err != nil {
		return 0, fmt.Errorf("resolving this column's deck: %w", err)
	}

	var matched []TrackedColumn
	for _, c := range tc.Columns() {
		if c.DeckID != deckID {
			continue
		}
		if labelEquals(columnReference, ColumnLabel(c.Index)) {
			matched = append(matched, c)
		}
	}
	switch len(matched) {
	case 1:
		return matched[0].ID, nil
	case 0:
		return 0, fmt.Errorf("no column named %q was found on deck %q", columnReference, deckReference)
	default:
		positions := make([]string, len(matched))
		for i, c := range matched {
			positions[i] = fmt.Sprintf("columnIndex %d", c.Index)
		}
		return 0, fmt.Errorf(
			"more than one column named %q was found on deck %q (%s); rename one of them in Resolume to disambiguate",
			columnReference, deckReference, strings.Join(positions, ", "))
	}
}

// --- Clip: deck-scoped or persistent, optionally disambiguated by layer ----

// ClipReference is launchClip's own reference vocabulary (TRACK-D-SEAM-B-NAMES-SPEC.md
// §2.1). Deck is conditional rather than optional: exactly one of "Deck
// named" or "Persistent true" may hold, checked by [ResolveClip] itself
// before either candidate set is even searched, because a clip reference
// without its deck cannot tell "this clip was replaced" from "this clip's
// deck simply is not showing" (ADR-032 decision 6) and inferring
// "persistent" from an absent deck would make an omitted field silently
// change which object is addressed.
type ClipReference struct {
	Clip       string
	Deck       string
	Persistent bool
	Layer      string
}

// clipLayerLabel renders the label of the layer a clip's own resolved
// LayerID names, for an ambiguity refusal's disambiguating detail (§2.3:
// "for a clip, each candidate's layer label"). A clip whose layerIndex does
// not resolve to any tracked layer names "an unknown layer" rather than
// panicking or fabricating an id.
func clipLayerLabel(tc *TrackedComposition, layerID *ObjectID) string {
	if layerID != nil {
		if l, ok := tc.LayerByID(*layerID); ok {
			label, _ := LayerLabel(l.Index, l.Name)
			return label
		}
	}
	return "an unknown layer"
}

// ResolveClip resolves ref against tc. Persistent and non-persistent clips
// are two disjoint candidate sets (ADR-032 decision 6's own "modelled
// separately and explicitly"); which one ref searches is READ from
// ref.Persistent, never inferred, and the wrong combination (both a deck
// and Persistent, or neither) is refused before any search happens.
func ResolveClip(tc *TrackedComposition, ref ClipReference) (ObjectID, error) {
	if ref.Persistent {
		if ref.Deck != "" {
			return 0, errors.New(`a persistent clip reference must not also name a "deck"`)
		}
		return resolvePersistentClip(tc, ref)
	}
	if ref.Deck == "" {
		return 0, errors.New("a clip reference must name the deck the clip lives on")
	}
	return resolveDeckClip(tc, ref)
}

// clipCandidate pairs a matched clip with the layer label an ambiguity
// refusal names — computed once per match rather than re-derived at
// message-formatting time.
type clipCandidate struct {
	id         ObjectID
	layerLabel string
}

func resolveDeckClip(tc *TrackedComposition, ref ClipReference) (ObjectID, error) {
	deckID, err := ResolveDeck(tc, ref.Deck)
	if err != nil {
		return 0, fmt.Errorf("resolving this clip's deck: %w", err)
	}

	var matched []clipCandidate
	for _, c := range tc.Clips() {
		if c.DeckID != deckID {
			continue
		}
		label, _ := ClipLabel(c.LayerIndex, c.ColumnIndex, c.Name)
		if !labelEquals(ref.Clip, label) {
			continue
		}
		layerLabel := clipLayerLabel(tc, c.LayerID)
		// A layer disambiguator that does not match NARROWS the candidate
		// set to zero — it is never a fallback to the unfiltered set (§2.1).
		if ref.Layer != "" && ref.Layer != layerLabel {
			continue
		}
		matched = append(matched, clipCandidate{c.ID, layerLabel})
	}
	return resolveClipCandidates(matched, ref, ref.Deck)
}

func resolvePersistentClip(tc *TrackedComposition, ref ClipReference) (ObjectID, error) {
	var matched []clipCandidate
	for i, c := range tc.PersistentClips() {
		label, _ := PersistentClipLabel(i+1, c.Name)
		if !labelEquals(ref.Clip, label) {
			continue
		}
		layerLabel := clipLayerLabel(tc, c.LayerID)
		if ref.Layer != "" && ref.Layer != layerLabel {
			continue
		}
		matched = append(matched, clipCandidate{c.ID, layerLabel})
	}
	return resolveClipCandidates(matched, ref, "")
}

// resolveClipCandidates applies the shared zero/one/many outcome to a
// clip search's own matched set — deckDesc is "" for a persistent search
// (there is no deck to name in the message).
func resolveClipCandidates(matched []clipCandidate, ref ClipReference, deckDesc string) (ObjectID, error) {
	scope := "was found among the persistent clips"
	if deckDesc != "" {
		scope = fmt.Sprintf("was found on deck %q", deckDesc)
	}
	switch len(matched) {
	case 1:
		return matched[0].id, nil
	case 0:
		if ref.Layer != "" {
			return 0, fmt.Errorf("no clip named %q on layer %q %s", ref.Clip, ref.Layer, scope)
		}
		return 0, fmt.Errorf("no clip named %q %s", ref.Clip, scope)
	default:
		layers := make([]string, len(matched))
		layerCounts := make(map[string]int, len(matched))
		for i, m := range matched {
			layers[i] = fmt.Sprintf("on layer %q", m.layerLabel)
			layerCounts[m.layerLabel]++
		}
		// ADR-037 amendment (2026-08-16): if two or more of the matched
		// candidates share the identical layer too, "add a layer" is not a
		// remedy that works for them — they already agree on their own
		// layer, which is exactly what a layer disambiguator narrows on.
		// Measured against the operator's real composition: this is the
		// common case, not the rare one (16 of 36 clips on one deck alone).
		for _, n := range layerCounts {
			if n > 1 {
				return 0, fmt.Errorf(
					"more than one clip named %q %s (%s); these clips also share the same layer, so no reference can "+
						"ever tell them apart — rename one of them in Resolume and re-upload the composition",
					ref.Clip, scope, strings.Join(layers, ", "))
			}
		}
		return 0, fmt.Errorf(
			`more than one clip named %q %s (%s); add a "layer" to this reference to disambiguate`,
			ref.Clip, scope, strings.Join(layers, ", "))
	}
}
