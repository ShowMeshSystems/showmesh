// Package store is the coordinator's embedded SQLite persistence layer
// (ADR-009): node identity and capability advertisements, the raw liveness
// evidence (last-will state, health heartbeats, boot ID and sequence) that
// internal/coordinator/inventory turns into a liveness verdict on read, and
// — added for Step 3 — pkg/observation evidence (latest value per
// resource+signal) and an append-only event history.
//
// This package stores evidence, never a derived verdict. Per ADR-011, a
// stored "online"/"offline" column would go stale the instant the clock
// moved without a corresponding write, which is exactly the kind of lie
// ADR-011 exists to prevent; liveness is computed in Go, on read, from the
// evidence here plus the caller's current time (see
// internal/coordinator/inventory). A nil pointer field on [NodeRecord] or a
// nil ObservedAt on [HelloRecord]/[HealthRecord] means "no evidence" or
// "evidence with unknown age" — never "evidence of a zero/empty value".
// See each type's doc comment in model.go. observations.go and events.go
// carry the identical rule forward for pkg/observation.Observation: no
// state/health column, ObservedAt round-trips exactly as given (see
// observations.go's restart test), and value_kind/value_text exists so
// that bool/string/int64/float64 round-trip exactly rather than through a
// lossy JSON/NUMERIC column.
//
// The driver is modernc.org/sqlite, a pure-Go implementation, deliberately
// never mattn/go-sqlite3: ADR-012 requires the coordinator to build
// CGo-free so its container image can be a static binary on a distroless
// base and cross-compile cleanly to linux/arm64.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" database/sql driver

	"github.com/showmeshsystems/showmesh/internal/coordinator/readiness"
)

// dbFileName is the SQLite database's filename within the coordinator's
// configured data directory (SHOWMESH_DATA_DIR).
const dbFileName = "showmesh.db"

// pingTimeout bounds how long a single Readiness check may wait on the
// database before treating it as unreachable, so a hung filesystem cannot
// block /readyz indefinitely.
const pingTimeout = 2 * time.Second

// Store is the coordinator's SQLite-backed store. A *Store returned by
// [Open] is always already open and migrated to the current schema
// version; there is no separate "open but not ready" state to check for.
// See [Store.Readiness] for why an open Store can still stop being ready
// later.
type Store struct {
	db *sql.DB

	// now is the clock used for bookkeeping timestamps this package
	// controls itself (FirstSeenAt/UpdatedAt, and Readiness's ObservedAt).
	// It is never used for the evidence timestamps callers pass in
	// (HelloRecord.ObservedAt and friends): those come from
	// internal/coordinator/inventory, which alone decides whether a
	// delivery is live or retained and stamps accordingly. A field rather
	// than a direct time.Now call, so tests can drive it deterministically
	// — the same pattern internal/coordinator/broker's BrokerManager.now
	// and pkg/multisync's Timeline.now use.
	now func() time.Time

	logger *slog.Logger

	// maxEventAge and maxEventRows are the two event-retention bounds
	// [pruneEvents] enforces; see retention.go, where both are labeled as
	// the ShowMesh hypotheses the Step 3 task spec requires them to be.
	maxEventAge  time.Duration
	maxEventRows int64

	// eventAppendCount and lastPruneAtNanos are [Store.AppendEvent]'s two,
	// independent triggers for the amortized-on-insert pruning pass (see
	// retention.go): in-process counters/clocks, not stored ones, so both
	// reset to zero on every restart. That reset is deliberate for what
	// eventAppendCount alone decides — a restart making the NEXT insert
	// look like "the 1st" rather than "the 4,801st" only changes how soon
	// the row-count bound (maxEventRows) gets re-checked, never whether it
	// is correctly enforced once checked. It is NOT, on its own, "exactly
	// as correct as a counter that persisted": a coordinator restarted
	// between shows, appending only a handful of events a week, could take
	// years of wall-clock time to accumulate pruneEveryNEvents more
	// inserts, during which its documented age bound (DefaultMaxEventAge)
	// was silent fiction — a real defect a previous version of this
	// comment asserted away (Step 3 review finding 3.5). lastPruneAtNanos
	// is what actually keeps the age bound honest under a low insert rate:
	// see retention.go's pruneCheckInterval doc comment for how the two
	// triggers combine, and why lastPruneAtNanos resetting to zero on
	// restart is exactly the behavior that makes this correct (it forces
	// an immediate prune check on the very next AppendEvent call, rather
	// than something that has to be worked around).
	//
	// Both atomic.Int64 rather than plain fields: AppendEvent can be called
	// concurrently by more than one goroutine, and both are read/written
	// outside of any lock the database's own single-connection pool
	// provides. lastPruneAtNanos stores a UnixNano time.Time, not a
	// time.Time directly, because atomic.Value's happens-before guarantee
	// is unnecessary ceremony for a single int64 and because time.Time is
	// not itself safe for atomic.Value's consistent-concrete-type
	// requirement across a zero value and a set one.
	eventAppendCount atomic.Int64
	lastPruneAtNanos atomic.Int64

	// maxAuditAge, maxAuditRows, auditAppendCount, and lastAuditPruneAtNanos
	// are audit.go's exact counterparts to the four fields above, applied
	// to audit_log instead of events: see retention.go's
	// DefaultMaxAuditAge/DefaultMaxAuditRows doc comment for why bounding
	// this table is a Step 6 obligation rather than an RES-013 deferral,
	// and audit.go's pruneAudit for the write-coupled trigger logic, which
	// is pruneEvents's, unchanged in shape.
	maxAuditAge           time.Duration
	maxAuditRows          int64
	auditAppendCount      atomic.Int64
	lastAuditPruneAtNanos atomic.Int64

	// maxCommandAge, maxCommandRows, commandInsertCount, and
	// lastCommandPruneAtNanos are commands.go's counterparts to the four
	// audit fields above, applied to the commands table — see retention.go's
	// DefaultMaxCommandAge/DefaultMaxCommandRows doc comment for why this
	// bound exists in Step 7 rather than being left to RES-013 (ADR-024
	// decision 11 already names disk exhaustion as a real trigger, and a
	// command journal is exactly the kind of unbounded-write-driven table
	// that reaches it). maxDiscoveryRunAge, maxDiscoveryRunRows,
	// discoveryRunInsertCount, and lastDiscoveryRunPruneAtNanos are the
	// identical pattern applied to discovery_runs.
	maxCommandAge           time.Duration
	maxCommandRows          int64
	commandInsertCount      atomic.Int64
	lastCommandPruneAtNanos atomic.Int64

	maxDiscoveryRunAge           time.Duration
	maxDiscoveryRunRows          int64
	discoveryRunInsertCount      atomic.Int64
	lastDiscoveryRunPruneAtNanos atomic.Int64
}

// Open opens (creating if necessary) the SQLite database under dataDir,
// sets WAL mode, foreign keys, and a busy timeout, and applies migrations.
// opts configures optional behavior — currently only the event-retention
// bounds in retention.go — and may be omitted; every Option has a default
// that keeps existing callers (e.g. internal/coordinator/coordinator.go's
// Step 2 call site) working unchanged.
//
// Unlike internal/coordinator/broker's NewBrokerManager, which per ADR-012
// must never fail merely because the broker is unreachable and instead
// retries forever, Open fails immediately and permanently on any error: a
// database that cannot be opened or migrated (bad permissions, a corrupt
// file, a schema newer than this binary understands, per
// [ErrSchemaTooNew]) is a local deployment fault on this host right now,
// not a transient network condition, and no retry loop can fix it. The
// caller (coordinator.Run) must treat a non-nil error here as fatal
// startup failure. Do not add a retry loop here to make this "consistent"
// with the broker's tolerance — the two are asymmetric on purpose; see the
// Step 2 round 2 store task spec.
func Open(ctx context.Context, dataDir string, logger *slog.Logger, opts ...Option) (*Store, error) {
	// [WithClock] is read here, ahead of open's own opts parsing, purely to
	// pick which clock function to pass as open's explicit now parameter;
	// open parses opts again itself for every other Option, which is safe
	// because every Option is an idempotent pure setter. See WithClock's
	// doc comment in retention.go for why this indirection exists instead
	// of threading a clock override through open's signature directly.
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	now := cfg.clock
	if now == nil {
		now = time.Now
	}
	return open(ctx, dataDir, logger, now, opts...)
}

// open is Open with the clock made explicit, so tests can drive Store's
// own bookkeeping timestamps deterministically without real sleeps.
func open(ctx context.Context, dataDir string, logger *slog.Logger, now func() time.Time, opts ...Option) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
	}
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}

	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("store: create data directory %q: %w", dataDir, err)
	}
	dbPath := filepath.Join(dataDir, dbFileName)

	// Pragmas are passed as modernc.org/sqlite's mattn-go-sqlite3-compatible
	// DSN shorthand, which applies them to every connection the driver
	// opens (not just the first): journal_mode=WAL so readers are not
	// blocked by a writer's transaction, foreign_keys=on because this
	// package's schema relies on FK integrity between nodes and its
	// evidence tables (see migrations.go), and busy_timeout so a momentary
	// writer conflict blocks briefly instead of surfacing as SQLITE_BUSY to
	// the caller.
	dsn := "file:" + dbPath + "?" + url.Values{
		"_journal_mode": {"WAL"},
		"_foreign_keys": {"on"},
		"_busy_timeout": {"5000"},
	}.Encode()

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", dbPath, err)
	}

	// SQLite allows only one writer at a time no matter how many
	// database/sql connections are pooled, and the coordinator is an
	// accepted single-writer per ADR-009 in any case. Capping the pool at
	// one connection makes that explicit instead of leaving it to
	// busy_timeout retries — and, more importantly, guarantees
	// RecordHealth's read-then-conditionally-write boot/sequence check
	// (see queries.go) always runs without another goroutine's write ever
	// interleaving between the read and the write on a second connection.
	db.SetMaxOpenConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: connect to %q: %w", dbPath, err)
	}

	if err := migrate(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}

	return &Store{
		db:                  db,
		now:                 now,
		logger:              logger,
		maxEventAge:         cfg.maxEventAge,
		maxEventRows:        cfg.maxEventRows,
		maxAuditAge:         cfg.maxAuditAge,
		maxAuditRows:        cfg.maxAuditRows,
		maxCommandAge:       cfg.maxCommandAge,
		maxCommandRows:      cfg.maxCommandRows,
		maxDiscoveryRunAge:  cfg.maxDiscoveryRunAge,
		maxDiscoveryRunRows: cfg.maxDiscoveryRunRows,
	}, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error {
	return s.db.Close()
}

// Readiness implements readiness.Source. Unlike
// internal/coordinator/broker's BrokerManager, which re-stamps its
// evidence on a fixed background interval because a network partition can
// fail silently between checks, Store checks synchronously on every call:
// a local database ping is cheap, and its failure mode (disk gone, file
// locked, or corrupted) does not need a background poll to detect between
// requests. Open already guarantees the schema is migrated, so the only
// way Readiness reports not-ready is a ping failure occurring after a
// previously successful Open.
func (s *Store) Readiness() readiness.Report {
	ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()

	now := s.now()
	if err := s.db.PingContext(ctx); err != nil {
		return readiness.Report{
			Ready:      false,
			Reason:     fmt.Sprintf("sqlite store is not reachable: %v", err),
			ObservedAt: now,
		}
	}
	return readiness.Report{Ready: true, ObservedAt: now}
}
