package agent

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// fakeClock lets tests drive runHeartbeat's timestamps deterministically,
// matching the pattern internal/coordinator/broker's tests already use.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// drainHeartbeat runs runHeartbeat in a goroutine, sends n ticks (advancing
// clock by HeartbeatInterval before each), waits for each resulting publish
// via pub.notify (a real synchronization point, not a sleep), then cancels
// ctx and waits for the loop to exit before returning.
func drainHeartbeat(t *testing.T, pub *fakePublisher, nodeID, bootID string, startedAt time.Time, clock *fakeClock, n int) {
	t.Helper()

	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// nil: this helper only drives the tick-triggered path; see
		// TestRunHeartbeatConnectTrigger for the connect-triggered one. A
		// nil receive-only channel is never ready in a select, so this is
		// simply "no connect triggers", not an error.
		runHeartbeat(ctx, pub, nodeID, bootID, startedAt, clock.now, ticks, nil, discardLogger())
	}()

	for i := 0; i < n; i++ {
		clock.advance(HeartbeatInterval)
		select {
		case ticks <- clock.t:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out sending tick %d", i)
		}
		select {
		case <-pub.notify:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for publish notification for tick %d", i)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("runHeartbeat did not return after ctx cancellation")
	}
}

func decodeHealth(t *testing.T, payload []byte) (mqttproto.Envelope, mqttproto.HealthPayload) {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	health, err := mqttproto.DecodeHealthPayload(env)
	if err != nil {
		t.Fatalf("DecodeHealthPayload() error = %v", err)
	}
	return env, health
}

func TestRunHeartbeatSequenceIncrementsMonotonically(t *testing.T) {
	pub := newFakePublisher()
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	startedAt := clock.t

	drainHeartbeat(t, pub, "media-03", "boot-1", startedAt, clock, 3)

	calls := pub.snapshot()
	if len(calls) != 3 {
		t.Fatalf("len(calls) = %d, want 3", len(calls))
	}

	wantTopic, err := mqttproto.ObservedTopic("media-03", "health")
	if err != nil {
		t.Fatalf("ObservedTopic() error = %v", err)
	}

	for i, c := range calls {
		if c.topic != wantTopic {
			t.Errorf("call %d topic = %q, want %q", i, c.topic, wantTopic)
		}
		if c.qos != mqttproto.ObservedDeliveryPolicy.QoS {
			t.Errorf("call %d qos = %d, want %d", i, c.qos, mqttproto.ObservedDeliveryPolicy.QoS)
		}
		if c.retain != mqttproto.ObservedDeliveryPolicy.Retain {
			t.Errorf("call %d retain = %v, want %v", i, c.retain, mqttproto.ObservedDeliveryPolicy.Retain)
		}

		_, health := decodeHealth(t, c.payload)
		if health.Sequence != uint64(i) {
			t.Errorf("call %d Sequence = %d, want %d", i, health.Sequence, i)
		}
		if health.BootID != "boot-1" {
			t.Errorf("call %d BootID = %q, want %q", i, health.BootID, "boot-1")
		}
		if health.AgentState != agentStateRunning {
			t.Errorf("call %d AgentState = %q, want %q", i, health.AgentState, agentStateRunning)
		}
		if health.UptimeMS < 0 {
			t.Errorf("call %d UptimeMS = %d, want >= 0", i, health.UptimeMS)
		}
	}

	// Uptime must increase from one heartbeat to the next, since the clock
	// advances by HeartbeatInterval between each.
	_, first := decodeHealth(t, calls[0].payload)
	_, last := decodeHealth(t, calls[2].payload)
	if last.UptimeMS <= first.UptimeMS {
		t.Errorf("UptimeMS did not increase across ticks: first=%d last=%d", first.UptimeMS, last.UptimeMS)
	}
}

func TestRunHeartbeatBootIDDiffersAcrossInstances(t *testing.T) {
	clock1 := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	pub1 := newFakePublisher()
	drainHeartbeat(t, pub1, "media-03", "boot-aaa", clock1.t, clock1, 1)

	clock2 := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	pub2 := newFakePublisher()
	drainHeartbeat(t, pub2, "media-03", "boot-bbb", clock2.t, clock2, 1)

	_, h1 := decodeHealth(t, pub1.snapshot()[0].payload)
	_, h2 := decodeHealth(t, pub2.snapshot()[0].payload)

	if h1.BootID == h2.BootID {
		t.Fatalf("BootID = %q for both instances, want them to differ", h1.BootID)
	}
	if h1.BootID != "boot-aaa" || h2.BootID != "boot-bbb" {
		t.Errorf("BootID not threaded through unchanged: got %q and %q", h1.BootID, h2.BootID)
	}
}

// TestRunHeartbeatPublishFailureDoesNotStopLaterTicks is the test the Task
// D spec calls out explicitly: a transient publish failure on one tick
// must not wedge the loop or stop it from publishing on the next tick.
func TestRunHeartbeatPublishFailureDoesNotStopLaterTicks(t *testing.T) {
	pub := newFakePublisher()
	pub.failOn = map[int]bool{1: true} // the second tick's publish fails

	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	drainHeartbeat(t, pub, "media-03", "boot-1", clock.t, clock, 3)

	calls := pub.snapshot()
	if len(calls) != 3 {
		t.Fatalf("len(calls) = %d, want 3 (a failed publish attempt must still be followed by later ticks)", len(calls))
	}

	// Sequence must still increment across the failure: 0, 1, 2, not 0, 1
	// (retried), 1 again.
	for i, c := range calls {
		_, health := decodeHealth(t, c.payload)
		if health.Sequence != uint64(i) {
			t.Errorf("call %d Sequence = %d, want %d (failure must not stall Sequence)", i, health.Sequence, i)
		}
	}
}

func TestRunHeartbeatReturnsOnContextDone(t *testing.T) {
	pub := newFakePublisher()
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, pub, "media-03", "boot-1", time.Now(), time.Now, ticks, nil, discardLogger())
	}()

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("runHeartbeat did not return promptly after ctx cancellation")
	}
}

// TestRunHeartbeatConnectTrigger verifies the round-2 review's fix: a
// signal on the connected channel produces an immediate heartbeat publish
// without waiting for the next tick, and Sequence stays monotonic and
// gapless across connect- and tick-triggered publishes — a reconnect is
// not a new boot, so it must not reset or reuse a sequence number either.
// See runHeartbeat's doc comment on its connected parameter for the gap
// this closes: without it, a node the coordinator just received live
// presence evidence for still reads `unknown` for a full HeartbeatInterval.
func TestRunHeartbeatConnectTrigger(t *testing.T) {
	pub := newFakePublisher()
	ticks := make(chan time.Time)
	connected := make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	startedAt := clock.t

	done := make(chan struct{})
	go func() {
		defer close(done)
		runHeartbeat(ctx, pub, "media-03", "boot-1", startedAt, clock.now, ticks, connected, discardLogger())
	}()

	// A connect signal, with no tick having fired yet, must produce a
	// publish immediately: this is the fix itself.
	sendAndAwait(t, connected, struct{}{}, pub, "connect trigger")

	// A regular tick afterward must still work, continuing Sequence from
	// where the connect-triggered publish left off rather than resetting or
	// reusing it.
	clock.advance(HeartbeatInterval)
	sendAndAwait(t, ticks, clock.t, pub, "tick")

	// A second connect signal (simulating a reconnect) must also publish
	// immediately, again continuing Sequence: BootID is unchanged across a
	// reconnect, so Sequence must not restart either.
	sendAndAwait(t, connected, struct{}{}, pub, "second connect trigger")

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runHeartbeat did not return after ctx cancellation")
	}

	calls := pub.snapshot()
	if len(calls) != 3 {
		t.Fatalf("len(calls) = %d, want 3 (connect, tick, connect)", len(calls))
	}
	for i, c := range calls {
		_, health := decodeHealth(t, c.payload)
		if health.Sequence != uint64(i) {
			t.Errorf("call %d Sequence = %d, want %d (connect and tick triggers must share one monotonic counter)", i, health.Sequence, i)
		}
		if health.BootID != "boot-1" {
			t.Errorf("call %d BootID = %q, want %q (a connect trigger must not be treated as a new boot)", i, health.BootID, "boot-1")
		}
	}
}

// TestUptimeMSClampsNegativeToZero covers the case the round-2 review
// flagged: publishOneHeartbeat derives UptimeMS from sentAt.Sub(startedAt)
// using an injected wall clock (necessarily so, for test determinism — see
// uptimeMS's doc comment for why no monotonic clock reading is available
// here), and a wall clock stepping backward (NTP correction, manual
// adjustment) must not produce a negative UptimeMS. The existing
// tick-driven tests only ever advance a fake clock forward, so they could
// never have caught this; this test exercises uptimeMS directly against
// the backward case instead.
func TestUptimeMSClampsNegativeToZero(t *testing.T) {
	base := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		startedAt time.Time
		sentAt    time.Time
		want      int64
	}{
		{name: "normal forward elapsed", startedAt: base, sentAt: base.Add(5 * time.Second), want: 5000},
		{name: "zero elapsed", startedAt: base, sentAt: base, want: 0},
		{name: "wall clock stepped backward", startedAt: base, sentAt: base.Add(-5 * time.Second), want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := uptimeMS(tt.startedAt, tt.sentAt); got != tt.want {
				t.Errorf("uptimeMS(%v, %v) = %d, want %d", tt.startedAt, tt.sentAt, got, tt.want)
			}
		})
	}
}

// sendAndAwait sends value on ch, then waits for the resulting publish
// notification on pub.notify, both bounded so a stuck runHeartbeat fails
// the test instead of hanging it. what names what was sent, for the
// failure message only.
func sendAndAwait[T any](t *testing.T, ch chan T, value T, pub *fakePublisher, what string) {
	t.Helper()
	select {
	case ch <- value:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out sending %s", what)
	}
	select {
	case <-pub.notify:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the publish triggered by %s", what)
	}
}
