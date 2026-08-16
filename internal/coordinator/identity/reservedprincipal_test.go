package identity

import (
	"context"
	"errors"
	"testing"
)

// This file is Track D seam D-3a's criterion 13 at the identity.Service
// layer — see internal/coordinator/store/reservedprincipal_test.go for
// the store layer's own half of build contract §1.2's enumerated survey.

func TestServiceCreatePrincipalRefusesReservedName(t *testing.T) {
	svc, _, _ := newTestService(t, nil)
	_, err := svc.CreatePrincipal(context.Background(), ReservedResolumeRecoveryPrincipalID, KindHuman, RoleAdmin, "somepassword")
	if !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("CreatePrincipal(reserved name) error = %v, want ErrReservedPrincipal", err)
	}
}

func TestServiceSetDisabledRefusesReservedID(t *testing.T) {
	svc, _, _ := newTestService(t, nil)
	if _, err := svc.EnsureReservedRecoveryPrincipal(context.Background()); err != nil {
		t.Fatalf("EnsureReservedRecoveryPrincipal: %v", err)
	}
	if _, err := svc.SetDisabled(context.Background(), ReservedResolumeRecoveryPrincipalID, true); !errors.Is(err, ErrReservedPrincipal) {
		t.Errorf("SetDisabled(true) error = %v, want ErrReservedPrincipal", err)
	}
	if _, err := svc.SetDisabled(context.Background(), ReservedResolumeRecoveryPrincipalID, false); !errors.Is(err, ErrReservedPrincipal) {
		t.Errorf("SetDisabled(false) error = %v, want ErrReservedPrincipal", err)
	}
}

func TestServiceSetRoleRefusesReservedID(t *testing.T) {
	svc, _, _ := newTestService(t, nil)
	if _, err := svc.EnsureReservedRecoveryPrincipal(context.Background()); err != nil {
		t.Fatalf("EnsureReservedRecoveryPrincipal: %v", err)
	}
	if _, err := svc.SetRole(context.Background(), ReservedResolumeRecoveryPrincipalID, RoleAdmin); !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("SetRole error = %v, want ErrReservedPrincipal", err)
	}
}

func TestServiceSetPasswordRefusesReservedID(t *testing.T) {
	svc, _, _ := newTestService(t, nil)
	if _, err := svc.EnsureReservedRecoveryPrincipal(context.Background()); err != nil {
		t.Fatalf("EnsureReservedRecoveryPrincipal: %v", err)
	}
	if _, err := svc.SetPassword(context.Background(), ReservedResolumeRecoveryPrincipalID, "newpassword"); !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("SetPassword error = %v, want ErrReservedPrincipal", err)
	}
}

func TestServiceClaimBootstrapRefusesReservedName(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-01-01T00:00:00Z")}
	svc, _, _ := newTestService(t, clock)
	if err := svc.EnsureBootstrap(context.Background()); err != nil {
		t.Fatalf("EnsureBootstrap: %v", err)
	}
	_, err := svc.ClaimBootstrap(context.Background(), "whatever-code", ReservedResolumeRecoveryPrincipalID, "password123", "laptop", "127.0.0.1", FormPassword, clock.now())
	if !errors.Is(err, ErrReservedPrincipal) {
		t.Fatalf("ClaimBootstrap(reserved name) error = %v, want ErrReservedPrincipal (checked before the code is even validated)", err)
	}
}

// TestEnsureReservedRecoveryPrincipalIsIdempotent: called twice, the
// second call finds the row already there rather than erroring — the
// startup path this is meant for calls it on every boot.
func TestEnsureReservedRecoveryPrincipalIsIdempotent(t *testing.T) {
	svc, _, _ := newTestService(t, nil)
	first, err := svc.EnsureReservedRecoveryPrincipal(context.Background())
	if err != nil {
		t.Fatalf("first EnsureReservedRecoveryPrincipal: %v", err)
	}
	second, err := svc.EnsureReservedRecoveryPrincipal(context.Background())
	if err != nil {
		t.Fatalf("second EnsureReservedRecoveryPrincipal: %v", err)
	}
	if first.ID != second.ID || first.ID != ReservedResolumeRecoveryPrincipalID {
		t.Fatalf("EnsureReservedRecoveryPrincipal ids = %q, %q, want both %q", first.ID, second.ID, ReservedResolumeRecoveryPrincipalID)
	}
	if second.Role != RoleRecovery {
		t.Errorf("Role = %q, want %q", second.Role, RoleRecovery)
	}
}

// TestReservedRecoveryPrincipalIsMarkedInListings is build contract
// §1.2's "visible wherever principals are listed... marked as built-in".
func TestReservedRecoveryPrincipalIsMarkedInListings(t *testing.T) {
	svc, _, _ := newTestService(t, nil)
	if _, err := svc.EnsureReservedRecoveryPrincipal(context.Background()); err != nil {
		t.Fatalf("EnsureReservedRecoveryPrincipal: %v", err)
	}
	principals, err := svc.ListPrincipals(context.Background())
	if err != nil {
		t.Fatalf("ListPrincipals: %v", err)
	}
	found := false
	for _, p := range principals {
		if p.ID == ReservedResolumeRecoveryPrincipalID {
			found = true
			if !p.Reserved {
				t.Errorf("Principal.Reserved = false for %q, want true", p.ID)
			}
		}
	}
	if !found {
		t.Fatal("reserved recovery principal not found in ListPrincipals")
	}
}

// TestRoleRecoveryHoldsExactlyResolumeAction pins build contract §1.2's
// own exact scope grant: "exactly resolume:action and nothing wider".
func TestRoleRecoveryHoldsExactlyResolumeAction(t *testing.T) {
	scopes := RoleRecovery.Scopes()
	if len(scopes) != 1 || scopes[0] != ScopeResolumeAction {
		t.Fatalf("RoleRecovery.Scopes() = %v, want exactly [%q]", scopes, ScopeResolumeAction)
	}
}
