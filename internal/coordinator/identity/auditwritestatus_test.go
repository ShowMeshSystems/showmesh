package identity

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file proves [Service.AuditWriteStatus] (docs/build/IDENTIFIER-REGISTER.md's
// coordinator.audit.store.state/coordinator.audit.store.reason pair, ADR-024
// decision 11's 2026-08-26 amendment): a signal computed fresh on every
// call via a real probe write, distinct from a per-command
// attributionDegraded flag because this is meant to answer "is audit down
// right now" without any caller having invoked an action at all, and
// answerable even when no action has ever been invoked.

// TestAuditWriteStatusProbesFreshWithNoPriorTraffic is the direct proof a
// review finding on this task's own change asked for: an audit store that
// fails with NO real command traffic having happened yet (the "store
// fails at 02:00 on an idle coordinator" case) must still be reported
// correctly, because this reads live off a real probe write, never off a
// latch that only real traffic would have updated.
func TestAuditWriteStatusProbesFreshWithNoPriorTraffic(t *testing.T) {
	svc, storeDir, _ := newServiceWithOwnStoreDir(t, nil)

	if state, reason := svc.AuditWriteStatus(context.Background()); state != "usable" || reason != "" {
		t.Fatalf("AuditWriteStatus on a healthy, never-written-to store = (%q, %q), want (\"usable\", \"\")", state, reason)
	}

	// Installed with NO WriteAudit/AuditedWrite call ever having run
	// against this Service - there is no prior latch value to have
	// carried this fact forward.
	installFailAuditTrigger(t, storeDir)

	state, reason := svc.AuditWriteStatus(context.Background())
	if state != "unusable" {
		t.Errorf("state = %q, want \"unusable\" (the probe write itself must have just failed)", state)
	}
	if reason == "" {
		t.Error("reason = \"\", want a non-empty explanation of the probe failure")
	}
}

// TestAuditWriteStatusProbeNeverLeavesARow proves the probe is genuinely
// non-destructive: whether it succeeds or fails, it must never leave a
// row behind in the append-only audit log it is not this Service's place
// to invent.
func TestAuditWriteStatusProbeNeverLeavesARow(t *testing.T) {
	svc, _, _ := newServiceWithOwnStoreDir(t, nil)

	if _, reason := svc.AuditWriteStatus(context.Background()); reason != "" {
		t.Fatalf("test setup: probe reported unusable (%q) on a healthy store", reason)
	}
	entries, err := svc.ListAudit(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("audit_log has %d entries after a probe, want 0: the probe must always roll back", len(entries))
	}
}

// TestAuditWriteStatusTracksAuditedWrite proves the SAME latch WriteAudit/
// AuditedWrite already maintain stays consistent with what AuditWriteStatus
// itself would report right now, for a caller checking immediately after
// real command traffic.
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
	if state, reason := svc.AuditWriteStatus(context.Background()); state != "usable" || reason != "" {
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
	state, reason := svc.AuditWriteStatus(context.Background())
	if state != "unusable" {
		t.Errorf("state = %q, want \"unusable\"", state)
	}
	if reason == "" {
		t.Error("reason = \"\", want a non-empty explanation")
	}
}

// TestClassifyAuditedWriteResultTreatsCommitFailureAsAuditWriteFailure
// proves the OTHER real failure mode this task's review found: an
// unwritable audit_log is not the only way a caller's
// errors.Is(err, identity.ErrAuditWrite) check must fire. A commit
// failure after fn AND the append both already succeeded inside the
// transaction (store.ErrCommitFailed, e.g. a disk that fills between the
// last write and the fsync COMMIT performs) is decision 11's identical
// "could not be attributed durably" fact, caught one step later, and
// every one of ADR-024 decision 11's own callers (actioninvoke.go and
// its siblings) checks for ErrAuditWrite specifically, not for
// store.ErrCommitFailed, which they have no reason to import.
// store.InTx's own TestInTxCommitFailureIsWrappedInErrCommitFailed
// (store package) proves ErrCommitFailed is what a REAL commit failure
// produces; this test proves classifyAuditedWriteResult's own mapping of
// that real sentinel is correct, without needing to reproduce the
// deferred-foreign-key mechanics that store package test already owns
// (store.Tx exposes no raw SQL access outside its own package for a
// second real reproduction here to use). Checked against the LATCH's own
// fields directly, not through AuditWriteStatus: that method now always
// re-probes a real (here, healthy) store, which would immediately
// overwrite whatever this synthetic error latched.
func TestClassifyAuditedWriteResultTreatsCommitFailureAsAuditWriteFailure(t *testing.T) {
	iface, _, _ := newServiceWithOwnStoreDir(t, nil)
	s := iface.(*svc)

	commitErr := fmt.Errorf("%w: %v", store.ErrCommitFailed, errors.New("disk full"))
	got := s.classifyAuditedWriteResult(commitErr)
	if !errors.Is(got, ErrAuditWrite) {
		t.Fatalf("classifyAuditedWriteResult(%v) = %v, want it wrapped in ErrAuditWrite", commitErr, got)
	}
	if !errors.Is(got, store.ErrCommitFailed) {
		t.Errorf("classifyAuditedWriteResult(%v) = %v, want the original store.ErrCommitFailed still reachable via errors.Is", commitErr, got)
	}
	s.auditWriteMu.Lock()
	state, reason := s.auditWriteState, s.auditWriteReason
	s.auditWriteMu.Unlock()
	if state != "unusable" {
		t.Errorf("latch state = %q, want \"unusable\" (a commit failure is exactly as unusable as an append failure)", state)
	}
	if reason == "" {
		t.Error("latch reason = \"\", want a non-empty explanation")
	}
}

// TestClassifyAuditedWriteResultLatchesSuccessOnlyOnNilError proves the
// OTHER half of the same fix: the "usable" latch must never be set from
// inside AuditedWrite's own InTx closure, before COMMIT has actually run.
// classifyAuditedWriteResult is the ONLY place that ever calls
// recordAuditWriteOutcome(nil) now, and it only runs once InTx has
// already returned, which is only after a real COMMIT succeeded. Checked
// against the latch's own fields directly, for the identical reason
// given above.
func TestClassifyAuditedWriteResultLatchesSuccessOnlyOnNilError(t *testing.T) {
	iface, _, _ := newServiceWithOwnStoreDir(t, nil)
	s := iface.(*svc)

	// Seed a prior failure so the latch starts somewhere other than its
	// own zero value - if success were latched independent of the actual
	// argument, this would still (wrongly) read "usable" afterward.
	s.recordAuditWriteOutcome(errors.New("seed failure"))
	s.auditWriteMu.Lock()
	seeded := s.auditWriteState
	s.auditWriteMu.Unlock()
	if seeded != "unusable" {
		t.Fatalf("test setup: latch state = %q after seeding a failure, want \"unusable\"", seeded)
	}

	if err := s.classifyAuditedWriteResult(nil); err != nil {
		t.Fatalf("classifyAuditedWriteResult(nil) = %v, want nil", err)
	}
	s.auditWriteMu.Lock()
	state, reason := s.auditWriteState, s.auditWriteReason
	s.auditWriteMu.Unlock()
	if state != "usable" || reason != "" {
		t.Errorf("latch after classifyAuditedWriteResult(nil) = (%q, %q), want (\"usable\", \"\")", state, reason)
	}
}

// TestAuditWriteStatusIgnoresFnBusinessError proves recordAuditWriteOutcome
// is never reached for AuditedWrite's fn's OWN error (a business-logic
// failure that never touched audit_log at all): the latch's own fields
// stay at their untouched zero value right after the call (checked
// directly, before AuditWriteStatus's own live probe would set them to
// something else regardless), and a subsequent AuditWriteStatus call
// still reports the live probe's own healthy answer, never a stale
// "unusable" manufactured from an unrelated business error.
func TestAuditWriteStatusIgnoresFnBusinessError(t *testing.T) {
	iface, _, _ := newServiceWithOwnStoreDir(t, nil)
	s := iface.(*svc)

	fnErr := context.Canceled // any sentinel; content is irrelevant here.
	err := s.AuditedWrite(context.Background(), func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
		return AuditEntry{}, fnErr
	})
	if err == nil {
		t.Fatal("AuditedWrite did not propagate fn's own error")
	}
	s.auditWriteMu.Lock()
	state, reason := s.auditWriteState, s.auditWriteReason
	s.auditWriteMu.Unlock()
	if state != "" || reason != "" {
		t.Errorf("latch after fn's own business error = (%q, %q), want the untouched zero value (\"\", \"\")", state, reason)
	}

	if state, reason := s.AuditWriteStatus(context.Background()); state != "usable" || reason != "" {
		t.Errorf("AuditWriteStatus after fn's own business error = (%q, %q), want (\"usable\", \"\") "+
			"(the store itself is healthy; fn's own error must not manufacture a false \"unusable\")", state, reason)
	}
}
