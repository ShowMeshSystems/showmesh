package resolume

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/resolumecomp"
)

// This file is Track D seam D-2/B's own test suite: [BuildTrackedComposition]
// and [CompositionStore] as pure, non-networked transforms of an already-
// parsed [resolumecomp.Composition]. It reuses pkg/resolumecomp's own
// synthetic testdata/complete.avc fixture (via [parseTestComposition])
// rather than adding a new large fixture — see that package's own
// testdata/README.md for what complete.avc contains and why every value in
// it is a fabricated placeholder, never real operator content.

// parseTestComposition parses pkg/resolumecomp's synthetic
// testdata/complete.avc fixture, failing the test immediately if it cannot
// be parsed — a parse failure here means this package's own test fixture
// dependency broke, not something any test body below is trying to
// exercise.
func parseTestComposition(t *testing.T) *resolumecomp.Composition {
	t.Helper()
	f, err := os.Open("../../../../pkg/resolumecomp/testdata/complete.avc")
	if err != nil {
		t.Fatalf("opening testdata fixture: %v", err)
	}
	defer func() { _ = f.Close() }()

	comp, err := resolumecomp.Parse(f)
	if err != nil {
		t.Fatalf("parsing testdata fixture: %v", err)
	}
	return comp
}

// --- BuildTrackedComposition ------------------------------------------------

func TestBuildTrackedCompositionCountsAndWrittenBy(t *testing.T) {
	comp := parseTestComposition(t)

	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	if got, want := len(tc.Layers()), 3; got != want {
		t.Errorf("Layers() len = %d, want %d", got, want)
	}
	if got, want := len(tc.LayerGroups()), 2; got != want {
		t.Errorf("LayerGroups() len = %d, want %d", got, want)
	}
	if got, want := len(tc.Decks()), 2; got != want {
		t.Errorf("Decks() len = %d, want %d", got, want)
	}
	// complete.avc: Deck One has two non-empty clips (6000000000001,
	// 6000000000003) and one deliberately empty slot (6000000000002,
	// excluded by pkg/resolumecomp itself); Deck Two has one
	// (6000000000101). 2 + 1 = 3 total deck clips.
	if got, want := len(tc.Clips()), 3; got != want {
		t.Errorf("Clips() len = %d, want %d", got, want)
	}
	if got, want := len(tc.PersistentClips()), 2; got != want {
		t.Errorf("PersistentClips() len = %d, want %d", got, want)
	}

	if got, want := tc.Name(), "Holiday Test Show"; got != want {
		t.Errorf("Name() = %q, want %q", got, want)
	}

	wb := tc.WrittenBy()
	if wb.Product != "Resolume Arena" || wb.Major != 7 || wb.Minor != 23 || wb.Micro != 2 || wb.Revision != 51094 {
		t.Errorf("WrittenBy() = %+v, want Resolume Arena 7.23.2 r51094", wb)
	}
}

// TestBuildTrackedCompositionClipsCarryTheirDeck is ADR-032 decision 6's
// own acceptance criterion for this seam: every entry in
// [TrackedComposition.Clips] must carry the deck it belongs to, because a
// 404 on a stored clip id cannot be told apart from a deck mismatch
// without it.
func TestBuildTrackedCompositionClipsCarryTheirDeck(t *testing.T) {
	comp := parseTestComposition(t)
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	deckOne := ObjectID(2000000000001)
	deckTwo := ObjectID(2000000000002)

	byID := make(map[ObjectID]TrackedClip, len(tc.Clips()))
	for _, c := range tc.Clips() {
		byID[c.ID] = c
	}

	snowflakes, ok := byID[6000000000001]
	if !ok {
		t.Fatalf("clip 6000000000001 not found in Clips()")
	}
	if snowflakes.DeckID != deckOne {
		t.Errorf("clip 6000000000001 DeckID = %v, want %v (Deck One)", snowflakes.DeckID, deckOne)
	}
	if snowflakes.Name != "Snowflakes" {
		t.Errorf("clip 6000000000001 Name = %q, want %q", snowflakes.Name, "Snowflakes")
	}

	clipC, ok := byID[6000000000101]
	if !ok {
		t.Fatalf("clip 6000000000101 not found in Clips()")
	}
	if clipC.DeckID != deckTwo {
		t.Errorf("clip 6000000000101 DeckID = %v, want %v (Deck Two)", clipC.DeckID, deckTwo)
	}

	// The empty slot (6000000000002) must never appear at all.
	if _, ok := byID[6000000000002]; ok {
		t.Errorf("empty clip slot 6000000000002 must not appear in Clips()")
	}
}

// TestBuildTrackedCompositionPersistentClipsHaveNoDeckField is ADR-032
// decision 6's "modelled separately and explicitly" requirement, checked
// the way this package's own doc comments say it should be checked:
// structurally. [TrackedPersistentClip] simply has no DeckID field —
// this test would fail to COMPILE, not fail at runtime, if that ever
// regressed, which is the point: it is included here as an explicit,
// named test rather than left implicit so a future reader sees the
// property is deliberately tested, not merely true by accident.
func TestBuildTrackedCompositionPersistentClipsHaveNoDeckField(t *testing.T) {
	comp := parseTestComposition(t)
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	byID := make(map[ObjectID]TrackedPersistentClip, len(tc.PersistentClips()))
	for _, c := range tc.PersistentClips() {
		byID[c.ID] = c
	}

	a, ok := byID[7000000000001]
	if !ok {
		t.Fatalf("persistent clip 7000000000001 not found")
	}
	if a.Name != "Persistent A" {
		t.Errorf("persistent clip 7000000000001 Name = %q, want %q", a.Name, "Persistent A")
	}

	b, ok := byID[7000000000002]
	if !ok {
		t.Fatalf("persistent clip 7000000000002 not found")
	}
	if b.Name != "Persistent B" {
		t.Errorf("persistent clip 7000000000002 Name = %q, want %q", b.Name, "Persistent B")
	}
}

// TestBuildTrackedCompositionLayerGroupMembership checks
// [TrackedLayer.LayerGroupID]'s resolution: complete.avc's first layer
// (index 0) has layerGroup="0" (the first <Group>), the second (index 1)
// has layerGroup="1" (the second <Group>), and the third has no layerGroup
// attribute at all.
func TestBuildTrackedCompositionLayerGroupMembership(t *testing.T) {
	comp := parseTestComposition(t)
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	layers := tc.Layers()
	if len(layers) != 3 {
		t.Fatalf("Layers() len = %d, want 3", len(layers))
	}

	groupIDs := make([]ObjectID, len(tc.LayerGroups()))
	for i, g := range tc.LayerGroups() {
		groupIDs[i] = g.ID
	}

	if layers[0].LayerGroupID == nil || *layers[0].LayerGroupID != groupIDs[0] {
		t.Errorf("layers[0].LayerGroupID = %v, want %v", layers[0].LayerGroupID, groupIDs[0])
	}
	if layers[1].LayerGroupID == nil || *layers[1].LayerGroupID != groupIDs[1] {
		t.Errorf("layers[1].LayerGroupID = %v, want %v", layers[1].LayerGroupID, groupIDs[1])
	}
	if layers[2].LayerGroupID != nil {
		t.Errorf("layers[2].LayerGroupID = %v, want nil (no layerGroup attribute in the file)", layers[2].LayerGroupID)
	}
}

// TestBuildTrackedCompositionLayerName is ADR-037 decision 7's own claim
// carried through to the bridge type: complete.avc's first layer is named
// "Peak Only" via its own Params block, its second layer has no Params
// block at all, and [TrackedLayer.Name] must reflect exactly that — not
// drop the name, and not invent one for the unnamed layer.
func TestBuildTrackedCompositionLayerName(t *testing.T) {
	comp := parseTestComposition(t)
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	layers := tc.Layers()
	if len(layers) != 3 {
		t.Fatalf("Layers() len = %d, want 3", len(layers))
	}
	if layers[0].Name != "Peak Only" {
		t.Errorf("layers[0].Name = %q, want %q", layers[0].Name, "Peak Only")
	}
	if layers[1].Name != "" {
		t.Errorf("layers[1].Name = %q, want \"\" (no Params block in the fixture)", layers[1].Name)
	}
}

func TestBuildTrackedCompositionNilComposition(t *testing.T) {
	tc, err := BuildTrackedComposition(nil)
	if err == nil {
		t.Fatalf("BuildTrackedComposition(nil) succeeded, want an error")
	}
	if tc != nil {
		t.Errorf("BuildTrackedComposition(nil) returned a non-nil *TrackedComposition alongside an error")
	}
}

// TestBuildTrackedCompositionInvalidObjectID exercises this package's own
// ErrInvalidObjectID path — a shape pkg/resolumecomp's own parser cannot
// produce from a real file (it never validates uniqueId as numeric), but
// this package's own contract is that it does not know that, and refuses
// to build a tracked-object set it could not later address by id.
func TestBuildTrackedCompositionInvalidObjectID(t *testing.T) {
	comp := &resolumecomp.Composition{
		Name: "Bad Ids",
		Layers: []resolumecomp.Layer{
			{ID: "not-a-number", Index: 0},
		},
	}

	tc, err := BuildTrackedComposition(comp)
	if err == nil {
		t.Fatalf("BuildTrackedComposition with a non-numeric layer id succeeded, want an error")
	}
	if !errors.Is(err, ErrInvalidObjectID) {
		t.Errorf("error = %v, want it to wrap ErrInvalidObjectID", err)
	}
	if tc != nil {
		t.Errorf("BuildTrackedComposition returned a non-nil *TrackedComposition alongside an error (ADR-032 decision 7: a rejected build changes nothing)")
	}
}

// --- IdentitySample ----------------------------------------------------------

func TestIdentitySampleDrawsFromSelectedDeckPlusAllPersistent(t *testing.T) {
	comp := parseTestComposition(t)
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	deckOne := ObjectID(2000000000001) // 2 non-empty clips
	deckTwo := ObjectID(2000000000002) // 1 non-empty clip

	sampleOne := tc.IdentitySample(deckOne)
	if got, want := len(sampleOne.DeckClips), 2; got != want {
		t.Errorf("IdentitySample(deckOne).DeckClips len = %d, want %d", got, want)
	}
	if got, want := len(sampleOne.PersistentClips), 2; got != want {
		t.Errorf("IdentitySample(deckOne).PersistentClips len = %d, want %d", got, want)
	}
	if sampleOne.SelectedDeck != deckOne {
		t.Errorf("IdentitySample(deckOne).SelectedDeck = %v, want %v", sampleOne.SelectedDeck, deckOne)
	}
	for _, c := range sampleOne.DeckClips {
		found := false
		for _, tracked := range tc.ClipsOnDeck(deckOne) {
			if tracked.ID == c.ID {
				found = true
			}
		}
		if !found {
			t.Errorf("IdentitySample(deckOne).DeckClips contains id %v which is not on deckOne", c.ID)
		}
	}

	sampleTwo := tc.IdentitySample(deckTwo)
	if got, want := len(sampleTwo.DeckClips), 1; got != want {
		t.Errorf("IdentitySample(deckTwo).DeckClips len = %d, want %d", got, want)
	}
	if got, want := len(sampleTwo.PersistentClips), 2; got != want {
		t.Errorf("IdentitySample(deckTwo).PersistentClips len = %d, want %d", got, want)
	}
}

// TestIdentitySampleUnknownDeckYieldsNoDeckClips is ADR-032 decision 6's
// deck-mismatch case at the sample-building layer: a deck this
// composition holds no clips for produces zero DeckClips, never an error
// — interpreting that as a positive or negative identity signal is
// D-2/C's job (TRACK-D-D2-SPEC.md §6's "unknown" rung), not this method's.
func TestIdentitySampleUnknownDeckYieldsNoDeckClips(t *testing.T) {
	comp := parseTestComposition(t)
	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}

	sample := tc.IdentitySample(ObjectID(999999999999))
	if len(sample.DeckClips) != 0 {
		t.Errorf("IdentitySample(unknown deck).DeckClips = %v, want empty", sample.DeckClips)
	}
	if len(sample.PersistentClips) != 2 {
		t.Errorf("IdentitySample(unknown deck).PersistentClips len = %d, want 2 (always included)", len(sample.PersistentClips))
	}
}

// TestIdentitySampleCapsAtEight is TRACK-D-D2-SPEC.md §6's own cap,
// checked against a synthetic 10-clip deck built directly in Go (not a new
// XML fixture — resolumecomp.Composition is a plain struct and this edge
// case has nothing to do with .avc parsing, only with this package's own
// sampling logic).
func TestIdentitySampleCapsAtEight(t *testing.T) {
	const deckID = "2000000000001"
	comp := &resolumecomp.Composition{
		Name: "Ten Clips",
		Decks: []resolumecomp.Deck{
			{ID: deckID, Name: "Only Deck"},
		},
	}
	for i := 0; i < 10; i++ {
		comp.Clips = append(comp.Clips, resolumecomp.Clip{
			ID:     strconv.FormatInt(6000000000000+int64(i), 10),
			DeckID: deckID,
			Name:   "Clip",
		})
	}

	tc, err := BuildTrackedComposition(comp)
	if err != nil {
		t.Fatalf("BuildTrackedComposition: %v", err)
	}
	if got, want := len(tc.Clips()), 10; got != want {
		t.Fatalf("Clips() len = %d, want %d", got, want)
	}

	sample := tc.IdentitySample(ObjectID(2000000000001))
	if got, want := len(sample.DeckClips), identitySampleMaxDeckClips; got != want {
		t.Errorf("IdentitySample DeckClips len = %d, want %d (the §6 cap)", got, want)
	}
}

// --- CompositionStore: "not uploaded" as a distinguishable state -----------

func TestCompositionStoreZeroValueIsNotUploaded(t *testing.T) {
	var s CompositionStore

	tc, err := s.Current()
	if tc != nil {
		t.Errorf("Current() on a zero-value CompositionStore returned a non-nil *TrackedComposition")
	}
	if !errors.Is(err, ErrCompositionNotUploaded) {
		t.Errorf("Current() error = %v, want it to wrap ErrCompositionNotUploaded", err)
	}
	if got := s.LoadedRevision(); got != 0 {
		t.Errorf("LoadedRevision() = %d, want 0", got)
	}
}

// fakeCompositionConfigReader is a hand-rolled [CompositionConfigReader]
// for testing [CompositionStore.Refresh] without any store package or
// network dependency — exactly the kind of pure unit this method's own
// interface-at-the-consumer design exists to make possible.
type fakeCompositionConfigReader struct {
	mu           sync.Mutex
	revision     int64
	compJSON     []byte
	ok           bool
	err          error
	refreshCalls int
}

func (f *fakeCompositionConfigReader) CurrentCompositionRevision(context.Context) (int64, []byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshCalls++
	return f.revision, f.compJSON, f.ok, f.err
}

func (f *fakeCompositionConfigReader) setRevision(t *testing.T, revision int64, comp *resolumecomp.Composition) {
	t.Helper()
	raw, err := json.Marshal(comp)
	if err != nil {
		t.Fatalf("marshaling test composition: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revision = revision
	f.compJSON = raw
	f.ok = true
	f.err = nil
}

func (f *fakeCompositionConfigReader) setNotUploaded() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.revision = 0
	f.compJSON = nil
	f.ok = false
	f.err = nil
}

func TestCompositionStoreRefreshNotUploadedStaysNotUploaded(t *testing.T) {
	var s CompositionStore
	reader := &fakeCompositionConfigReader{}
	reader.setNotUploaded()

	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	_, err := s.Current()
	if !errors.Is(err, ErrCompositionNotUploaded) {
		t.Errorf("Current() error = %v, want ErrCompositionNotUploaded", err)
	}
}

func TestCompositionStoreRefreshLoadsAndSwaps(t *testing.T) {
	comp := parseTestComposition(t)

	var s CompositionStore
	reader := &fakeCompositionConfigReader{}
	reader.setRevision(t, 1, comp)

	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	tc, err := s.Current()
	if err != nil {
		t.Fatalf("Current() after Refresh: %v", err)
	}
	if len(tc.Layers()) != 3 {
		t.Errorf("Current().Layers() len = %d, want 3", len(tc.Layers()))
	}
	if got, want := s.LoadedRevision(), int64(1); got != want {
		t.Errorf("LoadedRevision() = %d, want %d", got, want)
	}

	// A second Refresh at the SAME revision must not rebuild. Checked by
	// pointer identity, not merely by re-reading Layers() again: a version
	// of Refresh that always decoded and rebuilt regardless of the
	// revision check would still pass every assertion above (the rebuilt
	// value is equal in content), so the claim this comment makes —
	// "does not rebuild" — has to be checked on the *TrackedComposition
	// pointer itself, which only stays identical if [CompositionStore.set]
	// was never called a second time. Confirmed by deleting the
	// `revision == s.revision.Load()` short-circuit in idmap.go while
	// developing this test: with it removed, this exact assertion failed
	// (a new pointer, unequal to the first) while every earlier assertion
	// in this test still passed — i.e. this line is the one that actually
	// detects the regression its neighbors do not.
	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}
	tc2, err := s.Current()
	if err != nil {
		t.Fatalf("Current() after second Refresh: %v", err)
	}
	if tc2 != tc {
		t.Errorf("Current() returned a different *TrackedComposition after a Refresh at an unchanged revision; Refresh must not rebuild when the revision has not moved")
	}
	if got, want := s.LoadedRevision(), int64(1); got != want {
		t.Errorf("LoadedRevision() after unchanged Refresh = %d, want %d", got, want)
	}

	// Now advance to revision 2 with a DIFFERENT composition (fewer
	// layers) and confirm the swap actually took effect.
	smaller := &resolumecomp.Composition{Name: "Smaller Show"}
	reader.setRevision(t, 2, smaller)
	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh to revision 2: %v", err)
	}
	tc3, err := s.Current()
	if err != nil {
		t.Fatalf("Current() after Refresh to revision 2: %v", err)
	}
	if len(tc3.Layers()) != 0 {
		t.Errorf("Current().Layers() len after revision 2 = %d, want 0", len(tc3.Layers()))
	}
	if tc3 == tc2 {
		t.Errorf("Current() returned the SAME *TrackedComposition after Refresh moved to a genuinely new revision; the swap did not take effect")
	}
	if got, want := s.LoadedRevision(), int64(2); got != want {
		t.Errorf("LoadedRevision() = %d, want %d", got, want)
	}
}

func TestCompositionStoreRefreshCanRevertToNotUploaded(t *testing.T) {
	comp := parseTestComposition(t)

	var s CompositionStore
	reader := &fakeCompositionConfigReader{}
	reader.setRevision(t, 1, comp)
	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if _, err := s.Current(); err != nil {
		t.Fatalf("Current() after load: %v", err)
	}

	reader.setNotUploaded()
	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("Refresh back to not-uploaded: %v", err)
	}

	_, err := s.Current()
	if !errors.Is(err, ErrCompositionNotUploaded) {
		t.Errorf("Current() error = %v, want ErrCompositionNotUploaded", err)
	}
	if got := s.LoadedRevision(); got != 0 {
		t.Errorf("LoadedRevision() = %d, want 0", got)
	}
}

func TestCompositionStoreRefreshReaderError(t *testing.T) {
	var s CompositionStore
	reader := &fakeCompositionConfigReader{err: errors.New("boom")}

	if err := s.Refresh(context.Background(), reader); err == nil {
		t.Fatalf("Refresh with a failing reader succeeded, want an error")
	}

	// A failed Refresh must not clobber whatever state was already there
	// — here, still "not uploaded" — exactly the same "a rejected write
	// changes nothing" principle ADR-032 decision 7 states for the upload
	// handler itself, applied to this read-refresh path.
	_, err := s.Current()
	if !errors.Is(err, ErrCompositionNotUploaded) {
		t.Errorf("Current() error after failed Refresh = %v, want ErrCompositionNotUploaded", err)
	}
}

// --- Concurrency: the swap must be atomic -----------------------------------

// TestCompositionStoreConcurrentRefreshAndCurrent is this seam's own
// concurrency acceptance criterion (TRACK-D-D2-SPEC.md §9's D-2/B row):
// many goroutines calling Current() must never observe a half-installed
// TrackedComposition while another goroutine calls Refresh concurrently.
// Run with -race; a torn read here would show up as a data race, and an
// incomplete *TrackedComposition would show up as a nil-slice-vs-populated
// inconsistency within one Current() call's own return value, which this
// test checks for directly rather than relying on -race alone to catch it.
func TestCompositionStoreConcurrentRefreshAndCurrent(t *testing.T) {
	comp := parseTestComposition(t)

	var s CompositionStore
	reader := &fakeCompositionConfigReader{}
	reader.setRevision(t, 1, comp)
	if err := s.Refresh(context.Background(), reader); err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	const readers = 8
	const iterations = 500

	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(readers)
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				tc, err := s.Current()
				if err != nil {
					// Only reachable if a writer concurrently reverted to
					// "not uploaded" — exercised below — never a torn
					// read, since Current is a single atomic load.
					continue
				}
				// A *TrackedComposition built by BuildTrackedComposition
				// is fully populated before it is ever installed
				// (idmap.go's own single-return-at-the-end shape), so
				// Layers/LayerGroups/Decks/Clips/PersistentClips must
				// always be internally consistent with EACH OTHER within
				// one Current() call — e.g. a layer's LayerGroupID, if
				// set, must resolve against LayerGroups. This is the
				// "never observe a half-installed map" check made
				// concrete rather than aspirational.
				for _, l := range tc.Layers() {
					if l.LayerGroupID == nil {
						continue
					}
					found := false
					for _, g := range tc.LayerGroups() {
						if g.ID == *l.LayerGroupID {
							found = true
						}
					}
					if !found {
						t.Errorf("torn read: layer %v names LayerGroupID %v not present in LayerGroups() %v", l.ID, *l.LayerGroupID, tc.LayerGroups())
					}
				}
			}
		}()
	}

	writer := func(revision int64, c *resolumecomp.Composition) {
		reader.setRevision(t, revision, c)
		if err := s.Refresh(context.Background(), reader); err != nil {
			t.Errorf("Refresh(%d): %v", revision, err)
		}
	}

	for i := int64(2); i < int64(iterations); i++ {
		if i%2 == 0 {
			writer(i, comp)
		} else {
			writer(i, &resolumecomp.Composition{Name: "Alt"})
		}
	}

	close(stop)
	wg.Wait()
}
