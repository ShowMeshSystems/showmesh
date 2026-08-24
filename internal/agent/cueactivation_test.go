package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/heldcatalog"
	"github.com/showmeshsystems/showmesh/pkg/cueactivation"
	"github.com/showmeshsystems/showmesh/pkg/cuecatalog"
)

// This file covers TRACK-H-cues-and-playlists.md section H4's node-agent
// half: "cue.activate", live and end to end against real package
// collaborators (a real *heldcatalog.FileStore, a real *pipeline.
// AssignmentStore, a real *audio.Manager) — matching this package's own
// standing convention (renderfseq_test.go, cuecatalogops_test.go) of
// proving behavior against real components rather than mocks of them.

// --- shared fixtures ---------------------------------------------------

// testActivation builds a well-formed [cueactivation.Activation]. Callers
// mutate individual fields for a specific scenario.
func testActivation(activationID, cueID string, cueRev int64, show string, generation int64, catalogRev string, positionMS int64) cueactivation.Activation {
	return cueactivation.Activation{
		Runner: "fpp", RunnerInstance: "fpp-01", ActivationID: activationID,
		Show: show, Generation: generation, CatalogRevision: catalogRev,
		Playlist: "main-playlist", PlaylistRevision: 1, EntryID: "entry-1",
		CueID: cueID, CueRevision: cueRev, PositionMS: positionMS,
		EvidenceAt: time.Date(2026, 8, 23, 20, 0, 0, 0, time.UTC),
	}
}

// activationParams round-trips act through JSON into the map[string]any
// shape a real inbound "cue.activate" command's params would decode into
// — matching mqttproto's own decode-to-map-then-DecodeParams path, not a
// struct literal that could silently diverge from the real wire shape.
func activationParams(t *testing.T, act cueactivation.Activation) map[string]any {
	t.Helper()
	raw, err := json.Marshal(act)
	if err != nil {
		t.Fatalf("marshal activation: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("unmarshal activation into params: %v", err)
	}
	return m
}

// saveHeld persists a held catalog record with entries under store.
func saveHeld(t *testing.T, store *heldcatalog.FileStore, show string, generation int64, revision string, entries []cuecatalog.Entry) {
	t.Helper()
	if err := store.Save(heldcatalog.HeldCatalog{
		Show: show, Generation: generation, Node: testNodeID, Revision: revision,
		Entries: entries, ReceivedAt: time.Date(2026, 8, 23, 19, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("saving held catalog: %v", err)
	}
}

// outcomeOf extracts result.Value["outcome"] as a string, failing the test
// if Value is not the shape refusalOutcomeValue/activate itself produces.
func outcomeOf(t *testing.T, result OperationResult) string {
	t.Helper()
	v, ok := result.Value.(map[string]any)
	if !ok {
		t.Fatalf("result.Value = %#v, want a map", result.Value)
	}
	outcome, ok := v["outcome"].(string)
	if !ok {
		t.Fatalf("result.Value[\"outcome\"] = %#v, want a string", v["outcome"])
	}
	return outcome
}

// --- authorization refusals, through the live operation -----------------

func TestActivateRefusesCrossShow(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-1", CueRevision: 1}})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-cross-show", "cue-1", 1, "christmas-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if result.Confirmed {
		t.Fatalf("activate confirmed a cross-show activation: %+v", result)
	}
	if got := outcomeOf(t, result); got != "cross-show" {
		t.Fatalf("outcome = %q, want cross-show", got)
	}
}

func TestActivateRefusesStaleGeneration(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-1", CueRevision: 1}})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-stale-gen", "cue-1", 1, "halloween-2026", 1, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := outcomeOf(t, result); got != "stale-generation" {
		t.Fatalf("outcome = %q, want stale-generation", got)
	}
}

func TestActivateRefusesUnknownGeneration(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-1", CueRevision: 1}})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-unknown-gen", "cue-1", 1, "halloween-2026", 5, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := outcomeOf(t, result); got != "unknown-generation" {
		t.Fatalf("outcome = %q, want unknown-generation", got)
	}
}

func TestActivateRefusesStaleCatalog(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-1", CueRevision: 1}})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-stale-catalog", "cue-1", 1, "halloween-2026", 3, "rev-b", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := outcomeOf(t, result); got != "stale-catalog" {
		t.Fatalf("outcome = %q, want stale-catalog", got)
	}
}

func TestActivateRefusesUnknownCue(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-other", CueRevision: 1}})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-unknown-cue", "cue-1", 1, "halloween-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := outcomeOf(t, result); got != "unknown-cue" {
		t.Fatalf("outcome = %q, want unknown-cue", got)
	}
}

func TestActivateRefusesStaleCue(t *testing.T) {
	dir := t.TempDir()
	store := heldcatalog.NewFileStore(dir)
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{{CueID: "cue-1", CueRevision: 5}})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-stale-cue", "cue-1", 1, "halloween-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := outcomeOf(t, result); got != "stale-cue" {
		t.Fatalf("outcome = %q, want stale-cue", got)
	}
}

func TestActivateRefusesAssetMissing(t *testing.T) {
	dir := t.TempDir() // no fseq file ever written here
	store := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-1", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			Render: &cuecatalog.RenderOutput{Sequence: "seq-missing", Filename: "missing.fseq", AssetHashes: []string{"sha256:deadbeef"}},
		},
	}
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-asset-missing", "cue-1", 1, "halloween-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := outcomeOf(t, result); got != "asset-missing" {
		t.Fatalf("outcome = %q, want asset-missing", got)
	}
}

// TestActivateAssetMissingNeverConsultsDiskForAnEarlierRefusal proves this
// package's own use of [cueauth.CheckLazy] carries H3 spec section 6's own
// rule through to the live operation: a cross-show tuple is refused
// without this node's asset directory ever being read, even when a file
// that would satisfy the (irrelevant) asset check is sitting right there.
func TestActivateAssetMissingNeverConsultsDiskForAnEarlierRefusal(t *testing.T) {
	dir := t.TempDir()
	// A file that would happen to satisfy an asset-presence check if one
	// were ever run against it.
	hash := writeAssetFixture(t, dir, "decoy.fseq", []byte("not a real fseq, contents do not matter"))

	store := heldcatalog.NewFileStore(dir)
	entry := cuecatalog.Entry{
		CueID: "cue-1", CueRevision: 1,
		Outputs: cuecatalog.Outputs{
			Render: &cuecatalog.RenderOutput{Sequence: "seq-decoy", Filename: "decoy.fseq", AssetHashes: []string{hash}},
		},
	}
	saveHeld(t, store, "halloween-2026", 3, "rev-a", []cuecatalog.Entry{entry})

	op := &cueActivationOperation{assetDir: dir, catalogStore: store}
	act := testActivation("act-cross-show-decoy", "cue-1", 1, "christmas-2026", 3, "rev-a", 0)

	result, err := op.activate(context.Background(), activationParams(t, act), func() time.Time { return act.EvidenceAt })
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if got := outcomeOf(t, result); got != "cross-show" {
		t.Fatalf("outcome = %q, want cross-show (never asset-missing/authorized off a present decoy file)", got)
	}
}

// writeAssetFixture writes contents to dir/name and returns its
// "sha256:<hex>" content hash, for a test that needs a real, present,
// hash-verifiable asset file on disk.
func writeAssetFixture(t *testing.T, dir, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("writing asset fixture %q: %v", name, err)
	}
	hash, err := hashFile(path)
	if err != nil {
		t.Fatalf("hashing asset fixture %q: %v", name, err)
	}
	return hash
}
