// Package resolumecomp parses a Resolume Arena composition file (".avc",
// plain XML) into the id map ShowMesh stores as configuration.
//
// This package exists because of ADR-032: Resolume's own composition
// enumeration endpoint, GET /composition, is known to crash Arena 7.23.2 on
// the bench, and targeted by-id reads are the only endpoints confirmed
// safe. The .avc file the operator already has carries the same id map —
// clips, layers, layer groups, columns and decks, each with a stable
// uniqueId — so parsing the file at upload time removes the need to ever
// call the crashing endpoint at runtime. See ADR-032 decisions 1 and 2.
//
// This package is a pure parser: no OSC, no HTTP, no network, and it
// imports nothing outside the standard library. It never reads a file from
// a Resolume host's disk; the only ingestion path is whatever the caller
// hands to [Parse] (ADR-032 decision 4).
//
// The .avc format is undocumented. Everything this package assumes about
// element and attribute shape was measured against Resolume Arena 7.23.2
// composition files on one arm64 laptop (see the bench capture this ADR
// cites). [Composition.WrittenBy] records the Arena version that wrote the
// file specifically so a future parse that looks wrong has a version to
// suspect first, per ADR-032 decision 7 — it is a tripwire, not a
// guarantee that any other version parses the same way.
package resolumecomp

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
)

// DefaultMaxBytes bounds how much of the input [Parse] will read before
// giving up, so that an uploaded file cannot force unbounded allocation.
// Real composition files measured during the bench capture ranged from
// about 30 KB to about 2.6 MB; 64 MiB leaves generous headroom above that
// without being unbounded. Callers that know their own upload limits
// should prefer [ParseWithLimit] with that limit instead of relying on
// this default.
const DefaultMaxBytes int64 = 64 * 1024 * 1024

// Sentinel errors returned by [Parse] and [ParseWithLimit]. Use
// [errors.Is] to distinguish them; the wrapped detail is for logs, not for
// branching on.
var (
	// ErrNotXML means the input was not well-formed XML at all, or ended
	// before any element was found.
	ErrNotXML = errors.New("resolumecomp: input is not well-formed XML")

	// ErrWrongRoot means the input was well-formed XML but its root
	// element was not <Composition>.
	ErrWrongRoot = errors.New("resolumecomp: root element is not <Composition>")

	// ErrMissingCompositionInfo means the <Composition> element had no
	// <CompositionInfo> child, which is where the composition's name and
	// canvas size live.
	ErrMissingCompositionInfo = errors.New("resolumecomp: composition has no CompositionInfo")

	// ErrMalformedIndex means a <Clip> or <Layer> element was missing a
	// required index attribute, or the attribute was not a base-10
	// integer. This is only ever raised for a NON-EMPTY clip slot (or a
	// <Layer>, which has no empty-slot concept): an empty clip slot with a
	// malformed index is skipped, never rejected — see [isEmptyInnerXML]'s
	// call sites in [transform].
	ErrMalformedIndex = errors.New("resolumecomp: element has a missing or non-numeric index")

	// ErrMissingDeckID means a <Deck> element had no uniqueId attribute.
	// This aborts the whole parse rather than excluding that deck's
	// clips: [Composition.Clips] entries carry their DeckID (ADR-032
	// decision 6) specifically so a client can tell a stale clip
	// reference from one whose deck simply is not selected, and an empty
	// DeckID is the wire encoding reserved exclusively for a persistent
	// clip (ADR-032 decision 6's "PersistentClips ... live outside any
	// deck"). A deck clip with no deck id would be indistinguishable on
	// the wire from a persistent one — silently misclassifying it is
	// worse than refusing the file, so a composition with an
	// unidentifiable deck is not something this package half-imports.
	ErrMissingDeckID = errors.New("resolumecomp: a <Deck> element has no uniqueId")

	// ErrTooLarge means the input exceeded the size limit passed to
	// [ParseWithLimit] (or [DefaultMaxBytes] for [Parse]).
	ErrTooLarge = errors.New("resolumecomp: input exceeds the configured size limit")
)

// Composition is everything ShowMesh needs from a Resolume Arena
// composition file: the object id map, the names attached to it, and
// enough version and canvas information to sanity-check a future parse.
//
// A [Parse] that returns an error never returns a partially populated
// Composition (ADR-032 decision 7: a rejected file registers nothing).
type Composition struct {
	// Name is the composition's own name, from CompositionInfo/@name.
	// This is the value §12.3 of the bench capture found is mutable and
	// not unique across shows; it is a display label, not an identity.
	Name string `json:"name"`

	// WrittenBy is the product and version that wrote this file, read
	// from versionInfo. The format is undocumented and version-specific,
	// so this is what a future parse that looks wrong should check
	// first.
	WrittenBy WrittenBy `json:"writtenBy"`

	// Canvas is the composition's output size, from CompositionInfo.
	// This is not available anywhere over Resolume's REST API.
	Canvas Canvas `json:"canvas"`

	// Decks lists every deck in the file, in document order.
	Decks []Deck `json:"decks"`

	// LayerGroups lists every layer group in the file, in document
	// order. A composition with no groups has an empty slice.
	LayerGroups []LayerGroup `json:"layerGroups"`

	// Layers lists every layer in the file, in document order. Layers
	// are deck-independent (bench capture §16.1).
	Layers []Layer `json:"layers"`

	// Columns lists every distinct column per deck. See [Column] for the
	// deduplication rule and its uncertainty.
	Columns []Column `json:"columns"`

	// Clips lists every non-empty clip found inside a <Deck>. Every
	// entry carries a non-empty DeckID (ADR-032 decision 6): a clip id
	// resolves over Resolume's REST API only while its own deck is
	// selected, so a reference without a deck cannot tell a stale id
	// from an unselected deck.
	Clips []Clip `json:"clips"`

	// PersistentClips lists every non-empty clip found inside
	// <PersistentClips>. These live outside any deck and resolve by id
	// regardless of which deck is selected (bench capture §16.2), so
	// they are modelled separately and their DeckID is always empty.
	PersistentClips []Clip `json:"persistentClips"`
}

// WrittenBy identifies the application and version that wrote a
// composition file, read from the file's own versionInfo element. Nothing
// in this package validates these numbers against any known-good range;
// they are recorded so a caller can decide whether to trust a parse.
type WrittenBy struct {
	Product  string `json:"product"` // e.g. "Resolume Arena"
	Major    int    `json:"major"`
	Minor    int    `json:"minor"`
	Micro    int    `json:"micro"`
	Revision int    `json:"revision"`
}

// String renders WrittenBy as "<Product> <major>.<minor>.<micro>
// r<revision>", e.g. "Resolume Arena 7.23.2 r51094".
func (w WrittenBy) String() string {
	return fmt.Sprintf("%s %d.%d.%d r%d", w.Product, w.Major, w.Minor, w.Micro, w.Revision)
}

// Canvas is a composition's output size in pixels.
type Canvas struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// Deck is one deck (what the operator's own vocabulary and the bench
// capture's §9.3 correction call a "page"). Its Name is not on the <Deck>
// element itself — that element's name attribute is the literal string
// "Deck" — but joined from CompositionInfo/DeckInfo by id. If no DeckInfo
// entry matches a deck's id, Name is left empty rather than erroring the
// whole parse; that mismatch has not been observed in any file this
// package was verified against.
type Deck struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Closed bool   `json:"closed"`
}

// LayerGroup is one layer group. Its Index is its position among
// <Group> elements in document order, which is what [Layer.LayerGroupIndex]
// refers to — the file's own layerGroup attribute on a Layer is a small
// integer matching this position, not a Group's uniqueId.
type LayerGroup struct {
	ID    string `json:"id"`
	Index int    `json:"index"`
}

// Layer is one layer, deck-independent per the bench capture's §16.1
// correction (all 18 layer ids resolved regardless of which deck was
// selected).
type Layer struct {
	ID    string `json:"id"`
	Index int    `json:"index"`

	// Name is the layer's display name, read from the nested
	// Param[@name='Name'] inside the layer's own Params block — never
	// from the <Layer> element's own name attribute, which is always the
	// literal string "Layer" (ADR-037 decision 7; identical trap and
	// identical fix to [Clip.Name]). Empty when the layer has no such
	// Param at all: measured 5 of 18 layers in the operator's own
	// composition, and this package does not invent a value for those —
	// a generated positional label is a display concern for a caller
	// that wants an operator-facing name, not a fact this parser reports.
	Name string `json:"name"`

	// LayerGroupIndex is the layer's own layerGroup attribute, taken
	// as-is from the file. It is nil, not zero, when the composition has
	// no layer groups at all: the file omits the layerGroup attribute
	// entirely in that case rather than writing a 0 that would look like
	// membership in a first group that does not exist.
	//
	// This package does NOT validate that the value is within
	// [0, len(Composition.LayerGroups)): it is reported exactly as the
	// file states it, never clamped, defaulted, or rejected. A
	// composition file whose layerGroup value points past the end of its
	// own <Group> elements (or is negative) is not a shape this package
	// has ever observed, but the fix for an out-of-range value is a
	// question for whoever wrote the file, not something this parser
	// should paper over by inventing a group that is not there. A caller
	// that needs an actual [LayerGroup] must bounds-check this value
	// against [Composition.LayerGroups] itself before indexing with it.
	LayerGroupIndex *int `json:"layerGroupIndex,omitempty"`
}

// Column is one column position within one deck.
//
// Measured, not inferred: a deck's <Column> elements outnumber its visible
// columns — 56 elements for 14 visible columns on one deck captured, 36 for
// 9 on two others, consistently 4 elements per column index. The bench
// capture's own §9.2 records this as consistent with one composition-level
// column plus one per layer group, but is explicit that this is an
// inference, not a measurement, and this package does not build on it.
//
// What this package does instead: for each deck, elements are deduplicated
// by columnIndex, keeping the id of the first Column element seen for that
// index in document order and discarding the rest. If the 4-elements-per-
// column pattern reflects distinct per-layer-group column objects rather
// than duplicates of one column, this discards information this package
// cannot currently interpret. That loss is deliberate: modelling a
// structure this package has not verified would be worse than reporting
// one id per column index.
type Column struct {
	ID     string `json:"id"`
	DeckID string `json:"deckId"`
	Index  int    `json:"index"`
}

// Clip is one non-empty clip: either a deck clip (in [Composition.Clips],
// with DeckID set) or a persistent clip (in
// [Composition.PersistentClips], with DeckID empty).
//
// Only non-empty clips are worth storing. A clip slot with no content is a
// <Clip> element with no children at all — measured at 226 empty of 252
// slots on one deck — and this package excludes those entirely rather than
// representing them as zero-valued entries.
type Clip struct {
	ID string `json:"id"`

	// DeckID is the id of the deck this clip belongs to, or empty for a
	// persistent clip. Every entry in Composition.Clips has this set;
	// every entry in Composition.PersistentClips does not. See ADR-032
	// decision 6 for why the deck is part of a clip reference's identity.
	DeckID string `json:"deckId,omitempty"`

	LayerIndex  int `json:"layerIndex"`
	ColumnIndex int `json:"columnIndex"`

	// Name is the clip's display name, read from the nested
	// Param[@name='Name'] inside the clip's Params block — never from
	// the <Clip> element's own name attribute, which is always the
	// literal string "Clip" and carries no information. Reading the
	// attribute instead of the nested param is the one mistake this
	// package's tests exist specifically to catch.
	Name string `json:"name"`

	// TransportTypeIndex is the raw index carried by the clip's
	// TransportType ParamChoice, or nil if the clip has no such param.
	// This package deliberately does not map it to a label: the bench
	// capture's §4.3 and §8.1 established that the option list for this
	// parameter is served inline over REST, varies per clip, and is not
	// present in the file at all. Inventing an enum from one sample
	// would be exactly the mistake that finding warns against.
	TransportTypeIndex *int `json:"transportTypeIndex,omitempty"`

	// SourcePath is the clip's source media path, from
	// PreloadData/VideoFile. It is empty for a clip with no PreloadData
	// element at all, which is normal for a generator clip (a solid
	// color, a router, and similar have no source file) — every
	// persistent clip captured during the bench work was of this kind.
	SourcePath string `json:"sourcePath,omitempty"`

	// Width and Height are the clip's VideoTrack/Params dimensions, or
	// nil if the clip has no VideoTrack/Params block at all. This is
	// not necessarily the source media's native resolution; it is
	// whatever the clip's video track parameters record.
	Width  *int `json:"width,omitempty"`
	Height *int `json:"height,omitempty"`
}

// Parse reads a Resolume .avc composition file from r and returns its
// parsed id map. It is equivalent to ParseWithLimit(r, DefaultMaxBytes).
//
// A malformed file never produces a partially populated *Composition: on
// any error the returned Composition is nil (ADR-032 decision 7).
func Parse(r io.Reader) (*Composition, error) {
	return ParseWithLimit(r, DefaultMaxBytes)
}

// ParseWithLimit is [Parse] with a caller-supplied ceiling on how many
// bytes will be read from r. This is the bound an upload handler should
// use: an uploaded file is untrusted input, and reading it without a limit
// lets a hostile or merely corrupt file force unbounded allocation before
// this package ever gets to look at its structure.
//
// maxBytes must be positive.
//
// On XML entity expansion: Go's encoding/xml, which this package uses
// throughout and never configures with a custom Decoder.Entity map,
// resolves only the five predefined XML entities and does not process
// DTD-declared or external entities at all. That means the classic
// XXE and "billion laughs" expansion attacks, which depend on a parser
// expanding attacker-declared entities, have no entity declaration to
// expand here. This is based on encoding/xml's documented behavior, not
// on this package having been fuzzed against those attacks.
func ParseWithLimit(r io.Reader, maxBytes int64) (*Composition, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("resolumecomp: maxBytes must be positive, got %d", maxBytes)
	}

	// Read up to one byte past the limit so we can tell "exactly at the
	// limit" apart from "over the limit" without a separate stat call —
	// io.Reader does not guarantee one is possible (e.g. r may be a
	// network body with no known length).
	data, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("resolumecomp: reading input: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, ErrTooLarge
	}

	dec := xml.NewDecoder(bytes.NewReader(data))

	var start *xml.StartElement
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, ErrNotXML
			}
			return nil, fmt.Errorf("%w: %v", ErrNotXML, err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			start = &se
			break
		}
	}

	if start.Name.Local != "Composition" {
		return nil, fmt.Errorf("%w: found <%s>", ErrWrongRoot, start.Name.Local)
	}

	var raw rawComposition
	if err := dec.DecodeElement(&raw, start); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrNotXML, err)
	}

	return transform(&raw)
}

// --- raw XML shape ---
//
// These types mirror the .avc structure measured against real composition
// files, per doc.go's provenance note. They are unexported: nothing outside
// this file should depend on the raw shape, only on the transformed
// [Composition] model.
//
// A recurring trap in this format, documented once here rather than on
// every field it touches: <Deck>, <Layer> and <Clip> elements all carry a
// name attribute, and on every one of them the value is the literal
// string "Deck", "Layer", or "Clip" — never the operator's own name for
// that object. The real names live elsewhere: deck names in
// CompositionInfo/DeckInfo joined by id, and layer and clip names in a
// nested Param[@name='Name'] inside that element's own Params block
// (ADR-037 decision 7 added layers to this rule; clip names were already
// read this way). A layer's Params block is optional — 5 of 18 layers in
// the operator's own composition had none — so a layer's Name is simply
// empty in that case, not an error. None of the raw structs below capture
// the literal name attributes, specifically so that nothing downstream
// can be tempted to read one.

type rawComposition struct {
	CompositionInfo *rawCompositionInfo `xml:"CompositionInfo"`
	VersionInfo     *rawVersionInfo     `xml:"versionInfo"`
	Layers          []rawLayer          `xml:"Layer"`
	Groups          []rawGroup          `xml:"Group"`
	Decks           []rawDeck           `xml:"Deck"`
	PersistentClips *rawPersistentClips `xml:"PersistentClips"`
}

type rawVersionInfo struct {
	Name     string `xml:"name,attr"`
	Major    string `xml:"majorVersion,attr"`
	Minor    string `xml:"minorVersion,attr"`
	Micro    string `xml:"microVersion,attr"`
	Revision string `xml:"revision,attr"`
}

type rawCompositionInfo struct {
	Name   string        `xml:"name,attr"`
	Width  string        `xml:"width,attr"`
	Height string        `xml:"height,attr"`
	Decks  []rawDeckInfo `xml:"DeckInfo"`
}

type rawDeckInfo struct {
	Name   string `xml:"name,attr"`
	ID     string `xml:"id,attr"`
	Closed string `xml:"closed,attr"`
}

// rawLayer declares ONLY the elements a layer's own Name lives in — no
// field here can match anything nested deeper (e.g. inside a clip or an
// effect the real Layer element may also carry): encoding/xml only
// descends into elements a struct field names, so an undeclared child is
// skipped whole, never searched. That is what keeps [layerName] attributed
// to the layer itself rather than to something the layer merely contains.
type rawLayer struct {
	ID         string           `xml:"uniqueId,attr"`
	Index      *string          `xml:"layerIndex,attr"`
	LayerGroup *string          `xml:"layerGroup,attr"`
	Params     []rawParamsBlock `xml:"Params"`
}

type rawGroup struct {
	ID string `xml:"uniqueId,attr"`
}

type rawDeck struct {
	ID      string      `xml:"uniqueId,attr"`
	Columns []rawColumn `xml:"Column"`
	Clips   []rawClip   `xml:"Clip"`
}

type rawColumn struct {
	ID    string  `xml:"uniqueId,attr"`
	Index *string `xml:"columnIndex,attr"`
}

// rawClip covers both a deck's direct <Clip> children and a
// <PersistentClip>'s nested <Clip>. On a deck clip, LayerIndex and
// ColumnIndex are attributes of this element. On a persistent clip's
// inner <Clip>, they are not present here at all — they live on the
// enclosing <PersistentClip> instead (rawPersistentClip below) — so both
// fields are pointers and callers must get index information from the
// right place for the kind of clip they are looking at.
type rawClip struct {
	ID          string           `xml:"uniqueId,attr"`
	LayerIndex  *string          `xml:"layerIndex,attr"`
	ColumnIndex *string          `xml:"columnIndex,attr"`
	PreloadData *rawPreloadData  `xml:"PreloadData"`
	Params      []rawParamsBlock `xml:"Params"`
	VideoTrack  *rawVideoTrack   `xml:"VideoTrack"`

	// InnerXML is every byte inside this <Clip>...</Clip>, used only to
	// tell an empty clip slot (no children at all) apart from a clip
	// this package simply has no named field for. Measured emptiness is
	// "a Clip element with no children", not "a Clip element with none
	// of the three children this package understands" — those are not
	// the same claim, and using InnerXML keeps this package honest about
	// which one it is checking.
	InnerXML []byte `xml:",innerxml"`
}

type rawPreloadData struct {
	VideoFile *rawValueAttr `xml:"VideoFile"`
}

type rawValueAttr struct {
	Value string `xml:"value,attr"`
}

// rawParamsBlock is a <Params name="..."> block. A <Clip> has more than
// one of these (at least "Params" and "Dashboard" were observed); only the
// one whose Name is "Params" carries the clip's display Name and
// TransportType, and this package searches for it by that attribute
// rather than assuming position or count.
type rawParamsBlock struct {
	Name         string              `xml:"name,attr"`
	Params       []rawNamedValueAttr `xml:"Param"`
	ParamChoices []rawNamedValueAttr `xml:"ParamChoice"`
}

type rawNamedValueAttr struct {
	Name  string `xml:"name,attr"`
	Value string `xml:"value,attr"`
}

type rawVideoTrack struct {
	Params *rawVideoTrackParams `xml:"Params"`
}

type rawVideoTrackParams struct {
	Ranges []rawNamedValueAttr `xml:"ParamRange"`
}

type rawPersistentClips struct {
	Items []rawPersistentClip `xml:"PersistentClip"`
}

type rawPersistentClip struct {
	LayerIndex  *string                `xml:"layerIndex,attr"`
	ColumnIndex *string                `xml:"columnIndex,attr"`
	Clip        rawPersistentInnerClip `xml:"Clip"`
}

// rawPersistentInnerClip is a <PersistentClip>'s nested <Clip>. It shares
// its content fields with rawClip but, measured against every persistent
// clip in the bench capture's composition, carries no layerIndex or
// columnIndex of its own and no PreloadData (persistent clips observed so
// far are all generator clips with no source file) — see rawClip's doc
// comment for where those indices actually live for this kind of clip.
type rawPersistentInnerClip struct {
	ID          string           `xml:"uniqueId,attr"`
	PreloadData *rawPreloadData  `xml:"PreloadData"`
	Params      []rawParamsBlock `xml:"Params"`
	VideoTrack  *rawVideoTrack   `xml:"VideoTrack"`
	InnerXML    []byte           `xml:",innerxml"`
}

// --- transform: raw XML shape -> public model ---

func transform(raw *rawComposition) (*Composition, error) {
	if raw.CompositionInfo == nil {
		return nil, ErrMissingCompositionInfo
	}

	c := &Composition{
		Name:      raw.CompositionInfo.Name,
		WrittenBy: transformVersionInfo(raw.VersionInfo),
	}

	c.Canvas = Canvas{
		Width:  parseOptionalInt(raw.CompositionInfo.Width),
		Height: parseOptionalInt(raw.CompositionInfo.Height),
	}

	deckInfoByID := make(map[string]rawDeckInfo, len(raw.CompositionInfo.Decks))
	for _, di := range raw.CompositionInfo.Decks {
		deckInfoByID[di.ID] = di
	}

	for _, g := range raw.Groups {
		c.LayerGroups = append(c.LayerGroups, LayerGroup{
			ID:    g.ID,
			Index: len(c.LayerGroups),
		})
	}

	for _, l := range raw.Layers {
		idx, err := parseRequiredInt(l.Index, "layer layerIndex")
		if err != nil {
			return nil, err
		}
		var groupIdx *int
		if l.LayerGroup != nil {
			n, err := strconv.Atoi(*l.LayerGroup)
			if err != nil {
				return nil, fmt.Errorf("%w: layer layerGroup %q is not numeric", ErrMalformedIndex, *l.LayerGroup)
			}
			groupIdx = &n
		}
		c.Layers = append(c.Layers, Layer{
			ID:              l.ID,
			Index:           idx,
			Name:            layerName(l.Params),
			LayerGroupIndex: groupIdx,
		})
	}

	for _, d := range raw.Decks {
		if d.ID == "" {
			return nil, ErrMissingDeckID
		}
		info, known := deckInfoByID[d.ID]
		deck := Deck{ID: d.ID}
		if known {
			deck.Name = info.Name
			deck.Closed = info.Closed == "1"
		}
		c.Decks = append(c.Decks, deck)

		seenColumn := make(map[int]bool, len(d.Columns))
		for _, col := range d.Columns {
			idx, err := parseRequiredInt(col.Index, "column columnIndex")
			if err != nil {
				return nil, err
			}
			if seenColumn[idx] {
				continue
			}
			seenColumn[idx] = true
			c.Columns = append(c.Columns, Column{
				ID:     col.ID,
				DeckID: d.ID,
				Index:  idx,
			})
		}

		for _, rc := range d.Clips {
			// Emptiness is checked BEFORE either index is parsed
			// (finding I): an empty clip slot is excluded regardless of
			// what its layerIndex/columnIndex attributes say, so a
			// malformed index on a slot this package was going to
			// discard anyway must never cost the operator the rest of
			// the file. Real compositions are mostly empty slots (226 of
			// 252 measured in the bench capture), so this ordering is
			// the common case, not an edge case.
			if isEmptyInnerXML(rc.InnerXML) {
				continue
			}
			layerIdx, err := parseRequiredInt(rc.LayerIndex, "clip layerIndex")
			if err != nil {
				return nil, err
			}
			colIdx, err := parseRequiredInt(rc.ColumnIndex, "clip columnIndex")
			if err != nil {
				return nil, err
			}
			clip := Clip{
				ID:          rc.ID,
				DeckID:      d.ID,
				LayerIndex:  layerIdx,
				ColumnIndex: colIdx,
			}
			populateClipContent(&clip, rc.Params, rc.PreloadData, rc.VideoTrack)
			c.Clips = append(c.Clips, clip)
		}
	}

	if raw.PersistentClips != nil {
		for _, pc := range raw.PersistentClips.Items {
			// Same ordering, and the same reason, as the deck-clip loop
			// above: emptiness is checked before either index is parsed.
			if isEmptyInnerXML(pc.Clip.InnerXML) {
				continue
			}
			layerIdx, err := parseRequiredInt(pc.LayerIndex, "persistent clip layerIndex")
			if err != nil {
				return nil, err
			}
			colIdx, err := parseRequiredInt(pc.ColumnIndex, "persistent clip columnIndex")
			if err != nil {
				return nil, err
			}
			clip := Clip{
				ID:          pc.Clip.ID,
				LayerIndex:  layerIdx,
				ColumnIndex: colIdx,
			}
			populateClipContent(&clip, pc.Clip.Params, pc.Clip.PreloadData, pc.Clip.VideoTrack)
			c.PersistentClips = append(c.PersistentClips, clip)
		}
	}

	return c, nil
}

func transformVersionInfo(vi *rawVersionInfo) WrittenBy {
	if vi == nil {
		return WrittenBy{}
	}
	// versionInfo's numeric attributes are not validated as numeric here:
	// an unparsable version number degrades to 0 in that one field rather
	// than rejecting the whole composition, because a file's own version
	// stamp being odd is not evidence its id map is wrong. Any such
	// degradation is visible in the returned struct, which is the point
	// of recording it at all.
	major, _ := strconv.Atoi(vi.Major)
	minor, _ := strconv.Atoi(vi.Minor)
	micro, _ := strconv.Atoi(vi.Micro)
	revision, _ := strconv.Atoi(vi.Revision)
	return WrittenBy{
		Product:  vi.Name,
		Major:    major,
		Minor:    minor,
		Micro:    micro,
		Revision: revision,
	}
}

// layerName reads a layer's display name out of its own Params blocks —
// the identical Param[@name='Name'] shape [populateClipContent] reads a
// clip's Name from. Only the block whose own name attribute is "Params"
// is searched (a layer, like a clip, may carry more than one Params
// block), and a layer with no such block, or no such Param inside it,
// returns "" rather than an invented value — see [Layer.Name]'s own doc
// comment for why that is correct rather than an omission.
func layerName(blocks []rawParamsBlock) string {
	for _, block := range blocks {
		if block.Name != "Params" {
			continue
		}
		for _, p := range block.Params {
			if p.Name == "Name" {
				return p.Value
			}
		}
	}
	return ""
}

// populateClipContent fills in the parts of clip that come from a clip's
// Params blocks, PreloadData, and VideoTrack. It is shared between deck
// clips and persistent clips, which carry these in the same shape.
func populateClipContent(clip *Clip, params []rawParamsBlock, preload *rawPreloadData, videoTrack *rawVideoTrack) {
	for _, block := range params {
		if block.Name != "Params" {
			continue
		}
		for _, p := range block.Params {
			if p.Name == "Name" {
				clip.Name = p.Value
			}
		}
		for _, pc := range block.ParamChoices {
			if pc.Name == "TransportType" {
				if n, err := strconv.Atoi(pc.Value); err == nil {
					clip.TransportTypeIndex = &n
				}
			}
		}
	}

	if preload != nil && preload.VideoFile != nil {
		clip.SourcePath = preload.VideoFile.Value
	}

	if videoTrack != nil && videoTrack.Params != nil {
		for _, r := range videoTrack.Params.Ranges {
			switch r.Name {
			case "Width":
				if n, err := strconv.Atoi(r.Value); err == nil {
					clip.Width = &n
				}
			case "Height":
				if n, err := strconv.Atoi(r.Value); err == nil {
					clip.Height = &n
				}
			}
		}
	}
}

func isEmptyInnerXML(b []byte) bool {
	return len(bytes.TrimSpace(b)) == 0
}

func parseRequiredInt(s *string, what string) (int, error) {
	if s == nil {
		return 0, fmt.Errorf("%w: %s is missing", ErrMalformedIndex, what)
	}
	n, err := strconv.Atoi(*s)
	if err != nil {
		return 0, fmt.Errorf("%w: %s %q is not numeric", ErrMalformedIndex, what, *s)
	}
	return n, nil
}

// parseOptionalInt parses s as a base-10 integer, returning 0 for an empty
// string (an absent width/height attribute) AND for a present but
// non-numeric one — the canvas size is useful metadata, not a
// correctness-critical index (unlike a layer or clip index, which
// [parseRequiredInt] guards), so any way s fails to parse degrades to 0
// rather than rejecting the whole file. This has no error return at all
// (unlike an earlier version of this function) specifically so this
// behaviour cannot silently regress into "blank degrades, malformed
// rejects": there is no error path left for a caller to mishandle.
func parseOptionalInt(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}
