package broker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/packets"
	"github.com/eclipse/paho.golang/paho"
)

// fakeMQTTClient is an in-process double for [mqttClient], so Publish and
// the response-waiter machinery (registerResponseWaiter, dispatchToWaiters,
// AwaitResponse) can be exercised deterministically without a real broker
// connection. It records every call so tests can assert on subscribe/
// unsubscribe refcounting (see TestRegisterResponseWaiterSharesOneSubscribe
// etc.) and lets a test inject broker-side behavior (a failing SUBSCRIBE, a
// rejected PUBLISH, or — critically — a message "arriving" at a precise
// point in the call sequence) via the *Func hooks.
//
// This file's tests prove this package's OWN routing, refcounting and
// ordering logic — the part a fake can prove. The retained-message trap,
// the lost-wakeup race and the deadline outcome (this package's task
// specification names these as the three that matter) are proved again in
// response_integration_test.go against a real Mosquitto broker, because a
// fake that never exercises a real broker's actual RETAIN and SUBACK timing
// would only prove that this file's own model of that timing is
// self-consistent, not that it is correct.
type fakeMQTTClient struct {
	mu sync.Mutex

	publishFunc     func(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error)
	subscribeFunc   func(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error)
	unsubscribeFunc func(ctx context.Context, u *paho.Unsubscribe) (*paho.Unsuback, error)

	publishes    []*paho.Publish
	subscribes   []*paho.Subscribe
	unsubscribes []*paho.Unsubscribe
}

func (f *fakeMQTTClient) Publish(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
	f.mu.Lock()
	f.publishes = append(f.publishes, p)
	f.mu.Unlock()
	if f.publishFunc != nil {
		return f.publishFunc(ctx, p)
	}
	return &paho.PublishResponse{}, nil
}

func (f *fakeMQTTClient) Subscribe(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error) {
	f.mu.Lock()
	f.subscribes = append(f.subscribes, s)
	f.mu.Unlock()
	if f.subscribeFunc != nil {
		return f.subscribeFunc(ctx, s)
	}
	return &paho.Suback{}, nil
}

func (f *fakeMQTTClient) Unsubscribe(ctx context.Context, u *paho.Unsubscribe) (*paho.Unsuback, error) {
	f.mu.Lock()
	f.unsubscribes = append(f.unsubscribes, u)
	f.mu.Unlock()
	if f.unsubscribeFunc != nil {
		return f.unsubscribeFunc(ctx, u)
	}
	return &paho.Unsuback{}, nil
}

func (f *fakeMQTTClient) AwaitConnection(ctx context.Context) error { return nil }
func (f *fakeMQTTClient) Disconnect(ctx context.Context) error      { return nil }

func (f *fakeMQTTClient) subscribeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subscribes)
}

func (f *fakeMQTTClient) unsubscribeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.unsubscribes)
}

func (f *fakeMQTTClient) publishCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.publishes)
}

// newResponseTestBrokerManager builds a BrokerManager wired to cm, with a
// real clock (deliberately: AwaitResponse's deadline math uses time.Now/
// time.Until directly, so a fake clock disconnected from real elapsed time
// would make its timer loop nonsensical — see AwaitResponse's doc comment).
func newResponseTestBrokerManager(cm mqttClient) *BrokerManager {
	return &BrokerManager{now: time.Now, cm: cm}
}

func textMatcher(want string) Matcher {
	return func(m Message) bool { return string(m.Payload) == want }
}

// --- Publish ---

func TestPublishFailsClearlyWithNoBroker(t *testing.T) {
	bm := newResponseTestBrokerManager(nil)

	err := bm.Publish(context.Background(), "home/projectors/set", 1, false, []byte("ON"))
	if !errors.Is(err, ErrBrokerUnavailable) {
		t.Fatalf("Publish with no broker: err = %v, want it to wrap ErrBrokerUnavailable", err)
	}
}

func TestPublishReturnsWrappedErrorOnFailure(t *testing.T) {
	boom := errors.New("boom")
	cm := &fakeMQTTClient{
		publishFunc: func(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
			return nil, boom
		},
	}
	bm := newResponseTestBrokerManager(cm)

	err := bm.Publish(context.Background(), "home/projectors/set", 1, false, []byte("ON"))
	if !errors.Is(err, boom) {
		t.Fatalf("Publish: err = %v, want it to wrap the underlying error", err)
	}
}

func TestPublishDistinguishesNotAuthorized(t *testing.T) {
	underlying := errors.New("puback reason code 135")
	cm := &fakeMQTTClient{
		publishFunc: func(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
			return &paho.PublishResponse{ReasonCode: packets.PubackNotAuthorized}, underlying
		},
	}
	bm := newResponseTestBrokerManager(cm)

	err := bm.Publish(context.Background(), "home/projectors/set", 1, false, []byte("ON"))
	if !errors.Is(err, ErrPublishNotAuthorized) {
		t.Fatalf("Publish: err = %v, want it to wrap ErrPublishNotAuthorized", err)
	}
}

func TestPublishSendsExactTopicQoSRetainPayload(t *testing.T) {
	cm := &fakeMQTTClient{}
	bm := newResponseTestBrokerManager(cm)

	if err := bm.Publish(context.Background(), "home/projectors/set", 1, true, []byte("ON")); err != nil {
		t.Fatalf("Publish: unexpected error: %v", err)
	}

	if cm.publishCount() != 1 {
		t.Fatalf("publishCount = %d, want 1", cm.publishCount())
	}
	got := cm.publishes[0]
	if got.Topic != "home/projectors/set" || got.QoS != 1 || !got.Retain || string(got.Payload) != "ON" {
		t.Errorf("Publish sent %+v, did not preserve topic/qos/retain/payload", got)
	}
}

// --- validateResponseTopic ---

func TestValidateResponseTopicRejectsWildcardsAndEmpty(t *testing.T) {
	cases := []string{"", "home/+/state", "home/#"}
	for _, topic := range cases {
		if err := validateResponseTopic(topic); !errors.Is(err, ErrInvalidResponseTopic) {
			t.Errorf("validateResponseTopic(%q) = %v, want ErrInvalidResponseTopic", topic, err)
		}
	}
}

func TestValidateResponseTopicAcceptsConcreteTopic(t *testing.T) {
	if err := validateResponseTopic("home/projectors/state"); err != nil {
		t.Errorf("validateResponseTopic(concrete topic) = %v, want nil", err)
	}
}

// --- dispatchToWaiters: the RETAIN discard ---

// TestDispatchToWaitersDiscardsRetainedBeforeMatcher is this package's
// direct unit-level proof of the single most important line in this file:
// a retained delivery must never even reach a waiter's Matcher. It uses a
// Matcher that always returns true, so if the retained check were missing
// or came after the Matcher call, this message would be delivered — the
// test would then fail to observe "nothing arrived" and instead see a
// spurious match.
func TestDispatchToWaitersDiscardsRetainedBeforeMatcher(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})
	bm.respTopics = map[string]*responseTopicState{
		"home/projectors/state": {
			waiters: map[uint64]*pendingWaiter{
				1: {id: 1, topic: "home/projectors/state", match: func(Message) bool { return true }, ch: make(chan deliveredMessage, 1)},
			},
		},
	}

	bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: true})

	select {
	case dm := <-bm.respTopics["home/projectors/state"].waiters[1].ch:
		t.Fatalf("retained delivery reached the waiter channel: %+v, want it discarded before the Matcher", dm)
	default:
	}
}

func TestDispatchToWaitersDeliversLiveMatchingMessage(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})
	ch := make(chan deliveredMessage, 1)
	bm.respTopics = map[string]*responseTopicState{
		"home/projectors/state": {
			waiters: map[uint64]*pendingWaiter{
				1: {id: 1, topic: "home/projectors/state", match: textMatcher("on"), ch: ch},
			},
		},
	}

	bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})

	select {
	case dm := <-ch:
		if string(dm.msg.Payload) != "on" || dm.msg.Retained {
			t.Errorf("delivered message = %+v, want live payload \"on\"", dm.msg)
		}
	default:
		t.Fatalf("live matching message did not reach the waiter channel")
	}
}

func TestDispatchToWaitersSkipsNonMatchingMessage(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})
	ch := make(chan deliveredMessage, 1)
	bm.respTopics = map[string]*responseTopicState{
		"home/projectors/state": {
			waiters: map[uint64]*pendingWaiter{
				1: {id: 1, topic: "home/projectors/state", match: textMatcher("on"), ch: ch},
			},
		},
	}

	bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("off"), Retained: false})

	select {
	case dm := <-ch:
		t.Fatalf("non-matching message reached the waiter channel: %+v", dm)
	default:
	}
}

// TestDispatchToWaitersFansOutToEveryWaiterOnTopic is the direct proof of
// this file's concurrency requirement: two waiters live on the same topic
// must both see one matching delivery, not race each other for it.
func TestDispatchToWaitersFansOutToEveryWaiterOnTopic(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})
	chA := make(chan deliveredMessage, 1)
	chB := make(chan deliveredMessage, 1)
	bm.respTopics = map[string]*responseTopicState{
		"home/projectors/state": {
			waiters: map[uint64]*pendingWaiter{
				1: {id: 1, topic: "home/projectors/state", match: textMatcher("on"), ch: chA},
				2: {id: 2, topic: "home/projectors/state", match: textMatcher("on"), ch: chB},
			},
		},
	}

	bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})

	for name, ch := range map[string]chan deliveredMessage{"A": chA, "B": chB} {
		select {
		case <-ch:
		default:
			t.Errorf("waiter %s did not receive the fanned-out delivery", name)
		}
	}
}

func TestDispatchToWaitersIgnoresUnrelatedTopic(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})
	ch := make(chan deliveredMessage, 1)
	bm.respTopics = map[string]*responseTopicState{
		"home/projectors/state": {
			waiters: map[uint64]*pendingWaiter{
				1: {id: 1, topic: "home/projectors/state", match: textMatcher("on"), ch: ch},
			},
		},
	}

	bm.dispatchToWaiters(Message{Topic: "home/garage/state", Payload: []byte("on"), Retained: false})

	select {
	case dm := <-ch:
		t.Fatalf("delivery on an unrelated topic reached the waiter: %+v", dm)
	default:
	}
}

// --- registerResponseWaiter / releaseResponseWaiter: refcounting ---

func TestRegisterResponseWaiterFailsWithNoBroker(t *testing.T) {
	bm := newResponseTestBrokerManager(nil)

	_, err := bm.registerResponseWaiter(context.Background(), "home/projectors/state", 1, textMatcher("on"))
	if !errors.Is(err, ErrBrokerUnavailable) {
		t.Fatalf("registerResponseWaiter with no broker: err = %v, want ErrBrokerUnavailable", err)
	}
}

func TestRegisterResponseWaiterSharesOneSubscribeAcrossWaitersOnSameTopic(t *testing.T) {
	cm := &fakeMQTTClient{}
	bm := newResponseTestBrokerManager(cm)

	w1, err := bm.registerResponseWaiter(context.Background(), "home/projectors/state", 1, textMatcher("on"))
	if err != nil {
		t.Fatalf("first registerResponseWaiter: %v", err)
	}
	w2, err := bm.registerResponseWaiter(context.Background(), "home/projectors/state", 1, textMatcher("on"))
	if err != nil {
		t.Fatalf("second registerResponseWaiter: %v", err)
	}

	if cm.subscribeCount() != 2 {
		t.Errorf("subscribeCount = %d, want 2 (each waiter subscribes independently — see registerResponseWaiter's doc comment)", cm.subscribeCount())
	}

	// Releasing the first waiter must not unsubscribe while the second is
	// still live.
	bm.releaseResponseWaiter(w1)
	if cm.unsubscribeCount() != 0 {
		t.Errorf("unsubscribeCount = %d after releasing one of two waiters, want 0", cm.unsubscribeCount())
	}

	// A message must still reach the remaining waiter.
	bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
	select {
	case <-w2.ch:
	default:
		t.Fatalf("remaining waiter did not receive a delivery after the sibling waiter released")
	}

	// Releasing the last waiter must unsubscribe exactly once.
	bm.releaseResponseWaiter(w2)
	if cm.unsubscribeCount() != 1 {
		t.Errorf("unsubscribeCount = %d after releasing the last waiter, want 1", cm.unsubscribeCount())
	}
}

func TestRegisterResponseWaiterSubscribeFailureRollsBackOnlyItself(t *testing.T) {
	var calls int
	cm := &fakeMQTTClient{
		subscribeFunc: func(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error) {
			calls++
			if calls == 2 {
				return nil, errors.New("subscribe rejected")
			}
			return &paho.Suback{}, nil
		},
	}
	bm := newResponseTestBrokerManager(cm)

	w1, err := bm.registerResponseWaiter(context.Background(), "home/projectors/state", 1, textMatcher("on"))
	if err != nil {
		t.Fatalf("first registerResponseWaiter: unexpected error: %v", err)
	}

	_, err = bm.registerResponseWaiter(context.Background(), "home/projectors/state", 1, textMatcher("on"))
	if err == nil {
		t.Fatalf("second registerResponseWaiter: want an error from the failing SUBSCRIBE, got nil")
	}

	// The first, successfully-subscribed waiter must be unaffected: it is
	// still registered and still receives deliveries.
	bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
	select {
	case <-w1.ch:
	default:
		t.Fatalf("surviving waiter did not receive a delivery after a sibling's subscribe failed and rolled back")
	}
}

func TestReleaseResponseWaiterToleratesUnsubscribeFailure(t *testing.T) {
	cm := &fakeMQTTClient{
		unsubscribeFunc: func(ctx context.Context, u *paho.Unsubscribe) (*paho.Unsuback, error) {
			return nil, errors.New("boom")
		},
	}
	bm := newResponseTestBrokerManager(cm)

	w, err := bm.registerResponseWaiter(context.Background(), "home/projectors/state", 1, textMatcher("on"))
	if err != nil {
		t.Fatalf("registerResponseWaiter: %v", err)
	}

	// Must not panic even though the underlying Unsubscribe call fails.
	bm.releaseResponseWaiter(w)

	bm.respMu.Lock()
	_, stillTracked := bm.respTopics["home/projectors/state"]
	bm.respMu.Unlock()
	if stillTracked {
		t.Errorf("topic still tracked in respTopics after its only waiter released, want it removed regardless of the unsubscribe outcome")
	}
}

// --- AwaitResponse ---

func TestAwaitResponseRejectsNonPositiveDeadline(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})

	_, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:  "home/projectors/set",
		ResponseTopic: "home/projectors/state",
		Deadline:      0,
		Match:         textMatcher("on"),
	})
	if err == nil {
		t.Fatalf("AwaitResponse with a zero deadline: want an error, got nil")
	}
}

func TestAwaitResponseRejectsDeadlineExceedingMaximum(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})

	_, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:  "home/projectors/set",
		ResponseTopic: "home/projectors/state",
		Deadline:      MaxResponseDeadline + time.Second,
		Match:         textMatcher("on"),
	})
	if err == nil {
		t.Fatalf("AwaitResponse with a deadline past the maximum: want an error, got nil")
	}

	// Exactly at the maximum must still be accepted as a valid request — the
	// boundary is inclusive. A responder that answers the instant our own
	// publish goes out (dispatched from inside the fake's Publish call, the
	// same technique TestAwaitResponseSeesResponsePublishedBetweenSubscribeAndWait
	// uses) proves this resolves via the ordinary success path rather than a
	// validation rejection, without this test actually waiting out a
	// 120-second deadline. Dispatching from the subscribe step instead would
	// predate publishedAt and be discarded by AwaitResponse's own rule 5.
	fastCM := &fakeMQTTClient{
		publishFunc: func(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
			bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
			return &paho.PublishResponse{}, nil
		},
	}
	bm.cm = fastCM

	msg, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:  "home/projectors/set",
		ResponseTopic: "home/projectors/state",
		Deadline:      MaxResponseDeadline,
		Match:         textMatcher("on"),
	})
	if err != nil {
		t.Fatalf("AwaitResponse with a deadline exactly at the maximum: unexpected error: %v", err)
	}
	if string(msg.Payload) != "on" {
		t.Errorf("AwaitResponse returned %+v, want the live \"on\" delivery", msg)
	}
}

// TestAwaitResponseSeesResponsePublishedBetweenSubscribeAndWait is the
// no-lost-wakeup test named by this package's task specification: a live,
// matching message dispatched WHILE the publish call is still in flight
// (i.e. strictly between the subscribe completing and AwaitResponse's own
// wait loop starting to select) must still be seen, because it is queued
// in the waiter's buffered channel rather than requiring the wait loop to
// already be listening at the exact instant it arrives.
func TestAwaitResponseSeesResponsePublishedBetweenSubscribeAndWait(t *testing.T) {
	bm := &BrokerManager{now: time.Now}
	cm := &fakeMQTTClient{
		publishFunc: func(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
			// Simulate the external system reacting essentially
			// instantaneously: the response is dispatched to this
			// BrokerManager's own delivery path from inside the Publish
			// call itself, before AwaitResponse's wait loop has been
			// reached at all.
			bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
			return &paho.PublishResponse{}, nil
		},
	}
	bm.cm = cm

	msg, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       2 * time.Second,
		Match:          textMatcher("on"),
	})
	if err != nil {
		t.Fatalf("AwaitResponse: unexpected error: %v", err)
	}
	if string(msg.Payload) != "on" {
		t.Errorf("AwaitResponse returned %+v, want the live \"on\" delivery", msg)
	}
}

// TestAwaitResponseRetainedDeliveryDoesNotConfirm is this package's fake-
// based counterpart of the acceptance-level retained-message trap: a
// retained delivery arriving as soon as the SUBSCRIBE completes (this is
// exactly how a real broker behaves — see response_integration_test.go for
// the same scenario against a real Mosquitto) must not resolve
// AwaitResponse. Only the live delivery, dispatched afterward, may.
func TestAwaitResponseRetainedDeliveryDoesNotConfirm(t *testing.T) {
	bm := &BrokerManager{now: time.Now}
	cm := &fakeMQTTClient{
		subscribeFunc: func(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error) {
			// A retained replay delivered the instant the subscription is
			// established, exactly as Home Assistant/Node-RED would.
			bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: true})
			return &paho.Suback{}, nil
		},
	}
	bm.cm = cm

	const settle = 60 * time.Millisecond
	go func() {
		time.Sleep(settle)
		bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
	}()

	start := time.Now()
	msg, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       2 * time.Second,
		Match:          textMatcher("on"),
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AwaitResponse: unexpected error: %v", err)
	}
	if msg.Retained {
		t.Fatalf("AwaitResponse returned a retained delivery, want only the live one to confirm")
	}
	if elapsed < settle/2 {
		t.Errorf("AwaitResponse resolved in %v, want it to wait for the live delivery (~%v) rather than confirming instantly off the retained one — this is the defect this test exists to catch", elapsed, settle)
	}
}

func TestAwaitResponseDeadlineExceededIsDistinctOutcome(t *testing.T) {
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})

	start := time.Now()
	msg, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       80 * time.Millisecond,
		Match:          textMatcher("on"),
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrResponseDeadlineExceeded) {
		t.Fatalf("AwaitResponse with nothing published: err = %v, want it to wrap ErrResponseDeadlineExceeded", err)
	}
	if err.Error() == "" {
		t.Errorf("ErrResponseDeadlineExceeded error string is empty, want it to name the topic and deadline")
	}
	if msg.Topic != "" || msg.Payload != nil || msg.Retained {
		t.Errorf("AwaitResponse returned non-zero Message %+v on deadline expiry, want the zero value", msg)
	}
	if elapsed < 80*time.Millisecond {
		t.Errorf("AwaitResponse returned after %v, want it to have actually waited out the 80ms deadline", elapsed)
	}
}

func TestAwaitResponseDiscardsDeliveryPredatingThePublish(t *testing.T) {
	bm := &BrokerManager{now: time.Now}
	// A message dispatched to a topic BEFORE anyone has subscribed simply
	// finds no waiter registered (see dispatchToWaiters) and is a no-op;
	// to simulate a delivery in the subscribe-to-publish window that DOES
	// reach a registered waiter, dispatch it from within subscribeFunc,
	// then require Publish to be an observably later event by sleeping
	// before it "sends".
	cm := &fakeMQTTClient{
		subscribeFunc: func(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error) {
			bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
			return &paho.Suback{}, nil
		},
		publishFunc: func(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
			time.Sleep(20 * time.Millisecond)
			return &paho.PublishResponse{}, nil
		},
	}
	bm.cm = cm

	const postPublishDelay = 60 * time.Millisecond
	go func() {
		time.Sleep(postPublishDelay)
		bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
	}()

	start := time.Now()
	_, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       2 * time.Second,
		Match:          textMatcher("on"),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("AwaitResponse: unexpected error: %v", err)
	}
	// Both deliveries have identical topic/payload, so the only way to
	// tell them apart is timing: the subscribe-time delivery predates
	// publishedAt by program order (registerResponseWaiter's Subscribe call
	// completes before AwaitResponse captures publishedAt) and must be
	// discarded, so this call may only resolve once the second,
	// genuinely-post-publish delivery arrives.
	if elapsed < postPublishDelay/2 {
		t.Errorf("AwaitResponse resolved in %v, want it to have waited for the post-publish delivery (~%v) rather than the discarded pre-publish one", elapsed, postPublishDelay)
	}
}

func TestAwaitResponseReleasesWaiterOnCtxCancel(t *testing.T) {
	cm := &fakeMQTTClient{}
	bm := newResponseTestBrokerManager(cm)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := bm.AwaitResponse(ctx, ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       5 * time.Second,
		Match:          textMatcher("on"),
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("AwaitResponse after ctx cancellation: err = %v, want it to wrap context.Canceled", err)
	}

	bm.respMu.Lock()
	_, stillTracked := bm.respTopics["home/projectors/state"]
	bm.respMu.Unlock()
	if stillTracked {
		t.Errorf("response topic still tracked after ctx cancellation, want the waiter released")
	}
	if cm.unsubscribeCount() != 1 {
		t.Errorf("unsubscribeCount = %d after ctx cancellation released the only waiter, want 1", cm.unsubscribeCount())
	}
}

// TestAwaitResponseConcurrentWaitersOnSameTopicBothResolve is the
// AwaitResponse-level proof of this file's correlation rule (item 5): two
// runs waiting on the same response topic have no way to distinguish each
// other, because expect names a topic and a value, not an instance. Both
// legitimately observing the SAME external delivery is the correct
// behavior, not a bug — what must never happen is one consuming the
// delivery and starving the other. dispatchToWaiters' own tests already
// prove the routing table fans out; this proves two real, independent
// AwaitResponse calls both actually complete off one delivery.
func TestAwaitResponseConcurrentWaitersOnSameTopicBothResolve(t *testing.T) {
	bm := &BrokerManager{now: time.Now}
	bm.cm = &fakeMQTTClient{}

	req := ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       2 * time.Second,
		Match:          textMatcher("on"),
	}

	type result struct {
		msg Message
		err error
	}
	resultsA := make(chan result, 1)
	resultsB := make(chan result, 1)

	go func() {
		msg, err := bm.AwaitResponse(context.Background(), req)
		resultsA <- result{msg, err}
	}()
	go func() {
		msg, err := bm.AwaitResponse(context.Background(), req)
		resultsB <- result{msg, err}
	}()

	// Wait for both waiters to actually register before delivering, or one
	// might dispatch before the other has even subscribed and this test
	// would only be proving the single-waiter case twice.
	deadline := time.Now().Add(2 * time.Second)
	for {
		bm.respMu.Lock()
		n := 0
		if state, ok := bm.respTopics["home/projectors/state"]; ok {
			n = len(state.waiters)
		}
		bm.respMu.Unlock()
		if n == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("both waiters never registered (saw %d of 2)", n)
		}
		time.Sleep(time.Millisecond)
	}

	bm.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})

	rA := <-resultsA
	rB := <-resultsB
	if rA.err != nil {
		t.Errorf("waiter A: unexpected error: %v", rA.err)
	}
	if rB.err != nil {
		t.Errorf("waiter B: unexpected error: %v", rB.err)
	}
	if string(rA.msg.Payload) != "on" || string(rB.msg.Payload) != "on" {
		t.Errorf("resolved payloads = %q / %q, want both to have observed \"on\" off the single delivery", rA.msg.Payload, rB.msg.Payload)
	}
}

func TestAwaitResponseReleasesWaiterOnPublishFailure(t *testing.T) {
	boom := errors.New("boom")
	cm := &fakeMQTTClient{
		publishFunc: func(ctx context.Context, p *paho.Publish) (*paho.PublishResponse, error) {
			return nil, boom
		},
	}
	bm := newResponseTestBrokerManager(cm)

	_, err := bm.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       5 * time.Second,
		Match:          textMatcher("on"),
	})
	if !errors.Is(err, boom) {
		t.Fatalf("AwaitResponse: err = %v, want it to wrap the publish failure", err)
	}

	if cm.unsubscribeCount() != 1 {
		t.Errorf("unsubscribeCount = %d after a publish failure, want 1 (the waiter must still be released)", cm.unsubscribeCount())
	}
}
