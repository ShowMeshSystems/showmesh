package identity

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file proves [Service.AuditWriteStatus] (docs/build/IDENTIFIER-REGISTER.md's
// coordinator.audit.store.state/coordinator.audit.store.reason pair, ADR-024
// decision 11's 2026-08-26 amendment): a live signal, updated only by
// WriteAudit's and AuditedWrite's own real append outcomes, distinct from a
// per-write attributionDegraded flag because this is meant to answer "is
// audit down right now" without any caller having invoked an action at all.

// TestAuditWriteStatusStartsUnknownAndTracksWriteAudit proves the WriteAudit
// half: unknown before any attempt (ADR-011's "no evidence is never
// reported as healthy", applied here), usable after a real successful
// append, and unusable with a non-empty reason once the real fail_audit
// trigger is installed and a subsequent append genuinely fails.
func TestAuditWriteStatusStartsUnknownAndTracksWriteAudit(t *testing.T) {
	svc, storeDir, _ := newServiceWithOwnStoreDir(t, nil)

	if state, reason := svc.AuditWriteStatus(); state != "unknown" || reason != "" {
		t.Fatalf("initial AuditWriteStatus = (%q, %q), want (\"unknown\", \"\")", state, reason)
	}

	if err := svc.WriteAudit(context.Background(), AuditEntry{
		Timestamp: time.Now(), PrincipalID: "p1", PrincipalName: "principal-1",
		Action: "test.action", Target: "t1", Kind: AuditDispatch,
	}); err != nil {
		t.Fatalf("WriteAudit (healthy): %v", err)
	}
	if state, reason := svc.AuditWriteStatus(); state != "usable" || reason != "" {
		t.Fatalf("AuditWriteStatus after a successful append = (%q, %q), want (\"usable\", \"\")", state, reason)
	}

	installFailAuditTrigger(t, storeDir)

	if err := svc.WriteAudit(context.Background(), AuditEntry{
		Timestamp: time.Now(), PrincipalID: "p1", PrincipalName: "principal-1",
		Action: "test.action", Target: "t2", Kind: AuditDispatch,
	}); err == nil {
		t.Fatal("WriteAudit succeeded with the fail_audit trigger installed, want an error")
	}
	state, reason := svc.AuditWriteStatus()
	if state != "unusable" {
		t.Errorf("state = %q, want \"unusable\"", state)
	}
	if reason == "" {
		t.Error("reason = \"\", want a non-empty explanation of the append failure")
	}
}

// TestAuditWriteStatusTracksAuditedWrite proves the SAME signal updates from
// [Service.AuditedWrite]'s own append step, not only from WriteAudit: the
// pre-dispatch path every one of the three corrected request paths uses.
func TestAuditWriteStatusTracksAuditedWrite(t *testing.T) {
	svc, storeDir, _ := newServiceWithOwnStoreDir(t, nil)

	if err := svc.AuditedWrite(context.Background(), func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
		return AuditEntry{
			Timestamp: time.Now(), PrincipalID: "p1", PrincipalName: "principal-1",
			Action: "test.action", Target: "t1", Kind: AuditDispatch,
		}, nil
	}); err != nil {
		t.Fatalf("AuditedWrite (healthy): %v", err)
	}
	if state, reason := svc.AuditWriteStatus(); state != "usable" || reason != "" {
		t.Fatalf("AuditWriteStatus after a successful AuditedWrite = (%q, %q), want (\"usable\", \"\")", state, reason)
	}

	installFailAuditTrigger(t, storeDir)

	err := svc.AuditedWrite(context.Background(), func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
		return AuditEntry{
			Timestamp: time.Now(), PrincipalID: "p1", PrincipalName: "principal-1",
			Action: "test.action", Target: "t2", Kind: AuditDispatch,
		}, nil
	})
	if err == nil {
		t.Fatal("AuditedWrite succeeded with the fail_audit trigger installed, want an error")
	}
	state, reason := svc.AuditWriteStatus()
	if state != "unusable" {
		t.Errorf("state = %q, want \"unusable\"", state)
	}
	if reason == "" {
		t.Error("reason = \"\", want a non-empty explanation of the append failure")
	}
}

// TestAuditWriteStatusIgnoresFnBusinessError proves recordAuditWriteOutcome
// is never reached for AuditedWrite's fn's OWN error (a business-logic
// failure that never touched audit_log at all), only for the append
// step's own outcome. Conflating the two would report "audit unusable" for
// an ordinary duplicate-key rejection with a perfectly healthy audit store.
func TestAuditWriteStatusIgnoresFnBusinessError(t *testing.T) {
	svc, _, _ := newServiceWithOwnStoreDir(t, nil)

	fnErr := context.Canceled // any sentinel; content is irrelevant here.
	err := svc.AuditedWrite(context.Background(), func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
		return AuditEntry{}, fnErr
	})
	if err == nil {
		t.Fatal("AuditedWrite did not propagate fn's own error")
	}
	if state, reason := svc.AuditWriteStatus(); state != "unknown" || reason != "" {
		t.Errorf("AuditWriteStatus after fn's own business error = (%q, %q), want (\"unknown\", \"\") "+
			"(the append was never attempted, so the audit-store signal must not move)", state, reason)
	}
}
