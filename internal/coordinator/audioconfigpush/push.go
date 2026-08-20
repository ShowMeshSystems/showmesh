// Package audioconfigpush is ADR-039/ADR-036's "applies without a
// restart" half for the audio.node and audio.settings configuration
// kinds: every write to either, and every node hello, pushes the
// resolved current revision to the affected node over its own cmd
// topic, so a node that was offline during the write converges once it
// reconnects. The agent's paired audio.node.configure/
// audio.settings.configure operations (internal/agent) are the only way
// an agent ever learns its own output binding. This is a direct
// consequence of an existing config write and of a node announcing
// itself — no new API endpoint, scope, or CLI verb exists for it.
package audioconfigpush

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

// ConfigStore is this package's read dependency — [store.Store] already
// satisfies it with no adapter, the same property api.ConfigStore's own
// doc comment notes for itself.
type ConfigStore interface {
	GetConfigObject(ctx context.Context, kind, id string) (store.ConfigObjectRecord, error)
	GetConfigRevision(ctx context.Context, kind, id string, revision int64) (store.ConfigRevisionRecord, error)
}

// Publisher is this package's MQTT publish dependency — *broker.
// BrokerManager already satisfies it with no adapter, matching api.
// RenderPublisher's identical shape one package over.
type Publisher interface {
	Publish(ctx context.Context, topic string, qos byte, retain bool, payload []byte) error
}

// pushIssuer names this package's own commands in every audit trail and
// log line a node's CommandHandler produces for them — never a real
// operator principal, since nothing here was issued by one.
const pushIssuer = "showmesh-coordinator-config-push"

// ToNode pushes nodeID's current audio.node binding (a no-op, not an
// error, when nothing has ever been configured for this node) and the
// coordinator's current audio.settings (always resolvable —
// [config.AudioSettingsDefaultPayload] when nothing has ever been
// written). Both failures are reported, never silently merged into one.
func ToNode(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string) error {
	if err := pushNode(ctx, cs, pub, now, nodeID); err != nil {
		return fmt.Errorf("push audio.node to %s: %w", nodeID, err)
	}
	if err := pushSettings(ctx, cs, pub, now, nodeID); err != nil {
		return fmt.Errorf("push audio.settings to %s: %w", nodeID, err)
	}
	return nil
}

// BestEffort calls [ToNode] and logs any failure rather than propagating
// it: a push that cannot reach a node right now is not a reason to fail
// the write or the hello that triggered it. The node converges on its
// next successful push — its next hello, or the next write to either
// kind.
func BestEffort(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string, logger *slog.Logger) {
	if err := ToNode(ctx, cs, pub, now, nodeID); err != nil && logger != nil {
		logger.Warn("audio config push failed", "node_id", nodeID, "error", err)
	}
}

func pushNode(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string) error {
	obj, err := cs.GetConfigObject(ctx, config.AudioNodeConfigKind, nodeID)
	if errors.Is(err, store.ErrConfigObjectNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if obj.CurrentRevision == 0 {
		return nil
	}
	rev, err := cs.GetConfigRevision(ctx, config.AudioNodeConfigKind, nodeID, obj.CurrentRevision)
	if err != nil {
		return err
	}
	payload, verr := config.DecodeAudioNodePayload(rev.PayloadJSON)
	if verr != nil {
		return fmt.Errorf("decode stored audio.node payload: %s", verr.Error())
	}

	params := map[string]any{
		"programRoute": payload.ProgramRoute, "ltcRoute": payload.LTCRoute,
		"programChannels": payload.ProgramChannels, "ltcChannel": payload.LTCChannel,
		"clockDomain": payload.ClockDomain, "clockDomainProvenance": payload.ClockDomainProvenance,
		"revision": obj.CurrentRevision,
	}
	idempotencyKey := fmt.Sprintf("audio.node.configure/%s/rev-%d", nodeID, obj.CurrentRevision)
	return publish(ctx, pub, now, nodeID, "audio.node.configure", idempotencyKey, params)
}

func pushSettings(ctx context.Context, cs ConfigStore, pub Publisher, now func() time.Time, nodeID string) error {
	payload := config.AudioSettingsDefaultPayload
	revision := int64(0)

	obj, err := cs.GetConfigObject(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		// Default payload, revision 0 — nothing has ever been written.
	case err != nil:
		return err
	case obj.CurrentRevision == 0:
		// Same as above: an object row exists with no active revision.
	default:
		rev, gerr := cs.GetConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, obj.CurrentRevision)
		if gerr != nil {
			return gerr
		}
		decoded, verr := config.DecodeAudioSettingsPayload(rev.PayloadJSON)
		if verr != nil {
			return fmt.Errorf("decode stored audio.settings payload: %s", verr.Error())
		}
		payload = decoded
		revision = obj.CurrentRevision
	}

	params := map[string]any{
		"driftIgnoreThresholdMs":   payload.DriftIgnoreThresholdMs,
		"defaultFadeCurve":         payload.DefaultFadeCurve,
		"defaultFadeDurationMs":    payload.DefaultFadeDurationMs,
		"defaultMaxBackgroundGain": payload.DefaultMaxBackgroundGain,
		"ltcFrameRate":             payload.LTCFrameRate,
		"ltcDefaultStartOffset":    payload.LTCDefaultStartOffset,
		"revision":                 revision,
	}
	idempotencyKey := fmt.Sprintf("audio.settings.configure/%s/rev-%d", nodeID, revision)
	return publish(ctx, pub, now, nodeID, "audio.settings.configure", idempotencyKey, params)
}

// publish builds and sends one CmdPayload. CommandID is a fresh UUID on
// every call (topic-shape rules require it — see
// [mqttproto.ValidateCmdID]); IdempotencyKey is deterministic per
// (action, node, revision) instead, so a redelivery of the same revision
// (a write immediately followed by a hello, for instance) is answered
// from the node's own idempotency cache rather than re-applied twice.
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
