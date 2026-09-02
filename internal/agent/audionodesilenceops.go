package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// This file wires "audio.node.silence" — node-scoped like
// audionodeops.go's "audio.node.configure": no sessionId, no revision,
// never routed through parseAudioSessionCommon. It is the installation-
// wide emergency stop's one-dispatch-per-node primitive: every existing
// audio.session.* operation requires a session id and a revision this
// coordinator has already established for it, so a session the
// coordinator never observed cannot be reached by any of them.
// audio.node.silence bypasses that entirely via [audio.Manager.
// SilenceAll], stopping every session this node's Manager currently
// holds regardless of what the coordinator knew about any of them.

// audioNodeSilenceKnownKeys is empty: audio.node.silence takes no
// params at all.
var audioNodeSilenceKnownKeys = map[string]bool{}

// audioNodeSilenceOperations builds the one allowlist entry against mgr.
// mgr is nil-safe at construction, matching audioSessionOperations and
// audioNodeConfigureOperations' identical nil-disables convention: a
// node with no audio manager wired never wires this either.
func audioNodeSilenceOperations(mgr *audio.Manager) map[string]OperationFunc {
	if mgr == nil {
		return nil
	}
	return map[string]OperationFunc{
		"audio.node.silence": silenceNode(mgr),
	}
}

// silenceNode returns the OperationFunc for "audio.node.silence".
// Confirmed is always true: this is an unconditional safety command,
// never refused, and idempotent — silencing an already-silent node is a
// success reporting zero or more already-stopped sessions, not an
// error.
func silenceNode(mgr *audio.Manager) OperationFunc {
	return func(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
		if mgr == nil {
			return OperationResult{}, fmt.Errorf("audio.node.silence: audio session operations are not wired on this node (no asset directory configured)")
		}
		if err := rejectUnknownKeys("audio.node.silence", params, audioNodeSilenceKnownKeys); err != nil {
			return OperationResult{}, err
		}

		executedAt := now()
		results := mgr.SilenceAll(ctx)
		observedAt := now()

		sessions := make([]map[string]any, 0, len(results))
		for _, r := range results {
			sessions = append(sessions, map[string]any{
				"sessionId": string(r.ID),
				"outcome":   string(r.Outcome.Outcome),
				"reason":    r.Outcome.Reason,
			})
		}

		return OperationResult{
			Confirmed: true,
			Signal:    "node.audio.node_silence",
			Value: map[string]any{
				"sessionsFound": len(results),
				"sessions":      sessions,
			},
			ExecutedAt: executedAt,
			ObservedAt: observedAt,
		}, nil
	}
}
