// This file adds two things on top of broker.go's connection management:
// a publish capability (BrokerManager.Publish), and a response waiter
// (BrokerManager.AwaitResponse) that publishes a command and waits for a
// live, matching reply on a separate topic within a deadline. Both exist
// for Step 9's external MQTT macro step: a macro can invoke a home-
// automation action over MQTT and, optionally, confirm it happened by
// watching a response topic, per ADR-029's action/binding split. This
// package provides only the transport-level primitive — publish, and wait
// for a live matching message on a topic within a deadline. The response
// CONTRACT semantics (none/boolean/number/text/match) belong to the macro
// executor, which supplies Matcher as an injected predicate rather than
// this package reimplementing them.
//
// The trap this file exists to defend against: home-automation brokers
// retain state topics as a matter of course, so a step that publishes ON
// and waits on a state topic will, on subscribing, immediately receive
// last night's retained value unless retained deliveries are discarded
// before they ever reach a waiter's Matcher. See dispatchToWaiters.
package broker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

// unsubscribeTimeout bounds the best-effort MQTT UNSUBSCRIBE
// releaseResponseWaiter issues once the last waiter on a topic releases.
// It is deliberately independent of any caller-supplied context, because by
// the time it runs the caller that registered the waiter may already have
// returned (see releaseResponseWaiter's doc comment) — there is no live
// caller context left to derive it from.
const unsubscribeTimeout = 5 * time.Second

// MaxResponseDeadline bounds ResponseRequest.Deadline. STEP-9's specification
// requires deadlineSeconds on an mqtt show.action to be operator-authored
// and bounded, at a stated maximum of 120 seconds, because a longer wait is
// a run held open past any useful operator response and a projector that
// has not answered in two minutes is not going to. The action's write-time
// validation (wave 2, outside this package) is the primary enforcement
// point; AwaitResponse enforces the same bound independently so a response
// waiter — and the broker subscription and goroutine backing it — cannot be
// held open indefinitely by any caller of this package alone, regardless of
// what validation ran further up the stack.
const MaxResponseDeadline = 120 * time.Second

// pendingWaiterBuffer sizes each waiter's delivery channel. dispatchToWaiters
// only ever enqueues a delivery that already passed both the RETAIN check
// and the waiter's own Matcher (see that function), so this only needs to
// absorb a short burst of qualifying deliveries arriving before
// AwaitResponse's loop has drained any of them — not general traffic on the
// topic. A handful is generous headroom without inviting unbounded growth;
// see dispatchToWaiters for what happens if it's ever exceeded anyway.
const pendingWaiterBuffer = 4

// ErrBrokerUnavailable is returned by Publish and by AwaitResponse's
// subscribe step when there is no live connection to the broker — either
// this BrokerManager was never given one (e.g. a bare struct built directly
// in a test) or autopaho itself reports the connection is down (see
// mqttClient.Publish/Subscribe/Unsubscribe on *autopaho.ConnectionManager,
// which return autopaho.ConnectionDownError immediately rather than
// blocking when disconnected). Per ADR-008, broker loss is a
// management-plane failure that must never block or panic the caller: this
// error fails the one operation attempted and nothing else.
var ErrBrokerUnavailable = errors.New("broker: not connected to mqtt broker")

// ErrPublishNotAuthorized mirrors internal/agent/mqtt.go's error of the
// same name and the same reasoning: the broker accepted the publish
// transaction at the transport level (a PUBACK/PUBREC round-trip
// completed) but rejected it with an authorization-family reason code. Per
// ADR-024 decision 10 this is a permanent, credential-related condition, not
// a transient failure, and callers should be able to tell it apart with
// errors.Is rather than parsing a generic error string.
var ErrPublishNotAuthorized = errors.New("broker: publish rejected by broker (not authorized)")

// ErrResponseDeadlineExceeded is returned by AwaitResponse when Deadline
// elapses (measured from the publish, not from the subscribe — see that
// method's doc comment) with no live delivery on the response topic
// matching Match. This is a distinct, stated outcome, never collapsed into
// a bare error and never confused with success: the macro executor (Wave 2)
// is expected to map it to its own "unconfirmed" step outcome.
var ErrResponseDeadlineExceeded = errors.New("broker: deadline exceeded waiting for a live matching response")

// ErrResponseFailedBeforePublish wraps any error AwaitResponse returns for
// which nothing reached the wire: deadline validation, response-topic
// validation, a Match nil check, a broker-unavailable subscribe, a
// rejected SUBSCRIBE, ctx canceled during subscribe, or the Publish call
// itself failing. A caller uses errors.Is against this to tell "nothing
// reached the wire" from a failure at or after a successful publish, which
// is what determines whether dispatch evidence such as PublishAttempted
// and dispatchedAt may be recorded as non-null.
var ErrResponseFailedBeforePublish = errors.New("broker: response wait failed before any publish was attempted")

// ErrInvalidResponseTopic is wrapped by the error AwaitResponse returns
// when ResponseTopic is empty or contains an MQTT wildcard character ('+'
// or '#').
//
// Wildcards in a response topic are deliberately not supported: this
// package's dispatch (see dispatchToWaiters) matches an inbound message to
// a waiter by exact topic string, which is correct and cheap for the exact,
// single-topic responses ADR-029's mqtt action target describes
// (target.expect.topic) and never a filter. Supporting a wildcard filter
// correctly would mean implementing MQTT's '+'/'#' matching rules against
// every inbound topic on the dispatch path for a case this step's action
// schema does not produce; rejecting it here keeps that undefined territory
// from being reachable at all rather than leaving it silently wrong.
var ErrInvalidResponseTopic = errors.New("broker: invalid response topic")

// validateResponseTopic rejects an empty topic or one containing an MQTT
// wildcard character. See [ErrInvalidResponseTopic]'s doc comment for why
// wildcards are refused rather than supported.
func validateResponseTopic(topic string) error {
	if topic == "" {
		return fmt.Errorf("%w: topic must not be empty", ErrInvalidResponseTopic)
	}
	if strings.ContainsAny(topic, "+#") {
		return fmt.Errorf("%w: %q: response topics may not contain an MQTT wildcard ('+' or '#')", ErrInvalidResponseTopic, topic)
	}
	return nil
}

// Publish sends one MQTT PUBLISH with the given topic, QoS, retain flag and
// payload.
//
// Per ADR-008, broker loss is a management-plane failure: with no broker
// (b.cm == nil, or autopaho reports the connection down) Publish returns
// [ErrBrokerUnavailable] immediately rather than blocking or panicking —
// see mqttClient's doc comment for why the underlying call itself already
// cannot block on a down connection. When connected, this call blocks only
// as long as ctx allows; callers that must never block indefinitely are
// responsible for giving ctx a deadline, the same contract
// *autopaho.ConnectionManager.Publish already has and
// internal/agent/mqtt.go's Publish already follows.
func (b *BrokerManager) Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error {
	if b.cm == nil {
		return fmt.Errorf("%w: cannot publish to %q", ErrBrokerUnavailable, topic)
	}

	resp, err := b.cm.Publish(ctx, &paho.Publish{
		QoS:     qos,
		Retain:  retain,
		Topic:   topic,
		Payload: payload,
	})
	if err != nil {
		if resp != nil && isAuthReasonCode(resp.ReasonCode) {
			return fmt.Errorf("%w: topic %q, reason code %d: %w", ErrPublishNotAuthorized, topic, resp.ReasonCode, err)
		}
		return fmt.Errorf("publishing to %q: %w", topic, err)
	}
	return nil
}

// Matcher decides whether a live (RETAIN=0) message on a response topic is
// the awaited response. AwaitResponse never calls Matcher for a retained
// delivery (see dispatchToWaiters) — Matcher exists purely so the macro
// executor's response-CONTRACT semantics (none/boolean/number/text/match,
// per the mqtt action's `expect.kind`) are injected here rather than
// duplicated in this package, which owns only the transport-level "wait for
// a live matching message" primitive.
//
// Matcher runs on the same delivery path as every other inbound-message
// handling in this package (see MessageHandler's doc comment on paho's
// OnPublishReceived callback goroutine): it must return quickly and must
// not block, or it delays this connection's own PUBACKs.
type Matcher func(Message) bool

// ResponseRequest bundles everything AwaitResponse needs to publish a
// command and wait for its live response in one call. The
// subscribe-then-publish ordering that makes the wait correct (see
// AwaitResponse's doc comment) is enforced inside AwaitResponse itself
// rather than left for a caller to sequence two separate calls correctly.
type ResponseRequest struct {
	// PublishTopic, PublishPayload, PublishQoS and PublishRetain describe
	// the outbound publish AwaitResponse issues after subscribing.
	PublishTopic   string
	PublishPayload []byte
	PublishQoS     byte
	PublishRetain  bool

	// ResponseTopic is the topic AwaitResponse subscribes to before
	// publishing. It must be a concrete topic, not a wildcard filter — see
	// [ErrInvalidResponseTopic].
	ResponseTopic string
	ResponseQoS   byte

	// Deadline is measured from the publish, not from the subscribe (see
	// AwaitResponse's doc comment on why those differ). It must be
	// positive and no greater than [MaxResponseDeadline]; AwaitResponse
	// validates both before subscribing or publishing anything.
	Deadline time.Duration

	// Match is consulted only for live (non-retained) deliveries on
	// ResponseTopic; see [Matcher]. Required.
	Match Matcher
}

// deliveredMessage pairs an inbound [Message] with the time this
// BrokerManager received it, so AwaitResponse's wait loop can distinguish a
// delivery that predates its own publish (see step (e) below) from a
// genuine live response.
type deliveredMessage struct {
	msg        Message
	receivedAt time.Time
}

// pendingWaiter is one live AwaitResponse call's registration in the
// response-topic routing table (see responseTopicState and
// dispatchToWaiters).
type pendingWaiter struct {
	id    uint64
	topic string
	match Matcher
	ch    chan deliveredMessage

	// drops counts deliveries dispatchToWaiters dropped for this waiter
	// because ch was already full of qualifying entries it had not
	// finished draining (see pendingWaiterBuffer and dispatchToWaiters).
	// Not surfaced through any exported API today; it exists so the count
	// is at least visible in the warning log dispatchToWaiters emits on
	// every drop, rather than a silent, unrecorded loss.
	drops atomic.Uint64
}

// responseTopicState is the shared, refcounted MQTT subscription state for
// one response topic: every live [pendingWaiter] registered against it, so
// a burst of concurrent AwaitResponse calls on the same topic (see item 4
// of this file's task spec — several waiters may be live at once on the
// same topic) share one routing entry that dispatchToWaiters fans out to
// all of them.
type responseTopicState struct {
	qos     byte
	waiters map[uint64]*pendingWaiter
}

// dispatchToWaiters is called for every inbound publish this BrokerManager
// receives (see combinedHandler in NewBrokerManager), after the original
// caller-supplied handler has already seen it — this function must never
// alter or intercept what that handler receives, only additionally route
// the same message to any live response waiters.
//
// The RETAIN check below is, per this package's task specification, the
// single most important line of code in this file: a retained delivery is
// the broker replaying a value it once held — possibly hours or days
// earlier, per Message.Retained's own doc comment — never proof that
// anything happened after this process's own publish. It is discarded here,
// unconditionally, before a Matcher is ever consulted, so no Matcher
// implementation can get this wrong by omission.
func (b *BrokerManager) dispatchToWaiters(m Message) {
	if m.Retained {
		return
	}

	b.respMu.Lock()
	state, ok := b.respTopics[m.Topic]
	var snapshot []*pendingWaiter
	if ok {
		snapshot = make([]*pendingWaiter, 0, len(state.waiters))
		for _, w := range state.waiters {
			snapshot = append(snapshot, w)
		}
	}
	b.respMu.Unlock()

	if len(snapshot) == 0 {
		return
	}

	receivedAt := time.Now()
	for _, w := range snapshot {
		if !w.match(m) {
			continue
		}
		dm := deliveredMessage{msg: m, receivedAt: receivedAt}
		select {
		case w.ch <- dm:
		default:
			// Every queued entry already passed this waiter's own Matcher,
			// so a full buffer can only mean multiple qualifying
			// deliveries arrived before AwaitResponse's loop drained any
			// of them (see pendingWaiterBuffer). The prior version of this
			// code silently dropped THIS arrival — the newest one — which
			// is backwards: AwaitResponse's own loop (step 5) discards
			// anything predating the publish and stops at the first
			// delivery that does not, so the entries most likely to be
			// stale (and therefore safe to lose) are the OLDEST ones
			// sitting in the buffer, not whatever just arrived. Drop the
			// oldest queued entry to make room instead, so a full buffer
			// biases toward keeping the most recent evidence rather than
			// discarding it. Still non-blocking throughout, per Matcher's
			// doc comment on this callback path never blocking, and the
			// drop is counted and logged rather than silent.
			select {
			case <-w.ch:
				dropped := w.drops.Add(1)
				b.log().Warn("mqtt response waiter buffer full; dropped the oldest queued delivery to make room for a newer one",
					"topic", w.topic, "dropped_total", dropped)
			default:
				// Drained by AwaitResponse's own loop between our failed
				// send above and this receive; nothing to drop.
			}
			select {
			case w.ch <- dm:
			default:
				// Refilled again in this tiny window by a concurrent
				// dispatch; give up on this one delivery rather than
				// spinning or blocking. AwaitResponse's own loop still
				// resolves off whichever deliveries did make it through.
				dropped := w.drops.Add(1)
				b.log().Warn("mqtt response waiter buffer full; dropped a delivery",
					"topic", w.topic, "dropped_total", dropped)
			}
		}
	}
}

// registerResponseWaiter subscribes to topic and adds a new [pendingWaiter]
// to this BrokerManager's routing table, in that logical order but with the
// routing-table insertion happening BEFORE the network SUBSCRIBE call is
// issued.
//
// That ordering — not the reverse — is what makes registration race-free
// against delivery. No live publish for topic can reach dispatchToWaiters
// through this connection until the broker has processed a SUBSCRIBE from
// it, and that SUBSCRIBE is not sent until after this function's insert
// into respTopics has already completed. Registering only after Subscribe
// returns would leave exactly the window this must close: between the
// broker beginning to route matching publishes to this connection and this
// goroutine getting back around to updating the map, a fast responder's
// answer would arrive, find nothing registered, and be silently dropped —
// the MQTT-side lost-wakeup this package's task specification calls the
// sharpest trap in this step.
//
// Every waiter subscribes independently, even when another live waiter
// already holds the same topic: MQTT re-subscribing to a topic you already
// hold is a harmless, idempotent refresh, and sharing one waiter's
// SUBSCRIBE outcome across others would mean a subscribe failure for THIS
// waiter could roll back a topic another, already-successful waiter still
// depends on. See releaseResponseWaiter for the matching reasoning on the
// unsubscribe side.
//
// The whole call runs under topic's [BrokerManager.topicLock] — see that
// method's doc comment for why: a released waiter's UNSUBSCRIBE for this
// exact topic may be in flight right now, having already removed its
// routing-table entry, and this call must not be allowed to insert a new
// entry and SUBSCRIBE while that UNSUBSCRIBE is still outstanding, or the
// UNSUBSCRIBE can land on the wire AFTER this call's SUBSCRIBE and silently
// tear the new registration down. That is review finding 3 on commit
// 9dcab74: reproduced with a gated fake client, the broker's own call order
// came out subscribe/subscribe/unsubscribe, with the second (live) waiter
// torn down by the first waiter's stale, in-flight unsubscribe.
func (b *BrokerManager) registerResponseWaiter(ctx context.Context, topic string, qos byte, match Matcher) (*pendingWaiter, error) {
	if err := validateResponseTopic(topic); err != nil {
		return nil, err
	}
	if match == nil {
		return nil, errors.New("broker: AwaitResponse requires a non-nil Match")
	}
	if b.cm == nil {
		return nil, fmt.Errorf("%w: cannot subscribe to %q", ErrBrokerUnavailable, topic)
	}

	w := &pendingWaiter{
		id:    b.nextWaiterID.Add(1),
		topic: topic,
		match: match,
		ch:    make(chan deliveredMessage, pendingWaiterBuffer),
	}

	topicMu := b.topicLock(topic)
	topicMu.Lock()
	defer topicMu.Unlock()

	b.respMu.Lock()
	if b.respTopics == nil {
		b.respTopics = make(map[string]*responseTopicState)
	}
	state, ok := b.respTopics[topic]
	if !ok {
		state = &responseTopicState{qos: qos, waiters: make(map[uint64]*pendingWaiter)}
		b.respTopics[topic] = state
	}
	state.waiters[w.id] = w
	b.respMu.Unlock()

	if _, err := b.cm.Subscribe(ctx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{
		{
			Topic: topic,
			QoS:   qos,
			// Left false for exactly the reason subscriptionsToOptions
			// already documents for the fixed subscription set: this
			// package must be able to tell a retained replay from a live
			// publish for as long as this subscription lives, and
			// RetainAsPublished=true would make the broker echo RETAIN=1
			// on every subsequent live publish on a topic that was ever
			// retained, destroying that distinction for the response-topic
			// case exactly as it would have for the fixed one.
			RetainAsPublished: false,
			// RetainHandling is deliberately left at its zero value,
			// "send retained messages at every subscribe", and that is a
			// decision rather than an omission.
			//
			// RetainHandling=2 was applied here as defence in depth and
			// removed on 2026-08-14, because it silently disarmed the
			// acceptance criterion that proves the rule it was defending.
			// A broker told never to replay its retained store cannot
			// produce the delivery dispatchToWaiters' RETAIN check exists
			// to discard, so the real-broker test for that check passed
			// with the check deleted. That was measured, not reasoned
			// about. The hardening made the trap unreachable and the guard
			// against the trap unverifiable in the same move, and an
			// unverifiable guard is how this stops working without anyone
			// noticing.
			//
			// So the trap stays reachable on purpose, and the RETAIN check
			// in dispatchToWaiters is the single thing that stops it,
			// which is what this step's specification asks for and what
			// TestIntegrationRetainedResponseDoesNotConfirm now pins
			// against a real broker. Wanting RetainHandling=2 back means
			// first finding another way to prove the RETAIN check works.
		},
	}}); err != nil {
		// Roll back only this waiter's own registration. state may still
		// hold other, already-successfully-subscribed waiters on the same
		// topic; this failure must not affect them (see this function's
		// doc comment). Handled inline rather than by calling
		// releaseResponseWaiter: that method also acquires topicMu, which
		// this goroutine already holds, and sync.Mutex is not reentrant.
		if isLast := b.removeWaiter(w); isLast {
			b.unsubscribeNow(topic)
		}
		return nil, fmt.Errorf("subscribing to response topic %q: %w", topic, err)
	}

	return w, nil
}

// removeWaiter deletes w from the routing table and reports whether it was
// the last live waiter on its topic — in which case a caller holding
// topic's topicLock may need to issue the network UNSUBSCRIBE (see
// releaseResponseWaiter and registerResponseWaiter's subscribe-failure
// rollback). It only ever touches respTopics under respMu; it never makes a
// network call and never touches topicLock itself, so it is safe to call
// from a context that already holds a topicLock (registerResponseWaiter's
// rollback path does exactly that).
func (b *BrokerManager) removeWaiter(w *pendingWaiter) (lastWaiter bool) {
	b.respMu.Lock()
	defer b.respMu.Unlock()
	state, ok := b.respTopics[w.topic]
	if !ok {
		return false
	}
	delete(state.waiters, w.id)
	if len(state.waiters) == 0 {
		delete(b.respTopics, w.topic)
		return true
	}
	return false
}

// unsubscribeNow issues the best-effort MQTT UNSUBSCRIBE for topic, logging
// rather than surfacing a failure to any caller — see releaseResponseWaiter's
// doc comment for the reasoning. Every caller must hold topic's topicLock
// for the duration of this call: that is what review finding 3 on commit
// 9dcab74 needed, so a registerResponseWaiter for the same topic starting
// concurrently cannot issue its own SUBSCRIBE while this UNSUBSCRIBE is
// still outstanding on the wire.
func (b *BrokerManager) unsubscribeNow(topic string) {
	ctx, cancel := context.WithTimeout(context.Background(), unsubscribeTimeout)
	defer cancel()
	if _, err := b.cm.Unsubscribe(ctx, &paho.Unsubscribe{Topics: []string{topic}}); err != nil {
		b.log().Warn("mqtt unsubscribe failed after last response waiter released; broker will keep delivering this topic until the next reconnect, which is wasted traffic, not incorrect behavior",
			"topic", topic, "error", err)
	}
}

// releaseResponseWaiter removes w from the routing table and, if it was the
// last waiter on its topic, issues a best-effort MQTT UNSUBSCRIBE.
//
// This is always called from a defer in AwaitResponse (see that method),
// so it runs on every exit path — completion, deadline, ctx cancellation,
// and a failed registerResponseWaiter rolling itself back — per this
// package's task specification: a leaked subscription or waiter is a slow
// failure that only shows up during a show.
//
// The unsubscribe, when it happens, is logged rather than surfaced as an
// error to any caller: by the time the last waiter releases, the caller
// that registered it is already returning from AwaitResponse with its own
// result, and an unsubscribe failure here changes nothing about that
// result's correctness — it only means the broker keeps sending this
// process traffic for a topic nothing is routing anymore, which
// dispatchToWaiters already handles for free (it finds no registered
// waiter and does nothing). This mirrors subscribeAll's own precedent: log
// loudly, never fail the caller, for this class of housekeeping failure.
//
// Review finding 3 on commit 9dcab74: removeWaiter (map bookkeeping) and the
// decision to actually unsubscribe used to happen under one respMu
// critical section that ended BEFORE the network UNSUBSCRIBE call — so a
// registerResponseWaiter for the same topic, arriving in the window between
// this function's own unlock and its Unsubscribe call actually reaching the
// broker, could insert a brand new entry and SUBSCRIBE, and then have this
// call's stale UNSUBSCRIBE land afterward and tear it down. The fix is two
// parts: removeWaiter no longer decides anything about the network call by
// itself, and the network call (in unsubscribeNow) now runs under topic's
// topicLock with a re-check of the routing table taken immediately before
// it — so a registerResponseWaiter racing this release either completes
// entirely before this function acquires topicLock (and the re-check below
// sees it and skips the stale UNSUBSCRIBE), or blocks on topicLock until
// this function's UNSUBSCRIBE has fully completed (and only then inserts
// and SUBSCRIBEs, strictly after, never before or during).
func (b *BrokerManager) releaseResponseWaiter(w *pendingWaiter) {
	isLast := b.removeWaiter(w)
	if !isLast || b.cm == nil {
		return
	}

	topicMu := b.topicLock(w.topic)
	topicMu.Lock()
	defer topicMu.Unlock()

	// Re-check immediately before issuing the network UNSUBSCRIBE, now that
	// this goroutine holds topicMu: a registerResponseWaiter for this same
	// topic that started after removeWaiter above but managed to acquire
	// topicMu before this function did would already have completed its
	// entire insert-and-SUBSCRIBE by the time this Lock() call returns. If
	// the topic is live again, this UNSUBSCRIBE must not run — that stale
	// UNSUBSCRIBE tearing down the new registration is exactly what finding
	// 3 reproduced.
	b.respMu.Lock()
	_, stillWanted := b.respTopics[w.topic]
	b.respMu.Unlock()
	if stillWanted {
		return
	}

	b.unsubscribeNow(w.topic)
}

// AwaitResponse subscribes to req.ResponseTopic, publishes req's publish
// fields, and waits up to req.Deadline for a live (non-retained) message on
// ResponseTopic for which req.Match returns true, reporting what arrived.
//
// The order below is the correctness argument for this method, not
// incidental implementation detail:
//
//  1. Subscribe to the response topic FIRST, before publishing (see
//     registerResponseWaiter) — publishing first would race a fast
//     responder's answer against our own subscribe, and the answer would
//     arrive, find nothing registered, and be lost.
//  2. Publish.
//  3. The deadline starts at the publish, not at the subscribe: an operator
//     authors Deadline to bound how long the external system may reasonably
//     take to react to what we just sent it, not to bound how long our own
//     subscribe took.
//  4. Every retained delivery is discarded before req.Match is ever
//     consulted (see dispatchToWaiters) — see [ErrResponseDeadlineExceeded]
//     and Message.Retained's doc comment for why this matters: a
//     home-automation broker retaining last night's state on this exact
//     topic is the common case, not an edge case.
//  5. Only a live delivery whose receive time is at or after the publish
//     counts; anything that arrived in the subscribe-to-publish window (a
//     real message, just not evidence about THIS publish) is discarded and
//     the wait continues.
//  6. The waiter is always released — unsubscribing if it was the last one
//     on its topic — on every exit path: completion, deadline, ctx
//     cancellation, and a failed registration rolling itself back. See
//     releaseResponseWaiter.
//
// On success the returned Message is the live delivery that matched, with
// Retained always false. On failure the zero Message is returned alongside
// an error: [ErrResponseDeadlineExceeded] (wrapped, naming the topic and
// deadline) if Deadline elapsed with nothing matching, or
// [ErrResponseFailedBeforePublish] (wrapped, alongside [ErrBrokerUnavailable]
// or the underlying subscribe/publish error) if either the subscribe or
// the publish itself failed, or ctx's own error if ctx was canceled first.
// These are always distinguishable with errors.Is — AwaitResponse never
// reports success for a deadline expiry, and never returns a bare error
// with no stated reason.
func (b *BrokerManager) AwaitResponse(ctx context.Context, req ResponseRequest) (Message, error) {
	if req.Deadline <= 0 {
		return Message{}, fmt.Errorf("%w: broker: AwaitResponse deadline must be positive, got %v", ErrResponseFailedBeforePublish, req.Deadline)
	}
	if req.Deadline > MaxResponseDeadline {
		return Message{}, fmt.Errorf("%w: broker: AwaitResponse deadline %v exceeds the maximum of %v", ErrResponseFailedBeforePublish, req.Deadline, MaxResponseDeadline)
	}

	w, err := b.registerResponseWaiter(ctx, req.ResponseTopic, req.ResponseQoS, req.Match)
	if err != nil {
		return Message{}, fmt.Errorf("%w: awaiting response on %q: %w", ErrResponseFailedBeforePublish, req.ResponseTopic, err)
	}
	defer b.releaseResponseWaiter(w)

	// publishedAt is stamped BEFORE the publish call, not after it
	// returns: "the publish", as an event an external system might react
	// to, begins the instant we send it, not once our own QoS1 PUBACK
	// round-trip completes. Stamping after would let a genuinely-live
	// reply that races our own PUBACK wait be misclassified as predating
	// the publish and wrongly discarded by step 5 above.
	publishedAt := time.Now()
	if err := b.Publish(ctx, req.PublishTopic, req.PublishQoS, req.PublishRetain, req.PublishPayload); err != nil {
		// The publish call itself failing means nothing reached the wire,
		// the identical outcome a failed subscribe already reports —
		// wrapped in the same sentinel so every caller's existing
		// errors.Is check classifies it correctly.
		return Message{}, fmt.Errorf("%w: publishing to %q while awaiting response on %q: %w", ErrResponseFailedBeforePublish, req.PublishTopic, req.ResponseTopic, err)
	}

	deadlineAt := publishedAt.Add(req.Deadline)
	for {
		remaining := time.Until(deadlineAt)
		if remaining <= 0 {
			return Message{}, fmt.Errorf("%w: response topic %q, deadline %v", ErrResponseDeadlineExceeded, req.ResponseTopic, req.Deadline)
		}

		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			timer.Stop()
			return Message{}, fmt.Errorf("awaiting response on %q: %w", req.ResponseTopic, ctx.Err())
		case <-timer.C:
			return Message{}, fmt.Errorf("%w: response topic %q, deadline %v", ErrResponseDeadlineExceeded, req.ResponseTopic, req.Deadline)
		case dm := <-w.ch:
			timer.Stop()
			if dm.receivedAt.Before(publishedAt) {
				// A live delivery that matched, but arrived in the
				// subscribe-to-publish window: real, but not evidence
				// about THIS publish (step 5 above). Keep waiting.
				continue
			}
			return dm.msg, nil
		}
	}
}
