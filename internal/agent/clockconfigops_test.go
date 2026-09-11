package agent

import (
	"context"
	"testing"

	"github.com/showmeshsystems/showmesh/internal/agent/clock"
)

// TestClockBindingCurrentInterfaceUnconfigured proves a clockBinding
// with no accepted node.clock.configure reports ok=false -- what
// [audioEngineRebuilder.buildPipelineClockLocked] treats as "attempt no
// PHC clock at all", matching a node that never had node.clock
// configuration before this seam existed.
func TestClockBindingCurrentInterfaceUnconfigured(t *testing.T) {
	b := newClockBinding(clock.NewManager(nil, nil))
	iface, ok := b.currentInterface()
	if ok || iface != "" {
		t.Fatalf("currentInterface() = (%q, %v), want (\"\", false)", iface, ok)
	}
}

// TestClockBindingCurrentInterfaceReportsTheAcceptedConfiguration proves
// currentInterface reports the SAME interface node.clock.configure most
// recently accepted, which is what lets the audio pipeline clock read
// the same PHC node.clock.ptp.* already evaluates.
func TestClockBindingCurrentInterfaceReportsTheAcceptedConfiguration(t *testing.T) {
	b := newClockBinding(clock.NewManager(nil, nil))
	// applyConfig calls into clock.Manager.SetConfig, which for an
	// external provider builds no supervised process and cannot fail --
	// see clock.Manager.SetConfig's own doc comment.
	if err := b.applyConfig(context.Background(), clockNodeConfig{
		Schema: clockConfigSchema, Provider: "external", Interface: "eth0", Domain: 0, Revision: 1,
	}); err != nil {
		t.Fatalf("applyConfig: %v", err)
	}
	iface, ok := b.currentInterface()
	if !ok || iface != "eth0" {
		t.Fatalf("currentInterface() = (%q, %v), want (\"eth0\", true)", iface, ok)
	}
}
