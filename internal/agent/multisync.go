package agent

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/showmeshsystems/showmesh/internal/fppconnect"
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
func runMultiSyncListener(ctx context.Context, nodeID, listenAddr, interfaceName string, timeline *multisync.Timeline, status *multiSyncStatus, fppConnect *fppConnectState, logger *slog.Logger) {
	ranges := rangesFunc(fppConnect)
	// oversizeRangesLogged rate-limits the warning below to once per node
	// process life, not once per discover ping: DiscoverResponseFunc runs on
	// every reply, and a held string that is already over the limit stays
	// over the limit on every subsequent ping too.
	var oversizeRangesLogged sync.Once

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
		// DiscoverResponseFunc, not the static DiscoverResponse template,
		// because the ranges field must be read fresh at reply time from
		// fppConnect rather than fixed at listener construction.
		DiscoverResponseFunc: func() multisync.PingPacket {
			v := ranges()
			if len(v) > multisync.MaxPingRangesLength {
				oversizeRangesLogged.Do(func() {
					logger.Warn("multisync: held channel ranges string exceeds the ping Ranges field capacity; replying with an empty Ranges field instead",
						"length", len(v), "limit", multisync.MaxPingRangesLength)
				})
			}
			// discoverResponse independently clamps an over-long v to "",
			// so this reply is never left unencodable even if that check
			// above is ever bypassed.
			return discoverResponse(nodeID, func() string { return v })
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

// discoverResponse builds the Ping packet this node answers a discover
// request with. It is a pure function, factored out of
// runMultiSyncListener's Listener construction, so the wire-level values it
// produces can be tested without a socket. ranges is called once, at build
// time, to read the node's currently advertised channel range string; nil
// is treated the same as a func returning "".
//
// Every value here is a deliberate compatibility choice pinned by RES-003
// and ADR-044, not a builder's choice:
//   - SystemType 0x7F: the type xLights must see to offer this node as an
//     FPP Connect upload target at all (RES-003 section 10.2).
//   - VersionMajor/VersionMinor 9/5: past xLights' 7.1 eligibility gate and
//     the 7.0 and 9.3 FSEQ gates (RES-003 section 10.5).
//   - Mode stays PingModeRemote, NOT the "player" mode the HTTP seam's
//     GET /api/system/info serves. FPP reads this ping byte, not the HTTP
//     mode, to decide whether to unicast sync to this node at all
//     (supportsUnicast = type < 0x80 && fppMode == REMOTE_MODE, RES-003
//     section 10.2); xLights instead takes its mode from the HTTP surface
//     (fppconnect.AdvertisedMode). Putting the HTTP mode's player bits here
//     would silently stop FPP itself from unicasting to this node. See
//     ADR-044 decision 7.
//   - VersionString "9.5.0": matches the major/minor integers above
//     (RES-003 section 10.5).
//   - HardwareType is left empty: nothing served may contain the string
//     "Falcon Player" (ADR-044 decision 10).
//
// If ranges() returns a string longer than multisync.MaxPingRangesLength,
// Ranges is left empty rather than encoded: EncodePing would otherwise
// reject the whole packet, which answers no discover ping at all.
func discoverResponse(nodeID string, ranges func() string) multisync.PingPacket {
	var rangeStr string
	if ranges != nil {
		rangeStr = ranges()
	}
	if len(rangeStr) > multisync.MaxPingRangesLength {
		rangeStr = ""
	}
	return multisync.PingPacket{
		SystemType:    multisync.SystemTypeShowMesh,
		VersionMajor:  fppconnect.AdvertisedVersionMajor,
		VersionMinor:  fppconnect.AdvertisedVersionMinor,
		Mode:          multisync.PingModeRemote,
		Hostname:      nodeID,
		VersionString: fppconnect.AdvertisedVersion,
		Ranges:        rangeStr,
	}
}

// rangesFunc returns a func reading fppConnect's channel ranges, or a func
// always returning "" if fppConnect is nil. A nil *fppConnectState must
// never reach fppConnect.ChannelRanges as a bare method value: calling it
// dereferences the nil receiver's mu field from inside the per-ping
// discover-response goroutine (pkg/multisync's Listener.maybeRespondToDiscover
// has no recover), which crashes the whole agent process rather than just
// failing to answer that one discover ping.
func rangesFunc(fppConnect *fppConnectState) func() string {
	if fppConnect == nil {
		return func() string { return "" }
	}
	return fppConnect.ChannelRanges
}

// sourceIP extracts just the IP address from a UDP source address, per
// runMultiSyncListener's doc comment — see the contract at
// pkg/multisync/timeline.go:510.
func sourceIP(addr *net.UDPAddr) string {
	return addr.IP.String()
}
