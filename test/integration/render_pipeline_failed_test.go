//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// This file is Finding 15's own acceptance proof: showmeshctl's
// exitRenderPipelineDown (23) must be reachable through the REAL
// coordinator dispatch/confirm path (renderdispatch.go's
// evaluateRenderSurfaceState), never through a fabricated
// {"outcomeState":"failed"} response a real coordinator could never send —
// OutcomeState only ever carries pkg/observation's State vocabulary
// (current/stale/unknown_age/not_collected/collection_failed/unsupported),
// and "failed" is a surface.pipeline.state VALUE, which used to be visible
// only inside the free-text OutcomeReason.
//
// No real agent process runs here: like agent_command_test.go plays the
// coordinator's role with a raw MQTT client to prove the agent's own
// behavior, this test plays the AGENT's role with a raw MQTT client
// (credentialed via provisionAgentCredential, exactly the access a real
// agent would hold — the "coordinator" role has no write grant on any
// observed/# topic, so this could not be faked with the fixed role used
// elsewhere in this package) to prove the coordinator's own confirm logic
// against a render report that never claims to be anything but data on
// the wire. The dispatch itself goes through the real HTTP endpoint via
// the real showmeshctl binary — nothing here calls coordinator code
// directly.
func TestRenderRestartUnconfirmedWithFailedPipelineExits23(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()

	// render.pipeline.restart's params are just {"surfaceId": surfaceID}
	// (renderdispatch.go's dispatchRenderCommand) — no show, asset, or
	// show.surface config object is required to dispatch or confirm it,
	// so none is created here.
	username, password := provisionAgentCredential(t, nodeID)
	agentCli := rawConnect(t, username, password)

	type ctlResult struct {
		code           int
		stdout, stderr string
	}
	done := make(chan ctlResult, 1)
	go func() {
		code, stdout, stderr := runCtl(t, coord, token, []string{"render", "restart"}, nodeID, surfaceID)
		done <- ctlResult{code, stdout, stderr}
	}()

	// Give the dispatch time to record the command and publish it before
	// this test's own report lands — evaluateRenderSurfaceState fences
	// evidence on ObservedAt >= dispatchedAt, so evidence published too
	// early would be silently ignored as pre-dispatch, and the command
	// would simply run out its full 15s confirm deadline on absence
	// instead of exercising the branch this test is about.
	time.Sleep(500 * time.Millisecond)

	now := time.Now()
	topic, err := mqttproto.ObservedTopic(nodeID, "render")
	if err != nil {
		t.Fatalf("ObservedTopic() error = %v", err)
	}
	env, err := mqttproto.NewRenderEnvelope(func() time.Time { return now }, nodeID, mqttproto.RenderPayload{
		GstLaunchPath:      "/usr/bin/gst-launch-1.0",
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{{
			SurfaceID:     surfaceID,
			PipelineState: mqttproto.RenderPipelineStateFailed,
			Reason:        "starting gst-launch-1.0: exec: fake failure injected by TestRenderRestartUnconfirmedWithFailedPipelineExits23",
			Since:         now,
			LastExitCode:  intPtr(1),
			ObservedAt:    now,
		}},
	})
	if err != nil {
		t.Fatalf("NewRenderEnvelope() error = %v", err)
	}
	payload, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(env) error = %v", err)
	}
	pubCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	resp, err := agentCli.Publish(pubCtx, &paho.Publish{
		QoS:     mqttproto.ObservedDeliveryPolicy.QoS,
		Retain:  mqttproto.ObservedDeliveryPolicy.Retain,
		Topic:   topic,
		Payload: payload,
	})
	if err != nil {
		t.Fatalf("PUBLISH %s: %v", topic, err)
	}
	if resp != nil && resp.ReasonCode >= 0x80 {
		t.Fatalf("PUBLISH %s: broker rejected with reason code %d (provisionAgentCredential should grant this node's own observed/render write)", topic, resp.ReasonCode)
	}

	select {
	case res := <-done:
		if res.code != 23 {
			t.Fatalf("render restart against a surface reporting pipelineState=failed: exit = %d, want 23 (exitRenderPipelineDown)\nstdout=%s\nstderr=%s",
				res.code, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stdout, "unconfirmed") {
			t.Errorf("stdout = %q, want it to report unconfirmed", res.stdout)
		}
		if !strings.Contains(res.stdout, "failed") {
			t.Errorf("stdout = %q, want it to name the failed pipeline state somewhere in the reason", res.stdout)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("render restart did not return within 25s")
	}
}

func intPtr(v int) *int { return &v }
