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
	"path"
	"strings"
	"sync"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
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

// PlayerReading is the coordinator's own current reading of an FPP player.
// Current is false when it has no reading it can stand on.
type PlayerReading struct {
	Current bool
	// Status is FPP's status word, "idle" or "playing" among others.
	Status string
	// Playlist is set only when PlaylistCurrent is true.
	Playlist        string
	PlaylistCurrent bool
}

// PlayerReader reads an FPP player by its configured id, ignoring anything
// collected before notBefore. The API layer supplies it, so this package
// reads a player exactly as the night loop does.
type PlayerReader func(ctx context.Context, fppInstanceID string, notBefore, now time.Time) PlayerReading

// AuditWriter is the slice of identity.Service this package writes to: a
// store write and its audit entry in one transaction.
type AuditWriter interface {
	AuditedWrite(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error)) error
}

// OverReadingMaxGap is the longest two readings of a player may be apart
// and still count as one standing reading. A longer gap restarts the count,
// so the standing rule does not depend on how often a caller evaluates.
const OverReadingMaxGap = 15 * time.Second

// errNothingToClear rolls an automatic clear back when the report it was
// decided on is no longer the stored one.
var errNothingToClear = errors.New("fallbackhold: the stored report changed before the clear")

const (
	statusIdle    = "idle"
	statusPlaying = "playing"

	systemPrincipalID    = "system-fallback-hold"
	systemPrincipalName  = "ShowMesh fallback hold"
	auditActionAutoClear = "fallback.player_state.auto_clear"
)

// playlistKey is a playlist name without a directory or a trailing ".json".
func playlistKey(name string) string {
	return strings.TrimSuffix(path.Base(strings.TrimSpace(name)), ".json")
}

// playlistOver says whether reading shows the player is done with the
// playlist named reported: idle, or playing a playlist of another name.
// Anything else, a pause or a graceful stop among them, is not over.
func playlistOver(reading PlayerReading, reported string) bool {
	if !reading.Current {
		return false
	}
	switch reading.Status {
	case statusIdle:
		return true
	case statusPlaying:
		return reading.PlaylistCurrent && reading.Playlist != "" && playlistKey(reading.Playlist) != playlistKey(reported)
	}
	return false
}

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
	// playlistOver is true when the coordinator's own current reading of
	// the player, taken after the report, has shown the reported playlist
	// over for longer than [SilentAfter] without a break. A plugin whose
	// link has only just come back reports well inside that time.
	playlistOver bool
	startedAt    time.Time
	now          time.Time
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
	// The coordinator's own downtime is not the plugin's silence.
	heardOrStarted := rec.ReceivedAt
	if in.startedAt.After(heardOrStarted) {
		heardOrStarted = in.startedAt
	}
	silent := in.now.Sub(heardOrStarted) > SilentAfter
	switch {
	case rec.ReceivedAt.Before(in.startedAt) && in.now.Sub(in.startedAt) <= StartGrace:
		// Whatever the stored row says, the plugin gets its chance to
		// report in this run first.
		v.Held, v.Reason = true, ReasonCoordinatorStarting
	case rec.State != StateNormal && silent && in.playlistOver:
		// The show the hold protected is over, seen by the coordinator.
		v.EndedWithoutPlugin = true
	case rec.State != StateNormal:
		v.Held, v.Reason = true, ReasonExecutor
	case !rec.AckWaitSince.IsZero() && !in.ackCurrent && !silent:
		// A silent plugin cannot be waited on.
		v.Held, v.Reason, v.AckWaitSince = true, ReasonAwaitingAcknowledgement, rec.AckWaitSince
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
	reader    PlayerReader
	audit     AuditWriter
	nudge     func()
	// overSince is when each player's reported playlist was first read
	// as over, for the report named in it.
	overSince map[string]overReading
}

type overReading struct {
	bootID   string
	sequence int64
	since    time.Time
	lastSeen time.Time
}

// NewService builds a Service. startedAt is what [StartGrace] counts from
// until [Service.MarkStarted] moves it.
func NewService(st *store.Store, startedAt time.Time, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{st: st, startedAt: startedAt, logger: logger, now: time.Now}
}

// SetPlayerReader wires the coordinator's own reading of an FPP player.
// Without one no hold ever ends without its plugin.
func (s *Service) SetPlayerReader(r PlayerReader) {
	s.mu.Lock()
	s.reader = r
	s.mu.Unlock()
}

// SetNudge wires what is told when an automatic clear lets a player go,
// the Cue loop in the coordinator.
func (s *Service) SetNudge(nudge func()) {
	s.mu.Lock()
	s.nudge = nudge
	s.mu.Unlock()
}

// SetAudit wires where an automatic clear is audited.
func (s *Service) SetAudit(a AuditWriter) {
	s.mu.Lock()
	s.audit = a
	s.mu.Unlock()
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

// readPlaylistOver asks the coordinator's own reading of the player, taken
// after the report arrived, whether the reported playlist is over.
func (s *Service) readPlaylistOver(ctx context.Context, rec store.FallbackPlayerStateRecord, now time.Time) (bool, error) {
	s.mu.Lock()
	reader := s.reader
	s.mu.Unlock()
	if reader == nil || rec.State == StateNormal || rec.State == stateCleared || rec.PlaylistName == "" {
		return false, nil
	}
	endpoints, err := s.st.GetFPPInstanceUUIDByUUID(ctx, rec.FPPInstanceUUID)
	if err != nil {
		return false, fmt.Errorf("fallbackhold: find the FPP player for %q: %w", rec.FPPInstanceUUID, err)
	}
	if len(endpoints) != 1 {
		return false, nil
	}
	over := playlistOver(reader(ctx, endpoints[0].EndpointID, rec.ReceivedAt, now), rec.PlaylistName)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !over {
		delete(s.overSince, rec.FPPInstanceUUID)
		return false, nil
	}
	seen, ok := s.overSince[rec.FPPInstanceUUID]
	if !ok || seen.bootID != rec.BootID || seen.sequence != rec.Sequence || now.Sub(seen.lastSeen) > OverReadingMaxGap {
		// Nobody looked for too long to say the reading stood meanwhile.
		seen = overReading{bootID: rec.BootID, sequence: rec.Sequence, since: now}
	}
	seen.lastSeen = now
	if s.overSince == nil {
		s.overSince = map[string]overReading{}
	}
	s.overSince[rec.FPPInstanceUUID] = seen
	return now.Sub(seen.since) > SilentAfter, nil
}

// readInputs gathers what [decide] needs. Inside a transaction r is the
// transaction, and the FPP status is left unread: no caller there needs it.
func (s *Service) readInputs(ctx context.Context, r reader, rec store.FallbackPlayerStateRecord, now time.Time) (inputs, error) {
	instanceUUID := rec.FPPInstanceUUID
	in := inputs{startedAt: s.started(), now: now}
	if _, ok := r.(*store.Store); ok {
		over, err := s.readPlaylistOver(ctx, rec, now)
		if err != nil {
			return inputs{}, err
		}
		in.playlistOver = over
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
	if v.EndedWithoutPlugin && !s.clearEndedWithoutPlugin(ctx, rec, now) {
		// Nothing was cleared: a newer report stands, or the clear
		// failed. The answer is whatever the stored report says now.
		current, err := s.st.GetFallbackPlayerState(ctx, rec.FPPInstanceUUID)
		if errors.Is(err, store.ErrFallbackPlayerStateNotFound) {
			return Verdict{}, nil
		}
		if err != nil {
			return Verdict{}, fmt.Errorf("fallbackhold: re-read player state for %q: %w", rec.FPPInstanceUUID, err)
		}
		if current.BootID == rec.BootID && current.Sequence == rec.Sequence {
			// Same report and it could not be cleared: keep holding.
			held := decide(rec, in)
			held.EndedWithoutPlugin, held.Held, held.Reason = false, true, ReasonExecutor
			return held, nil
		}
		return s.evaluateRecord(ctx, current, now)
	}
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

// clearEndedWithoutPlugin forgets a report whose hold ended without the
// plugin, exactly as an operator's clear does, so the hold cannot return
// when the player next plays a playlist of that name. The clear and its
// audit entry are one transaction. It reports whether it cleared.
func (s *Service) clearEndedWithoutPlugin(ctx context.Context, rec store.FallbackPlayerStateRecord, now time.Time) bool {
	s.mu.Lock()
	audit, nudge := s.audit, s.nudge
	s.mu.Unlock()
	clear := func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		current, err := tx.GetFallbackPlayerState(ctx, rec.FPPInstanceUUID)
		if err != nil || current.BootID != rec.BootID || current.Sequence != rec.Sequence {
			// Gone, or a newer report arrived meanwhile and stands.
			return identity.AuditEntry{}, errNothingToClear
		}
		cleared, err := s.Clear(ctx, tx, rec.FPPInstanceUUID, now)
		if err != nil {
			return identity.AuditEntry{}, err
		}
		if !cleared {
			return identity.AuditEntry{}, errNothingToClear
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: systemPrincipalID, PrincipalName: systemPrincipalName,
			Action: auditActionAutoClear, Target: rec.FPPInstanceUUID, Kind: identity.AuditOutcome,
			Params:        map[string]any{"reportedState": rec.State, "playlistName": rec.PlaylistName},
			OutcomeReason: "the plugin stopped reporting and the player is no longer playing that playlist",
		}, nil
	}
	var err error
	if audit != nil {
		err = audit.AuditedWrite(ctx, clear)
	} else {
		err = s.st.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
			_, cerr := clear(ctx, tx)
			return cerr
		})
	}
	if err != nil {
		if !errors.Is(err, errNothingToClear) {
			s.logger.Warn("fallback hold: clearing a hold that ended without its plugin failed", "fppInstanceUuid", rec.FPPInstanceUUID, "error", err)
		}
		return false
	}
	message := fmt.Sprintf("The plugin on this FPP player stopped reporting while it was running playlist %q from its fallback program, and the player is no longer playing that playlist. The coordinator has taken the player back.", rec.PlaylistName)
	s.logger.Warn("fallback hold: ended without the plugin's report", "fppInstanceUuid", rec.FPPInstanceUUID,
		"reportedState", rec.State, "playlistName", rec.PlaylistName, "lastReportAt", rec.ReceivedAt)
	if _, err := s.st.AppendEvent(ctx, store.EventRecord{
		OccurredAt: &now, Source: ObservationSource,
		Resource: observation.ResourceRef{Kind: observation.ResourceFallbackProgram, ID: rec.FPPInstanceUUID},
		Category: EventHoldEndedWithoutPlugin, Severity: "warning", Summary: message,
	}); err != nil {
		s.logger.Warn("fallback hold: recording the end of a hold failed", "fppInstanceUuid", rec.FPPInstanceUUID, "error", err)
	}
	s.ForgetSignals(ctx, rec.FPPInstanceUUID, now)
	if nudge != nil {
		nudge()
	}
	return true
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
