package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// TestShutdownCleanlyCallsPublishBeforeDisconnectOnGivenConn is the test
// the Task D spec calls out explicitly: assert the ORDERING, not merely
// that both the offline publish and the disconnect happened, given a Conn
// that shutdownCleanly is already holding.
//
// WHAT THIS DOES NOT COVER, AND WHY THE NAME SAYS "ON GIVEN CONN": this
// only proves shutdownCleanly itself orders its two calls correctly against
// a fake. It says nothing about whether the *runtime* Conn that reaches
// shutdownCleanly still has a live, un-torn-down connection to make that
// publish over — that depends on internal/agent.Run's wiring of the MQTT
// connection manager's context lifetime, which a fake Conn cannot exercise
// (see agent.go's connCtx comment for the bug this exact gap in coverage
// let through once already: signal-context cancellation racing ahead of
// this function and discarding the Will before it ever got here). That
// wiring is unit-untestable without dialing a broker, so it is the job of
// TestAgentCleanShutdownGoesOfflinePromptly in
// test/integration/lifecycle_test.go, not this test, to be the real guard
// against that regression. Do not treat this test passing as evidence that
// a clean shutdown actually publishes the offline message in production.
func TestShutdownCleanlyCallsPublishBeforeDisconnectOnGivenConn(t *testing.T) {
	fc := newFakeConn()

	shutdownCleanly(context.Background(), fc, "media-03", discardLogger())

	order := fc.callOrder()
	lwtTopic, _ := mqttproto.LWTTopic("media-03")
	want := []string{"publish:" + lwtTopic, "disconnect"}

	if len(order) != len(want) {
		t.Fatalf("call order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("call order = %v, want %v", order, want)
		}
	}
}

func TestShutdownCleanlyPublishesOfflineWithReason(t *testing.T) {
	fc := newFakeConn()

	shutdownCleanly(context.Background(), fc, "media-03", discardLogger())

	calls := fc.snapshot()
	if len(calls) != 1 {
		t.Fatalf("len(calls) = %d, want 1", len(calls))
	}

	env, err := mqttproto.DecodeEnvelope(calls[0].payload)
	if err != nil {
		t.Fatalf("DecodeEnvelope() error = %v", err)
	}
	lwt, err := mqttproto.DecodeLWTPayload(env)
	if err != nil {
		t.Fatalf("DecodeLWTPayload() error = %v", err)
	}
	if lwt.Online {
		t.Errorf("Online = true, want false")
	}
	if lwt.Reason == "" {
		t.Errorf("Reason is empty, want a clean-shutdown reason")
	}
	if calls[0].retain != true {
		t.Errorf("retain = %v, want true", calls[0].retain)
	}
}

// TestShutdownCleanlyStillDisconnectsWhenPublishFails verifies Disconnect
// is called even when the offline publish itself fails: a failed
// notification is not a reason to also leave the connection open.
func TestShutdownCleanlyStillDisconnectsWhenPublishFails(t *testing.T) {
	fc := newFakeConn()
	fc.failOn = map[int]bool{0: true}

	shutdownCleanly(context.Background(), fc, "media-03", discardLogger())

	order := fc.callOrder()
	if len(order) != 2 || order[0] == "disconnect" || order[1] != "disconnect" {
		t.Fatalf("call order = %v, want a publish attempt followed by disconnect even though the publish failed", order)
	}
}

func TestShutdownCleanlyLogsButDoesNotPanicOnDisconnectError(t *testing.T) {
	fc := newFakeConn()
	fc.disconnectErr = errors.New("simulated disconnect error")

	// Must not panic.
	shutdownCleanly(context.Background(), fc, "media-03", discardLogger())
}
