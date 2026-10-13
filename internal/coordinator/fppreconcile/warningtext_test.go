package fppreconcile

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// warningText is every warning on one report, one per line, for tests that
// assert a fact appears somewhere among them.
func warningText(r Report) string { return strings.Join(r.Warnings, "\n") }

// joinedClockReadiness is audioTargetClockReadiness with its warnings on one line each.
func joinedClockReadiness(ctx context.Context, st *store.Store, logger *slog.Logger, clock ClockObservationsLister, now time.Time, p config.ShowPlaylistPayload) (string, error) {
	warnings, err := audioTargetClockReadiness(ctx, st, logger, clock, now, p)
	return strings.Join(warnings, "\n"), err
}

// joinedAudioTargetReadiness is audioTargetReadiness with its warnings on one line each.
func joinedAudioTargetReadiness(ctx context.Context, st *store.Store, logger *slog.Logger, p config.ShowPlaylistPayload) (ReadinessCondition, string, string, error) {
	cond, reason, warnings, err := audioTargetReadiness(ctx, st, logger, p)
	return cond, reason, strings.Join(warnings, "\n"), err
}
