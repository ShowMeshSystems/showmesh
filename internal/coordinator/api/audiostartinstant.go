package api

import (
	"context"

	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
)

// This file is ADR-049 decision 1's extraction: alignedstart.go's own
// per-node readiness-from-evidence-then-audiosched.Select shape, pulled
// out so a second caller (ADR-049 decision 3's Cue scheduling step,
// cueactivationschedule.go) can choose one shared start instant without
// duplicating it. alignedstart.go's own handler is left calling its
// original inline loop unchanged — its own external behavior, including
// aborting the whole request on one node's problem, must stay exactly as
// it is (that abort is wrong for a Cue: one node's problem must never
// stop the others, ADR-049 decision 4) — so this file is additive only,
// never a refactor of that handler.

// AudioStartInstantReading is one node's own fresh evidence for
// [SelectAudioStartInstant]: [readinessFromEvidence]'s own input shape,
// obtained for THIS selection, never a retained observation, for the
// identical reason [audiosched.Readiness]'s own doc comment states.
type AudioStartInstantReading struct {
	// HoldsMediaClock is true for the node carrying the program plus LTC
	// role — see [handlers.nodeHoldsMediaClock].
	HoldsMediaClock bool

	// Evidence is the node's own audio.session.prepare result evidence
	// map (pkg/audio's Result* fields), or nil when no evidence was
	// obtained at all (a dispatch that itself failed, rather than one
	// that succeeded and reported an invalid clock).
	Evidence map[string]any
}

// ReadAudioStartInstantNode obtains nodeID's own fresh
// [AudioStartInstantReading] for [SelectAudioStartInstant]. Kept free of
// Cue-specific types deliberately: a night-mode caller (a background bed
// or announcement reaching several nodes) supplies its own reader against
// this identical signature, never a Cue-shaped one.
type ReadAudioStartInstantNode func(ctx context.Context, nodeID string) (AudioStartInstantReading, error)

// SelectAudioStartInstant chooses the one start instant every node in
// nodeIDs is started at, from a FRESH reading obtained via read for each
// — read is called for every node IN PARALLEL, since ADR-049 decision 3
// requires this selection reach the same instant regardless of how many
// nodes it fans out to, and a slow node's own read must not delay
// another's. It returns whatever [audiosched.Select] returns, including
// *[audiosched.ErrNoUsableClock] when no target holds a usable reading —
// never a fabricated instant.
//
// A non-nil error from read for one node is not fatal to the others: that
// node's own reading is recorded as absent evidence (readinessFromEvidence
// already treats absent evidence as an invalid clock with a reason), and
// [audiosched.Select] still runs against every other node's own reading.
// Only the node actually HOLDING the media clock can make the whole
// selection fail this way — a non-holder's failed read costs that node's
// own preroll contribution, nothing more.
func SelectAudioStartInstant(ctx context.Context, nodeIDs []string, settings config.AudioSettingsPayload, read ReadAudioStartInstantNode) (audiosched.Selection, error) {
	readings := make([]audiosched.Readiness, len(nodeIDs))
	type result struct {
		reading AudioStartInstantReading
		err     error
	}
	results := make([]result, len(nodeIDs))
	done := make(chan int, len(nodeIDs))
	for i, nodeID := range nodeIDs {
		go func(i int, nodeID string) {
			reading, err := read(ctx, nodeID)
			results[i] = result{reading: reading, err: err}
			done <- i
		}(i, nodeID)
	}
	for range nodeIDs {
		<-done
	}
	for i, nodeID := range nodeIDs {
		r := results[i]
		evidence := r.reading.Evidence
		holdsClock := r.reading.HoldsMediaClock
		if r.err != nil {
			evidence = nil
		}
		readings[i] = readinessFromEvidence(nodeID, holdsClock, evidence)
	}
	return audiosched.Select(readings, settings.ScheduledStartDeliveryBoundMs, settings.ScheduledStartMarginMs)
}
