package broker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

// testLogger returns a *slog.Logger that discards everything, so tests
// exercising the error-logging paths below don't spam test output.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeClock lets tests drive BrokerManager's timestamps deterministically,
// without real sleeps.
type fakeClock struct {
	t time.Time
}

func (c *fakeClock) now() time.Time { return c.t }

func (c *fakeClock) advance(d time.Duration) { c.t = c.t.Add(d) }

// newTestBrokerManager builds a BrokerManager whose state-tracking logic
// (setConnected/State) can be exercised directly, without dialing a real
// broker: cm is left nil, which is fine since these tests never call
// anything that touches it.
func newTestBrokerManager(clock *fakeClock) *BrokerManager {
	initAt := clock.now()
	return &BrokerManager{
		now:   clock.now,
		state: BrokerState{Connected: false, Since: initAt, ObservedAt: initAt},
	}
}

func TestBrokerManagerSetConnectedMovesSinceOnlyOnChange(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	bm := newTestBrokerManager(clock)

	initial := bm.State()

	// Re-confirm the same value (false) at a later time: ObservedAt should
	// advance, Since should not move.
	clock.advance(5 * time.Second)
	bm.setConnected(false)

	afterReconfirm := bm.State()
	if !afterReconfirm.Since.Equal(initial.Since) {
		t.Errorf("Since = %v after re-confirming the same value, want unchanged %v", afterReconfirm.Since, initial.Since)
	}
	if !afterReconfirm.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", afterReconfirm.ObservedAt, clock.now())
	}
	if afterReconfirm.ObservedAt.Equal(initial.ObservedAt) {
		t.Errorf("ObservedAt did not advance on re-confirmation")
	}

	// Now actually change the value: Since must move to the new time.
	clock.advance(5 * time.Second)
	bm.setConnected(true)

	afterChange := bm.State()
	if !afterChange.Connected {
		t.Fatalf("Connected = false, want true")
	}
	if !afterChange.Since.Equal(clock.now()) {
		t.Errorf("Since = %v after a real transition, want %v", afterChange.Since, clock.now())
	}
	if !afterChange.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", afterChange.ObservedAt, clock.now())
	}

	// Re-confirm the new value (true): Since should stay at the transition
	// time, only ObservedAt should move.
	sinceAfterChange := afterChange.Since
	clock.advance(5 * time.Second)
	bm.setConnected(true)

	afterSecondReconfirm := bm.State()
	if !afterSecondReconfirm.Since.Equal(sinceAfterChange) {
		t.Errorf("Since = %v after re-confirming true, want unchanged %v", afterSecondReconfirm.Since, sinceAfterChange)
	}
	if !afterSecondReconfirm.ObservedAt.Equal(clock.now()) {
		t.Errorf("ObservedAt = %v, want %v", afterSecondReconfirm.ObservedAt, clock.now())
	}
}

func TestBrokerManagerStateInitiallyDisconnected(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	bm := newTestBrokerManager(clock)

	state := bm.State()
	if state.Connected {
		t.Errorf("Connected = true before any observation, want false")
	}
	if state.ObservedAt.IsZero() {
		t.Errorf("ObservedAt is zero, want an initial timestamp so freshness is well-defined from construction")
	}
}

// The Readiness tests below exercise the staleness rule that used to live
// in the httpapi package's handleReadyz (back when BrokerState was read
// directly by the HTTP server); it now lives here because BrokerManager is
// the thing that owns evidenceStalenessWindow and the mechanism that detects
// loss. httpapi/server_test.go covers the generic "Server threads a
// readiness.Source's report into the response" behavior instead.

func TestBrokerManagerReadinessNotConnected(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	bm := newTestBrokerManager(clock)

	report := bm.Readiness()
	if report.Ready {
		t.Errorf("Ready = true, want false when never connected")
	}
	if report.Reason != "mqtt broker not connected" {
		t.Errorf("Reason = %q, want %q", report.Reason, "mqtt broker not connected")
	}
}

func TestBrokerManagerReadinessConnectedFresh(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	bm := newTestBrokerManager(clock)
	bm.setConnected(true)

	report := bm.Readiness()
	if !report.Ready {
		t.Errorf("Ready = false, want true for a fresh connected observation")
	}
	if report.Reason != "" {
		t.Errorf("Reason = %q, want empty when Ready", report.Reason)
	}
}

func TestBrokerManagerReadinessConnectedButStale(t *testing.T) {
	clock := &fakeClock{t: time.Now().Add(-(evidenceStalenessWindow + 5*time.Second))}
	bm := newTestBrokerManager(clock)
	bm.setConnected(true) // stamps ObservedAt at the stale clock time

	report := bm.Readiness()
	if report.Ready {
		t.Errorf("Ready = true, want false for stale evidence")
	}
	if report.Reason != "mqtt broker evidence is stale" {
		t.Errorf("Reason = %q, want it to name stale evidence", report.Reason)
	}

	// observedAgeSecs is no longer hand-built into Details: freshness must
	// be derivable from the typed ObservedAt field alone (httpapi's
	// writeNotReady does exactly this), per ADR-011 and
	// readiness.Report.ObservedAt's doc comment.
	if report.ObservedAt.IsZero() {
		t.Fatalf("ObservedAt is zero, want it set so freshness is derivable")
	}
	age := time.Since(report.ObservedAt)
	if age < evidenceStalenessWindow {
		t.Errorf("time.Since(ObservedAt) = %v, want it to reflect the stale age (> %v)", age, evidenceStalenessWindow)
	}
	if report.Details["connected"] != true {
		t.Errorf("Details[connected] = %v, want true", report.Details["connected"])
	}
	if _, ok := report.Details["observedAgeSecs"]; ok {
		t.Errorf("Details contains observedAgeSecs = %v, want it omitted now that ObservedAt is the single source of freshness", report.Details["observedAgeSecs"])
	}
}

// TestBrokerManagerDisconnectWaitsForProbeGoroutine verifies Disconnect
// joins the probe goroutine (started by NewBrokerManager, see wg.Add there)
// rather than just canceling its context and returning immediately: the
// WaitGroup must be a real join, not decoration that reads like
// synchronization without being one.
func TestBrokerManagerDisconnectWaitsForProbeGoroutine(t *testing.T) {
	bm := &BrokerManager{now: time.Now}

	probeCtx, cancelProbe := context.WithCancel(context.Background())
	defer cancelProbe()

	probeExited := make(chan struct{})
	bm.wg.Add(1)
	go func() {
		defer bm.wg.Done()
		<-probeCtx.Done()
		// A small delay models real work happening between "ctx canceled"
		// and "goroutine actually finished" so this test can distinguish a
		// real join from Disconnect merely observing ctx cancellation.
		time.Sleep(10 * time.Millisecond)
		close(probeExited)
	}()

	cancelProbe()

	disconnectCtx, cancelDisconnect := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelDisconnect()
	if err := bm.Disconnect(disconnectCtx); err != nil {
		t.Fatalf("Disconnect returned error: %v", err)
	}

	select {
	case <-probeExited:
	default:
		t.Fatalf("Disconnect returned before the probe goroutine exited; wg is not being joined")
	}
}

// TestBrokerManagerDisconnectBoundedByCtxOnHungProbe verifies a probe
// goroutine that never exits (e.g. because the caller failed to cancel the
// ctx passed to NewBrokerManager) cannot block Disconnect past its own
// ctx's deadline.
func TestBrokerManagerDisconnectBoundedByCtxOnHungProbe(t *testing.T) {
	bm := &BrokerManager{now: time.Now}

	// Simulate a hung probe goroutine: it never returns, so wg never
	// reaches zero.
	bm.wg.Add(1)

	disconnectCtx, cancelDisconnect := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelDisconnect()

	start := time.Now()
	if err := bm.Disconnect(disconnectCtx); err != nil {
		t.Fatalf("Disconnect returned error: %v", err)
	}
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Errorf("Disconnect took %v to return with a hung probe goroutine, want it bounded by ctx's deadline", elapsed)
	}
}

// fakeSubscriber records every Subscribe call it receives, standing in for
// *autopaho.ConnectionManager so subscribeAll's re-establish behavior can
// be tested without a real broker connection.
type fakeSubscriber struct {
	calls    int
	lastOpts []paho.SubscribeOptions
	err      error
}

func (f *fakeSubscriber) Subscribe(_ context.Context, s *paho.Subscribe) (*paho.Suback, error) {
	f.calls++
	f.lastOpts = s.Subscriptions
	return nil, f.err
}

func TestSubscriptionsToOptionsLeavesRetainAsPublishedOff(t *testing.T) {
	opts := subscriptionsToOptions([]Subscription{
		{Filter: "showmesh/nodes/+/hello", QoS: 1},
		{Filter: "showmesh/nodes/+/lwt", QoS: 1},
	})
	if len(opts) != 2 {
		t.Fatalf("len(opts) = %d, want 2", len(opts))
	}
	for _, o := range opts {
		if o.RetainAsPublished {
			t.Errorf("RetainAsPublished = true for %q, want false: per the shared contract the coordinator must be able to tell a retained replay from a live publish", o.Topic)
		}
	}
	if opts[0].Topic != "showmesh/nodes/+/hello" || opts[0].QoS != 1 {
		t.Errorf("opts[0] = %+v, did not preserve Filter/QoS", opts[0])
	}
}

func TestSubscribeAllSendsOneSubscribeCallPerInvocation(t *testing.T) {
	sub := &fakeSubscriber{}
	opts := subscriptionsToOptions([]Subscription{{Filter: "a/+/hello", QoS: 1}})

	subscribeAll(context.Background(), sub, opts, testLogger())
	if sub.calls != 1 {
		t.Fatalf("calls = %d, want 1", sub.calls)
	}
	if len(sub.lastOpts) != 1 || sub.lastOpts[0].Topic != "a/+/hello" {
		t.Errorf("lastOpts = %+v, want the single hello filter", sub.lastOpts)
	}
}

// TestSubscribeAllReestablishesOnEachCall simulates OnConnectionUp firing
// twice — once for the initial connection, once for a reconnect after a
// broker outage — and checks that subscribeAll sends the complete
// subscription set again both times rather than assuming anything about
// prior state. This is the unit-testable half of "subscriptions must be
// re-established after a reconnect": it proves subscribeAll itself is
// reconnect-safe; it cannot prove autopaho actually calls OnConnectionUp on
// every reconnect, since that requires a real broker connection (an
// integration-test concern, out of scope for this task per its spec).
func TestSubscribeAllReestablishesOnEachCall(t *testing.T) {
	sub := &fakeSubscriber{}
	opts := subscriptionsToOptions([]Subscription{{Filter: "a/+/hello", QoS: 1}})

	subscribeAll(context.Background(), sub, opts, testLogger()) // initial connect
	subscribeAll(context.Background(), sub, opts, testLogger()) // simulated reconnect

	if sub.calls != 2 {
		t.Errorf("calls = %d, want 2 (one per OnConnectionUp firing)", sub.calls)
	}
}

func TestSubscribeAllNoopOnEmptySubscriptions(t *testing.T) {
	sub := &fakeSubscriber{}
	subscribeAll(context.Background(), sub, nil, testLogger())
	if sub.calls != 0 {
		t.Errorf("calls = %d, want 0 for an empty subscription list", sub.calls)
	}
}

func TestSubscribeAllLogsAndDoesNotPanicOnSubscribeError(t *testing.T) {
	sub := &fakeSubscriber{err: errors.New("boom")}
	opts := subscriptionsToOptions([]Subscription{{Filter: "a/+/hello", QoS: 1}})

	// Must not panic; the failure is logged (via testLogger, discarded) and
	// swallowed so it cannot crash the connection callback.
	subscribeAll(context.Background(), sub, opts, testLogger())

	if sub.calls != 1 {
		t.Errorf("calls = %d, want 1 (the attempt still happened)", sub.calls)
	}
}

// TestPublishReceivedHandlerPassesRetainFlag is the unit test for the
// single most important piece of wiring in this file: that the coordinator
// actually reads paho's RETAIN flag off every inbound publish and carries
// it through to [Message.Retained], rather than defaulting it away. Getting
// this wrong means every retained delivery on reconnect looks exactly like
// a live one, which is precisely the failure the shared contract exists to
// prevent.
func TestPublishReceivedHandlerPassesRetainFlag(t *testing.T) {
	var got []Message
	h := newPublishReceivedHandler(func(m Message) { got = append(got, m) })

	cases := []struct {
		name    string
		publish *paho.Publish
	}{
		{"retained", &paho.Publish{Topic: "showmesh/nodes/a/hello", Payload: []byte("x"), Retain: true}},
		{"live", &paho.Publish{Topic: "showmesh/nodes/a/hello", Payload: []byte("y"), Retain: false}},
	}
	for _, tc := range cases {
		handled, err := h(paho.PublishReceived{Packet: tc.publish})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tc.name, err)
		}
		if !handled {
			t.Errorf("%s: handled = false, want true", tc.name)
		}
	}

	if len(got) != 2 {
		t.Fatalf("len(got) = %d, want 2", len(got))
	}
	if !got[0].Retained {
		t.Errorf("retained publish: Retained = false, want true")
	}
	if got[1].Retained {
		t.Errorf("live publish: Retained = true, want false")
	}
	if got[0].Topic != "showmesh/nodes/a/hello" || string(got[0].Payload) != "x" {
		t.Errorf("Topic/Payload not carried through: %+v", got[0])
	}
}

func TestPublishReceivedHandlerToleratesNilHandler(t *testing.T) {
	h := newPublishReceivedHandler(nil)
	handled, err := h(paho.PublishReceived{Packet: &paho.Publish{Topic: "t", Retain: true}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Errorf("handled = false, want true even with a nil handler")
	}
}
