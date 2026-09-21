package agent

import (
	"context"
	"fmt"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// This file wires "audio.node.silence", node-scoped like
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
		string(pkgaudio.OperationNodeSilence): silenceNode(mgr),
	}
}

// silenceNode returns the OperationFunc for "audio.node.silence": never
// refused, and idempotent, silencing an already-silent node is a
// success reporting zero or more already-stopped sessions, not an
// error. Confirmed is true only when every session genuinely stopped,
// matching [sessionOp]'s computation for audio.session.stop, AND the
// final engine-wide sweep itself ran to completion: a sweep that could
// not run (an engine rebind window, most likely) must never let a
// node full of stopped sessions read as a clean stop when the sweep
// behind them never actually happened.
func silenceNode(mgr *audio.Manager) OperationFunc {
	return func(ctx context.Context, params map[string]any, now func() time.Time) (OperationResult, error) {
		if mgr == nil {
			return OperationResult{}, fmt.Errorf("audio.node.silence: audio session operations are not wired on this node (no asset directory configured)")
		}
		if err := rejectUnknownKeys("audio.node.silence", params, audioNodeSilenceKnownKeys); err != nil {
			return OperationResult{}, err
		}

		executedAt := now()
		results, unclaimedReleased, sweepConfirmed := mgr.SilenceAll(ctx)
		observedAt := now()

		confirmed := sweepConfirmed
		sessions := make([]map[string]any, 0, len(results))
		for _, r := range results {
			if !outcomeConfirmed(r.Outcome) {
				confirmed = false
			}
			sessions = append(sessions, map[string]any{
				"sessionId": string(r.ID),
				"outcome":   string(r.Outcome.Outcome),
				"reason":    r.Outcome.Reason,
			})
		}

		return OperationResult{
			Confirmed: confirmed,
			Signal:    "node.audio.silence",
			Value: map[string]any{
				"sessionsFound": len(results),
				"sessions":      sessions,
				// unclaimedBranchesReleased is the final engine-wide sweep's
				// own count (audio.Manager.SilenceAll): branches it released
				// that no session above already accounted for. Carried in
				// this node-side evidence and, when nonzero, in one WARN
				// this node's own log names by handle (audio.Manager.
				// releaseEveryEngineBranchExcept) -- not yet surfaced
				// through the coordinator's own audio.node.silence API
				// result, which is a later change.
				"unclaimedBranchesReleased": unclaimedReleased,
			},
			ExecutedAt: executedAt,
			ObservedAt: observedAt,
		}, nil
	}
}
