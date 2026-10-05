package fallbackhold

import (
	"context"
	"time"

	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// ObservationSource names this package on the observations it writes. It
// is a direct source, not a collector.Runner entry.
const ObservationSource = "fallback-player-state"

// The signals an operator reads, all on the FPP player's fallback program.
const (
	SignalPlayerState            observation.SignalID = "fallback_program.player_state"
	SignalCoordinatorHolding     observation.SignalID = "fallback_program.coordinator_holding"
	SignalPluginReporting        observation.SignalID = "fallback_program.plugin_reporting"
	SignalAcknowledgementWaitSec observation.SignalID = "fallback_program.acknowledgement_wait_seconds"
)

// SignalInterval is how often [Service.Run] rewrites the signals.
const SignalInterval = 5 * time.Second

// signalValidFor lets a signal survive two missed passes.
const signalValidFor = 3 * SignalInterval

// Message is the one or two sentences an operator reads for v, empty when
// there is nothing to say.
func Message(v Verdict) string {
	switch {
	case !v.Reported:
		return ""
	case v.Held && v.Reason == ReasonExecutor && v.Record.State == StateResting:
		return "This player's fallback program reached its end time. Cues already started keep playing, and no new Cue starts until the playlist ends."
	case v.Held && v.Reason == ReasonExecutor:
		return "This player is running the show from its fallback program. The coordinator starts no Cues for it until the playlist ends."
	case v.Held && v.Reason == ReasonAwaitingAcknowledgement:
		return "This player has finished running from its fallback program and has not confirmed its current program yet. The coordinator starts no Cues for it until it does."
	case v.Held && v.Reason == ReasonCoordinatorStarting:
		return "The coordinator has just started and has not heard from this player's plugin yet. It starts no Cues for this player until the plugin reports, for up to 45 seconds."
	case !v.PluginReporting:
		return "This player's plugin has stopped reporting. Check the plugin on the FPP player."
	}
	return ""
}

// Run rewrites every reporting player's signals until ctx is done.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(SignalInterval)
	defer ticker.Stop()
	for {
		s.WriteSignals(ctx, s.now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// WriteSignals writes the four signals for every player that has reported.
func (s *Service) WriteSignals(ctx context.Context, now time.Time) {
	all, err := s.All(ctx, now)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Warn("fallback hold: reading player states for the signals failed", "error", err)
		}
		return
	}
	for _, v := range all {
		s.writeVerdictSignals(ctx, v, now)
	}
}

func (s *Service) writeVerdictSignals(ctx context.Context, v Verdict, now time.Time) {
	res := observation.ResourceRef{Kind: observation.ResourceFallbackProgram, ID: v.Record.FPPInstanceUUID}
	opts := []observation.Option{
		observation.WithSource(ObservationSource), observation.WithCollectedAt(now), observation.WithValidFor(signalValidFor),
	}
	var waited float64
	if !v.AckWaitSince.IsZero() {
		waited = now.Sub(v.AckWaitSince).Seconds()
	}
	// The reported state is as old as the report, so it goes stale by
	// itself when the plugin stops reporting.
	s.upsert(ctx, res, SignalPlayerState, v.Record.State, v.Record.ReceivedAt,
		observation.WithSource(ObservationSource), observation.WithCollectedAt(now), observation.WithValidFor(SilentAfter))
	s.upsert(ctx, res, SignalCoordinatorHolding, v.Held, now, opts...)
	s.upsert(ctx, res, SignalPluginReporting, v.PluginReporting, now, opts...)
	s.upsert(ctx, res, SignalAcknowledgementWaitSec, waited, now, append(opts, observation.WithUnit("s"))...)
}

func (s *Service) upsert(ctx context.Context, res observation.ResourceRef, sig observation.SignalID, value any, observedAt time.Time, opts ...observation.Option) {
	obs, err := observation.Measured(res, sig, value, observedAt, opts...)
	if err == nil {
		err = s.st.UpsertObservation(ctx, obs)
	}
	if err != nil && ctx.Err() == nil {
		s.logger.Warn("fallback hold: writing a player state signal failed", "signal", string(sig), "error", err)
	}
}

// ForgetSignals marks a cleared player's signals as no longer collected.
func (s *Service) ForgetSignals(ctx context.Context, instanceUUID string, now time.Time) {
	res := observation.ResourceRef{Kind: observation.ResourceFallbackProgram, ID: instanceUUID}
	for _, sig := range []observation.SignalID{SignalPlayerState, SignalCoordinatorHolding, SignalPluginReporting, SignalAcknowledgementWaitSec} {
		obs, err := observation.NotCollected(res, sig, "This player's stored fallback state was cleared. It shows again when the plugin next reports.",
			observation.WithSource(ObservationSource), observation.WithCollectedAt(now))
		if err == nil {
			err = s.st.UpsertObservation(ctx, obs)
		}
		if err != nil {
			s.logger.Warn("fallback hold: clearing a player state signal failed", "signal", string(sig), "error", err)
		}
	}
}
