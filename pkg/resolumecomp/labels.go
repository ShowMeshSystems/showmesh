package resolumecomp

import "fmt"

// This file is the label vocabulary every operator-facing Resolume surface
// renders an object as. Every function here is pure over this package's own
// parsed types — no HTTP, no collector, no dispatcher — so both the
// composition read surface (internal/coordinator/api) and the live
// reference resolver (internal/coordinator/collector/resolume) call these
// same functions rather than keeping their own copies.
//
// One function per object kind; every caller uses these, never a second
// labeller. Compared byte-for-byte, case-sensitive: the operator's real
// composition contains a layer authored as "Peak + Under " (a trailing
// space) and a clip name containing a full-width vertical bar, so trimming
// or normalizing either side would silently address the wrong object.

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
// index. Columns never carry an authored name at all ([Column] has no Name
// field — the .avc format does not give one), so this is always generated.
func ColumnLabel(index int) string {
	return fmt.Sprintf("Column %d", index+1)
}

// DeckLabel returns a deck's operator-facing label from its 1-based
// position among a composition's decks and its authored name: the name
// when non-empty, otherwise the generated "Deck <n>" form. position is a
// caller-supplied ordinal, never recomputed from anything this function
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
// are the file's own semantic values ([Clip.LayerIndex],
// [Clip.ColumnIndex]), never a slice position.
func ClipLabel(layerIndex, columnIndex int, name string) (label string, generated bool) {
	if name != "" {
		return name, false
	}
	return fmt.Sprintf("Clip L%dC%d", layerIndex+1, columnIndex+1), true
}

// PersistentClipLabel returns a persistent clip's operator-facing label
// from its 1-based position among a composition's persistent clips and its
// authored name: the name when non-empty, otherwise the generated
// "Persistent clip <n>" form.
func PersistentClipLabel(position int, name string) (label string, generated bool) {
	if name != "" {
		return name, false
	}
	return fmt.Sprintf("Persistent clip %d", position), true
}

// LayerLabelByIndex resolves layerIndex (a clip's own layerIndex attribute,
// matching [Layer.Index]) to that layer's own [LayerLabel] among layers.
// known is false when no layer has that index; the caller must never treat
// two such clips as sharing a layer just because both are unresolved.
//
// A duplicate index keeps the LAST match, agreeing with any caller building
// its own id map with a plain map write (last one seen wins).
func LayerLabelByIndex(layers []Layer, layerIndex int) (label string, known bool) {
	for _, l := range layers {
		if l.Index == layerIndex {
			label, _ = LayerLabel(l.Index, l.Name)
			known = true
		}
	}
	return label, known
}

// ClipTripleKey is the (deck, layer, label) tuple two distinct clips must
// not share. Deck is "" for a persistent clip — a persistent clip's key is
// really (persistent, layer, label), and using "" rather than a real deck
// id is what keeps it from ever colliding with a deck clip's key, since a
// real deck id is never empty.
type ClipTripleKey struct {
	Deck  string
	Layer string
	Label string
}

// AmbiguousClipIDs reports, for every id in entries, whether its
// [ClipTripleKey] is shared by another entry. Every caller builds its own
// id/key pairs from its own data model and calls this one function, rather
// than each independently deciding what "shares a triple" means.
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
