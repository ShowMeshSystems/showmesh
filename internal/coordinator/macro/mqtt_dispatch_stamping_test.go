package macro

import (
	"context"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// Guards that dispatchedAt is stamped before publishAndAwait, not after.
// Uses a real clock and a broker fake that sleeps: with a fixed clock the
// two orderings produce identical timestamps and this test would pass
// either way.
func TestDispatchMQTTStepStampsDispatchedAtBeforeThePublish(t *testing.T) {
	const awaitDelay = 75 * time.Millisecond

	st, svc, _ := newTestStoreAndIdentity(t, time.Now)
	brokers := &fakeBrokers{
		awaitFn: func(ctx context.Context, id string, req broker.ResponseRequest) (broker.Message, error) {
			time.Sleep(awaitDelay)
			return broker.Message{Topic: req.ResponseTopic, Payload: []byte("true")}, nil
		},
	}
	e, _ := newTestExecutor(t, st, svc, &fakeDispatcher{}, brokers)

	expect := config.ShowActionMQTTExpect{Kind: config.MQTTExpectKindBoolean, Topic: "resp", DeadlineSeconds: 5}
	action := resolvedAction{
		ObjectID: "a-stamping",
		Revision: 1,
		Payload:  mqttAction("home-automation", config.ShowSafetyClassNone, expect),
	}
	run := store.MacroRunRecord{ID: "run-stamping"}
	step := store.MacroRunStepRecord{RunID: run.ID, StepIndex: 0, StepID: "step-1"}

	res := e.dispatchMQTTStep(context.Background(), run, step, action, testIssuer())

	if res.dispatchedAt == nil {
		t.Fatal("dispatchedAt = nil, want non-nil: this step put a publish on the wire")
	}
	if res.resolvedAt == nil {
		t.Fatal("resolvedAt = nil, want non-nil")
	}
	gap := res.resolvedAt.Sub(*res.dispatchedAt)
	if gap < awaitDelay {
		t.Fatalf("resolvedAt - dispatchedAt = %v, want >= %v (the AwaitResponse sleep): "+
			"dispatchedAt was stamped after the publish/await instead of before it", gap, awaitDelay)
	}
}
