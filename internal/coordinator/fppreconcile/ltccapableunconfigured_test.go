package fppreconcile

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

const ltcCapableUnconfiguredText = "Node node-1 can send timecode but none is configured."

func makeNodeProgramOnly(t *testing.T, st *store.Store, nodeID string) {
	t.Helper()
	raw, err := config.EncodeAudioNodePayload(config.AudioNodePayload{ProgramRoute: "usb-interface", ProgramChannels: []int{1, 2}})
	if err != nil {
		t.Fatalf("encode audio.node payload: %v", err)
	}
	ctx := context.Background()
	if _, err := st.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
		Kind: config.AudioNodeConfigKind, ObjectID: nodeID, Revision: 2, PayloadJSON: raw, Source: "api",
	}); err != nil {
		t.Fatalf("create audio.node revision: %v", err)
	}
	if _, err := st.ActivateConfigRevision(ctx, config.AudioNodeConfigKind, nodeID, 2); err != nil {
		t.Fatalf("activate audio.node revision: %v", err)
	}
}

func advertiseOutputs(t *testing.T, st *store.Store, nodeID string, withLTC bool) {
	t.Helper()
	caps := capability.Set{{ID: "audio.output.local", Version: 1, Attributes: map[string]any{"routes": []any{"usb-interface"}}}}
	if withLTC {
		caps = append(caps, capability.Capability{ID: "audio.output.ltc", Version: 1, Attributes: map[string]any{"routes": []any{"usb-interface"}}})
	}
	observedAt := time.Now()
	if err := st.UpsertHello(context.Background(), nodeID, store.HelloRecord{
		Label: nodeID, Capabilities: caps, ObservedAt: &observedAt, Provenance: store.ProvenanceAgentReport,
	}); err != nil {
		t.Fatalf("upsert hello: %v", err)
	}
}

func ltcWarningFor(t *testing.T, st *store.Store, p config.ShowPlaylistPayload) string {
	t.Helper()
	report, err := PlaylistReadiness(context.Background(), st, nil, nil, "playlist-1", 1, p)
	if err != nil {
		t.Fatalf("PlaylistReadiness: %v", err)
	}
	if !report.Ready || report.FailingCondition != "" {
		t.Fatalf("Ready = %v, failing = %q, want ready: this condition only warns", report.Ready, report.FailingCondition)
	}
	return warningText(report)
}

func TestPlaylistReadinessWarnsWhenAnLTCCapableNodeHasNoLTCChannel(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)
	makeNodeProgramOnly(t, st, "node-1")
	advertiseOutputs(t, st, "node-1", true)

	got := ltcWarningFor(t, st, p)
	if !strings.Contains(got, ltcCapableUnconfiguredText+" Add an LTC channel in Node routing if this node should.") {
		t.Fatalf("Warning = %q, want the LTC-capable-unconfigured line", got)
	}
}

func TestPlaylistReadinessSilentForProgramOnlyNodeWithNoLTCCapableRoute(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)
	makeNodeProgramOnly(t, st, "node-1")
	advertiseOutputs(t, st, "node-1", false)

	if got := ltcWarningFor(t, st, p); strings.Contains(got, "can send timecode") {
		t.Fatalf("Warning = %q, want no timecode warning for a node with no LTC-capable route", got)
	}
}

func TestPlaylistReadinessSilentWhenTheLTCChannelIsConfigured(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)
	advertiseOutputs(t, st, "node-1", true)

	if got := ltcWarningFor(t, st, p); strings.Contains(got, "can send timecode") {
		t.Fatalf("Warning = %q, want no timecode warning once an LTC channel is configured", got)
	}
}

func TestPlaylistReadinessSilentWhenOnlyAnotherDeviceIsLTCCapable(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)
	makeNodeProgramOnly(t, st, "node-1")
	observedAt := time.Now()
	caps := capability.Set{
		{ID: "audio.output.local", Version: 1, Attributes: map[string]any{"routes": []any{"usb-interface", "hdmi-out"}}},
		{ID: "audio.output.ltc", Version: 1, Attributes: map[string]any{"routes": []any{"hdmi-out"}}},
	}
	if err := st.UpsertHello(context.Background(), "node-1", store.HelloRecord{
		Label: "node-1", Capabilities: caps, ObservedAt: &observedAt, Provenance: store.ProvenanceAgentReport,
	}); err != nil {
		t.Fatalf("upsert hello: %v", err)
	}

	if got := ltcWarningFor(t, st, p); strings.Contains(got, "can send timecode") {
		t.Fatalf("Warning = %q, want no timecode warning when only a device the program does not use is LTC-capable", got)
	}
}

func TestPlaylistReadinessSilentWhenTheNodeHasNotReportedCapabilities(t *testing.T) {
	st := openTestStore(t)
	p := multisyncReadyPlaylistFixture(t, st)
	makeNodeProgramOnly(t, st, "node-1")

	if got := ltcWarningFor(t, st, p); strings.Contains(got, "can send timecode") {
		t.Fatalf("Warning = %q, want no timecode warning before the node has reported capabilities", got)
	}
}
