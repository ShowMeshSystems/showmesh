package coordinator

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/collector"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
)

// This file covers the seam between ADR-033's mode and the Resolume
// footprint switch: the mode reaches whichever bundle is current, survives
// a bundle rebuild, and never overrides the startup-only debug kill switch.

func newTestResolumeManagerWithConfig(t *testing.T, cfg config.Config) *resolumeManager {
	t.Helper()
	st := openTestStore(t)
	identitySvc := identity.NewService(st, time.Now, t.TempDir(), identity.WithLogger(testLogger()))
	sink := &fppSink{st: st, logger: testLogger()}
	runner := collector.NewRunner(sink, testLogger())
	mgr := newResolumeManager(cfg, runner, &resolume.CompositionStore{}, st, identitySvc, testLogger(), func() {})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go runner.Run(ctx)
	return mgr
}

func currentFootprintEnabled(t *testing.T, mgr *resolumeManager) bool {
	t.Helper()
	mgr.mu.Lock()
	defer mgr.mu.Unlock()
	if mgr.bundle == nil {
		t.Fatal("no bundle is current")
	}
	return mgr.bundle.wiring.collector.Footprint().WebSocketEnabled()
}

// Both directions, live, against a real bundle: entering show closes the
// WebSocket and returning to program reopens it.
func TestResolumeManagerSetWebSocketEnabledDrivesTheCurrentBundle(t *testing.T) {
	srv := httptest.NewServer(resolumeProductHandler())
	defer srv.Close()

	mgr := newTestResolumeManagerWithConfig(t, config.Config{})
	mgr.reconcile(context.Background(), []config.ResolumeInstance{{ID: "arena-1", URL: srv.URL}})

	if !currentFootprintEnabled(t, mgr) {
		t.Fatal("a bundle built in program mode must hold the WebSocket open")
	}

	mgr.SetWebSocketEnabled(false)
	if currentFootprintEnabled(t, mgr) {
		t.Fatal("entering show mode did not close the WebSocket")
	}

	mgr.SetWebSocketEnabled(true)
	if !currentFootprintEnabled(t, mgr) {
		t.Fatal("returning to program mode did not reopen the WebSocket")
	}
}

// A bundle built while the installation is already in show mode must start
// with its WebSocket closed. Without this, an operator configuring a
// Resolume instance mid-show would open one and only have it closed again
// on the next mode reconcile tick.
func TestResolumeManagerNewBundleInheritsTheCurrentShowMode(t *testing.T) {
	srv := httptest.NewServer(resolumeProductHandler())
	defer srv.Close()

	mgr := newTestResolumeManagerWithConfig(t, config.Config{})

	// Show mode arrives before any instance is configured.
	mgr.SetWebSocketEnabled(false)

	mgr.reconcile(context.Background(), []config.ResolumeInstance{{ID: "arena-1", URL: srv.URL}})
	if currentFootprintEnabled(t, mgr) {
		t.Fatal("a bundle built during show mode opened a WebSocket")
	}

	// And a rebuild (a changed instance URL) keeps it closed.
	srv2 := httptest.NewServer(resolumeProductHandler())
	defer srv2.Close()
	mgr.reconcile(context.Background(), []config.ResolumeInstance{{ID: "arena-1", URL: srv2.URL}})
	if currentFootprintEnabled(t, mgr) {
		t.Fatal("a rebuilt bundle opened a WebSocket while the installation was in show mode")
	}
}

// SHOWMESH_RESOLUME_WEBSOCKET_DISABLED is a startup-only debug kill switch,
// and a kill switch a later configuration write can silently undo is not
// one. Program mode must not reopen a WebSocket the operator disabled at
// the process level.
func TestResolumeManagerStartupWebSocketDisableOverridesProgramMode(t *testing.T) {
	srv := httptest.NewServer(resolumeProductHandler())
	defer srv.Close()

	mgr := newTestResolumeManagerWithConfig(t, config.Config{ResolumeWebSocketDisabled: true})
	mgr.reconcile(context.Background(), []config.ResolumeInstance{{ID: "arena-1", URL: srv.URL}})

	if currentFootprintEnabled(t, mgr) {
		t.Fatal("the startup disable did not hold on the first bundle")
	}

	mgr.SetWebSocketEnabled(true)
	if currentFootprintEnabled(t, mgr) {
		t.Fatal("program mode reopened a WebSocket the startup override disabled")
	}
}

// The mode never touches the poll interval in this build: the poll knob
// stays a startup-only debug override.
func TestResolumeManagerShowModeLeavesThePollIntervalAlone(t *testing.T) {
	srv := httptest.NewServer(resolumeProductHandler())
	defer srv.Close()

	mgr := newTestResolumeManagerWithConfig(t, config.Config{ResolumePollInterval: 7 * time.Second})
	mgr.reconcile(context.Background(), []config.ResolumeInstance{{ID: "arena-1", URL: srv.URL}})

	mgr.mu.Lock()
	before := mgr.bundle.wiring.collector.Footprint().PollInterval()
	mgr.mu.Unlock()

	mgr.SetWebSocketEnabled(false)
	mgr.SetWebSocketEnabled(true)

	mgr.mu.Lock()
	after := mgr.bundle.wiring.collector.Footprint().PollInterval()
	mgr.mu.Unlock()

	if before != 7*time.Second || after != before {
		t.Fatalf("poll interval moved from %v to %v; the mode must not touch it", before, after)
	}
}
