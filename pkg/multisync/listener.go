package multisync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/net/ipv4"
)

// FPPCtrlPort is the fixed UDP port FPP's own source calls FPP_CTRL_PORT,
// documented and confirmed in RES-002.
const FPPCtrlPort = 32320

// MulticastGroup is the multicast group an FPP master joins for MultiSync
// traffic when multicast is the active transport, confirmed in RES-002.
// Multicast is the default transport on FPP 9.x, but NOT on a fresh FPP 10
// install: FPP 10 ships with MultiSyncUnicast defaulting to on and
// MultiSyncMulticast carrying no default at all (RES-002, upstream
// www/settings.json at the 10.0 tag), and its automatic unicast targeting
// only ever selects other FPP instances in remote mode (supportsUnicast in
// src/MultiSync.cpp), never a third-party listener like ShowMesh. A fresh
// FPP 10 player can therefore be configured exactly as it ships and still
// send nothing on this group, with no error on either side; Listener still
// accepts broadcast/unicast on the same socket for exactly this reason, and
// RES-002's operator procedure documents adding a third-party listener to
// MultiSyncRemotes/MultiSyncExtraRemotes as the supported way onto an FPP
// 10 player's sync path.
const MulticastGroup = "239.70.80.80"

// maxDatagramSize bounds the read buffer for a single UDP datagram. No
// MultiSync packet this package decodes is anywhere near this large (a
// version 3 Ping is 301 bytes with header, and a Sync packet's filename is
// capped at MaxFilenameLength); this is sized to the maximum a UDP/IPv4
// datagram can ever be, so a buffer size is never itself the reason a
// packet is truncated or rejected.
const maxDatagramSize = 65535

// Transport names which delivery path a packet arrived on: multicast,
// broadcast, or direct unicast. RES-002 records that FPP selects among the
// three by settings, and that the default differs by FPP version: multicast
// on FPP 9.x, unicast (to other FPP instances only) on a fresh FPP 10
// install. A listener that only ever reports one transport (or none) has
// not actually confirmed which paths a real master is using. TransportUnknown
// is returned whenever the
// destination address cannot be determined or does not match anything
// classifiable, deliberately never guessed.
//
// LIMITATION: classification of TransportUnicast and TransportBroadcast
// depends on the host's local IPv4 addresses, which are gathered once (see
// localAddressSets) when NewListener constructs the Listener. If the host's
// addressing changes afterward, for example a DHCP lease renewal assigning
// a new address, packets addressed to the new address will no longer match
// either set and will silently degrade to TransportUnknown rather than
// being classified correctly (and never incorrectly, since TransportUnknown
// is the deliberate fallback, not a guess). A long-running listener on a
// host with non-static addressing should treat Transport as a snapshot of
// addressing at startup, not a live property.
type Transport string

const (
	TransportUnicast   Transport = "unicast"
	TransportBroadcast Transport = "broadcast"
	TransportMulticast Transport = "multicast"
	TransportUnknown   Transport = "unknown"
)

// Received is delivered to a Handler for every UDP datagram read from the
// MultiSync control socket, decoded successfully or not. Raw is always
// populated, independent of DecodeErr, since a caller building bench
// evidence (see cmd/showmesh-multisync-probe) needs the exact bytes even
// when decoding failed. Raw is a fresh copy owned by the caller; the
// listener does not retain or reuse it after Handler returns.
type Received struct {
	// ReceivedAt is when this datagram was read off the socket.
	ReceivedAt time.Time

	// SrcAddr is the packet's source address. Nil only if the underlying
	// net.Addr returned by the socket was not a *net.UDPAddr, which does
	// not happen in practice for a socket opened as "udp4" but is checked
	// rather than assumed.
	SrcAddr *net.UDPAddr

	// DstAddr is the packet's destination address (which local or
	// multicast/broadcast address it was sent to), if the platform
	// delivered that control information. Nil if not determinable; see
	// Transport's doc comment on why an undetermined destination becomes
	// TransportUnknown rather than a guess.
	DstAddr net.IP

	// IfIndex is the index of the network interface the packet arrived on,
	// 0 if not determinable.
	IfIndex int

	// Transport classifies how this packet was addressed, derived from
	// DstAddr. See the Transport type doc comment.
	Transport Transport

	// Header is the decoded common 7-byte header. It is the zero Header if
	// DecodeErr wraps ErrNotFPPD, meaning the datagram did not even look
	// like an FPPD control packet.
	Header Header

	// Payload is the decoded packet body: one of SyncPacket, BlankPacket,
	// PingPacket, PluginPacket, or CommandPacket, matching Header.Type. It
	// is nil if decoding failed at any stage.
	Payload any

	// Raw is the exact datagram bytes as received, independent of whether
	// decoding succeeded.
	Raw []byte

	// DecodeErr is the error Decode returned, or nil on full success. Per
	// Decode's own contract: wraps ErrNotFPPD if the datagram was not an
	// FPPD control packet at all, wraps ErrMalformed if the header parsed
	// but the body did not, or is an *UnknownPacketTypeError if the header
	// and framing were fine but the packet type is not one this package
	// decodes.
	DecodeErr error
}

// Handler is called once per received datagram, from the Listener's single
// read goroutine. A Handler that blocks or is slow delays reading the next
// packet; a caller needing to do slow work per packet should hand off to
// its own goroutine.
type Handler func(Received)

// JoinResult records the outcome of attempting to join MulticastGroup on
// one network interface.
type JoinResult struct {
	Interface net.Interface
	// Err is nil if the join succeeded on this interface.
	Err error
}

// Stats is a point-in-time snapshot of a Listener's packet counters. Every
// received datagram increments exactly one of DecodedOK, NotFPPD,
// Malformed, or UnknownType, in addition to PacketsReceived.
type Stats struct {
	PacketsReceived uint64
	DecodedOK       uint64
	// NotFPPD counts datagrams that did not even look like an FPPD control
	// packet: ordinary background UDP noise on the port, not a protocol
	// violation from a peer.
	NotFPPD uint64
	// Malformed counts datagrams with a valid FPPD header whose body did
	// not decode: a real MultiSync peer sending something broken.
	Malformed uint64
	// UnknownType counts datagrams with a valid, well-framed FPPD header
	// whose packet type byte is not one this package decodes a payload
	// for.
	UnknownType uint64
}

// ListenerConfig configures a Listener.
type ListenerConfig struct {
	// ListenAddr is the local "host:port" to bind, in the form
	// net.ListenConfig.ListenPacket accepts for network "udp4". An empty
	// host (the usual case, e.g. ":32320") binds the wildcard address so
	// unicast and broadcast datagrams addressed to any local address are
	// delivered to this socket; multicast delivery is separately enabled
	// by joining MulticastGroup below. Defaults to
	// fmt.Sprintf(":%d", FPPCtrlPort) if empty. Tests must always override
	// this to ":0" or another non-32320 port; see listener_test.go.
	ListenAddr string

	// InterfaceName, if non-empty, restricts the multicast group join to
	// this one named interface, and NewListener returns an error if the
	// join on it fails. If empty, NewListener joins every "suitable"
	// interface (up, multicast-capable, not loopback) it can find and
	// never fails startup because of a join failure; call JoinResults on
	// the returned Listener to see what succeeded. On a multi-homed show
	// host, silently joining the wrong (or no) interface is exactly the
	// failure mode RES-002 open item 5 asks a bench operator to be able to
	// see, so JoinResults is not optional plumbing, it is the answer to
	// that item.
	InterfaceName string

	// RespondToDiscoverPings enables the discover-ping responder: on
	// receiving a Ping packet with SubType PingSubTypeDiscover, the
	// Listener sends a Ping packet of its own back to the sender.
	//
	// This is off by default and must be enabled explicitly because doing
	// so transmits onto the show network. RES-002 records that answering
	// discover pings is what makes a third-party MultiSync node appear in
	// FPP's own MultiSync UI list; that is a real operational benefit, but
	// it is also traffic this package would otherwise never originate, so
	// a caller must opt in with eyes open rather than get it as a side
	// effect of merely listening.
	RespondToDiscoverPings bool

	// DiscoverResponse is the template used to build the Ping packet sent
	// when RespondToDiscoverPings is true. SubType is always overwritten to
	// PingSubTypePing (a response is not itself a discover request).
	// Hostname defaults to os.Hostname() if left empty. SystemType defaults
	// to SystemTypeShowMesh (see packet.go for why that value, not a
	// ShowMesh-reserved one, is used) if left as the zero value
	// (SystemTypeUnknown).
	DiscoverResponse PingPacket

	// DiscoverResponseFunc, if non-nil, is called fresh on every discover
	// ping to build the response, instead of using the DiscoverResponse
	// template captured once at NewListener time. This is for fields whose
	// current value is not known until reply time (for example a channel
	// range string sourced from a holder another goroutine updates); a
	// caller with only static fields should keep using DiscoverResponse
	// instead. SubType is always overwritten to PingSubTypePing on the
	// result, the same as for DiscoverResponse, and this func's own
	// Hostname/SystemType are used as returned, without DiscoverResponse's
	// zero-value defaulting.
	DiscoverResponseFunc func() PingPacket

	// DiscoverResponseDelay returns how long to wait before answering a
	// discover ping. Defaults to a random 0-5ms delay, mirroring FPP's own
	// MultiSync.cpp (~line 3088 as of the version read for RES-002), which
	// waits a random 0 to 5 milliseconds before replying specifically so
	// that many remotes on the same network do not all answer in lockstep.
	// Tests should set this to a function returning 0 so they do not have
	// to sleep real time waiting for a response.
	DiscoverResponseDelay func() time.Duration

	// AllowPortSharing, if true, sets SO_REUSEADDR and SO_REUSEPORT on the
	// listen socket (unix platforms only), letting this Listener bind
	// FPPCtrlPort even while fppd or another process already holds it.
	// Defaults to false, which sets neither option, so the bind fails
	// whenever anything else holds the port. That loud failure is the
	// intended behavior, not a limitation to work around.
	//
	// WARNING: this can silently steal fppd's own unicast MultiSync traffic
	// away from it instead of receiving a copy alongside it, and on Linux
	// still requires this process to run as the same UID as the other
	// bound process to work at all. Both options are gated, not just
	// SO_REUSEPORT: on Linux, two sockets setting only SO_REUSEADDR also
	// share a UDP port and one of them takes all the unicast traffic.
	// Read setSocketOptions's doc comment in sockopts_unix.go in full
	// before enabling this, and see ADR-013. Leave it false unless you have
	// a specific, understood reason not to.
	AllowPortSharing bool

	// Logger receives structured diagnostics: join results, decode-error
	// summaries are not logged per packet (see Handler/Stats for that),
	// but setup and discover-responder activity are. Defaults to a
	// discarding logger if nil.
	Logger *slog.Logger
}

// Listener receives MultiSync traffic on FPPCtrlPort (or ListenerConfig.ListenAddr,
// for tests) and, once Run is called, decodes each datagram and hands it to
// a Handler along with its receipt time, source, and raw bytes.
//
// A Listener is single-use: construct one with NewListener, call Run once,
// and discard it. It has no network I/O until Run is called except for the
// bind and multicast join NewListener itself performs.
type Listener struct {
	cfg    ListenerConfig
	logger *slog.Logger

	pconn *ipv4.PacketConn

	joinResults []JoinResult

	localUnicast   map[string]struct{}
	localBroadcast map[string]struct{}

	discoverResponse PingPacket

	// discoverReplyPort is the port a discover-ping response is sent to.
	// Always FPPCtrlPort in production (set by NewListener); see
	// sendDiscoverResponse for why the port a discover ping arrived FROM
	// must never be reused as the reply destination. Tests in this package
	// may override it to observe a response without binding FPPCtrlPort
	// themselves.
	discoverReplyPort int

	// wg tracks in-flight discover-response goroutines (see
	// maybeRespondToDiscover) so Close/Run can wait for every one of them
	// to finish before returning, rather than leaking a goroutine that
	// outlives the Listener.
	wg sync.WaitGroup

	// closed is closed exactly once, by Close, so a discover-response
	// goroutine waiting out its delay can wake up and give up immediately
	// on shutdown instead of always waiting out the full delay.
	closed chan struct{}
	// closeOnce and closeErr make Close idempotent: every call, from any
	// goroutine, returns the result of the first call.
	closeOnce sync.Once
	closeErr  error

	received, decodedOK, notFPPD, malformed, unknownType atomic.Uint64
}

// NewListener binds the MultiSync control socket per cfg, enables
// destination-address control messages (best effort; see the doc comment
// on the SetControlMessage call below), and joins MulticastGroup on the
// selected interface(s). It does not start reading; call Run for that.
func NewListener(cfg ListenerConfig) (*Listener, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = fmt.Sprintf(":%d", FPPCtrlPort)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if cfg.DiscoverResponseDelay == nil {
		cfg.DiscoverResponseDelay = defaultDiscoverResponseDelay
	}

	// net.ListenMulticastUDP is deliberately not used here: it binds only
	// the multicast group address and does not deliver unicast or
	// broadcast datagrams on the same socket. Binding the wildcard address
	// with net.ListenConfig and separately joining the group with
	// golang.org/x/net/ipv4 is what gets all three (RES-002 requires this;
	// see this package's doc comment and the ListenerConfig.ListenAddr field).
	//
	// AllowPortSharing is threaded through to setSocketOptions rather than
	// baked into a package-level Control function: see ListenerConfig.
	// AllowPortSharing and the doc comment on setSocketOptions in
	// sockopts_unix.go for why this defaults to false and what enabling it
	// costs.
	allowPortSharing := cfg.AllowPortSharing
	lc := net.ListenConfig{
		Control: func(network, address string, c syscall.RawConn) error {
			return setSocketOptions(network, address, c, allowPortSharing)
		},
	}
	pc, err := lc.ListenPacket(context.Background(), "udp4", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("multisync: listen on %q: %w", cfg.ListenAddr, err)
	}

	pconn := ipv4.NewPacketConn(pc)

	// Best effort: ask the kernel to also hand back which local address a
	// packet was sent to (and which interface it arrived on) with every
	// read. Not every platform/kernel combination supports this; when it
	// is unavailable, Received.DstAddr stays nil for every packet and
	// Transport classification falls back to TransportUnknown rather than
	// this constructor failing outright, since the listener is still fully
	// functional for receiving and decoding without it.
	if cmErr := pconn.SetControlMessage(ipv4.FlagDst|ipv4.FlagInterface, true); cmErr != nil {
		logger.Warn("multisync: destination control messages unavailable; transport will be classified as unknown for every packet", "error", cmErr)
	}

	ifaces, err := selectInterfaces(cfg.InterfaceName)
	if err != nil {
		_ = pconn.Close()
		return nil, err
	}

	group := &net.UDPAddr{IP: net.ParseIP(MulticastGroup)}
	joinResults := make([]JoinResult, 0, len(ifaces))
	for _, ifi := range ifaces {
		jerr := pconn.JoinGroup(&ifi, group)
		joinResults = append(joinResults, JoinResult{Interface: ifi, Err: jerr})
		if jerr != nil {
			logger.Warn("multisync: failed to join multicast group on interface", "interface", ifi.Name, "group", MulticastGroup, "error", jerr)
		} else {
			logger.Info("multisync: joined multicast group", "interface", ifi.Name, "group", MulticastGroup)
		}
	}
	// An explicitly named interface that fails to join is a setup error:
	// the operator asked for a specific interface and it did not work.
	// Auto-selection failing on some or all suitable interfaces is not
	// fatal (unicast and broadcast delivery on this socket are unaffected,
	// and a show network with no multicast-capable interface still allows
	// operating in broadcast/unicast mode), only reported via JoinResults
	// and the warning logged above.
	if cfg.InterfaceName != "" && len(joinResults) == 1 && joinResults[0].Err != nil {
		_ = pconn.Close()
		return nil, fmt.Errorf("multisync: joining multicast group %s on interface %q: %w",
			MulticastGroup, cfg.InterfaceName, joinResults[0].Err)
	}

	localUnicast, localBroadcast := localAddressSets()

	l := &Listener{
		cfg:               cfg,
		logger:            logger,
		pconn:             pconn,
		joinResults:       joinResults,
		localUnicast:      localUnicast,
		localBroadcast:    localBroadcast,
		discoverReplyPort: FPPCtrlPort,
		closed:            make(chan struct{}),
	}

	// Skipped entirely when DiscoverResponseFunc is set: that path builds
	// its own PingPacket fresh on every reply (see sendDiscoverResponse) and
	// never reads l.discoverResponse, so defaulting it here, including the
	// os.Hostname() call, would be dead work that the func path silently
	// discards.
	if cfg.RespondToDiscoverPings && cfg.DiscoverResponseFunc == nil {
		resp := cfg.DiscoverResponse
		if resp.Hostname == "" {
			if h, hErr := os.Hostname(); hErr == nil {
				resp.Hostname = h
			}
		}
		if resp.SystemType == SystemTypeUnknown {
			resp.SystemType = SystemTypeShowMesh
		}
		resp.SubType = PingSubTypePing
		l.discoverResponse = resp
	}

	return l, nil
}

// LocalAddr returns the address the listener's socket is bound to. Tests
// use this to discover the ephemeral port chosen when ListenerConfig.ListenAddr
// asks for port 0.
func (l *Listener) LocalAddr() net.Addr {
	return l.pconn.LocalAddr()
}

// JoinResults reports the outcome of every multicast group join attempted
// by NewListener, one entry per interface considered. See ListenerConfig.InterfaceName.
func (l *Listener) JoinResults() []JoinResult {
	return l.joinResults
}

// Stats returns a snapshot of the listener's packet counters, safe to call
// concurrently with Run.
func (l *Listener) Stats() Stats {
	return Stats{
		PacketsReceived: l.received.Load(),
		DecodedOK:       l.decodedOK.Load(),
		NotFPPD:         l.notFPPD.Load(),
		Malformed:       l.malformed.Load(),
		UnknownType:     l.unknownType.Load(),
	}
}

// Close releases the listener's socket, unblocking any call to Run that is
// currently reading, and waits for any in-flight discover-ping response
// goroutine (see maybeRespondToDiscover) to finish. It is idempotent and
// safe to call more than once, including concurrently from multiple
// goroutines: every call returns the result of the first one. It is also
// safe to call even if Run was never invoked.
//
// A caller that constructs a Listener via NewListener and then decides not
// to call Run must still call Close to release the socket and any joined
// multicast group memberships; nothing else does. Run itself also calls
// Close internally on every return path (see Run's doc comment), so a
// caller that only ever calls Run does not need to call Close separately
// except to force Run to return early, for example to retry after an
// interface change.
func (l *Listener) Close() error {
	l.closeOnce.Do(func() {
		close(l.closed)
		l.closeErr = l.pconn.Close()
	})
	return l.closeErr
}

// Run reads datagrams from the socket until ctx is cancelled or a fatal
// socket error occurs, calling handle once per datagram. It blocks until
// shutdown. A decode error never stops the loop: it is counted (see Stats)
// and delivered to handle on the Received value's DecodeErr field, and Run
// continues to the next datagram.
//
// Shutdown is context-driven and deterministic: a background goroutine
// closes the socket as soon as ctx is done, which unblocks the in-progress
// (or next) read with a "closed connection" error; Run recognizes that as a
// clean shutdown, by checking ctx.Err() rather than the error's text or
// type, and returns nil. That background goroutine always exits before Run
// returns (via the deferred close of the stop channel below), so Run never
// leaks it. Run itself never panics on the closed connection: the closed
// socket only ever produces an error return from ReadFrom, never a panic.
//
// On every return path, not only context cancellation, Run releases the
// socket (via the idempotent Close) and waits for every discover-response
// goroutine it spawned to finish before returning. This matters because a
// fatal ReadFrom error (ctx still live) must not leak the fd, leave
// multicast group membership held, or leave a goroutine running past Run's
// return; a caller retrying Run in a loop, the way a Step 2 agent reacting
// to interface changes would, would otherwise exhaust its fd limit.
func (l *Listener) Run(ctx context.Context, handle Handler) error {
	// Registered before the nil-handle check below so that even this
	// immediate-error return path releases the socket NewListener already
	// opened, rather than only the paths that reach the read loop.
	defer func() {
		_ = l.Close()
		l.wg.Wait()
	}()

	if handle == nil {
		return errors.New("multisync: Run: handle must not be nil")
	}

	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			_ = l.Close()
		case <-stop:
		}
	}()

	buf := make([]byte, maxDatagramSize)
	for {
		n, cm, srcAddr, err := l.pconn.ReadFrom(buf)
		receivedAt := time.Now()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("multisync: read: %w", err)
		}

		raw := make([]byte, n)
		copy(raw, buf[:n])

		rec := l.buildReceived(raw, receivedAt, srcAddr, cm)
		handle(rec)

		l.maybeRespondToDiscover(rec)
	}
}

// buildReceived decodes one datagram and updates the packet counters. It is
// the sole place Received values are constructed, so counter bookkeeping
// and Received construction can never drift apart.
func (l *Listener) buildReceived(raw []byte, receivedAt time.Time, srcAddr net.Addr, cm *ipv4.ControlMessage) Received {
	l.received.Add(1)

	rec := Received{
		ReceivedAt: receivedAt,
		Raw:        raw,
	}
	if udpAddr, ok := srcAddr.(*net.UDPAddr); ok {
		rec.SrcAddr = udpAddr
	}
	if cm != nil {
		rec.DstAddr = cm.Dst
		rec.IfIndex = cm.IfIndex
	}
	rec.Transport = l.classifyTransport(rec.DstAddr)

	header, payload, err := Decode(raw)
	rec.Header = header
	rec.Payload = payload
	rec.DecodeErr = err

	switch {
	case err == nil:
		l.decodedOK.Add(1)
	case errors.Is(err, ErrNotFPPD):
		l.notFPPD.Add(1)
	case errors.Is(err, ErrMalformed):
		l.malformed.Add(1)
	default:
		var unk *UnknownPacketTypeError
		if errors.As(err, &unk) {
			l.unknownType.Add(1)
		} else {
			// Decode's contract (see packet.go) only ever returns one of
			// the three cases above; this branch exists so a future
			// decoder change that adds a new error class fails loudly in
			// Stats (as an uncounted-but-present DecodeErr) rather than
			// silently miscounting.
			l.malformed.Add(1)
		}
	}

	return rec
}

// multicastGroupIP4 is MulticastGroup parsed once, rather than on every
// packet inside classifyTransport's hot path.
var multicastGroupIP4 = net.ParseIP(MulticastGroup).To4()

// classifyTransport infers Transport from a packet's destination address,
// using the multicast group constant plus the local unicast/broadcast
// addresses gathered from the host's interfaces at construction time.
// Anything that does not match one of those is TransportUnknown rather than
// guessed, per RES-002 open item 5's request to see this cleanly rather
// than assumed. See the Transport type doc comment for the staleness
// limitation this local-address snapshot carries.
func (l *Listener) classifyTransport(dst net.IP) Transport {
	if dst == nil {
		return TransportUnknown
	}
	if ip4 := dst.To4(); ip4 != nil && ip4.Equal(multicastGroupIP4) {
		return TransportMulticast
	}
	if dst.Equal(net.IPv4bcast) {
		return TransportBroadcast
	}
	key := dst.String()
	if _, ok := l.localUnicast[key]; ok {
		return TransportUnicast
	}
	if _, ok := l.localBroadcast[key]; ok {
		return TransportBroadcast
	}
	return TransportUnknown
}

// maybeRespondToDiscover sends the discover-ping response configured on the
// Listener when rec is a discover request and the responder is enabled. See
// ListenerConfig.RespondToDiscoverPings for why this is opt-in.
//
// The actual send happens on its own goroutine, tracked by l.wg (see Close
// and Run's doc comment), after waiting out cfg.DiscoverResponseDelay: this
// keeps the random reply delay (see sendDiscoverResponse) from blocking the
// read loop and delaying the next datagram.
func (l *Listener) maybeRespondToDiscover(rec Received) {
	if !l.cfg.RespondToDiscoverPings || rec.SrcAddr == nil {
		return
	}
	ping, ok := rec.Payload.(PingPacket)
	if !ok || ping.SubType != PingSubTypeDiscover {
		return
	}

	src := rec.SrcAddr
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		if d := l.cfg.DiscoverResponseDelay(); d > 0 {
			select {
			case <-time.After(d):
			case <-l.closed:
				return
			}
		}
		l.sendDiscoverResponse(src)
	}()
}

// sendDiscoverResponse answers a discover ping received from src.
//
// The destination port is deliberately NOT the port the discover ping
// arrived from. FPP's own MultiSync.cpp only ever binds one socket,
// m_receiveSock, to FPPCtrlPort (around line 2407 as of the version read
// for RES-002); m_controlSock, the broadcast send socket, and every
// per-interface multicast send socket it uses to originate a discover ping
// are all left unbound, so the OS assigns each an ephemeral source port. A
// discover ping from a real FPP instance therefore arrives here FROM an
// ephemeral port, and nothing is listening there: FPP will only ever
// receive a reply that lands on its bound receive socket, at FPPCtrlPort.
// Replying to the datagram's source port, which this package originally
// did, sends the response into the void. That failure is silent on both
// ends: the sender sees no error (UDP has none to give), and the operator
// running a probe with -respond-discover just never sees this node appear
// in FPP's MultiSync UI, with nothing to say why.
func (l *Listener) sendDiscoverResponse(src *net.UDPAddr) {
	dst := &net.UDPAddr{IP: src.IP, Port: l.discoverReplyPort}

	resp := l.discoverResponse
	if l.cfg.DiscoverResponseFunc != nil {
		resp = l.cfg.DiscoverResponseFunc()
		resp.SubType = PingSubTypePing
	}
	if resp.IP == ([4]byte{}) {
		if ip, ok := localIPToward(src.IP); ok {
			resp.IP = ip
		}
	}

	b, err := EncodePing(resp)
	if err != nil {
		l.logger.Warn("multisync: failed to encode discover-ping response", "error", err)
		return
	}
	if _, err := l.pconn.WriteTo(b, nil, dst); err != nil {
		l.logger.Warn("multisync: failed to send discover-ping response", "dst", dst, "error", err)
		return
	}
	l.logger.Info("multisync: answered discover ping", "dst", dst)
}

// defaultDiscoverResponseDelay returns a random delay in [0, 5ms], the
// default for ListenerConfig.DiscoverResponseDelay. See that field's doc
// comment for why: FPP itself waits a random 0 to 5 milliseconds before
// answering a discover ping so that many remotes do not all reply in the
// same instant.
func defaultDiscoverResponseDelay() time.Duration {
	return time.Duration(rand.IntN(6)) * time.Millisecond
}

// selectInterfaces resolves ListenerConfig.InterfaceName to the set of interfaces
// NewListener should attempt to join MulticastGroup on: exactly the named
// interface if given, or every "suitable" interface otherwise (up,
// multicast-capable, not loopback: MultiSync traffic is never expected on
// loopback in a real deployment, and joining it there would only add noise
// to JoinResults).
func selectInterfaces(name string) ([]net.Interface, error) {
	if name != "" {
		ifi, err := net.InterfaceByName(name)
		if err != nil {
			return nil, fmt.Errorf("multisync: interface %q: %w", name, err)
		}
		return []net.Interface{*ifi}, nil
	}

	all, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("multisync: enumerating interfaces: %w", err)
	}

	suitable := make([]net.Interface, 0, len(all))
	for _, ifi := range all {
		if ifi.Flags&net.FlagUp == 0 {
			continue
		}
		if ifi.Flags&net.FlagMulticast == 0 {
			continue
		}
		if ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		suitable = append(suitable, ifi)
	}
	return suitable, nil
}

// localAddressSets gathers, once, the set of local unicast IPv4 addresses
// and their subnet-directed broadcast addresses across every interface on
// the host, keyed by net.IP.String() for classifyTransport's lookup. Errors
// enumerating any single interface's addresses are skipped rather than
// failing the whole listener: this is best-effort evidence for transport
// classification, not something the listener's core job (receiving and
// decoding) depends on.
func localAddressSets() (unicast, broadcast map[string]struct{}) {
	unicast = make(map[string]struct{})
	broadcast = make(map[string]struct{})

	ifaces, err := net.Interfaces()
	if err != nil {
		return unicast, broadcast
	}
	for _, ifi := range ifaces {
		addrs, err := ifi.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue
			}
			unicast[ip4.String()] = struct{}{}

			mask := ipNet.Mask
			if len(mask) != net.IPv4len {
				continue
			}
			bcast := make(net.IP, net.IPv4len)
			for i := range bcast {
				bcast[i] = ip4[i] | ^mask[i]
			}
			broadcast[bcast.String()] = struct{}{}
		}
	}
	return unicast, broadcast
}

// localIPToward reports this host's IPv4 address on the interface routing
// toward peer. FPP registers a discovered remote at the address carried
// inside the ping body rather than the datagram's source address, so a zero
// here announces 0.0.0.0 and FPP silently ignores the node. Resolved per
// peer because a multi-homed host has no single correct answer.
func localIPToward(peer net.IP) ([4]byte, bool) {
	var zero [4]byte
	c, err := net.Dial("udp4", net.JoinHostPort(peer.String(), "9"))
	if err != nil {
		return zero, false
	}
	defer func() { _ = c.Close() }()
	ua, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return zero, false
	}
	v4 := ua.IP.To4()
	if v4 == nil {
		return zero, false
	}
	return [4]byte{v4[0], v4[1], v4[2], v4[3]}, true
}
