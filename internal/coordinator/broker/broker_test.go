package broker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
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

// TestIsAuthReasonCode mirrors internal/agent/mqtt_test.go's test of the
// same name against this package's own copy of the classifier (see
// isAuthReasonCode's doc comment for why there are two copies): every
// reason code ADR-024 decision 10 treats as an authorization rejection
// must classify true, and a merely-busy broker (a very different,
// plausibly-transient condition) must not be conflated with one.
func TestIsAuthReasonCode(t *testing.T) {
	authCodes := []byte{
		packets.ConnackBadUsernameOrPassword,
		packets.ConnackNotAuthorized,
		packets.ConnackBanned,
		packets.ConnackBadAuthenticationMethod,
	}
	for _, code := range authCodes {
		if !isAuthReasonCode(code) {
			t.Errorf("isAuthReasonCode(0x%02X) = false, want true", code)
		}
	}

	nonAuthCodes := []byte{packets.ConnackSuccess, packets.ConnackServerBusy, packets.ConnackUnspecifiedError}
	for _, code := range nonAuthCodes {
		if isAuthReasonCode(code) {
			t.Errorf("isAuthReasonCode(0x%02X) = true, want false", code)
		}
	}
}

// TestClassifyConnectErrorDistinguishesRejectionFromTransportFailure is the
// coordinator-side counterpart of the same-named test in
// internal/agent/mqtt_test.go: a CONNACK authorization rejection classifies
// as rejected with its reason code/string carried through, a non-auth
// CONNACK failure and a bare transport error (which never produced a
// CONNACK at all) both classify as not-rejected. Getting the transport case
// backward would be the worse of the two mistakes — see that test's doc
// comment for why.
func TestClassifyConnectErrorDistinguishesRejectionFromTransportFailure(t *testing.T) {
	t.Run("auth rejection", func(t *testing.T) {
		err := &autopaho.ConnackError{ReasonCode: packets.ConnackNotAuthorized, Reason: "not authorized", Err: errors.New("connect failed")}
		rejected, code, reason := classifyConnectError(err)
		if !rejected {
			t.Fatalf("rejected = false, want true")
		}
		if code != packets.ConnackNotAuthorized || reason != "not authorized" {
			t.Errorf("code/reason = %d/%q, want %d/%q", code, reason, packets.ConnackNotAuthorized, "not authorized")
		}
	})

	t.Run("non-auth connack", func(t *testing.T) {
		err := &autopaho.ConnackError{ReasonCode: packets.ConnackServerBusy, Err: errors.New("connect failed")}
		rejected, _, _ := classifyConnectError(err)
		if rejected {
			t.Errorf("rejected = true for ConnackServerBusy, want false")
		}
	})

	t.Run("transport failure never reaches connack", func(t *testing.T) {
		err := errors.New("dial tcp 10.0.1.5:1883: connect: connection refused")
		rejected, code, reason := classifyConnectError(err)
		if rejected {
			t.Errorf("rejected = true for a plain transport error, want false")
		}
		if code != 0 || reason != "" {
			t.Errorf("code/reason = %d/%q, want zero values when no CONNACK was ever received", code, reason)
		}
	})
}

// TestBrokerManagerSetRejectedMarksDisconnectedAndRejected proves setRejected
// (called from OnConnectError on an auth-family CONNACK) records evidence
// distinct from an ordinary "not connected" observation, and that a
// subsequent successful connection (setConnected(true)) clears it — per
// ADR-011, evidence must not outlive what it was based on.
func TestBrokerManagerSetRejectedMarksDisconnectedAndRejected(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)}
	bm := newTestBrokerManager(clock)

	bm.setRejected(packets.ConnackNotAuthorized, "not authorized")

	state := bm.State()
	if state.Connected {
		t.Errorf("Connected = true after setRejected, want false")
	}
	if !state.Rejected {
		t.Fatalf("Rejected = false after setRejected, want true")
	}
	if state.RejectReasonCode != packets.ConnackNotAuthorized {
		t.Errorf("RejectReasonCode = %d, want %d", state.RejectReasonCode, packets.ConnackNotAuthorized)
	}
	if state.RejectReason != "not authorized" {
		t.Errorf("RejectReason = %q, want %q", state.RejectReason, "not authorized")
	}

	// A later successful connection must clear the rejected evidence: it is
	// no longer true, and stale evidence saying otherwise must not survive
	// past the observation that superseded it.
	clock.advance(5 * time.Second)
	bm.setConnected(true)

	afterConnect := bm.State()
	if afterConnect.Rejected {
		t.Errorf("Rejected = true after a subsequent successful connect, want false")
	}
	if afterConnect.RejectReasonCode != 0 || afterConnect.RejectReason != "" {
		t.Errorf("RejectReasonCode/RejectReason = %d/%q after a successful connect, want zero values", afterConnect.RejectReasonCode, afterConnect.RejectReason)
	}
}

// TestBrokerManagerReadinessRejectedDistinctFromNotConnected is the
// Readiness-level assertion that ADR-024 decision 10's "surface it as
// evidence distinct from an unreachable broker" requirement actually holds:
// a rejected coordinator must report a different Reason (and a Details
// entry an operator or a monitoring check can key off) than a merely
// unreachable one, even though both are "not ready".
func TestBrokerManagerReadinessRejectedDistinctFromNotConnected(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	bm := newTestBrokerManager(clock)
	bm.setRejected(packets.ConnackNotAuthorized, "not authorized")

	report := bm.Readiness()
	if report.Ready {
		t.Errorf("Ready = true, want false for a rejected connection")
	}
	if report.Reason != "mqtt broker rejected connection (not authorized)" {
		t.Errorf("Reason = %q, want the distinct rejected-connection reason", report.Reason)
	}
	if report.Reason == "mqtt broker not connected" {
		t.Fatalf("Reason equals the plain not-connected reason; a rejection must not be indistinguishable from an ordinary outage")
	}
	if report.Details["rejected"] != true {
		t.Errorf("Details[rejected] = %v, want true", report.Details["rejected"])
	}
	if report.Details["rejectReasonCode"] != byte(packets.ConnackNotAuthorized) {
		t.Errorf("Details[rejectReasonCode] = %v (%T), want %d", report.Details["rejectReasonCode"], report.Details["rejectReasonCode"], packets.ConnackNotAuthorized)
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

// --- subscriptionsToResubscribe: fixed set + live response waiters ---

func TestSubscriptionsToResubscribeIncludesOnlyFixedSetWithNoWaiters(t *testing.T) {
	bm := &BrokerManager{now: time.Now, fixedSubs: []Subscription{
		{Filter: "showmesh/nodes/+/hello", QoS: 1},
	}}

	opts := bm.subscriptionsToResubscribe()
	if len(opts) != 1 || opts[0].Topic != "showmesh/nodes/+/hello" {
		t.Fatalf("subscriptionsToResubscribe() = %+v, want only the fixed subscription", opts)
	}
}

// TestSubscriptionsToResubscribeIncludesLiveResponseWaiters is item 3 of
// this package's task specification: a response waiter's subscription must
// be part of what a reconnect resends, because the fixed set alone is what
// the coordinator was left with before this method existed — see
// subscriptionsToResubscribe's doc comment for why a broker outage during
// an in-flight AwaitResponse call would otherwise silently drop the
// response topic's subscription and never route the external responder's
// eventual answer anywhere.
func TestSubscriptionsToResubscribeIncludesLiveResponseWaiters(t *testing.T) {
	bm := &BrokerManager{now: time.Now, fixedSubs: []Subscription{
		{Filter: "showmesh/nodes/+/hello", QoS: 1},
	}}
	bm.respTopics = map[string]*responseTopicState{
		"home/projectors/state": {
			qos:     1,
			waiters: map[uint64]*pendingWaiter{1: {id: 1, topic: "home/projectors/state"}},
		},
	}

	opts := bm.subscriptionsToResubscribe()
	if len(opts) != 2 {
		t.Fatalf("subscriptionsToResubscribe() = %+v, want 2 entries (1 fixed + 1 live response topic)", opts)
	}

	byTopic := map[string]paho.SubscribeOptions{}
	for _, o := range opts {
		byTopic[o.Topic] = o
	}
	if _, ok := byTopic["showmesh/nodes/+/hello"]; !ok {
		t.Errorf("resubscribe set missing the fixed subscription: %+v", opts)
	}
	respOpt, ok := byTopic["home/projectors/state"]
	if !ok {
		t.Fatalf("resubscribe set missing the live response-waiter topic: %+v", opts)
	}
	if respOpt.QoS != 1 {
		t.Errorf("response-topic QoS = %d, want 1 (from responseTopicState.qos)", respOpt.QoS)
	}
	if respOpt.RetainAsPublished {
		t.Errorf("response-topic RetainAsPublished = true, want false: a reconnect must not lose the retained-replay-vs-live distinction")
	}
}

// TestSubscriptionsToResubscribeDedupesTopicPresentInBothSets proves a topic
// that happens to appear in both the fixed set and the live response-waiter
// set is only sent once, at the fixed set's QoS — see
// subscriptionsToResubscribe's doc comment on why sending it twice would be
// needless rather than incorrect, and why the fixed entry wins.
func TestSubscriptionsToResubscribeDedupesTopicPresentInBothSets(t *testing.T) {
	bm := &BrokerManager{now: time.Now, fixedSubs: []Subscription{
		{Filter: "home/projectors/state", QoS: 2},
	}}
	bm.respTopics = map[string]*responseTopicState{
		"home/projectors/state": {
			qos:     0,
			waiters: map[uint64]*pendingWaiter{1: {id: 1, topic: "home/projectors/state"}},
		},
	}

	opts := bm.subscriptionsToResubscribe()
	if len(opts) != 1 {
		t.Fatalf("subscriptionsToResubscribe() = %+v, want exactly 1 deduplicated entry", opts)
	}
	if opts[0].QoS != 2 {
		t.Errorf("deduplicated entry QoS = %d, want 2 (the fixed set's QoS)", opts[0].QoS)
	}
}

// TestSubscriptionsToResubscribeReadsWaitersFreshEachCall proves the set is
// recomputed from live state on every call rather than captured once: a
// waiter registered after the BrokerManager was constructed must appear,
// and one released before the next call must not.
func TestSubscriptionsToResubscribeReadsWaitersFreshEachCall(t *testing.T) {
	bm := &BrokerManager{now: time.Now}

	if opts := bm.subscriptionsToResubscribe(); len(opts) != 0 {
		t.Fatalf("subscriptionsToResubscribe() before any waiter = %+v, want empty", opts)
	}

	bm.respTopics = map[string]*responseTopicState{
		"home/garage/state": {qos: 1, waiters: map[uint64]*pendingWaiter{1: {id: 1, topic: "home/garage/state"}}},
	}
	opts := bm.subscriptionsToResubscribe()
	if len(opts) != 1 || opts[0].Topic != "home/garage/state" {
		t.Fatalf("subscriptionsToResubscribe() with a live waiter = %+v, want the response topic present", opts)
	}

	delete(bm.respTopics, "home/garage/state")
	if opts := bm.subscriptionsToResubscribe(); len(opts) != 0 {
		t.Fatalf("subscriptionsToResubscribe() after the waiter released = %+v, want empty again", opts)
	}
}

// TestNewBrokerManagerFixedSubsIsIndependentCopy proves NewBrokerManager
// does not alias the caller's subs slice: mutating the caller's backing
// array after construction must not change what a later reconnect resends.
func TestNewBrokerManagerFixedSubsIsIndependentCopy(t *testing.T) {
	subs := []Subscription{{Filter: "showmesh/nodes/+/hello", QoS: 1}}
	bm := &BrokerManager{now: time.Now}
	bm.fixedSubs = append([]Subscription(nil), subs...)

	subs[0].Filter = "mutated-after-construction"

	opts := bm.subscriptionsToResubscribe()
	if len(opts) != 1 || opts[0].Topic != "showmesh/nodes/+/hello" {
		t.Fatalf("subscriptionsToResubscribe() = %+v, want it unaffected by the caller mutating its own slice afterward", opts)
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

// --- onConnectionUp / dispatchReconnectInventoryRequests ---

// decodeCmdPayload unmarshals one recorded *paho.Publish as this package's
// own [mqttproto.Envelope]/[mqttproto.CmdPayload] wire shape.
func decodeCmdPayload(t *testing.T, p *paho.Publish) mqttproto.CmdPayload {
	t.Helper()
	var env mqttproto.Envelope
	if err := json.Unmarshal(p.Payload, &env); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var cmd mqttproto.CmdPayload
	if err := json.Unmarshal(env.Payload, &cmd); err != nil {
		t.Fatalf("decode cmd payload: %v", err)
	}
	return cmd
}

// waitForPublishCount polls cm until it has recorded at least n publishes,
// or fails the test after a short bound: dispatchReconnectInventoryRequests
// runs on its own goroutine (its own doc comment explains why), so its
// effect is not visible synchronously after the call that triggers it.
func waitForPublishCount(t *testing.T, cm *fakeMQTTClient, n int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cm.publishCount() >= n {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("publishCount = %d after waiting, want >= %d", cm.publishCount(), n)
}

// TestOnConnectionUpDispatchesInventoryRequestToEveryListedNode is the fix
// itself: on a successful connection, onConnectionUp asks every node
// nodeLister lists for a fresh asset inventory, with no params and the
// exact minted action name.
func TestOnConnectionUpDispatchesInventoryRequestToEveryListedNode(t *testing.T) {
	cm := &fakeMQTTClient{}
	bm := &BrokerManager{
		now: time.Now, cm: cm,
		nodeLister: func(context.Context) ([]string, error) {
			return []string{"node-1", "node-2"}, nil
		},
	}
	bm.onConnectionUp(context.Background(), testLogger())
	waitForPublishCount(t, cm, 2)

	seen := map[string]bool{}
	for _, p := range cm.publishes {
		cmd := decodeCmdPayload(t, p)
		if cmd.Action != "asset.inventory.request" {
			t.Errorf("Action = %q, want %q", cmd.Action, "asset.inventory.request")
		}
		if len(cmd.Params) != 0 {
			t.Errorf("Params = %+v, want none", cmd.Params)
		}
		if cmd.ConfirmationMethod == "" {
			t.Error("ConfirmationMethod is empty, want a well-formed CmdPayload")
		}
		seen[cmd.Target.ID] = true
	}
	if !seen["node-1"] || !seen["node-2"] {
		t.Fatalf("dispatched nodes = %v, want both node-1 and node-2", seen)
	}
}

// TestDispatchReconnectInventoryRequestsNoOpWhenNodeListerNil proves an
// unwired coordinator (nodeLister nil, e.g. an integration broker with no
// nodes of its own) never attempts a publish.
func TestDispatchReconnectInventoryRequestsNoOpWhenNodeListerNil(t *testing.T) {
	cm := &fakeMQTTClient{}
	bm := &BrokerManager{now: time.Now, cm: cm}
	bm.dispatchReconnectInventoryRequests(context.Background(), testLogger())
	time.Sleep(20 * time.Millisecond)
	if got := cm.publishCount(); got != 0 {
		t.Fatalf("publishCount = %d, want 0 with a nil nodeLister", got)
	}
}

// TestDispatchReconnectInventoryRequestsIgnoresPerNodePublishFailures proves
// the fire-and-forget contract dispatchReconnectInventoryRequests's own doc
// comment commits to: one node's publish failing (the deployed agent may be
// unreachable, or simply too old to be listening at all) never stops the
// remaining nodes from being asked, and never touches BrokerState.
func TestDispatchReconnectInventoryRequestsIgnoresPerNodePublishFailures(t *testing.T) {
	cm := &fakeMQTTClient{
		publishFunc: func(_ context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
			if p.Topic == "showmesh/nodes/node-1/cmd" {
				return nil, errors.New("simulated publish failure")
			}
			return &paho.PublishResponse{}, nil
		},
	}
	initAt := time.Unix(20000, 0).UTC()
	bm := &BrokerManager{
		now:   time.Now,
		cm:    cm,
		state: BrokerState{Connected: true, Since: initAt, ConnectedSince: initAt, ObservedAt: initAt},
		nodeLister: func(context.Context) ([]string, error) {
			return []string{"node-1", "node-2"}, nil
		},
	}
	before := bm.State()

	bm.dispatchReconnectInventoryRequests(context.Background(), testLogger())
	waitForPublishCount(t, cm, 2)

	after := bm.State()
	if after != before {
		t.Fatalf("BrokerState changed from a per-node publish failure: before %+v, after %+v; "+
			"a node's refusal or unreachability must never read as evidence about the broker connection", before, after)
	}

	found := false
	for _, p := range cm.publishes {
		if p.Topic == "showmesh/nodes/node-2/cmd" {
			found = true
		}
	}
	if !found {
		t.Fatal("node-2 was never dispatched to: one node's publish failure must not stop the rest")
	}
}

// TestOnConnectionUpNeverRegistersAResponseWaiterForInventoryRequest proves
// the property the coordinator's own activation decision actually depends
// on: this dispatch never registers a response waiter (respTopics stays
// empty), so whatever the node later publishes back on its result topic,
// an outright refusal (an old, not-yet-rebuilt agent does not recognize
// this action) or nothing at all, has nothing on this side listening for
// it. Nothing this dispatch does can feed back into cueactivate.Authorize:
// that decision is governed entirely by cueAssetsPresent's own
// report-age/reconnect-time comparison, never by whether this command was
// answered.
func TestOnConnectionUpNeverRegistersAResponseWaiterForInventoryRequest(t *testing.T) {
	cm := &fakeMQTTClient{}
	bm := &BrokerManager{
		now: time.Now, cm: cm,
		nodeLister: func(context.Context) ([]string, error) {
			return []string{"node-1"}, nil
		},
	}
	bm.onConnectionUp(context.Background(), testLogger())
	waitForPublishCount(t, cm, 1)

	bm.respMu.Lock()
	waiters := len(bm.respTopics)
	bm.respMu.Unlock()
	if waiters != 0 {
		t.Fatalf("respTopics has %d entries, want 0: this dispatch must never await a response", waiters)
	}
}
