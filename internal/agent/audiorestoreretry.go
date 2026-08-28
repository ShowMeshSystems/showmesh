package agent

import (
	"context"
	"log/slog"
	"time"

	"github.com/showmeshsystems/showmesh/internal/agent/audio"
)

// audioRestoreRetryDelays is the bounded, backed-off schedule
// runAudioRestoreRetry uses to re-probe the device and retry every
// deferred restore on its own, without waiting for a coordinator-pushed
// audio.node binding: doubling from 5s to a 5-minute ceiling, eight
// attempts (just under 19 minutes of automatic retrying) before the
// driver stops trying on its own and leaves the standing fault as the
// only remaining signal — a genuine audio.node.configure delivery still
// resolves it at any time via [audio.Manager.RebindEngine]'s own
// retryDeferredRestores, bounded schedule or not.
// SHOWMESH HYPOTHESIS, NOT MEASURED: chosen so the first few attempts
// land well inside the "plugged in ~30s later" acceptance window, not
// against bench data.
var audioRestoreRetryDelays = []time.Duration{
	5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second,
	80 * time.Second, 160 * time.Second, 5 * time.Minute, 5 * time.Minute,
}

// audioRestoreRetryPollInterval is how often runAudioRestoreRetry wakes
// to check whether a backed-off attempt is due. There is no event to
// wait on for "a restore was just deferred", so this polls
// [audio.Manager.PendingRestoreCount] instead — matching every other
// ticker-driven loop this package already runs (agent.go).
const audioRestoreRetryPollInterval = 5 * time.Second

// audioRestoreRetryer is runAudioRestoreRetry's own loop state: how many
// automatic attempts it has made since every deferred restore last
// resolved, and when it may try again. Not guarded by a mutex — only
// runAudioRestoreRetry's own goroutine ever touches it; the status other
// packages can read is [audio.Manager]'s own
// SetRestoreRetryStatus/ClearRestoreRetryStatus mirror, updated every
// tick.
type audioRestoreRetryer struct {
	attempts      int
	nextAttemptAt time.Time
}

// runAudioRestoreRetry is this node's own automatic half of the retry
// machinery [audio.Manager.RebindEngine] already provides: RebindEngine
// resolves every deferred restore the moment a NEW audio.node binding
// arrives, but
// nothing before this drove that resolution on its own: an interface
// that enumerates late, with no coordinator reconnect and no newer
// audio.node revision, left the session pending indefinitely — this
// loop's whole reason to exist.
//
// On a bounded, backed-off schedule, whenever [audio.Manager.
// PendingRestoreCount] is nonzero AND the currently bound engine reports
// itself unavailable, this calls rebuild against the SAME,
// already-accepted audio.node binding currentNode reports.
// [audioEngineRebuilder.rebuildResult] re-probes the device fresh on
// every call ([buildGstEngineConfig] runs discovery inline), so a
// device that only just enumerated is picked up with no binding
// redelivery at all, and its own [audio.Manager.RebindEngine] call
// resolves every pending session exactly as a real binding delivery
// would.
//
// The engineAvailable gate is deliberate and load-bearing: when the
// bound engine already reports available, whatever is keeping a session
// pending has nothing to do with device availability (a genuinely
// failing asset, say), and rebuilding here would invalidate — and
// audibly interrupt — every OTHER session currently playing on that same
// working engine for no benefit to the stuck one. This loop only ever
// acts when there is nothing already working to lose.
//
// currentNode reports the most recently accepted audio.node binding —
// nothing to retry against until the first one lands, so a false ok
// leaves this driver idle rather than probing with a zero-value config.
func runAudioRestoreRetry(
	ctx context.Context,
	mgr *audio.Manager,
	engineAvailable func() (bool, string),
	currentNode func() (audioNodeConfig, bool),
	rebuild func(audioNodeConfig) audioRebuildOutcome,
	now func() time.Time,
	ticks <-chan time.Time,
	logger *slog.Logger,
) {
	var retry audioRestoreRetryer
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-ticks:
			if !ok {
				return
			}
			runAudioRestoreRetryTick(mgr, engineAvailable, currentNode, rebuild, now, t, &retry, logger)
		}
	}
}

// runAudioRestoreRetryTick is one poll of [runAudioRestoreRetry], split
// out so audiorestoreretry_test.go can drive it directly against a fake
// clock instead of a real ticker channel.
func runAudioRestoreRetryTick(
	mgr *audio.Manager,
	engineAvailable func() (bool, string),
	currentNode func() (audioNodeConfig, bool),
	rebuild func(audioNodeConfig) audioRebuildOutcome,
	now func() time.Time,
	tick time.Time,
	retry *audioRestoreRetryer,
	logger *slog.Logger,
) {
	if mgr.PendingRestoreCount() == 0 {
		if retry.attempts != 0 {
			retry.attempts = 0
			retry.nextAttemptAt = time.Time{}
			mgr.ClearRestoreRetryStatus()
		}
		return
	}
	if ok, _ := engineAvailable(); ok {
		// See runAudioRestoreRetry's own doc comment: a working engine
		// means whatever is keeping this session pending is not a device
		// availability problem, and rebuilding here would only risk the
		// sessions that ARE working. Leave the standing fault as the
		// operator-visible signal instead of retrying.
		return
	}
	if retry.attempts >= len(audioRestoreRetryDelays) {
		// Bounded: automatic retries exhausted for this deferral. The
		// last SetRestoreRetryStatus call below already left the frozen
		// attempts/lastReason visible; a genuine audio.node.configure
		// delivery still resolves this at any time via
		// [audio.Manager.RebindEngine]'s own retryDeferredRestores.
		return
	}
	if !retry.nextAttemptAt.IsZero() && tick.Before(retry.nextAttemptAt) {
		return
	}
	node, haveNode := currentNode()
	if !haveNode {
		return
	}

	outcome := rebuild(node)
	retry.attempts++
	attemptsUsed := retry.attempts
	pending := mgr.PendingRestoreCount()
	if pending == 0 {
		retry.attempts = 0
		retry.nextAttemptAt = time.Time{}
		mgr.ClearRestoreRetryStatus()
		if logger != nil {
			logger.Info("automatic audio restore retry resolved every deferred restore", "attempt", attemptsUsed)
		}
		return
	}

	reason := outcome.Reason
	switch {
	case !outcome.Attempted:
		// The driver replays the same, already-accepted revision on
		// every attempt, so this only happens if a genuinely newer
		// binding raced in and won concurrently — not a build refusal,
		// but still worth surfacing rather than leaving the prior reason
		// stale.
		reason = "a newer audio.node binding was accepted concurrently with this automatic retry attempt"
	case reason == "":
		reason = "the engine rebuilt but a deferred restore still did not resolve"
	}
	delayIndex := retry.attempts - 1
	if delayIndex >= len(audioRestoreRetryDelays) {
		delayIndex = len(audioRestoreRetryDelays) - 1
	}
	retry.nextAttemptAt = now().Add(audioRestoreRetryDelays[delayIndex])
	mgr.SetRestoreRetryStatus(retry.attempts, retry.nextAttemptAt, reason)
	if logger != nil {
		logger.Warn("automatic audio restore retry attempt did not resolve every deferred restore",
			"attempt", retry.attempts, "pending", pending, "reason", reason, "next_attempt_at", retry.nextAttemptAt)
	}
}
