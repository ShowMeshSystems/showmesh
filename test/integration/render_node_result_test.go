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

// A node that starts its pipeline and then fails the command reports both a
// fresh "running" pipeline and a failed result. The failed result must win.
// This test plays the node with a raw MQTT client, as
// TestRenderRestartUnconfirmedWithFailedPipelineExits23 does.
func TestRenderRestartNodeFailedResultWinsOverFreshRunningPipeline(t *testing.T) {
	requireBroker(t)
	dataDir := t.TempDir()
	coord, token, _ := startAssetCoordinator(t, dataDir, false)

	nodeID := "node-" + uniqueSuffix()
	surfaceID := "surface-" + uniqueSuffix()
	const nodeReason = "render.pipeline.restart: starting frame writer: fseq: requested channel range is not covered by this file"

	username, password := provisionAgentCredential(t, nodeID)
	nodeCli := rawConnect(t, username, password)

	cmdTopic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		t.Fatalf("CmdTopic() error = %v", err)
	}
	cmds := make(chan mqttproto.CmdPayload, 1)
	nodeCli.AddOnPublishReceived(func(pr paho.PublishReceived) (bool, error) {
		if pr.Packet == nil || pr.Packet.Topic != cmdTopic {
			return false, nil
		}
		env, err := mqttproto.DecodeEnvelope(pr.Packet.Payload)
		if err != nil {
			return true, nil
		}
		if cmd, err := mqttproto.DecodeCmdPayload(env); err == nil {
			select {
			case cmds <- cmd:
			default:
			}
		}
		return true, nil
	})
	subCtx, cancelSub := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSub()
	sa, err := nodeCli.Subscribe(subCtx, &paho.Subscribe{Subscriptions: []paho.SubscribeOptions{{Topic: cmdTopic, QoS: 1}}})
	if err != nil {
		t.Fatalf("SUBSCRIBE %s: %v", cmdTopic, err)
	}
	if len(sa.Reasons) == 1 && sa.Reasons[0] >= 0x80 {
		t.Fatalf("SUBSCRIBE %s rejected with reason code %d", cmdTopic, sa.Reasons[0])
	}

	publish := func(topic string, qos byte, retain bool, env mqttproto.Envelope) {
		t.Helper()
		payload, err := json.Marshal(env)
		if err != nil {
			t.Fatalf("json.Marshal(env) error = %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := nodeCli.Publish(ctx, &paho.Publish{QoS: qos, Retain: retain, Topic: topic, Payload: payload})
		if err != nil {
			t.Fatalf("PUBLISH %s: %v", topic, err)
		}
		if resp != nil && resp.ReasonCode >= 0x80 {
			t.Fatalf("PUBLISH %s: broker rejected with reason code %d", topic, resp.ReasonCode)
		}
	}

	type ctlResult struct {
		code           int
		stdout, stderr string
	}
	done := make(chan ctlResult, 1)
	go func() {
		code, stdout, stderr := runCtl(t, coord, token, []string{"render", "restart"}, nodeID, surfaceID)
		done <- ctlResult{code, stdout, stderr}
	}()

	var cmd mqttproto.CmdPayload
	select {
	case cmd = <-cmds:
	case <-time.After(10 * time.Second):
		t.Fatal("the node never received the render.pipeline.restart command")
	}

	// The pipeline came up for this command: a real transition after dispatch.
	now := time.Now()
	renderTopic, err := mqttproto.ObservedTopic(nodeID, "render")
	if err != nil {
		t.Fatalf("ObservedTopic() error = %v", err)
	}
	renderEnv, err := mqttproto.NewRenderEnvelope(func() time.Time { return now }, nodeID, mqttproto.RenderPayload{
		GstLaunchPath:      "/usr/bin/gst-launch-1.0",
		GstLaunchAvailable: true,
		Surfaces: []mqttproto.RenderSurfaceReport{{
			SurfaceID: surfaceID, PipelineState: mqttproto.RenderPipelineStateRunning, Since: now, ObservedAt: now,
		}},
	})
	if err != nil {
		t.Fatalf("NewRenderEnvelope() error = %v", err)
	}
	publish(renderTopic, mqttproto.ObservedDeliveryPolicy.QoS, mqttproto.ObservedDeliveryPolicy.Retain, renderEnv)

	// Long enough for the coordinator to have read the running report first.
	time.Sleep(7 * time.Second)

	responded := time.Now()
	resultTopic, err := mqttproto.ResultTopic(nodeID, cmd.CommandID)
	if err != nil {
		t.Fatalf("ResultTopic() error = %v", err)
	}
	resultEnv, err := mqttproto.NewResultEnvelope(func() time.Time { return responded }, nodeID, mqttproto.ResultPayload{
		CommandID: cmd.CommandID, IdempotencyKey: cmd.IdempotencyKey, Action: cmd.Action,
		Outcome: mqttproto.OutcomeFailed, Reason: nodeReason,
		ReceivedAt: now, RespondedAt: responded,
	})
	if err != nil {
		t.Fatalf("NewResultEnvelope() error = %v", err)
	}
	publish(resultTopic, mqttproto.ResultDeliveryPolicy.QoS, mqttproto.ResultDeliveryPolicy.Retain, resultEnv)

	select {
	case res := <-done:
		if res.code != 9 {
			t.Fatalf("render restart the node failed: exit = %d, want 9 (unconfirmed)\nstdout=%s\nstderr=%s", res.code, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stdout, "unconfirmed") || !strings.Contains(res.stdout, nodeReason) {
			t.Fatalf("stdout = %q, want unconfirmed with the node's own reason", res.stdout)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("render restart did not return within 25s")
	}
}
