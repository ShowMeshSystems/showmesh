package store

import (
	"context"
	"database/sql"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestProbeAuditWriteFailsAgainstARealReadOnlyDatabaseFile is review round
// 5's own named requirement, added on request after the round accepted
// finding 2's fix: the closed-store test (auditstoreunreachable_test.go,
// internal/coordinator/api) fails at BeginTx, earlier than the INSERT, so
// it cannot tell a real write-probe apart from a degenerate one (a PRAGMA
// check, a scratch table, a no-op transaction) that would pass every
// other test in this PR while no longer detecting a full disk.
//
// Opens a SECOND connection to the same already-migrated database file
// with SQLite's own mode=ro URI parameter, rather than chmod-ing the
// file: CI runs this suite as root inside a container, where chmod's
// 0444 is enforced against every OTHER uid and is a no-op against the
// one actually running the test, so a chmod-based version of this test
// passed locally and passed vacuously in CI, proving nothing there. This
// version does not depend on OS permission bits or uid at all - mode=ro
// is a connection-level restriction SQLite itself enforces
// (SQLITE_OPEN_READONLY), so root refusing to be stopped by chmod is not
// a way around it. The failure still lands exactly on the real INSERT
// (confirmed locally: "attempt to write a readonly database"), so a
// degenerate probe still fails this test the same way a chmod-based one
// would have on a non-root platform. If mode=ro ever stops producing
// that failure on some platform or SQLite build, the right response is
// to say so here, not to force this test green some other way.
func TestProbeAuditWriteFailsAgainstARealReadOnlyDatabaseFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	dbPath := filepath.Join(dir, dbFileName)
	roDSN := "file:" + dbPath + "?" + url.Values{
		"mode":          {"ro"},
		"_journal_mode": {"WAL"},
		"_foreign_keys": {"on"},
		"_busy_timeout": {"5000"},
	}.Encode()
	roDB, err := sql.Open("sqlite", roDSN)
	if err != nil {
		t.Fatalf("sql.Open mode=ro: %v", err)
	}
	t.Cleanup(func() { _ = roDB.Close() })
	roDB.SetMaxOpenConns(1)
	if err := roDB.PingContext(ctx); err != nil {
		t.Fatalf("ping mode=ro connection failed, before ProbeAuditWrite ever ran: %v "+
			"(this is not the failure this test means to exercise; if this is now expected on this platform, "+
			"say so rather than working around it)", err)
	}

	// A bare literal, not open()/Open(): the only thing this test needs
	// from Store is db and now, and going through open() would run
	// migrate(), which itself may need to write.
	roStore := &Store{db: roDB, now: time.Now}

	err = roStore.ProbeAuditWrite(ctx)
	if err == nil {
		t.Fatal("ProbeAuditWrite against a mode=ro connection = nil error, want a real write failure; " +
			"a degenerate probe (a PRAGMA check, a scratch table, a no-op transaction) could pass every other " +
			"test in this file while returning nil here")
	}
	if !strings.Contains(err.Error(), "readonly") {
		t.Errorf("ProbeAuditWrite error = %q, want it to name the real cause (readonly database); "+
			"a generic failure here would not distinguish a real write attempt from a connectivity check", err.Error())
	}
}

// TestProbeAuditWriteDoesNotConsumeTheCountPruneTrigger is review round 5's
// own named requirement (finding 2). appendAuditEntry's retention
// bookkeeping (auditAppendCount, lastAuditPruneAtNanos) is process-wide,
// in-memory state that ProbeAuditWrite's own rolled-back transaction
// cannot undo: before this fix, the probe called Tx.AppendAuditEntry the
// same as a real write, so it permanently advanced auditAppendCount and,
// on hitting the count trigger, permanently advanced
// lastAuditPruneAtNanos too, even though the prune's own DELETE (run
// inside the probe's transaction) rolled back with it. A probe that lands
// on the count trigger this way silently consumes it: the next real
// write to actually reach that trigger point is 100 appends later than
// it should be, and every dashboard open on GET /api/v1/snapshot polls
// this probe every 30s, far more often than real audit traffic.
func TestProbeAuditWriteDoesNotConsumeTheCountPruneTrigger(t *testing.T) {
	st := openTestStore(t, nil, WithMaxAuditRows(1), WithMaxAuditAge(0))
	ctx := context.Background()

	for i := 0; i < 99; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "x"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if err := st.ProbeAuditWrite(ctx); err != nil {
		t.Fatalf("ProbeAuditWrite: %v", err)
	}
	// The probe must not have moved the count-based prune trigger: this,
	// the 100th REAL append, is what should fire it.
	if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "the-100th-real-append"}); err != nil {
		t.Fatalf("100th real append: %v", err)
	}
	got, err := st.ListAuditEntries(ctx, 0, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("after 99 real appends, 1 probe, then the 100th real append (maxAuditRows=1): len(got) = %d, want 1. "+
			"A probe that silently consumed the count-based prune trigger would leave this unbounded until the 200th real append instead.", len(got))
	}
	if got[0].Action != "the-100th-real-append" {
		t.Fatalf("surviving row action = %q, want %q (the newest)", got[0].Action, "the-100th-real-append")
	}
}

// BenchmarkProbeAuditWrite measures the marginal cost identity.svc.AuditWriteStatus
// adds to every GET /api/v1/snapshot request: a real INSERT into audit_log
// inside a transaction, always rolled back. Answers the question the
// finding-5 redesign (a per-request probe replacing a passive latch) must
// justify on a polled path.
//
// Review round 5 finding 3: this originally measured a probe against a
// fresh, empty TempDir store, which never hits pruneAudit's 1-in-100
// branch and so understated the real cost. See
// BenchmarkProbeAuditWriteAgainstPopulatedTable below for the number
// that actually matters, against a table shaped like production. This
// benchmark is kept for the trivial-case number alone, not as the one to
// cite in the PR.
func BenchmarkProbeAuditWrite(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	st, err := open(ctx, dir, nil, time.Now)
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.ProbeAuditWrite(ctx); err != nil {
			b.Fatalf("ProbeAuditWrite: %v", err)
		}
	}
}

// BenchmarkProbeAuditWriteAgainstPopulatedTable is review round 5 finding
// 3's own named requirement: benchmark the state the code will actually
// meet, not an empty TempDir. Seeds 50,000 real rows first (the pool is
// SetMaxOpenConns(1), so this seeding, and the probe calls below, both
// hold the coordinator's one connection - every other database access
// blocks for the duration, which is exactly why this number, not the
// empty-store one, is the one that answers whether a per-request probe
// belongs on a path this hot).
//
// With finding 2 fixed (ProbeAuditWrite calls insertAuditRow directly,
// never appendAuditEntry), the probe never runs pruneAudit at all - it
// has no prune-trigger bookkeeping to hit - so this should track plain
// INSERT cost against a larger table, not the pre-fix reproduction's
// runaway, never-converging scan (a rolled-back DELETE repeating on
// every probe as the table kept growing).
func BenchmarkProbeAuditWriteAgainstPopulatedTable(b *testing.B) {
	ctx := context.Background()
	dir := b.TempDir()
	st, err := open(ctx, dir, nil, time.Now, WithMaxAuditRows(1_000_000), WithMaxAuditAge(0))
	if err != nil {
		b.Fatalf("open: %v", err)
	}
	b.Cleanup(func() { _ = st.Close() })

	const seedRows = 50_000
	for i := 0; i < seedRows; i++ {
		if _, err := st.AppendAuditEntry(ctx, AuditRecord{Kind: "admin", Action: "seed"}); err != nil {
			b.Fatalf("seed append %d: %v", i, err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.ProbeAuditWrite(ctx); err != nil {
			b.Fatalf("ProbeAuditWrite: %v", err)
		}
	}
}
