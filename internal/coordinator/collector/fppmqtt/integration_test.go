//go:build integration

// This file is this package's own integration suite, in the shape
// internal/coordinator/collector/fpp/integration_test.go established for
// its FPP bench counterpart: it proves what only a real broker connection
// can prove — that [Collector.Run]'s actual autopaho wiring (connect,
// subscribeAll, the OnPublishReceived handler) delivers messages into
// [Collector.Poll]'s output at all, end to end, as opposed to the rest of
// this package's unit suite, which drives [Collector.Poll] by calling the
// publish handler directly and never exercises Run or a real Subscribe
// call.
//
// It never fails for want of the dependency: it skips cleanly, with a
// clear message, unless SHOWMESH_TEST_MQTT_BROKER and the separate collector
// and publisher credentials are set — see requireTestBroker.
// `make test-integration-fppmqtt`
// (scripts/test-integration-fppmqtt.sh) provisions an authenticated
// throwaway Mosquitto and exports those variables; that is the normal way
// to run this suite. A manual run must likewise use only a disposable broker
// and supply distinct collector-read/publisher-write credentials.
//
// Per the Step 5 spec section 0's absolute rule, this suite must NEVER be
// pointed at the reference installation's broker, or any other live broker, — only a
// throwaway local Mosquitto, exactly as scripts/test-integration.sh runs
// for the coordinator's own control-plane suite. It publishes freely
// (this test IS the publisher, playing the role of a real FPP), which is
// the one context in this whole package where that is safe: the broker
// under test is disposable and nothing downstream of it is a real display.
package fppmqtt

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/autopaho"
	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const (
	envTestMQTTBroker        = "SHOWMESH_TEST_MQTT_BROKER"
	envTestCollectorUsername = "SHOWMESH_TEST_FPPMQTT_COLLECTOR_USERNAME"
	envTestCollectorPassword = "SHOWMESH_TEST_FPPMQTT_COLLECTOR_PASSWORD"
	envTestPublisherUsername = "SHOWMESH_TEST_FPPMQTT_PUBLISHER_USERNAME"
	envTestPublisherPassword = "SHOWMESH_TEST_FPPMQTT_PUBLISHER_PASSWORD"
)

type testBroker struct {
	url string

	collectorUsername string
	collectorPassword string
	publisherUsername string
	publisherPassword string
}

// requireTestBroker skips t (with an explicit message, per the Step 5
// contract section 9 and this repo's established "say so explicitly"
// convention — see scripts/test-integration.sh) unless
// SHOWMESH_TEST_MQTT_BROKER names a throwaway local broker to run against;
// the distinct credentials prove the collector's read-only role against a
// publisher that can write only FPP telemetry.
func requireTestBroker(t *testing.T) testBroker {
	t.Helper()
	broker := os.Getenv(envTestMQTTBroker)
	if broker == "" {
		skipOrFatalDependency(t, depBroker, "%s not set; skipping this package's real-broker integration suite (see this file's doc comment for how to run it)", envTestMQTTBroker)
		return testBroker{}
	}
	credentials := testBroker{
		url:               broker,
		collectorUsername: os.Getenv(envTestCollectorUsername),
		collectorPassword: os.Getenv(envTestCollectorPassword),
		publisherUsername: os.Getenv(envTestPublisherUsername),
		publisherPassword: os.Getenv(envTestPublisherPassword),
	}
	if credentials.collectorUsername == "" || credentials.collectorPassword == "" || credentials.publisherUsername == "" || credentials.publisherPassword == "" {
		skipOrFatalDependency(t, depBroker, "authenticated FPP MQTT test credentials are incomplete; run scripts/test-integration-fppmqtt.sh (or set %s, %s, %s, and %s)", envTestCollectorUsername, envTestCollectorPassword, envTestPublisherUsername, envTestPublisherPassword)
		return testBroker{}
	}
	return credentials
}

// envRequireTestDeps names which dependencies the harness that invoked
// `go test` actually guarantees, as a comma-separated list (for example
// "broker" or "broker,fpp"). A guard for a dependency on that list becomes
// a hard failure instead of a skip; a guard for anything else still skips.
//
// It is a LIST rather than a boolean deliberately. The first revision was a
// truthy flag and CI failed on it at once: `make test-integration` starts a
// broker and no fppd, so a boolean made it demand an FPP it never supplies
// and turned three legitimately-skipping tests into failures. A harness may
// only be held to the dependencies it actually provides. See
// test/integration/harness_test.go for the full note.
const envRequireTestDeps = "SHOWMESH_REQUIRE_TEST_DEPS"

// Dependency names carried in envRequireTestDeps.
const (
	depBroker = "broker"
	depFPP    = "fpp"
)

// requireTestDep reports whether the invoking harness declared that it
// supplies dep. "1"/"true"/"yes"/"all" mean every dependency, so an older
// harness cannot silently weaken the guard by naming nothing.
func requireTestDep(dep string) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(envRequireTestDeps)))
	switch raw {
	case "", "0", "false", "no":
		return false
	case "1", "true", "yes", "all":
		return true
	}
	for _, part := range strings.Split(raw, ",") {
		if strings.TrimSpace(part) == dep {
			return true
		}
	}
	return false
}

// skipOrFatalDependency skips when the invoking harness did not claim to
// supply dep, and fails hard when it did and then did not.
func skipOrFatalDependency(t *testing.T, dep string, format string, args ...any) {
	t.Helper()
	if requireTestDep(dep) {
		t.Fatalf(format, args...)
		return
	}
	t.Skipf(format, args...)
}

// testPublisher is a throwaway MQTT client this suite uses to play the
// role of a real FPP: it is the ONE place in this package's test suite
// that publishes anything, and it does so only against the disposable
// broker requireTestBroker validated, never against a device this project
// does not own.
type testPublisher struct {
	cm     *autopaho.ConnectionManager
	cancel context.CancelFunc
}

// newTestPublisher's connection is governed by a long-lived context
// (cancelled only by disconnect), deliberately separate from the short
// timeout used merely to wait for the initial connect below: autopaho's
// ConnectionManager treats the context passed to NewConnection as the
// connection's own lifetime, not a connect deadline (the same pattern
// internal/coordinator/broker.NewBrokerManager and this package's own Run
// follow) — an early version of this helper used one short-lived context
// for both and found the connection torn down immediately after
// construction, before a single Publish could succeed.
func newTestPublisher(t *testing.T, broker testBroker) *testPublisher {
	t.Helper()
	serverURL, err := url.Parse(broker.url)
	if err != nil {
		t.Fatalf("parsing broker url %q: %v", broker.url, err)
	}

	connCtx, cancel := context.WithCancel(context.Background())

	connected := make(chan struct{}, 1)
	cfg := autopaho.ClientConfig{
		ServerUrls:      []*url.URL{serverURL},
		KeepAlive:       30,
		ConnectTimeout:  10 * time.Second,
		ConnectUsername: broker.publisherUsername,
		ConnectPassword: []byte(broker.publisherPassword),
		OnConnectionUp: func(*autopaho.ConnectionManager, *paho.Connack) {
			select {
			case connected <- struct{}{}:
			default:
			}
		},
		ClientConfig: paho.ClientConfig{ClientID: "fppmqtt-integration-publisher"},
	}
	cm, err := autopaho.NewConnection(connCtx, cfg)
	if err != nil {
		cancel()
		t.Fatalf("starting test publisher connection: %v", err)
	}

	waitCtx, waitCancel := context.WithTimeout(connCtx, 15*time.Second)
	defer waitCancel()
	select {
	case <-connected:
	case <-waitCtx.Done():
		cancel()
		t.Fatalf("test publisher did not connect within timeout")
	}
	return &testPublisher{cm: cm, cancel: cancel}
}

func (p *testPublisher) publish(t *testing.T, topic string, payload []byte, retain bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := p.cm.Publish(ctx, &paho.Publish{
		Topic:   topic,
		Payload: payload,
		QoS:     1,
		Retain:  retain,
	}); err != nil {
		t.Fatalf("publishing to %q: %v", topic, err)
	}
}

func (p *testPublisher) disconnect(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.cm.Disconnect(ctx)
	p.cancel()
}

// TestIntegrationRunDeliversRetainedAndLiveMessages is this suite's one
// real end-to-end case: it starts a genuine Collector.Run against a real
// broker, publishes a RETAINED message before the collector's subscribe
// has necessarily happened (so it is replayed on subscribe, exactly the
// real-fpp-ghost scenario), then a LIVE (non-retained) message on a different
// topic, and asserts Poll sees both, with the same retained/live
// distinction the rest of this package's unit suite proves against a
// directly-injected handler call.
func TestIntegrationRunDeliversRetainedAndLiveMessages(t *testing.T) {
	broker := requireTestBroker(t)

	pub := newTestPublisher(t, broker)
	defer pub.disconnect(t)

	// Publish the retained message FIRST, before the collector even
	// exists — this is what makes it a genuine "replay on subscribe" case
	// rather than a live delivery that merely happens to carry
	// Retain=true.
	pub.publish(t, "falcon/player/FPP-IT/status", []byte("idle"), true)

	now := time.Now()
	c, err := New(Options{
		BrokerURL: broker.url,
		Username:  broker.collectorUsername,
		Password:  broker.collectorPassword,
		Hosts:     map[string]string{"it": "FPP-IT"},
		Now:       func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runDone := make(chan error, 1)
	go func() { runDone <- c.Run(runCtx) }()

	// Poll until the retained message has been observed, or time out.
	deadline := time.Now().Add(10 * time.Second)
	var retainedObs observation.Observation
	for time.Now().Before(deadline) {
		obs, _ := c.Poll(context.Background())
		found := false
		for _, o := range obs {
			if o.Signal == SignalStatus && o.Absence == "" {
				retainedObs = o
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if retainedObs.Signal == "" {
		t.Fatalf("fpp.status was never observed within the deadline; Run/subscribe/handler wiring did not deliver the retained message")
	}
	if retainedObs.ObservedAt != nil {
		t.Errorf("retained message: ObservedAt = %v, want nil (unknown age) even over a real broker connection", *retainedObs.ObservedAt)
	}
	if retainedObs.StateAt(time.Now()) != observation.StateUnknownAge {
		t.Errorf("retained message: StateAt = %q, want %q", retainedObs.StateAt(time.Now()), observation.StateUnknownAge)
	}
	if retainedObs.Value != "idle" {
		t.Errorf("retained message: Value = %#v, want %q", retainedObs.Value, "idle")
	}

	// Now publish a LIVE message on a different topic while the collector
	// is definitely already connected and subscribed.
	pub.publish(t, "falcon/player/FPP-IT/ready", []byte("1"), false)

	deadline = time.Now().Add(10 * time.Second)
	var liveObs observation.Observation
	for time.Now().Before(deadline) {
		obs, _ := c.Poll(context.Background())
		found := false
		for _, o := range obs {
			if o.Signal == SignalReady && o.Absence == "" {
				liveObs = o
				found = true
			}
		}
		if found {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if liveObs.Signal == "" {
		t.Fatalf("fpp.ready was never observed within the deadline")
	}
	if liveObs.ObservedAt == nil {
		t.Errorf("live message: ObservedAt = nil, want a real receipt time")
	}
	if liveObs.StateAt(time.Now()) != observation.StateCurrent {
		t.Errorf("live message: StateAt = %q, want %q", liveObs.StateAt(time.Now()), observation.StateCurrent)
	}
	if liveObs.Value != true {
		t.Errorf("live message: Value = %#v, want true", liveObs.Value)
	}

	cancel()
	select {
	case err := <-runDone:
		if err != nil {
			t.Errorf("Run() returned error = %v after ctx cancellation, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("Run() did not return within 10s of ctx cancellation")
	}
}

// TestIntegrationSubscriptionSurvivesUnrelatedHostPublish is a light
// second case proving contract section 4.4's routing against a real
// broker: a publish for an unconfigured host must never appear as an
// observation for any configured resource.
func TestIntegrationSubscriptionSurvivesUnrelatedHostPublish(t *testing.T) {
	broker := requireTestBroker(t)

	pub := newTestPublisher(t, broker)
	defer pub.disconnect(t)

	c, err := New(Options{
		BrokerURL: broker.url,
		Username:  broker.collectorUsername,
		Password:  broker.collectorPassword,
		Hosts:     map[string]string{"it2": "FPP-IT2"},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(runCtx) }()

	// Give the connection a moment to come up and subscribe.
	time.Sleep(1 * time.Second)

	pub.publish(t, "falcon/player/FPP-Unrelated/status", []byte("idle"), false)
	pub.publish(t, "falcon/player/FPP-IT2/status", []byte("playing"), false)

	deadline := time.Now().Add(10 * time.Second)
	var got observation.Observation
	for time.Now().Before(deadline) {
		polled, _ := c.Poll(context.Background())
		for _, o := range polled {
			if o.Signal == SignalStatus && o.Absence == "" {
				got = o
			}
		}
		if got.Signal != "" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got.Signal == "" {
		t.Fatalf("fpp.status for the configured host was never observed")
	}
	if got.Resource.ID != "it2" {
		t.Errorf("observation resource = %q, want %q", got.Resource.ID, "it2")
	}
	if got.Value != "playing" {
		t.Errorf("observation value = %#v, want %q (the unrelated host's message must never be attributed here)", got.Value, "playing")
	}
}
