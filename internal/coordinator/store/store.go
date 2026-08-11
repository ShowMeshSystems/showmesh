// Package store is the coordinator's embedded SQLite persistence layer
// (ADR-009): node identity and capability advertisements, and the raw
// liveness evidence (last-will state, health heartbeats, boot ID and
// sequence) that internal/coordinator/inventory turns into a liveness
// verdict on read.
//
// This package stores evidence, never a derived verdict. Per ADR-011, a
// stored "online"/"offline" column would go stale the instant the clock
// moved without a corresponding write, which is exactly the kind of lie
// ADR-011 exists to prevent; liveness is computed in Go, on read, from the
// evidence here plus the caller's current time (see
// internal/coordinator/inventory). A nil pointer field on [NodeRecord] or a
// nil ObservedAt on [HelloRecord]/[HealthRecord] means "no evidence" or
// "evidence with unknown age" — never "evidence of a zero/empty value".
// See each type's doc comment in model.go.
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
}

// Open opens (creating if necessary) the SQLite database under dataDir,
// sets WAL mode, foreign keys, and a busy timeout, and applies migrations.
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
func Open(ctx context.Context, dataDir string, logger *slog.Logger) (*Store, error) {
	return open(ctx, dataDir, logger, time.Now)
}

// open is Open with the clock made explicit, so tests can drive Store's
// own bookkeeping timestamps deterministically without real sleeps.
func open(ctx context.Context, dataDir string, logger *slog.Logger, now func() time.Time) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if now == nil {
		now = time.Now
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

	return &Store{db: db, now: now, logger: logger}, nil
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
