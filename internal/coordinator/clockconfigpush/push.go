// Package clockconfigpush is ADR-039/ADR-036's "applies without a
// restart" half for the node.clock configuration kind: every write to it,
// and every node hello, pushes the resolved current revision to the
// affected node over its own cmd topic, so a node that was offline
// during the write converges once it reconnects. The agent's paired
// node.clock.configure operation (internal/agent/clockconfigops.go) is
// the only way an agent ever learns which PTP provider to run. Mirrors
// internal/coordinator/audioconfigpush exactly, one configuration kind
// over — per the orchestrator's own ruling that node.clock.configure
// follows the audio.node.configure precedent exactly (push on write and
// on hello, no restart).
package clockconfigpush

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/mqttproto"
)

// clockConfigSchema is "node.clock.configure"'s params schema string
// (docs/build/IDENTIFIER-REGISTER.md's reservation), independently
// mirrored on internal/agent's side (clockconfigops.go's identical
// constant) — this package has no agent dependency, matching every other
// wire boundary in this codebase.
const clockConfigSchema = "showmesh.node.clock.config/v1"

// ConfigStore is this package's read dependency — [store.Store] already
// satisfies it with no adapter, matching audioconfigpush.ConfigStore's
// identical shape.
type ConfigStore interface {
	GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
}

// Publisher is this package's MQTT publish dependency — *broker.
// BrokerManager already satisfies it with no adapter, matching
// audioconfigpush.Publisher's identical shape.
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// pushIssuer names this package's own commands in every audit trail and
// log line a node's CommandHandler produces for them, matching
// audioconfigpush.pushIssuer's identical role — never a real operator
// principal, since nothing here was issued by one.
const pushIssuer = "showmesh-coordinator-config-push"

// ToNode pushes nodeID's current node.clock binding — a no-op, not an
// error, when nothing has ever been configured for this node (a node with
// no node.clock object reports "unsynchronized" and behaves exactly as
// today, per this seam's own acceptance criterion).
func ToNode(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string) error {
	obj, err := cs.GetConfigObject(ctx, config.NodeClockConfigKind, nodeID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("push node.clock to %s: %w", nodeID, err)
	}
	if obj.CurrentRevision == 0 {
		return nil
	}
	rev, err := cs.GetConfigRevision(ctx, config.NodeClockConfigKind, nodeID, obj.CurrentRevision)
	if err != nil {
		return fmt.Errorf("push node.clock to %s: %w", nodeID, err)
	}
	payload, verr := config.DecodeNodeClockPayload(rev.PayloadJSON)
	if verr != nil {
		return fmt.Errorf("push node.clock to %s: decode stored payload: %s", nodeID, verr.Error())
	}

	params := map[string]any{
		"schema":   clockConfigSchema,
		"provider": payload.Provider, "interface": payload.Interface, "domain": payload.Domain,
		"revision": obj.CurrentRevision,
	}
	if payload.ClientOnly {
		params["clientOnly"] = true
	}
	if payload.HoldoverLimitSeconds != 0 {
		params["holdoverLimitSeconds"] = payload.HoldoverLimitSeconds
	}
	if payload.Priority1 != 0 {
		params["priority1"] = payload.Priority1
	}
	if payload.HardwareTimestamping {
		params["hardwareTimestamping"] = true
	}
	if payload.ExternalUDSAddress != "" {
		params["externalUdsAddress"] = payload.ExternalUDSAddress
	}
	if payload.FPPBaseURL != "" {
		params["fppBaseUrl"] = payload.FPPBaseURL
	}

	idempotencyKey := fmt.Sprintf("node.clock.configure/%s/rev-%d", nodeID, obj.CurrentRevision)
	if err := publish(ctx, pub, now, nodeID, "node.clock.configure", idempotencyKey, params); err != nil {
		return fmt.Errorf("push node.clock to %s: %w", nodeID, err)
	}
	return nil
}

// BestEffort calls [ToNode] and logs any failure rather than propagating
// it, matching audioconfigpush.BestEffort's identical contract: a push
// that cannot reach a node right now is not a reason to fail the write or
// the hello that triggered it. The node converges on its next successful
// push.
func BestEffort(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string, logger *slog.Logger) {
	if err := ToNode(ctx, cs, pub, now, nodeID); err != nil && logger != nil {
		logger.Warn("node.clock config push failed", "node_id", nodeID, "error", err)
	}
}

// publish builds and sends one CmdPayload, matching audioconfigpush's own
// identical helper — see that function's doc comment for CommandID/
// IdempotencyKey's distinct roles.
func publish(ctx context.Context, pub Publisher, now func() time.Time, nodeID, action, idempotencyKey string, params map[string]any) error {
	topic, err := mqttproto.CmdTopic(nodeID)
	if err != nil {
		return err
	}
	t := now()
	cmd := mqttproto.CmdPayload{
		CommandID: uuid.NewString(), IdempotencyKey: idempotencyKey, Action: action,
		Target: mqttproto.CmdTarget{Kind: "node", ID: nodeID}, Params: params,
		Issuer:             mqttproto.CmdIssuer{PrincipalID: pushIssuer, PrincipalName: pushIssuer},
		ConfirmationMethod: "evidence",
	}
	env, err := mqttproto.NewCmdEnvelope(func() time.Time { return t }, nodeID, cmd)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(env)
	if err != nil {
		return err
	}
	return pub.Publish(ctx, topic, mqttproto.CmdDeliveryPolicy.QoS, mqttproto.CmdDeliveryPolicy.Retain, raw)
}
