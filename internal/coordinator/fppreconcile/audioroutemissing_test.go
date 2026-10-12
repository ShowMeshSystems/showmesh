package fppreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

func advertiseLocalOutput(t *testing.T, st *store.Store, nodeID string, missing []any) {
	t.Helper()
	attrs := map[string]any{"routes": []any{"other-interface"}}
	if missing != nil {
		attrs["missingRoutes"] = missing
	}
	observedAt := time.Now()
	if err := st.UpsertHello(context.Background(), nodeID, store.HelloRecord{
		Label: nodeID, Capabilities: capability.Set{{ID: "audio.output.local", Version: 1, Attributes: attrs}},
		ObservedAt: &observedAt, Provenance: store.ProvenanceAgentReport,
	}); err != nil {
		t.Fatalf("upsert hello: %v", err)
	}
}

func TestPlaylistReadinessWarnsWhenAHeldRouteHasGone(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)
	advertiseLocalOutput(t, st, "node-1", []any{map[string]any{"route": "usb-interface", "reason": "gone"}})

	report, err := PlaylistReadiness(context.Background(), st, nil, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready || report.FailingCondition != "" {
		t.Fatalf("Ready = %v, failing = %q, want ready: this condition only warns", report.Ready, report.FailingCondition)
	}
	want := "Node node-1 no longer reports its program interface usb-interface. Reconnect it or pick another route in Node routing."
	if !strings.Contains(report.Warning, want) {
		t.Fatalf("Warning = %q, want it to contain %q", report.Warning, want)
	}
}

func TestPlaylistReadinessSilentWhenTheHeldRouteIsPresent(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)
	advertiseLocalOutput(t, st, "node-1", nil)

	report, err := PlaylistReadiness(context.Background(), st, nil, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if strings.Contains(report.Warning, "no longer reports") {
		t.Fatalf("Warning = %q, want no missing-interface warning while the route is present", report.Warning)
	}
}
