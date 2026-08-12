package identity

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file proves Step 7 seam 0's two acceptance criteria that require an
// audit-store failure to be REAL rather than mocked: a SQLite trigger is
// installed on the live database (the exact mechanism BUILD-PLAN's Step 7
// spec names), through a second raw connection to the same on-disk file —
// package identity has no other way to reach *store.Store's own
// unexported *sql.DB, and this is deliberate: it proves the failure the
// way an actual disk-full or corrupted-index condition would surface,
// against the real store.Store.InTx / store.Tx.AppendAuditEntry path, not
// against a hand-rolled fake that could pass or fail independently of
// whether the real code is correct.

// installFailAuditTrigger opens a second connection to storeDir's SQLite
// file and creates a trigger that aborts every INSERT into audit_log —
// exactly the trigger BUILD-PLAN's Step 7 spec specifies. The trigger is
// schema, not connection state, so it takes effect for every subsequent
// connection (including the *store.Store under test) once created, and
// this helper closes its own connection immediately afterward.
func installFailAuditTrigger(t *testing.T, storeDir string) {
	t.Helper()
	dbPath := filepath.Join(storeDir, "showmesh.db")
	raw, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw connection to %q: %v", dbPath, err)
	}
	defer func() { _ = raw.Close() }()

	_, err = raw.ExecContext(context.Background(), `
		CREATE TRIGGER fail_audit BEFORE INSERT ON audit_log
		BEGIN SELECT RAISE(ABORT, 'injected audit failure'); END;
	`)
	if err != nil {
		t.Fatalf("install fail_audit trigger: %v", err)
	}
}

// newServiceWithOwnStoreDir is [newTestService], but returns the store's
// OWN directory (where showmesh.db actually lives) rather than only the
// identity package's bootstrap-file dataDir — needed here, and nowhere
// else in this package's existing suite, to open the second raw
// connection [installFailAuditTrigger] requires.
func newServiceWithOwnStoreDir(t *testing.T, clock *fakeClock) (svc Service, storeDir, dataDir string) {
	t.Helper()
	dir := t.TempDir()
	now := time.Now
	if clock != nil {
		now = clock.now
	}
	storeDir = filepath.Join(dir, "db")
	st, err := store.Open(context.Background(), storeDir, nil, store.WithClock(now))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dataDir = filepath.Join(dir, "data")
	svc = NewService(st, now, dataDir)
	return svc, storeDir, dataDir
}

// TestClaimBootstrapWithFailingAuditRefusesAndCreatesNoPrincipal is
// acceptance criterion 1, the criterion this whole seam exists for: "with
// [the fail_audit] trigger installed, a bootstrap claim is refused, and no
// principal exists afterwards." This test is not merely checking that
// ClaimBootstrap always returns some error for some unrelated reason —
// [TestClaimBootstrapWithFailingAuditWithoutTriggerSucceeds] below is the
// control that proves it: identical setup, no trigger installed, where a
// principal MUST exist afterward. (An earlier version of this comment
// pointed at "this file's git history" as evidence the behavior asserted
// here was confirmed broken-then-fixed; this file is untracked, so there
// is no history to cite — the control test below is the honest,
// reproducible version of that same check.)
func TestClaimBootstrapWithFailingAuditRefusesAndCreatesNoPrincipal(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-12T00:00:00Z")}
	svc, storeDir, dataDir := newServiceWithOwnStoreDir(t, clock)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}
	code := readBootstrapCode(t, dataDir)

	installFailAuditTrigger(t, storeDir)

	_, err := svc.ClaimBootstrap(ctx, code, "first-admin", "a-strong-password-1", "laptop", "203.0.113.5", FormPassword, clock.now())
	if err == nil {
		t.Fatalf("ClaimBootstrap succeeded with the audit store failing, want an error")
	}
	if !errors.Is(err, ErrAuditWrite) {
		t.Errorf("error = %v, want it to wrap ErrAuditWrite", err)
	}

	has, herr := svc.HasAnyPrincipal(ctx)
	if herr != nil {
		t.Fatalf("has any principal: %v", herr)
	}
	if has {
		t.Fatalf("a principal exists after a bootstrap claim whose audit write failed — same-transaction rule violated")
	}

	// The bootstrap code itself must not have been consumed either: the
	// whole point of atomicity is that NOTHING from this attempt landed,
	// so the operator can simply try again once the disk/audit problem is
	// fixed. (The fail_audit trigger only blocks audit_log; the bootstrap
	// row's own claimed_at column is a different table and rolls back
	// with everything else in the same transaction.)
	code2 := readBootstrapCode(t, dataDir)
	if code2 != code {
		t.Errorf("bootstrap code changed after a failed claim: %q -> %q, want it unchanged and still claimable", code, code2)
	}
}

// TestCreateSessionWithFailingAuditRefusesAndCreatesNoSession is
// acceptance criterion 2: "with that trigger installed, a login is
// refused and no session row exists afterwards." This exercises
// [Service.CreateSession] directly (the shared atomic path both
// POST /api/v1/session and POST /api/v1/bootstrap's post-claim login go
// through), since driving it through the full HTTP handler is
// internal/coordinator/api's own test suite's job, not this package's.
func TestCreateSessionWithFailingAuditRefusesAndCreatesNoSession(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-12T00:00:00Z")}
	svc, storeDir, _ := newServiceWithOwnStoreDir(t, clock)
	ctx := context.Background()

	p, err := svc.CreatePrincipal(ctx, "operator-1", KindHuman, RoleOperator, "a-strong-password-1")
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}

	installFailAuditTrigger(t, storeDir)

	_, _, err = svc.CreateSession(ctx, p.ID, p.Name, "phone", "203.0.113.5", clock.now())
	if err == nil {
		t.Fatalf("CreateSession succeeded with the audit store failing, want an error")
	}
	if !errors.Is(err, ErrAuditWrite) {
		t.Errorf("error = %v, want it to wrap ErrAuditWrite", err)
	}

	sessions, lerr := svc.ListSessions(ctx, p.ID)
	if lerr != nil {
		t.Fatalf("list sessions: %v", lerr)
	}
	if len(sessions) != 0 {
		t.Fatalf("session(s) exist after a login whose audit write failed: %+v — same-transaction rule violated (this is the exact orphaned-row defect ADR-024 and session.go used to document)", sessions)
	}
}

// TestCreateSessionInsideAuditedWriteClosurePanicsRatherThanHanging is F1's
// HIGH finding, reproduced against the REALISTIC path rather than a
// contrived nested store.InTx call: a later seam composing an identity
// write with a config or discovery write inside one AuditedWrite closure —
//
//	svc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
//	    svc.CreateSession(ctx, ...)   // hangs forever, pre-fix
//	    ...
//	})
//
// — because AuditedWrite's fn already runs inside store.Store.InTx, and
// CreateSession calls s.AuditedWrite (and therefore s.st.InTx) again,
// nested. Before store.Store.InTx guarded against a nested call (see
// store/tx.go's guardNotInTx), this hung on the single-connection pool
// with no error and no log line — reproduced directly by a reviewer. It
// must now panic instead, immediately, naming Store.InTx. A
// context.WithTimeout bounds this test so a regression back to the
// hanging behavior fails fast rather than by exhausting the whole test
// binary's own timeout with no attribution.
func TestCreateSessionInsideAuditedWriteClosurePanicsRatherThanHanging(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-12T00:00:00Z")}
	svc, _, _ := newServiceWithOwnStoreDir(t, clock)
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	var panicMsg string
	func() {
		defer func() {
			if r := recover(); r != nil {
				panicMsg, _ = r.(string)
			}
		}()
		_ = svc.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (AuditEntry, error) {
			// CreateSession routes through AuditedWrite -> store.Store.InTx
			// again, nested inside the outer AuditedWrite's own InTx call —
			// exactly the shape this test exists to catch.
			_, _, err := svc.CreateSession(ctx, "some-principal-id", "some-principal-name", "device", "", clock.now())
			return AuditEntry{}, err
		})
	}()

	if ctx.Err() != nil {
		t.Fatalf("CreateSession called from inside an AuditedWrite closure hung until the test's own timeout instead of panicking (ctx.Err() = %v)", ctx.Err())
	}
	if panicMsg == "" {
		t.Fatalf("CreateSession called from inside an AuditedWrite closure did not panic")
	}
	if !strings.Contains(panicMsg, "Store.InTx") {
		t.Errorf("panic message = %q, want it to name Store.InTx", panicMsg)
	}
}

// TestClaimBootstrapWithFailingAuditWithoutTriggerSucceeds is the control
// for TestClaimBootstrapWithFailingAuditRefusesAndCreatesNoPrincipal:
// identical setup, no trigger installed, proving the earlier test's
// failure really does come from the trigger and not from some other
// defect in this test's own scaffolding — see this package's control test
// pattern generally (e.g. store.TestGuardNotInTxAllowsOrdinaryContext,
// which proves the identical guard does NOT fire on an ordinary context),
// applied here to this test itself.
func TestClaimBootstrapWithFailingAuditWithoutTriggerSucceeds(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-12T00:00:00Z")}
	svc, _, dataDir := newServiceWithOwnStoreDir(t, clock)
	ctx := context.Background()

	if err := svc.EnsureBootstrap(ctx); err != nil {
		t.Fatalf("ensure bootstrap: %v", err)
	}
	code := readBootstrapCode(t, dataDir)

	admin, err := svc.ClaimBootstrap(ctx, code, "first-admin", "a-strong-password-1", "laptop", "203.0.113.5", FormPassword, clock.now())
	if err != nil {
		t.Fatalf("ClaimBootstrap without the trigger installed: %v", err)
	}
	if admin.Name != "first-admin" {
		t.Errorf("Name = %q, want %q", admin.Name, "first-admin")
	}

	entries, err := svc.ListAudit(ctx, 0, 10)
	if err != nil {
		t.Fatalf("list audit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "bootstrap.claim" && e.Target == admin.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("no bootstrap.claim audit entry found for principal %q among %d entries", admin.ID, len(entries))
	}
}
