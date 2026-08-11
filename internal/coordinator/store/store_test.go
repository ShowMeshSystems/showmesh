package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// fakeClock lets tests drive Store's own bookkeeping timestamps
// deterministically, matching internal/coordinator/broker's fakeClock.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time          { return c.t }
func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// openTestStore opens a throwaway Store for a test. opts is passed straight
// through to open, so a test that needs a non-default event-retention
// bound (see retention.go) can pass e.g. WithMaxEventRows(2) without this
// helper needing its own retention-specific variant.
func openTestStore(t *testing.T, clock *fakeClock, opts ...Option) *Store {
	t.Helper()
	dir := t.TempDir()
	now := time.Now
	if clock != nil {
		now = clock.now
	}
	st, err := open(context.Background(), dir, nil, now, opts...)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// timePtr lets a test build a *time.Time inline, matching the
// internal/coordinator/inventory package's own timePtr test helper.
func timePtr(t time.Time) *time.Time { return &t }

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parse time %q: %v", s, err)
	}
	return parsed.UTC()
}

// TestOpenAppliesPragmas asserts the pragmas open's DSN requests actually
// took effect, rather than trusting that they did because the DSN string
// looks right. open builds the DSN using modernc.org/sqlite's
// mattn/go-sqlite3-compatible shorthand (_journal_mode, _foreign_keys,
// _busy_timeout as URL query parameters) — a compatibility surface layered
// on top of the driver's own native pragma names, not the driver's native
// form. Nothing before this test proved that shorthand actually reaches the
// driver: a version bump that dropped the aliases would silently leave the
// database in SQLite's default rollback-journal mode with foreign keys off,
// and every other test in this package would still pass, since none of them
// depend on WAL concurrency or FK enforcement to fail loudly (SQLite simply
// ignores an FK violation when foreign_keys is off, rather than erroring).
func TestOpenAppliesPragmas(t *testing.T) {
	st := openTestStore(t, nil)

	var journalMode string
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}

	var foreignKeys int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if foreignKeys != 1 {
		t.Errorf("foreign_keys = %d, want 1 (on)", foreignKeys)
	}

	var busyTimeoutMS int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA busy_timeout`).Scan(&busyTimeoutMS); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeoutMS != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeoutMS)
	}

	// Prove foreign_keys=on is not just reported but enforced: node_lwt and
	// node_health both reference nodes(node_id) (see schemaV1), so an
	// insert against a node ID with no nodes row must fail loudly rather
	// than silently succeeding the way it would with FK enforcement off.
	_, err := st.db.ExecContext(context.Background(), `
		INSERT INTO node_lwt (node_id, online, reason, observed_at, provenance, retained, updated_at)
		VALUES ('no-such-node', 1, '', NULL, 'agent_report', 0, ?)
	`, timeToDB(time.Now()))
	if err == nil {
		t.Errorf("insert against a nonexistent node_id succeeded, want a foreign key violation")
	}
}

func TestOpenAppliesMigrationsFromEmpty(t *testing.T) {
	dir := t.TempDir()
	st, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer func() { _ = st.Close() }()

	var version int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d, want %d (len(migrations))", version, len(migrations))
	}

	// Every table schemaV1 creates must exist.
	for _, table := range []string{"nodes", "node_lwt", "node_health"} {
		var name string
		err := st.db.QueryRowContext(context.Background(),
			`SELECT name FROM sqlite_master WHERE type='table' AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q: %v", table, err)
		}
	}
}

func TestOpenIsIdempotentOnSecondOpen(t *testing.T) {
	dir := t.TempDir()

	st1, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("first open: %v", err)
	}
	// Write something so the second open has real content to preserve.
	if err := st1.UpsertHello(context.Background(), "node-a", HelloRecord{
		Label: "A", Platform: "linux-amd64", AgentVersion: "0.1.0", BootID: "boot-1",
		StartedAt: time.Now().UTC(), Provenance: ProvenanceAgentReport,
	}); err != nil {
		t.Fatalf("upsert hello: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("second open: %v", err)
	}
	defer func() { _ = st2.Close() }()

	rec, err := st2.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node after reopen: %v", err)
	}
	if rec.Hello == nil || rec.Hello.Label != "A" {
		t.Errorf("hello not preserved across reopen: %+v", rec.Hello)
	}
}

// TestMigrationV2MakesLWTObservedAtNullableAndPreservesData simulates a
// database written by an older binary that only knew schemaV1 (where
// node_lwt.observed_at was NOT NULL), then reopens it through the normal
// [open] path and checks migration 2 both relaxes the constraint and
// preserves the row that was already there — the append-only migration
// pattern this package requires (see migrations.go), exercised for real
// rather than only asserted in a comment.
func TestMigrationV2MakesLWTObservedAtNullableAndPreservesData(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, dbFileName)

	// Build a v1-only database directly, bypassing open/migrate (which
	// always brings a database to the newest known version).
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open raw db: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), schemaV1); err != nil {
		t.Fatalf("apply schemaV1: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `PRAGMA user_version = 1`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}

	now := timeToDB(time.Now())
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO nodes (node_id, first_seen_at, updated_at) VALUES (?, ?, ?)`, "node-v1", now, now); err != nil {
		t.Fatalf("insert node stub: %v", err)
	}
	// node_lwt.observed_at is NOT NULL under schemaV1, so this pre-migration
	// row must carry a real value, exactly like every row a v1 binary ever
	// wrote.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO node_lwt (node_id, online, reason, observed_at, provenance, retained, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, "node-v1", 1, "pre-migration reason", now, "broker_last_will", 0, now); err != nil {
		t.Fatalf("insert v1 lwt row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	st, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open (should apply migration 2): %v", err)
	}
	defer func() { _ = st.Close() }()

	var version int
	if err := st.db.QueryRowContext(context.Background(), `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != len(migrations) {
		t.Errorf("user_version = %d, want %d (len(migrations))", version, len(migrations))
	}

	rec, err := st.GetNode(context.Background(), "node-v1")
	if err != nil {
		t.Fatalf("get node after migration: %v", err)
	}
	if rec.LWT == nil || !rec.LWT.Online || rec.LWT.Reason != "pre-migration reason" {
		t.Fatalf("LWT data not preserved across migration: %+v", rec.LWT)
	}
	if rec.LWT.ObservedAt == nil {
		t.Errorf("ObservedAt = nil, want the pre-migration value preserved")
	}

	var notNull int
	if err := st.db.QueryRowContext(context.Background(),
		`SELECT "notnull" FROM pragma_table_info('node_lwt') WHERE name = 'observed_at'`).Scan(&notNull); err != nil {
		t.Fatalf("read node_lwt.observed_at notnull flag: %v", err)
	}
	if notNull != 0 {
		t.Errorf(`node_lwt.observed_at "notnull" = %d, want 0 (nullable after migration 2)`, notNull)
	}

	// Prove the relaxed constraint is not just reported but actually
	// enforced: writing a nil ObservedAt (a retained delivery, per
	// RecordLWT's contract) must succeed post-migration.
	if err := st.RecordLWT(context.Background(), "node-v1", LWTRecord{
		Online: true, ObservedAt: nil, Provenance: ProvenanceRetainedBrokerState, Retained: true,
	}); err != nil {
		t.Fatalf("record lwt with nil ObservedAt after migration: %v", err)
	}
}

func TestMigrateRefusesNewerSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, dbFileName)

	st, err := open(context.Background(), dir, nil, time.Now)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// Simulate a database written by a future binary with one more
	// migration than this binary knows about.
	if _, err := st.db.ExecContext(context.Background(), `PRAGMA user_version = 999`); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err = open(context.Background(), filepath.Dir(dbPath), nil, time.Now)
	if err == nil {
		t.Fatalf("open with a too-new schema version succeeded, want an error")
	}
	if !errors.Is(err, ErrSchemaTooNew) {
		t.Errorf("error = %v, want it to wrap ErrSchemaTooNew", err)
	}
}

func TestCapabilitySetRoundTripsWithAttributes(t *testing.T) {
	st := openTestStore(t, nil)

	caps := capability.Set{
		{ID: "matrix.render", Version: 1, Attributes: map[string]any{
			"max_width":   float64(1920), // JSON numbers decode to float64
			"tested_fps":  float64(60),
			"device_name": "Kulp K16-Max",
			"eFuse":       true,
			"nested":      map[string]any{"a": float64(1), "b": "two"},
		}},
		{ID: "audio.output.ltc", Version: 2, Attributes: nil},
	}

	err := st.UpsertHello(context.Background(), "node-cap", HelloRecord{
		Label: "Cap Node", Platform: "linux-arm64", AgentVersion: "0.2.0", BootID: "boot-cap",
		StartedAt: mustTime(t, "2026-08-10T12:00:00Z"), Capabilities: caps,
		Provenance: ProvenanceAgentReport,
	})
	if err != nil {
		t.Fatalf("upsert hello: %v", err)
	}

	rec, err := st.GetNode(context.Background(), "node-cap")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Hello == nil {
		t.Fatalf("Hello = nil, want the stored record")
	}
	if !reflect.DeepEqual(rec.Hello.Capabilities, caps) {
		t.Errorf("capabilities did not round-trip:\n got  %#v\n want %#v", rec.Hello.Capabilities, caps)
	}
}

func TestGetNodeNotFound(t *testing.T) {
	st := openTestStore(t, nil)
	_, err := st.GetNode(context.Background(), "does-not-exist")
	if !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("error = %v, want ErrNodeNotFound", err)
	}
}

func TestUpsertHelloPreservesFirstSeenAt(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-10T12:00:00Z")}
	st := openTestStore(t, clock)

	hello := HelloRecord{
		Label: "A", Platform: "linux-amd64", AgentVersion: "0.1.0", BootID: "boot-1",
		StartedAt: clock.now(), Provenance: ProvenanceAgentReport,
	}
	if err := st.UpsertHello(context.Background(), "node-a", hello); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	first, err := st.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}

	clock.advance(5 * time.Minute)
	hello.Label = "A renamed"
	if err := st.UpsertHello(context.Background(), "node-a", hello); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	second, err := st.GetNode(context.Background(), "node-a")
	if err != nil {
		t.Fatalf("get node after update: %v", err)
	}

	if !second.FirstSeenAt.Equal(first.FirstSeenAt) {
		t.Errorf("FirstSeenAt changed on update: %v -> %v", first.FirstSeenAt, second.FirstSeenAt)
	}
	if !second.UpdatedAt.After(first.UpdatedAt) {
		t.Errorf("UpdatedAt did not advance: %v -> %v", first.UpdatedAt, second.UpdatedAt)
	}
	if second.Hello.Label != "A renamed" {
		t.Errorf("Label = %q, want updated value", second.Hello.Label)
	}
}

// TestRetainedHelloHasNilObservedAt is the store-level half of the
// retained-freshness rule: a retained delivery's ObservedAt must round-trip
// as nil, never as some receipt time that would let a caller mistakenly
// treat it as fresh. The full liveness-derivation test lives in
// internal/coordinator/inventory, which is where that nil actually gets
// interpreted; this test only guards the storage layer's part of the
// contract.
func TestRetainedHelloHasNilObservedAt(t *testing.T) {
	st := openTestStore(t, nil)

	err := st.UpsertHello(context.Background(), "node-retained", HelloRecord{
		Label: "R", Platform: "linux-amd64", AgentVersion: "0.1.0", BootID: "boot-1",
		StartedAt: mustTime(t, "2026-08-10T12:00:00Z"),
		// ObservedAt intentionally left nil, and Provenance/Retained set as
		// internal/coordinator/inventory would for a retained delivery.
		ObservedAt: nil, Provenance: ProvenanceRetainedBrokerState, Retained: true,
	})
	if err != nil {
		t.Fatalf("upsert hello: %v", err)
	}

	rec, err := st.GetNode(context.Background(), "node-retained")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Hello.ObservedAt != nil {
		t.Errorf("ObservedAt = %v, want nil for a retained delivery", *rec.Hello.ObservedAt)
	}
	if rec.Hello.Provenance != ProvenanceRetainedBrokerState {
		t.Errorf("Provenance = %q, want %q", rec.Hello.Provenance, ProvenanceRetainedBrokerState)
	}
	if !rec.Hello.Retained {
		t.Errorf("Retained = false, want true")
	}
}

// TestRetainedLWTHasNilObservedAt is the store-level half of the LWT
// retained-freshness fix: a retained LWT delivery must round-trip with
// ObservedAt nil and Provenance ProvenanceRetainedBrokerState, exactly like
// [TestRetainedHelloHasNilObservedAt] for hello, never with some receipt
// time a coordinator restart would falsely stamp as "just observed".
func TestRetainedLWTHasNilObservedAt(t *testing.T) {
	st := openTestStore(t, nil)

	err := st.RecordLWT(context.Background(), "node-retained-lwt", LWTRecord{
		Online: true, Reason: "",
		ObservedAt: nil, Provenance: ProvenanceRetainedBrokerState, Retained: true,
	})
	if err != nil {
		t.Fatalf("record lwt: %v", err)
	}

	rec, err := st.GetNode(context.Background(), "node-retained-lwt")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.LWT == nil {
		t.Fatalf("LWT = nil, want the stored record")
	}
	if rec.LWT.ObservedAt != nil {
		t.Errorf("ObservedAt = %v, want nil for a retained delivery", *rec.LWT.ObservedAt)
	}
	if rec.LWT.Provenance != ProvenanceRetainedBrokerState {
		t.Errorf("Provenance = %q, want %q", rec.LWT.Provenance, ProvenanceRetainedBrokerState)
	}
	if !rec.LWT.Retained {
		t.Errorf("Retained = false, want true")
	}
}

// TestLiveLWTHasObservedAtAndAgentReportProvenance is
// [TestRetainedLWTHasNilObservedAt]'s counterpart: a live delivery must
// store the given ObservedAt and ProvenanceAgentReport, not the previous
// hardcoded ProvenanceBrokerLastWill (which mislabeled an agent's own live
// "online: true" report — see model.go's ProvenanceBrokerLastWill doc
// comment).
func TestLiveLWTHasObservedAtAndAgentReportProvenance(t *testing.T) {
	st := openTestStore(t, nil)
	observedAt := mustTime(t, "2026-08-10T12:00:00Z")

	err := st.RecordLWT(context.Background(), "node-live-lwt", LWTRecord{
		Online: true, ObservedAt: &observedAt, Provenance: ProvenanceAgentReport, Retained: false,
	})
	if err != nil {
		t.Fatalf("record lwt: %v", err)
	}

	rec, err := st.GetNode(context.Background(), "node-live-lwt")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.LWT == nil || rec.LWT.ObservedAt == nil || !rec.LWT.ObservedAt.Equal(observedAt) {
		t.Fatalf("LWT = %+v, want ObservedAt %v", rec.LWT, observedAt)
	}
	if rec.LWT.Provenance != ProvenanceAgentReport {
		t.Errorf("Provenance = %q, want %q", rec.LWT.Provenance, ProvenanceAgentReport)
	}
}

func TestRecordLWTBeforeHelloCreatesStubNode(t *testing.T) {
	st := openTestStore(t, nil)

	observedAt := mustTime(t, "2026-08-10T12:00:00Z")
	err := st.RecordLWT(context.Background(), "node-early-lwt", LWTRecord{
		Online: false, Reason: "unexpected disconnect",
		ObservedAt: &observedAt, Provenance: ProvenanceAgentReport,
	})
	if err != nil {
		t.Fatalf("record lwt: %v", err)
	}

	rec, err := st.GetNode(context.Background(), "node-early-lwt")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Hello != nil {
		t.Errorf("Hello = %+v, want nil (no hello has ever been observed for this node)", rec.Hello)
	}
	if rec.LWT == nil || rec.LWT.Online {
		t.Fatalf("LWT = %+v, want a stored offline record", rec.LWT)
	}
}

func TestRecordHealthAcceptsHigherSequenceSameBoot(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	accepted, err := st.RecordHealth(ctx, "node-h", HealthRecord{
		BootID: "boot-1", Sequence: 1, AgentState: "running",
		Provenance: ProvenanceAgentReport,
	})
	if err != nil || !accepted {
		t.Fatalf("first record: accepted=%v err=%v, want true, nil", accepted, err)
	}

	accepted, err = st.RecordHealth(ctx, "node-h", HealthRecord{
		BootID: "boot-1", Sequence: 2, AgentState: "running",
		Provenance: ProvenanceAgentReport,
	})
	if err != nil || !accepted {
		t.Fatalf("higher sequence: accepted=%v err=%v, want true, nil", accepted, err)
	}

	rec, err := st.GetNode(ctx, "node-h")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health.Sequence != 2 {
		t.Errorf("stored sequence = %d, want 2", rec.Health.Sequence)
	}
}

func TestRecordHealthIgnoresDuplicateSequenceSameBoot(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.RecordHealth(ctx, "node-h", HealthRecord{BootID: "boot-1", Sequence: 5, Provenance: ProvenanceAgentReport}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	accepted, err := st.RecordHealth(ctx, "node-h", HealthRecord{BootID: "boot-1", Sequence: 5, AgentState: "changed", Provenance: ProvenanceAgentReport})
	if err != nil {
		t.Fatalf("duplicate sequence: unexpected error %v", err)
	}
	if accepted {
		t.Errorf("accepted = true, want false for a duplicate sequence on the same boot ID")
	}

	rec, err := st.GetNode(ctx, "node-h")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health.AgentState == "changed" {
		t.Errorf("stored record was overwritten by an ignored duplicate")
	}
}

// TestRecordHealthLiveDuplicateAdvancesObservedAtWithoutTouchingSequence is
// the regression test for the denial-of-liveness half of the Step 2 round 2
// review's forged-health-sequence finding: a node whose stored sequence has
// been pinned at (or forged to) a maximum value must still be able to prove
// it is alive via later LIVE (non-retained) heartbeats, even though those
// heartbeats' own sequence numbers can never again count as new content.
func TestRecordHealthLiveDuplicateAdvancesObservedAtWithoutTouchingSequence(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-10T12:00:00Z")}
	st := openTestStore(t, clock)
	ctx := context.Background()

	firstObservedAt := clock.now()
	accepted, err := st.RecordHealth(ctx, "node-pinned", HealthRecord{
		BootID: "boot-1", Sequence: math.MaxInt64, AgentState: "running",
		ObservedAt: timePtr(firstObservedAt), Provenance: ProvenanceAgentReport, Retained: false,
	})
	if err != nil || !accepted {
		t.Fatalf("seed record: accepted=%v err=%v, want true, nil", accepted, err)
	}

	// A later, perfectly genuine live heartbeat: its sequence (a real
	// agent would never regress, but this simulates the pinned-at-max case)
	// can never be > math.MaxInt64, so it is always treated as a duplicate
	// by the boot-ID/sequence rule. It must still be able to prove liveness.
	clock.advance(10 * time.Second)
	secondObservedAt := clock.now()
	accepted, err = st.RecordHealth(ctx, "node-pinned", HealthRecord{
		BootID: "boot-1", Sequence: math.MaxInt64, AgentState: "running",
		ObservedAt: timePtr(secondObservedAt), Provenance: ProvenanceAgentReport, Retained: false,
	})
	if err != nil {
		t.Fatalf("live duplicate: unexpected error %v", err)
	}
	if accepted {
		t.Errorf("accepted = true, want false: the content is still a duplicate")
	}

	rec, err := st.GetNode(ctx, "node-pinned")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health.Sequence != math.MaxInt64 {
		t.Errorf("sequence = %d, want unchanged %d", rec.Health.Sequence, uint64(math.MaxInt64))
	}
	if rec.Health.ObservedAt == nil || !rec.Health.ObservedAt.Equal(secondObservedAt) {
		t.Errorf("ObservedAt = %v, want it advanced to the live duplicate's %v (proof of life must not be denied)", rec.Health.ObservedAt, secondObservedAt)
	}
}

// TestRecordHealthRetainedDuplicateDoesNotAdvanceObservedAt is
// [TestRecordHealthLiveDuplicateAdvancesObservedAtWithoutTouchingSequence]'s
// counterpart: a RETAINED duplicate/reorder must be ignored exactly as
// before, with no observed_at refresh, since a retained delivery's age is
// unknown and is not proof of anything happening right now.
func TestRecordHealthRetainedDuplicateDoesNotAdvanceObservedAt(t *testing.T) {
	clock := &fakeClock{t: mustTime(t, "2026-08-10T12:00:00Z")}
	st := openTestStore(t, clock)
	ctx := context.Background()

	firstObservedAt := clock.now()
	if _, err := st.RecordHealth(ctx, "node-h", HealthRecord{
		BootID: "boot-1", Sequence: 5, AgentState: "running",
		ObservedAt: timePtr(firstObservedAt), Provenance: ProvenanceAgentReport, Retained: false,
	}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	clock.advance(10 * time.Second)
	accepted, err := st.RecordHealth(ctx, "node-h", HealthRecord{
		BootID: "boot-1", Sequence: 5, AgentState: "running",
		ObservedAt: nil, Provenance: ProvenanceRetainedBrokerState, Retained: true,
	})
	if err != nil {
		t.Fatalf("retained duplicate: unexpected error %v", err)
	}
	if accepted {
		t.Errorf("accepted = true, want false")
	}

	rec, err := st.GetNode(ctx, "node-h")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health.ObservedAt == nil || !rec.Health.ObservedAt.Equal(firstObservedAt) {
		t.Errorf("ObservedAt = %v, want unchanged %v: a retained duplicate must never refresh freshness", rec.Health.ObservedAt, firstObservedAt)
	}
}

func TestRecordHealthIgnoresLowerSequenceSameBoot(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.RecordHealth(ctx, "node-h", HealthRecord{BootID: "boot-1", Sequence: 10, Provenance: ProvenanceAgentReport}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	accepted, err := st.RecordHealth(ctx, "node-h", HealthRecord{BootID: "boot-1", Sequence: 3, Provenance: ProvenanceAgentReport})
	if err != nil {
		t.Fatalf("lower sequence: unexpected error %v", err)
	}
	if accepted {
		t.Errorf("accepted = true, want false for a lower (reordered) sequence")
	}
}

func TestRecordHealthNewBootIDResetsTracking(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if _, err := st.RecordHealth(ctx, "node-h", HealthRecord{BootID: "boot-1", Sequence: 100, Provenance: ProvenanceAgentReport}); err != nil {
		t.Fatalf("seed record: %v", err)
	}

	// A fresh agent session starts its own sequence back at (or near) zero;
	// a different boot ID must be accepted even though the sequence is far
	// lower than the previous boot's.
	accepted, err := st.RecordHealth(ctx, "node-h", HealthRecord{BootID: "boot-2", Sequence: 0, AgentState: "running", Provenance: ProvenanceAgentReport})
	if err != nil {
		t.Fatalf("new boot id: unexpected error %v", err)
	}
	if !accepted {
		t.Errorf("accepted = false, want true for a new boot ID regardless of sequence")
	}

	rec, err := st.GetNode(ctx, "node-h")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health.BootID != "boot-2" || rec.Health.Sequence != 0 {
		t.Errorf("stored health = %+v, want boot-2/seq 0", rec.Health)
	}
}

// TestRecordHealthConcurrentWritesForSameNodeAreSerialized exercises the
// property Store.SetMaxOpenConns(1) in open() exists for (see its doc
// comment and RecordHealth's): the read (current stored boot ID/sequence)
// and the conditional write must happen atomically, so a second concurrent
// RecordHealth call for the SAME node can never interleave between them and
// produce a lost update. Run under `go test -race`, this also catches any
// data race the concurrency itself would introduce, not just the logical
// atomicity property the final assertion checks.
//
// Every one of n goroutines calls RecordHealth with a distinct, strictly
// increasing sequence on the same boot ID. Regardless of the order the Go
// scheduler actually runs them in, the boot-ID/sequence acceptance rule
// (see RecordHealth's doc comment) guarantees the highest sequence, n, is
// the one left standing once every goroutine has completed: whichever
// goroutine writes n cannot be overwritten by any of the (at most n-1)
// others, since none of the rest ever carries a higher sequence. If the
// read-then-write were not atomic, two goroutines could interleave such
// that a lower sequence's write clobbers a higher one that already landed.
func TestRecordHealthConcurrentWritesForSameNodeAreSerialized(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	const n = 50
	var wg sync.WaitGroup
	for i := 1; i <= n; i++ {
		wg.Add(1)
		go func(seq uint64) {
			defer wg.Done()
			if _, err := st.RecordHealth(ctx, "node-race", HealthRecord{
				BootID: "boot-1", Sequence: seq, AgentState: "running",
				ObservedAt: timePtr(time.Now()), Provenance: ProvenanceAgentReport,
			}); err != nil {
				t.Errorf("record health seq %d: %v", seq, err)
			}
		}(uint64(i))
	}
	wg.Wait()

	rec, err := st.GetNode(ctx, "node-race")
	if err != nil {
		t.Fatalf("get node: %v", err)
	}
	if rec.Health.Sequence != n {
		t.Errorf("final sequence = %d, want %d: the highest sequence must win regardless of goroutine scheduling order (no lost update)", rec.Health.Sequence, n)
	}
	if rec.Health.BootID != "boot-1" {
		t.Errorf("final boot ID = %q, want boot-1", rec.Health.BootID)
	}
}

func TestListNodesOrderedAndIncludesAllThree(t *testing.T) {
	st := openTestStore(t, nil)
	ctx := context.Background()

	if err := st.UpsertHello(ctx, "node-b", HelloRecord{Platform: "p", AgentVersion: "v", BootID: "b", StartedAt: time.Now(), Provenance: ProvenanceAgentReport}); err != nil {
		t.Fatalf("hello b: %v", err)
	}
	if err := st.UpsertHello(ctx, "node-a", HelloRecord{Platform: "p", AgentVersion: "v", BootID: "b", StartedAt: time.Now(), Provenance: ProvenanceAgentReport}); err != nil {
		t.Fatalf("hello a: %v", err)
	}
	if err := st.RecordLWT(ctx, "node-c", LWTRecord{Online: false, ObservedAt: timePtr(time.Now()), Provenance: ProvenanceAgentReport}); err != nil {
		t.Fatalf("lwt c: %v", err)
	}

	nodes, err := st.ListNodes(ctx)
	if err != nil {
		t.Fatalf("list nodes: %v", err)
	}
	if len(nodes) != 3 {
		t.Fatalf("len(nodes) = %d, want 3", len(nodes))
	}
	var ids []string
	for _, n := range nodes {
		ids = append(ids, n.NodeID)
	}
	want := []string{"node-a", "node-b", "node-c"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("node IDs = %v, want sorted %v", ids, want)
	}
}

func TestStoreReadinessReadyAfterOpen(t *testing.T) {
	st := openTestStore(t, nil)
	report := st.Readiness()
	if !report.Ready {
		t.Errorf("Ready = false, want true for a freshly opened store: %+v", report)
	}
	if report.ObservedAt.IsZero() {
		t.Errorf("ObservedAt is zero, want it set")
	}
}

func TestStoreReadinessNotReadyAfterClose(t *testing.T) {
	st := openTestStore(t, nil)
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	report := st.Readiness()
	if report.Ready {
		t.Errorf("Ready = true after Close, want false")
	}
	if report.Reason == "" {
		t.Errorf("Reason is empty, want an explanation")
	}
}
