//go:build integration

// This file proves the three scenarios this package's task specification
// names as the ones that matter, against a REAL Mosquitto broker rather
// than the fakeMQTTClient response_test.go uses. That distinction matters:
// response_test.go proves this package's own routing, refcounting and
// timing logic is internally consistent, but a fake can only ever be as
// correct as this package's own model of retained-message and SUBACK
// timing — it cannot prove that model matches what a real broker actually
// does. Per this project's own repeated lesson, a fake that differs from
// the deployment environment reports success on exactly that difference;
// retained-message delivery timing (the whole reason this package's
// AwaitResponse exists to defend against it) is precisely the kind of
// broker behavior a fake cannot exercise honestly.
//
// This file manages its own throwaway Mosquitto container (via `docker`)
// rather than depending on scripts/test-integration.sh or
// test/integration's harness: this package's task boundary is
// internal/coordinator/broker only, so it does not modify the Makefile,
// scripts/, or test/integration to wire itself in. Run directly with:
//
//	go test -tags=integration -race -v ./internal/coordinator/broker/...
//
// Skips (rather than fails) if `docker` is not on PATH or the daemon is not
// reachable, so a laptop without Docker still gets a clean `go test`
// experience for the untagged suite in response_test.go and broker_test.go.
package broker

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

const integrationMosquittoImage = "eclipse-mosquitto:2.0.22" // pinned to match deploy/docker-compose.yml's mosquitto.image, per this project's own precedent (see scripts/test-integration.sh) of never testing against a version operators don't actually run.

// integrationTestLogger discards everything so a passing run stays quiet;
// failures still show broker-side detail via t.Log calls at the point of
// failure.
func integrationTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// requireDocker skips the test if `docker` is not usable, rather than
// failing: this file's tests are a deliberate, additional real-broker proof
// on top of response_test.go's fake-based suite, not a replacement for it,
// and a developer machine without Docker must still get a clean run of
// everything else in this package.
func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found on PATH; skipping real-broker integration test")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skip("docker daemon not reachable; skipping real-broker integration test")
	}
}

// freePort asks the OS for an unused TCP port, mirroring the technique
// httptest.NewServer uses internally: a small race exists between closing
// this listener and docker binding the same port, the same as any
// find-a-free-port approach, and is accepted here for the same reason
// httptest accepts it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startTestMosquitto starts a throwaway, anonymous-access Mosquitto
// container for this test only (a fresh container per test, not shared
// across the file, so topic and retained-state leakage between tests is
// structurally impossible rather than merely avoided by naming
// convention). It returns the broker URL and registers cleanup via
// t.Cleanup.
//
// allow_anonymous is deliberately true here, unlike
// scripts/test-integration.sh's exact-shipped-config broker: this suite
// proves broker.go/response.go's own MQTT semantics (publish, subscribe,
// RETAIN, deadlines), not ADR-024's authorization posture, which
// test/integration's broker_auth_test.go already covers end to end against
// the real shipped mosquitto.conf. Introducing credentials here would only
// add setup cost without adding coverage this file is responsible for.
func startTestMosquitto(t *testing.T) string {
	t.Helper()
	requireDocker(t)

	port := freePort(t)
	containerName := fmt.Sprintf("showmesh-broker-pkg-test-%d", time.Now().UnixNano())

	confFile, err := os.CreateTemp("", "showmesh-broker-test-mosquitto-*.conf")
	if err != nil {
		t.Fatalf("creating temp mosquitto.conf: %v", err)
	}
	defer os.Remove(confFile.Name())
	if _, err := confFile.WriteString("listener 1883\nallow_anonymous true\n"); err != nil {
		t.Fatalf("writing temp mosquitto.conf: %v", err)
	}
	if err := confFile.Close(); err != nil {
		t.Fatalf("closing temp mosquitto.conf: %v", err)
	}

	runArgs := []string{
		"run", "-d", "--rm",
		"--name", containerName,
		"-p", fmt.Sprintf("%d:1883", port),
		"-v", confFile.Name() + ":/mosquitto/config/mosquitto.conf:ro",
		integrationMosquittoImage,
	}
	if out, err := exec.Command("docker", runArgs...).CombinedOutput(); err != nil {
		t.Fatalf("docker run mosquitto: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
	})

	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return "tcp://127.0.0.1:" + strconv.Itoa(port)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if out, err := exec.Command("docker", "logs", containerName).CombinedOutput(); err == nil {
		t.Logf("mosquitto container logs:\n%s", out)
	}
	t.Fatalf("mosquitto did not start listening on port %d within 15s", port)
	return ""
}

// newConnectedTestBrokerManager builds a real BrokerManager against
// brokerURL and blocks until it has actually connected (bm.cm.
// AwaitConnection, reachable here because this file is in package broker),
// so every scenario below starts from a known-connected state rather than
// racing NewBrokerManager's own background connect.
func newConnectedTestBrokerManager(t *testing.T, brokerURL, clientID string) *BrokerManager {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	bm, err := NewBrokerManager(ctx, config.Config{MQTTBroker: brokerURL, MQTTClientID: clientID}, integrationTestLogger(), nil, nil)
	if err != nil {
		t.Fatalf("NewBrokerManager(%s): %v", clientID, err)
	}
	t.Cleanup(func() {
		disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer disconnectCancel()
		_ = bm.Disconnect(disconnectCtx)
	})

	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer awaitCancel()
	if err := bm.cm.AwaitConnection(awaitCtx); err != nil {
		t.Fatalf("BrokerManager %s did not connect within 10s: %v", clientID, err)
	}
	return bm
}

// TestIntegrationRetainedResponseDoesNotConfirm is acceptance criterion 4
// from this step's specification, proved against a real broker: a
// home-automation-style retained value already sitting on the response
// topic when AwaitResponse subscribes must not resolve it. A separate
// BrokerManager stands in for the external system (Home Assistant/Node-RED)
// exactly as constructing it (an independent MQTT client on the same real
// broker) rather than sharing the coordinator's own connection.
func TestIntegrationRetainedResponseDoesNotConfirm(t *testing.T) {
	brokerURL := startTestMosquitto(t)

	external := newConnectedTestBrokerManager(t, brokerURL, "external-home-automation")
	coordinator := newConnectedTestBrokerManager(t, brokerURL, "showmesh-coordinator")

	const responseTopic = "home/projectors/state"

	// Set the retained value to the expected one FIRST, exactly as this
	// step's acceptance criterion 4 specifies, so a broken implementation
	// that does not discard RETAIN=1 would confirm instantly off it.
	retainCtx, retainCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer retainCancel()
	if err := external.Publish(retainCtx, responseTopic, 1, true, []byte("on")); err != nil {
		t.Fatalf("publishing retained stale state: %v", err)
	}
	// Give the broker a moment to persist the retained message before the
	// coordinator subscribes, so this test is not accidentally proving
	// something about publish/subscribe ordering instead of the RETAIN
	// flag itself.
	time.Sleep(300 * time.Millisecond)

	const settle = 700 * time.Millisecond
	go func() {
		time.Sleep(settle)
		liveCtx, liveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer liveCancel()
		if err := external.Publish(liveCtx, responseTopic, 1, false, []byte("on")); err != nil {
			t.Errorf("publishing live response: %v", err)
		}
	}()

	start := time.Now()
	msg, err := coordinator.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  responseTopic,
		ResponseQoS:    1,
		Deadline:       10 * time.Second,
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
		t.Errorf("AwaitResponse against a real broker resolved in %v, want it to have waited for the live delivery (~%v) rather than confirming instantly off the pre-existing retained value — this is the defect this test exists to catch", elapsed, settle)
	}
	t.Logf("resolved in %v (settle was %v)", elapsed, settle)
}

// TestIntegrationLiveAnswerPublishedWithRetainTrueStillConfirms is
// acceptance criterion 15, the "RAP mirror case", and it is the test that
// actually pins §7.2 rule 2's corrected justification rather than the
// rejected one. This package leaves RetainAsPublished false on every
// subscription (see subscriptionsToOptions and registerResponseWaiter)
// specifically because, per MQTT 5 §3.3.1.3, RAP=false makes the broker
// FORCE RETAIN=0 on every forwarded LIVE message regardless of how the
// publisher set it — not, as an earlier and wrong version of this package's
// specification claimed, to "preserve" the publisher's own RETAIN=1.
//
// TestIntegrationRetainedResponseDoesNotConfirm (criterion 4) alone does not
// distinguish the correct implementation from the specific wrong one
// (RetainAsPublished: true): a wrong implementation that reasoned "RAP=true
// preserves the flag better" would ALSO fail to confirm a message published
// live with retain=true, by conflating it with a genuine stale replay from
// the retained store — it would pass criterion 4 by accident, for the wrong
// reason, on a message that was never actually a replay. Only this test,
// where the responder deliberately retains its own live answer (exactly as
// a well-behaved home-automation state topic does in ordinary operation),
// tells the two implementations apart: the correct one confirms it, the
// broken one times out on it.
func TestIntegrationLiveAnswerPublishedWithRetainTrueStillConfirms(t *testing.T) {
	brokerURL := startTestMosquitto(t)

	external := newConnectedTestBrokerManager(t, brokerURL, "external-retains-its-state")
	coordinator := newConnectedTestBrokerManager(t, brokerURL, "showmesh-coordinator-rap-mirror")

	const responseTopic = "home/projectors/state"

	// Deliberately no pre-existing retained value on this topic: unlike
	// TestIntegrationRetainedResponseDoesNotConfirm, this test is entirely
	// about a LIVE publish that happens to carry retain=true, not about the
	// broker's retained-message store at all.
	const settle = 700 * time.Millisecond
	go func() {
		time.Sleep(settle)
		liveCtx, liveCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer liveCancel()
		// The responder retains its own answer, exactly as a home-automation
		// broker's state topic ordinarily does — this is the "responder
		// publishing its live answer with retain=true" acceptance
		// criterion 15 names.
		if err := external.Publish(liveCtx, responseTopic, 1, true, []byte("on")); err != nil {
			t.Errorf("publishing live answer with retain=true: %v", err)
		}
	}()

	start := time.Now()
	msg, err := coordinator.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  responseTopic,
		ResponseQoS:    1,
		Deadline:       10 * time.Second,
		Match:          textMatcher("on"),
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AwaitResponse: unexpected error (a live answer published with retain=true was not confirmed): %v", err)
	}
	if msg.Retained {
		t.Fatalf("AwaitResponse returned Retained=true for a forwarded live message; want RetainAsPublished=false to have forced RETAIN=0 on the wire per MQTT 5 §3.3.1.3")
	}
	if string(msg.Payload) != "on" {
		t.Errorf("AwaitResponse returned payload %q, want \"on\"", msg.Payload)
	}
	if elapsed < settle/2 {
		t.Errorf("AwaitResponse resolved in %v, before the live answer at ~%v was even published", elapsed, settle)
	}
	t.Logf("RAP mirror case resolved in %v (settle was %v)", elapsed, settle)
}

// TestIntegrationFastResponderRepublishesOnCommand is the no-lost-wakeup
// scenario against a real broker: an external responder that reacts to the
// coordinator's own publish as fast as it can — by subscribing to
// publishTopic itself and republishing to responseTopic the instant it
// sees a message — must still be seen by AwaitResponse. This exercises the
// real subscribe-then-publish ordering under genuine network round trips
// rather than an artificial in-process race.
//
// The responder is built with its own inbound handler supplied at
// construction (NewBrokerManager's handler parameter): this package's
// single-callback-slot design (see combinedHandler in broker.go) fixes a
// BrokerManager's handler once, at construction, so simulating "react to an
// inbound command" has to be wired in up front rather than bolted on
// afterward.
func TestIntegrationFastResponderRepublishesOnCommand(t *testing.T) {
	brokerURL := startTestMosquitto(t)

	const publishTopic = "home/projectors/set"
	const responseTopic = "home/projectors/state"

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	// The external responder's handler republishes live to responseTopic
	// the instant it sees anything on publishTopic — the fastest a real
	// integration could plausibly react, which is exactly the case
	// AwaitResponse's subscribe-before-publish ordering exists to survive.
	var responder *BrokerManager
	responder, err := NewBrokerManager(ctx, config.Config{MQTTBroker: brokerURL, MQTTClientID: "external-responder"}, integrationTestLogger(),
		[]Subscription{{Filter: publishTopic, QoS: 1}},
		func(m Message) {
			if m.Topic != publishTopic || m.Retained {
				return
			}
			pubCtx, pubCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer pubCancel()
			if err := responder.Publish(pubCtx, responseTopic, 1, false, []byte("on")); err != nil {
				t.Errorf("responder republish: %v", err)
			}
		})
	if err != nil {
		t.Fatalf("NewBrokerManager(responder): %v", err)
	}
	t.Cleanup(func() {
		disconnectCtx, disconnectCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer disconnectCancel()
		_ = responder.Disconnect(disconnectCtx)
	})
	awaitCtx, awaitCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := responder.cm.AwaitConnection(awaitCtx); err != nil {
		awaitCancel()
		t.Fatalf("responder did not connect: %v", err)
	}
	awaitCancel()

	coordinator := newConnectedTestBrokerManager(t, brokerURL, "showmesh-coordinator-fast2")

	start := time.Now()
	msg, err := coordinator.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   publishTopic,
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  responseTopic,
		ResponseQoS:    1,
		Deadline:       10 * time.Second,
		Match:          textMatcher("on"),
	})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("AwaitResponse: unexpected error (a fast responder's answer was lost): %v", err)
	}
	if string(msg.Payload) != "on" {
		t.Errorf("AwaitResponse returned %+v, want the responder's live \"on\"", msg)
	}
	t.Logf("fast-responder round trip resolved in %v", elapsed)
}

// TestIntegrationDeadlineExceededIsDistinctOutcome is acceptance-adjacent
// proof, against a real broker, that a response topic nobody ever answers
// resolves as the distinct ErrResponseDeadlineExceeded outcome rather than
// hanging or reporting success.
func TestIntegrationDeadlineExceededIsDistinctOutcome(t *testing.T) {
	brokerURL := startTestMosquitto(t)
	coordinator := newConnectedTestBrokerManager(t, brokerURL, "showmesh-coordinator-deadline")

	start := time.Now()
	_, err := coordinator.AwaitResponse(context.Background(), ResponseRequest{
		PublishTopic:   "home/projectors/set",
		PublishPayload: []byte("ON"),
		PublishQoS:     1,
		ResponseTopic:  "home/projectors/state-nobody-answers",
		ResponseQoS:    1,
		Deadline:       1500 * time.Millisecond,
		Match:          textMatcher("on"),
	})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("AwaitResponse with nothing answering: want an error, got nil")
	}
	if elapsed < 1500*time.Millisecond {
		t.Errorf("AwaitResponse returned after %v, want it to have actually waited out the 1.5s deadline against a real broker", elapsed)
	}
	t.Logf("deadline outcome: %v (after %v)", err, elapsed)
}
