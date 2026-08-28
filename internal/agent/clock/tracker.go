package clock

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// TrackerConfig is the operator-declared context [Tracker] needs to turn a
// raw provider reading into RES-019 section 9's state machine: the
// declared domain/grandmaster a "locked" reading is checked against for
// [Status.Mismatch], and the holdover limit a holdover episode ages
// against before becoming StateUnsynchronized.
type TrackerConfig struct {
	Domain         int
	DomainDeclared bool

	GrandmasterIdentity string
	GMDeclared          bool

	// HoldoverLimit bounds how long StateHoldover is reported before
	// [Tracker] gives up and reports StateUnsynchronized instead — RES-019
	// section 9: "after a configured holdover limit becomes
	// unsynchronized". Zero is treated as HoldoverLimitUnset's default by
	// [NewTracker].
	HoldoverLimit time.Duration
}

// HoldoverLimitDefault is used when [TrackerConfig.HoldoverLimit] is zero.
// SHOWMESH HYPOTHESIS, NOT MEASURED: no bench data exists yet for how long
// a real show's timing tolerance survives an un-synchronized holdover;
// this is a conservative starting point an operator can override per node
// (node.clock's own holdoverLimitSeconds field), not a claim about
// audible drift.
const HoldoverLimitDefault = 60 * time.Second

// Tracker wraps a [Provider] and turns its per-poll [RawStatus] into a
// fully-derived [Status], holding exactly the bookkeeping RES-019 section
// 9's transitions need (when the current lock/holdover episode began, the
// last detected step) — see this package's own doc comment for why this
// logic lives in one place shared by all three providers rather than
// duplicated per implementation. The zero value is not usable; construct
// with [NewTracker].
type Tracker struct {
	provider Provider
	cfg      TrackerConfig
	now      func() time.Time

	mu sync.Mutex

	state  State
	reason string

	// lockedSince is when the CURRENT lock episode began — reset on
	// every transition into StateLocked from anything else, and on every
	// detected step (a grandmaster change starts a new episode: RES-019
	// section 9 counts it as a step, and this package treats a stepped
	// lock as a fresh one for LockedSeconds' own purpose, since the prior
	// episode's evidence no longer describes the clock now in effect).
	lockedSince      time.Time
	lockedSinceKnown bool

	// holdoverSince is when the current holdover episode began — set on
	// the first poll after a lock is lost, cleared on recovery.
	holdoverSince      time.Time
	holdoverSinceKnown bool

	lastGM      string
	lastGMKnown bool

	lastStepAt    time.Time
	lastStepNs    int64
	lastStepKnown bool
}

// NewTracker builds a Tracker polling p, starting in [StateAcquiring]
// (RES-019 section 9: "startup before lock is acquiring") — matches
// [Provider]'s own contract that a Provider never claims a lock before
// its underlying source proves one. now defaults to [time.Now] when nil.
func NewTracker(p Provider, cfg TrackerConfig, now func() time.Time) *Tracker {
	if now == nil {
		now = time.Now
	}
	if cfg.HoldoverLimit <= 0 {
		cfg.HoldoverLimit = HoldoverLimitDefault
	}
	return &Tracker{
		provider: p,
		cfg:      cfg,
		now:      now,
		state:    StateAcquiring,
		reason:   "startup: this node's clock provider has not yet reported a lock",
	}
}

// Poll asks the wrapped [Provider] for one raw reading and applies
// RES-019 section 9's transition rules, returning the resulting [Status].
// Safe for concurrent use (a config-reload rebuild and a report-loop tick
// never race against each other's own state).
func (t *Tracker) Poll(ctx context.Context) Status {
	raw := t.provider.Poll(ctx)
	now := t.now()

	t.mu.Lock()
	defer t.mu.Unlock()

	switch {
	case !raw.Reachable:
		t.transitionFailed(raw.Reason)
	case raw.Locked:
		t.transitionLocked(raw, now)
	default:
		t.transitionUnlocked(raw, now)
	}

	return t.buildStatus(raw, now)
}

// transitionFailed handles RES-019 section 9's immediate-failure case:
// interface/link loss, or the owning ptp4l gone. Any lock/holdover episode
// in progress ends; recovery (the next Reachable poll) re-enters via
// StateAcquiring, matching "failed then acquiring".
func (t *Tracker) transitionFailed(reason string) {
	if reason == "" {
		reason = "provider reports this node's clock source is unreachable"
	}
	t.state = StateFailed
	t.reason = reason
	t.lockedSinceKnown = false
	t.holdoverSinceKnown = false
}

// transitionLocked handles a raw reading claiming a usable lock this poll.
// A grandmaster change is RES-019 section 9's own "a grandmaster change is
// a step": lockedSince resets (the prior episode's evidence no longer
// describes the clock now in effect) and the step is recorded, in
// addition to the ordinary recovery-into-locked reset below.
func (t *Tracker) transitionLocked(raw RawStatus, now time.Time) {
	steppedGM := raw.GMKnown && t.lastGMKnown && raw.GrandmasterIdentity != t.lastGM
	enteringLock := t.state != StateLocked

	if steppedGM {
		var magnitude int64
		if raw.OffsetKnown {
			magnitude = raw.OffsetNs
		}
		t.lastStepAt = now
		t.lastStepNs = magnitude
		t.lastStepKnown = true
	}

	if enteringLock || steppedGM {
		t.lockedSince = now
		t.lockedSinceKnown = true
	}

	t.state = StateLocked
	t.reason = ""
	t.holdoverSinceKnown = false

	if raw.GMKnown {
		t.lastGM = raw.GrandmasterIdentity
		t.lastGMKnown = true
	}
}

// transitionUnlocked handles a raw reading that is reachable but not
// currently locked: startup (StateAcquiring stays put), or a lock just
// lost (enter StateHoldover), or a holdover episode ageing past
// [TrackerConfig.HoldoverLimit] into StateUnsynchronized.
func (t *Tracker) transitionUnlocked(raw RawStatus, now time.Time) {
	reason := raw.Reason
	if reason == "" {
		reason = "provider reports this node's clock is not currently locked"
	}

	switch t.state {
	case StateLocked:
		t.state = StateHoldover
		t.reason = "lock lost; holding over on the last known-good clock"
		t.holdoverSince = now
		t.holdoverSinceKnown = true
	case StateHoldover:
		if now.Sub(t.holdoverSince) > t.cfg.HoldoverLimit {
			t.state = StateUnsynchronized
			t.reason = fmt.Sprintf("holdover exceeded its %s limit with no recovery", t.cfg.HoldoverLimit)
			t.holdoverSinceKnown = false
			t.lockedSinceKnown = false
		}
		// else: still within the holdover window, stay in StateHoldover.
	case StateFailed:
		t.state = StateAcquiring
		t.reason = "recovered from failed; not yet locked"
	default: // StateAcquiring, StateUnsynchronized
		t.state = StateAcquiring
		t.reason = reason
	}
}

func (t *Tracker) buildStatus(raw RawStatus, now time.Time) Status {
	s := Status{
		State:               t.state,
		Reason:              t.reason,
		Role:                raw.Role,
		RoleKnown:           raw.RoleKnown,
		Provider:            t.provider.Kind(),
		Owner:               raw.Owner,
		Interface:           t.provider.Interface(),
		Domain:              raw.Domain,
		DomainKnown:         raw.DomainKnown,
		GrandmasterIdentity: raw.GrandmasterIdentity,
		GMKnown:             raw.GMKnown,
		Timescale:           raw.Timescale,
		OffsetNs:            raw.OffsetNs,
		OffsetKnown:         raw.OffsetKnown && t.state == StateLocked,
		ClockClass:          raw.ClockClass,
		ClockClassKnown:     raw.ClockClassKnown,
		Timestamping:        raw.Timestamping,
		TimestampingKnown:   raw.TimestampingKnown,
		ObservedAt:          now,
	}

	if t.lockedSinceKnown && (t.state == StateLocked || t.state == StateHoldover) {
		s.LockedSeconds = int64(now.Sub(t.lockedSince).Seconds())
		s.LockedSecondsKnown = true
	}
	if t.holdoverSinceKnown && t.state == StateHoldover {
		s.HoldoverAge = now.Sub(t.holdoverSince)
		s.HoldoverAgeKnown = true
	}
	if t.lastStepKnown {
		s.LastStepAt = t.lastStepAt
		s.LastStepNs = t.lastStepNs
		s.LastStepKnown = true
	}

	if t.state == StateLocked {
		if mismatch, reason := t.mismatch(raw); mismatch {
			s.Mismatch = true
			s.MismatchReason = reason
		}
	}

	return s
}

// mismatch reports RES-019 section 9's "locked, but not to the declared
// domain or grandmaster": operator-visible, no automatic action. Checked
// only while locked (see [Tracker.buildStatus]) — an un-locked reading
// has no domain/grandmaster claim to compare.
func (t *Tracker) mismatch(raw RawStatus) (bool, string) {
	if t.cfg.DomainDeclared && raw.DomainKnown && raw.Domain != t.cfg.Domain {
		return true, fmt.Sprintf("locked to domain %d, declared domain is %d", raw.Domain, t.cfg.Domain)
	}
	if t.cfg.GMDeclared && raw.GMKnown && raw.GrandmasterIdentity != t.cfg.GrandmasterIdentity {
		return true, fmt.Sprintf("locked to grandmaster %s, declared grandmaster is %s", raw.GrandmasterIdentity, t.cfg.GrandmasterIdentity)
	}
	return false, ""
}

// Now delegates to the wrapped [Provider] unchanged — media time reading
// carries no state-machine derivation of its own (RES-019: it is the PHC
// read's own validity flag and error bound that answer "can this be
// trusted", not the PTP status Tracker derives).
func (t *Tracker) Now(ctx context.Context) MediaTime {
	return t.provider.Now(ctx)
}

// Close releases the wrapped [Provider].
func (t *Tracker) Close() error {
	return t.provider.Close()
}
