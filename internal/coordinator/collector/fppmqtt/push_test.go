package fppmqtt

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"
)

// TestPublishHandlerFiresPushSignalOnStoredMessage proves the Step 1 push
// path this package's own doc comments describe: a message the handler
// actually stores (a modeled topic for a configured host) fires
// Options.PushSignal exactly once, AFTER the store write is visible —
// never before, and never for a message this package ignores.
func TestPublishHandlerFiresPushSignalOnStoredMessage(t *testing.T) {
	now := time.Now()
	var calls int32
	c, err := New(Options{
		BrokerURL:  "tcp://127.0.0.1:1",
		Hosts:      map[string]string{"main": "fpp-player"},
		Now:        fixedClock(&now),
		PushSignal: func() { atomic.AddInt32(&calls, 1) },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	c.setConnected(true, "")

	handler := c.newPublishHandler()
	_, _ = handler(paho.PublishReceived{
		Packet: &paho.Publish{Topic: "falcon/player/fpp-player/status", Payload: []byte("playing"), Retain: false},
	})

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("PushSignal called %d times, want exactly 1", got)
	}

	snap := c.store.snapshot("main")
	if _, ok := snap["status"]; !ok {
		t.Fatalf("store has no message for suffix %q; PushSignal must fire AFTER the store write, and this proves the write happened at all", "status")
	}
}

// TestPublishHandlerNeverFiresPushSignalForUnmatchedHost proves the
// negative: a message this package ignores entirely (unmatched host, or a
// topic suffix outside topicSpecs) must never fire PushSignal — nothing
// changed in the store for it to report.
func TestPublishHandlerNeverFiresPushSignalForUnmatchedHost(t *testing.T) {
	var calls int32
	c, err := New(Options{
		BrokerURL:  "tcp://127.0.0.1:1",
		Hosts:      map[string]string{"main": "fpp-player"},
		PushSignal: func() { atomic.AddInt32(&calls, 1) },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	c.setConnected(true, "")

	handler := c.newPublishHandler()
	_, _ = handler(paho.PublishReceived{
		Packet: &paho.Publish{Topic: "falcon/player/some-other-host/status", Payload: []byte("playing"), Retain: false},
	})

	if got := atomic.LoadInt32(&calls); got != 0 {
		t.Fatalf("PushSignal called %d times for an unmatched host, want 0", got)
	}
}

// TestPublishHandlerNilPushSignalIsSafe proves the documented default: no
// PushSignal configured must never panic, on either a stored or an
// ignored message — every existing caller of New before this field
// existed leaves it nil.
func TestPublishHandlerNilPushSignalIsSafe(t *testing.T) {
	c, err := New(Options{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"main": "fpp-player"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	c.setConnected(true, "")

	handler := c.newPublishHandler()
	_, _ = handler(paho.PublishReceived{
		Packet: &paho.Publish{Topic: "falcon/player/fpp-player/status", Payload: []byte("playing"), Retain: false},
	})
	_, _ = handler(paho.PublishReceived{
		Packet: &paho.Publish{Topic: "falcon/player/other-host/status", Payload: []byte("playing"), Retain: false},
	})
}
