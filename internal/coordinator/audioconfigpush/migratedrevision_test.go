package audioconfigpush

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"

	_ "modernc.org/sqlite" // the same pure-Go driver internal/coordinator/store registers
)

// This file is the end-to-end half of the decibel migration's acceptance:
// a revision written by a pre-decibel coordinator, opened by this one, and
// followed all the way to the linear value that leaves for the node. The
// store's own migration tests prove the rewrite in isolation; only this
// one proves the rewritten payload still DECODES through the shipped
// validator and still pushes the level it was stored at.

// writePreDecibelDatabase creates a coordinator data directory holding a
// database at schema v18 with one audio.settings revision written the way
// a pre-decibel coordinator wrote it, then closes it so store.Open can
// migrate it.
func writePreDecibelDatabase(t *testing.T, linearMaxGain, linearDuckGain float64) string {
	t.Helper()
	dir := t.TempDir()

	db, err := sql.Open("sqlite", filepath.Join(dir, "showmesh.db"))
	if err != nil {
		t.Fatalf("open pre-decibel database: %v", err)
	}
	ctx := context.Background()

	// Reaching v18 is the store's own business, so this borrows the only
	// public way to get there: open the directory with the current binary
	// (which migrates all the way to the newest version), then rewrite the
	// one revision back into its pre-decibel shape and stamp the version
	// back to 18 so the migration under test runs again on reopen.
	_ = db.Close()
	st, err := store.Open(ctx, dir, nil)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := st.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	db, err = sql.Open("sqlite", filepath.Join(dir, "showmesh.db"))
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = db.Close() }()

	payload := fmt.Sprintf(
		`{"driftIgnoreThresholdMs":20,"defaultFadeCurve":"linear","defaultFadeDurationMs":1000,`+
			`"defaultMaxBackgroundGain":%v,"duckTargetGain":%v,"ltcFrameRate":"30","ltcDefaultStartOffset":"00:00:00:00"}`,
		linearMaxGain, linearDuckGain)

	if _, err := db.ExecContext(ctx,
		`INSERT INTO config_objects (kind, id, current_revision, created_at, updated_at)
		 VALUES (?, ?, 1, '2026-08-01T00:00:00Z', '2026-08-01T00:00:00Z')`,
		config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID); err != nil {
		t.Fatalf("seed config object: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO config_revisions (kind, object_id, revision, payload_json, created_at, source)
		 VALUES (?, ?, 1, ?, '2026-08-01T00:00:00Z', ?)`,
		config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, payload,
		config.AudioSettingsSourceAPI); err != nil {
		t.Fatalf("seed revision: %v", err)
	}
	if _, err := db.ExecContext(ctx, `PRAGMA user_version = 18`); err != nil {
		t.Fatalf("stamp user_version back to 18: %v", err)
	}
	return dir
}

// The whole claim, end to end: a revision stored before the unit changed
// still decodes after the migration, and the node still receives the same
// linear values it would have received before.
func TestMigratedRevisionDecodesAndPushesTheSameLevel(t *testing.T) {
	for _, tc := range []struct {
		name      string
		max, duck float64
	}{
		{"ordinary", 0.6, 0.25},
		{"unity ceiling and full silence", 1.0, 0},
		{"the old ceiling bound", 4.0, 0.25},
		// A duck that barely ducks: its decibel value rounds to zero, and
		// zero is not a duck, so the migration must land on a value the
		// validator still accepts rather than one it refuses.
		{"a duck just under unity", 0.6, 0.9999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writePreDecibelDatabase(t, tc.max, tc.duck)

			st, err := store.Open(context.Background(), dir, nil)
			if err != nil {
				t.Fatalf("reopen and migrate: %v", err)
			}
			defer func() { _ = st.Close() }()

			pub := &fakePublisher{}
			if err := ToNode(context.Background(), st, pub, time.Now, "node-a"); err != nil {
				t.Fatalf("ToNode: %v", err)
			}
			params, ok := pub.actionParams("audio.settings.configure")
			if !ok {
				t.Fatal("audio.settings.configure was not published: the migrated revision did not decode")
			}
			if fmt.Sprint(params["revision"]) != "1" {
				t.Fatalf("revision = %v, want the stored revision 1 rather than the built-in default", params["revision"])
			}

			gotCeiling := params["defaultMaxBackgroundGain"].(float64)
			wantCeiling := tc.max
			if wantCeiling > float64(pkgaudio.CeilingFromDb(12)) {
				// The old bound allowed exactly 4.0, which is +12.04 dB and
				// clamps to +12 dB. That is the one deliberate level change.
				wantCeiling = float64(pkgaudio.CeilingFromDb(12))
			}
			if math.Abs(gotCeiling-wantCeiling) > wantCeiling*0.001 {
				t.Errorf("pushed ceiling = %v, want %v", gotCeiling, wantCeiling)
			}

			gotDuck := params["duckTargetGain"].(float64)
			switch {
			case tc.duck == 0:
				if gotDuck != 0 {
					t.Errorf("pushed duck = %v, want exactly 0: it was stored as full silence", gotDuck)
				}
			case tc.duck > 0.99:
				// Rounded to the smallest expressible duck; it must still be
				// a duck (below unity), not a lift.
				if gotDuck >= 1 || gotDuck <= 0 {
					t.Errorf("pushed duck = %v, want a value below unity and above silence", gotDuck)
				}
			default:
				if math.Abs(gotDuck-tc.duck) > tc.duck*0.001 {
					t.Errorf("pushed duck = %v, want %v", gotDuck, tc.duck)
				}
			}

			if _, present := params["duckTargetGainDb"]; present {
				t.Error("a decibel key reached the node; the coordinator-to-agent wire must stay linear")
			}
		})
	}
}
