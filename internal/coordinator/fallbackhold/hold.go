// Package fallbackhold decides when the coordinator must leave an FPP
// player alone because that player's plugin is, or may be, running the show
// from its fallback program (ADR-048 decision 4). The plugin says which
// state it is in; this package keeps the latest report and answers one
// question for the loops that act on a show: is this player held?
//
// The wire is FPP-PLUGIN-COORDINATOR-CONTRACTS.md sections 5.15 and 5.16.
package fallbackhold

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// The three states a plugin reports.
const (
	StateNormal   = "normal"
	StateFallback = "fallback"
	StateResting  = "resting"
)

// ValidState reports whether s is one of the three reported states.
func ValidState(s string) bool {
	return s == StateNormal || s == StateFallback || s == StateResting
}

// SilentAfter is how old the latest report may be before the plugin counts
// as no longer reporting. A plugin reports every 10 seconds.
const SilentAfter = 45 * time.Second

// StartGrace is how long after the coordinator starts a player that has
// reported before stays held while its first report of this run is awaited.
const StartGrace = 45 * time.Second

// Reason says why a player is held.
type Reason string

const (
	ReasonNone Reason = ""
	// ReasonExecutor: the latest report says fallback or resting.
	ReasonExecutor Reason = "running-from-fallback"
	// ReasonAwaitingAcknowledgement: the plugin handed back and has not
	// acknowledged its current program yet.
	ReasonAwaitingAcknowledgement Reason = "waiting-for-acknowledgement"
	// ReasonCoordinatorStarting: the coordinator has just started and has
	// not heard from a plugin that reported before.
	ReasonCoordinatorStarting Reason = "coordinator-starting"
)

// Verdict is the answer for one FPP player at one moment.
type Verdict struct {
	// Reported is false when the player's plugin has never reported. Such
	// a player is never held.
	Reported bool
	Held     bool
	Reason   Reason
	// PluginReporting is false when a plugin that reported before has been
	// silent for longer than [SilentAfter].
	PluginReporting bool
	// AckWaitSince is set only while Reason is ReasonAwaitingAcknowledgement.
	AckWaitSince time.Time
	// IgnoreObservationsThrough is the receive time at or before which a
	// playlist-entry observation from this player is never acted on.
	IgnoreObservationsThrough time.Time
	Record                    store.FallbackPlayerStateRecord
}

// IgnoresObservation reports whether an observation received at receivedAt
// must not be acted on under v.
func (v Verdict) IgnoresObservation(receivedAt time.Time) bool {
	return v.Held || (!v.IgnoreObservationsThrough.IsZero() && !receivedAt.After(v.IgnoreObservationsThrough))
}

// inputs is everything [decide] reads besides the report itself.
type inputs struct {
	// observationAt is when the player's latest playlist-entry observation
	// arrived, zero when there is none.
	observationAt time.Time
	// ackCurrent is true when the player has acknowledged the program
	// published for it, or nothing is published for it.
	ackCurrent bool
	startedAt  time.Time
	now        time.Time
}

// decide applies contract section 5.16 to one stored report.
func decide(rec store.FallbackPlayerStateRecord, in inputs) Verdict {
	v := Verdict{
		Reported: true, Record: rec,
		PluginReporting:           in.now.Sub(rec.ReceivedAt) <= SilentAfter,
		IgnoreObservationsThrough: rec.ObservationsIgnoredThrough,
	}
	// A plugin that sends observations and no reports is one that does not
	// report at all, so its last report no longer describes it.
	if !in.observationAt.IsZero() && in.observationAt.After(rec.ReceivedAt.Add(SilentAfter)) {
		v.PluginReporting = false
		return v
	}
	switch {
	case rec.State != StateNormal:
		v.Held, v.Reason = true, ReasonExecutor
	case !rec.AckWaitSince.IsZero() && !in.ackCurrent:
		v.Held, v.Reason, v.AckWaitSince = true, ReasonAwaitingAcknowledgement, rec.AckWaitSince
	case rec.ReceivedAt.Before(in.startedAt) && in.now.Sub(in.startedAt) <= StartGrace:
		v.Held, v.Reason = true, ReasonCoordinatorStarting
	}
	return v
}

// Report is one accepted state report, already validated by its route.
type Report struct {
	FPPInstanceUUID string
	BootID          string
	Sequence        int64
	State           string
	Since           time.Time
	PlaylistName    string
	PackageID       string
	PackageRevision string
	CutoffAt        string
}

// RecordResult says what [Service.Record] did with a report.
type RecordResult struct {
	// Recorded is false when a report with the same boot id and an equal
	// or higher sequence was already held and kept.
	Recorded bool
	Stored   store.FallbackPlayerStateRecord
	// StateChanged is true for a first report and for a different state.
	StateChanged  bool
	PreviousState string
}

// Service keeps the latest report per player and answers [Service.Evaluate].
type Service struct {
	st        *store.Store
	startedAt time.Time
	logger    *slog.Logger
	now       func() time.Time
}

// NewService builds a Service. startedAt is when this coordinator process
// started, which is what [StartGrace] counts from.
func NewService(st *store.Store, startedAt time.Time, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{st: st, startedAt: startedAt, logger: logger, now: time.Now}
}

// reader is the read surface [store.Store] and [store.Tx] share.
type reader interface {
	GetFPPPlaylistEntryObservation(ctx context.Context, instanceUUID string) (store.FPPPlaylistEntryObservationRecord, error)
	GetFallbackProgram(ctx context.Context, instanceUUID string) (store.FallbackProgramRecord, error)
	GetFallbackProgramAck(ctx context.Context, instanceUUID string) (store.FallbackProgramAckRecord, error)
}

func (s *Service) readInputs(ctx context.Context, r reader, instanceUUID string, now time.Time) (inputs, error) {
	in := inputs{startedAt: s.startedAt, now: now}
	obs, err := r.GetFPPPlaylistEntryObservation(ctx, instanceUUID)
	switch {
	case err == nil:
		in.observationAt = obs.ReceivedAt
	case !errors.Is(err, store.ErrFPPPlaylistEntryObservationNotFound):
		return inputs{}, fmt.Errorf("fallbackhold: read latest observation for %q: %w", instanceUUID, err)
	}
	program, err := r.GetFallbackProgram(ctx, instanceUUID)
	if errors.Is(err, store.ErrFallbackProgramNotFound) {
		in.ackCurrent = true
		return in, nil
	}
	if err != nil {
		return inputs{}, fmt.Errorf("fallbackhold: read published program for %q: %w", instanceUUID, err)
	}
	ack, err := r.GetFallbackProgramAck(ctx, instanceUUID)
	switch {
	case errors.Is(err, store.ErrFallbackProgramAckNotFound):
	case err != nil:
		return inputs{}, fmt.Errorf("fallbackhold: read program acknowledgement for %q: %w", instanceUUID, err)
	default:
		in.ackCurrent = ack.VerificationResult == v1.FallbackProgramVerificationVerified &&
			ack.PackageID == program.PackageID && ack.Revision == program.Revision
	}
	return in, nil
}

// Evaluate answers whether the player is held at now. A player whose
// plugin never reported gets the zero Verdict.
func (s *Service) Evaluate(ctx context.Context, instanceUUID string, now time.Time) (Verdict, error) {
	rec, err := s.st.GetFallbackPlayerState(ctx, instanceUUID)
	if errors.Is(err, store.ErrFallbackPlayerStateNotFound) {
		return Verdict{}, nil
	}
	if err != nil {
		return Verdict{}, fmt.Errorf("fallbackhold: read player state for %q: %w", instanceUUID, err)
	}
	return s.evaluateRecord(ctx, rec, now)
}

func (s *Service) evaluateRecord(ctx context.Context, rec store.FallbackPlayerStateRecord, now time.Time) (Verdict, error) {
	in, err := s.readInputs(ctx, s.st, rec.FPPInstanceUUID, now)
	if err != nil {
		return Verdict{}, err
	}
	v := decide(rec, in)
	if rec.State == StateNormal && !rec.AckWaitSince.IsZero() && in.ackCurrent {
		// The wait is over for good: a later program change must not
		// bring the hold back.
		if err := s.st.ClearFallbackPlayerStateAckWait(ctx, rec.FPPInstanceUUID, rec.AckWaitSince); err != nil {
			s.logger.Warn("fallback hold: could not record that the program acknowledgement arrived",
				"fppInstanceUuid", rec.FPPInstanceUUID, "error", err)
		}
	}
	return v, nil
}

// All returns the verdict for every player whose plugin has reported.
func (s *Service) All(ctx context.Context, now time.Time) ([]Verdict, error) {
	recs, err := s.st.ListFallbackPlayerStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fallbackhold: list player states: %w", err)
	}
	out := make([]Verdict, 0, len(recs))
	for _, rec := range recs {
		v, err := s.evaluateRecord(ctx, rec, now)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// AnyHeld reports whether any player is held, and names the first one.
func (s *Service) AnyHeld(ctx context.Context, now time.Time) (held bool, instanceUUID string, err error) {
	all, err := s.All(ctx, now)
	if err != nil {
		return false, "", err
	}
	for _, v := range all {
		if v.Held {
			return true, v.Record.FPPInstanceUUID, nil
		}
	}
	return false, "", nil
}

// Record stores r as the player's latest report unless an equal or newer
// one from the same plugin start is already held.
func (s *Service) Record(ctx context.Context, r Report, now time.Time) (RecordResult, error) {
	var result RecordResult
	err := s.st.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		prev, err := tx.GetFallbackPlayerState(ctx, r.FPPInstanceUUID)
		found := err == nil
		if err != nil && !errors.Is(err, store.ErrFallbackPlayerStateNotFound) {
			return err
		}
		if found && prev.BootID == r.BootID && r.Sequence <= prev.Sequence {
			result = RecordResult{Stored: prev, PreviousState: prev.State}
			return nil
		}
		rec := store.FallbackPlayerStateRecord{
			FPPInstanceUUID: r.FPPInstanceUUID, BootID: r.BootID, Sequence: r.Sequence, State: r.State, Since: r.Since,
			PlaylistName: r.PlaylistName, PackageID: r.PackageID, PackageRevision: r.PackageRevision, CutoffAt: r.CutoffAt,
			ReceivedAt: now, StateChangedAt: now,
		}
		var wasHeld Verdict
		if found {
			in, err := s.readInputs(ctx, tx, r.FPPInstanceUUID, now)
			if err != nil {
				return err
			}
			wasHeld = decide(prev, in)
			rec.ObservationsIgnoredThrough, rec.AckWaitSince = prev.ObservationsIgnoredThrough, prev.AckWaitSince
			if prev.State == r.State {
				rec.StateChangedAt = prev.StateChangedAt
			}
		}
		switch {
		case r.State != StateNormal:
			rec.ObservationsIgnoredThrough, rec.AckWaitSince = now, time.Time{}
		case found && prev.State != StateNormal:
			// The hand-back: nothing the player did before it is replayed,
			// and the program acknowledgement is now awaited.
			rec.ObservationsIgnoredThrough, rec.AckWaitSince = now, now
		case wasHeld.Held && wasHeld.Reason == ReasonCoordinatorStarting:
			rec.ObservationsIgnoredThrough = now
		}
		if err := tx.PutFallbackPlayerState(ctx, rec); err != nil {
			return err
		}
		result = RecordResult{Recorded: true, Stored: rec, StateChanged: !found || prev.State != r.State}
		if found {
			result.PreviousState = prev.State
		}
		return nil
	})
	if err != nil {
		return RecordResult{}, fmt.Errorf("fallbackhold: record report for %q: %w", r.FPPInstanceUUID, err)
	}
	return result, nil
}
