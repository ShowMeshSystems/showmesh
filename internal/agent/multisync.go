package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"github.com/showmeshsystems/showmesh/internal/version"
	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// runMultiSyncListener binds this render node's MultiSync control socket
// and drives timeline with every received packet, until ctx is done. A bind
// failure degrades the node with a stated reason and never stops the agent
// (build contract seam B3, generalizing ADR-026 decision 6's "an absent
// runtime degrades, never stops" rule to an absent/unbindable socket).
//
// status carries that degradation to the render report (finding 7): its own
// doc comment claimed this was already "degradation with a stated reason,"
// but before status existed the only place that reason ever went was a log
// line — every reported field on a surface still under this node's
// supervision (PipelineState=="running", FramesWritten climbing,
// FramesDropped==0) looked completely healthy while the timeline sat frozen
// at StateUnknown and every surface free-ran idle output all night. status
// is set exactly once here, to the real outcome, before this function does
// anything else that could return early.
//
// ADR-013 does NOT apply here: that rule protects a co-located fppd's own
// unicast MultiSync stream from a second listener silently stealing it via
// SO_REUSEPORT. A render node runs no fppd, so listenAddr and
// AllowPortSharing=false (never overridden — see ListenerConfig.
// AllowPortSharing's own warning) bind normally; the failure mode ADR-013
// guards against cannot occur on a host with nothing else bound to this
// port. If this process WERE ever co-located with a real fppd, binding
// normally is still correct: a bind conflict must fail loudly (which it
// does, here, as a degraded-with-reason node) rather than silently sharing
// the port and risking exactly the desync ADR-013 documents.
func runMultiSyncListener(ctx context.Context, nodeID, listenAddr, interfaceName string, timeline *multisync.Timeline, status *multiSyncStatus, logger *slog.Logger) {
	l, err := multisync.NewListener(multisync.ListenerConfig{
		ListenAddr:    listenAddr,
		InterfaceName: interfaceName,
		Logger:        logger,
		// AllowPortSharing is never set true here; see this function's own
		// doc comment and ADR-013.

		// Answering discovery is what lets FPP unicast sync to this node:
		// FPP only unicasts to remotes it has discovered, so a node that
		// stays silent works on a multicast LAN and is unreachable on a
		// unicast-only one. It also puts the node in FPP's own MultiSync
		// list, which is where an operator looks first.
		RespondToDiscoverPings: true,
		DiscoverResponse: multisync.PingPacket{
			SystemType:    multisync.SystemTypeShowMesh,
			Mode:          multisync.PingModeRemote,
			Hostname:      nodeID,
			VersionString: version.Version,
		},
	})
	if err != nil {
		reason := fmt.Sprintf("failed to bind multisync listener on %s (interface %q): %v", listenAddr, interfaceName, err)
		logger.Warn("multisync: failed to bind listener; this node's surfaces will render idle output until this is fixed",
			"listen_addr", listenAddr, "interface", interfaceName, "error", err)
		status.set(false, reason)
		return
	}
	status.set(true, "")

	for _, jr := range l.JoinResults() {
		if jr.Err != nil {
			logger.Warn("multisync: failed to join multicast group on interface", "interface", jr.Interface.Name, "error", jr.Err)
		}
	}

	handle := func(rec multisync.Received) {
		if rec.DecodeErr != nil {
			return
		}
		sync, ok := rec.Payload.(multisync.SyncPacket)
		if !ok {
			return
		}
		// Pass the source IP only, never "ip:port" — see
		// pkg/multisync/timeline.go:510's 30-line contract on why an
		// ip:port string makes one real master look like several
		// conflicting ones.
		var source string
		if rec.SrcAddr != nil {
			source = sourceIP(rec.SrcAddr)
		}
		timeline.Observe(sync, source)
	}

	if err := l.Run(ctx, handle); err != nil {
		// Run returns nil on ordinary shutdown (ctx canceled); a non-nil
		// error here is a genuine mid-session socket failure, the same
		// degradation as never having bound at all — status must reflect
		// it for the same reason the bind-failure branch above does.
		logger.Warn("multisync: listener stopped", "error", err)
		status.set(false, fmt.Sprintf("multisync listener stopped unexpectedly: %v", err))
	}
}

// sourceIP extracts just the IP address from a UDP source address, per
// runMultiSyncListener's doc comment — see the contract at
// pkg/multisync/timeline.go:510.
func sourceIP(addr *net.UDPAddr) string {
	return addr.IP.String()
}
