package broker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

func TestRegistryGetUnknownIDIsError(t *testing.T) {
	r := NewRegistry()
	if _, err := r.Get("home-automation"); !errors.Is(err, ErrUnknownBroker) {
		t.Fatalf("Get(unregistered) = %v, want ErrUnknownBroker", err)
	}
}

// TestRegistryGetEmptyIDIsErrorNotDefault is the direct proof of "there is
// no default broker": an empty identifier must be refused exactly like an
// unknown one, never silently resolved to whichever broker happens to be
// registered.
func TestRegistryGetEmptyIDIsErrorNotDefault(t *testing.T) {
	r := NewRegistry()
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})
	if err := r.Register("home-automation", bm); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := r.Get(""); !errors.Is(err, ErrUnknownBroker) {
		t.Fatalf("Get(\"\") = %v, want ErrUnknownBroker even though a broker IS registered under a different id", err)
	}
}

func TestRegistryRegisterRejectsEmptyID(t *testing.T) {
	r := NewRegistry()
	bm := newResponseTestBrokerManager(&fakeMQTTClient{})
	if err := r.Register("", bm); !errors.Is(err, ErrUnknownBroker) {
		t.Fatalf("Register(\"\", bm) = %v, want ErrUnknownBroker", err)
	}
}

func TestRegistryRegisterRejectsNilManager(t *testing.T) {
	r := NewRegistry()
	if err := r.Register("home-automation", nil); err == nil {
		t.Fatalf("Register(id, nil) = nil error, want a refusal")
	}
}

func TestRegistryRegisterRejectsDuplicateID(t *testing.T) {
	r := NewRegistry()
	bm1 := newResponseTestBrokerManager(&fakeMQTTClient{})
	bm2 := newResponseTestBrokerManager(&fakeMQTTClient{})

	if err := r.Register("home-automation", bm1); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	if err := r.Register("home-automation", bm2); !errors.Is(err, ErrBrokerAlreadyRegistered) {
		t.Fatalf("second Register under the same id = %v, want ErrBrokerAlreadyRegistered", err)
	}

	// The first registration must survive the rejected second one.
	got, err := r.Get("home-automation")
	if err != nil {
		t.Fatalf("Get after rejected duplicate: %v", err)
	}
	if got != bm1 {
		t.Errorf("Get returned %p, want the original registration %p, not the rejected replacement %p", got, bm1, bm2)
	}
}

func TestRegistryIDsReturnsRegisteredIdentifiers(t *testing.T) {
	r := NewRegistry()
	if ids := r.IDs(); len(ids) != 0 {
		t.Fatalf("IDs() on an empty registry = %v, want empty", ids)
	}

	if err := r.Register("home-automation", newResponseTestBrokerManager(&fakeMQTTClient{})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := r.Register("garage", newResponseTestBrokerManager(&fakeMQTTClient{})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	ids := r.IDs()
	if len(ids) != 2 {
		t.Fatalf("IDs() = %v, want 2 entries", ids)
	}
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	if !seen["home-automation"] || !seen["garage"] {
		t.Errorf("IDs() = %v, want both registered identifiers", ids)
	}
}

// TestRegistryPublishRoutesToTheNamedBrokerOnly is the direct proof, at the
// Registry level, that naming one broker never touches another: Publish
// through "a" must reach only a's underlying client, never b's, even though
// both are registered and both would accept an identical publish.
func TestRegistryPublishRoutesToTheNamedBrokerOnly(t *testing.T) {
	cmA := &fakeMQTTClient{}
	cmB := &fakeMQTTClient{}
	r := NewRegistry()
	if err := r.Register("broker-a", newResponseTestBrokerManager(cmA)); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := r.Register("broker-b", newResponseTestBrokerManager(cmB)); err != nil {
		t.Fatalf("Register b: %v", err)
	}

	if err := r.Publish(context.Background(), "broker-a", "home/projectors/set", 1, false, []byte("ON")); err != nil {
		t.Fatalf("Publish via registry: %v", err)
	}

	if cmA.publishCount() != 1 {
		t.Errorf("broker A publishCount = %d, want 1", cmA.publishCount())
	}
	if cmB.publishCount() != 0 {
		t.Errorf("broker B publishCount = %d, want 0: a publish named for broker A must never reach broker B", cmB.publishCount())
	}
}

func TestRegistryPublishUnknownBrokerIsError(t *testing.T) {
	r := NewRegistry()
	err := r.Publish(context.Background(), "does-not-exist", "home/projectors/set", 1, false, []byte("ON"))
	if !errors.Is(err, ErrUnknownBroker) {
		t.Fatalf("Publish(unknown broker) = %v, want ErrUnknownBroker", err)
	}
}

func TestRegistryAwaitResponseUnknownBrokerIsError(t *testing.T) {
	r := NewRegistry()
	_, err := r.AwaitResponse(context.Background(), "does-not-exist", ResponseRequest{
		PublishTopic:  "home/projectors/set",
		ResponseTopic: "home/projectors/state",
		Deadline:      time.Second,
		Match:         textMatcher("on"),
	})
	if !errors.Is(err, ErrUnknownBroker) {
		t.Fatalf("AwaitResponse(unknown broker) = %v, want ErrUnknownBroker", err)
	}
}

// TestRegistryAwaitResponseResolvesPublishAndSubscribeToOneBroker proves,
// with fakes standing in for two independent broker connections, that
// AwaitResponse's publish and its matching subscribe both land on the
// BrokerManager registered under the named id, and never touch the other
// registered broker even though it is wired up identically and would
// happily accept the same traffic. This is the fake-based counterpart of
// registry_integration_test.go's real-broker proof of the same rule.
func TestRegistryAwaitResponseResolvesPublishAndSubscribeToOneBroker(t *testing.T) {
	bmA := &BrokerManager{now: time.Now}
	cmB := &fakeMQTTClient{}
	cmA := &fakeMQTTClient{
		subscribeFunc: func(ctx context.Context, s *paho.Subscribe) (*paho.Suback, error) {
			// The instant broker A's own SUBSCRIBE "lands", simulate broker
			// A's external responder answering on broker A — this proves
			// the wait resolved off broker A's own delivery path, not
			// broker B's.
			go bmA.dispatchToWaiters(Message{Topic: "home/projectors/state", Payload: []byte("on"), Retained: false})
			return &paho.Suback{}, nil
		},
	}
	bmA.cm = cmA

	r := NewRegistry()
	if err := r.Register("broker-a", bmA); err != nil {
		t.Fatalf("Register a: %v", err)
	}
	if err := r.Register("broker-b", newResponseTestBrokerManager(cmB)); err != nil {
		t.Fatalf("Register b: %v", err)
	}

	msg, err := r.AwaitResponse(context.Background(), "broker-a", ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state",
		ResponseQoS:    1,
		Deadline:       2 * time.Second,
		Match:          textMatcher("on"),
	})
	if err != nil {
		t.Fatalf("AwaitResponse via registry: %v", err)
	}
	if string(msg.Payload) != "on" {
		t.Errorf("AwaitResponse returned %+v, want the live \"on\" delivered on broker A", msg)
	}

	if cmA.subscribeCount() != 1 {
		t.Errorf("broker A subscribeCount = %d, want 1", cmA.subscribeCount())
	}
	if cmB.subscribeCount() != 0 {
		t.Errorf("broker B subscribeCount = %d, want 0: naming broker A must never subscribe on broker B", cmB.subscribeCount())
	}
	if cmB.publishCount() != 0 {
		t.Errorf("broker B publishCount = %d, want 0: naming broker A must never publish on broker B", cmB.publishCount())
	}
}
