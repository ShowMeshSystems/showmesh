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

	bm, err := NewBrokerManager(ctx, config.Config{MQTTBroker: brokerURL, MQTTClientID: clientID}, integrationTestLogger(), nil, nil, nil)
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
// home-automation-style retained value on the response topic must never
// resolve AwaitResponse. A separate BrokerManager stands in for the
// external system (Home Assistant/Node-RED), an independent MQTT client on
// the same real broker rather than sharing the coordinator's own
// connection.
//
// Review finding 2 on commit 9dcab74: the original version of this test set
// the retained value BEFORE calling AwaitResponse at all, so the
// coordinator's own initial SUBSCRIBE received the replay immediately —
// which happens strictly before AwaitResponse stamps publishedAt (that
// happens after Subscribe returns, right before its own Publish call — see
// AwaitResponse's doc comment). That made the delivery predate the publish,
// so AwaitResponse's own step-5 publish fence discarded it independently of
// line 236's RETAIN check: deleting line 236 left this test passing,
// because it measured the fence, not the rule it is named for.
//
// The fix reproduces the actual production scenario the finding named:
// registerResponseWaiter subscribes unconditionally for EVERY waiter, even
// when the topic already has one (see that method's doc comment), and
// RetainHandling is left at its default (send retained messages at every
// subscribe — see registerResponseWaiter's SubscribeOptions for why
// finding 2's optional RetainHandling=2 hardening is deliberately not
// applied, since it would make this exact scenario unreachable and this
// test unable to prove anything). So a SECOND, independent
// registerResponseWaiter call on the SAME topic — standing in for a second
// concurrent macro run's step, "run B" in the finding's own scenario —
// makes the real broker replay the pre-existing retained value a SECOND
// time, well after the first AwaitResponse call's own publishedAt, and
// dispatchToWaiters fans that replay to EVERY live waiter on the topic,
// including the original AwaitResponse call's. That delivery arrives after
// the publish, so it is NOT caught by the publish fence — only line 236 can
// stop it from confirming, which is what this test now actually pins.
//
// Run B used to fire from a background goroutine after a blind 100ms sleep
// and report its own registration failure with t.Errorf. Verified
// 2026-08-14: against a real broker this occasionally failed with "no
// connection available" — a transient state, not a defect in the code under
// test — which made the WHOLE test a coin flip that failed regardless of
// whether dispatchToWaiters' RETAIN check was correct. Worse, a silent run B
// failure let the rest of the test still pass off the ordinary live
// delivery, proving nothing about the RETAIN check while reporting green.
//
// Run B now registers synchronously in the main test goroutine, and a
// failure there is a synchronous t.Fatalf: this test cannot trigger the
// replay it exists to prove discards correctly, so it must fail loudly as
// inconclusive rather than pass quietly for the wrong reason. The wait for
// "run A is actually ready for run B to fire" races two conditions, not one
// — and the second one was found chasing the first fix down: a bare
// respTopics poll (waiting for run A's own waiter to appear) intermittently
// timed out with an empty routing table even though run A HAD registered,
// logged and all. The cause was a second race entirely, already present
// before this fix and orthogonal to run B: run A's own initial SUBSCRIBE
// (against the retained value already sitting on the topic — see below) can
// itself receive that retained replay, and whether it arrives before or
// after AwaitResponse stamps publishedAt is a genuine, microsecond-scale
// race between two independent goroutines. In a broken implementation (the
// RETAIN check missing) that race can resolve run A's own AwaitResponse
// call in well under a millisecond, off its own retained replay, before run
// B — or this test's own poll loop — ever gets a look in; AwaitResponse's
// deferred releaseResponseWaiter then removes run A's waiter from the
// routing table on its way out, so a poll that only checks "is a waiter
// registered" finds nothing, forever, and reports a misleading "connection
// unavailable". It is a second, earlier way the SAME missing check can
// reveal itself, not a new defect and not flakiness in this test's
// synchronization — so the wait below treats "run A already resolved" as a
// legitimate, informative outcome to assert on directly, rather than a
// timeout to misreport.
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

	// Run A's own AwaitResponse call runs in a goroutine so the main
	// goroutine is free to register (and, if necessary, fail on) run B
	// below, deterministically ordered against run A rather than raced
	// against it.
	type awaitResult struct {
		msg Message
		err error
	}
	resultCh := make(chan awaitResult, 1)
	start := time.Now()
	go func() {
		msg, err := coordinator.AwaitResponse(context.Background(), ResponseRequest{
			PublishTopic:   "home/projectors/set",
			PublishPayload: []byte("ON"),
			PublishQoS:     1,
			ResponseTopic:  responseTopic,
			ResponseQoS:    1,
			Deadline:       10 * time.Second,
			Match:          textMatcher("on"),
		})
		resultCh <- awaitResult{msg, err}
	}()

	// Deterministic wait for run A's own registration, rather than a blind
	// sleep: registerResponseWaiter inserts into the routing table BEFORE
	// issuing its network SUBSCRIBE (see that method's doc comment), and
	// holds topic's topicLock for the whole call, so seeing run A's waiter
	// here proves the connection was live enough for run A to reach that
	// point, and that run B's own registerResponseWaiter call below — which
	// needs the identical topicLock — cannot begin its own SUBSCRIBE until
	// run A's has fully completed and released it.
	//
	// The second condition raced here, in the SAME loop rather than checked
	// only after a timeout, is resultCh itself: see this test's own doc
	// comment for why run A can legitimately finish before ever appearing
	// "still registered" to this poll, and why that is not a synchronization
	// failure in this test.
	var early awaitResult
	sawEarly := false
	deadline := time.Now().Add(5 * time.Second)
	for {
		coordinator.respMu.Lock()
		n := 0
		if state, ok := coordinator.respTopics[responseTopic]; ok {
			n = len(state.waiters)
		}
		coordinator.respMu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case early = <-resultCh:
			sawEarly = true
		default:
		}
		if sawEarly {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run A's waiter never appeared in the routing table within 5s, and AwaitResponse had not returned either — the coordinator's connection may not have been available")
		}
		time.Sleep(5 * time.Millisecond)
	}

	if sawEarly {
		// AwaitResponse resolved before run B ever got to register — see
		// this test's own doc comment for why that is possible and what it
		// means. Assert on it directly: this is a legitimate, if earlier,
		// way for the exact defect this test exists to catch to reveal
		// itself, and forcing run B to run against a call that has already
		// ended would only produce a confusing second error.
		if early.err != nil {
			t.Fatalf("AwaitResponse: unexpected error: %v", early.err)
		}
		if early.msg.Retained {
			t.Fatalf("AwaitResponse returned a retained delivery, want only the live one to confirm — it resolved off its own initial SUBSCRIBE's retained replay before run B ever ran, which a correct RETAIN discard makes impossible regardless of timing")
		}
		t.Fatalf("AwaitResponse resolved (msg=%+v) before run B could register — this run cannot exercise the run-B-triggered replay path this test is named for; with the RETAIN check in place this should not be reachable within 5s, so re-run to confirm before treating this as a real failure", early.msg)
	}

	// "Run B": a second, independent waiter registration on the identical
	// response topic. Its own SUBSCRIBE is what triggers the real broker to
	// replay the still-pre-existing retained "on" a second time, post-
	// publish, fanned out to run A's waiter by dispatchToWaiters. It is
	// released shortly after registering — it exists only to trigger the
	// replay, not to confirm anything itself. A bounded AwaitConnection
	// wait immediately precedes it: the routing-table wait above already
	// makes this vanishingly unlikely to hit a down connection, but this is
	// the actual readiness signal for the specific failure this fix
	// targets ("no connection available"), and it costs nothing when the
	// connection is already up.
	connCtx, connCancel := context.WithTimeout(context.Background(), 5*time.Second)
	connErr := coordinator.cm.AwaitConnection(connCtx)
	connCancel()
	if connErr != nil {
		t.Fatalf("coordinator connection not available before run B's registration: %v — cannot trigger the retained replay this test depends on", connErr)
	}

	runBCtx, runBCancel := context.WithTimeout(context.Background(), 5*time.Second)
	wB, err := coordinator.registerResponseWaiter(runBCtx, responseTopic, 1, func(Message) bool { return false })
	runBCancel()
	if err != nil {
		t.Fatalf("run B's registerResponseWaiter: %v — the retained replay this test exists to trigger could not fire, so this run proves nothing about the RETAIN check and must fail rather than pass quietly", err)
	}
	// Give the broker a moment to actually deliver the retained replay this
	// registration triggers before releasing.
	time.Sleep(200 * time.Millisecond)
	coordinator.releaseResponseWaiter(wB)

	r := <-resultCh
	msg, err := r.msg, r.err
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("AwaitResponse: unexpected error: %v", err)
	}
	if msg.Retained {
		t.Fatalf("AwaitResponse returned a retained delivery, want only the live one to confirm — run B's own registerResponseWaiter should have triggered a post-publish retained replay that line 236 must discard")
	}
	if elapsed < settle/2 {
		t.Errorf("AwaitResponse against a real broker resolved in %v, want it to have waited for the live delivery (~%v) rather than confirming off the run-B-triggered retained replay — this is the defect this test exists to catch", elapsed, settle)
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
		}, nil)
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

// TestIntegrationReconnectResubscribesResponseWaiterTopic is review finding
// 1's missing proof. broker.go's NewBrokerManager wires OnConnectionUp to
// call bm.subscriptionsToResubscribe() — which folds every LIVE response
// waiter's topic in alongside the fixed subscription set (see that
// method's own unit tests in broker_test.go) — but nothing before this test
// proved the call site itself: reverting that one line to the pre-fix
// subscribeAll(ctx, cm, subscriptionsToOptions(bm.fixedSubs), logger) left
// both the unit suite AND the rest of this file's integration suite green.
//
// This coordinator is constructed with an EMPTY fixed subscription set
// (newConnectedTestBrokerManager passes nil for subs), so the distinction
// is stark: with the fix, a broker restart mid-wait still resends the
// response-topic subscription on reconnect (via subscriptionsToResubscribe);
// with the pre-fix call, resubscribing an empty fixed set is a no-op
// (subscribeAll returns immediately — see that function's own guard), so
// the coordinator would come back from the restart subscribed to nothing at
// all, and the live answer published afterward would never be routed
// anywhere. AwaitResponse would then time out, indistinguishable from a
// projector that never answered — exactly the failure scenario the finding
// names.
func TestIntegrationReconnectResubscribesResponseWaiterTopic(t *testing.T) {
	requireDocker(t)

	port := freePort(t)
	containerName := fmt.Sprintf("showmesh-broker-pkg-test-reconnect-%d", time.Now().UnixNano())
	brokerURL := "tcp://127.0.0.1:" + strconv.Itoa(port)

	confFile, err := os.CreateTemp("", "showmesh-broker-test-mosquitto-reconnect-*.conf")
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

	// runMosquitto and waitListening are startTestMosquitto's own
	// docker-run/wait-for-listening logic, factored out as closures over a
	// FIXED container name and port rather than a fresh pair each call, so
	// this test can start the same broker identity twice: once for the
	// initial connection, and again — a genuinely new container, since
	// --rm auto-removes the old one the instant it's killed — standing in
	// for "the broker process came back" on the same address the coordinator
	// is already trying to reconnect to.
	runMosquitto := func() {
		t.Helper()
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
	}
	waitListening := func() {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:"+strconv.Itoa(port), 500*time.Millisecond)
			if err == nil {
				conn.Close()
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		if out, err := exec.Command("docker", "logs", containerName).CombinedOutput(); err == nil {
			t.Logf("mosquitto container logs:\n%s", out)
		}
		t.Fatalf("mosquitto did not start listening on port %d within 15s", port)
	}

	runMosquitto()
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", containerName).Run() })
	waitListening()

	coordinator := newConnectedTestBrokerManager(t, brokerURL, "showmesh-coordinator-reconnect")
	external := newConnectedTestBrokerManager(t, brokerURL, "external-reconnect-responder")

	const responseTopic = "home/projectors/state-reconnect"

	type awaitResult struct {
		msg Message
		err error
	}
	resultCh := make(chan awaitResult, 1)
	go func() {
		msg, err := coordinator.AwaitResponse(context.Background(), ResponseRequest{
			PublishTopic:   "home/projectors/set-reconnect",
			PublishPayload: []byte("ON"),
			PublishQoS:     1,
			ResponseTopic:  responseTopic,
			ResponseQoS:    1,
			Deadline:       30 * time.Second,
			Match:          textMatcher("on"),
		})
		resultCh <- awaitResult{msg, err}
	}()

	// Give AwaitResponse's own SUBSCRIBE (against the ORIGINAL container) a
	// moment to actually land before killing the broker out from under it —
	// this test is about surviving a restart mid-wait, not one that races
	// the initial subscribe.
	time.Sleep(500 * time.Millisecond)

	t.Log("killing the broker mid-wait and starting a fresh one on the same port")
	if out, err := exec.Command("docker", "rm", "-f", containerName).CombinedOutput(); err != nil {
		t.Fatalf("docker rm -f %s: %v\n%s", containerName, err, out)
	}
	runMosquitto()
	waitListening()

	reconnectCtx, reconnectCancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer reconnectCancel()
	if err := coordinator.cm.AwaitConnection(reconnectCtx); err != nil {
		t.Fatalf("coordinator did not reconnect within 20s: %v", err)
	}
	if err := external.cm.AwaitConnection(reconnectCtx); err != nil {
		t.Fatalf("external did not reconnect within 20s: %v", err)
	}

	// OnConnectionUp's own resubscribe fires from autopaho's reconnect
	// callback; AwaitConnection returning proves the CONNECT/CONNACK
	// completed, not that the resubscribe SUBSCRIBE issued from that
	// callback has also landed at the broker yet. This margin is for that
	// gap, not for the reconnect itself.
	time.Sleep(1 * time.Second)

	if err := external.Publish(context.Background(), responseTopic, 1, false, []byte("on")); err != nil {
		t.Fatalf("publishing live response after reconnect: %v", err)
	}

	select {
	case r := <-resultCh:
		if r.err != nil {
			t.Fatalf("AwaitResponse after broker restart: %v — the response-topic subscription this package's whole mechanism depends on did not survive the reconnect", r.err)
		}
		if string(r.msg.Payload) != "on" {
			t.Errorf("AwaitResponse returned %+v, want the post-reconnect live \"on\"", r.msg)
		}
	case <-time.After(25 * time.Second):
		t.Fatalf("AwaitResponse never returned after the broker restart and the post-reconnect live publish")
	}
}
