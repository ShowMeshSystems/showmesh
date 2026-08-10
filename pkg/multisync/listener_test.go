package multisync

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

// recvTimeout bounds how long a test waits for a packet the listener should
// have delivered. Generous enough to never flake under load, short enough
// that a genuine failure (nothing ever arrives) does not stall the suite.
const recvTimeout = 3 * time.Second

// runTimeout bounds how long a test waits for Run to return after context
// cancellation. Shutdown is specified to be prompt; this catches a hang.
const runTimeout = 2 * time.Second

// startTestListener starts a Listener bound to loopback on an OS-assigned
// port (never FPPCtrlPort: tests must not be able to collide with real
// MultiSync traffic or with each other) and runs it in a background
// goroutine. It returns the Listener, a channel fed by every Received value,
// and a stop function that cancels the run context and waits for Run to
// return, failing the test if it does not return within runTimeout.
//
// InterfaceName is deliberately left empty (auto-select) rather than
// pointed at loopback: loopback is excluded from auto-selected multicast
// joins by design (see selectInterfaces), and these tests only need
// ordinary unicast delivery to 127.0.0.1, which works regardless of
// multicast join outcome.
func startTestListener(t *testing.T, configure func(*ListenerConfig)) (*Listener, chan Received, func()) {
	t.Helper()

	cfg := ListenerConfig{ListenAddr: "127.0.0.1:0"}
	if configure != nil {
		configure(&cfg)
	}

	l, err := NewListener(cfg)
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}

	recvCh := make(chan Received, 64)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- l.Run(ctx, func(r Received) { recvCh <- r })
	}()

	stop := func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run() returned error on shutdown: %v", err)
			}
		case <-time.After(runTimeout):
			t.Fatalf("Run() did not return within %s of context cancellation", runTimeout)
		}
	}
	t.Cleanup(stop)

	return l, recvCh, stop
}

// sendTo sends b as a single UDP datagram to the listener's bound address
// from an ephemeral loopback port, mirroring how a real MultiSync peer
// (FPP master, remote, or another third-party node) would deliver a
// datagram: one independent send per packet, no shared connection state
// with the listener.
func sendTo(t *testing.T, l *Listener, b []byte) {
	t.Helper()

	conn, err := net.DialUDP("udp4", nil, l.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatalf("DialUDP() error = %v", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.Write(b); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

// recvOne waits for the next Received value on ch, failing the test if none
// arrives within recvTimeout.
func recvOne(t *testing.T, ch chan Received) Received {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(recvTimeout):
		t.Fatalf("timed out after %s waiting for a packet", recvTimeout)
		return Received{}
	}
}

// --- Valid packet: received, decoded, and delivered with everything a
// caller needs ---

func TestListener_ReceivesAndDecodesValidSyncPacket(t *testing.T) {
	l, recvCh, _ := startTestListener(t, nil)

	want := SyncPacket{
		Action:         SyncActionStart,
		FileType:       SyncFileTypeSequence,
		FrameNumber:    42,
		SecondsElapsed: 1.5,
		Filename:       "show.fseq",
	}
	raw, err := EncodeSync(want)
	if err != nil {
		t.Fatalf("EncodeSync() error = %v", err)
	}

	sendTo(t, l, raw)
	rec := recvOne(t, recvCh)

	if rec.DecodeErr != nil {
		t.Fatalf("DecodeErr = %v, want nil", rec.DecodeErr)
	}
	got, ok := rec.Payload.(SyncPacket)
	if !ok {
		t.Fatalf("Payload type = %T, want SyncPacket", rec.Payload)
	}
	if got != want {
		t.Errorf("decoded payload = %+v, want %+v", got, want)
	}
	if rec.Header.Type != PacketTypeMultiSync {
		t.Errorf("Header.Type = %v, want %v", rec.Header.Type, PacketTypeMultiSync)
	}
	if string(rec.Raw) != string(raw) {
		t.Errorf("Raw = % x, want % x", rec.Raw, raw)
	}
	if rec.SrcAddr == nil {
		t.Error("SrcAddr = nil, want the sender's address")
	}
	if rec.ReceivedAt.IsZero() {
		t.Error("ReceivedAt is zero, want a receipt timestamp")
	}

	stats := l.Stats()
	if stats.PacketsReceived != 1 || stats.DecodedOK != 1 {
		t.Errorf("Stats() = %+v, want PacketsReceived=1 DecodedOK=1", stats)
	}
}

// --- Malformed packet: counted, surfaced, does not kill the listener ---

func TestListener_MalformedPacketCountedAndListenerSurvives(t *testing.T) {
	l, recvCh, _ := startTestListener(t, nil)

	// Valid FPPD header for a Sync packet (type 0x01), but the declared
	// extra data length (3) is far short of syncFixedLen+1 (11): a real
	// header, a broken body.
	malformed := []byte{'F', 'P', 'P', 'D', 0x01, 0x03, 0x00, 0x01, 0x02, 0x03}
	sendTo(t, l, malformed)

	rec := recvOne(t, recvCh)
	if rec.DecodeErr == nil {
		t.Fatal("DecodeErr = nil, want a malformed-body error")
	}
	if !errors.Is(rec.DecodeErr, ErrMalformed) {
		t.Errorf("DecodeErr = %v, want errors.Is(_, ErrMalformed)", rec.DecodeErr)
	}
	// Not asserting Payload here: per DecodeSync's contract, a body-level
	// decode failure still returns a zero-value SyncPacket alongside the
	// error (only a header-level failure or an unknown type yields a true
	// nil Payload), so DecodeErr is what a caller must check, not Payload.
	if string(rec.Raw) != string(malformed) {
		t.Errorf("Raw = % x, want % x (raw bytes must survive a decode failure)", rec.Raw, malformed)
	}

	// The listener must still be alive: send a valid packet next and
	// confirm it is received and decoded normally.
	valid, err := EncodeSync(SyncPacket{Action: SyncActionStop, FileType: SyncFileTypeMedia, Filename: "audio.mp3"})
	if err != nil {
		t.Fatalf("EncodeSync() error = %v", err)
	}
	sendTo(t, l, valid)
	rec2 := recvOne(t, recvCh)
	if rec2.DecodeErr != nil {
		t.Fatalf("after malformed packet, next valid packet DecodeErr = %v, want nil", rec2.DecodeErr)
	}

	stats := l.Stats()
	if stats.PacketsReceived != 2 {
		t.Errorf("Stats().PacketsReceived = %d, want 2", stats.PacketsReceived)
	}
	if stats.Malformed != 1 {
		t.Errorf("Stats().Malformed = %d, want 1", stats.Malformed)
	}
	if stats.DecodedOK != 1 {
		t.Errorf("Stats().DecodedOK = %d, want 1", stats.DecodedOK)
	}
}

// --- Not-an-FPPD-packet noise: also counted, also does not kill the
// listener, and counted distinctly from a malformed FPPD packet ---

func TestListener_NotFPPDNoiseCountedSeparatelyFromMalformed(t *testing.T) {
	l, recvCh, _ := startTestListener(t, nil)

	noise := []byte("not an fppd packet at all")
	sendTo(t, l, noise)

	rec := recvOne(t, recvCh)
	if !errors.Is(rec.DecodeErr, ErrNotFPPD) {
		t.Errorf("DecodeErr = %v, want errors.Is(_, ErrNotFPPD)", rec.DecodeErr)
	}
	if rec.Header != (Header{}) {
		t.Errorf("Header = %+v, want zero value when the datagram is not FPPD at all", rec.Header)
	}

	stats := l.Stats()
	if stats.NotFPPD != 1 {
		t.Errorf("Stats().NotFPPD = %d, want 1", stats.NotFPPD)
	}
	if stats.Malformed != 0 {
		t.Errorf("Stats().Malformed = %d, want 0 (not-FPPD noise must not count as malformed)", stats.Malformed)
	}
}

// --- Unknown packet type: reported and counted distinctly from malformed ---

func TestListener_UnknownPacketTypeReportedDistinctly(t *testing.T) {
	l, recvCh, _ := startTestListener(t, nil)

	// Type 0x02 is FPP's deprecated legacy Event type: a well-formed FPPD
	// header (magic, type, zero-length extra data all consistent), just a
	// type this package's Decode does not have a payload decoder for.
	unknownType := []byte{'F', 'P', 'P', 'D', 0x02, 0x00, 0x00}
	sendTo(t, l, unknownType)

	rec := recvOne(t, recvCh)
	var unk *UnknownPacketTypeError
	if !errors.As(rec.DecodeErr, &unk) {
		t.Fatalf("DecodeErr = %v (%T), want *UnknownPacketTypeError", rec.DecodeErr, rec.DecodeErr)
	}
	if unk.Type != PacketType(0x02) {
		t.Errorf("UnknownPacketTypeError.Type = %v, want 0x02", unk.Type)
	}
	// The header itself is well-formed, so it should still be reported.
	if rec.Header.Type != PacketType(0x02) {
		t.Errorf("Header.Type = %v, want 0x02 (header decodes even though the type is unrecognized)", rec.Header.Type)
	}

	stats := l.Stats()
	if stats.UnknownType != 1 {
		t.Errorf("Stats().UnknownType = %d, want 1", stats.UnknownType)
	}
	if stats.Malformed != 0 || stats.NotFPPD != 0 {
		t.Errorf("Stats() = %+v, want Malformed=0 NotFPPD=0 (an unknown type is neither)", stats)
	}
}

// --- Clean, deterministic shutdown on context cancellation ---

func TestListener_ContextCancellation_ShutsDownCleanly(t *testing.T) {
	cfg := ListenerConfig{ListenAddr: "127.0.0.1:0"}
	l, err := NewListener(cfg)
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- l.Run(ctx, func(Received) {})
	}()

	// Give Run a moment to actually enter its read loop before cancelling,
	// so this exercises cancellation of an in-progress blocking read, not
	// just a ctx that was already done before Run started.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run() returned %v after context cancellation, want nil", err)
		}
	case <-time.After(runTimeout):
		t.Fatalf("Run() did not return within %s of context cancellation; shutdown is not prompt", runTimeout)
	}
}

// TestListener_Run_RejectsNilHandler guards against a caller mistake that
// would otherwise panic deep in the read loop on the first packet instead
// of failing immediately and legibly at the call that omitted the handler.
func TestListener_Run_RejectsNilHandler(t *testing.T) {
	l, err := NewListener(ListenerConfig{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	defer func() { _ = l.Close() }()

	if err := l.Run(context.Background(), nil); err == nil {
		t.Fatal("Run(ctx, nil) error = nil, want an error")
	}
}

// --- SHOULD FIX 6: Close must be idempotent and release resources on every
// Run return path ---

func TestListener_Close_IsIdempotent(t *testing.T) {
	l, err := NewListener(ListenerConfig{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}

	if err := l.Close(); err != nil {
		t.Fatalf("first Close() error = %v, want nil", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil (idempotent)", err)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("third Close() error = %v, want nil (idempotent)", err)
	}
}

func TestListener_Close_UnblocksRunAndReleasesResources(t *testing.T) {
	l, err := NewListener(ListenerConfig{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- l.Run(context.Background(), func(Received) {})
	}()

	time.Sleep(50 * time.Millisecond) // let Run enter its read loop
	if err := l.Close(); err != nil {
		t.Fatalf("Close() error = %v, want nil", err)
	}

	select {
	case err := <-done:
		// ctx was never cancelled, so Run must report the closed-socket
		// read error rather than nil: Close on its own is not the "clean
		// shutdown" signal that context cancellation is.
		if err == nil {
			t.Error("Run() returned nil after an external Close() with no context cancellation, want a read error")
		}
	case <-time.After(runTimeout):
		t.Fatalf("Run() did not return within %s of Close(); resources were not released on this return path", runTimeout)
	}

	// Close must remain safe, and idempotent, after Run has already
	// returned on its own.
	if err := l.Close(); err != nil {
		t.Errorf("Close() after Run returned: error = %v, want nil", err)
	}
}

// --- Interface selection: report, don't guess ---

func TestNewListener_UnknownNamedInterface_ReturnsError(t *testing.T) {
	_, err := NewListener(ListenerConfig{
		ListenAddr:    "127.0.0.1:0",
		InterfaceName: "showmesh-test-nonexistent-iface-0",
	})
	if err == nil {
		t.Fatal("NewListener() with a nonexistent named interface: error = nil, want an error")
	}
}

// --- Transport classification: pure logic, independent of any real socket
// or platform control-message support, so it cannot flake in CI ---

func TestClassifyTransport(t *testing.T) {
	l := &Listener{
		localUnicast:   map[string]struct{}{"192.168.1.50": {}},
		localBroadcast: map[string]struct{}{"192.168.1.255": {}},
	}

	tests := []struct {
		name string
		dst  net.IP
		want Transport
	}{
		{"nil destination is unknown, not guessed", nil, TransportUnknown},
		{"multicast group", net.ParseIP(MulticastGroup), TransportMulticast},
		{"limited broadcast", net.IPv4bcast, TransportBroadcast},
		{"local unicast address", net.ParseIP("192.168.1.50"), TransportUnicast},
		{"subnet-directed broadcast address", net.ParseIP("192.168.1.255"), TransportBroadcast},
		{"unrelated address is unknown", net.ParseIP("10.0.0.9"), TransportUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := l.classifyTransport(tt.dst); got != tt.want {
				t.Errorf("classifyTransport(%v) = %v, want %v", tt.dst, got, tt.want)
			}
		})
	}
}

// --- BLOCKER 1: the discover-ping responder must reply to FPPCtrlPort, not
// to the datagram's source port ---

func TestNewListener_DiscoverReplyPortDefaultsToFPPCtrlPort(t *testing.T) {
	l, err := NewListener(ListenerConfig{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	defer func() { _ = l.Close() }()

	if l.discoverReplyPort != FPPCtrlPort {
		t.Errorf("discoverReplyPort = %d, want %d (FPPCtrlPort)", l.discoverReplyPort, FPPCtrlPort)
	}
}

func TestDefaultDiscoverResponseDelay_WithinFPPsZeroToFiveMillisecondWindow(t *testing.T) {
	for i := 0; i < 50; i++ {
		d := defaultDiscoverResponseDelay()
		if d < 0 || d > 5*time.Millisecond {
			t.Fatalf("defaultDiscoverResponseDelay() = %v, want within [0, 5ms]", d)
		}
	}
}

// TestListener_DiscoverPingResponder_RepliesToFPPCtrlPortNotSourcePort
// reproduces BLOCKER 1 directly: FPP's own MultiSync.cpp never binds the
// sockets it sends a discover ping from, so a real discover ping arrives
// here FROM an ephemeral source port, and a reply must go to FPPCtrlPort
// regardless of that port, or it lands on a socket nothing is listening on.
//
// Tests must not bind port 32320 (FPPCtrlPort itself), so this uses the
// unexported discoverReplyPort seam (defaults to FPPCtrlPort in production,
// see TestNewListener_DiscoverReplyPortDefaultsToFPPCtrlPort above) to
// redirect the SUT's reply to a capture socket this test controls, so the
// destination address the fix computes can actually be observed. The field
// is set before Run starts (not via startTestListener, which starts Run
// immediately) so there is no data race between this goroutine's write and
// the listener's read goroutine.
func TestListener_DiscoverPingResponder_RepliesToFPPCtrlPortNotSourcePort(t *testing.T) {
	capture, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatalf("ListenUDP() error = %v", err)
	}
	defer func() { _ = capture.Close() }()

	l, err := NewListener(ListenerConfig{
		ListenAddr:             "127.0.0.1:0",
		RespondToDiscoverPings: true,
		DiscoverResponseDelay:  func() time.Duration { return 0 }, // no need to sleep in a test
	})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	// Safe to mutate here: Run has not started yet, so no other goroutine
	// can be reading this field concurrently.
	l.discoverReplyPort = capture.LocalAddr().(*net.UDPAddr).Port

	recvCh := make(chan Received, 64)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- l.Run(ctx, func(r Received) { recvCh <- r })
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run() returned error on shutdown: %v", err)
			}
		case <-time.After(runTimeout):
			t.Fatalf("Run() did not return within %s of context cancellation", runTimeout)
		}
	})

	cases := []struct {
		name    string
		srcPort int
	}{
		{"OS-assigned ephemeral source port", 0},
		{"an explicit, arbitrary high source port", 54321},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			reqConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: tc.srcPort})
			if err != nil {
				t.Fatalf("ListenUDP() error = %v", err)
			}
			defer func() { _ = reqConn.Close() }()

			discover, err := EncodePing(PingPacket{SubType: PingSubTypeDiscover, SystemType: SystemTypeOther})
			if err != nil {
				t.Fatalf("EncodePing() error = %v", err)
			}
			if _, err := reqConn.WriteToUDP(discover, l.LocalAddr().(*net.UDPAddr)); err != nil {
				t.Fatalf("WriteToUDP() error = %v", err)
			}

			recvOne(t, recvCh) // wait until the SUT has processed the discover ping

			if err := capture.SetReadDeadline(time.Now().Add(recvTimeout)); err != nil {
				t.Fatalf("SetReadDeadline() error = %v", err)
			}
			buf := make([]byte, maxDatagramSize)
			n, _, err := capture.ReadFromUDP(buf)
			if err != nil {
				t.Fatalf("ReadFromUDP() on the capture socket: %v (the response was not sent to the reply port, regardless of the request's source port %d)", err, tc.srcPort)
			}

			h, payload, err := Decode(buf[:n])
			if err != nil {
				t.Fatalf("Decode(response) error = %v", err)
			}
			if h.Type != PacketTypePing {
				t.Fatalf("response Header.Type = %v, want PacketTypePing", h.Type)
			}
			resp, ok := payload.(PingPacket)
			if !ok || resp.SubType != PingSubTypePing {
				t.Fatalf("response payload = %+v, want a non-discover Ping", payload)
			}
		})
	}
}

// --- BLOCKER 4: SO_REUSEPORT must be opt-in and default off ---

func TestListener_AllowPortSharing_DefaultOffPreventsSecondBindOnSamePort(t *testing.T) {
	l1, err := NewListener(ListenerConfig{ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	defer func() { _ = l1.Close() }()

	addr := l1.LocalAddr().(*net.UDPAddr).String()

	// Default (AllowPortSharing false): a second listener on the exact
	// same address must fail to bind. This is the loud, correct failure
	// BLOCKER 4 asks for, instead of a silent coexistence hazard that can
	// steal fppd's own unicast MultiSync traffic.
	if l2, err := NewListener(ListenerConfig{ListenAddr: addr}); err == nil {
		_ = l2.Close()
		t.Fatal("second NewListener() on the same address with AllowPortSharing=false: error = nil, want a bind error")
	}
}

func TestListener_AllowPortSharing_EnabledAllowsSecondBindOnSamePort(t *testing.T) {
	l1, err := NewListener(ListenerConfig{ListenAddr: "127.0.0.1:0", AllowPortSharing: true})
	if err != nil {
		t.Fatalf("NewListener() error = %v", err)
	}
	defer func() { _ = l1.Close() }()

	addr := l1.LocalAddr().(*net.UDPAddr).String()

	l2, err := NewListener(ListenerConfig{ListenAddr: addr, AllowPortSharing: true})
	if err != nil {
		t.Fatalf("second NewListener() with AllowPortSharing=true on the same address: error = %v, want nil (same-process, same-UID coexistence must work)", err)
	}
	defer func() { _ = l2.Close() }()
}
