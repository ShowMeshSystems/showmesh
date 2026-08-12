package fppmqtt

import (
	"context"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"
)

// TestSubscriberInterfaceHasNoPublishMethod is the structural core of
// contract section 4.5's "cannot publish, not merely does not." It
// inspects, via reflection, the DECLARED METHOD SET of the [subscriber]
// interface type itself — the only type this package's Collector ever
// holds a handle to its live MQTT connection as (see buildClientConfig's
// doc comment) — never the runtime type of any value. This is what makes
// the assertion structural rather than a promise about today's source: no
// call in this package can invoke Publish through a subscriber-typed
// value without an explicit type assertion back to a publish-capable
// concrete type, and no such assertion exists anywhere in this package
// (grepped below, as a second, independent check).
//
// Before trusting this test, subscriber was widened to also embed
// *autopaho.ConnectionManager's Publish method (a one-line change) and
// confirmed to make this test fail; see the Step 5 Seam B report for that
// verification.
func TestSubscriberInterfaceHasNoPublishMethod(t *testing.T) {
	typ := reflect.TypeOf((*subscriber)(nil)).Elem()
	if typ.Kind() != reflect.Interface {
		t.Fatalf("subscriber is not an interface type: %v", typ.Kind())
	}

	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		if strings.Contains(strings.ToLower(name), "publish") {
			t.Errorf("subscriber interface declares method %q, which must never exist on the only handle this package holds to its live connection", name)
		}
	}

	if typ.NumMethod() != 1 || typ.Method(0).Name != "Subscribe" {
		t.Errorf("subscriber interface method set = %v methods (want exactly [Subscribe]); a wider method set on this interface is exactly the risk this test exists to catch, even if none of them happen to be named \"Publish\"",
			methodNames(typ))
	}

	// *autopaho.ConnectionManager (the real production type this
	// interface is satisfied by, per buildClientConfig/Run) DOES have a
	// Publish method — confirming that this package's read-only guarantee
	// comes from the narrow interface it chooses to use, not from the
	// underlying library lacking the capability altogether.
	cmType := reflect.TypeOf((*autopaho.ConnectionManager)(nil))
	if _, ok := cmType.MethodByName("Publish"); !ok {
		t.Fatalf("test assumption invalid: *autopaho.ConnectionManager no longer has a Publish method at all")
	}
}

func methodNames(typ reflect.Type) []string {
	names := make([]string, typ.NumMethod())
	for i := range names {
		names[i] = typ.Method(i).Name
	}
	return names
}

// TestNoWillMessageConfigured asserts directly on the
// autopaho.ClientConfig this package actually builds (buildClientConfig,
// the one function Run uses to open a connection) that WillMessage is
// nil. An LWT is itself a publish (the broker sends it on this client's
// behalf on an abnormal disconnect) — see doc.go — so this is part of the
// same read-only guarantee as TestSubscriberInterfaceHasNoPublishMethod,
// not a separate concern.
//
// Before trusting this test, buildClientConfig was changed to set
// cfg.WillMessage to a non-nil value and confirmed to make this test
// fail; see the Step 5 Seam B report for that verification.
func TestNoWillMessageConfigured(t *testing.T) {
	c, err := New(Options{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"main": "FPP-Main"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	serverURL, err := url.Parse(c.brokerURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	cfg := c.buildClientConfig(context.Background(), serverURL)
	if cfg.WillMessage != nil {
		t.Errorf("buildClientConfig().WillMessage = %+v, want nil (an LWT is itself a publish)", cfg.WillMessage)
	}
}

// TestBuildClientConfigUsesCleanStartNoSessionExpiry covers the rest of
// contract section 4.1's connection shape: "CleanStart true, no session
// expiry."
func TestBuildClientConfigUsesCleanStartNoSessionExpiry(t *testing.T) {
	c, err := New(Options{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"main": "FPP-Main"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	serverURL, err := url.Parse(c.brokerURL)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	cfg := c.buildClientConfig(context.Background(), serverURL)
	if !cfg.CleanStartOnInitialConnection {
		t.Errorf("buildClientConfig().CleanStartOnInitialConnection = false, want true")
	}
	if cfg.SessionExpiryInterval != 0 {
		t.Errorf("buildClientConfig().SessionExpiryInterval = %d, want 0 (no session expiry)", cfg.SessionExpiryInterval)
	}
}

// mqttTopicMatches reports whether topic (a concrete, non-wildcard MQTT
// topic name, as a real publish would carry) matches filter (an MQTT
// subscription filter, which may contain the wildcards '+' and '#'), per
// the MQTT specification's own matching rules: '+' matches exactly one
// level, '#' (valid only as the final filter level) matches that level and
// everything after it, and every other level must match literally.
//
// Step 5 review finding 4: the test this helper backs used to substring-
// match the filter strings this package builds — e.g. checking whether the
// literal text "command" appeared anywhere in a filter. That is NOT how
// MQTT decides whether a subscription filter matches a topic: a filter of
// "falcon/player/FPP-Main/#" contains no substring "command" anywhere in
// its own text, yet the broker WOULD deliver
// "falcon/player/FPP-Main/command/run" to a subscriber holding that
// filter, because '#' matches every remaining level. A substring check
// cannot see that; only real wildcard-matching semantics can, which is
// exactly what this function implements and what
// TestSubscriptionFiltersNeverIncludeCommandTopics now checks against.
func mqttTopicMatches(filter, topic string) bool {
	filterLevels := strings.Split(filter, "/")
	topicLevels := strings.Split(topic, "/")

	for i, f := range filterLevels {
		if f == "#" {
			// '#' matches this level and everything after it, including
			// zero further levels — valid only as the last filter level,
			// which is the only shape this package or its tests ever
			// construct.
			return true
		}
		if i >= len(topicLevels) {
			// Filter has more levels than the topic and did not end in
			// '#': no match.
			return false
		}
		if f == "+" {
			continue // matches exactly this one level, whatever it is
		}
		if f != topicLevels[i] {
			return false
		}
	}
	// Every filter level matched; the topic matches only if it has no
	// extra levels beyond what the filter accounted for.
	return len(filterLevels) == len(topicLevels)
}

// TestSubscriptionFiltersNeverIncludeCommandTopics enumerates every
// subscription filter this package would ever send to a broker (via
// subscribeAll, the only place Subscribe is ever called — see topics.go's
// doc comment for why this is a stronger property than "the wildcard
// happens not to be used for anything dangerous") and asserts NONE of them
// matches — under real MQTT wildcard semantics, via mqttTopicMatches, not
// a substring search — any of section 0's named live command topics nor
// anything under "falcon/control/".
//
// Before trusting this test, topicSpecs was temporarily given an entry for
// "command/run" and confirmed to make this test fail; see the Step 5 Seam
// B report for that verification. Step 5 review finding 4 additionally
// confirmed: mutating subscribeAll (mqttclient.go) to subscribe to
// prefix+"/"+host+"/#" instead of one filter per topicSpecs entry left the
// OLD (substring-based) version of this test green, because none of the
// resulting filter strings contain the literal text "command" — yet that
// mutated subscription genuinely receives
// "falcon/player/FPP-Main/command/run" from a real broker. This rewritten
// version, using mqttTopicMatches, was re-run against that exact mutation
// and confirmed to fail; see this package's Step 5 review-fix report.
func TestSubscriptionFiltersNeverIncludeCommandTopics(t *testing.T) {
	c, err := New(Options{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"main": "FPP-Main", "remote": "FPP-remote-01"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var captured []string
	fake := &fakeSubscriber{}
	c.subscribeAll(context.Background(), fake)
	for _, sub := range fake.subscriptions {
		captured = append(captured, sub.Topic)
	}

	if len(captured) == 0 {
		t.Fatalf("subscribeAll issued no subscriptions at all; test setup is broken")
	}

	// Concrete, real topics a live FPP or the operator's own control
	// surface actually publishes to (section 0) — not filters themselves,
	// resolved against captured via real MQTT matching semantics, never a
	// substring search.
	forbiddenTopics := []string{
		"falcon/player/FPP-Main/command/run",
		"falcon/player/FPP-Main/command/preset/triggered",
		"falcon/player/FPP-remote-01/command/run",
		"falcon/player/FPP-remote-01/command/preset/triggered",
		"falcon/control/power",
		"falcon/control/projectors",
		"falcon/control/transmitter",
		"falcon/control/brightness",
	}

	for _, filter := range captured {
		for _, topic := range forbiddenTopics {
			if mqttTopicMatches(filter, topic) {
				t.Errorf("subscription filter %q matches forbidden live topic %q under real MQTT wildcard semantics", filter, topic)
			}
		}
	}
}

// TestPublishHandlerNeverStoresCommandTopicPayload is the second, deeper
// layer of the same guarantee: even IF a broker delivered a command topic
// to this client (e.g. via a broader subscription this package does not
// actually create), the publish handler must never store or process it.
// This exercises newPublishHandler directly rather than relying solely on
// "we never subscribe to it" holding forever.
func TestPublishHandlerNeverStoresCommandTopicPayload(t *testing.T) {
	c, err := New(Options{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"main": "FPP-Main"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	deliver(c, "falcon/player/FPP-Main/command/run", []byte(`{"command":"Stop Now"}`), false)

	snap := c.store.snapshot("main")
	if _, stored := snap["command/run"]; stored {
		t.Errorf("a command/run publish was stored in the message store; it must be silently discarded, never processed")
	}
	if len(snap) != 0 {
		t.Errorf("message store for instance %q = %v, want empty (no topic outside topicSpecs was ever stored)", "main", snap)
	}
}

// fakeSubscriber records every Subscribe call without needing a real
// broker connection, mirroring internal/coordinator/broker's own
// subscriber-fake test pattern.
type fakeSubscriber struct {
	subscriptions []subscribeOptionRecord
}

// subscribeOptionRecord captures the FULL subscription configuration
// subscribeAll asks the broker for, including RetainAsPublished. Step 5
// review finding 5: an earlier version of this record dropped
// RetainAsPublished on the floor entirely (only Topic and QoS), so nothing
// in this package's unit suite could ever have caught a regression to
// RetainAsPublished: true — see TestSubscribeAllSetsRetainAsPublishedFalse
// below for why that field is load-bearing, not cosmetic.
type subscribeOptionRecord struct {
	Topic             string
	QoS               byte
	RetainAsPublished bool
}

func (f *fakeSubscriber) Subscribe(_ context.Context, s *paho.Subscribe) (*paho.Suback, error) {
	for _, opt := range s.Subscriptions {
		f.subscriptions = append(f.subscriptions, subscribeOptionRecord{
			Topic:             opt.Topic,
			QoS:               opt.QoS,
			RetainAsPublished: opt.RetainAsPublished,
		})
	}
	return nil, nil
}

// TestSubscribeAllSetsRetainAsPublishedFalse is the Step 5 review finding
// 5 regression test. RetainAsPublished:false tells the broker to set the
// RETAIN flag on a message it forwards to THIS subscriber only when the
// broker's own stored retained value for that topic is what's being
// delivered (a replay on (re)subscribe) — never on an ordinary live
// forward, regardless of whether the ORIGINAL publisher set retain=true
// when it published. Setting RetainAsPublished:true instead would mean:
// this fleet publishes EVERY topic retained (contract section 1.2), so the
// broker would set the RETAIN flag on every live forward too, and
// render.go's buildObservation treats every retained delivery as
// [observation.MeasuredUnknownAge] — ObservedAt nil, forever
// unknown_age, never StateCurrent. The consequence is not a subtle
// staleness bug: it is every signal on every otherwise-healthy configured
// host rendering as the FPP-01 ghost renders on purpose (contract section
// 4.2), permanently, fleet-wide.
//
// Before trusting this test, subscribeAll (mqttclient.go) was changed to
// RetainAsPublished: true and confirmed to make this test fail; see this
// package's Step 5 review-fix report for that verification. This is a
// unit test, not the integration-tagged suite (which is out of CI, per
// this finding's own instruction not to rely on it).
func TestSubscribeAllSetsRetainAsPublishedFalse(t *testing.T) {
	c, err := New(Options{
		BrokerURL: "tcp://127.0.0.1:1",
		Hosts:     map[string]string{"main": "FPP-Main", "remote": "FPP-remote-01"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	fake := &fakeSubscriber{}
	c.subscribeAll(context.Background(), fake)
	if len(fake.subscriptions) == 0 {
		t.Fatalf("subscribeAll issued no subscriptions at all; test setup is broken")
	}

	for _, sub := range fake.subscriptions {
		if sub.RetainAsPublished {
			t.Errorf("subscription %q has RetainAsPublished = true, want false: this fleet publishes every topic retained (contract section 1.2), so true would set the RETAIN flag on every live forward too, making every configured host's signals render unknown_age forever — the ghost rule applied fleet-wide", sub.Topic)
		}
	}
}
