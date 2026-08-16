package resolume

import (
	"errors"
	"fmt"
	"strings"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file holds the pure resolve step that turns a named reference into
// the ObjectID Resolume is still addressed by on the wire. Every function
// here takes only a *TrackedComposition and issues no HTTP request.
//
// The label vocabulary itself ([resolumecomp.LayerLabel] and friends) lives
// in pkg/resolumecomp: it is pure over that package's own parsed types, and
// the composition read surface (internal/coordinator/api) needs the same
// functions without importing this collector package. This file calls
// those functions rather than keeping its own copies, so there is exactly
// one implementation of each label.

// labelEquals is every reference comparison in this file's one call site:
// exact, byte-for-byte, case-sensitive. pkg/resolumecomp's own labels.go
// carries the trailing-space and full-width-character examples that rule
// out trimming or normalizing either side.
func labelEquals(reference, label string) bool {
	return reference == label
}

// --- Deck and layer: flat candidate sets -----------------------------------

// ResolveDeck resolves reference against every deck in tc by
// [resolumecomp.DeckLabel], returning the one matching deck's [ObjectID].
// Zero or more than one match is a refusal naming the label and, for an
// ambiguous match, every candidate's position (ADR-037 decision 5:
// ambiguity is an error, never a coin flip).
func ResolveDeck(tc *TrackedComposition, reference string) (ObjectID, error) {
	decks := tc.Decks()
	var matched []struct {
		id       ObjectID
		position int
	}
	for i, d := range decks {
		label, _ := resolumecomp.DeckLabel(i+1, d.Name)
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
// [resolumecomp.LayerLabel], returning the one matching layer's [ObjectID].
// Zero or more than one match is a refusal naming the label and, for an
// ambiguous match, every candidate's position.
func ResolveLayer(tc *TrackedComposition, reference string) (ObjectID, error) {
	layers := tc.Layers()
	var matched []TrackedLayer
	for _, l := range layers {
		label, _ := resolumecomp.LayerLabel(l.Index, l.Name)
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
// not found" — then resolves columnReference against
// [resolumecomp.ColumnLabel] within that deck alone.
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
		if labelEquals(columnReference, resolumecomp.ColumnLabel(c.Index)) {
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

// ClipReference is launchClip's own reference vocabulary. Deck is
// conditional rather than optional: exactly one of "Deck named" or
// "Persistent true" may hold, checked by [ResolveClip] itself before
// either candidate set is even searched, since a clip reference without
// its deck cannot tell "this clip was replaced" from "this clip's deck
// simply is not showing" (ADR-032 decision 6).
type ClipReference struct {
	Clip       string
	Deck       string
	Persistent bool
	Layer      string
}

// clipLayerLabel renders the label of the layer a clip's own resolved
// LayerID names, and reports whether one was actually found. A clip whose
// layerIndex does not resolve to any tracked layer has no label at all —
// known is false, and the caller must never treat that as a value a
// "layer" reference could name: "an unknown layer" is prose for a human,
// not a candidate for string equality against ref.Layer.
func clipLayerLabel(tc *TrackedComposition, layerID *ObjectID) (label string, known bool) {
	if layerID != nil {
		if l, ok := tc.LayerByID(*layerID); ok {
			label, _ := resolumecomp.LayerLabel(l.Index, l.Name)
			return label, true
		}
	}
	return "", false
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
// message-formatting time. layerKnown is false when the clip's own
// layerIndex did not resolve to a tracked layer at all.
type clipCandidate struct {
	id         ObjectID
	layerLabel string
	layerKnown bool
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
		label, _ := resolumecomp.ClipLabel(c.LayerIndex, c.ColumnIndex, c.Name)
		if !labelEquals(ref.Clip, label) {
			continue
		}
		layerLabel, layerKnown := clipLayerLabel(tc, c.LayerID)
		// A layer disambiguator that does not match NARROWS the candidate
		// set to zero — never a fallback to the unfiltered set. An unknown
		// layer can never match: it has no name a reference could ever
		// carry.
		if ref.Layer != "" && (!layerKnown || ref.Layer != layerLabel) {
			continue
		}
		matched = append(matched, clipCandidate{c.ID, layerLabel, layerKnown})
	}
	return resolveClipCandidates(matched, ref, ref.Deck)
}

func resolvePersistentClip(tc *TrackedComposition, ref ClipReference) (ObjectID, error) {
	var matched []clipCandidate
	for i, c := range tc.PersistentClips() {
		label, _ := resolumecomp.PersistentClipLabel(i+1, c.Name)
		if !labelEquals(ref.Clip, label) {
			continue
		}
		layerLabel, layerKnown := clipLayerLabel(tc, c.LayerID)
		if ref.Layer != "" && (!layerKnown || ref.Layer != layerLabel) {
			continue
		}
		matched = append(matched, clipCandidate{c.ID, layerLabel, layerKnown})
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
			if m.layerKnown {
				layers[i] = fmt.Sprintf("on layer %q", m.layerLabel)
				layerCounts[m.layerLabel]++
			} else {
				// Not a real label: never rendered with %q, and never
				// grouped with another unknown-layer candidate, since two
				// clips this package cannot name a layer for are not
				// thereby known to share one.
				layers[i] = "on a layer this composition cannot identify"
				layerCounts[fmt.Sprintf("\x00unknown-%d", i)]++
			}
		}
		// Renaming is the only remedy exactly when EVERY matched candidate
		// already agrees on one layer: adding that layer to the reference
		// would still leave all of them matching. If even one candidate
		// differs, naming its layer resolves the reference right now, so
		// "add a layer" stays the correct advice for the whole refusal.
		if len(layerCounts) == 1 {
			return 0, fmt.Errorf(
				"more than one clip named %q %s (%s); these clips also share the same layer, so no reference can "+
					"ever tell them apart — rename one of them in Resolume and re-upload the composition",
				ref.Clip, scope, strings.Join(layers, ", "))
		}
		return 0, fmt.Errorf(
			`more than one clip named %q %s (%s); add a "layer" to this reference to disambiguate`,
			ref.Clip, scope, strings.Join(layers, ", "))
	}
}
