package api

import (
	"context"
	"strings"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/inventory"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

func clockPTPNodeView(nodeID string, externalProvider bool) inventory.NodeView {
	return inventory.NodeView{NodeID: nodeID, Liveness: inventory.LivenessOnline, Hello: &store.HelloRecord{Capabilities: capability.Set{{
		ID: "clock.ptp", Version: 1,
		Attributes: map[string]any{
			"interfaces":       []any{map[string]any{"name": "eno1"}, map[string]any{"name": "eno2", "phcDevice": "/dev/ptp0"}},
			"externalProvider": externalProvider,
		},
	}}}}
}

func TestCheckNodeClockAgainstNode(t *testing.T) {
	cases := []struct {
		name     string
		view     *inventory.NodeView
		payload  config.NodeClockPayload
		wantText string
	}{
		{"audio output named as the interface", ptr(clockPTPNodeView("node-a", true)),
			config.NodeClockPayload{Provider: config.NodeClockProviderExternal, Interface: "alsa_output.usb-MOTU_M4"}, "is not a network interface on node node-a. Pick one of eno1, eno2."},
		{"agent ptp4l beside the installed service", ptr(clockPTPNodeView("node-a", true)),
			config.NodeClockPayload{Provider: config.NodeClockProviderManaged, Interface: "eno2"}, "already runs its own PTP service"},
		{"installed service on a real interface", ptr(clockPTPNodeView("node-a", true)),
			config.NodeClockPayload{Provider: config.NodeClockProviderExternal, Interface: "eno2"}, ""},
		{"agent ptp4l with no installed service", ptr(clockPTPNodeView("node-a", false)),
			config.NodeClockPayload{Provider: config.NodeClockProviderManaged, Interface: "eno1"}, ""},
		{"node that never reported its interfaces", nil,
			config.NodeClockPayload{Provider: config.NodeClockProviderExternal, Interface: "anything"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, _, _ := nightBackgroundAudioTestHandlers(t)
			if tc.view != nil {
				h.deps.Nodes.(*fakeNodeLister).setViews([]inventory.NodeView{*tc.view})
			}
			verr, err := h.checkNodeClockAgainstNode(context.Background(), testNow, "node-a", tc.payload)
			if err != nil {
				t.Fatal(err)
			}
			switch {
			case tc.wantText == "" && verr != nil:
				t.Fatalf("refused with %q, want accepted", verr.Detail)
			case tc.wantText != "" && (verr == nil || !strings.Contains(verr.Detail, tc.wantText)):
				t.Fatalf("verr = %+v, want a refusal containing %q", verr, tc.wantText)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }
