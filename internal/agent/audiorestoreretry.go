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
	lastReason    string
	// exhausted is true once attempts has reached the bounded schedule's
	// length and that fact has been reported (mgr.SetRestoreRetryStatus
	// called with a zero nextAttemptAt, and the one-time log line
	// emitted) — see runAudioRestoreRetryTick's own doc comment on why
	// this must be reported explicitly rather than left for
	// next_attempt_ms to keep counting down to an attempt that will
	// never happen.
	exhausted bool
}

// runAudioRestoreRetry is this node's own automatic half of the retry
// machinery [audio.Manager.RebindEngine] already provides: RebindEngine
// resolves every deferred restore the moment a NEW audio.node binding
// arrives, but nothing before this drove that resolution on its own: an
// interface that enumerates late, with no coordinator reconnect and no
// newer audio.node revision, left the session pending indefinitely, or
// left a bound engine reporting unavailable with zero pending restores
// to even signal it (a delivered binding with no persisted session):
// this loop's whole reason to exist.
//
// On a bounded, backed-off schedule, whenever [audio.Manager.
// PendingRestoreCount] is nonzero OR engineAvailable reports false, this
// calls rebuild against the SAME, already-accepted audio.node binding
// currentNode reports. [audioEngineRebuilder.rebuildIfUnavailable]
// re-probes the device fresh on every call ([buildGstEngineConfig] runs
// discovery inline) and atomically declines to act at all when the bound
// engine already reports available. See that method's own doc comment
// for why the availability check must be atomic with the decision to
// act, not a separate read this driver takes on its own: a genuine
// audio.node.configure delivery finishing in the gap between a stale
// read and this driver's own rebuild call would otherwise get its own
// working engine torn back down for no benefit to the session this
// driver is trying to help. A skipped attempt (outcome.Skipped) counts
// against nothing: no attempt increment, no backoff advance, no status
// change, exactly as if this tick had found nothing to do.
//
// engineAvailable only gates whether to attempt at all. rebuildIfUnavailable
// still rechecks under its own lock, so this separate read is not the
// TOCTOU guard: a stale answer either way costs at most one no-op skip.
//
// currentNode reports the most recently accepted audio.node binding —
// nothing to retry against until the first one lands, so a false ok
// leaves this driver idle rather than probing with a zero-value config.
func runAudioRestoreRetry(
	ctx context.Context,
	mgr *audio.Manager,
	currentNode func() (audioNodeConfig, bool),
	rebuild func(audioNodeConfig) audioRebuildOutcome,
	engineAvailable func() (bool, string),
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
			runAudioRestoreRetryTick(mgr, currentNode, rebuild, engineAvailable, now, t, &retry, logger)
		}
	}
}

// runAudioRestoreRetryTick is one poll of [runAudioRestoreRetry], split
// out so audiorestoreretry_test.go can drive it directly against a fake
// clock instead of a real ticker channel.
//
// Two independent reasons keep this driver active: a deferred restore
// pending, or the bound engine unavailable with nothing pending at all.
// Both must clear together for the reset below to fire.
//
// Exhaustion clears nextAttemptAt to zero and pushes that to
// [audio.Manager.SetRestoreRetryStatus], so
// audio_session.restore.next_attempt_ms stops counting down to an
// attempt that will never happen. retry.exhausted guards the one-time
// log line so a node stuck for hours does not spam it every poll.
//
// [audio.Manager.SetRestoreRetryStatus]'s own status write reaches only
// sessions with a pending restore, so every call site here also calls
// [audio.Manager.SetNodeRestoreRetryStatus]/
// [audio.Manager.ClearNodeRestoreRetryStatus]: a node with no pending
// session at all (a delivered audio.node binding, zero persisted
// sessions) still gets node.audio.engine.restore.state/.attempts/
// .next_attempt_ms/.last_reason on its own published report, whether
// scheduled or exhausted.
func runAudioRestoreRetryTick(
	mgr *audio.Manager,
	currentNode func() (audioNodeConfig, bool),
	rebuild func(audioNodeConfig) audioRebuildOutcome,
	engineAvailable func() (bool, string),
	now func() time.Time,
	tick time.Time,
	retry *audioRestoreRetryer,
	logger *slog.Logger,
) {
	pending := mgr.PendingRestoreCount()
	available, _ := engineAvailable()
	if pending == 0 && available {
		if retry.attempts != 0 || retry.exhausted {
			*retry = audioRestoreRetryer{}
			mgr.ClearRestoreRetryStatus()
			mgr.ClearNodeRestoreRetryStatus()
		}
		return
	}
	if retry.attempts >= len(audioRestoreRetryDelays) {
		// Already reported as exhausted by the attempt that reached the
		// bound, below. Nothing left to do until both conditions above
		// clear or a genuine binding delivery resolves this outside this
		// driver entirely.
		return
	}
	if !retry.nextAttemptAt.IsZero() && tick.Before(retry.nextAttemptAt) {
		return
	}
	node, haveNode := currentNode()
	if !haveNode {
		// No binding yet: an unbound engine reports unavailable, which
		// would otherwise trip the widened trigger on every idle node.
		return
	}

	outcome := rebuild(node)
	if outcome.Skipped {
		// The bound engine already reports available — see this
		// function's own doc comment. Not a device problem this driver
		// can help with; do not count it against the bounded schedule or
		// advance the backoff clock.
		return
	}
	retry.attempts++
	attemptsUsed := retry.attempts
	pending = mgr.PendingRestoreCount()
	available, _ = engineAvailable()
	if pending == 0 && available {
		*retry = audioRestoreRetryer{}
		mgr.ClearRestoreRetryStatus()
		mgr.ClearNodeRestoreRetryStatus()
		if logger != nil {
			logger.Info("automatic audio restore retry resolved: no pending restore and the audio engine is available", "attempt", attemptsUsed)
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
		reason = "the engine rebuilt but automatic retry still did not fully resolve"
	}
	retry.lastReason = reason

	if retry.attempts >= len(audioRestoreRetryDelays) {
		retry.exhausted = true
		retry.nextAttemptAt = time.Time{}
		mgr.SetRestoreRetryStatus(retry.attempts, time.Time{}, reason)
		mgr.SetNodeRestoreRetryStatus(audio.EngineRestoreExhausted, retry.attempts, time.Time{}, reason)
		if logger != nil {
			logger.Warn("automatic audio restore retry exhausted its bounded schedule; no further automatic attempts will run",
				"attempts", retry.attempts, "pending", pending, "engine_available", available, "reason", reason)
		}
		return
	}

	delayIndex := retry.attempts - 1
	retry.nextAttemptAt = now().Add(audioRestoreRetryDelays[delayIndex])
	mgr.SetRestoreRetryStatus(retry.attempts, retry.nextAttemptAt, reason)
	mgr.SetNodeRestoreRetryStatus(audio.EngineRestoreScheduled, retry.attempts, retry.nextAttemptAt, reason)
	if logger != nil {
		logger.Warn("automatic audio restore retry attempt did not fully resolve",
			"attempt", retry.attempts, "pending", pending, "engine_available", available, "reason", reason, "next_attempt_at", retry.nextAttemptAt)
	}
}
