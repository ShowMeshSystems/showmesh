package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

// This file is Track D seam D-3a's criterion 13: the built-in
// automatic-recovery principal cannot be deleted, disabled, or have its
// scope removed through any store-level path. See identity/reservedprincipal_test.go
// for the identity.Service-level half of the same survey (build contract
// §1.2's enumerated list).

// TestCreatePrincipalRefusesReservedID is one path per criterion 13: a
// user-created principal claiming the reserved id, through the ordinary
// creation path, is refused.
func TestCreatePrincipalRefusesReservedID(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.CreatePrincipal(context.Background(), PrincipalRecord{
		ID: ReservedPrincipalID, Name: ReservedPrincipalID, Kind: "human", Role: "admin",
	})
	if !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("CreatePrincipal(reserved id) error = %v, want ErrReservedPrincipal", err)
	}
}

// TestCreatePrincipalRefusesReservedName is the name-only collision: a
// different id, but the reserved name.
func TestCreatePrincipalRefusesReservedName(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.CreatePrincipal(context.Background(), PrincipalRecord{
		ID: "some-other-id", Name: ReservedPrincipalID, Kind: "human", Role: "admin",
	})
	if !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("CreatePrincipal(reserved name) error = %v, want ErrReservedPrincipal", err)
	}
}

// TestSetPrincipalDisabledRefusesReservedID covers both directions
// (disabling AND re-enabling): the reserved principal's disabled flag is
// never touched through this path.
func TestSetPrincipalDisabledRefusesReservedID(t *testing.T) {
	st := openTestStore(t, nil)
	ensureReservedPrincipalForTest(t, st)

	if _, err := st.SetPrincipalDisabled(context.Background(), ReservedPrincipalID, true); !errors.Is(err, ErrReservedPrincipal) {
		t.Errorf("SetPrincipalDisabled(true) error = %v, want ErrReservedPrincipal", err)
	}
	if _, err := st.SetPrincipalDisabled(context.Background(), ReservedPrincipalID, false); !errors.Is(err, ErrReservedPrincipal) {
		t.Errorf("SetPrincipalDisabled(false) error = %v, want ErrReservedPrincipal", err)
	}
}

func TestSetPrincipalRoleRefusesReservedID(t *testing.T) {
	st := openTestStore(t, nil)
	ensureReservedPrincipalForTest(t, st)

	if _, err := st.SetPrincipalRole(context.Background(), ReservedPrincipalID, "admin"); !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("SetPrincipalRole error = %v, want ErrReservedPrincipal", err)
	}
}

func TestSetPrincipalPasswordHashRefusesReservedID(t *testing.T) {
	st := openTestStore(t, nil)
	ensureReservedPrincipalForTest(t, st)

	if _, err := st.SetPrincipalPasswordHash(context.Background(), ReservedPrincipalID, "somehash"); !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("SetPrincipalPasswordHash error = %v, want ErrReservedPrincipal", err)
	}
}

// TestClaimBootstrapAndCreatePrincipalRefusesReservedName: the bootstrap
// path (which creates the FIRST admin) must also never be able to mint
// the reserved principal.
func TestClaimBootstrapAndCreatePrincipalRefusesReservedName(t *testing.T) {
	st := openTestStore(t, nil)
	if _, err := st.PutBootstrap(context.Background(), BootstrapRecord{CodeDigest: "x", ExpiresAt: mustTime(t, "2099-01-01T00:00:00Z")}); err != nil {
		t.Fatalf("PutBootstrap: %v", err)
	}
	_, err := st.ClaimBootstrapAndCreatePrincipal(context.Background(), PrincipalRecord{
		ID: ReservedPrincipalID, Name: ReservedPrincipalID, Kind: "human", Role: "admin",
	})
	if !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("ClaimBootstrapAndCreatePrincipal error = %v, want ErrReservedPrincipal", err)
	}
}

// TestNoDeletePrincipalMethodExists is criterion 13's "there is no
// DeletePrincipal anywhere in this repository today... a test that fails
// if a DeletePrincipal method appears without a reserved-name refusal
// alongside it" (build contract §1.2) — a path that does not exist
// satisfies "cannot be deleted" by construction, so this test's job is
// making sure nobody adds one silently, without ALSO reading this
// comment and this file's own sibling tests.
func TestNoDeletePrincipalMethodExists(t *testing.T) {
	typ := reflect.TypeOf(&Store{})
	if _, ok := typ.MethodByName("DeletePrincipal"); ok {
		t.Fatal("Store.DeletePrincipal now exists — build contract §1.2 requires either it refuses the reserved " +
			"principal (see this file's own tests for the pattern) or a new test asserting that refusal is added " +
			"before this one may be relaxed")
	}
}

// ensureReservedPrincipalForTest seeds the reserved principal directly
// (bypassing the guard under test) via EnsureReservedPrincipal, so a
// SetX test has a real row to attempt its refused mutation against.
func ensureReservedPrincipalForTest(t *testing.T, st *Store) {
	t.Helper()
	if _, _, err := st.EnsureReservedPrincipal(context.Background(), PrincipalRecord{
		ID: ReservedPrincipalID, Name: ReservedPrincipalID, Kind: "machine", Role: "recovery",
	}); err != nil {
		t.Fatalf("EnsureReservedPrincipal: %v", err)
	}
}
