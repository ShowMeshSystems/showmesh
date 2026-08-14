package resolume

import (
	"encoding/json"
	"testing"
	"time"
)

func mustDecodeComposition(t *testing.T, data []byte) *Composition {
	t.Helper()
	var comp Composition
	if err := json.Unmarshal(data, &comp); err != nil {
		t.Fatalf("json.Unmarshal(Composition) error = %v", err)
	}
	return &comp
}

// --- Required test 5: json.Marshal of a ParameterID errors ------------------

// TestParameterIDMarshalJSONErrors is section 3.6's structural
// enforcement, tested directly: a bare ParameterID must refuse to
// marshal.
//
// Before trusting this test, ParameterID.MarshalJSON was temporarily
// changed to `return []byte(strconv.FormatInt(int64(p), 10)), nil` (the
// obvious, wrong implementation that just encodes the number) and this
// test was re-run: it failed, with json.Marshal succeeding and returning
// the raw parameter id as a JSON number. Reverted afterward.
func TestParameterIDMarshalJSONErrors(t *testing.T) {
	id := ParameterID(1786724946918)
	if _, err := json.Marshal(id); err == nil {
		t.Fatalf("json.Marshal(ParameterID) error = nil, want a non-nil error")
	}
}

// TestStructContainingParameterIDMarshalJSONErrors proves the enforcement
// survives nesting: a struct that merely CONTAINS a ParameterID field
// must also fail to marshal, since encoding/json calls the field's own
// MarshalJSON while encoding the containing struct. This is the case that
// actually matters — nobody marshals a bare ParameterID by itself; the
// realistic mistake is a struct with a ParameterID field accidentally
// reaching an API handler's json.NewEncoder.
func TestStructContainingParameterIDMarshalJSONErrors(t *testing.T) {
	type wrapper struct {
		ObjectID ObjectID    `json:"objectId"`
		ParamID  ParameterID `json:"paramId"`
	}
	w := wrapper{ObjectID: 1765396769079, ParamID: 1786724946918}
	if _, err := json.Marshal(w); err == nil {
		t.Fatalf("json.Marshal(struct containing ParameterID) error = nil, want a non-nil error")
	}
}

// TestObjectIDMarshalsNormally is the explicit non-regression check: only
// ParameterID is blocked. ObjectID — safe to hold and, per this package's
// doc comment, a later seam's decision whether to persist — must marshal
// like an ordinary integer.
func TestObjectIDMarshalsNormally(t *testing.T) {
	b, err := json.Marshal(ObjectID(1765396769079))
	if err != nil {
		t.Fatalf("json.Marshal(ObjectID) error = %v, want ObjectID to marshal normally", err)
	}
	if string(b) != "1765396769079" {
		t.Errorf("json.Marshal(ObjectID) = %s, want the bare integer", b)
	}
}

// ParameterID must still be UNMARSHALABLE — the restriction is on writing
// it, never on reading it off the wire; see this package's doc comment.
func TestParameterIDUnmarshalsNormally(t *testing.T) {
	var id ParameterID
	if err := json.Unmarshal([]byte("1786724946918"), &id); err != nil {
		t.Fatalf("json.Unmarshal(ParameterID) error = %v, want ParameterID to still be readable from JSON", err)
	}
	if id != 1786724946918 {
		t.Errorf("ParameterID = %d, want 1786724946918", id)
	}
}

// --- Required test 6: Resolve indexes exactly the closed set ---------------

// TestResolveIndexesExactlyTheClosedSet decodes composition_minimal.json
// and checks the parameter index against the exact closed set this
// package's doc comment enumerates: 3 (composition) + 4*2 (layers) + 2*1
// (layergroups) + 2*4 (clips, across the two layers' grids) + 2*1
// (columns) + 2*1 (decks) = 3 + 8 + 2 + 8 + 2 + 2 = 25 entries — and
// spot-checks that a syntactically plausible but NOT-indexed name (a
// layer's "solo", which composition_minimal.json's layer 1 deliberately
// carries in the wire data) is correctly absent from the index.
//
// Before trusting this test, resolve.go's Resolve was temporarily changed
// to also index each layer's "solo" (r.index(ObjectKindLayer, l.ID,
// "solo", ...) — solo has no ParamBoolean field on Layer today, so this
// was simulated by indexing composition.Bypassed's id under the "solo"
// key as a stand-in) and this test's solo-must-be-absent assertion was
// confirmed to fail. Reverted afterward.
func TestResolveIndexesExactlyTheClosedSet(t *testing.T) {
	comp := mustDecodeComposition(t, loadTestdata(t, "composition_minimal.json"))
	now := time.Now()

	res, err := Resolve(comp, now)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	wantCount := 3 + 2*4 + 1*2 + 4*2 + 2*1 + 2*1
	if got := len(res.params); got != wantCount {
		t.Errorf("len(indexed parameters) = %d, want %d", got, wantCount)
	}

	// Composition-level.
	for _, name := range []string{"master", "bypassed", "name"} {
		if _, ok := res.ParameterID(ObjectKindComposition, compositionObjectID, name); !ok {
			t.Errorf("composition parameter %q not indexed", name)
		}
	}

	// Every layer.
	for _, layerID := range res.LayerIDs() {
		for _, name := range []string{"master", "bypassed", "video.opacity", "transition.duration"} {
			if _, ok := res.ParameterID(ObjectKindLayer, layerID, name); !ok {
				t.Errorf("layer %v parameter %q not indexed", layerID, name)
			}
		}
	}

	// Every layer group.
	for _, groupID := range res.LayerGroupIDs() {
		for _, name := range []string{"master", "bypassed"} {
			if _, ok := res.ParameterID(ObjectKindLayerGroup, groupID, name); !ok {
				t.Errorf("layergroup %v parameter %q not indexed", groupID, name)
			}
		}
	}

	// Every clip.
	for _, clipID := range res.ClipIDs() {
		for _, name := range []string{"connected", "transporttype"} {
			if _, ok := res.ParameterID(ObjectKindClip, clipID, name); !ok {
				t.Errorf("clip %v parameter %q not indexed", clipID, name)
			}
		}
	}

	// Every column.
	for _, colID := range res.ColumnIDs() {
		if _, ok := res.ParameterID(ObjectKindColumn, colID, "connected"); !ok {
			t.Errorf("column %v parameter \"connected\" not indexed", colID)
		}
	}

	// Every deck.
	for _, deckID := range res.DeckIDs() {
		if _, ok := res.ParameterID(ObjectKindDeck, deckID, "selected"); !ok {
			t.Errorf("deck %v parameter \"selected\" not indexed", deckID)
		}
	}

	// Nothing outside the closed set, even for a name that plausibly
	// exists on the wire (composition_minimal.json's first layer carries
	// a "solo" ParamBoolean specifically so this negative check has
	// something real to fail against).
	firstLayer := res.LayerIDs()[0]
	if _, ok := res.ParameterID(ObjectKindLayer, firstLayer, "solo"); ok {
		t.Errorf("layer %v \"solo\" IS indexed; the parameter index must be the closed set only", firstLayer)
	}
	if _, ok := res.ParameterID(ObjectKindComposition, compositionObjectID, "video.opacity"); ok {
		t.Errorf("composition \"video.opacity\" IS indexed; composition only indexes master/bypassed/name")
	}
	if _, ok := res.ParameterID(ObjectKindDeck, res.DeckIDs()[0], "closed"); ok {
		t.Errorf("deck \"closed\" IS indexed; deck only indexes \"selected\"")
	}
}

// TestResolveObjectCountsAndSelectedDeck is a structural sanity check on
// the same fixture: object counts by kind, and which deck Resolve
// determined was selected.
func TestResolveObjectCountsAndSelectedDeck(t *testing.T) {
	comp := mustDecodeComposition(t, loadTestdata(t, "composition_minimal.json"))
	res, err := Resolve(comp, time.Now())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if got := len(res.DeckIDs()); got != 2 {
		t.Errorf("len(DeckIDs()) = %d, want 2", got)
	}
	if got := len(res.ColumnIDs()); got != 2 {
		t.Errorf("len(ColumnIDs()) = %d, want 2", got)
	}
	if got := len(res.LayerIDs()); got != 2 {
		t.Errorf("len(LayerIDs()) = %d, want 2", got)
	}
	if got := len(res.LayerGroupIDs()); got != 1 {
		t.Errorf("len(LayerGroupIDs()) = %d, want 1", got)
	}
	if got := len(res.ClipIDs()); got != 4 {
		t.Errorf("len(ClipIDs()) = %d, want 4 (2 layers x 2 clips)", got)
	}

	if res.SelectedDeckID != 1733100600915 {
		t.Errorf("SelectedDeckID = %v, want 1733100600915 (\"Main\", the only deck with selected=true)", res.SelectedDeckID)
	}
	if res.SelectedDeckName != "Main" {
		t.Errorf("SelectedDeckName = %q, want %q", res.SelectedDeckName, "Main")
	}

	scope := res.ClipScope()
	if scope.DeckID != res.SelectedDeckID || scope.DeckName != res.SelectedDeckName {
		t.Errorf("ClipScope() = %+v, want it to match the selected deck exactly", scope)
	}
}

// TestResolveRejectsNoDeckSelected: if no deck reports selected=true,
// Resolve must fail rather than silently leaving SelectedDeckID at its
// zero value (which would collide with compositionObjectID's meaning of
// "no real object").
func TestResolveRejectsNoDeckSelected(t *testing.T) {
	raw := `{
		"name": {"id":1,"value":"x"},
		"bypassed": {"id":2,"value":false},
		"master": {"id":3,"value":1.0},
		"decks": [
			{"id": 100, "closed": false, "name": {"id":4,"value":"A"}, "selected": {"id":5,"value":false}}
		]
	}`
	comp := mustDecodeComposition(t, []byte(raw))
	if _, err := Resolve(comp, time.Now()); err == nil {
		t.Fatalf("Resolve() error = nil, want an error when no deck reports selected=true")
	}
}

func TestResolveRejectsNilComposition(t *testing.T) {
	if _, err := Resolve(nil, time.Now()); err == nil {
		t.Fatalf("Resolve(nil) error = nil, want an error")
	}
}

// --- Required test 7: fingerprints isolate parameter-id churn --------------

// TestResolveFingerprintsIsolateParameterIDChurn is the direct
// reproduction of capture section 3.2's restart measurement: two
// Resolve() calls over compositions with IDENTICAL object ids but
// DIFFERENT parameter ids must produce the SAME ObjectFingerprint and a
// DIFFERENT ParameterFingerprint.
//
// Before trusting this test, ObjectFingerprint was temporarily changed to
// hash r.params's values (parameter ids) instead of the object-id slices,
// and this test's "same ObjectFingerprint" assertion was confirmed to
// fail. Reverted afterward.
func TestResolveFingerprintsIsolateParameterIDChurn(t *testing.T) {
	before := mustDecodeComposition(t, loadTestdata(t, "composition_minimal.json"))
	after := mustDecodeComposition(t, loadTestdata(t, "composition_minimal_restarted.json"))

	resBefore, err := Resolve(before, time.Now())
	if err != nil {
		t.Fatalf("Resolve(before) error = %v", err)
	}
	resAfter, err := Resolve(after, time.Now())
	if err != nil {
		t.Fatalf("Resolve(after) error = %v", err)
	}

	if resBefore.ObjectFingerprint() != resAfter.ObjectFingerprint() {
		t.Errorf("ObjectFingerprint changed across simulated restart (before=%s after=%s), want it unchanged: object ids are identical in both fixtures",
			resBefore.ObjectFingerprint(), resAfter.ObjectFingerprint())
	}
	if resBefore.ParameterFingerprint() == resAfter.ParameterFingerprint() {
		t.Errorf("ParameterFingerprint did NOT change across simulated restart (both=%s), want it to differ: every parameter id in the \"after\" fixture was shifted",
			resBefore.ParameterFingerprint())
	}
}

// TestFingerprintsAreShortHex is a basic shape check: both fingerprints
// are non-empty 8-character hex strings, per this package's doc comment
// ("first 8 hex chars of a SHA-256").
func TestFingerprintsAreShortHex(t *testing.T) {
	comp := mustDecodeComposition(t, loadTestdata(t, "composition_minimal.json"))
	res, err := Resolve(comp, time.Now())
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	for name, fp := range map[string]string{
		"ObjectFingerprint":             res.ObjectFingerprint(),
		"ParameterFingerprint":          res.ParameterFingerprint(),
		"SelectedDeckObjectFingerprint": res.SelectedDeckObjectFingerprint(),
	} {
		if len(fp) != 8 {
			t.Errorf("%s = %q, want an 8-character string", name, fp)
		}
		for _, r := range fp {
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
				t.Errorf("%s = %q, want lowercase hex only", name, fp)
				break
			}
		}
	}
}

// --- Review finding B: ObjectFingerprint must be deck-independent ----------

// compositionMainDeckSelected and compositionRestStagingDeckSelected are
// hand-built to reproduce capture section 9.4's measured deck race, small
// enough to read in one sitting rather than a third testdata fixture file:
// identical layer, layer group, and deck object ids in both (deck
// identity is deck-independent per the capture), but a DIFFERENT clip and
// column id set in each — modelling GET /composition's own behavior of
// returning only the selected deck's clip/column grid. Parameter ids also
// differ between the two on purpose, so a test asserting "only the
// SELECTED-DECK fingerprint differs" has something to fail against if
// ObjectFingerprint ever regresses to folding parameter ids in too.
const compositionMainDeckSelected = `{
	"name": {"id": 7001, "value": "Christmas 25"},
	"bypassed": {"id": 7002, "value": false},
	"master": {"id": 7003, "value": 1.0},
	"decks": [
		{"id": 1733100600915, "closed": false, "name": {"id": 7010, "value": "Main"}, "selected": {"id": 7011, "value": true}},
		{"id": 1733100600921, "closed": false, "name": {"id": 7012, "value": "Rest Staging"}, "selected": {"id": 7013, "value": false}}
	],
	"columns": [
		{"id": 1765224900001, "connected": {"id": 7020, "value": "Disconnected", "options": ["Empty", "Disconnected", "Connected"]}},
		{"id": 1765224900002, "connected": {"id": 7021, "value": "Connected", "options": ["Empty", "Disconnected", "Connected"]}}
	],
	"layergroups": [
		{"id": 1765224910001, "name": {"id": 7030, "value": "Group 1"}, "bypassed": {"id": 7031, "value": false}, "master": {"id": 7032, "value": 1.0}, "layers": [{"id": 1765224917300}]}
	],
	"layers": [
		{
			"id": 1765224917300,
			"bypassed": {"id": 7040, "value": false},
			"master": {"id": 7041, "value": 1.0},
			"video": {"opacity": {"id": 7042, "value": 1.0}},
			"transition": {"duration": {"id": 7043, "value": 2.5}},
			"clips": [
				{"id": 1765396769079, "connected": {"id": 7050, "value": "Connected", "options": ["Empty", "Disconnected", "Previewing", "Connected", "Connected & previewing"]}, "transporttype": {"id": 7051, "value": "Timeline", "options": ["Timeline"]}}
			]
		}
	]
}`

const compositionRestStagingDeckSelected = `{
	"name": {"id": 7101, "value": "Christmas 25"},
	"bypassed": {"id": 7102, "value": false},
	"master": {"id": 7103, "value": 1.0},
	"decks": [
		{"id": 1733100600915, "closed": false, "name": {"id": 7110, "value": "Main"}, "selected": {"id": 7111, "value": false}},
		{"id": 1733100600921, "closed": false, "name": {"id": 7112, "value": "Rest Staging"}, "selected": {"id": 7113, "value": true}}
	],
	"columns": [
		{"id": 1765224917471, "connected": {"id": 7120, "value": "Disconnected", "options": ["Empty", "Disconnected", "Connected"]}}
	],
	"layergroups": [
		{"id": 1765224910001, "name": {"id": 7130, "value": "Group 1"}, "bypassed": {"id": 7131, "value": false}, "master": {"id": 7132, "value": 1.0}, "layers": [{"id": 1765224917300}]}
	],
	"layers": [
		{
			"id": 1765224917300,
			"bypassed": {"id": 7140, "value": false},
			"master": {"id": 7141, "value": 1.0},
			"video": {"opacity": {"id": 7142, "value": 1.0}},
			"transition": {"duration": {"id": 7143, "value": 2.5}},
			"clips": [
				{"id": 1765224917471, "connected": {"id": 7150, "value": "Disconnected", "options": ["Empty", "Disconnected", "Previewing", "Connected", "Connected & previewing"]}, "transporttype": {"id": 7151, "value": "Timeline", "options": ["Timeline"]}}
			]
		}
	]
}`

// TestObjectFingerprintIsDeckIndependentAndSelectedDeckFingerprintIsNot is
// the direct reproduction of review finding B (2026-08-14): the SAME
// composition, differing only in which deck is selected, must produce the
// SAME ObjectFingerprint (layers/layergroups/decks are deck-independent,
// capture section 9.4) and a DIFFERENT SelectedDeckObjectFingerprint
// (clips/columns are scoped to whichever deck GET /composition returned).
//
// Before trusting this test, ObjectFingerprint was temporarily reverted to
// its pre-correction form (hashing r.allObjectIDs(), which folded clipIDs
// and columnIDs in alongside layers/layergroups/decks) and this test was
// re-run: the "same ObjectFingerprint" assertion failed, exactly
// reproducing the reviewer's live 9aae306b / d1e71589 finding against the
// operator's own composition. Reverted afterward.
func TestObjectFingerprintIsDeckIndependentAndSelectedDeckFingerprintIsNot(t *testing.T) {
	mainComp := mustDecodeComposition(t, []byte(compositionMainDeckSelected))
	restComp := mustDecodeComposition(t, []byte(compositionRestStagingDeckSelected))

	mainRes, err := Resolve(mainComp, time.Now())
	if err != nil {
		t.Fatalf("Resolve(main) error = %v", err)
	}
	restRes, err := Resolve(restComp, time.Now())
	if err != nil {
		t.Fatalf("Resolve(restStaging) error = %v", err)
	}

	if mainRes.ObjectFingerprint() != restRes.ObjectFingerprint() {
		t.Errorf("ObjectFingerprint differs across a deck selection change alone (main=%s, restStaging=%s), want it identical: layers/layergroups/decks are deck-independent",
			mainRes.ObjectFingerprint(), restRes.ObjectFingerprint())
	}
	if mainRes.SelectedDeckObjectFingerprint() == restRes.SelectedDeckObjectFingerprint() {
		t.Errorf("SelectedDeckObjectFingerprint did NOT change across a deck selection change (both=%s), want it to differ: the two decks carry disjoint clip/column ids",
			mainRes.SelectedDeckObjectFingerprint())
	}
}

// --- Review finding C: a null or absent parameter id must not be indexed ---

// presenceCompositionAbsent, presenceCompositionNull, and
// presenceCompositionPresent are the same tiny composition shape, varied
// only in how five representative parameters — one of each envelope type
// resolve.go's closed index actually uses (ParamRange, ParamBoolean,
// ParamString, ParamState, ParamChoice) — are encoded: key missing
// entirely, key present with JSON null, and key present with a real
// value. A deck with selected=true is present and unaffected in every
// variant so Resolve itself always succeeds; only the five parameters
// under test vary.
const presenceCompositionAbsent = `{
	"decks": [{"id": 900, "closed": false, "name": {"id": 9100, "value": "Main"}, "selected": {"id": 9101, "value": true}}],
	"layers": [
		{
			"id": 910,
			"master": {"id": 9110, "value": 1.0},
			"video": {"opacity": {"id": 9111, "value": 1.0}},
			"transition": {"duration": {"id": 9112, "value": 0.0}},
			"clips": [{"id": 920}]
		}
	]
}`

const presenceCompositionNull = `{
	"name": null,
	"bypassed": null,
	"master": null,
	"decks": [{"id": 900, "closed": false, "name": {"id": 9210, "value": "Main"}, "selected": {"id": 9211, "value": true}}],
	"layers": [
		{
			"id": 910,
			"bypassed": {"id": 9220, "value": false},
			"master": {"id": 9221, "value": 1.0},
			"video": {"opacity": {"id": 9222, "value": 1.0}},
			"transition": {"duration": {"id": 9223, "value": 0.0}},
			"clips": [{"id": 920, "connected": null, "transporttype": null}]
		}
	]
}`

const presenceCompositionPresent = `{
	"name": {"id": 9301, "value": "Test"},
	"bypassed": {"id": 9302, "value": false},
	"master": {"id": 9303, "value": 1.0},
	"decks": [{"id": 900, "closed": false, "name": {"id": 9310, "value": "Main"}, "selected": {"id": 9311, "value": true}}],
	"layers": [
		{
			"id": 910,
			"bypassed": {"id": 9320, "value": false},
			"master": {"id": 9321, "value": 1.0},
			"video": {"opacity": {"id": 9322, "value": 1.0}},
			"transition": {"duration": {"id": 9323, "value": 0.0}},
			"clips": [
				{
					"id": 920,
					"connected": {"id": 9330, "value": "Connected", "index": 3, "options": ["Empty", "Disconnected", "Previewing", "Connected", "Connected & previewing"]},
					"transporttype": {"id": 9331, "value": "Timeline", "options": ["Timeline"]}
				}
			]
		}
	]
}`

// TestResolveDoesNotIndexNullOrAbsentParameterIDs is the direct
// reproduction of review finding C (2026-08-14): a parameter envelope that
// is absent, or present with JSON null, must leave [Resolution.ParameterID]
// answering ok=false — never a fabricated ParameterID(0) reported as
// resolved — for at least one parameter of each envelope type this
// package's closed index uses (ParamRange: composition.master;
// ParamBoolean: composition.bypassed; ParamString: composition.name;
// ParamState: clip.connected; ParamChoice: clip.transporttype).
//
// Before trusting this test, index()'s `if id == 0 { return }` guard was
// temporarily removed and this test was re-run: every absent/null case
// failed, with ParameterID reporting ok=true and a fabricated
// ParameterID(0) — the exact "ma": null shape CLAUDE.md names, reproduced
// as a resolved id instead of a resolved reading. Restored afterward.
func TestResolveDoesNotIndexNullOrAbsentParameterIDs(t *testing.T) {
	clipID := ObjectID(920)

	tests := []struct {
		name   string
		raw    string
		wantOK bool
	}{
		{"absent", presenceCompositionAbsent, false},
		{"null", presenceCompositionNull, false},
		{"present", presenceCompositionPresent, true},
	}

	type lookup struct {
		label string
		kind  ObjectKind
		obj   ObjectID
		field string
	}
	lookups := []lookup{
		{"composition.master (ParamRange)", ObjectKindComposition, compositionObjectID, "master"},
		{"composition.bypassed (ParamBoolean)", ObjectKindComposition, compositionObjectID, "bypassed"},
		{"composition.name (ParamString)", ObjectKindComposition, compositionObjectID, "name"},
		{"clip.connected (ParamState)", ObjectKindClip, clipID, "connected"},
		{"clip.transporttype (ParamChoice)", ObjectKindClip, clipID, "transporttype"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comp := mustDecodeComposition(t, []byte(tt.raw))
			res, err := Resolve(comp, time.Now())
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			for _, l := range lookups {
				id, ok := res.ParameterID(l.kind, l.obj, l.field)
				if ok != tt.wantOK {
					t.Errorf("%s: ParameterID(%s) ok = %v (id=%v), want %v", tt.name, l.label, ok, id, tt.wantOK)
				}
				if ok && id == 0 {
					t.Errorf("%s: ParameterID(%s) = 0 with ok=true, want a real non-zero id or ok=false", tt.name, l.label)
				}
			}
		})
	}
}
