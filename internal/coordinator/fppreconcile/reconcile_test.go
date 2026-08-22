package fppreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/fppidentity"
)

// --- test fixtures, mirroring internal/coordinator/assetsync's own
// manifest_test.go openTestStore/putConfig helpers. ---

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), t.TempDir(), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func putConfig(t *testing.T, st *store.Store, kind, id, payload string) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: 1, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision %s/%s: %v", kind, id, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, 1); err != nil {
		t.Fatalf("activate config revision %s/%s: %v", kind, id, err)
	}
}

func putShow(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	payload, err := config.EncodeShowPayload(config.ShowPayload{Name: name})
	if err != nil {
		t.Fatalf("encode show payload: %v", err)
	}
	putConfig(t, st, config.ShowConfigKind, id, payload)
}

func putActiveShow(t *testing.T, st *store.Store, showID string) {
	t.Helper()
	payload, err := config.EncodeShowActivePayload(config.ShowActivePayload{Show: showID})
	if err != nil {
		t.Fatalf("encode show.active payload: %v", err)
	}
	// show.active is a singleton object: a SECOND call (switching the
	// active show mid-test, TestReconcileCrossShow's own case) must write
	// the NEXT revision, not revision 1 again.
	ctx := context.Background()
	revision := int64(1)
	if obj, err := st.GetConfigObject(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID); err == nil {
		revision = obj.CurrentRevision + 1
	}
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.ShowActiveConfigKind, ObjectID: config.ShowActiveObjectID, Revision: revision, PayloadJSON: payload, Source: "api",
	}); err != nil {
		t.Fatalf("create config revision show.active/active: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.ShowActiveConfigKind, config.ShowActiveObjectID, revision); err != nil {
		t.Fatalf("activate config revision show.active/active: %v", err)
	}
}

func putCue(t *testing.T, st *store.Store, id, showID string) {
	t.Helper()
	payload, err := config.EncodeShowCuePayload(config.ShowCuePayload{
		Show: showID, Name: id,
		Outputs: config.ShowCueOutputs{Render: &config.ShowCueRenderOutput{Sequence: "seq-" + id}},
	})
	if err != nil {
		t.Fatalf("encode show.cue payload: %v", err)
	}
	putConfig(t, st, config.ShowCueConfigKind, id, payload)
}

func putPlaylist(t *testing.T, st *store.Store, id string, p config.ShowPlaylistPayload) {
	t.Helper()
	payload, err := config.EncodeShowPlaylistPayload(p)
	if err != nil {
		t.Fatalf("encode show.playlist payload: %v", err)
	}
	putConfig(t, st, config.ShowPlaylistConfigKind, id, payload)
}

func putDefinition(t *testing.T, st *store.Store, instanceUUID, playlistHash string) {
	t.Helper()
	_, err := st.PutFPPPlaylistDefinition(context.Background(), store.FPPPlaylistDefinitionRecord{
		InstanceUUID: instanceUUID, PlaylistHash: playlistHash, PlaylistName: "def",
		DefinitionJSON: `{"leadIn":[],"mainPlaylist":[],"leadOut":[]}`,
		CapturedAt:     time.Unix(1000, 0).UTC(), ReceivedAt: time.Unix(1000, 0).UTC(),
	})
	if err != nil {
		t.Fatalf("put fpp playlist definition: %v", err)
	}
}

// hash64 produces a syntactically valid 64-lowercase-hex hash from a short
// label, so tests can name hashes readably ("hash-a") while still passing
// fppidentity.IsHash64.
func hash64(label string) string {
	h := strings.Repeat("0", 64-len(label)) + label
	return h[len(h)-64:]
}

func entryKeyFor(t *testing.T, p config.ShowPlaylistPayload, entryID string) string {
	t.Helper()
	k, err := config.DerivePlaylistEntryKey(p, entryID)
	if err != nil {
		t.Fatalf("derive entry key for %q: %v", entryID, err)
	}
	return k
}

func baseObservation(instanceUUID string) store.FPPPlaylistEntryObservationRecord {
	return store.FPPPlaylistEntryObservationRecord{
		InstanceUUID: instanceUUID, SchemaVersion: 1, Sequence: 1,
		Action: "playing", ObservedAt: time.Unix(2000, 0).UTC(), ReceivedAt: time.Unix(2000, 0).UTC(),
	}
}

// --- table-driven outcome coverage ---

func TestReconcileIdentityUnavailable(t *testing.T) {
	st := openTestStore(t)
	obs := baseObservation("inst-1")
	obs.Unavailable = string(fppidentity.UnavailableMissingDefinition)

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeIdentityUnavailable {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeIdentityUnavailable)
	}
	if result.ObservedUnavailable != string(fppidentity.UnavailableMissingDefinition) {
		t.Fatalf("ObservedUnavailable = %q, want %q", result.ObservedUnavailable, fppidentity.UnavailableMissingDefinition)
	}
	if result.DefinitionAvailable {
		t.Fatal("DefinitionAvailable = true for an identity-unavailable observation, want false: there is no hash to check")
	}
}

// TestReconcileIdentityUnavailableNeverFallsBackToFilenameMatchingEvenWhenABindingExists
// is review fix item 9: TestReconcileIdentityUnavailable alone runs
// against an empty store, so an implementation that quietly fell back to
// matching by filename whenever SOME binding existed for the instance
// would still pass it. This is the sharper test the fixlist calls for: a
// real, active-show binding IS present, and its entry's expected
// filenames are made to equal the unavailable observation's own reported
// filenames exactly (the strongest possible temptation to "just match
// it"), and it must still resolve to nothing.
func TestReconcileIdentityUnavailableNeverFallsBackToFilenameMatchingEvenWhenABindingExists(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "seq.fseq", "media.mp4")
	putPlaylist(t, st, "playlist-1", p)

	obs := baseObservation("inst-1")
	obs.Unavailable = string(fppidentity.UnavailableMissingInstanceUUID)
	// Deliberately equal to the stored binding's own expected filenames:
	// contracts §1.4's forbidden fallback is exactly "match this
	// observation to that entry because the filenames agree."
	obs.SequenceFilename = "seq.fseq"
	obs.MediaFilename = "media.mp4"

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeIdentityUnavailable {
		t.Fatalf("Outcome = %q, want %q (an unavailable observation must NEVER fall back to filename matching, even with a matching binding present)", result.Outcome, OutcomeIdentityUnavailable)
	}
	if result.EntryID != "" || result.CueID != "" || result.PlaylistID != "" {
		t.Fatalf("Result = %+v, want no Playlist/Entry/Cue populated for an identity-unavailable observation", result)
	}
}

func TestReconcileUnboundNoActiveShow(t *testing.T) {
	st := openTestStore(t)
	obs := baseObservation("inst-1")
	obs.PlaylistHash = hash64("h1")
	obs.EntryKey = hash64("e1")

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeUnbound {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeUnbound)
	}
}

func TestReconcileUnboundNoMatchingBinding(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	obs := baseObservation("inst-1")
	obs.PlaylistHash = hash64("h1")
	obs.EntryKey = hash64("e1")

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeUnbound {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeUnbound)
	}
}

// singleEntryPlaylist builds a minimal one-entry fpp-runner show.playlist
// payload, its cue already written to st.
func singleEntryPlaylist(t *testing.T, st *store.Store, showID, instanceUUID, playlistName, playlistHash, cueID string, section string, position int, expectedSeq, expectedMedia string) config.ShowPlaylistPayload {
	t.Helper()
	putCue(t, st, cueID, showID)
	return config.ShowPlaylistPayload{
		Show: showID, Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: instanceUUID, PlaylistName: playlistName, PlaylistHash: playlistHash},
		Entries: []config.ShowPlaylistEntry{{
			ID: "entry-1", Cue: cueID,
			FPP: &config.ShowPlaylistEntryFPP{Section: section, Position: position, ExpectedSequenceFilename: expectedSeq, ExpectedMediaFilename: expectedMedia},
		}},
	}
}

func TestReconcileStaleImportHoldsOldBindingNotRemapped(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	oldHash := hash64("old")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", oldHash, "cue-1", "mainPlaylist", 0, "", "")
	putPlaylist(t, st, "playlist-1", p)

	// The FPP playlist was edited: a NEW definition hash is posted, but the
	// binding is never remapped by reconciliation — only an operator
	// re-import (H1's write path) changes what the binding names.
	newHash := hash64("new")
	obs := baseObservation("inst-1")
	obs.PlaylistName = "Main"
	obs.PlaylistHash = newHash
	obs.Section = "mainPlaylist"
	obs.Position = 0
	obs.EntryKey, _ = deriveEntryKeyForTest("inst-1", "Main", newHash, "mainPlaylist", 0)

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeStaleImport {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeStaleImport)
	}
	if result.BindingPlaylistHash != oldHash {
		t.Fatalf("BindingPlaylistHash = %q, want the OLD bound hash %q (held, not remapped)", result.BindingPlaylistHash, oldHash)
	}
	if result.PlaylistID != "playlist-1" {
		t.Fatalf("PlaylistID = %q, want %q", result.PlaylistID, "playlist-1")
	}
}

func TestReconcileUnknownEntry(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putPlaylist(t, st, "playlist-1", p)
	putDefinition(t, st, "inst-1", hash)

	obs := baseObservation("inst-1")
	obs.PlaylistName = "Main"
	obs.PlaylistHash = hash
	obs.Section = "mainPlaylist"
	obs.Position = 5 // no entry at this position
	obs.EntryKey = hash64("unrelated-entry-key")

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeUnknownEntry {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeUnknownEntry)
	}
	// Hash matched (step 2 passed) before entry-key derivation failed, so
	// DefinitionAvailable is meaningful here and should be true: a
	// definition IS stored for this hash.
	if !result.DefinitionAvailable {
		t.Fatal("DefinitionAvailable = false, want true: a definition is stored for this instance/hash")
	}
}

func TestReconcileEvidenceMismatch(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
	putPlaylist(t, st, "playlist-1", p)

	obs := baseObservation("inst-1")
	obs.PlaylistName = "Main"
	obs.PlaylistHash = hash
	obs.Section = "mainPlaylist"
	obs.Position = 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")
	obs.SequenceFilename = "SomethingElse.fseq"

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeEvidenceMismatch {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeEvidenceMismatch)
	}
}

func TestReconcileCrossShow(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putShow(t, st, "show-2", "Show Two")
	// The binding lives in show-1, but show-2 is the currently active
	// show: step 1 finds the binding (it searches every show, not only
	// the active one — see fppRunnerBindingsForInstance's own doc
	// comment), and step 5's fresh active-show check is what actually
	// catches this.
	putActiveShow(t, st, "show-2")

	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putPlaylist(t, st, "playlist-1", p)

	obs := baseObservation("inst-1")
	obs.PlaylistName = "Main"
	obs.PlaylistHash = hash
	obs.Section = "mainPlaylist"
	obs.Position = 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeCrossShow {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeCrossShow)
	}
}

// TestReconcileTwoBindingsOnOneInstanceChoosesByHashThenActiveShow proves
// item 1 of the H2 review fix list: when two show.playlist objects (in two
// different shows) bind the SAME fpp instanceUuid, the candidate whose
// playlistHash matches the observation and whose show is active must win,
// even when the OTHER candidate sorts first by object id and the
// observation's playlistName does not disambiguate. Before the fix, the
// tiebreak was playlist-name-then-smallest-object-id, which would have
// picked "playlist-a" here: the stale-hash, non-active-show binding.
func TestReconcileTwoBindingsOnOneInstanceChoosesByHashThenActiveShow(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putShow(t, st, "show-2", "Show Two")
	putActiveShow(t, st, "show-2")

	staleHash := hash64("stale")
	activeHash := hash64("active")

	// "playlist-a" sorts first by object id, lives in the INACTIVE show,
	// and binds a hash the observation does NOT report.
	pA := singleEntryPlaylist(t, st, "show-1", "inst-1", "Alpha", staleHash, "cue-a", "mainPlaylist", 0, "", "")
	putPlaylist(t, st, "playlist-a", pA)

	// "playlist-b" sorts second, lives in the ACTIVE show, and binds the
	// hash the observation actually reports.
	pB := singleEntryPlaylist(t, st, "show-2", "inst-1", "Beta", activeHash, "cue-b", "mainPlaylist", 0, "", "")
	putPlaylist(t, st, "playlist-b", pB)

	obs := baseObservation("inst-1")
	// Deliberately not "Alpha" or "Beta": the playlistName tiebreak must
	// not be what resolves this, so the fix is what is under test.
	obs.PlaylistName = "Unrelated"
	obs.PlaylistHash = activeHash
	obs.Section = "mainPlaylist"
	obs.Position = 0
	obs.EntryKey = entryKeyFor(t, pB, "entry-1")

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeResolved {
		t.Fatalf("Outcome = %q, want %q (reason: %s)", result.Outcome, OutcomeResolved, result.Reason)
	}
	if result.PlaylistID != "playlist-b" {
		t.Fatalf("PlaylistID = %q, want %q: the hash-matching, active-show binding", result.PlaylistID, "playlist-b")
	}
	if result.CueID != "cue-b" {
		t.Fatalf("CueID = %q, want %q", result.CueID, "cue-b")
	}
}

func TestReconcileResolved(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "Thriller.fseq", "")
	putPlaylist(t, st, "playlist-1", p)
	putDefinition(t, st, "inst-1", hash)

	obs := baseObservation("inst-1")
	obs.PlaylistName = "Main"
	obs.PlaylistHash = hash
	obs.Section = "mainPlaylist"
	obs.Position = 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")
	obs.SequenceFilename = "Thriller.fseq"

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeResolved {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, OutcomeResolved)
	}
	if result.EntryID != "entry-1" || result.CueID != "cue-1" {
		t.Fatalf("EntryID/CueID = %q/%q, want entry-1/cue-1", result.EntryID, result.CueID)
	}
	if result.CueRevision != 1 {
		t.Fatalf("CueRevision = %d, want 1", result.CueRevision)
	}
	if !result.DefinitionAvailable {
		t.Fatal("DefinitionAvailable = false, want true: a definition is stored for this instance/hash")
	}
}

// TestReconcileResolvedWithDefinitionUnavailable covers H2 spec section
// 5's second "unavailable" case, distinct from OutcomeIdentityUnavailable:
// "an observation whose playlistHash has no stored definition resolves to
// definition-unavailable ... not fatal to matching by entry key." This
// implementation represents that as Result.DefinitionAvailable == false
// alongside a normal Outcome — see [Result.DefinitionAvailable]'s own doc
// comment for why it is a flag, not a seventh Outcome value.
func TestReconcileResolvedWithDefinitionUnavailable(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")

	hash := hash64("h1")
	p := singleEntryPlaylist(t, st, "show-1", "inst-1", "Main", hash, "cue-1", "mainPlaylist", 0, "", "")
	putPlaylist(t, st, "playlist-1", p)
	// Deliberately no putDefinition call: the plugin has not (yet) posted
	// the definition behind this hash.

	obs := baseObservation("inst-1")
	obs.PlaylistName = "Main"
	obs.PlaylistHash = hash
	obs.Section = "mainPlaylist"
	obs.Position = 0
	obs.EntryKey = entryKeyFor(t, p, "entry-1")

	result, err := Reconcile(context.Background(), st, obs)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if result.Outcome != OutcomeResolved {
		t.Fatalf("Outcome = %q, want %q (matching by entry key does not require a stored definition)", result.Outcome, OutcomeResolved)
	}
	if result.DefinitionAvailable {
		t.Fatal("DefinitionAvailable = true, want false: no definition was ever posted for this hash")
	}
}

// TestReconcileDuplicateSequenceFilenameResolvesByKeyAlone proves two
// entries sharing an identical sequence filename resolve to DIFFERENT
// entries purely by their derived entry key — the filename never selects
// anything (H2 spec section 4: "import never makes a filename the Cue
// identity").
func TestReconcileDuplicateSequenceFilenameResolvesByKeyAlone(t *testing.T) {
	st := openTestStore(t)
	putShow(t, st, "show-1", "Show One")
	putActiveShow(t, st, "show-1")
	putCue(t, st, "cue-a", "show-1")
	putCue(t, st, "cue-b", "show-1")

	hash := hash64("h1")
	const sameFilename = "Repeated.fseq"
	p := config.ShowPlaylistPayload{
		Show: "show-1", Name: "Main", Runner: config.ShowPlaylistRunnerFPP,
		MismatchPolicy: config.ShowPlaylistMismatchPolicyHold,
		FPP:            &config.ShowPlaylistFPPBinding{InstanceUUID: "inst-1", PlaylistName: "Main", PlaylistHash: hash},
		Entries: []config.ShowPlaylistEntry{
			{ID: "entry-a", Cue: "cue-a", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 0, ExpectedSequenceFilename: sameFilename}},
			{ID: "entry-b", Cue: "cue-b", FPP: &config.ShowPlaylistEntryFPP{Section: "mainPlaylist", Position: 1, ExpectedSequenceFilename: sameFilename}},
		},
	}
	putPlaylist(t, st, "playlist-1", p)

	obsA := baseObservation("inst-1")
	obsA.PlaylistName, obsA.PlaylistHash, obsA.Section, obsA.Position = "Main", hash, "mainPlaylist", 0
	obsA.EntryKey = entryKeyFor(t, p, "entry-a")
	obsA.SequenceFilename = sameFilename

	obsB := baseObservation("inst-1")
	obsB.PlaylistName, obsB.PlaylistHash, obsB.Section, obsB.Position = "Main", hash, "mainPlaylist", 1
	obsB.EntryKey = entryKeyFor(t, p, "entry-b")
	obsB.SequenceFilename = sameFilename

	resultA, err := Reconcile(context.Background(), st, obsA)
	if err != nil {
		t.Fatalf("Reconcile A: %v", err)
	}
	resultB, err := Reconcile(context.Background(), st, obsB)
	if err != nil {
		t.Fatalf("Reconcile B: %v", err)
	}

	if resultA.Outcome != OutcomeResolved || resultB.Outcome != OutcomeResolved {
		t.Fatalf("both must resolve: A=%q B=%q", resultA.Outcome, resultB.Outcome)
	}
	if resultA.EntryID != "entry-a" || resultA.CueID != "cue-a" {
		t.Fatalf("A resolved to entry %q cue %q, want entry-a/cue-a", resultA.EntryID, resultA.CueID)
	}
	if resultB.EntryID != "entry-b" || resultB.CueID != "cue-b" {
		t.Fatalf("B resolved to entry %q cue %q, want entry-b/cue-b", resultB.EntryID, resultB.CueID)
	}
}

// deriveEntryKeyForTest is a tiny local wrapper so
// TestReconcileStaleImportHoldsOldBindingNotRemapped can construct a
// syntactically valid entry key for an observation whose hash deliberately
// does not match any stored binding (so config.DerivePlaylistEntryKey,
// which needs an actual payload, cannot be used to build it).
func deriveEntryKeyForTest(instanceUUID, playlistName, playlistHash, section string, position int) (string, error) {
	return fppidentity.DeriveEntryKey(fppidentity.EntryIdentity{
		InstanceUUID: instanceUUID, PlaylistName: playlistName, PlaylistHash: playlistHash,
		Section: section, Position: position,
	})
}
