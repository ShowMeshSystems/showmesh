package store

import (
	"context"
	"errors"
	"testing"
)

// TestCreateConfigRevisionDuplicateFails is S0-3(a)'s explicit acceptance
// requirement: "a test that a second write of an existing (kind,
// object_id, revision) fails." Broken deliberately by asserting the SECOND
// call in particular fails, not merely that some call in the sequence
// does, so a version of this test that accidentally checked the first
// call's error instead would itself fail.
func TestCreateConfigRevisionDuplicateFails(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	rec := ConfigRevisionRecord{Kind: "fpp_endpoints", ObjectID: "default", Revision: 1, PayloadJSON: `{"hosts":[]}`}
	if _, err := st.CreateConfigRevision(ctx, rec); err != nil {
		t.Fatalf("first create: %v", err)
	}

	_, err := st.CreateConfigRevision(ctx, rec)
	if err == nil {
		t.Fatalf("second create of the same (kind, object_id, revision) succeeded, want ErrConfigRevisionExists")
	}
	if !errors.Is(err, ErrConfigRevisionExists) {
		t.Errorf("error = %v, want it to wrap ErrConfigRevisionExists", err)
	}

	// And the original payload must be exactly what it was — a duplicate
	// write must never have been allowed to overwrite it, which a bug in
	// this method could do even while still returning an error, if the
	// INSERT ran before the failure was detected some other way.
	got, err := st.GetConfigRevision(ctx, "fpp_endpoints", "default", 1)
	if err != nil {
		t.Fatalf("get config revision: %v", err)
	}
	if got.PayloadJSON != `{"hosts":[]}` {
		t.Errorf("payload = %q, want it unchanged by the failed duplicate write", got.PayloadJSON)
	}
}

// TestActivateConfigRevisionNeverMutatesRevisionPayload is S0-3(a)'s other
// explicit requirement: "a test that activating a new revision leaves
// every earlier payload_json byte-identical." Two revisions are created,
// the second is activated, and both are re-read to prove neither payload
// moved — config_revisions has no UPDATE path at all (see migrations.go's
// schemaV6 doc comment), so this also guards against a future change
// accidentally adding one.
func TestActivateConfigRevisionNeverMutatesRevisionPayload(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.CreateConfigRevision(ctx, ConfigRevisionRecord{
		Kind: "fpp_endpoints", ObjectID: "default", Revision: 1, PayloadJSON: `{"hosts":["a"]}`,
	}); err != nil {
		t.Fatalf("create revision 1: %v", err)
	}
	if _, err := st.CreateConfigRevision(ctx, ConfigRevisionRecord{
		Kind: "fpp_endpoints", ObjectID: "default", Revision: 2, PayloadJSON: `{"hosts":["a","b"]}`,
	}); err != nil {
		t.Fatalf("create revision 2: %v", err)
	}

	obj, err := st.ActivateConfigRevision(ctx, "fpp_endpoints", "default", 2)
	if err != nil {
		t.Fatalf("activate revision 2: %v", err)
	}
	if obj.CurrentRevision != 2 {
		t.Errorf("CurrentRevision = %d, want 2", obj.CurrentRevision)
	}

	rev1, err := st.GetConfigRevision(ctx, "fpp_endpoints", "default", 1)
	if err != nil {
		t.Fatalf("get revision 1: %v", err)
	}
	if rev1.PayloadJSON != `{"hosts":["a"]}` {
		t.Errorf("revision 1 payload = %q, want it byte-identical to what was created", rev1.PayloadJSON)
	}
	rev2, err := st.GetConfigRevision(ctx, "fpp_endpoints", "default", 2)
	if err != nil {
		t.Fatalf("get revision 2: %v", err)
	}
	if rev2.PayloadJSON != `{"hosts":["a","b"]}` {
		t.Errorf("revision 2 payload = %q, want it byte-identical to what was created", rev2.PayloadJSON)
	}
}

// TestActivateConfigRevisionCreatesObjectRowIfMissing proves
// current_revision defaults to "0 = no revision activated yet" being
// meaningful: an object with no prior config_objects row gets one on
// first activation, not an error.
func TestActivateConfigRevisionCreatesObjectRowIfMissing(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.GetConfigObject(ctx, "fpp_endpoints", "default"); !errors.Is(err, ErrConfigObjectNotFound) {
		t.Fatalf("GetConfigObject before any activation: err = %v, want ErrConfigObjectNotFound", err)
	}

	if _, err := st.CreateConfigRevision(ctx, ConfigRevisionRecord{
		Kind: "fpp_endpoints", ObjectID: "default", Revision: 1, PayloadJSON: `{}`,
	}); err != nil {
		t.Fatalf("create revision: %v", err)
	}
	obj, err := st.ActivateConfigRevision(ctx, "fpp_endpoints", "default", 1)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1", obj.CurrentRevision)
	}
}

// TestConfigRevisionsNeverPrunedAcrossManyInserts is a light check that
// this package never wires config_revisions into any prune trigger the
// way events/audit/commands/discovery_runs are — see migrations.go's
// schemaV6 doc comment ("NEVER pruned... would delete the rollback
// ADR-009 requires"). 150 inserts is comfortably past pruneEveryNEvents/
// pruneEveryNAuditEntries's 100, which would have started deleting rows
// by now if this table were ever accidentally coupled to that machinery.
func TestConfigRevisionsNeverPrunedAcrossManyInserts(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const n = 150
	for i := 1; i <= n; i++ {
		if _, err := st.CreateConfigRevision(ctx, ConfigRevisionRecord{
			Kind: "fpp_endpoints", ObjectID: "default", Revision: int64(i), PayloadJSON: `{}`,
		}); err != nil {
			t.Fatalf("create revision %d: %v", i, err)
		}
	}

	revs, err := st.ListConfigRevisions(ctx, "fpp_endpoints", "default")
	if err != nil {
		t.Fatalf("list revisions: %v", err)
	}
	if len(revs) != n {
		t.Errorf("len(revs) = %d, want %d (config_revisions must never be pruned)", len(revs), n)
	}
}

// TestCreateConfigObjectEstablishesRevisionZero is F11's addition: proves
// the documented-but-previously-unreachable "no revision activated yet"
// state (migrations.go's schemaV6 doc comment: current_revision = 0) is
// now actually reachable through a real creation path, not just asserted
// in a comment.
func TestCreateConfigObjectEstablishesRevisionZero(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.GetConfigObject(ctx, "fpp_endpoints", "default"); !errors.Is(err, ErrConfigObjectNotFound) {
		t.Fatalf("GetConfigObject before creation: err = %v, want ErrConfigObjectNotFound", err)
	}

	obj, err := st.CreateConfigObject(ctx, "fpp_endpoints", "default")
	if err != nil {
		t.Fatalf("create config object: %v", err)
	}
	if obj.CurrentRevision != 0 {
		t.Errorf("CurrentRevision = %d, want 0 (no revision activated yet)", obj.CurrentRevision)
	}

	got, err := st.GetConfigObject(ctx, "fpp_endpoints", "default")
	if err != nil {
		t.Fatalf("get config object after creation: %v", err)
	}
	if got.CurrentRevision != 0 {
		t.Errorf("stored CurrentRevision = %d, want 0", got.CurrentRevision)
	}
}

// TestCreateConfigObjectDuplicateFails proves a second creation of the
// same (kind, id) is refused rather than silently accepted or
// overwritten, mirroring TestCreateConfigRevisionDuplicateFails and
// TestCreatePrincipalDuplicateNameIsErrPrincipalNameTaken exactly.
func TestCreateConfigObjectDuplicateFails(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.CreateConfigObject(ctx, "fpp_endpoints", "default"); err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err := st.CreateConfigObject(ctx, "fpp_endpoints", "default")
	if !errors.Is(err, ErrConfigObjectExists) {
		t.Errorf("second create error = %v, want ErrConfigObjectExists", err)
	}
}

// TestConfigRevisionTxForm proves the Tx form runs the identical write
// inside a caller-supplied transaction, and that it commits when the
// outer InTx call succeeds.
func TestConfigRevisionTxForm(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if _, err := tx.CreateConfigRevision(ctx, ConfigRevisionRecord{
			Kind: "fpp_endpoints", ObjectID: "default", Revision: 1, PayloadJSON: `{}`,
		}); err != nil {
			return err
		}
		_, err := tx.ActivateConfigRevision(ctx, "fpp_endpoints", "default", 1)
		return err
	})
	if err != nil {
		t.Fatalf("InTx: %v", err)
	}

	obj, err := st.GetConfigObject(ctx, "fpp_endpoints", "default")
	if err != nil {
		t.Fatalf("get config object after commit: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1", obj.CurrentRevision)
	}
}

// --- migrateV30AddConfigObjectDeletedAtColumn tombstone delete ---

func createAndActivate(t *testing.T, st *Store, ctx context.Context, kind, id string, revision int64, payload string) {
	t.Helper()
	if _, err := st.CreateConfigRevision(ctx, ConfigRevisionRecord{
		Kind: kind, ObjectID: id, Revision: revision, PayloadJSON: payload,
	}); err != nil {
		t.Fatalf("create revision %s/%s/%d: %v", kind, id, revision, err)
	}
	if _, err := st.ActivateConfigRevision(ctx, kind, id, revision); err != nil {
		t.Fatalf("activate revision %s/%s/%d: %v", kind, id, revision, err)
	}
}

// TestTombstoneConfigObjectExcludesFromGetAndList is this seam's core
// acceptance requirement: a tombstoned object is absent from
// [Store.GetConfigObject] and [Store.ListConfigObjects], the two methods
// every existence check, GET-by-id handler, and resolution path in
// internal/coordinator/api builds on.
func TestTombstoneConfigObjectExcludesFromGetAndList(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	createAndActivate(t, st, ctx, "audio.node", "node-1", 1, `{"role":"zone"}`)
	createAndActivate(t, st, ctx, "audio.node", "node-2", 1, `{"role":"zone"}`)

	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "node-1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	if _, err := st.GetConfigObject(ctx, "audio.node", "node-1"); !errors.Is(err, ErrConfigObjectNotFound) {
		t.Fatalf("GetConfigObject after tombstone: err = %v, want ErrConfigObjectNotFound", err)
	}

	objs, err := st.ListConfigObjects(ctx, "audio.node")
	if err != nil {
		t.Fatalf("list config objects: %v", err)
	}
	if len(objs) != 1 || objs[0].ID != "node-2" {
		t.Fatalf("ListConfigObjects after tombstoning node-1 = %+v, want only node-2", objs)
	}
}

// TestTombstoneConfigObjectNotFoundWhenMissingOrAlreadyDeleted covers both
// halves of tombstoneConfigObject's own doc comment: an id that was never
// created, and a second delete of one already tombstoned, both refuse the
// same way: idempotent-delete-of-a-deleted-thing reads as 404, not a
// second success and not a distinct error.
func TestTombstoneConfigObjectNotFoundWhenMissingOrAlreadyDeleted(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "never-created"); !errors.Is(err, ErrConfigObjectNotFound) {
		t.Fatalf("tombstone of a never-created id: err = %v, want ErrConfigObjectNotFound", err)
	}

	createAndActivate(t, st, ctx, "audio.node", "node-1", 1, `{"role":"zone"}`)
	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "node-1"); err != nil {
		t.Fatalf("first tombstone: %v", err)
	}
	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "node-1"); !errors.Is(err, ErrConfigObjectNotFound) {
		t.Fatalf("second tombstone of an already-deleted id: err = %v, want ErrConfigObjectNotFound", err)
	}
}

// TestTombstoneConfigObjectLeavesRevisionHistoryReadable proves ADR-009's
// rule survives a delete: config_revisions is never touched by
// TombstoneConfigObject, so every revision this object ever held still
// reads back byte-identical through [Store.GetConfigRevision] and
// [Store.ListConfigRevisions] after it is gone from every list and every
// live lookup.
func TestTombstoneConfigObjectLeavesRevisionHistoryReadable(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	createAndActivate(t, st, ctx, "audio.node", "node-1", 1, `{"role":"zone","zone":"lobby"}`)
	createAndActivate(t, st, ctx, "audio.node", "node-1", 2, `{"role":"zone","zone":"lobby-renamed"}`)

	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "node-1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	rev1, err := st.GetConfigRevision(ctx, "audio.node", "node-1", 1)
	if err != nil {
		t.Fatalf("get revision 1 after tombstone: %v", err)
	}
	if rev1.PayloadJSON != `{"role":"zone","zone":"lobby"}` {
		t.Errorf("revision 1 payload after tombstone = %q, want it unchanged", rev1.PayloadJSON)
	}
	rev2, err := st.GetConfigRevision(ctx, "audio.node", "node-1", 2)
	if err != nil {
		t.Fatalf("get revision 2 after tombstone: %v", err)
	}
	if rev2.PayloadJSON != `{"role":"zone","zone":"lobby-renamed"}` {
		t.Errorf("revision 2 payload after tombstone = %q, want it unchanged", rev2.PayloadJSON)
	}

	revs, err := st.ListConfigRevisions(ctx, "audio.node", "node-1")
	if err != nil {
		t.Fatalf("list revisions after tombstone: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("ListConfigRevisions after tombstone returned %d revisions, want 2", len(revs))
	}
}

// TestTombstoneConfigObjectExcludesMediaPlaylistFromListAndResolutionWhileKeepingRevisions
// proves the v30 tombstone (migrateV30AddConfigObjectDeletedAtColumn) is
// kind-agnostic: media.playlist is deletable the same way as every other
// per-object configuration kind, needing no store change of its own. Kind
// is spelled as a literal here rather than imported from
// internal/coordinator/config, matching this package's one-way dependency
// rule (migration_v19.go's own doc comment).
func TestTombstoneConfigObjectExcludesMediaPlaylistFromListAndResolutionWhileKeepingRevisions(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	createAndActivate(t, st, ctx, "media.playlist", "resting-bed", 1, `{"show":"halloween-2026","label":"Resting bed"}`)
	createAndActivate(t, st, ctx, "media.playlist", "another-bed", 1, `{"show":"halloween-2026","label":"Another bed"}`)

	if _, err := st.TombstoneConfigObject(ctx, "media.playlist", "resting-bed"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	if _, err := st.GetConfigObject(ctx, "media.playlist", "resting-bed"); !errors.Is(err, ErrConfigObjectNotFound) {
		t.Fatalf("GetConfigObject after tombstone: err = %v, want ErrConfigObjectNotFound", err)
	}

	objs, err := st.ListConfigObjects(ctx, "media.playlist")
	if err != nil {
		t.Fatalf("list config objects: %v", err)
	}
	if len(objs) != 1 || objs[0].ID != "another-bed" {
		t.Fatalf("ListConfigObjects after tombstoning resting-bed = %+v, want only another-bed", objs)
	}

	rev, err := st.GetConfigRevision(ctx, "media.playlist", "resting-bed", 1)
	if err != nil {
		t.Fatalf("get revision after tombstone: %v", err)
	}
	if rev.PayloadJSON != `{"show":"halloween-2026","label":"Resting bed"}` {
		t.Errorf("revision payload after tombstone = %q, want it unchanged", rev.PayloadJSON)
	}
	revs, err := st.ListConfigRevisions(ctx, "media.playlist", "resting-bed")
	if err != nil {
		t.Fatalf("list revisions after tombstone: %v", err)
	}
	if len(revs) != 1 {
		t.Fatalf("ListConfigRevisions after tombstone returned %d revisions, want 1", len(revs))
	}
}

// TestGetConfigObjectIncludingDeletedSeesTombstone proves the one accessor
// that CAN see a tombstoned row does, while the default GetConfigObject
// still cannot: the two-method split this seam's design requires so that
// a call site added later is correct by default without knowing this
// feature exists.
func TestGetConfigObjectIncludingDeletedSeesTombstone(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	createAndActivate(t, st, ctx, "audio.node", "node-1", 1, `{"role":"zone"}`)
	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "node-1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	if _, err := st.GetConfigObject(ctx, "audio.node", "node-1"); !errors.Is(err, ErrConfigObjectNotFound) {
		t.Fatalf("GetConfigObject after tombstone: err = %v, want ErrConfigObjectNotFound", err)
	}

	obj, err := st.GetConfigObjectIncludingDeleted(ctx, "audio.node", "node-1")
	if err != nil {
		t.Fatalf("GetConfigObjectIncludingDeleted after tombstone: %v", err)
	}
	if obj.DeletedAt == nil {
		t.Errorf("GetConfigObjectIncludingDeleted DeletedAt = nil, want it set")
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("GetConfigObjectIncludingDeleted CurrentRevision = %d, want 1 (the true last-activated revision, unchanged by tombstoning)", obj.CurrentRevision)
	}
}

// TestReactivateAfterTombstoneContinuesRevisionNumberingAndUndeletes is
// this seam's re-creation acceptance requirement: PUTting a new revision
// for a tombstoned id (a) clears the tombstone, (b) is immediately visible
// again through GetConfigObject/ListConfigObjects, and (c) continues
// revision numbering from the object's true history rather than resetting
// to 1 and colliding with the revision that was already there.
func TestReactivateAfterTombstoneContinuesRevisionNumberingAndUndeletes(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	createAndActivate(t, st, ctx, "audio.node", "node-1", 1, `{"role":"zone","zone":"a"}`)
	createAndActivate(t, st, ctx, "audio.node", "node-1", 2, `{"role":"zone","zone":"b"}`)
	if _, err := st.TombstoneConfigObject(ctx, "audio.node", "node-1"); err != nil {
		t.Fatalf("tombstone: %v", err)
	}

	// A collision-free re-create computes its next revision number from
	// the object's TRUE current_revision (2), the way
	// writeShowConfigRevision's own AuditedWrite closure does via
	// GetConfigObjectIncludingDeleted, not from a filtered read that
	// would see nothing and restart numbering at 1.
	trueObj, err := st.GetConfigObjectIncludingDeleted(ctx, "audio.node", "node-1")
	if err != nil {
		t.Fatalf("get true object before re-create: %v", err)
	}
	nextRevision := trueObj.CurrentRevision + 1
	if nextRevision != 3 {
		t.Fatalf("computed next revision = %d, want 3", nextRevision)
	}

	// Colliding at revision 1 or 2 must fail loudly (ErrConfigRevisionExists),
	// proving the collision this design exists to avoid is real and would
	// have been hit by a naive "reset to 1" re-create.
	if _, err := st.CreateConfigRevision(ctx, ConfigRevisionRecord{
		Kind: "audio.node", ObjectID: "node-1", Revision: 1, PayloadJSON: `{"role":"zone","zone":"collide"}`,
	}); !errors.Is(err, ErrConfigRevisionExists) {
		t.Fatalf("re-creating at revision 1 (already used before the delete): err = %v, want ErrConfigRevisionExists", err)
	}

	createAndActivate(t, st, ctx, "audio.node", "node-1", nextRevision, `{"role":"zone","zone":"c"}`)

	obj, err := st.GetConfigObject(ctx, "audio.node", "node-1")
	if err != nil {
		t.Fatalf("get config object after re-create: %v", err)
	}
	if obj.DeletedAt != nil {
		t.Errorf("DeletedAt after re-create = %v, want nil: activating a revision must clear a tombstone", obj.DeletedAt)
	}
	if obj.CurrentRevision != 3 {
		t.Errorf("CurrentRevision after re-create = %d, want 3", obj.CurrentRevision)
	}

	objs, err := st.ListConfigObjects(ctx, "audio.node")
	if err != nil {
		t.Fatalf("list config objects after re-create: %v", err)
	}
	if len(objs) != 1 || objs[0].ID != "node-1" {
		t.Fatalf("ListConfigObjects after re-create = %+v, want node-1 visible again", objs)
	}

	revs, err := st.ListConfigRevisions(ctx, "audio.node", "node-1")
	if err != nil {
		t.Fatalf("list revisions after re-create: %v", err)
	}
	if len(revs) != 3 {
		t.Fatalf("ListConfigRevisions after re-create returned %d revisions, want 3 (1, 2, and the new 3; no gap, nothing overwritten)", len(revs))
	}
}
