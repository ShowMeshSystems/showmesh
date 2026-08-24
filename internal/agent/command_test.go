package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file tests CommandHandler.HandleMessage against fakePublisher (see
// fake_publisher_test.go) — no broker involved, matching this package's
// established style for heartbeat.go and advertise.go.
//
// TestHandleMessageConcurrentRedeliveryExecutesOnce is, per the build
// spec, the single most important test in this seam: it forces two
// GENUINELY CONCURRENT HandleMessage calls sharing one idempotency key —
// the shape mqtt.go's own "HandleMessage runs in its own goroutine per
// inbound PUBLISH" design actually produces for a close-together
// redelivery — and proves the allowlisted operation still runs exactly
// once. TestHandleMessageRedeliveryDoesNotReExecute, immediately below it,
// is the simpler SEQUENTIAL case (two HandleMessage calls, one after the
// other); it is kept because it documents and pins the non-concurrent
// contract, but it cannot by itself detect a check-then-act race between
// "is this key resolved" and "claim it and execute" — only the concurrent
// test can, and does.

const testNodeID = "media-03"

// countingOp is a test-only OperationFunc that counts its own invocations
// and echoes back params["value"], so a test can assert directly on how
// many times an operation actually ran — the "execution counter"
// alternative the build spec names alongside asserting on
// agentEchoState.appliedAt directly.
type countingOp struct {
	mu    sync.Mutex
	calls int
}

func (c *countingOp) run(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()

	v, _ := params["value"].(string)
	t := now()
	return OperationResult{
		Confirmed:  true,
		Signal:     "node.agent.echo_value",
		Value:      v,
		ExecutedAt: t,
		ObservedAt: t,
	}, nil
}

func (c *countingOp) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

// blockingOp is a test-only OperationFunc that signals entered once it
// starts running, then blocks on unblock until the test explicitly
// releases it. It exists to construct — deterministically, not by racing
// a kernel or hoping timing lines up (this project's own standing rule) —
// the exact interleaving TestHandleMessageConcurrentRedeliveryExecutesOnce
// needs: with the operation held open indefinitely, a second, wrongly
// concurrent delivery of the same idempotency key has effectively
// unlimited time to also reach and pass a buggy check-then-act sequence,
// so the regression this test guards is reproduced on every run if it
// exists, not occasionally.
type blockingOp struct {
	mu      sync.Mutex
	calls   int
	entered chan struct{}
	unblock chan struct{}
}

func newBlockingOp() *blockingOp {
	return &blockingOp{
		entered: make(chan struct{}, 8),
		unblock: make(chan struct{}),
	}
}

func (b *blockingOp) run(_ context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()

	b.entered <- struct{}{}
	<-b.unblock

	v, _ := params["value"].(string)
	t := now()
	return OperationResult{
		Confirmed:  true,
		Signal:     "node.agent.echo_value",
		Value:      v,
		ExecutedAt: t,
		ObservedAt: t,
	}, nil
}

func (b *blockingOp) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// newTestHandler builds a CommandHandler with a directly-injected ops map
// (bypassing newCommandHandler/newOperationRegistry), so tests can swap in
// a countingOp instead of the real agentEchoState — legitimate white-box
// construction, same package, same struct literal newCommandHandler itself
// builds.
func newTestHandler(ops map[string]OperationFunc, clock *fakeClock) *CommandHandler {
	return &CommandHandler{
		nodeID: testNodeID,
		ops:    ops,
		cache:  newIdempotencyCache(agentIdempotencyCacheCapacity),
		now:    clock.now,
		logger: discardLogger(),
	}
}

// buildCmdMessage marshals cmd into a showmesh.node.cmd/v1 envelope for
// testNodeID and returns the topic and payload bytes HandleMessage expects
// to receive from an MQTT PUBLISH.
func buildCmdMessage(t *testing.T, clock *fakeClock, cmd mqttproto.CmdPayload) (topic string, payload []byte) {
	t.Helper()
	env, err := mqttproto.NewCmdEnvelope(clock.now, testNodeID, cmd)
	if err != nil {
		t.Fatalf("NewCmdEnvelope() error = %v", err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	topicStr, err := mqttproto.CmdTopic(testNodeID)
	if err != nil {
		t.Fatalf("CmdTopic() error = %v", err)
	}
	return topicStr, data
}

func baseEchoCmd(commandID, idempotencyKey string) mqttproto.CmdPayload {
	return mqttproto.CmdPayload{
		CommandID:          commandID,
		IdempotencyKey:     idempotencyKey,
		Action:             "agent.echo",
		Target:             mqttproto.CmdTarget{Kind: "node", ID: testNodeID},
		Params:             map[string]any{"value": "hello"},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "principal-1", PrincipalName: "operator"},
		ConfirmationMethod: confirmationMethodEvidence,
	}
}

// decodeResultFromCall decodes a fakePublisher call's payload as a
// showmesh.node.result/v1 envelope and payload, failing t on any error.
func decodeResultFromCall(t *testing.T, call recordedPublish) mqttproto.ResultPayload {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(call.payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope(result call) error = %v", err)
	}
	result, err := mqttproto.DecodeResultPayload(env)
	if err != nil {
		t.Fatalf("DecodeResultPayload() error = %v", err)
	}
	return result
}

func decodeEchoFromCall(t *testing.T, call recordedPublish) mqttproto.AgentEchoPayload {
	t.Helper()
	env, err := mqttproto.DecodeEnvelope(call.payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope(echo call) error = %v", err)
	}
	var echo mqttproto.AgentEchoPayload
	if err := json.Unmarshal(env.Payload, &echo); err != nil {
		t.Fatalf("json.Unmarshal(echo payload) error = %v", err)
	}
	return echo
}

// TestHandleMessageAgentEchoConfirmed proves the allowlisted "agent.echo"
// operation executes and both publishes happen: a confirmed result on the
// command's result topic, and the retained observed/agent/echo signal
// carrying the new value — the seam's "outcome becomes an observation like
// every other signal in this system" requirement.
func TestHandleMessageAgentEchoConfirmed(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	h := newCommandHandler(testNodeID, t.TempDir(), "", nil, nil, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := baseEchoCmd("cmd-1", "idem-1")
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 2 {
		t.Fatalf("len(calls) = %d, want 2 (result + observed echo); calls = %+v", len(calls), calls)
	}

	wantResultTopic, err := mqttproto.ResultTopic(testNodeID, "cmd-1")
	if err != nil {
		t.Fatalf("ResultTopic() error = %v", err)
	}
	wantEchoTopic, err := mqttproto.ObservedTopic(testNodeID, "agent/echo")
	if err != nil {
		t.Fatalf("ObservedTopic() error = %v", err)
	}

	if calls[0].topic != wantResultTopic {
		t.Fatalf("calls[0].topic = %q, want %q (result must publish before the echo observation)", calls[0].topic, wantResultTopic)
	}
	if calls[0].retain {
		t.Fatalf("calls[0].retain = true, want false (ResultDeliveryPolicy is never retained)")
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("Outcome = %q, want %q; reason = %q", result.Outcome, mqttproto.OutcomeConfirmed, result.Reason)
	}
	if result.Evidence == nil || result.Evidence.Value != "hello" {
		t.Fatalf("Evidence = %+v, want Value \"hello\"", result.Evidence)
	}
	if result.Evidence.ObservedAt == nil {
		t.Fatalf("Evidence.ObservedAt is nil, want set")
	}

	if calls[1].topic != wantEchoTopic {
		t.Fatalf("calls[1].topic = %q, want %q", calls[1].topic, wantEchoTopic)
	}
	if !calls[1].retain {
		t.Fatalf("calls[1].retain = false, want true (ObservedDeliveryPolicy is retained)")
	}
	echo := decodeEchoFromCall(t, calls[1])
	if echo.Value != "hello" {
		t.Fatalf("echo.Value = %q, want %q", echo.Value, "hello")
	}
}

// TestHandleMessageRedeliveryDoesNotReExecute is this seam's most
// important test. It proves a redelivered message carrying the same
// IdempotencyKey does not re-run the allowlisted operation: the injected
// countingOp's call count stays at 1, the SAME evidence is republished
// (not recomputed from a second, different clock read or a second,
// different requested value), and the retained echo observation is
// published exactly once — a second execution would either bump the
// counter, change the republished evidence, or emit a second echo
// publish, and this test checks all three, not just "Publish was called
// twice."
func TestHandleMessageRedeliveryDoesNotReExecute(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	op := &countingOp{}
	h := newTestHandler(map[string]OperationFunc{"agent.echo": op.run}, clock)
	pub := newFakePublisher()

	cmd := baseEchoCmd("cmd-1", "idem-shared")
	topic, payload := buildCmdMessage(t, clock, cmd)

	// First delivery: executes for real.
	h.HandleMessage(context.Background(), pub, topic, payload)
	firstCalls := pub.snapshot()
	if len(firstCalls) != 2 {
		t.Fatalf("after first delivery: len(calls) = %d, want 2", len(firstCalls))
	}
	if got := op.callCount(); got != 1 {
		t.Fatalf("after first delivery: op.callCount() = %d, want 1", got)
	}
	firstResult := decodeResultFromCall(t, firstCalls[0])

	// Advance the clock so that, if this were mistakenly re-executed, the
	// evidence would visibly differ (a different ObservedAt/CollectedAt) —
	// making a false pass (silently republishing freshly-recomputed
	// evidence that merely happens to match) far less likely to slip past
	// this assertion.
	clock.advance(time.Hour)

	// Second delivery: EXACT same bytes (a true QoS 1 redelivery), same
	// idempotency key.
	h.HandleMessage(context.Background(), pub, topic, payload)

	if got := op.callCount(); got != 1 {
		t.Fatalf("after redelivery: op.callCount() = %d, want 1 (must not re-execute)", got)
	}

	secondCalls := pub.snapshot()
	if len(secondCalls) != 3 {
		t.Fatalf("after redelivery: len(calls) = %d, want 3 (one more result publish, no second echo publish); calls = %+v", len(secondCalls), secondCalls)
	}
	secondResult := decodeResultFromCall(t, secondCalls[2])

	if secondResult.CommandID != firstResult.CommandID ||
		secondResult.Outcome != firstResult.Outcome ||
		secondResult.Evidence == nil || firstResult.Evidence == nil ||
		secondResult.Evidence.Value != firstResult.Evidence.Value ||
		!secondResult.Evidence.ObservedAt.Equal(*firstResult.Evidence.ObservedAt) ||
		!secondResult.Evidence.CollectedAt.Equal(firstResult.Evidence.CollectedAt) {
		t.Fatalf("redelivery republished different evidence than the original:\nfirst  = %+v\nsecond = %+v", firstResult, secondResult)
	}

	// Exactly one echo observation publish across both deliveries — the
	// cache-hit path must not touch the observed topic a second time.
	echoTopic, err := mqttproto.ObservedTopic(testNodeID, "agent/echo")
	if err != nil {
		t.Fatalf("ObservedTopic() error = %v", err)
	}
	echoPublishes := 0
	for _, c := range secondCalls {
		if c.topic == echoTopic {
			echoPublishes++
		}
	}
	if echoPublishes != 1 {
		t.Fatalf("echo observation published %d times across both deliveries, want exactly 1", echoPublishes)
	}
}

// TestHandleMessageConcurrentRedeliveryExecutesOnce is this seam's most
// important test — see this file's own top-of-file doc comment for why
// TestHandleMessageRedeliveryDoesNotReExecute above (sequential) cannot
// substitute for it. It proves ADR-008's "a redelivered command executes
// exactly once" against a genuinely concurrent pair of HandleMessage
// calls sharing one idempotency key. A naive two-step idempotency check
// (look the key up under one lock, execute, store the result under a
// SECOND, later lock) has a check-then-act race: two concurrent calls can
// both observe a miss before either has stored anything, and both
// execute. This is exactly the bug an adversarial review found in this
// seam's first version, proved by temporarily removing the atomic
// claim-before-execute step and rerunning: the operation executed twice
// for one idempotency key.
//
// blockingOp forces the interleaving deterministically: it blocks INSIDE
// the allowlisted operation until this test releases it. Against the
// current, correct implementation, only the winner of
// idempotencyCache.claimOrAwait's atomic check-and-claim ever reaches
// blockingOp.run at all — the other call is genuinely parked inside
// claimOrAwait's own <-inf.done receive, nowhere near the operation, for
// as long as this test chooses to hold the winner open.
func TestHandleMessageConcurrentRedeliveryExecutesOnce(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	op := newBlockingOp()
	h := newTestHandler(map[string]OperationFunc{"agent.echo": op.run}, clock)

	// Two independent fake publishers, one per concurrent HandleMessage
	// call — matching how mqtt.go's real AddOnPublishReceived callback
	// hands each inbound PUBLISH its own freshly-built *mqttConn (see that
	// file's registerCommandHandling).
	pub1 := newFakePublisher()
	pub2 := newFakePublisher()

	cmd := baseEchoCmd("cmd-shared", "idem-shared")
	topic, payload := buildCmdMessage(t, clock, cmd)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		h.HandleMessage(context.Background(), pub1, topic, payload)
	}()
	go func() {
		defer wg.Done()
		h.HandleMessage(context.Background(), pub2, topic, payload)
	}()

	select {
	case <-op.entered:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for the allowlisted operation to be entered at all")
	}

	// Give a second, wrongly concurrent entrant every reasonable chance to
	// also arrive before releasing the first. Under the bug this fires
	// almost immediately (the second goroutine's own unguarded check races
	// the first's still-blocked execution). Under the fix it can never
	// fire, at any wait length: the second goroutine is parked inside a
	// channel receive that only unblocks after this test calls
	// close(op.unblock) below, so this 250ms window is generous headroom,
	// not a timing assumption the PASS depends on.
	select {
	case <-op.entered:
		t.Fatalf("the allowlisted operation was entered a SECOND time before being released: two concurrent deliveries of the same idempotency key both executed")
	case <-time.After(250 * time.Millisecond):
	}

	close(op.unblock)
	wg.Wait()

	if got := op.callCount(); got != 1 {
		t.Fatalf("op.callCount() = %d, want exactly 1 across two concurrent deliveries of the same idempotency key", got)
	}

	calls1 := pub1.snapshot()
	calls2 := pub2.snapshot()
	if total := len(calls1) + len(calls2); total != 3 {
		t.Fatalf("total publishes across both concurrent calls = %d, want 3 (the executor's result+echo, the waiter's replayed result); calls1=%+v calls2=%+v",
			total, calls1, calls2)
	}

	executorCalls, waiterCalls := calls1, calls2
	if len(executorCalls) != 2 {
		executorCalls, waiterCalls = calls2, calls1
	}
	if len(executorCalls) != 2 || len(waiterCalls) != 1 {
		t.Fatalf("expected one HandleMessage call to publish 2 messages (executor: result + echo) and the other to publish 1 (waiter: replayed result); got %d and %d",
			len(executorCalls), len(waiterCalls))
	}

	executorResult := decodeResultFromCall(t, executorCalls[0])
	waiterResult := decodeResultFromCall(t, waiterCalls[0])
	if executorResult.CommandID != waiterResult.CommandID ||
		executorResult.Outcome != waiterResult.Outcome ||
		executorResult.Evidence == nil || waiterResult.Evidence == nil ||
		executorResult.Evidence.Value != waiterResult.Evidence.Value ||
		!executorResult.Evidence.ObservedAt.Equal(*waiterResult.Evidence.ObservedAt) {
		t.Fatalf("the two concurrent deliveries published different evidence:\nexecutor = %+v\nwaiter   = %+v", executorResult, waiterResult)
	}
}

// TestHandleMessageNonAllowlistedActionRefused proves an action not in the
// registry is refused, a result is still published, and the operation
// registry is never invoked — ARCHITECTURE section 10.4's "agents accept
// only allowlisted operations" made concrete.
func TestHandleMessageNonAllowlistedActionRefused(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	op := &countingOp{}
	h := newTestHandler(map[string]OperationFunc{"agent.echo": op.run}, clock)
	pub := newFakePublisher()

	cmd := baseEchoCmd("cmd-1", "idem-1")
	cmd.Action = "fpp.stop_playlist" // not in this agent's allowlist
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1 (refusal result only, no echo observation)", len(calls))
	}
	if got := op.callCount(); got != 0 {
		t.Fatalf("op.callCount() = %d, want 0 (allowlisted op must never run for a disallowed action)", got)
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeRefused {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, mqttproto.OutcomeRefused)
	}
	if result.Reason == "" {
		t.Fatalf("Reason is empty, want an explanation naming the disallowed action")
	}
}

// TestHandleMessageTargetMismatchRefused proves a command addressed to a
// different node is refused rather than executed.
func TestHandleMessageTargetMismatchRefused(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	op := &countingOp{}
	h := newTestHandler(map[string]OperationFunc{"agent.echo": op.run}, clock)
	pub := newFakePublisher()

	cmd := baseEchoCmd("cmd-1", "idem-1")
	cmd.Target = mqttproto.CmdTarget{Kind: "node", ID: "some-other-node"}
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if got := op.callCount(); got != 0 {
		t.Fatalf("op.callCount() = %d, want 0", got)
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeRefused {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, mqttproto.OutcomeRefused)
	}
}

// TestHandleMessagePastDeadlineRefusedWithoutExecuting proves a command
// whose deadline has already elapsed at receipt is refused without ever
// calling the operation.
func TestHandleMessagePastDeadlineRefusedWithoutExecuting(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)}
	op := &countingOp{}
	h := newTestHandler(map[string]OperationFunc{"agent.echo": op.run}, clock)
	pub := newFakePublisher()

	deadline := clock.t.Add(-time.Minute) // already in the past
	cmd := baseEchoCmd("cmd-1", "idem-1")
	cmd.Deadline = &deadline
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}
	if got := op.callCount(); got != 0 {
		t.Fatalf("op.callCount() = %d, want 0 (must not execute past its deadline)", got)
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeRefused {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, mqttproto.OutcomeRefused)
	}
}

// TestHandleMessageMalformedPayloadDropsWithNoPublish is the actual test
// (not merely a comment) proving that a structurally malformed message —
// invalid JSON, or a literal JSON `null` payload — is logged and dropped
// BY THE ACTUAL DECODE-ERROR GUARD, with no publish at all.
//
// "No publish happened" alone is NOT sufficient evidence of that, and an
// earlier version of this test asserted only that — an adversarial review
// found it passed for the wrong reason: removing the top-level
// DecodeCmdPayload error guard leaves a zero-value CmdPayload (empty
// CommandID) flowing forward, which then fails a DIFFERENT, later guard —
// publishResult's own mqttproto.ResultTopic(h.nodeID, "") call, which
// rejects an empty command ID and logs "bug: could not build result
// topic" — and THAT accidentally also produces zero publishes, masking
// the missing top-level guard entirely. This version instead captures the
// logger's output and asserts the SPECIFIC decode-failure log line fired,
// so it can only pass via the guard it claims to test. Verified by
// temporarily removing each guard in turn and confirming this test then
// fails (not merely "some other test fails") before restoring it.
func TestHandleMessageMalformedPayloadDropsWithNoPublish(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	topic, err := mqttproto.CmdTopic(testNodeID)
	if err != nil {
		t.Fatalf("CmdTopic() error = %v", err)
	}

	tests := []struct {
		name          string
		payload       []byte
		wantLogSubstr string // the specific decode-failure guard that must have fired
	}{
		{
			name:          "invalid JSON",
			payload:       []byte("{not json"),
			wantLogSubstr: "envelope decode failed",
		},
		{
			name:          "literal null payload",
			payload:       []byte(`{"schema":"showmesh.node.cmd/v1","messageId":"m","nodeId":"` + testNodeID + `","sentAt":"2026-08-13T12:00:00Z","payload":null}`),
			wantLogSubstr: "payload decode failed",
		},
		{
			name:          "absent payload key",
			payload:       []byte(`{"schema":"showmesh.node.cmd/v1","messageId":"m","nodeId":"` + testNodeID + `","sentAt":"2026-08-13T12:00:00Z"}`),
			wantLogSubstr: "payload decode failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger, buf := capturingLogger()
			h := newCommandHandler(testNodeID, t.TempDir(), "", nil, nil, nil, nil, nil, nil, clock.now, logger)
			pub := newFakePublisher()

			h.HandleMessage(context.Background(), pub, topic, tt.payload)

			calls := pub.snapshot()
			if len(calls) != 0 {
				t.Fatalf("len(calls) = %d, want 0 (malformed message must be dropped, not answered); calls = %+v", len(calls), calls)
			}
			if got := buf.String(); !strings.Contains(got, tt.wantLogSubstr) {
				t.Fatalf("log output does not contain %q (the specific decode-failure guard this case must be dropped by); got:\n%s", tt.wantLogSubstr, got)
			}
		})
	}
}

// TestHandleMessageAgentEchoMissingOrWrongTypeParamFails proves a missing
// or wrong-typed params.value produces OutcomeFailed, not a panic and not
// a silently-accepted empty string.
func TestHandleMessageAgentEchoMissingOrWrongTypeParamFails(t *testing.T) {
	clock := &fakeClock{t: time.Now()}

	tests := []struct {
		name   string
		params map[string]any
	}{
		{name: "missing value", params: map[string]any{}},
		{name: "wrong type", params: map[string]any{"value": 42}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newCommandHandler(testNodeID, t.TempDir(), "", nil, nil, nil, nil, nil, nil, clock.now, discardLogger())
			pub := newFakePublisher()

			cmd := baseEchoCmd("cmd-1", "idem-"+tt.name)
			cmd.Params = tt.params
			topic, payload := buildCmdMessage(t, clock, cmd)

			h.HandleMessage(context.Background(), pub, topic, payload)

			calls := pub.snapshot()
			if len(calls) != 1 {
				t.Fatalf("len(calls) = %d, want 1 (failed result only, no echo observation)", len(calls))
			}
			result := decodeResultFromCall(t, calls[0])
			if result.Outcome != mqttproto.OutcomeFailed {
				t.Fatalf("Outcome = %q, want %q", result.Outcome, mqttproto.OutcomeFailed)
			}
			if result.Reason == "" {
				t.Fatalf("Reason is empty, want an explanation")
			}
		})
	}
}

// TestHandleMessageWrongTopicKindDropped proves a message delivered on
// something other than this node's cmd topic (defensive: the subscription
// itself is scoped to exactly one topic, so this should be unreachable in
// production) is dropped without a publish, matching every other
// unaddressable-message case in this file.
func TestHandleMessageWrongTopicKindDropped(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	h := newCommandHandler(testNodeID, t.TempDir(), "", nil, nil, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := baseEchoCmd("cmd-1", "idem-1")
	_, payload := buildCmdMessage(t, clock, cmd)

	wrongTopic, err := mqttproto.HelloTopic(testNodeID)
	if err != nil {
		t.Fatalf("HelloTopic() error = %v", err)
	}

	h.HandleMessage(context.Background(), pub, wrongTopic, payload)

	if calls := pub.snapshot(); len(calls) != 0 {
		t.Fatalf("len(calls) = %d, want 0", len(calls))
	}
}

// claimAndComplete simulates a synchronous, always-succeeding operation
// against c: claims key (failing t if it turns out to already be
// resolved — every call site below expects a fresh key) and immediately
// completes it with result, mirroring the claim-then-complete pairing
// HandleMessage itself uses for a real command.
func claimAndComplete(t *testing.T, c *idempotencyCache, key string, result mqttproto.ResultPayload) {
	t.Helper()
	if _, found := c.claimOrAwait(key); found {
		t.Fatalf("claimOrAwait(%q) found = true, want false (key must not already be resolved)", key)
	}
	c.complete(key, result)
}

// TestIdempotencyCacheClaimOrAwaitSequentialContract pins claimOrAwait's
// basic, non-concurrent contract directly, one layer below
// TestHandleMessageConcurrentRedeliveryExecutesOnce's end-to-end
// concurrency proof: a first claim on a fresh key returns the zero value
// and found=false; once completed, every subsequent call for that same
// key returns the completed result and found=true.
func TestIdempotencyCacheClaimOrAwaitSequentialContract(t *testing.T) {
	c := newIdempotencyCache(agentIdempotencyCacheCapacity)

	result, found := c.claimOrAwait("key-1")
	if found {
		t.Fatalf("first claimOrAwait(\"key-1\") found = true, want false (nothing has resolved it yet)")
	}
	if result != (mqttproto.ResultPayload{}) {
		t.Fatalf("first claimOrAwait(\"key-1\") result = %+v, want the zero value", result)
	}

	want := mqttproto.ResultPayload{CommandID: "cmd-1", Outcome: mqttproto.OutcomeConfirmed}
	c.complete("key-1", want)

	got, found := c.claimOrAwait("key-1")
	if !found || got != want {
		t.Fatalf("second claimOrAwait(\"key-1\") = %+v, %v, want %+v, true", got, found, want)
	}
}

// TestIdempotencyCacheEvictsOldestOverCapacity proves idempotencyCache's
// completed-entry cache is actually bounded, not an unbounded map with a
// capacity field nobody reads.
func TestIdempotencyCacheEvictsOldestOverCapacity(t *testing.T) {
	c := newIdempotencyCache(2)
	claimAndComplete(t, c, "a", mqttproto.ResultPayload{CommandID: "a"})
	claimAndComplete(t, c, "b", mqttproto.ResultPayload{CommandID: "b"})
	claimAndComplete(t, c, "c", mqttproto.ResultPayload{CommandID: "c"}) // evicts "a"

	// Check "b" and "c" BEFORE touching "a" again: reclaiming an evicted
	// key and completing it (below) inserts a brand new completed entry,
	// which would itself evict the new oldest survivor — checking "a"
	// first and then completing its reclaim would silently evict "b" and
	// invalidate this test's own assertion about it, which is exactly
	// what happened the first time this test was written.
	if r, found := c.claimOrAwait("b"); !found || r.CommandID != "b" {
		t.Fatalf("claimOrAwait(\"b\") = %+v, %v, want (CommandID: \"b\"), true", r, found)
	}
	if r, found := c.claimOrAwait("c"); !found || r.CommandID != "c" {
		t.Fatalf("claimOrAwait(\"c\") = %+v, %v, want (CommandID: \"c\"), true", r, found)
	}

	if _, found := c.claimOrAwait("a"); found {
		t.Fatalf("claimOrAwait(\"a\") found = true, want false (should have been evicted)")
	}
	// The call above claimed "a" as a brand new in-flight entry (since it
	// was evicted from the completed cache rather than merely "not found
	// yet"); complete it so the claim does not leak as a dangling
	// in-flight entry. Nothing after this point depends on cache state, so
	// the further FIFO churn this causes is harmless here.
	c.complete("a", mqttproto.ResultPayload{CommandID: "a-reclaimed-after-eviction"})
}

// TestHandleMessageAssetFetchEndToEnd proves "asset.fetch" is reachable
// through the real allowlist (newOperationRegistry via newCommandHandler,
// not a swapped-in test op): a real HandleMessage call against a real
// httptest server downloads, verifies, and installs the asset, reports
// OutcomeConfirmed with evidence, and signals the asset inventory trigger
// channel — the wiring command.go adds for this seam, in one place.
func TestHandleMessageAssetFetchEndToEnd(t *testing.T) {
	content := []byte("end-to-end asset content")
	hash := sha256Hash(content)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(content)
	}))
	defer srv.Close()

	dir := t.TempDir()
	trigger := make(chan struct{}, 1)
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	h := newCommandHandler(testNodeID, dir, "", trigger, nil, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := mqttproto.CmdPayload{
		CommandID:      "cmd-asset-1",
		IdempotencyKey: "idem-asset-1",
		Action:         "asset.fetch",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: testNodeID},
		Params: map[string]any{
			"assetId":     "asset-1",
			"contentHash": hash,
			"filename":    "Thriller.fseq",
			"sizeBytes":   float64(len(content)),
			"url":         srv.URL + "/asset",
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "principal-1", PrincipalName: "operator"},
		ConfirmationMethod: confirmationMethodEvidence,
	}
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1 (result only; asset.fetch has no retained echo-style observation of its own)", len(calls))
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeConfirmed {
		t.Fatalf("Outcome = %q, want %q; reason = %q", result.Outcome, mqttproto.OutcomeConfirmed, result.Reason)
	}
	if result.Evidence == nil || result.Evidence.Signal != "node.asset.held" {
		t.Fatalf("Evidence = %+v, want Signal %q", result.Evidence, "node.asset.held")
	}

	got, err := os.ReadFile(filepath.Join(dir, "Thriller.fseq"))
	if err != nil {
		t.Fatalf("reading final asset: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("final asset content = %q, want %q", got, content)
	}

	select {
	case <-trigger:
	default:
		t.Fatalf("assetFetchTrigger was not signalled after a completed asset.fetch")
	}
}

// TestHandleMessageAssetFetchFailureCarriesReasonAndSignal pins this
// seam's agent-side fix: a failed asset.fetch's published result must
// carry BOTH the free-text Reason (err.Error(), already true before this
// fix) AND the Evidence.Signal the operation itself computed
// ("node.asset.fetch_failed", assets.go's downloadErr branch), which used
// to be silently discarded by HandleMessage's error branch. The URL points
// at a closed listener so the download fails for a genuine, nameable
// reason (connection refused), the closest analog to this seam's original
// incident (an unreachable content base URL).
func TestHandleMessageAssetFetchFailureCarriesReasonAndSignal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	unreachableURL := srv.URL + "/asset"
	srv.Close() // closed immediately: the port is now unreachable

	dir := t.TempDir()
	clock := &fakeClock{t: time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)}
	h := newCommandHandler(testNodeID, dir, "", nil, nil, nil, nil, nil, clock.now, discardLogger())
	pub := newFakePublisher()

	cmd := mqttproto.CmdPayload{
		CommandID:      "cmd-asset-fail-1",
		IdempotencyKey: "idem-asset-fail-1",
		Action:         "asset.fetch",
		Target:         mqttproto.CmdTarget{Kind: "node", ID: testNodeID},
		Params: map[string]any{
			"assetId":     "asset-1",
			"contentHash": "sha256:doesnotmatter",
			"filename":    "Thriller.fseq",
			"sizeBytes":   float64(1024),
			"url":         unreachableURL,
		},
		Issuer:             mqttproto.CmdIssuer{PrincipalID: "principal-1", PrincipalName: "operator"},
		ConfirmationMethod: confirmationMethodEvidence,
	}
	topic, payload := buildCmdMessage(t, clock, cmd)

	h.HandleMessage(context.Background(), pub, topic, payload)

	calls := pub.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1 (result only)", len(calls))
	}
	result := decodeResultFromCall(t, calls[0])
	if result.Outcome != mqttproto.OutcomeFailed {
		t.Fatalf("Outcome = %q, want %q", result.Outcome, mqttproto.OutcomeFailed)
	}
	if result.Reason == "" || !strings.Contains(result.Reason, "asset.fetch: download failed") {
		t.Fatalf("Reason = %q, want it to name the download failure", result.Reason)
	}
	if result.Evidence == nil {
		t.Fatal("Evidence is nil, want the operation's own Signal (\"node.asset.fetch_failed\") carried through, not discarded")
	}
	if result.Evidence.Signal != "node.asset.fetch_failed" {
		t.Errorf("Evidence.Signal = %q, want %q", result.Evidence.Signal, "node.asset.fetch_failed")
	}
	if result.Evidence.Value != "asset-1" {
		t.Errorf("Evidence.Value = %v, want the asset ID %q", result.Evidence.Value, "asset-1")
	}
	if result.Evidence.ObservedAt != nil {
		t.Errorf("Evidence.ObservedAt = %v, want nil: a failed fetch never populates it, and a zero-time fallback would fabricate evidence per ADR-011", *result.Evidence.ObservedAt)
	}
}
