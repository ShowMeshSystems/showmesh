package store

import (
	"context"
	"errors"
	"testing"
)

const (
	testCredKind     = "fpp.mqtt"
	testCredObjectID = "default"
	testCredField    = "password"
)

var errRollbackForTest = errors.New("store: deliberate test rollback")

func TestCredentialRoundTrip(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	present, err := st.HasCredential(ctx, testCredKind, testCredObjectID, testCredField)
	if err != nil {
		t.Fatalf("HasCredential: %v", err)
	}
	if present {
		t.Fatalf("HasCredential = true before anything was written, want false")
	}
	if _, present, err := st.GetCredential(ctx, testCredKind, testCredObjectID, testCredField); err != nil || present {
		t.Fatalf("GetCredential = (_, %v, %v), want (_, false, nil) before anything was written", present, err)
	}

	if err := st.SetCredential(ctx, testCredKind, testCredObjectID, testCredField, "s3cret"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	present, err = st.HasCredential(ctx, testCredKind, testCredObjectID, testCredField)
	if err != nil {
		t.Fatalf("HasCredential: %v", err)
	}
	if !present {
		t.Fatalf("HasCredential = false after writing, want true")
	}
	got, present, err := st.GetCredential(ctx, testCredKind, testCredObjectID, testCredField)
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if !present || got != "s3cret" {
		t.Fatalf("GetCredential = (%q, %v), want (\"s3cret\", true)", got, present)
	}

	// SetCredential overwrites (upsert), not append.
	if err := st.SetCredential(ctx, testCredKind, testCredObjectID, testCredField, "rotated"); err != nil {
		t.Fatalf("SetCredential (rotate): %v", err)
	}
	got, present, err = st.GetCredential(ctx, testCredKind, testCredObjectID, testCredField)
	if err != nil || !present || got != "rotated" {
		t.Fatalf("GetCredential after rotation = (%q, %v, %v), want (\"rotated\", true, nil)", got, present, err)
	}

	if err := st.ClearCredential(ctx, testCredKind, testCredObjectID, testCredField); err != nil {
		t.Fatalf("ClearCredential: %v", err)
	}
	present, err = st.HasCredential(ctx, testCredKind, testCredObjectID, testCredField)
	if err != nil {
		t.Fatalf("HasCredential after clear: %v", err)
	}
	if present {
		t.Fatalf("HasCredential = true after clear, want false")
	}

	// Clearing an already-clear credential is not an error.
	if err := st.ClearCredential(ctx, testCredKind, testCredObjectID, testCredField); err != nil {
		t.Fatalf("ClearCredential (already clear): %v", err)
	}
}

// TestCredentialKeyIsScopedByAllThreeColumns proves the table is genuinely
// general-purpose: two different (kind, objectID) pairs, or two different
// fields under the same one, never collide: the shape a second consumer
// (e.g. a converted SHOWMESH_INTEGRATION_BROKERS, per-broker) would rely on
// without this package knowing anything about brokers.
func TestCredentialKeyIsScopedByAllThreeColumns(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if err := st.SetCredential(ctx, "fpp.mqtt", "default", "password", "fpp-secret"); err != nil {
		t.Fatalf("SetCredential fpp.mqtt: %v", err)
	}
	if err := st.SetCredential(ctx, "integration.broker", "home-automation", "password", "broker-secret"); err != nil {
		t.Fatalf("SetCredential integration.broker: %v", err)
	}
	if err := st.SetCredential(ctx, "integration.broker", "home-automation", "username", "broker-user"); err != nil {
		t.Fatalf("SetCredential integration.broker username: %v", err)
	}

	got, _, err := st.GetCredential(ctx, "fpp.mqtt", "default", "password")
	if err != nil || got != "fpp-secret" {
		t.Fatalf("GetCredential fpp.mqtt = (%q, %v), want \"fpp-secret\"", got, err)
	}
	got, _, err = st.GetCredential(ctx, "integration.broker", "home-automation", "password")
	if err != nil || got != "broker-secret" {
		t.Fatalf("GetCredential integration.broker password = (%q, %v), want \"broker-secret\"", got, err)
	}
	got, _, err = st.GetCredential(ctx, "integration.broker", "home-automation", "username")
	if err != nil || got != "broker-user" {
		t.Fatalf("GetCredential integration.broker username = (%q, %v), want \"broker-user\"", got, err)
	}

	// Clearing one key must never touch the others.
	if err := st.ClearCredential(ctx, "integration.broker", "home-automation", "password"); err != nil {
		t.Fatalf("ClearCredential: %v", err)
	}
	if present, err := st.HasCredential(ctx, "integration.broker", "home-automation", "username"); err != nil || !present {
		t.Fatalf("HasCredential integration.broker username = (%v, %v), want (true, nil) after clearing a sibling field", present, err)
	}
	if present, err := st.HasCredential(ctx, "fpp.mqtt", "default", "password"); err != nil || !present {
		t.Fatalf("HasCredential fpp.mqtt = (%v, %v), want (true, nil) after clearing an unrelated kind", present, err)
	}
}

func TestCredentialTxForm(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		return tx.SetCredential(ctx, testCredKind, testCredObjectID, testCredField, "in-tx")
	}); err != nil {
		t.Fatalf("InTx SetCredential: %v", err)
	}
	got, present, err := st.GetCredential(ctx, testCredKind, testCredObjectID, testCredField)
	if err != nil || !present || got != "in-tx" {
		t.Fatalf("GetCredential after Tx write = (%q, %v, %v), want (\"in-tx\", true, nil)", got, present, err)
	}

	// A Tx write that never commits (the closure returns an error) must
	// leave nothing behind: the same all-or-nothing guarantee every other
	// Tx-form writer in this package holds.
	if err := st.InTx(ctx, func(ctx context.Context, tx *Tx) error {
		if err := tx.SetCredential(ctx, testCredKind, testCredObjectID, "rolled_back_field", "never-committed"); err != nil {
			return err
		}
		return errRollbackForTest
	}); err == nil {
		t.Fatalf("InTx = nil error, want the closure's own error to roll back the transaction")
	}
	if present, err := st.HasCredential(ctx, testCredKind, testCredObjectID, "rolled_back_field"); err != nil || present {
		t.Fatalf("HasCredential = (%v, %v), want (false, nil): a rolled-back Tx write must persist nothing", present, err)
	}
}
