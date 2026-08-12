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
