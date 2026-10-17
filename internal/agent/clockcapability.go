package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/showmeshsystems/showmesh/internal/agent/clock"
	"github.com/showmeshsystems/showmesh/pkg/capability"
)

// sysClassNet and phcIndexForNetworkInterface are package vars so tests
// can describe a host's network interfaces without its real hardware.
var (
	sysClassNet                 = "/sys/class/net"
	phcIndexForNetworkInterface = clock.PHCIndexForInterface
	externalPTPSocketPresent    = func() bool {
		_, err := os.Stat(clock.DefaultExternalUDSAddress)
		return err == nil
	}
)

// detectClockPTPCapability advertises clock.ptp: this node's physical
// network interfaces, each one's PTP hardware clock device when it has
// one, and whether a PTP service outside ShowMesh already runs here. ok is
// false when the node has no physical network interface to report.
func detectClockPTPCapability() (capability.Capability, bool) {
	entries, err := os.ReadDir(sysClassNet)
	if err != nil {
		return capability.Capability{}, false
	}
	var names []string
	for _, e := range entries {
		if _, err := os.Stat(filepath.Join(sysClassNet, e.Name(), "device")); err == nil {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return capability.Capability{}, false
	}
	sort.Strings(names)
	interfaces := make([]any, 0, len(names))
	for _, name := range names {
		iface := map[string]any{"name": name}
		if index, ok, err := phcIndexForNetworkInterface(name); err == nil && ok {
			iface["phcDevice"] = fmt.Sprintf("/dev/ptp%d", index)
		}
		interfaces = append(interfaces, iface)
	}
	return capability.Capability{
		ID:      "clock.ptp",
		Version: 1,
		Attributes: map[string]any{
			"interfaces":       interfaces,
			"externalProvider": externalPTPSocketPresent(),
		},
	}, true
}
