package coordinator

// Track G seam G-3's own deliverable proof: the zero-to-one and one-to-zero
// transitions ADR-039 decision 6 names as the ones that must work with no
// coordinator restart, exercised against fppMQTTManager directly, mirroring
// resolumemanager_test.go's identical shape. Unlike Resolume's REST
// collector, fppmqtt.Collector connects over MQTT, which httptest cannot
// fake — these tests point at an address nothing listens on
// ("tcp://127.0.0.1:1") and assert the manager's own bundle bookkeeping
// (CollectorStatuses, Configured-equivalent, CurrentHosts), never an actual
// broker round trip. internal/coordinator/collector/fppmqtt's own
// integration_test.go covers the real wire protocol against a real
// Mosquitto.

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

func newTestFPPMQTTManager(t *testing.T) (*fppMQTTManager, *fppMQTTConfigSource) {
	t.Helper()
	st := openTestStore(t)
	sink := &fppSink{st: st, logger: testLogger()}
	runner := collector.NewRunner(sink, testLogger())
	src := newFPPMQTTConfigSource(st, t.TempDir(), testLogger(), config.FPPMQTTConfig{}, "")
	mgr := newFPPMQTTManager(runner, src, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go runner.Run(ctx)

	return mgr, src
}

// unreachableBrokerCfg is a syntactically valid fpp.mqtt configuration
// whose broker nothing listens on — enough for fppmqtt.New to accept and
// for the manager's own bookkeeping to be exercised, without needing a
// real broker.
func unreachableBrokerCfg() config.FPPMQTTConfig {
	return config.FPPMQTTConfig{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"player-01": "player-01"},
	}
}

// seedFPPMQTTRevision writes cfg as revision rev of fpp.mqtt and activates
// it, mirroring what a PUT does to the store.
func seedFPPMQTTRevision(t *testing.T, st *store.Store, rev int64, cfg config.FPPMQTTConfig) {
	t.Helper()
	payload, err := config.EncodeFPPMQTTPayload(cfg, false)
	if err != nil {
		t.Fatalf("EncodeFPPMQTTPayload: %v", err)
	}
	if _, err := st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.FPPMQTTConfigKind, ObjectID: config.FPPMQTTConfigObjectID,
		Revision: rev, PayloadJSON: payload, Source: config.FPPMQTTSourceAPI,
	}); err != nil {
		t.Fatalf("seed fpp.mqtt configuration: create revision %d: %v", rev, err)
	}
	if _, err := st.ActivateConfigRevision(context.Background(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, rev); err != nil {
		t.Fatalf("seed fpp.mqtt configuration: activate revision %d: %v", rev, err)
	}
}

// TestFPPMQTTManagerReconcileZeroToOneToZero is ADR-039 decision 6's own
// deliverable, verbatim: "the transition that must work is zero to one and
// back to zero... the transition a configuration path built by editing an
// already-populated list never exercises." Configuration is delivered
// through the store because CurrentHosts resolves from it, never from the
// running bundle.
func TestFPPMQTTManagerReconcileZeroToOneToZero(t *testing.T) {
	mgr, src := newTestFPPMQTTManager(t)
	ctx := context.Background()

	// --- Zero: nothing configured yet ---
	statuses, err := mgr.CollectorStatuses(ctx)
	if err != nil {
		t.Fatalf("CollectorStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != "not_configured" {
		t.Fatalf("CollectorStatuses = %+v, want exactly one not_configured entry", statuses)
	}
	hosts, err := mgr.CurrentHosts(ctx)
	if err != nil {
		t.Fatalf("CurrentHosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("CurrentHosts = %+v, want empty before any broker is configured", hosts)
	}

	// --- Zero to one: the first-time-setup transition ---
	seedFPPMQTTRevision(t, src.st, 1, unreachableBrokerCfg())
	cfg, password := src.Current(ctx)
	mgr.reconcile(ctx, cfg, password)

	statuses, err = mgr.CollectorStatuses(ctx)
	if err != nil {
		t.Fatalf("CollectorStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != "running" {
		t.Fatalf("CollectorStatuses = %+v, want exactly one running entry", statuses)
	}
	hosts, err = mgr.CurrentHosts(ctx)
	if err != nil {
		t.Fatalf("CurrentHosts: %v", err)
	}
	if hosts["player-01"] != "player-01" {
		t.Fatalf("CurrentHosts = %+v, want the configured host map", hosts)
	}

	// --- One to zero: decommissioning ---
	seedFPPMQTTRevision(t, src.st, 2, config.FPPMQTTConfig{})
	cfg, password = src.Current(ctx)
	mgr.reconcile(ctx, cfg, password)

	statuses, err = mgr.CollectorStatuses(ctx)
	if err != nil {
		t.Fatalf("CollectorStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != "not_configured" {
		t.Fatalf("CollectorStatuses after removal = %+v, want exactly one not_configured entry", statuses)
	}
	hosts, err = mgr.CurrentHosts(ctx)
	if err != nil {
		t.Fatalf("CurrentHosts: %v", err)
	}
	if len(hosts) != 0 {
		t.Fatalf("CurrentHosts after removal = %+v, want empty", hosts)
	}
}

// TestFPPMQTTManagerDeferredMigrationSurvivesReconcileTick reproduces the
// deferred-migration teardown: the boot migration could not write the
// store, so the environment stays authoritative, the source is seeded with
// it, and the store holds NO fpp.mqtt object. A reconcile tick reading the
// source must keep the env-built collector, never manufacture an empty
// configuration from the not-found read and tear it down.
func TestFPPMQTTManagerDeferredMigrationSurvivesReconcileTick(t *testing.T) {
	st := openTestStore(t)
	sink := &fppSink{st: st, logger: testLogger()}
	runner := collector.NewRunner(sink, testLogger())
	envCfg := unreachableBrokerCfg()
	src := newFPPMQTTConfigSource(st, t.TempDir(), testLogger(), envCfg, "pw")
	mgr := newFPPMQTTManager(runner, src, testLogger())

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go runner.Run(ctx)

	// Boot's synchronous first reconcile, from the env-authoritative value.
	mgr.reconcile(ctx, envCfg, "pw")
	first := mgr.bundle
	if first == nil {
		t.Fatal("bundle = nil after the boot reconcile, want the env-built collector")
	}

	// The periodic tick: the store has no fpp.mqtt object (the deferred
	// state), and the tick must not tear the bundle down.
	cfg, password := src.Current(ctx)
	mgr.reconcile(ctx, cfg, password)
	if mgr.bundle != first {
		t.Fatal("reconcile tick during a deferred migration replaced or tore down the env-built bundle, want it untouched")
	}

	// A transient store read error must keep the current state too, not
	// manufacture empty: a closed store fails every read with a non-not-found
	// error.
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	cfg, password = src.Current(ctx)
	mgr.reconcile(ctx, cfg, password)
	if mgr.bundle != first {
		t.Fatal("reconcile tick during a transient store read error tore down the env-built bundle, want it untouched")
	}
}

// TestFPPMQTTManagerCurrentHostsReadsStoreNotRunningBundle covers the
// hosts-with-empty-brokerURL case: a stored fpp.mqtt with hosts but no
// brokerURL is valid, starts no collector bundle, and its hosts must still
// be visible to the fpp.endpoints collision check — otherwise a PUT could
// strand them and the next boot's cross-check would refuse to start.
func TestFPPMQTTManagerCurrentHostsReadsStoreNotRunningBundle(t *testing.T) {
	mgr, src := newTestFPPMQTTManager(t)
	ctx := context.Background()

	seedFPPMQTTRevision(t, src.st, 1, config.FPPMQTTConfig{
		Hosts: map[string]string{"player-01": "FPP-Player"},
	})
	cfg, password := src.Current(ctx)
	mgr.reconcile(ctx, cfg, password)

	statuses, err := mgr.CollectorStatuses(ctx)
	if err != nil {
		t.Fatalf("CollectorStatuses: %v", err)
	}
	if len(statuses) != 1 || statuses[0].State != "not_configured" {
		t.Fatalf("CollectorStatuses = %+v, want not_configured (no brokerURL, no bundle)", statuses)
	}
	hosts, err := mgr.CurrentHosts(ctx)
	if err != nil {
		t.Fatalf("CurrentHosts: %v", err)
	}
	if hosts["player-01"] != "FPP-Player" {
		t.Fatalf("CurrentHosts = %+v, want the stored host map even with no running bundle", hosts)
	}
}

// TestFPPMQTTManagerReconcileUnchangedConfigLeavesBundleAlone asserts an
// identical reconcile pass is a no-op rather than tearing down and
// rebuilding the collector — the same "unchanged input leaves the bundle
// alone" property resolumeManager's own reconcile holds.
func TestFPPMQTTManagerReconcileUnchangedConfigLeavesBundleAlone(t *testing.T) {
	mgr, _ := newTestFPPMQTTManager(t)
	ctx := context.Background()

	cfg := unreachableBrokerCfg()
	mgr.reconcile(ctx, cfg, "")
	first := mgr.bundle

	mgr.reconcile(ctx, cfg, "")
	if mgr.bundle != first {
		t.Fatalf("reconcile with an unchanged configuration rebuilt the bundle, want it left alone")
	}
}

// TestFPPMQTTManagerRunAppliesConfigurationOnATicker mirrors
// TestResolumeManagerRunAppliesConfigurationOnATicker: a fpp.mqtt
// configuration written to the store after the reconcile loop has already
// started is picked up on the next tick, with no restart — proving the
// live source feeds the manager correctly, without waiting out the real
// production fppMQTTReconcileInterval (10s).
func TestFPPMQTTManagerRunAppliesConfigurationOnATicker(t *testing.T) {
	mgr, src := newTestFPPMQTTManager(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Millisecond):
				cfg, password := src.Current(ctx)
				mgr.reconcile(ctx, cfg, password)
			}
		}
	}()

	payload, err := config.EncodeFPPMQTTPayload(unreachableBrokerCfg(), false)
	if err != nil {
		t.Fatalf("EncodeFPPMQTTPayload: %v", err)
	}
	if _, err := src.st.CreateConfigRevision(context.Background(), store.ConfigRevisionRecord{
		Kind: config.FPPMQTTConfigKind, ObjectID: config.FPPMQTTConfigObjectID,
		Revision: 1, PayloadJSON: payload, Source: config.FPPMQTTSourceAPI,
	}); err != nil {
		t.Fatalf("seed fpp.mqtt configuration: create revision: %v", err)
	}
	if _, err := src.st.ActivateConfigRevision(context.Background(), config.FPPMQTTConfigKind, config.FPPMQTTConfigObjectID, 1); err != nil {
		t.Fatalf("seed fpp.mqtt configuration: activate revision: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		statuses, err := mgr.CollectorStatuses(ctx)
		if err != nil {
			t.Fatalf("CollectorStatuses: %v", err)
		}
		if len(statuses) == 1 && statuses[0].State == "running" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("CollectorStatuses = %+v after deadline, want running once the store-seeded configuration is picked up", statuses)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestFPPMQTTCollectorStateMapsSilentToConnectedNoData is the silent=true
// direction of fppMQTTCollectorState, independent of any live connection:
// SilentSinceConnect's own threshold and message-tracking behavior are
// covered exhaustively by the fppmqtt package's own tests; this only checks
// the mapping onto api.CollectorState is correct, now including the
// host-qualified id.
func TestFPPMQTTCollectorStateMapsSilentToConnectedNoData(t *testing.T) {
	got := fppMQTTCollectorState("player-01", true, "connected to the broker since 2026-08-31T00:00:00Z but has received no message on any subscribed topic for host \"player-01\" since 2026-08-30T23:59:00Z")
	wantID := fppMQTTCollectorSourceID + ":player-01"
	if got.ID != wantID {
		t.Errorf("ID = %q, want %q", got.ID, wantID)
	}
	if got.State != "connected_no_data" {
		t.Errorf("State = %q, want %q", got.State, "connected_no_data")
	}
	if got.Reason == nil || *got.Reason == "" {
		t.Errorf("Reason = %v, want the silence reason set", got.Reason)
	}
}

// TestFPPMQTTCollectorStateMapsNotSilentToRunning is the mirror case: a
// host that has received evidence (or was never silent) reports running,
// with no reason: the same shape this endpoint has always used for a
// healthy collector, now per host.
func TestFPPMQTTCollectorStateMapsNotSilentToRunning(t *testing.T) {
	got := fppMQTTCollectorState("player-01", false, "")
	wantID := fppMQTTCollectorSourceID + ":player-01"
	if got.ID != wantID {
		t.Errorf("ID = %q, want %q", got.ID, wantID)
	}
	if got.State != "running" {
		t.Errorf("State = %q, want %q", got.State, "running")
	}
	if got.Reason != nil {
		t.Errorf("Reason = %v, want nil for running", got.Reason)
	}
}

// TestFPPMQTTManagerCollectorStatusesOneRowPerHost is the acceptance test
// at the manager layer: three configured hosts produce three rows, each
// with its own host-qualified id, none of them silent (the broker is
// unreachable here, so every host is still inside SilentSinceConnect's
// disconnected=>false branch. The point of this test is the per-host row
// count and id shape, not the silence verdict, which fppmqtt's own
// package tests cover exhaustively).
func TestFPPMQTTManagerCollectorStatusesOneRowPerHost(t *testing.T) {
	mgr, src := newTestFPPMQTTManager(t)
	ctx := context.Background()

	cfg := config.FPPMQTTConfig{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"c-player": "c", "a-player": "a", "b-player": "b"},
	}
	seedFPPMQTTRevision(t, src.st, 1, cfg)
	got, password := src.Current(ctx)
	mgr.reconcile(ctx, got, password)

	statuses, err := mgr.CollectorStatuses(ctx)
	if err != nil {
		t.Fatalf("CollectorStatuses: %v", err)
	}
	if len(statuses) != 3 {
		t.Fatalf("len(statuses) = %d, want 3 (one row per configured host)", len(statuses))
	}

	seen := make(map[string]bool, len(statuses))
	for _, s := range statuses {
		seen[s.ID] = true
	}
	for _, id := range []string{"a-player", "b-player", "c-player"} {
		want := fppMQTTCollectorSourceID + ":" + id
		if !seen[want] {
			t.Errorf("no row with ID %q in %+v", want, statuses)
		}
	}
}

// TestFPPMQTTManagerCollectorStatusesOrderedByHostIDDeterministically is
// the ordering guarantee under test: bundle.cfg.Hosts is a map with no
// order of its own, so the only order this method can promise is a sort by
// instance id, asserted here as determinism across repeated calls against
// the SAME configuration, not merely that one call happens to come back
// sorted.
func TestFPPMQTTManagerCollectorStatusesOrderedByHostIDDeterministically(t *testing.T) {
	mgr, src := newTestFPPMQTTManager(t)
	ctx := context.Background()

	cfg := config.FPPMQTTConfig{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"z-player": "z", "m-player": "m", "a-player": "a"},
	}
	seedFPPMQTTRevision(t, src.st, 1, cfg)
	got, password := src.Current(ctx)
	mgr.reconcile(ctx, got, password)

	wantOrder := []string{
		fppMQTTCollectorSourceID + ":a-player",
		fppMQTTCollectorSourceID + ":m-player",
		fppMQTTCollectorSourceID + ":z-player",
	}

	for attempt := 0; attempt < 5; attempt++ {
		statuses, err := mgr.CollectorStatuses(ctx)
		if err != nil {
			t.Fatalf("CollectorStatuses (attempt %d): %v", attempt, err)
		}
		if len(statuses) != len(wantOrder) {
			t.Fatalf("attempt %d: len(statuses) = %d, want %d", attempt, len(statuses), len(wantOrder))
		}
		for i, want := range wantOrder {
			if statuses[i].ID != want {
				t.Fatalf("attempt %d: statuses[%d].ID = %q, want %q (order must be identical on every call)", attempt, i, statuses[i].ID, want)
			}
		}
	}
}
