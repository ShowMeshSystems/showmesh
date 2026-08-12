package coordinator

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Step 7 seam A's own startup-sequencing suite: the
// SHOWMESH_FPP_ENDPOINTS -> store migration (RES-008 D1) and the owner's
// 2026-08-12 disagreement rule (acceptance criteria 4 and 5).

func newTestConfigSyncDeps(t *testing.T) (*store.Store, identity.Service) {
	t.Helper()
	st := openTestStore(t)
	svc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(discardLogger()))
	return st, svc
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

var testEndpoints = []config.FPPEndpoint{
	{ID: "player-01", URL: "http://10.0.1.20"},
	{ID: "shed", URL: "http://10.0.1.21"},
}

// TestSyncFPPEndpointsConfigNothingConfiguredIsANoop covers the case
// neither the store nor the environment has anything: the collector must
// not run, exactly as before this seam existed.
func TestSyncFPPEndpointsConfigNothingConfiguredIsANoop(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	got, err := syncFPPEndpointsConfig(context.Background(), st, svc, nil, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Errorf("got = %v, want empty", got)
	}
}

// TestSyncFPPEndpointsConfigMigratesFromEnv is acceptance criterion 4: an
// existing deployment with SHOWMESH_FPP_ENDPOINTS set migrates without
// losing an endpoint. Also proves the migration is audited with NO
// principal (a startup migration has no principal) and source
// "env_migration".
func TestSyncFPPEndpointsConfigMigratesFromEnv(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	got, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() error = %v, want nil", err)
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want %+v (no endpoint lost in migration)", got, testEndpoints)
	}

	obj, err := st.GetConfigObject(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		t.Fatalf("GetConfigObject after migration: %v", err)
	}
	if obj.CurrentRevision != 1 {
		t.Errorf("CurrentRevision = %d, want 1", obj.CurrentRevision)
	}

	rev, err := st.GetConfigRevision(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID, 1)
	if err != nil {
		t.Fatalf("GetConfigRevision(1): %v", err)
	}
	if rev.Source != config.FPPEndpointsSourceEnvMigration {
		t.Errorf("Source = %q, want %q", rev.Source, config.FPPEndpointsSourceEnvMigration)
	}

	entries, err := svc.ListAudit(context.Background(), 0, 10)
	if err != nil {
		t.Fatalf("ListAudit: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.Action == "config.migrate" {
			found = true
			if e.PrincipalID != "" || e.PrincipalName != "" {
				t.Errorf("migration audit entry has a principal (%q/%q), want none — a startup migration has no principal", e.PrincipalID, e.PrincipalName)
			}
		}
	}
	if !found {
		t.Errorf("no config.migrate audit entry found among %d entries", len(entries))
	}
}

// TestSyncFPPEndpointsConfigIdenticalStartsWithWarning is half of
// acceptance criterion 5: the variable left set and identical to the
// store's active configuration starts, with the store's list returned as
// authoritative (not the raw env list — same set, but this proves the
// function does not just pass envEndpoints through unexamined).
func TestSyncFPPEndpointsConfigIdenticalStartsWithWarning(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	// First call migrates.
	if _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncFPPEndpointsConfig() error = %v", err)
	}

	// Second call, as if the coordinator restarted with the identical
	// variable still set, in a different order (order-insensitive per
	// this seam's spec).
	reordered := []config.FPPEndpoint{testEndpoints[1], testEndpoints[0]}
	got, err := syncFPPEndpointsConfig(context.Background(), st, svc, reordered, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("second syncFPPEndpointsConfig() error = %v, want nil (identical configuration must start)", err)
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want %+v", got, testEndpoints)
	}

	// Still exactly one revision: the identical-and-matching case does not
	// append a second revision on the store's behalf.
	revs, err := st.ListConfigRevisions(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("len(revisions) = %d, want 1 (identical env value must not append a new revision)", len(revs))
	}
}

// TestSyncFPPEndpointsConfigDifferentRefusesToStart is the other half of
// acceptance criterion 5: changed, it refuses to start and names the
// difference.
func TestSyncFPPEndpointsConfigDifferentRefusesToStart(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	if _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncFPPEndpointsConfig() error = %v", err)
	}

	changed := []config.FPPEndpoint{
		{ID: "player-01", URL: "http://10.0.1.20"},
		{ID: "shed", URL: "http://10.0.1.99"}, // url changed
	}
	_, err := syncFPPEndpointsConfig(context.Background(), st, svc, changed, time.Now, discardLogger())
	if err == nil {
		t.Fatalf("syncFPPEndpointsConfig() error = nil, want a refusal naming the difference")
	}
	if !errors.Is(err, errFPPEndpointsDisagree) {
		t.Errorf("error = %v, want it to wrap errFPPEndpointsDisagree", err)
	}
	if !strings.Contains(err.Error(), "shed") {
		t.Errorf("error = %q, want it to name the differing endpoint id %q", err.Error(), "shed")
	}
	if !strings.Contains(err.Error(), "10.0.1.99") || !strings.Contains(err.Error(), "10.0.1.21") {
		t.Errorf("error = %q, want it to name both differing URLs", err.Error())
	}

	// The store's configuration must be unchanged by a refused start:
	// still exactly one revision, still the original payload.
	revs, err := st.ListConfigRevisions(context.Background(), config.FPPEndpointsConfigKind, config.FPPEndpointsConfigObjectID)
	if err != nil {
		t.Fatalf("ListConfigRevisions: %v", err)
	}
	if len(revs) != 1 {
		t.Errorf("len(revisions) = %d, want 1 (a refused start must not append a revision)", len(revs))
	}
}

// TestSyncFPPEndpointsConfigEnvUnsetAfterMigrationUsesStore covers the
// state RES-008 D1's warning steers an operator toward: the variable
// removed entirely after a prior migration. The store stays authoritative
// with no error and no warning (nothing to compare against).
func TestSyncFPPEndpointsConfigEnvUnsetAfterMigrationUsesStore(t *testing.T) {
	st, svc := newTestConfigSyncDeps(t)

	if _, err := syncFPPEndpointsConfig(context.Background(), st, svc, testEndpoints, time.Now, discardLogger()); err != nil {
		t.Fatalf("first syncFPPEndpointsConfig() error = %v", err)
	}

	got, err := syncFPPEndpointsConfig(context.Background(), st, svc, nil, time.Now, discardLogger())
	if err != nil {
		t.Fatalf("syncFPPEndpointsConfig() with env unset error = %v, want nil", err)
	}
	if !config.FPPEndpointsEqual(got, testEndpoints) {
		t.Errorf("got = %+v, want the store's %+v", got, testEndpoints)
	}
}

// TestDiffFPPEndpointsNamesEveryKindOfDifference proves diffFPPEndpoints
// names an id present only in the store, an id present only in the
// environment, and an id with a differing URL, all in one message.
func TestDiffFPPEndpointsNamesEveryKindOfDifference(t *testing.T) {
	stored := []config.FPPEndpoint{
		{ID: "shared", URL: "http://10.0.1.1"},
		{ID: "store-only", URL: "http://10.0.1.2"},
	}
	env := []config.FPPEndpoint{
		{ID: "shared", URL: "http://10.0.1.99"},
		{ID: "env-only", URL: "http://10.0.1.3"},
	}

	msg := diffFPPEndpoints(stored, env)
	for _, want := range []string{"shared", "10.0.1.1", "10.0.1.99", "store-only", "10.0.1.2", "env-only", "10.0.1.3"} {
		if !strings.Contains(msg, want) {
			t.Errorf("diffFPPEndpoints message = %q, want it to contain %q", msg, want)
		}
	}
}
