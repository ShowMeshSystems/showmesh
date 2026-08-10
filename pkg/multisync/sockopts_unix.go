//go:build unix

package multisync

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// setSocketOptions is the net.ListenConfig.Control hook used when binding
// the MultiSync listen socket.
//
// SO_REUSEADDR is always set. On its own, for UDP, this does NOT let two
// processes bind the exact same address:port at once; that is a common
// misconception carried over from TCP. It only helps this process rebind
// promptly after its own previous socket on the port has not yet fully
// released. It does not achieve coexistence with fppd, or with a second
// ShowMesh instance, by itself, and the doc comment that used to live here
// claimed otherwise. It did not: that claim was never verified and is
// wrong.
//
// SO_REUSEPORT is set only when allowPortSharing is true (see
// ListenerConfig.AllowPortSharing), and it defaults to false. Do not flip
// AllowPortSharing on just to make a "bind: address already in use" error
// go away without reading the rest of this comment; here is what it costs.
//
// What FPP itself does: FPP's own MultiSync.cpp sets ONLY SO_REUSEPORT on
// its receive socket (around line 2401 as of the version read for
// RES-002), and never sets SO_REUSEADDR at all.
//
// The Linux UID trap: Linux's UDP bind-conflict check allows two sockets on
// the same address:port only if EITHER both have SO_REUSEADDR set, OR both
// have SO_REUSEPORT set AND belong to the same UID. fppd on a real show
// host typically runs as root. A ShowMesh listener that also sets
// SO_REUSEPORT, but runs as a non-root user, will therefore FAIL to bind
// alongside it even with AllowPortSharing enabled: "bind: address already
// in use", despite both processes asking for reuse. This was reproduced in
// a Linux container: an fppd-like process (root, REUSEPORT only) binds
// fine; a second process as uid 1000 with REUSEADDR+REUSEPORT fails to
// bind alongside it; the same second process as root succeeds; a second
// process as root with REUSEADDR only (no REUSEPORT) also fails. BSD's
// SO_REUSEPORT has no such UID check, which is exactly why this can look
// fine in local development on a Mac and then fail, or silently misbehave,
// on the real Linux show host.
//
// The unicast interception hazard, which is the real reason this defaults
// off: SO_REUSEPORT load-balances UNICAST datagrams across every socket in
// the reuseport group by a hash of the 4-tuple; it does not deliver a copy
// to each member the way it does for multicast and broadcast. Verified: 20
// unicast datagrams sent to one port with two reuseport sockets bound to it
// went 20 to one socket and 0 to the other. Because multicast and broadcast
// ARE fanned out to every member, this only bites in unicast MultiSync
// mode, which is exactly the mode a show uses when multicast is blocked on
// the network. If an FPP master is configured to unicast sync packets to
// known remotes and a co-located ShowMesh listener binds with
// SO_REUSEPORT, it can silently steal some or all of fppd's own unicast
// sync stream for itself, desyncing the real show; nothing here is
// malicious, it is simply what the kernel does with two reuseport sockets.
// Per CLAUDE.md constraint 6 and ADR-001, ShowMesh must never be in FPP's
// timing path and FPP must stay the authoritative scheduler. A listener
// that can intercept FPP's own sync traffic violates that.
//
// Default-safe behavior: with AllowPortSharing false, this listener simply
// fails to bind when fppd (or anything else) already holds FPPCtrlPort.
// That is a loud, immediate, correct failure, not a silent show hazard.
// Enable AllowPortSharing only when you have a specific, understood reason
// to run alongside another bound process, and have verified that unicast
// sync (if used) is not going to be routed away from fppd as a result.
func setSocketOptions(_, _ string, c syscall.RawConn, allowPortSharing bool) error {
	var sockErr error
	err := c.Control(func(fd uintptr) {
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
			sockErr = err
			return
		}
		if !allowPortSharing {
			return
		}
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEPORT, 1); err != nil {
			sockErr = err
			return
		}
	})
	if err != nil {
		return err
	}
	return sockErr
}
