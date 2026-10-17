package clock

import (
	"errors"
	"net"
	"os"
	"time"
)

// interfaceExists and externalPTPServiceRunning are package vars so tests
// can describe a host without its real network interfaces or sockets.
var (
	interfaceExists = func(name string) bool {
		_, err := net.InterfaceByName(name)
		return err == nil
	}
	externalPTPServiceRunning = func() bool {
		_, err := os.Stat(DefaultExternalUDSAddress)
		return !errors.Is(err, os.ErrNotExist)
	}
)

// RejectionReason is the operator-facing reason cfg could not be applied,
// naming the setting to change rather than the internal error.
func RejectionReason(cfg Config) string {
	if cfg.Provider != ProviderFPP && cfg.Interface != "" && !interfaceExists(cfg.Interface) {
		return cfg.Interface + " is not a network interface on this node. Pick a network interface."
	}
	if cfg.Provider == ProviderManaged && externalPTPServiceRunning() {
		return "This node already runs its own PTP service, so ShowMesh cannot start another. Choose this node's PTP service."
	}
	return "This node could not use its clock settings. Check this node's log."
}

// StatusRejected is what a node reports after its clock settings were
// refused and no earlier settings are running.
func StatusRejected(now time.Time, reason string) Status {
	s := StatusUnconfigured(now)
	s.State = StateFailed
	s.Reason = reason
	return s
}
