//go:build !unix

package multisync

import "syscall"

// setSocketOptions is a no-op fallback for platforms outside the "unix"
// build tag set (notably Windows), where SO_REUSEADDR/SO_REUSEPORT are
// either unavailable or have different semantics than the unix.go
// implementation assumes. The listener still binds and works on such a
// platform; it just cannot coexist with another process already bound to
// the same port the way the unix implementation optionally can.
// allowPortSharing is accepted, to match setSocketOptions's signature on
// unix, but has no effect here. See sockopts_unix.go for the full
// coexistence tradeoffs on the platforms ShowMesh actually targets (linux
// and darwin, per ADR-006/ADR-012); Windows is not a deployment target this
// package designs for.
func setSocketOptions(_, _ string, _ syscall.RawConn, _ bool) error {
	return nil
}
