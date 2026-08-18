package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestAsset(id, showID, sequenceID, targetKind, targetID, hash, filename string) AssetRecord {
	return AssetRecord{
		ID:                     id,
		ShowID:                 showID,
		SequenceID:             sequenceID,
		TargetKind:             targetKind,
		TargetID:               targetID,
		MediaType:              "fseq",
		ContentHash:            hash,
		RuntimeFilename:        filename,
		SizeBytes:              1234,
		Backend:                "volume",
		StorageKey:             "ab/" + hash,
		CreatedByPrincipalID:   "principal-1",
		CreatedByPrincipalName: "operator",
	}
}

// TestCreateAssetPersistsAndIsCurrent proves a fresh asset is written with
// SupersededAt nil (current) and every field round-trips.
func TestCreateAssetPersistsAndIsCurrent(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	in := newTestAsset("asset-1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:aaa", "Thriller.fseq")
	out, _, err := st.CreateAsset(ctx, in)
	if err != nil {
		t.Fatalf("create asset: %v", err)
	}
	if out.SupersededAt != nil {
		t.Errorf("SupersededAt = %v, want nil (a fresh asset is current)", out.SupersededAt)
	}
	if out.CreatedAt.IsZero() {
		t.Errorf("CreatedAt is zero, want it stamped")
	}

	got, err := st.GetAsset(ctx, "asset-1")
	if err != nil {
		t.Fatalf("get asset: %v", err)
	}
	if got.ShowID != in.ShowID || got.SequenceID != in.SequenceID || got.TargetKind != in.TargetKind ||
		got.TargetID != in.TargetID || got.MediaType != in.MediaType || got.ContentHash != in.ContentHash ||
		got.RuntimeFilename != in.RuntimeFilename || got.SizeBytes != in.SizeBytes || got.Backend != in.Backend ||
		got.StorageKey != in.StorageKey {
		t.Errorf("round-tripped asset = %+v, want it to match input %+v", got, in)
	}
}

// TestGetAssetNotFound proves an unknown id returns ErrAssetNotFound rather
// than a zero-value success.
func TestGetAssetNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.GetAsset(ctx, "does-not-exist"); !errors.Is(err, ErrAssetNotFound) {
		t.Errorf("err = %v, want ErrAssetNotFound", err)
	}
}

// TestCreateAssetIdenticalIdentityIsIdempotent is spec §3.3's requirement:
// "Re-uploading identical bytes for an identity that already exists is
// idempotent: 200 with the existing asset, no new row, no new blob."
// Deliberately checks the SECOND call specifically returns the sentinel
// AND that the first row is untouched, so a version of this test that
// only checked "an error occurred somewhere" could not pass by accident.
func TestCreateAssetIdenticalIdentityIsIdempotent(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := newTestAsset("asset-1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:aaa", "Thriller.fseq")
	created, _, err := st.CreateAsset(ctx, first)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Same identity tuple, same content hash, but a different caller-chosen
	// ID and StorageKey — as a real re-upload would produce, since a second
	// upload of the same bytes streams to a new staging file before the
	// content-addressed backend collapses it. Identity, not row equality,
	// is what CreateAsset must detect.
	second := newTestAsset("asset-2", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:aaa", "Thriller.fseq")
	second.StorageKey = "ab/different-staging-path"

	_, _, err = st.CreateAsset(ctx, second)
	if err == nil {
		t.Fatalf("second create of an identical identity succeeded, want *AssetIdentityExistsError")
	}
	if !errors.Is(err, ErrAssetExists) {
		t.Errorf("error = %v, want it to wrap ErrAssetExists", err)
	}
	var idErr *AssetIdentityExistsError
	if !errors.As(err, &idErr) {
		t.Fatalf("error = %v, want *AssetIdentityExistsError", err)
	}
	if idErr.Existing.ID != created.ID {
		t.Errorf("Existing.ID = %q, want %q (the first-created row)", idErr.Existing.ID, created.ID)
	}

	// No new row: asset-2 must not exist, and the original must be untouched
	// (still current — a duplicate write must never have superseded itself).
	if _, err := st.GetAsset(ctx, "asset-2"); !errors.Is(err, ErrAssetNotFound) {
		t.Errorf("GetAsset(asset-2) err = %v, want ErrAssetNotFound (no new row)", err)
	}
	original, err := st.GetAsset(ctx, "asset-1")
	if err != nil {
		t.Fatalf("get original: %v", err)
	}
	if original.SupersededAt != nil {
		t.Errorf("original.SupersededAt = %v, want nil (an idempotent re-upload must not supersede itself)", original.SupersededAt)
	}
}

// TestCreateAssetSupersedesPriorCurrentInSameTransaction proves uploading
// DIFFERENT bytes for the same (show, sequence, target) marks the previous
// asset superseded and makes the new one current, per spec §3.3.
func TestCreateAssetSupersedesPriorCurrentInSameTransaction(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first := newTestAsset("asset-1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:aaa", "Thriller.fseq")
	if _, _, err := st.CreateAsset(ctx, first); err != nil {
		t.Fatalf("create first: %v", err)
	}

	second := newTestAsset("asset-2", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:bbb", "Thriller.fseq")
	if _, _, err := st.CreateAsset(ctx, second); err != nil {
		t.Fatalf("create second: %v", err)
	}

	gotFirst, err := st.GetAsset(ctx, "asset-1")
	if err != nil {
		t.Fatalf("get first: %v", err)
	}
	if gotFirst.SupersededAt == nil {
		t.Errorf("first asset SupersededAt = nil, want it superseded by the second upload")
	}

	gotSecond, err := st.GetAsset(ctx, "asset-2")
	if err != nil {
		t.Fatalf("get second: %v", err)
	}
	if gotSecond.SupersededAt != nil {
		t.Errorf("second asset SupersededAt = %v, want nil (it is now current)", gotSecond.SupersededAt)
	}

	current, err := st.ListCurrentAssetsForTarget(ctx, "halloween-2026", AssetTargetKindNode, "render-01")
	if err != nil {
		t.Fatalf("list current: %v", err)
	}
	if len(current) != 1 || current[0].ID != "asset-2" {
		t.Errorf("current assets = %+v, want exactly [asset-2]", current)
	}
}

// TestCreateAssetRollsBackSupersededIdentity proves the rollback path
// (ADR-028 decision 10): rolledBack=true, no third row inserted.
func TestCreateAssetRollsBackSupersededIdentity(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	a := newTestAsset("asset-a", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:aaa", "Thriller.fseq")
	if _, rb, err := st.CreateAsset(ctx, a); err != nil || rb {
		t.Fatalf("create a: rolledBack=%v err=%v, want false/nil", rb, err)
	}
	b := newTestAsset("asset-b", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:bbb", "Thriller.fseq")
	if _, rb, err := st.CreateAsset(ctx, b); err != nil || rb {
		t.Fatalf("create b: rolledBack=%v err=%v, want false/nil", rb, err)
	}

	// Re-upload A's exact identity (same ID, same everything) — the
	// rollback trigger.
	rolledBack, rb, err := st.CreateAsset(ctx, a)
	if err != nil {
		t.Fatalf("rollback create: %v", err)
	}
	if !rb {
		t.Fatal("rolledBack = false, want true (a's identity was superseded)")
	}
	if rolledBack.ID != "asset-a" || rolledBack.SupersededAt != nil {
		t.Errorf("rollback result = %+v, want asset-a current", rolledBack)
	}

	gotA, err := st.GetAsset(ctx, "asset-a")
	if err != nil {
		t.Fatalf("get a: %v", err)
	}
	if gotA.SupersededAt != nil {
		t.Errorf("asset-a SupersededAt = %v, want nil after rollback", gotA.SupersededAt)
	}
	gotB, err := st.GetAsset(ctx, "asset-b")
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
	if gotB.SupersededAt == nil {
		t.Error("asset-b SupersededAt = nil, want it superseded by the rollback")
	}

	all, err := st.ListAssets(ctx, AssetFilter{ShowID: "halloween-2026", SequenceID: "opening"})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("row count after rollback = %d, want 2 (no third row inserted)", len(all))
	}

	current, err := st.ListCurrentAssetsForTarget(ctx, "halloween-2026", AssetTargetKindNode, "render-01")
	if err != nil {
		t.Fatalf("list current: %v", err)
	}
	if len(current) != 1 || current[0].ID != "asset-a" {
		t.Errorf("current assets = %+v, want exactly [asset-a]", current)
	}
}

// TestCreateAssetRollbackRollForwardRollbackCycleTerminates proves a
// rollback/roll-forward/rollback cycle stays at two rows, never a walk.
func TestCreateAssetRollbackRollForwardRollbackCycleTerminates(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	a := newTestAsset("cycle-a", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:aaa", "f.fseq")
	b := newTestAsset("cycle-b", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:bbb", "f.fseq")

	mustCurrent := func(wantID string) {
		t.Helper()
		current, err := st.ListCurrentAssetsForTarget(ctx, "halloween-2026", AssetTargetKindNode, "render-01")
		if err != nil {
			t.Fatalf("list current: %v", err)
		}
		if len(current) != 1 || current[0].ID != wantID {
			t.Fatalf("current = %+v, want exactly [%s]", current, wantID)
		}
	}

	if _, rb, err := st.CreateAsset(ctx, a); err != nil || rb {
		t.Fatalf("create a: rolledBack=%v err=%v", rb, err)
	}
	mustCurrent("cycle-a")

	if _, rb, err := st.CreateAsset(ctx, b); err != nil || rb {
		t.Fatalf("create b: rolledBack=%v err=%v", rb, err)
	}
	mustCurrent("cycle-b")

	// Rollback to A.
	if _, rb, err := st.CreateAsset(ctx, a); err != nil || !rb {
		t.Fatalf("rollback to a: rolledBack=%v err=%v, want true/nil", rb, err)
	}
	mustCurrent("cycle-a")

	// Roll forward to B — mechanically the identical rollback operation
	// run against B's now-superseded identity.
	if _, rb, err := st.CreateAsset(ctx, b); err != nil || !rb {
		t.Fatalf("roll forward to b: rolledBack=%v err=%v, want true/nil", rb, err)
	}
	mustCurrent("cycle-b")

	// Rollback to A again — the second time around the cycle.
	if _, rb, err := st.CreateAsset(ctx, a); err != nil || !rb {
		t.Fatalf("second rollback to a: rolledBack=%v err=%v, want true/nil", rb, err)
	}
	mustCurrent("cycle-a")

	all, err := st.ListAssets(ctx, AssetFilter{ShowID: "halloween-2026", SequenceID: "opening"})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("row count after a full cycle = %d, want 2 (still no third row)", len(all))
	}
}

// TestAssetsCurrentPartialIndexRejectsTwoCurrentRows is the acceptance
// requirement to verify the partial unique index actually works under
// modernc.org/sqlite by running it, not by assuming CREATE UNIQUE INDEX ...
// WHERE succeeded silently. It goes around CreateAsset's own supersede
// logic and inserts two rows with superseded_at IS NULL for the identical
// (show, sequence, target) tuple directly, proving the database itself —
// not just this package's application logic — refuses the second one.
func TestAssetsCurrentPartialIndexRejectsTwoCurrentRows(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const insert = `
		INSERT INTO assets (
			id, show_id, sequence_id, target_kind, target_id, media_type, content_hash,
			runtime_filename, size_bytes, backend, storage_key, created_at,
			created_by_principal_id, created_by_principal_name, superseded_at
		) VALUES (?, 'halloween-2026', 'opening', 'node', 'render-01', 'fseq', ?, 'Thriller.fseq', 1, 'volume', ?, ?, '', '', NULL)
	`
	now := timeToDB(time.Now())
	if _, err := st.db.ExecContext(ctx, insert, "raw-1", "sha256:aaa", "ab/aaa", now); err != nil {
		t.Fatalf("insert first current row directly: %v", err)
	}

	_, err := st.db.ExecContext(ctx, insert, "raw-2", "sha256:bbb", "ab/bbb", now)
	if err == nil {
		t.Fatalf("insert of a second current row for the same (show, sequence, target) succeeded, want a UNIQUE constraint violation from assets_current")
	}
	if !isUniqueConstraintErr(err) {
		t.Errorf("error = %v, want modernc.org/sqlite's UNIQUE constraint failed text", err)
	}
}

// TestCreateAssetRequiresFields is a table-driven check that every required
// field is enforced before any database work happens.
func TestCreateAssetRequiresFields(t *testing.T) {
	base := newTestAsset("asset-1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:aaa", "Thriller.fseq")

	cases := []struct {
		name   string
		break_ func(*AssetRecord)
	}{
		{"ID", func(r *AssetRecord) { r.ID = "" }},
		{"ShowID", func(r *AssetRecord) { r.ShowID = "" }},
		{"SequenceID", func(r *AssetRecord) { r.SequenceID = "" }},
		{"TargetKind", func(r *AssetRecord) { r.TargetKind = "" }},
		{"MediaType", func(r *AssetRecord) { r.MediaType = "" }},
		{"ContentHash", func(r *AssetRecord) { r.ContentHash = "" }},
		{"RuntimeFilename", func(r *AssetRecord) { r.RuntimeFilename = "" }},
		{"Backend", func(r *AssetRecord) { r.Backend = "" }},
		{"StorageKey", func(r *AssetRecord) { r.StorageKey = "" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := openTestStore(t, nil)
			ctx := context.Background()
			rec := base
			tc.break_(&rec)
			if _, _, err := st.CreateAsset(ctx, rec); err == nil {
				t.Errorf("CreateAsset with empty %s succeeded, want an error", tc.name)
			}
		})
	}
}

// TestThreeAssetsSameRuntimeFilenameResolveIndependently is spec §3.1's
// named acceptance test: "three rows, same runtime_filename, different
// hashes, different targets, each resolves to its own." This is ADR-028
// decision 1 made concrete: xLights gives three per-target FSEQ variants
// of one sequence the identical filename, and a store keyed on filename
// would silently collapse them into one artifact.
func TestThreeAssetsSameRuntimeFilenameResolveIndependently(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const filename = "HalloweenOpening.fseq"
	assets := []AssetRecord{
		newTestAsset("asset-front", "halloween-2026", "opening", AssetTargetKindNode, "media-front", "sha256:front", filename),
		newTestAsset("asset-side", "halloween-2026", "opening", AssetTargetKindNode, "media-side", "sha256:side", filename),
		newTestAsset("asset-garage", "halloween-2026", "opening", AssetTargetKindNode, "media-garage", "sha256:garage", filename),
	}
	for _, a := range assets {
		if _, _, err := st.CreateAsset(ctx, a); err != nil {
			t.Fatalf("create %s: %v", a.ID, err)
		}
	}

	cases := []struct {
		node     string
		wantID   string
		wantHash string
	}{
		{"media-front", "asset-front", "sha256:front"},
		{"media-side", "asset-side", "sha256:side"},
		{"media-garage", "asset-garage", "sha256:garage"},
	}
	for _, tc := range cases {
		current, err := st.ListCurrentAssetsForTarget(ctx, "halloween-2026", AssetTargetKindNode, tc.node)
		if err != nil {
			t.Fatalf("list current for %s: %v", tc.node, err)
		}
		if len(current) != 1 {
			t.Fatalf("node %s: %d current assets, want exactly 1: %+v", tc.node, len(current), current)
		}
		if current[0].ID != tc.wantID || current[0].ContentHash != tc.wantHash {
			t.Errorf("node %s resolved to %+v, want id=%s hash=%s", tc.node, current[0], tc.wantID, tc.wantHash)
		}
		if current[0].RuntimeFilename != filename {
			t.Errorf("node %s runtime filename = %q, want %q preserved", tc.node, current[0].RuntimeFilename, filename)
		}
	}

	// And a filter-based listing by node must return exactly that node's
	// own asset, never another node's row that happens to share a filename.
	frontOnly, err := st.ListAssets(ctx, AssetFilter{NodeID: "media-front"})
	if err != nil {
		t.Fatalf("list assets filtered by node: %v", err)
	}
	if len(frontOnly) != 1 || frontOnly[0].ID != "asset-front" {
		t.Errorf("ListAssets(NodeID=media-front) = %+v, want exactly [asset-front]", frontOnly)
	}
}

// TestListAssetsFilters proves ShowID/SequenceID/NodeID each narrow the
// result independently and in combination.
func TestListAssetsFilters(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	seed := []AssetRecord{
		newTestAsset("a1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:a1", "f.fseq"),
		newTestAsset("a2", "halloween-2026", "closing", AssetTargetKindNode, "render-01", "sha256:a2", "f.fseq"),
		newTestAsset("a3", "halloween-2026", "opening", AssetTargetKindNode, "render-02", "sha256:a3", "f.fseq"),
		newTestAsset("a4", "christmas-2026", "opening", AssetTargetKindNode, "render-01", "sha256:a4", "f.fseq"),
		newTestAsset("a5", "halloween-2026", "opening", AssetTargetKindShow, "", "sha256:a5", "announce.mp3"),
	}
	for _, a := range seed {
		if _, _, err := st.CreateAsset(ctx, a); err != nil {
			t.Fatalf("create %s: %v", a.ID, err)
		}
	}

	all, err := st.ListAssets(ctx, AssetFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != len(seed) {
		t.Errorf("list all = %d rows, want %d", len(all), len(seed))
	}

	byShow, err := st.ListAssets(ctx, AssetFilter{ShowID: "halloween-2026"})
	if err != nil {
		t.Fatalf("list by show: %v", err)
	}
	if len(byShow) != 4 {
		t.Errorf("list by show = %d rows, want 4 (a1, a2, a3, a5)", len(byShow))
	}

	bySequence, err := st.ListAssets(ctx, AssetFilter{ShowID: "halloween-2026", SequenceID: "opening"})
	if err != nil {
		t.Fatalf("list by show+sequence: %v", err)
	}
	if len(bySequence) != 3 {
		t.Errorf("list by show+sequence = %d rows, want 3 (a1, a3, a5)", len(bySequence))
	}

	byNode, err := st.ListAssets(ctx, AssetFilter{NodeID: "render-01"})
	if err != nil {
		t.Fatalf("list by node: %v", err)
	}
	if len(byNode) != 3 {
		t.Errorf("list by node = %d rows, want 3 (a1, a2, a4)", len(byNode))
	}
	for _, a := range byNode {
		if a.TargetKind != AssetTargetKindNode || a.TargetID != "render-01" {
			t.Errorf("node filter returned non-matching row %+v", a)
		}
	}
}

// TestListCurrentAssetsForTargetExcludesSuperseded proves the manifest
// primitive never returns a superseded row.
func TestListCurrentAssetsForTargetExcludesSuperseded(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, _, err := st.CreateAsset(ctx, newTestAsset("a1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:a1", "f.fseq")); err != nil {
		t.Fatalf("create a1: %v", err)
	}
	if _, _, err := st.CreateAsset(ctx, newTestAsset("a2", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:a2", "f.fseq")); err != nil {
		t.Fatalf("create a2: %v", err)
	}

	current, err := st.ListCurrentAssetsForTarget(ctx, "halloween-2026", AssetTargetKindNode, "render-01")
	if err != nil {
		t.Fatalf("list current: %v", err)
	}
	if len(current) != 1 || current[0].ID != "a2" {
		t.Fatalf("current = %+v, want exactly [a2] (a1 was superseded)", current)
	}

	// A target nobody has ever uploaded for gets an empty slice, not an error.
	empty, err := st.ListCurrentAssetsForTarget(ctx, "halloween-2026", AssetTargetKindNode, "render-99")
	if err != nil {
		t.Fatalf("list current for unknown target: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("current for unknown target = %+v, want empty", empty)
	}
}

// TestCreateAssetTxForm proves the Tx form composes into a caller-supplied
// transaction and commits with it, mirroring TestConfigRevisionTxForm.
func TestCreateAssetTxForm(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		_, _, err := tx.CreateAsset(ctx, newTestAsset("a1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:a1", "f.fseq"))
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	got, err := st.GetAsset(ctx, "a1")
	if err != nil {
		t.Fatalf("get after commit: %v", err)
	}
	if got.ID != "a1" {
		t.Errorf("got.ID = %q, want a1", got.ID)
	}
}

// TestCreateAssetTxFormRollsBackOnError proves a failed Tx-form write
// (here, a second CreateAsset within the same transaction hitting the
// idempotent-identity sentinel) rolls the whole transaction back, so a
// caller composing the metadata write with an audit entry per ADR-024
// decision 11 never gets a half-applied result.
func TestCreateAssetTxFormRollsBackOnError(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	rec := newTestAsset("a1", "halloween-2026", "opening", AssetTargetKindNode, "render-01", "sha256:a1", "f.fseq")

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, _, err := tx.CreateAsset(ctx, rec); err != nil {
			return err
		}
		// Deliberately fail the transaction after a successful write inside
		// it, so nothing about this InTx call should be observable afterward.
		return errors.New("boom")
	})
	if err == nil {
		t.Fatalf("InTx succeeded, want the injected failure to propagate")
	}

	if _, err := st.GetAsset(ctx, "a1"); !errors.Is(err, ErrAssetNotFound) {
		t.Errorf("GetAsset(a1) after rollback err = %v, want ErrAssetNotFound", err)
	}
}
