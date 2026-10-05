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
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// The three states a plugin reports.
const (
	StateNormal   = "normal"
	StateFallback = "fallback"
	StateResting  = "resting"
)

// stateCleared marks a row an operator cleared. It is never on the wire:
// such a player reads as never reported, and keeps its observation floor.
const stateCleared = "cleared"

// ValidState reports whether s is one of the three reported states.
func ValidState(s string) bool {
	return s == StateNormal || s == StateFallback || s == StateResting
}

// SilentAfter is how old the latest report may be before the plugin counts
// as no longer reporting. A plugin reports every 10 seconds.
const SilentAfter = 45 * time.Second

// StartGrace is how long after the coordinator's loops and listener are up
// a player that has reported before stays held while its first report of
// this run is awaited.
const StartGrace = 45 * time.Second

// The coordinator's own reading of an FPP player, from its FPP collector.
const (
	fppStatusSignal       observation.SignalID = "fpp.status"
	fppPlaylistNameSignal observation.SignalID = "fpp.playlist.name"
	fppStatusPlaying                           = "playing"
)

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
	// EndedWithoutPlugin is true when the latest report still says the
	// plugin runs the show, the plugin is silent, and the coordinator's
	// own reading of the player shows that playlist is no longer playing.
	EndedWithoutPlugin bool
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
	// playlistOverAt is when the coordinator's own FPP collector read
	// the player as no longer playing the reported playlist, zero when it
	// has no such reading from after the report.
	playlistOverAt time.Time
	startedAt      time.Time
	now            time.Time
}

// decide applies contract section 5.16 to one stored report.
func decide(rec store.FallbackPlayerStateRecord, in inputs) Verdict {
	if rec.State == stateCleared {
		return Verdict{IgnoreObservationsThrough: rec.ObservationsIgnoredThrough}
	}
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
	case rec.State != StateNormal && !v.PluginReporting && !in.playlistOverAt.IsZero():
		// The show the hold protected is over, seen by the coordinator.
		v.EndedWithoutPlugin = true
	case rec.State != StateNormal:
		v.Held, v.Reason = true, ReasonExecutor
	case !rec.AckWaitSince.IsZero() && !in.ackCurrent && v.PluginReporting:
		// A silent plugin cannot be waited on.
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
	st     *store.Store
	logger *slog.Logger
	now    func() time.Time

	mu        sync.Mutex
	startedAt time.Time
	// endedLogged remembers the report each hold that ended without its
	// plugin was announced for, so it is announced once.
	endedLogged map[string]string
}

// NewService builds a Service. startedAt is what [StartGrace] counts from
// until [Service.MarkStarted] moves it.
func NewService(st *store.Store, startedAt time.Time, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{st: st, startedAt: startedAt, logger: logger, now: time.Now, endedLogged: map[string]string{}}
}

// MarkStarted says the coordinator's loops and HTTP listener are up now.
func (s *Service) MarkStarted(at time.Time) {
	s.mu.Lock()
	s.startedAt = at
	s.mu.Unlock()
}

func (s *Service) started() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startedAt
}

// reader is the read surface [store.Store] and [store.Tx] share.
type reader interface {
	GetFPPPlaylistEntryObservation(ctx context.Context, instanceUUID string) (store.FPPPlaylistEntryObservationRecord, error)
	GetFallbackProgram(ctx context.Context, instanceUUID string) (store.FallbackProgramRecord, error)
	GetFallbackProgramAck(ctx context.Context, instanceUUID string) (store.FallbackProgramAckRecord, error)
}

// playlistOverAt reads the coordinator's own FPP status for the player:
// the fpp.status and fpp.playlist.name signals its FPP collector writes.
// It answers only from readings taken after the report arrived.
func playlistOverAt(ctx context.Context, r *store.Store, rec store.FallbackPlayerStateRecord) (time.Time, error) {
	if rec.State == StateNormal || rec.State == stateCleared || rec.PlaylistName == "" {
		return time.Time{}, nil
	}
	endpoints, err := r.GetFPPInstanceUUIDByUUID(ctx, rec.FPPInstanceUUID)
	if err != nil {
		return time.Time{}, fmt.Errorf("fallbackhold: find the FPP player for %q: %w", rec.FPPInstanceUUID, err)
	}
	if len(endpoints) != 1 {
		return time.Time{}, nil
	}
	all, err := r.ListObservations(ctx, store.ObservationFilter{ResourceKind: observation.ResourceFPP, ResourceID: endpoints[0].EndpointID})
	if err != nil {
		return time.Time{}, fmt.Errorf("fallbackhold: read FPP status for %q: %w", endpoints[0].EndpointID, err)
	}
	var over time.Time
	for _, o := range all {
		text, ok := o.Value.(string)
		if !ok || o.ObservedAt == nil || !o.ObservedAt.After(rec.ReceivedAt) {
			continue
		}
		notPlaying := o.Signal == fppStatusSignal && text != fppStatusPlaying
		otherPlaylist := o.Signal == fppPlaylistNameSignal && text != rec.PlaylistName
		if (notPlaying || otherPlaylist) && o.ObservedAt.After(over) {
			over = *o.ObservedAt
		}
	}
	return over, nil
}

// readInputs gathers what [decide] needs. Inside a transaction r is the
// transaction, and the FPP status is left unread: no caller there needs it.
func (s *Service) readInputs(ctx context.Context, r reader, rec store.FallbackPlayerStateRecord, now time.Time) (inputs, error) {
	instanceUUID := rec.FPPInstanceUUID
	in := inputs{startedAt: s.started(), now: now}
	if st, ok := r.(*store.Store); ok {
		over, err := playlistOverAt(ctx, st, rec)
		if err != nil {
			return inputs{}, err
		}
		in.playlistOverAt = over
	}
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
	in, err := s.readInputs(ctx, s.st, rec, now)
	if err != nil {
		return Verdict{}, err
	}
	v := decide(rec, in)
	s.announceEndedWithoutPlugin(ctx, v, in)
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

// announceEndedWithoutPlugin writes one warning and one event the first
// time a hold is seen to have ended without its plugin saying so.
func (s *Service) announceEndedWithoutPlugin(ctx context.Context, v Verdict, in inputs) {
	if !v.EndedWithoutPlugin {
		return
	}
	rec := v.Record
	key := rec.BootID + "/" + fmt.Sprint(rec.Sequence)
	s.mu.Lock()
	seen := s.endedLogged[rec.FPPInstanceUUID] == key
	s.endedLogged[rec.FPPInstanceUUID] = key
	s.mu.Unlock()
	if seen {
		return
	}
	message := fmt.Sprintf("The plugin on this FPP player stopped reporting while it was running playlist %q from its fallback program, and the player is no longer playing that playlist. The coordinator has taken the player back.", rec.PlaylistName)
	s.logger.Warn("fallback hold: ended without the plugin's report", "fppInstanceUuid", rec.FPPInstanceUUID,
		"reportedState", rec.State, "playlistName", rec.PlaylistName, "lastReportAt", rec.ReceivedAt, "playlistOverAt", in.playlistOverAt)
	at := in.playlistOverAt
	if _, err := s.st.AppendEvent(ctx, store.EventRecord{
		OccurredAt: &at, Source: ObservationSource,
		Resource: observation.ResourceRef{Kind: observation.ResourceFallbackProgram, ID: rec.FPPInstanceUUID},
		Category: EventHoldEndedWithoutPlugin, Severity: "warning", Summary: message,
	}); err != nil {
		s.logger.Warn("fallback hold: recording the end of a hold failed", "fppInstanceUuid", rec.FPPInstanceUUID, "error", err)
	}
}

// All returns the verdict for every player whose plugin has reported.
func (s *Service) All(ctx context.Context, now time.Time) ([]Verdict, error) {
	recs, err := s.st.ListFallbackPlayerStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fallbackhold: list player states: %w", err)
	}
	out := make([]Verdict, 0, len(recs))
	for _, rec := range recs {
		if rec.State == stateCleared {
			continue
		}
		v, err := s.evaluateRecord(ctx, rec, now)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// FirstHeld returns the first of instanceUUIDs that is held, "" when none is.
func (s *Service) FirstHeld(ctx context.Context, instanceUUIDs []string, now time.Time) (string, Verdict, error) {
	for _, id := range instanceUUIDs {
		v, err := s.Evaluate(ctx, id, now)
		if err != nil {
			return "", Verdict{}, err
		}
		if v.Held {
			return id, v, nil
		}
	}
	return "", Verdict{}, nil
}

// Clear forgets the player's report and keeps its observation floor,
// raised to now when the report said the plugin was running the show. It
// says whether there was a report to forget.
func (s *Service) Clear(ctx context.Context, tx *store.Tx, instanceUUID string, now time.Time) (bool, error) {
	prev, err := tx.GetFallbackPlayerState(ctx, instanceUUID)
	if errors.Is(err, store.ErrFallbackPlayerStateNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if prev.State == stateCleared {
		return false, nil
	}
	cleared := store.FallbackPlayerStateRecord{
		FPPInstanceUUID: instanceUUID, State: stateCleared, Since: now, ReceivedAt: now, StateChangedAt: now,
		ObservationsIgnoredThrough: prev.ObservationsIgnoredThrough,
	}
	// A plugin that last said normal was not running the show, so what it
	// observed since then stays good.
	if prev.State != StateNormal {
		cleared.ObservationsIgnoredThrough = now
	}
	return true, tx.PutFallbackPlayerState(ctx, cleared)
}

// Record stores r as the player's latest report unless an equal or newer
// one from the same plugin start is already held.
func (s *Service) Record(ctx context.Context, r Report, now time.Time) (RecordResult, error) {
	var result RecordResult
	err := s.st.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		prev, err := tx.GetFallbackPlayerState(ctx, r.FPPInstanceUUID)
		stored := err == nil
		if err != nil && !errors.Is(err, store.ErrFallbackPlayerStateNotFound) {
			return err
		}
		// A cleared row is no report, and still carries a floor to keep.
		found := stored && prev.State != stateCleared
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
		if stored && !found {
			rec.ObservationsIgnoredThrough = prev.ObservationsIgnoredThrough
		}
		if found {
			in, err := s.readInputs(ctx, tx, prev, now)
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
			// This report ended no time as executor. Only what arrived
			// before this run of the coordinator stays out.
			if started := s.started(); started.After(rec.ObservationsIgnoredThrough) {
				rec.ObservationsIgnoredThrough = started
			}
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
