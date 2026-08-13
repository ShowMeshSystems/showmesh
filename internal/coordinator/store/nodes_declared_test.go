package store

import (
	"context"
	"errors"
	"testing"
)

// TestDeclareNodeSurvivesAbsentObservedRow is this table's central
// property (migrations.go's schemaV6 doc comment): a declared node has NO
// foreign key to nodes, so declaring a node this store has never seen an
// agent hello from must succeed rather than fail on a constraint — the
// opposite of an ON DELETE CASCADE, and the whole reason node_id carries
// no FK here.
func TestDeclareNodeSurvivesAbsentObservedRow(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.GetNode(ctx, "media-99"); !errors.Is(err, ErrNodeNotFound) {
		t.Fatalf("precondition: GetNode should find nothing, got err = %v", err)
	}

	rec, err := st.DeclareNode(ctx, NodeDeclarationRecord{NodeID: "media-99", Label: "Roof"})
	if err != nil {
		t.Fatalf("declare node with no corresponding observed nodes row: %v", err)
	}
	if rec.NodeID != "media-99" {
		t.Errorf("NodeID = %q, want %q", rec.NodeID, "media-99")
	}
	if rec.LastDiscoveredAt != nil {
		t.Errorf("LastDiscoveredAt = %v, want nil (no complete discovery run has ever seen it)", rec.LastDiscoveredAt)
	}
}

// TestDeclareNodeUpdateKeepsOriginalAttribution proves re-declaring an
// already-declared node updates its editable fields (Label, Notes) but
// never overwrites who first declared it or when.
func TestDeclareNodeUpdateKeepsOriginalAttribution(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	first, err := st.DeclareNode(ctx, NodeDeclarationRecord{
		NodeID: "media-01", Label: "Front", DeclaredByPrincipalID: "admin-1", DeclaredByPrincipalName: "Admin",
	})
	if err != nil {
		t.Fatalf("first declare: %v", err)
	}

	second, err := st.DeclareNode(ctx, NodeDeclarationRecord{
		NodeID: "media-01", Label: "Front Yard", DeclaredByPrincipalID: "operator-1", DeclaredByPrincipalName: "Operator",
	})
	if err != nil {
		t.Fatalf("second declare (update): %v", err)
	}
	if second.Label != "Front Yard" {
		t.Errorf("Label = %q, want %q (updated)", second.Label, "Front Yard")
	}
	if second.DeclaredByPrincipalID != first.DeclaredByPrincipalID {
		t.Errorf("DeclaredByPrincipalID = %q, want it to stay %q (the original declarer)", second.DeclaredByPrincipalID, first.DeclaredByPrincipalID)
	}
	if !second.DeclaredAt.Equal(first.DeclaredAt) {
		t.Errorf("DeclaredAt changed on update: first %v, second %v", first.DeclaredAt, second.DeclaredAt)
	}
}

// TestRecordNodeDiscoverySeenNeverCreatesOrDeletes proves the absence-is-
// not-evidence-of-absence rule at the repository layer: seeing an
// UNDECLARED node during discovery must not silently declare it (that is
// an operator's promotion, not this method's job), and this method has no
// delete path at all.
func TestRecordNodeDiscoverySeenNeverCreatesOrDeletes(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	err := st.RecordNodeDiscoverySeen(ctx, "never-declared", "run-1", st.now())
	if !errors.Is(err, ErrNodeDeclarationNotFound) {
		t.Fatalf("RecordNodeDiscoverySeen for an undeclared node: err = %v, want ErrNodeDeclarationNotFound", err)
	}
	if _, err := st.GetNodeDeclaration(ctx, "never-declared"); !errors.Is(err, ErrNodeDeclarationNotFound) {
		t.Errorf("a declaration was created as a side effect of RecordNodeDiscoverySeen: err = %v", err)
	}
}

// TestRecordNodeDiscoverySeenUpdatesEvidence proves the presence-recording
// path this method DOES support, for an already-declared node.
func TestRecordNodeDiscoverySeenUpdatesEvidence(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, NodeDeclarationRecord{NodeID: "media-02"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	seenAt := st.now()
	if err := st.RecordNodeDiscoverySeen(ctx, "media-02", "run-7", seenAt); err != nil {
		t.Fatalf("record seen: %v", err)
	}

	rec, err := st.GetNodeDeclaration(ctx, "media-02")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if rec.LastDiscoveryRunID != "run-7" {
		t.Errorf("LastDiscoveryRunID = %q, want %q", rec.LastDiscoveryRunID, "run-7")
	}
	if rec.LastDiscoveredAt == nil || !rec.LastDiscoveredAt.Equal(seenAt) {
		t.Errorf("LastDiscoveredAt = %v, want %v", rec.LastDiscoveredAt, seenAt)
	}
}

// TestFinishingAQuietDiscoveryRunDoesNotTouchNodeDeclarations is F12's
// rename of what this file used to call
// TestDiscoveryNeverDeletesADeclaredNodeThatGoesQuiet — a review finding
// caught that the old name claimed a resilience GUARANTEE ("discovery
// never deletes a declared node that goes quiet") the seam does not
// actually have yet: nothing on the path this test exercises
// (DeclareNode, StartDiscoveryRun, FinishDiscoveryRun) is CAPABLE of
// deleting a node_declarations row at all — [Store.DeleteNodeDeclaration]
// exists, but nothing here calls it. Seam B owns the real version of the
// resilience property the old name claimed — deciding what a completed
// discovery run finding nothing should ever do (if anything) to a
// declaration it did not see, and proving that decision holds under its
// own test.
//
// A later review pass (this project's own "a test's name is a claim"
// rule) noted the RENAMED test still only checked non-deletion — asserting
// len(decls) == 1 — while its own name promised the broader "does not
// touch". A finishDiscoveryRun that wiped every declaration's
// LastDiscoveryRunID/LastDiscoveredAt on every run (never deleting the row
// itself) would have passed this test unchanged. This version asserts the
// full record is byte-for-byte unchanged — LastDiscoveryRunID still empty,
// LastDiscoveredAt still nil — so the test now proves what its name says,
// rather than merely a narrower fact that happens to share the name.
func TestFinishingAQuietDiscoveryRunDoesNotTouchNodeDeclarations(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, NodeDeclarationRecord{NodeID: "media-03"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	before, err := st.GetNodeDeclaration(ctx, "media-03")
	if err != nil {
		t.Fatalf("get before: %v", err)
	}

	// A "discovery run" that finds nothing at all — the powered-off-outside-
	// display-hours case — touches node_declarations not at all.
	if _, err := st.StartDiscoveryRun(ctx, DiscoveryRunRecord{ID: "run-quiet"}); err != nil {
		t.Fatalf("start run: %v", err)
	}
	if err := st.FinishDiscoveryRun(ctx, "run-quiet", true, "", 0); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	decls, err := st.ListNodeDeclarations(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(decls) != 1 {
		t.Fatalf("len(decls) = %d, want 1 — a discovery run finding nothing must not delete the declaration", len(decls))
	}

	after, err := st.GetNodeDeclaration(ctx, "media-03")
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.LastDiscoveryRunID != before.LastDiscoveryRunID {
		t.Errorf("LastDiscoveryRunID changed from %q to %q — a discovery run that never observed this node must not stamp it", before.LastDiscoveryRunID, after.LastDiscoveryRunID)
	}
	if before.LastDiscoveredAt != nil || after.LastDiscoveredAt != nil {
		t.Errorf("LastDiscoveredAt = %v (before) / %v (after), want both nil — this node was never observed", before.LastDiscoveredAt, after.LastDiscoveredAt)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Errorf("UpdatedAt changed from %v to %v — a run that did not touch this declaration must not bump its own update timestamp either", before.UpdatedAt, after.UpdatedAt)
	}
}

// TestDeleteNodeDeclarationRemovesRow proves the explicit-operator-action
// deletion path this seam lands the primitive for (seam B builds the
// confirmed-endpoint behavior on top of it).
func TestDeleteNodeDeclarationRemovesRow(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.DeclareNode(ctx, NodeDeclarationRecord{NodeID: "media-04"}); err != nil {
		t.Fatalf("declare: %v", err)
	}
	if err := st.DeleteNodeDeclaration(ctx, "media-04"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := st.GetNodeDeclaration(ctx, "media-04"); !errors.Is(err, ErrNodeDeclarationNotFound) {
		t.Errorf("declaration still exists after delete: err = %v", err)
	}
}
