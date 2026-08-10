//go:build unix

package multisync

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// setSocketOptions is the net.ListenConfig.Control hook used when binding
// the MultiSync listen socket.
//
// NEITHER SO_REUSEADDR NOR SO_REUSEPORT is set unless allowPortSharing is
// true (see ListenerConfig.AllowPortSharing), and it defaults to false. Do
// not flip AllowPortSharing on just to make a "bind: address already in
// use" error go away without reading the rest of this comment; here is what
// it costs.
//
// Why SO_REUSEADDR is also gated, which is not obvious: a previous version
// of this file set SO_REUSEADDR unconditionally, on the stated belief that
// for UDP it cannot by itself let two processes bind the same address:port,
// and that the TCP intuition does not carry over. That belief is wrong on
// Linux, and CI on a Linux runner is what caught it. Verified in a Linux
// container: two sockets that set ONLY SO_REUSEADDR both bind the same UDP
// port successfully, and 20 unicast datagrams sent to that port were
// delivered 20 to one socket and 0 to the other. So SO_REUSEADDR alone
// reproduces the same interception hazard described below. macOS does not
// behave this way, which is why local testing missed it.
//
// Leaving SO_REUSEADDR on by default would also have made ADR-013's
// protection depend on FPP's exact socket options rather than on ours.
// Today fppd sets SO_REUSEPORT only, so a ShowMesh socket setting
// SO_REUSEADDR only fails to bind alongside it, and the mismatch happens to
// protect us. If a future FPP release added SO_REUSEADDR, that accident
// would evaporate and ShowMesh would silently begin intercepting. Setting
// no sharing options at all makes the guarantee ours: the bind fails
// whenever anything else holds the port, whatever options that other
// process chose.
//
// UDP has no TIME_WAIT, so nothing here is needed to rebind promptly after
// this process restarts, and a single listener joins a multicast group
// perfectly well without either option.
//
// What FPP itself does: FPP's own MultiSync.cpp sets ONLY SO_REUSEPORT on
// its receive socket (around line 2401 as of the version read for
// RES-002), and never sets SO_REUSEADDR at all.
//
// The Linux UID trap: Linux's UDP bind-conflict check allows two sockets on
// the same address:port only if EITHER both have SO_REUSEADDR set (see
// above, this is the case that was missed), OR both have SO_REUSEPORT set
// AND belong to the same UID. fppd on a real show
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
		if !allowPortSharing {
			// Default path: set nothing. The bind then fails whenever
			// anything else already holds the port, which is the loud,
			// correct outcome ADR-013 requires.
			return
		}
		if err := unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1); err != nil {
			sockErr = err
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
