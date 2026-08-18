package agent

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/multisync"
)

// TestRunMultiSyncListenerBindFailureSetsStatus is finding 7's second-half
// regression test: before status existed, a bind failure was ONLY a log
// line, so a node's render report could carry PipelineState=="running" and
// climbing FramesWritten while the timeline sat frozen at StateUnknown all
// night. Occupies a fixed local UDP address first so multisync.NewListener's
// own bind is guaranteed to fail (AllowPortSharing is never true here — see
// runMultiSyncListener's doc comment), then asserts status reflects that
// failure with a real reason. Revert the status.set(false, reason) call in
// multisync.go's bind-failure branch to see this fail.
func TestRunMultiSyncListenerBindFailureSetsStatus(t *testing.T) {
	occupied, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("occupy a listen address: %v", err)
	}
	defer func() { _ = occupied.Close() }()
	addr := occupied.LocalAddr().String()

	status := newMultiSyncStatus()
	timeline := multisync.NewTimeline(time.Now, multisync.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Bind failure returns synchronously, before runMultiSyncListener ever
	// reaches its blocking Run call, so no goroutine is needed here.
	runMultiSyncListener(ctx, addr, "", timeline, status, discardLogger())

	// afterCall is captured AFTER runMultiSyncListener already returned, so
	// a correctly-stamped observedAt (set DURING that call) must be at or
	// before it.
	afterCall := time.Now()
	listening, reason, observedAt := status.get()
	if listening {
		t.Fatalf("listening = true after binding onto an already-occupied address, want false")
	}
	// newMultiSyncStatus's own zero-state default is ALSO listening=false
	// with a non-empty reason ("has not attempted to bind yet"), so
	// asserting reason != "" alone would pass even with no fix at all —
	// the reason must specifically name the bind attempt/address to prove
	// status.set was actually called with the real outcome.
	if !strings.Contains(reason, addr) {
		t.Fatalf("reason = %q, want it to name the address that failed to bind (%q) — a generic or default reason means status.set was never called with the real bind error", reason, addr)
	}
	// observedAt must be a real, recent timestamp (finding 7's coordinator
	// half reads this as the node-reported evidence time), never the zero
	// value newMultiSyncStatus's own default carries before any set call.
	if observedAt.IsZero() {
		t.Fatalf("observedAt is zero after a real bind attempt, want a real timestamp")
	}
	if observedAt.After(afterCall) {
		t.Fatalf("observedAt %s is after the call that produced it returned (%s)", observedAt, afterCall)
	}
}

// TestRunMultiSyncListenerSuccessSetsStatusListening proves the positive
// path too: a real bind (an ephemeral port, guaranteed free) sets
// listening=true with no reason, so a healthy node's render report does
// not carry a stale "not yet attempted" or false-degraded status forever.
func TestRunMultiSyncListenerSuccessSetsStatusListening(t *testing.T) {
	status := newMultiSyncStatus()
	timeline := multisync.NewTimeline(time.Now, multisync.Config{})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// listenAddr="" resolves to an OS-chosen port inside multisync.
	// NewListener via net.ListenConfig with port 0, so this cannot collide
	// with anything else on the machine running this test.
	done := make(chan struct{})
	go func() {
		defer close(done)
		runMultiSyncListener(ctx, "127.0.0.1:0", "", timeline, status, discardLogger())
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if listening, _, _ := status.get(); listening {
			cancel()
			<-done
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	<-done
	t.Fatalf("status never reported listening=true for a real bind")
}
