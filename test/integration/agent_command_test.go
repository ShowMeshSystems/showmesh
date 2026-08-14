//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file proves BUILD-PLAN Track B deliverable B1 — receive an MQTT
// command, execute it only if it is on the agent's allowlist, and report
// the outcome as evidence (never a bare "I received it") — against a real
// showmesh-agent subprocess and a real Mosquitto broker. See
// internal/agent/command.go's own doc comment for the decision sequence
// under test.
//
// There is no coordinator-side command dispatcher yet (out of scope for
// this seam), so this suite plays the dispatcher's role itself: a raw MQTT
// client, credentialed as "coordinator" — the one broker role
// deploy/mosquitto/acl.conf grants write on every node's cmd topic and
// read on every node's result and observed topics — publishes directly to
// the agent's own cmd topic and observes what comes back on the result and
// retained observed/agent/echo topics, exactly the two topics a real
// coordinator dispatcher will one day use.

// resultWatcher records every showmesh.node.result/v1 and
// showmesh.node.agent.echo/v1 message a subscribed raw MQTT client
// receives, so tests can poll for a specific command's result (or the
// current echo value) without racing a fixed sleep. Malformed or
// unrelated-schema messages are silently ignored — this watcher only cares
// about the two schemas this seam produces.
type resultWatcher struct {
	mu      sync.Mutex
	results []mqttproto.ResultPayload
	echoes  []mqttproto.AgentEchoPayload
}

func (w *resultWatcher) onPublish(pr paho.PublishReceived) (bool, error) {
	if pr.Packet == nil {
		return true, nil
	}
	env, err := mqttproto.DecodeEnvelope(pr.Packet.Payload)
	if err != nil {
		return true, nil
	}
	switch env.Schema {
	case mqttproto.SchemaNodeResultV1:
		if result, err := mqttproto.DecodeResultPayload(env); err == nil {
			w.mu.Lock()
			w.results = append(w.results, result)
			w.mu.Unlock()
		}
	case mqttproto.SchemaNodeAgentEchoV1:
		var echo mqttproto.AgentEchoPayload
		if err := json.Unmarshal(env.Payload, &echo); err == nil {
			w.mu.Lock()
			w.echoes = append(w.echoes, echo)
			w.mu.Unlock()
		}
	}
	return true, nil
}

// resultsFor returns every recorded result for commandID, in arrival
// order — plural, because the redelivery test needs to see both the
// original result and the cached replay.
func (w *resultWatcher) resultsFor(commandID string) []mqttproto.ResultPayload {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []mqttproto.ResultPayload
	for _, r := range w.results {
		if r.CommandID == commandID {
			out = append(out, r)
		}
	}
	return out
}

func (w *resultWatcher) resultCountFor(commandID string) int {
	return len(w.resultsFor(commandID))
}

// lastEcho returns the most recently recorded echo observation, and false
// if none has arrived yet. Since observed/agent/echo is retained per
// [mqttproto.ObservedDeliveryPolicy], the last one received is always this
// suite's best evidence of the topic's current retained value.
func (w *resultWatcher) lastEcho() (mqttproto.AgentEchoPayload, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.echoes) == 0 {
		return mqttproto.AgentEchoPayload{}, false
	}
	return w.echoes[len(w.echoes)-1], true
}

// startCmdClient connects a raw MQTT client as "coordinator" (see this
// file's doc comment on why that role) and subscribes it to nodeID's
// result topics and retained echo observation topic, wiring a
// [resultWatcher] to record everything that arrives. Reuses
// broker_auth_test.go's rawConnect, matching every other raw-client test
// in this package rather than inventing a second connection helper.
//
// This uses the package-level testMQTTCoordinatorUsername/Password —
// the SAME already-seeded "coordinator" credential every
// showmesh-coordinator subprocess in this suite connects with (see
// startCoordinatorWithConfig's identical use of these two vars) —
// rather than harness_test.go's provisionBrokerCredential(t,
// "coordinator"). That distinction is load-bearing, not stylistic:
// provisionBrokerCredential does not merely fetch a credential, it
// ROTATES the named user's password (mosquitto_passwd -b, then a broker
// SIGHUP reload) and is meant for provisioning a FRESH, per-node AGENT
// credential nothing else will reuse (every other caller in this
// package uses it exactly that way). "coordinator" is instead one of
// the harness's own FIXED, SHARED roles: rotating it here would
// silently invalidate every OTHER test's already-exported
// testMQTTCoordinatorUsername/Password for the rest of this test binary
// process, since TestMain reads them from the environment exactly once
// and startCoordinatorWithConfig never re-reads the broker's actual
// state. An earlier version of this file called
// provisionBrokerCredential(t, "coordinator") and broke roughly twenty
// unrelated tests later in the same `go test` run this way — every
// showmesh-coordinator subprocess started after this file's own tests
// got CONNACK 0x87 Not Authorized against a password that had quietly
// changed underneath it, reproducibly, on every run. Fixed by using the
// credential the harness already guarantees is live for the whole
// process instead of rotating it.
func startCmdClient(t *testing.T, nodeID string) (*paho.Client, *resultWatcher) {
	t.Helper()

	if testMQTTCoordinatorUsername == "" {
		t.Fatalf("no MQTT broker credential available (%s is unset) — run via `make test-integration`, which provisions one, rather than `go test` directly against an ad hoc broker",
			envTestMQTTCoordinatorUsername)
	}
	cli := rawConnect(t, testMQTTCoordinatorUsername, testMQTTCoordinatorPassword)

	w := &resultWatcher{}
	cli.AddOnPublishReceived(w.onPublish)

	// showmesh/nodes/<node-id>/result/<cmd-id> has no single-topic builder
	// for "every result on this node" (mqttproto.ResultTopic requires one
	// cmd-id); this is this suite's own '+' filter over that shape, the
	// same way topic.go's own SubscribeHello/SubscribeLWT/SubscribeObserved
	// are this package's derivation from the fixed ADR-008 shape rather
	// than something ADR-008 names directly.
	resultFilter := fmt.Sprintf("showmesh/nodes/%s/result/+", nodeID)
	echoTopic, err := mqttproto.ObservedTopic(nodeID, "agent/echo")
	if err != nil {
		t.Fatalf("ObservedTopic() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sa, err := cli.Subscribe(ctx, &paho.Subscribe{
		Subscriptions: []paho.SubscribeOptions{
			{Topic: resultFilter, QoS: 1},
			{Topic: echoTopic, QoS: 1},
		},
	})
	if err != nil {
		t.Fatalf("SUBSCRIBE %s / %s: %v", resultFilter, echoTopic, err)
	}
	for i, rc := range sa.Reasons {
		if rc >= 0x80 {
			t.Fatalf("SUBSCRIBE rejected: subscription index %d, reason code %d", i, rc)
		}
	}

	return cli, w
}

// dispatchCmd publishes cmd to nodeID's cmd topic on cli, at
// [mqttproto.CmdDeliveryPolicy]'s QoS and retain setting. Fails t only on a
// transport-level PUBLISH failure or an outright PUBACK rejection — the
// "coordinator" role's write grant on every node's cmd topic is exactly
// what every test in this file depends on to mean anything, so a rejection
// here is a harness problem, never an expected test outcome.
func dispatchCmd(t *testing.T, cli *paho.Client, nodeID string, cmd mqttproto.CmdPayload) {
	t.Helper()

	env, err := mqttproto.NewCmdEnvelope(time.Now, nodeID, cmd)
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	topic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		t.Fatalf("CmdTopic() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := cli.Publish(ctx, &paho.Publish{
		QoS:     mqttproto.CmdDeliveryPolicy.QoS,
		Retain:  mqttproto.CmdDeliveryPolicy.Retain,
		Topic:   topic,
		Payload: data,
	})
	if err != nil {
		t.Fatalf("PUBLISH %s: %v", topic, err)
	}
	if resp != nil && resp.ReasonCode >= 0x80 {
		t.Fatalf("PUBLISH %s: broker rejected with reason code %d (the \"coordinator\" role should have write access)", topic, resp.ReasonCode)
	}
}

// echoCmd builds a well-formed "agent.echo" [mqttproto.CmdPayload]
// targeting nodeID.
func echoCmd(nodeID, commandID, idempotencyKey, value string) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:          commandID,
		IdempotencyKey:     idempotencyKey,
		Action:             "agent.echo",
		Target:             mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Params:             map[string]any{"value": value},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
}

func dispatchEcho(t *testing.T, cli *paho.Client, nodeID, commandID, idempotencyKey, value string) {
	t.Helper()
	dispatchCmd(t, cli, nodeID, echoCmd(nodeID, commandID, idempotencyKey, value))
}

// waitForResult polls w for a result matching commandID, failing t if none
// arrives within timeout.
func waitForResult(t *testing.T, w *resultWatcher, commandID string, timeout time.Duration) mqttproto.ResultPayload {
	t.Helper()
	var result mqttproto.ResultPayload
	waitFor(t, timeout, 50*time.Millisecond, func() bool {
		results := w.resultsFor(commandID)
		if len(results) == 0 {
			return false
		}
		result = results[0]
		return true
	}, "a result for command "+commandID)
	return result
}

// waitForEchoValue polls w for the retained echo observation to read
// value, failing t if it never does within timeout.
func waitForEchoValue(t *testing.T, w *resultWatcher, value string, timeout time.Duration) {
	t.Helper()
	waitFor(t, timeout, 50*time.Millisecond, func() bool {
		echo, ok := w.lastEcho()
		return ok && echo.Value == value
	}, "observed/agent/echo to read value "+value)
}

// awaitAgentReceivingCommands blocks until nodeID's real agent subprocess
// has visibly processed at least one command, by publishing throwaway
// "agent.echo" probes (fresh command and idempotency IDs each attempt)
// until one gets a result back, or fails t after 15s.
//
// This exists because [mqttproto.CmdDeliveryPolicy] is never retained
// (ADR-008: a command is a point-in-time message, not durable state), so
// there is an unavoidable race, invisible at the publisher (the broker
// PUBACKs a cmd PUBLISH successfully regardless of whether any subscriber
// is listening yet), between "the agent subprocess has started" and "the
// agent's own SUBSCRIBE to its cmd topic (mqtt.go's
// registerCommandHandling, run fresh on every connect) has actually
// completed." A command published into that window is silently dropped,
// not delayed. Every test below calls this before sending the command
// sequence it actually wants to assert on, including
// TestAgentCommandRedeliveryExecutesOnce — which depends on its FIRST
// delivery genuinely arriving, or the test would trivially "pass" for the
// wrong reason (the first delivery lost to the race, only the second ever
// arriving, indistinguishable from correct once-only execution). Retrying
// with a fresh ID each attempt is safe specifically because this is
// "did my logically distinct probe even arrive," never the
// same-idempotency-key redelivery case the tests below exist to prove.
func awaitAgentReceivingCommands(t *testing.T, cli *paho.Client, w *resultWatcher, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		probeID := "warmup-" + uniqueSuffix()
		dispatchEcho(t, cli, nodeID, probeID, probeID, "warmup")

		found := false
		probeDeadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(probeDeadline) {
			if len(w.resultsFor(probeID)) > 0 {
				found = true
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if found {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("agent %s never became reachable on its cmd topic within 15s of repeated warm-up probes", nodeID)
		}
	}
}

// TestAgentCommandAgentEchoConfirmedAndObserved proves the well-formed
// path: a real agent subprocess executes the one allowlisted "agent.echo"
// operation, publishes a confirmed result carrying evidence collected
// AFTER the write (per ADR-003 — see internal/agent/command.go's
// OperationResult doc comment), and republishes the retained
// observed/agent/echo signal with the new value, exactly like every other
// observed signal in this system.
func TestAgentCommandAgentEchoConfirmedAndObserved(t *testing.T) {
	requireBroker(t)

	nodeID := "agent-" + uniqueSuffix()
	startAgent(t, agentConfig{nodeID: nodeID})

	cli, w := startCmdClient(t, nodeID)
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	commandID := "cmd-" + uniqueSuffix()
	idempotencyKey := "idem-" + uniqueSuffix()
	wantValue := "hello-" + uniqueSuffix()

	dispatchEcho(t, cli, nodeID, commandID, idempotencyKey, wantValue)

	result := waitForResult(t, w, commandID, 10*time.Second)
	if result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("Outcome = %q, want %q; reason = %q", result.Outcome, mqttproto.OutcomeConfirmed, result.Reason)
	}
	if result.Action != "agent.echo" {
		t.Errorf("Action = %q, want %q", result.Action, "agent.echo")
	}
	if result.Evidence == nil {
		t.Fatalf("Evidence is nil, want a post-write observation")
	}
	if result.Evidence.Value != wantValue {
		t.Errorf("Evidence.Value = %v, want %q", result.Evidence.Value, wantValue)
	}
	if result.Evidence.ObservedAt == nil {
		t.Errorf("Evidence.ObservedAt is nil, want set")
	}
	if result.ExecutedAt == nil {
		t.Errorf("ExecutedAt is nil, want set")
	}

	waitForEchoValue(t, w, wantValue, 10*time.Second)
}

// TestAgentCommandNonAllowlistedActionRefused proves ARCHITECTURE section
// 10.4's "agents accept only allowlisted operations" against a real agent:
// an action this agent does not implement is refused with an explanatory
// reason, and — the part no unit test running in-process can independently
// corroborate — the operation visibly never ran: a baseline echo value set
// beforehand is still what the retained observed topic reads afterward.
func TestAgentCommandNonAllowlistedActionRefused(t *testing.T) {
	requireBroker(t)

	nodeID := "agent-" + uniqueSuffix()
	startAgent(t, agentConfig{nodeID: nodeID})

	cli, w := startCmdClient(t, nodeID)
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	baselineID := "cmd-" + uniqueSuffix()
	baselineKey := "idem-" + uniqueSuffix()
	baselineValue := "baseline-" + uniqueSuffix()
	dispatchEcho(t, cli, nodeID, baselineID, baselineKey, baselineValue)
	baseline := waitForResult(t, w, baselineID, 10*time.Second)
	if baseline.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("baseline echo Outcome = %q, want %q", baseline.Outcome, mqttproto.OutcomeConfirmed)
	}
	waitForEchoValue(t, w, baselineValue, 10*time.Second)

	commandID := "cmd-" + uniqueSuffix()
	cmd := mqttproto.CmdPayload{
		CommandID:          commandID,
		IdempotencyKey:     "idem-" + uniqueSuffix(),
		Action:             "fpp.stop_playlist", // not on this agent's allowlist
		Target:             mqttproto.CmdTarget{Kind: "node", ID: nodeID},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "test-principal", PrincipalName: "integration-test"},
		ConfirmationMethod: "evidence",
	}
	dispatchCmd(t, cli, nodeID, cmd)

	result := waitForResult(t, w, commandID, 10*time.Second)
	if result.Outcome != mqttproto.OutcomeRefused {
		t.Fatalf("Outcome = %q, want %q; reason = %q", result.Outcome, mqttproto.OutcomeRefused, result.Reason)
	}
	if result.Reason == "" {
		t.Errorf("Reason is empty, want an explanation naming the disallowed action")
	}

	// Bounded window for a wrongly-executed operation to (incorrectly)
	// publish a changed echo observation, then confirm it never did.
	time.Sleep(1 * time.Second)
	echo, ok := w.lastEcho()
	if !ok || echo.Value != baselineValue {
		t.Fatalf("observed/agent/echo = %+v, want Value %q (a refused command must never execute)", echo, baselineValue)
	}
}

// TestAgentCommandRedeliveryExecutesOnce is this seam's most important
// integration test: it proves ADR-008's "QoS 1 + idempotency keys so a
// redelivered command executes exactly once" against a real agent process,
// a real broker, and two genuinely separate PUBLISH calls — not an
// in-process fake standing in for redelivery.
//
// The same CommandID and IdempotencyKey are published twice, with
// DIFFERENT requested echo values, specifically so a wrongly-reexecuting
// agent would be caught two independent ways: the retained echo topic
// would move to the second value (it must not), and the second delivery's
// published result would carry freshly recomputed evidence rather than the
// first delivery's evidence replayed verbatim (it must not do that
// either).
func TestAgentCommandRedeliveryExecutesOnce(t *testing.T) {
	requireBroker(t)

	nodeID := "agent-" + uniqueSuffix()
	startAgent(t, agentConfig{nodeID: nodeID})

	cli, w := startCmdClient(t, nodeID)
	awaitAgentReceivingCommands(t, cli, w, nodeID)

	commandID := "cmd-" + uniqueSuffix()
	idempotencyKey := "idem-" + uniqueSuffix()
	firstValue := "first-" + uniqueSuffix()
	secondValue := "second-must-not-apply-" + uniqueSuffix()

	dispatchEcho(t, cli, nodeID, commandID, idempotencyKey, firstValue)
	firstResult := waitForResult(t, w, commandID, 10*time.Second)
	if firstResult.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("first delivery Outcome = %q, want %q; reason = %q", firstResult.Outcome, mqttproto.OutcomeConfirmed, firstResult.Reason)
	}
	waitForEchoValue(t, w, firstValue, 10*time.Second)

	// Redelivery: identical CommandID and IdempotencyKey, a different
	// requested value.
	dispatchEcho(t, cli, nodeID, commandID, idempotencyKey, secondValue)

	waitFor(t, 10*time.Second, 50*time.Millisecond, func() bool {
		return w.resultCountFor(commandID) >= 2
	}, "a second result publish (the cached replay) for command "+commandID)

	results := w.resultsFor(commandID)
	if len(results) != 2 {
		t.Fatalf("resultsFor(%q) = %d entries, want exactly 2 (no more than one redelivery was sent)", commandID, len(results))
	}
	first, second := results[0], results[1]
	if first.Evidence == nil || second.Evidence == nil {
		t.Fatalf("Evidence missing: first = %+v, second = %+v", first, second)
	}
	if second.Evidence.Value != firstValue {
		t.Fatalf("redelivered result Evidence.Value = %v, want %q (the original, never re-executed against %q)",
			second.Evidence.Value, firstValue, secondValue)
	}
	if !second.Evidence.ObservedAt.Equal(*first.Evidence.ObservedAt) || !second.Evidence.CollectedAt.Equal(first.Evidence.CollectedAt) {
		t.Fatalf("redelivered result carries freshly recomputed evidence instead of the original replayed verbatim:\nfirst  = %+v\nsecond = %+v", first, second)
	}

	// Bounded window for a wrongly-reexecuting agent to (incorrectly)
	// publish a second echo observation, then confirm the retained topic
	// never moved off the first, correct value.
	time.Sleep(1 * time.Second)
	echo, ok := w.lastEcho()
	if !ok || echo.Value != firstValue {
		t.Fatalf("observed/agent/echo = %+v, want Value %q (redelivery must not re-execute)", echo, firstValue)
	}
}
