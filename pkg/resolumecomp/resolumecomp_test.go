package resolumecomp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParseTestdata(t *testing.T, name string) *Composition {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading testdata/%s: %v", name, err)
	}
	c, err := Parse(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Parse(testdata/%s): unexpected error: %v", name, err)
	}
	return c
}

// TestParse_CompleteComposition_RoundTrips covers claim 1 from the seam
// spec: a complete small composition round-trips into the expected model,
// including deck names joined from DeckInfo, group membership (including a
// layer with no group at all), and duplicate Column elements sharing one
// columnIndex being deduplicated.
func TestParse_CompleteComposition_RoundTrips(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	if c.Name != "Holiday Test Show" {
		t.Errorf("Name = %q, want %q", c.Name, "Holiday Test Show")
	}
	wantWrittenBy := WrittenBy{Product: "Resolume Arena", Major: 7, Minor: 23, Micro: 2, Revision: 51094}
	if c.WrittenBy != wantWrittenBy {
		t.Errorf("WrittenBy = %+v, want %+v", c.WrittenBy, wantWrittenBy)
	}
	if got, want := c.WrittenBy.String(), "Resolume Arena 7.23.2 r51094"; got != want {
		t.Errorf("WrittenBy.String() = %q, want %q", got, want)
	}
	if c.Canvas != (Canvas{Width: 1920, Height: 1080}) {
		t.Errorf("Canvas = %+v, want {1920 1080}", c.Canvas)
	}

	// Deck names are not on <Deck> itself; they must be joined from
	// CompositionInfo/DeckInfo by id.
	if len(c.Decks) != 2 {
		t.Fatalf("len(Decks) = %d, want 2", len(c.Decks))
	}
	wantDecks := map[string]Deck{
		"2000000000001": {ID: "2000000000001", Name: "Deck One", Closed: false},
		"2000000000002": {ID: "2000000000002", Name: "Deck Two", Closed: true},
	}
	for _, d := range c.Decks {
		want, ok := wantDecks[d.ID]
		if !ok {
			t.Errorf("unexpected deck id %q", d.ID)
			continue
		}
		if d != want {
			t.Errorf("deck %q = %+v, want %+v", d.ID, d, want)
		}
	}

	// LayerGroups: two groups, indexed by document order.
	if len(c.LayerGroups) != 2 {
		t.Fatalf("len(LayerGroups) = %d, want 2", len(c.LayerGroups))
	}
	if c.LayerGroups[0] != (LayerGroup{ID: "4000000000001", Index: 0}) {
		t.Errorf("LayerGroups[0] = %+v", c.LayerGroups[0])
	}
	if c.LayerGroups[1] != (LayerGroup{ID: "4000000000002", Index: 1}) {
		t.Errorf("LayerGroups[1] = %+v", c.LayerGroups[1])
	}

	// Layers: three layers, the third with no layerGroup attribute at
	// all, which must come through as nil rather than a zero pointing at
	// group 0.
	if len(c.Layers) != 3 {
		t.Fatalf("len(Layers) = %d, want 3", len(c.Layers))
	}
	if c.Layers[0].ID != "3000000000001" || c.Layers[0].LayerGroupIndex == nil || *c.Layers[0].LayerGroupIndex != 0 {
		t.Errorf("Layers[0] = %+v, want group 0", c.Layers[0])
	}
	if c.Layers[1].ID != "3000000000002" || c.Layers[1].LayerGroupIndex == nil || *c.Layers[1].LayerGroupIndex != 1 {
		t.Errorf("Layers[1] = %+v, want group 1", c.Layers[1])
	}
	if c.Layers[2].ID != "3000000000003" || c.Layers[2].LayerGroupIndex != nil {
		t.Errorf("Layers[2] = %+v, want LayerGroupIndex nil (no layerGroup attribute in the file)", c.Layers[2])
	}

	// Columns: Deck One has three <Column> elements but only two distinct
	// columnIndex values (0 duplicated, 1 once); Deck Two has one. The
	// duplicate must be deduplicated down to the first id seen.
	if len(c.Columns) != 3 {
		t.Fatalf("len(Columns) = %d, want 3 (2 on Deck One + 1 on Deck Two after dedup)", len(c.Columns))
	}
	foundDeckOneCol0 := false
	for _, col := range c.Columns {
		if col.DeckID == "2000000000001" && col.Index == 0 {
			foundDeckOneCol0 = true
			if col.ID != "5000000000001" {
				t.Errorf("Deck One column 0 id = %q, want the first-seen id 5000000000001 (duplicate 5000000000009 must be dropped)", col.ID)
			}
		}
	}
	if !foundDeckOneCol0 {
		t.Errorf("Deck One column 0 not found in %+v", c.Columns)
	}

	// Persistent clips are separate from deck clips.
	if len(c.PersistentClips) != 2 {
		t.Fatalf("len(PersistentClips) = %d, want 2", len(c.PersistentClips))
	}
	for _, pc := range c.PersistentClips {
		if pc.DeckID != "" {
			t.Errorf("persistent clip %q has DeckID %q, want empty", pc.ID, pc.DeckID)
		}
	}
}

// TestParse_ClipNameComesFromNestedParamNotAttribute is claim 2: a clip
// whose <Clip> name attribute is the literal string "Clip" but whose
// nested Param[@name='Name'] is "Snowflakes" must be named "Snowflakes".
// Reading the attribute instead of the nested param is the one mistake
// this format specifically sets up for a naive parser.
func TestParse_ClipNameComesFromNestedParamNotAttribute(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	var found *Clip
	for i := range c.Clips {
		if c.Clips[i].ID == "6000000000001" {
			found = &c.Clips[i]
		}
	}
	if found == nil {
		t.Fatalf("clip 6000000000001 not found in %+v", c.Clips)
	}
	if found.Name != "Snowflakes" {
		t.Errorf("clip name = %q, want %q (the nested Param[@name='Name'] value, not the element's own name=\"Clip\" attribute)", found.Name, "Snowflakes")
	}
}

// TestParse_LayerNameComesFromNestedParamNotAttribute is ADR-037 decision
// 7's own claim, the layer half of the identical trap
// TestParse_ClipNameComesFromNestedParamNotAttribute covers for clips: a
// layer whose <Layer> name attribute is the literal string "Layer" but
// whose own Params block carries a nested Param[@name='Name'] must be
// named from that param, not the attribute, and a decoy Params block named
// "Dashboard" on the same layer must be ignored. This fails if the name is
// dropped (Layers[0].Name comes back "").
func TestParse_LayerNameComesFromNestedParamNotAttribute(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	if len(c.Layers) < 1 {
		t.Fatalf("len(Layers) = %d, want at least 1", len(c.Layers))
	}
	if got, want := c.Layers[0].Name, "Peak Only"; got != want {
		t.Errorf("Layers[0].Name = %q, want %q (the nested Param[@name='Name'] inside the layer's own \"Params\" block, not the \"Dashboard\" decoy or the element's own name=\"Layer\" attribute)", got, want)
	}
}

// TestParse_LayerWithNoNameParamIsEmptyNotInvented is the "5 of 18 layers
// have no Name param at all" case measured against the operator's real
// composition: a layer with no Params block whatsoever must decode to an
// empty Name, never a placeholder invented at parse time — inventing a
// display label is a presentation decision for a caller, not a fact this
// parser reports (see [Layer.Name]'s own doc comment). This fails if an
// unnamed layer renders as anything other than "".
func TestParse_LayerWithNoNameParamIsEmptyNotInvented(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	if len(c.Layers) < 2 {
		t.Fatalf("len(Layers) = %d, want at least 2", len(c.Layers))
	}
	if got := c.Layers[1].Name; got != "" {
		t.Errorf("Layers[1].Name = %q, want \"\" (this layer has no Params block in the fixture)", got)
	}
}

// TestParse_LayerNameTrailingSpaceIsPreserved is the "Peak + Under "
// measurement from ADR-037: an operator-typed name is not an identifier
// and this package must never trim it, silently or otherwise.
func TestParse_LayerNameTrailingSpaceIsPreserved(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	if len(c.Layers) < 3 {
		t.Fatalf("len(Layers) = %d, want at least 3", len(c.Layers))
	}
	if got, want := c.Layers[2].Name, "Peak + Under "; got != want {
		t.Errorf("Layers[2].Name = %q, want %q (trailing space must survive verbatim)", got, want)
	}
}

// TestParse_EmptyClipSlotsExcluded is claim 3: an empty clip slot (a <Clip>
// element with no children) must not appear in the model, and the count
// arithmetic must hold: complete.avc has 4 <Clip> elements across both
// decks, 1 of which is empty, so exactly 3 must survive into Composition.Clips.
func TestParse_EmptyClipSlotsExcluded(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	if len(c.Clips) != 3 {
		t.Fatalf("len(Clips) = %d, want 3 (4 <Clip> elements in the fixture, 1 empty)", len(c.Clips))
	}
	for _, clip := range c.Clips {
		if clip.ID == "6000000000002" {
			t.Errorf("empty clip slot 6000000000002 was included in Clips")
		}
	}
}

// TestParse_EveryDeckClipCarriesMatchingDeckID is claim 5: every
// non-persistent clip must carry a DeckID that matches a deck present in
// the composition. This is ADR-032 decision 6: a clip id only resolves
// over Resolume's REST API while its own deck is selected, so a reference
// without a deck cannot tell a stale id from an unselected one.
func TestParse_EveryDeckClipCarriesMatchingDeckID(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	deckIDs := make(map[string]bool, len(c.Decks))
	for _, d := range c.Decks {
		deckIDs[d.ID] = true
	}
	if len(c.Clips) == 0 {
		t.Fatal("no deck clips parsed; nothing to check")
	}
	for _, clip := range c.Clips {
		if clip.DeckID == "" {
			t.Errorf("deck clip %q has empty DeckID", clip.ID)
			continue
		}
		if !deckIDs[clip.DeckID] {
			t.Errorf("deck clip %q has DeckID %q, which matches no deck in Decks", clip.ID, clip.DeckID)
		}
	}
}

// TestParse_TransportTypeIndexPreservedAsRawIndex is claim 7: the file
// gives only a raw ParamChoice index for TransportType, with no options
// list to resolve it against (that list is REST-only and varies per
// clip). This package must store the raw index and must not invent a
// label for it.
func TestParse_TransportTypeIndexPreservedAsRawIndex(t *testing.T) {
	c := mustParseTestdata(t, "complete.avc")

	want := map[string]int{
		"6000000000001": 2, // Snowflakes
		"6000000000003": 0, // Clip B
		"6000000000101": 1, // Clip C
	}
	got := make(map[string]int)
	for _, clip := range c.Clips {
		if clip.TransportTypeIndex == nil {
			t.Errorf("clip %q: TransportTypeIndex is nil, want a value", clip.ID)
			continue
		}
		got[clip.ID] = *clip.TransportTypeIndex
	}
	for id, wantIdx := range want {
		if gotIdx, ok := got[id]; !ok || gotIdx != wantIdx {
			t.Errorf("clip %q: TransportTypeIndex = %v, want %d", id, got[id], wantIdx)
		}
	}
}

// TestParse_MalformedInputs_ReturnNoPartialModel covers claim 6: each
// malformed-input case must error, and on error the returned *Composition
// must be nil, never a partially populated value (ADR-032 decision 7: a
// rejected file registers nothing).
func TestParse_MalformedInputs_ReturnNoPartialModel(t *testing.T) {
	cases := []struct {
		file    string
		wantErr error
	}{
		{"not-xml.txt", ErrNotXML},
		{"wrong-root.avc", ErrWrongRoot},
		{"missing-compositioninfo.avc", ErrMissingCompositionInfo},
		{"bad-layerindex.avc", ErrMalformedIndex},
		{"missing-layerindex.avc", ErrMalformedIndex},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tc.file))
			if err != nil {
				t.Fatalf("reading testdata/%s: %v", tc.file, err)
			}
			c, err := Parse(bytes.NewReader(data))
			if err == nil {
				t.Fatalf("Parse(testdata/%s) = %+v, nil; want an error", tc.file, c)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Errorf("Parse(testdata/%s) error = %v, want it to wrap %v", tc.file, err, tc.wantErr)
			}
			if c != nil {
				t.Errorf("Parse(testdata/%s) returned a non-nil Composition alongside an error: %+v", tc.file, c)
			}
		})
	}
}

// TestParseWithLimit_RejectsOversizedInput checks that an input larger
// than the caller-supplied limit is rejected before being handed to the
// XML decoder, rather than being read without bound.
func TestParseWithLimit_RejectsOversizedInput(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "complete.avc"))
	if err != nil {
		t.Fatalf("reading testdata/complete.avc: %v", err)
	}

	if _, err := ParseWithLimit(bytes.NewReader(data), int64(len(data))); err != nil {
		t.Fatalf("ParseWithLimit at exact size: unexpected error: %v", err)
	}

	_, err = ParseWithLimit(bytes.NewReader(data), int64(len(data))-1)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("ParseWithLimit one byte under size: error = %v, want ErrTooLarge", err)
	}
}

// TestParse_WrittenByString_TripwireFormat pins the String() rendering
// used as the version tripwire, so a future refactor cannot silently
// change what an operator sees without a test noticing.
func TestParse_WrittenByString_TripwireFormat(t *testing.T) {
	w := WrittenBy{Product: "Resolume Arena", Major: 7, Minor: 23, Micro: 2, Revision: 51094}
	if got := w.String(); !strings.Contains(got, "7.23.2") || !strings.Contains(got, "r51094") {
		t.Errorf("WrittenBy.String() = %q, want it to contain the version and revision", got)
	}
}

// TestParse_DeckWithNoUniqueIDIsRejected is review finding C: a <Deck> with
// no uniqueId must reject the whole parse with ErrMissingDeckID rather than
// silently producing a deck clip with an empty DeckID — the wire encoding
// ADR-032 decision 6 reserves exclusively for a persistent clip. Before
// this fix, transform() assigned DeckID: d.ID unvalidated, so this fixture
// would have parsed "successfully" into a clip indistinguishable, on the
// wire, from one living outside any deck.
func TestParse_DeckWithNoUniqueIDIsRejected(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "deck-missing-id.avc"))
	if err != nil {
		t.Fatalf("reading testdata/deck-missing-id.avc: %v", err)
	}
	c, err := Parse(bytes.NewReader(data))
	if !errors.Is(err, ErrMissingDeckID) {
		t.Fatalf("Parse(deck-missing-id.avc) error = %v, want it to wrap ErrMissingDeckID", err)
	}
	if c != nil {
		t.Errorf("Parse(deck-missing-id.avc) returned a non-nil Composition alongside an error: %+v", c)
	}
}

// TestParse_MalformedIndexOnEmptyClipSlotDoesNotRejectFile is review
// finding I's second bullet: parseRequiredInt used to run BEFORE the
// isEmptyInnerXML check, so a malformed index on a slot this package was
// always going to discard anyway rejected the entire file. Real
// compositions are mostly empty slots (226 of 252 measured in the bench
// capture), so this fixture's first <Clip> (empty, non-numeric layerIndex)
// must be silently skipped, and its second (non-empty, valid) must survive.
func TestParse_MalformedIndexOnEmptyClipSlotDoesNotRejectFile(t *testing.T) {
	c := mustParseTestdata(t, "malformed-index-empty-slot.avc")

	if len(c.Clips) != 1 {
		t.Fatalf("len(Clips) = %d, want 1 (the empty slot with the malformed index must be skipped, not reject the file)", len(c.Clips))
	}
	if c.Clips[0].ID != "6000000000702" || c.Clips[0].Name != "Survivor Clip" {
		t.Errorf("Clips[0] = %+v, want the surviving non-empty clip 6000000000702 (\"Survivor Clip\")", c.Clips[0])
	}
}

// TestParse_MalformedIndexOnNonEmptyClipStillRejects is the control for the
// fix above: a malformed index on a clip that is NOT an empty slot (this
// package's existing bad-layerindex.avc fixture, whose <Clip> carries real
// content) must still reject the whole parse. The reordering in transform()
// must not have accidentally made ErrMalformedIndex unreachable for a real,
// non-empty clip.
func TestParse_MalformedIndexOnNonEmptyClipStillRejects(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "bad-layerindex.avc"))
	if err != nil {
		t.Fatalf("reading testdata/bad-layerindex.avc: %v", err)
	}
	c, err := Parse(bytes.NewReader(data))
	if !errors.Is(err, ErrMalformedIndex) {
		t.Fatalf("Parse(bad-layerindex.avc) error = %v, want it to wrap ErrMalformedIndex", err)
	}
	if c != nil {
		t.Errorf("Parse(bad-layerindex.avc) returned a non-nil Composition alongside an error: %+v", c)
	}
}

// TestParseOptionalInt_NonNumericDegradesRatherThanErrors is review finding
// I's first bullet: parseOptionalInt's doc says a missing or blank canvas
// value degrades to 0 rather than rejecting the file, but before this fix a
// PRESENT, non-numeric value took a different path entirely — it aborted
// the whole parse with a raw, unwrapped strconv error matching none of this
// package's sentinels. This directly exercises parseOptionalInt (the unit
// the doc comment describes) rather than routing through a full composition
// fixture, so the assertion is exactly the function whose behavior and doc
// disagreed.
func TestParseOptionalInt_NonNumericDegradesRatherThanErrors(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"empty string", "", 0},
		{"valid integer", "1920", 1920},
		{"non-numeric", "not-a-number", 0},
		{"malformed decimal", "19.20", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseOptionalInt(tc.in); got != tc.want {
				t.Errorf("parseOptionalInt(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

// TestParse_CanvasWithNonNumericWidthDegradesRatherThanRejects is the
// full-file-level version of the parseOptionalInt unit test above: a
// composition whose CompositionInfo width is present but not numeric must
// still parse successfully, with Canvas.Width degraded to 0 — not reject
// the whole file the way a malformed clip or layer index does.
func TestParse_CanvasWithNonNumericWidthDegradesRatherThanRejects(t *testing.T) {
	const doc = `<?xml version="1.0" encoding="UTF-8"?>
<Composition name="Composition" uniqueId="1" numDecks="0" numLayers="0" numColumns="0">
  <versionInfo name="Resolume Arena" majorVersion="7" minorVersion="23" microVersion="2" revision="1"/>
  <CompositionInfo name="Bad Canvas" description="" width="not-a-number" height="1080"/>
</Composition>`
	c, err := Parse(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("Parse: unexpected error: %v", err)
	}
	if c.Canvas.Width != 0 {
		t.Errorf("Canvas.Width = %d, want 0 (degraded, not rejected)", c.Canvas.Width)
	}
	if c.Canvas.Height != 1080 {
		t.Errorf("Canvas.Height = %d, want 1080 (unaffected by the sibling attribute's malformed value)", c.Canvas.Height)
	}
}

// TestParse_NotXML_NoAngleBrackets covers the plain-text case
// specifically: input with no '<' at all must not be mistaken for
// well-formed XML content sitting outside a root element.
func TestParse_NotXML_NoAngleBrackets(t *testing.T) {
	_, err := Parse(strings.NewReader("just some text, no markup here"))
	if !errors.Is(err, ErrNotXML) {
		t.Errorf("Parse(plain text) error = %v, want ErrNotXML", err)
	}
}
