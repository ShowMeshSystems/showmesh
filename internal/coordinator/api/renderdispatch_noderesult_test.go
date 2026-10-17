package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/noderender"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

const renderFrameWriterRefusal = "render.surface.apply: starting frame writer: fseq: requested channel range is not covered by this file"

// renderNodeResultMessage builds a real showmesh.node.result/v1 envelope
// for action with the given outcome, reason and evidence value.
func renderNodeResultMessage(t *testing.T, nodeID, action, outcome, reason string, value map[string]any) broker.Message {
	t.Helper()
	now := time.Now().UTC()
	payload := mqttproto.ResultPayload{
		CommandID: "test-command", IdempotencyKey: "test-idempotency-key", Action: action,
		Outcome: outcome, Reason: reason, ReceivedAt: now, RespondedAt: now,
	}
	if value != nil {
		payload.Evidence = &mqttproto.ResultEvidence{Signal: "node.render.surface_state", Value: value, CollectedAt: now}
	}
	env, err := mqttproto.NewResultEnvelope(func() time.Time { return now }, nodeID, payload)
	if err != nil {
		t.Fatalf("build result envelope: %v", err)
	}
	raw, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal result envelope: %v", err)
	}
	return broker.Message{Payload: raw}
}

// runningPipelineObs is a current "running" reading whose transition is at
// transitionAt, the time the dispatch fence compares against.
func runningPipelineObs(transitionAt time.Time) []observation.Observation {
	return []observation.Observation{
		surfacePipelineStateObs("media-01", "wall-1", "running", transitionAt, transitionAt),
		surfacePipelineChangedAtObs("media-01", "wall-1", transitionAt, transitionAt, transitionAt),
	}
}

func TestRenderApplyNodeResultWinsOverPipelineSignal(t *testing.T) {
	renderCommandConfirmDeadline = 300 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	before, after := testNow.Add(-time.Minute), testNow.Add(time.Second)
	tests := []struct {
		name        string
		result      func(t *testing.T) *broker.Message
		obs         []observation.Observation
		wantOutcome string
		wantReason  string
	}{
		{
			name: "failed result beats a running reading left from before the command",
			result: func(t *testing.T) *broker.Message {
				m := renderNodeResultMessage(t, "media-01", "render.surface.apply", mqttproto.OutcomeFailed, renderFrameWriterRefusal, nil)
				return &m
			},
			obs: runningPipelineObs(before), wantOutcome: "unconfirmed", wantReason: renderFrameWriterRefusal,
		},
		{
			name: "failed result beats a running reading this command's own pipeline start produced",
			result: func(t *testing.T) *broker.Message {
				m := renderNodeResultMessage(t, "media-01", "render.surface.apply", mqttproto.OutcomeFailed, renderFrameWriterRefusal, nil)
				return &m
			},
			obs: runningPipelineObs(after), wantOutcome: "unconfirmed", wantReason: renderFrameWriterRefusal,
		},
		{
			name: "refused result beats a fresh running reading",
			result: func(t *testing.T) *broker.Message {
				m := renderNodeResultMessage(t, "media-01", "render.surface.apply", mqttproto.OutcomeRefused, "A weather delay is active.", nil)
				return &m
			},
			obs: runningPipelineObs(after), wantOutcome: "unconfirmed", wantReason: "A weather delay is active.",
		},
		{
			name: "confirmed result confirms with no signal at all",
			result: func(t *testing.T) *broker.Message {
				m := renderNodeResultMessage(t, "media-01", "render.surface.apply", mqttproto.OutcomeConfirmed, "",
					map[string]any{"surfaceId": "wall-1", "state": "running", "reason": ""})
				return &m
			},
			wantOutcome: "confirmed", wantReason: "The node reports the output is running.",
		},
		{
			name: "no result and a reading from before dispatch does not confirm",
			obs:  runningPipelineObs(before), wantOutcome: "unconfirmed",
		},
		{
			name: "no result and a reading from after dispatch confirms",
			obs:  runningPipelineObs(after), wantOutcome: "confirmed", wantReason: "The output is now running.",
		},
		{
			name: "an unconfirmed result leaves a fresh reading free to confirm",
			result: func(t *testing.T) *broker.Message {
				m := renderNodeResultMessage(t, "media-01", "render.surface.apply", mqttproto.OutcomeUnconfirmed, "pipeline did not reach running in time",
					map[string]any{"surfaceId": "wall-1", "state": "starting", "reason": ""})
				return &m
			},
			obs: runningPipelineObs(after), wantOutcome: "confirmed", wantReason: "The output is now running.",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
			if tc.result != nil {
				setup.pub.awaitResult = tc.result(t)
			}
			setup.obs.setObs(tc.obs)
			h := &handlers{deps: setup.deps(), clock: fixedClock(testNow), logger: testLogger()}

			outcome, problem, err := h.executeRenderDispatch(context.Background(), testNow, renderDispatchInput{
				Action: "render.surface.apply", NodeID: "media-01", SurfaceID: "wall-1",
				Params:         map[string]any{"surfaceId": "wall-1"},
				IdempotencyKey: "key-1", DesiredState: "running",
				IssuerID: "operator-1", IssuerName: "operator",
			})
			if err != nil || problem != nil {
				t.Fatalf("executeRenderDispatch: err %v, problem %+v", err, problem)
			}
			if outcome.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q; reason: %s", outcome.Outcome, tc.wantOutcome, outcome.OutcomeReason)
			}
			if tc.wantReason != "" && outcome.OutcomeReason != tc.wantReason {
				t.Fatalf("outcomeReason = %q, want %q", outcome.OutcomeReason, tc.wantReason)
			}
			if outcome.PipelineFailed {
				t.Fatalf("pipelineFailed = true, want false: no pipeline reported itself failed")
			}

			stored, err := setup.st.GetCommandByIdempotencyKey(context.Background(), "key-1")
			if err != nil {
				t.Fatalf("read stored command: %v", err)
			}
			if replay := renderCommandResultFromRecord(stored, true); replay.Outcome != outcome.Outcome || replay.OutcomeReason != outcome.OutcomeReason {
				t.Fatalf("replay = %q %q, want the dispatch's own %q %q", replay.Outcome, replay.OutcomeReason, outcome.Outcome, outcome.OutcomeReason)
			}
		})
	}
}

// A failed node result decides every render action, not only apply.
func TestRenderNodeFailureDecidesEveryRenderAction(t *testing.T) {
	renderCommandConfirmDeadline = 300 * time.Millisecond
	renderCommandPollInterval = 10 * time.Millisecond
	defer func() {
		renderCommandConfirmDeadline = 15 * time.Second
		renderCommandPollInterval = 250 * time.Millisecond
	}()

	after := testNow.Add(time.Second)
	tests := []struct {
		action, desired string
		obs             []observation.Observation
	}{
		{"render.surface.clear", "stopped", []observation.Observation{
			surfacePipelineStateObs("media-01", "wall-1", "stopped", after, after),
			surfacePipelineChangedAtObs("media-01", "wall-1", after, after, after),
		}},
		{"render.pipeline.restart", "running", runningPipelineObs(after)},
		{"render.transport.probe", "", []observation.Observation{surfaceTransportAvailableObs("media-01", "wall-1", true, after, after)}},
		{"render.surface.blackout", "blackout", []observation.Observation{mustObs(observation.Measured(
			observation.ResourceRef{Kind: observation.ResourceSurface, ID: "wall-1"},
			noderender.SignalSurfaceOutputMode, mqttproto.RenderDrawingBlackout, after,
			observation.WithValidFor(time.Hour), observation.WithCollectedAt(after), observation.WithSource(noderender.SourceFor("media-01")),
		))}},
	}
	for _, tc := range tests {
		t.Run(tc.action, func(t *testing.T) {
			setup := newRenderDispatchTestSetup(t, fixedClock(testNow))
			msg := renderNodeResultMessage(t, "media-01", tc.action, mqttproto.OutcomeFailed, tc.action+": the node could not do it", nil)
			setup.pub.awaitResult = &msg
			setup.obs.setObs(tc.obs)
			h := &handlers{deps: setup.deps(), clock: fixedClock(testNow), logger: testLogger()}

			outcome, problem, err := h.executeRenderDispatch(context.Background(), testNow, renderDispatchInput{
				Action: tc.action, NodeID: "media-01", SurfaceID: "wall-1",
				Params:         map[string]any{"surfaceId": "wall-1"},
				IdempotencyKey: "key-1", DesiredState: tc.desired,
				IssuerID: "operator-1", IssuerName: "operator",
			})
			if err != nil || problem != nil {
				t.Fatalf("executeRenderDispatch: err %v, problem %+v", err, problem)
			}
			if outcome.Outcome != "unconfirmed" || outcome.OutcomeReason != tc.action+": the node could not do it" {
				t.Fatalf("outcome = %q, reason %q; want unconfirmed with the node's own reason", outcome.Outcome, outcome.OutcomeReason)
			}
		})
	}
}
