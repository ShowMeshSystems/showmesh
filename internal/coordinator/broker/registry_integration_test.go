//go:build integration

// This file proves Registry's one real correctness property — that naming
// a broker identifier resolves publish AND subscribe to the same broker
// connection — against two REAL, independent Mosquitto brokers rather than
// fakes standing in for "another broker". registry_test.go's fake-based
// TestRegistryAwaitResponseResolvesPublishAndSubscribeToOneBroker proves the
// same rule at the Go-call level; this file proves it on the wire, with a
// message that a broken implementation really could receive from the wrong
// broker if Registry (or a future caller bypassing it) ever resolved the
// publish and the subscribe independently. See response_integration_test.go
// for this package's other real-broker tests and startTestMosquitto for the
// shared container helper this file reuses.
//
//	go test -tags=integration -race -v ./internal/coordinator/broker/...
package broker

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestIntegrationRegistryIsolatesBrokerAFromBrokerB is item 2 of this
// package's task specification: "a waiter that subscribes on one broker and
// publishes on another is the exact silent failure this exists to prevent,
// and it deserves a real test rather than a comment." Two independent
// Mosquitto containers stand in for the ShowMesh control-plane broker and
// the operator's home-automation broker; the SAME topic name is used on
// both, specifically so a Registry bug that resolved broker A's subscribe
// but broker B's publish (or the reverse) would still "work" on the wire —
// only genuine per-identifier isolation defeats that.
func TestIntegrationRegistryIsolatesBrokerAFromBrokerB(t *testing.T) {
	brokerAURL := startTestMosquitto(t)
	brokerBURL := startTestMosquitto(t)

	bmA := newConnectedTestBrokerManager(t, brokerAURL, "coordinator-broker-a")
	bmB := newConnectedTestBrokerManager(t, brokerBURL, "coordinator-broker-b")

	reg := NewRegistry()
	if err := reg.Register("broker-a", bmA); err != nil {
		t.Fatalf("Register broker-a: %v", err)
	}
	if err := reg.Register("broker-b", bmB); err != nil {
		t.Fatalf("Register broker-b: %v", err)
	}

	const responseTopic = "home/projectors/state"

	// An external responder that only ever publishes on broker B, on the
	// exact topic name a waiter on broker A is about to wait on. If
	// AwaitResponse-via-registry ever resolved its subscribe against broker
	// A but something let this delivery cross over, this message would
	// satisfy it even though it never touched broker A at all.
	externalOnB := newConnectedTestBrokerManager(t, brokerBURL, "external-on-broker-b")
	go func() {
		time.Sleep(600 * time.Millisecond)
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := externalOnB.Publish(pubCtx, responseTopic, 1, false, []byte("on")); err != nil {
			t.Errorf("publishing on broker B: %v", err)
		}
	}()

	start := time.Now()
	_, err := reg.AwaitResponse(context.Background(), "broker-a", ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  responseTopic,
		ResponseQoS:    1,
		Deadline:       1500 * time.Millisecond,
		Match:          textMatcher("on"),
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("AwaitResponse via registry named \"broker-a\" resolved successfully off a message published only on broker B; want it isolated to broker A")
	}
	if !errors.Is(err, ErrResponseDeadlineExceeded) {
		t.Fatalf("AwaitResponse: err = %v, want ErrResponseDeadlineExceeded (broker B's answer must never satisfy a wait on broker A)", err)
	}
	if elapsed < 1500*time.Millisecond {
		t.Errorf("AwaitResponse returned after %v, want it to have actually waited out the 1.5s deadline rather than being satisfied by broker B's traffic", elapsed)
	}

	// Now the positive case, against the same registry and the same topic
	// name: an external responder ON broker A resolves it normally. This
	// rules out the isolation above being a trivial pass from a broken
	// Registry that never routes anywhere.
	externalOnA := newConnectedTestBrokerManager(t, brokerAURL, "external-on-broker-a")
	go func() {
		time.Sleep(400 * time.Millisecond)
		pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := externalOnA.Publish(pubCtx, responseTopic, 1, false, []byte("on")); err != nil {
			t.Errorf("publishing on broker A: %v", err)
		}
	}()

	msg, err := reg.AwaitResponse(context.Background(), "broker-a", ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  responseTopic,
		ResponseQoS:    1,
		Deadline:       10 * time.Second,
		Match:          textMatcher("on"),
	})
	if err != nil {
		t.Fatalf("AwaitResponse via registry named \"broker-a\" against broker A's own responder: %v", err)
	}
	if string(msg.Payload) != "on" {
		t.Errorf("AwaitResponse returned payload %q, want \"on\"", msg.Payload)
	}
}
