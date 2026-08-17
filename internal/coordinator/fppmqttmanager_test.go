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
	mgr := newFPPMQTTManager(runner, testLogger())
	src := newFPPMQTTConfigSource(st, t.TempDir(), testLogger())

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

// TestFPPMQTTManagerReconcileZeroToOneToZero is ADR-039 decision 6's own
// deliverable, verbatim: "the transition that must work is zero to one and
// back to zero... the transition a configuration path built by editing an
// already-populated list never exercises."
func TestFPPMQTTManagerReconcileZeroToOneToZero(t *testing.T) {
	mgr, _ := newTestFPPMQTTManager(t)
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
	mgr.reconcile(ctx, unreachableBrokerCfg(), "")

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
	mgr.reconcile(ctx, config.FPPMQTTConfig{}, "")

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
