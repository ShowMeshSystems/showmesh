package macro

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// fakeAudioActions is this package's own audioActionDispatcher fake,
// mirroring fakeResolumeActions (step_resolume_test.go) one integration
// over.
type fakeAudioActions struct {
	mu    sync.Mutex
	calls []string // action names, in dispatch order

	dispatchFn func(ctx context.Context, in api.AudioDispatchInput) (v1.AudioSessionCommandResult, *v1.Problem, error)
}

func (f *fakeAudioActions) Dispatch(ctx context.Context, in api.AudioDispatchInput) (v1.AudioSessionCommandResult, *v1.Problem, error) {
	f.mu.Lock()
	f.calls = append(f.calls, in.Action)
	f.mu.Unlock()
	if f.dispatchFn != nil {
		return f.dispatchFn(ctx, in)
	}
	return v1.AudioSessionCommandResult{
		CommandID: "cmd-1", Action: in.Action, NodeID: in.NodeID, SessionID: in.SessionID,
		Outcome: "started", Reason: "test evidence confirmed playback began",
	}, nil, nil
}

func (f *fakeAudioActions) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// audioAction builds the smallest valid show.action payload this
// package's own decode expects for an audio target, mirroring
// fppAction/mqttAction/resolumeAction (testing_test.go/step_resolume_test.go).
func audioAction(nodeID, sessionID, action, safetyClass string) config.ShowActionPayload {
	return config.ShowActionPayload{
		Show: "test-show", Label: "test audio action", SafetyClass: safetyClass,
		Target: config.ShowActionTarget{
			Integration: config.ShowActionIntegrationAudio,
			AudioNodeIDs: config.AudioNodeIDList{nodeID}, AudioSessionID: sessionID, AudioAction: action,
		},
	}
}

// newTestExecutorWithAudio is newTestExecutor plus an audioActionDispatcher,
// mirroring newTestExecutorWithResolume's identical reasoning: setting
// Executor.audioActions directly (this file is package macro, not
// macro_test) avoids growing every existing newTestExecutor call site.
func newTestExecutorWithAudio(t *testing.T, dispatch fppDispatcher, brokers mqttRegistry, audioActions audioActionDispatcher) (*Executor, *store.Store, string, identity.Service) {
	t.Helper()
	st, svc, storeDir := newTestStoreAndIdentity(t, time.Now)
	e, _ := newTestExecutor(t, st, svc, dispatch, brokers)
	e.audioActions = audioActions
	return e, st, storeDir, svc
}

// TestMacroRunDispatchesAudioStepThroughSameDispatcher is this lane's own
// proof that a macro run's audio step dispatches through
// [api.AudioActionDispatcher] — the same in-process core POST
// /api/v1/nodes/{nodeId}/audio/sessions/{sessionId}/... uses — rather than
// falling into dispatchStep's default "unrecognized integration" branch,
// which is what happened before this integration had a case of its own
// (run.go's switch used to end at "resolume", answering audio with a
// failed step and outcomeState "unknown_integration" for every audio
// show.action, macro-run-tested or not).
func TestMacroRunDispatchesAudioStepThroughSameDispatcher(t *testing.T) {
	t.Run("confirmed", func(t *testing.T) {
		fake := &fakeAudioActions{}
		e, st, _, _ := newTestExecutorWithAudio(t, &fakeDispatcher{}, &fakeBrokers{}, fake)
		putAction(t, st, "a1", audioAction("node-a", "announcement", "audio.session.start", config.ShowSafetyClassNone))
		putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

		got := submitAndWait(t, e, api.MacroSubmitRequest{
			MacroObjectID: "m1", IdempotencyKey: "key-confirmed", Trigger: "api", Issuer: testIssuer(),
		})
		if fake.callCount() != 1 {
			t.Fatalf("audio dispatcher called %d times, want 1", fake.callCount())
		}
		if fake.calls[0] != "audio.session.start" {
			t.Fatalf("dispatched action = %q, want audio.session.start", fake.calls[0])
		}
		if got.Steps[0].Outcome != outcomeConfirmed {
			t.Fatalf("step outcome = %q, want %q: %+v", got.Steps[0].Outcome, outcomeConfirmed, got.Steps[0])
		}
		if got.Run.Confirmed == nil || !*got.Run.Confirmed {
			t.Fatalf("run Confirmed = %v, want true", got.Run.Confirmed)
		}
	})

	t.Run("unconfirmable", func(t *testing.T) {
		fake := &fakeAudioActions{dispatchFn: func(ctx context.Context, in api.AudioDispatchInput) (v1.AudioSessionCommandResult, *v1.Problem, error) {
			return v1.AudioSessionCommandResult{
				Outcome: "unconfirmable", Reason: "no result received from the node before the deadline",
			}, nil, nil
		}}
		e, st, _, _ := newTestExecutorWithAudio(t, &fakeDispatcher{}, &fakeBrokers{}, fake)
		putAction(t, st, "a1", audioAction("node-a", "announcement", "audio.session.start", config.ShowSafetyClassNone))
		putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

		got := submitAndWait(t, e, api.MacroSubmitRequest{
			MacroObjectID: "m1", IdempotencyKey: "key-unconfirmable", Trigger: "api", Issuer: testIssuer(),
		})
		if got.Steps[0].Outcome != outcomeUnconfirmable {
			t.Fatalf("step outcome = %q, want %q: %+v", got.Steps[0].Outcome, outcomeUnconfirmable, got.Steps[0])
		}
		if got.Run.Confirmed == nil || *got.Run.Confirmed {
			t.Fatalf("run Confirmed = %v, want false", got.Run.Confirmed)
		}
	})

	t.Run("not configured", func(t *testing.T) {
		e, st, _, _ := newTestExecutorWithAudio(t, &fakeDispatcher{}, &fakeBrokers{}, nil)
		putAction(t, st, "a1", audioAction("node-a", "announcement", "audio.session.start", config.ShowSafetyClassNone))
		putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

		got := submitAndWait(t, e, api.MacroSubmitRequest{
			MacroObjectID: "m1", IdempotencyKey: "key-not-configured", Trigger: "api", Issuer: testIssuer(),
		})
		if got.Steps[0].Outcome != outcomeFailed {
			t.Fatalf("step outcome = %q, want %q: %+v", got.Steps[0].Outcome, outcomeFailed, got.Steps[0])
		}
		if got.Steps[0].OutcomeState != audioStateNotConfigured {
			t.Fatalf("step outcomeState = %q, want %q", got.Steps[0].OutcomeState, audioStateNotConfigured)
		}
	})
}

// TestMacroRunAudioStepDispatchesOnlyTheFirstOfMultipleTargetNodes pins
// dispatchAudioStep's own documented contract: every consumer of an
// audio-integration show.action OTHER than a night-session announcement
// or the night-mode resting bed reads only the FIRST configured
// audioNodeId (api/openapi.yaml's ConfigShowActionTarget.audioNodeId doc
// comment). Names three distinct node ids, none of them alphabetically
// or positionally special beyond "first", so a test that happened to
// read the last or a sorted element would not accidentally pass.
// Asserting the exact dispatched node id, not merely that a dispatch
// happened, is what a silent last-element or all-elements substitution
// would fail.
func TestMacroRunAudioStepDispatchesOnlyTheFirstOfMultipleTargetNodes(t *testing.T) {
	var gotNodeIDs []string
	fake := &fakeAudioActions{dispatchFn: func(ctx context.Context, in api.AudioDispatchInput) (v1.AudioSessionCommandResult, *v1.Problem, error) {
		gotNodeIDs = append(gotNodeIDs, in.NodeID)
		return v1.AudioSessionCommandResult{
			CommandID: "cmd-1", Action: in.Action, NodeID: in.NodeID, SessionID: in.SessionID,
			Outcome: "started", Reason: "test evidence confirmed playback began",
		}, nil, nil
	}}
	e, st, _, _ := newTestExecutorWithAudio(t, &fakeDispatcher{}, &fakeBrokers{}, fake)
	multiTarget := config.ShowActionPayload{
		Show: "test-show", Label: "test multi-node audio action", SafetyClass: config.ShowSafetyClassNone,
		Target: config.ShowActionTarget{
			Integration:    config.ShowActionIntegrationAudio,
			AudioNodeIDs:   config.AudioNodeIDList{"porch-node", "yard-node", "attic-node"},
			AudioSessionID: "announcement", AudioAction: "audio.session.start",
		},
	}
	putAction(t, st, "a1", multiTarget)
	putMacro(t, st, "m1", testMacroPayload(testStep("s1", "a1")))

	got := submitAndWait(t, e, api.MacroSubmitRequest{
		MacroObjectID: "m1", IdempotencyKey: "key-multi-target", Trigger: "api", Issuer: testIssuer(),
	})
	if got.Steps[0].Outcome != outcomeConfirmed {
		t.Fatalf("step outcome = %q, want %q: %+v", got.Steps[0].Outcome, outcomeConfirmed, got.Steps[0])
	}
	if fake.callCount() != 1 {
		t.Fatalf("audio dispatcher called %d times, want exactly 1 (only the first node dispatched)", fake.callCount())
	}
	if len(gotNodeIDs) != 1 || gotNodeIDs[0] != "porch-node" {
		t.Fatalf("dispatched nodeId(s) = %v, want exactly [%q] (the first configured target node)", gotNodeIDs, "porch-node")
	}
}
