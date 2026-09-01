package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	pkgaudio "github.com/showmeshsystems/showmesh/pkg/audio"
)

// An announcement cue's duck/mix/interrupt policy (RESTING-MODE.md
// section 8, AUDIO-ENGINE.md section 9) is DECLARED on the announcement's
// own playback session and enforced by the audio node, never driven from
// here.
//
// The node already owns the whole mechanism: internal/agent/audio's
// duckLowerPriority/interruptLowerPriority run when a session whose
// declared source role outranks the bed's reaches Playing, and
// restoreDucked/restoreInterrupted run when it leaves Playing - on stop,
// on clear, and on natural completion. Only the node observes the last of
// those three, which is why this controller cannot own the release: the
// only signal it has is its own cue outbox row, and that row resolves
// when the announcement's DISPATCH is confirmed, not when the
// announcement finishes playing.
//
// A coordinator-driven duck was therefore both ineffective and harmful.
// Proven against a real audio.Manager with the exact gains this file used
// to send (internal/agent/audio/nightduck_test.go): a coordinator fade to
// a fraction of the configured ceiling, followed by the node's own duck,
// made the node capture the ALREADY-ducked gain as its pre-duck value, so
// the bed came back at duck gain and stayed there for the rest of the
// night - the exact stranded-quiet defect class, reintroduced by having
// two owners for one piece of state. IDENTIFIER-REGISTER.md names the
// same rule from the other direction: an announcement is
// audio.session.apply with source role "announcement" and a declared
// mix/duck/interrupt policy, and a second way to reach that state has "no
// way to say which one won".

func nightAnnouncementPolicy(cue config.NightSessionCue, payload config.NightSessionPayload) string {
	if cue.AnnouncementPolicy != nil {
		return *cue.AnnouncementPolicy
	}
	if payload.AnnouncementDefaultPolicy != "" {
		return payload.AnnouncementDefaultPolicy
	}
	return config.NightSessionAnnouncementPolicyDefault
}

// nightAnnouncementCueWithResolvedPolicy returns cue with its effective
// announcement policy materialized onto its own AnnouncementPolicy
// field, so the dispatch path downstream needs the cue alone and never
// the whole payload to know what was configured. Every other role is
// returned unchanged (announcementPolicy is refused at validation for
// them anyway).
func nightAnnouncementCueWithResolvedPolicy(cue config.NightSessionCue, payload config.NightSessionPayload) config.NightSessionCue {
	if cue.Role != config.NightSessionCueRoleAnnouncement {
		return cue
	}
	policy := nightAnnouncementPolicy(cue, payload)
	cue.AnnouncementPolicy = &policy
	return cue
}

// nightAnnouncementDeclaredTarget declares source role "announcement" and
// the configured mix policy on the session an announcement cue applies,
// as extra params on the cue's OWN audio.session.apply dispatch. It mints
// no operation and sends no extra command: the declaration rides the
// dispatch the cue was already going to make, under that cue's own outbox
// row, idempotency key, and action revision.
//
// Operator-authored params always win. A show.action that already spells
// out sourceRole or mixPolicy is left exactly as authored, so this can
// only ever fill in what the bound action did not say.
//
// Anything that is not an audio-integration audio.session.apply is
// returned untouched: an announcement played through FPP, MQTT, or
// Resolume carries no ShowMesh playback session for a policy to attach
// to, and this controller has no way to make room for it or to know when
// it ends. That is reported at readiness (nightaudioreadiness.go) rather
// than papered over with a gain command that would strand the bed.
func nightAnnouncementDeclaredTarget(cue config.NightSessionCue, target config.ShowActionTarget) config.ShowActionTarget {
	if cue.Role != config.NightSessionCueRoleAnnouncement || cue.AnnouncementPolicy == nil {
		return target
	}
	policy := *cue.AnnouncementPolicy
	if policy == "" || !nightAnnouncementTargetDeclarable(target) {
		return target
	}
	params := make(map[string]any, len(target.Params)+2)
	for k, v := range target.Params {
		params[k] = v
	}
	if _, ok := params["sourceRole"]; !ok {
		params["sourceRole"] = string(pkgaudio.SourceRoleAnnouncement)
	}
	if _, ok := params["mixPolicy"]; !ok {
		params["mixPolicy"] = policy
	}
	target.Params = params
	return target
}

// nightAnnouncementTargetDeclarable reports whether target is a dispatch
// a source role and mix policy can be declared on at all: only
// audio.session.apply carries an ApplyRequest for the node to merge them
// into (internal/agent/audiosessionops.go).
func nightAnnouncementTargetDeclarable(target config.ShowActionTarget) bool {
	return target.Integration == config.ShowActionIntegrationAudio && target.AudioAction == "audio.session.apply"
}

// The announcement session's own durable steps. An announcement is
// audio.session.apply with source role "announcement" and a declared
// mix policy, FOLLOWED BY audio.session.start (IDENTIFIER-REGISTER.md,
// in the paragraph explaining why no separate duck operation was
// minted). The apply is the cue's own bound show.action, dispatched by
// nightRunCue at that action's pinned config revision; the start is a
// step this controller owns, because a cue binds exactly one target and
// an apply alone never leaves the node's session Playing - and duck and
// interrupt are resolved at Start, not at Apply, so an apply-only
// announcement plays nothing and ducks nothing.
//
// A clear runs FIRST, before the apply. It is the only boundary this
// controller can name honestly: it cannot observe when an announcement
// ends, so it cannot clear afterwards without risking cutting one off,
// but "before this announcement is used again" is always safe. It does
// three things at once. It stops an announcement left playing by an
// earlier cycle, which would otherwise hold the bed ducked forever. It
// deletes the node's persisted session record, so an announcement
// session does not live on the node indefinitely. And because
// [audio.Manager.Clear] deletes the session's RevisionState with it, the
// apply that follows is accepted at its pinned config revision every
// cycle instead of being refused as stale from the second night on.
const (
	nightPhaseAnnouncementSession = "announcementSession"
	nightPhaseAnnouncementClear   = nightPhaseAnnouncementSession + ":clear"
	nightPhaseAnnouncementStart   = nightPhaseAnnouncementSession + ":start"
)

// The step kinds these two phases surface, distinct from background
// audio's own so an operator reading one step list can tell which
// sequence a failure belongs to (ADR-039: a durable step nothing can
// read is not evidence). A refused clear in particular means the
// PREVIOUS announcement may still be playing and still holding the bed
// ducked, which is exactly the kind of thing that must not live only in
// a log line.
const (
	nightAnnouncementStepClear = "announcementClear"
	nightAnnouncementStepStart = "announcementStart"
)

// nightParseAnnouncementRow classifies one outbox row under this
// controller's announcement-session phase family. false means the row
// matches no shape this build writes.
func nightParseAnnouncementRow(row store.NightCueOutboxRecord) (kind string, ok bool) {
	switch {
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementClear+":"):
		return nightAnnouncementStepClear, true
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementStart+":"):
		return nightAnnouncementStepStart, true
	}
	return "", false
}

// nightAnnouncementHistory returns every announcement-session step this
// controller has recorded for rec, across every cycle and every cue.
// Used only to keep revisions strictly increasing, so it deliberately
// does not care which cue or which audio session a row belongs to: one
// shared, monotonic counter can never refuse a later step as stale,
// while a per-cue counter would if two cues ever named the same audio
// session.
func (h *handlers) nightAnnouncementHistory(ctx context.Context, rec store.NightSessionRecord) ([]nightBackgroundAudioHistoryRow, error) {
	rows, err := h.deps.NightSessions.ListNightCueOutboxRowsForPhasePrefix(ctx, rec.ID, nightPhaseAnnouncementSession)
	if err != nil {
		return nil, fmt.Errorf("api: list announcement-session history: %w", err)
	}
	out := make([]nightBackgroundAudioHistoryRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, nightBackgroundAudioHistoryRow{Row: r})
	}
	return out, nil
}

// nightAnnouncementRevisions computes this cycle's clear, apply, and start
// revisions for one announcement cue, as three CONSECUTIVE values above a
// single floor: the coordinator's own persisted audio_sessions revision for
// this session (store/audiosessions.go, read via
// [handlers.nightAudioSessionPersistedRevision]). The apply's own pinned
// CONFIGURATION revision (which cue names it ran, recorded on the outbox
// row's own ActionRevision) plays no part in this floor: it is a config
// object's revision, not a session desired-state counter, and the two are
// unrelated units. Feeding it into a session-revision dispatch is exactly
// the bug this triple replaces - see [handlers.nightAnnouncementApplyDispatchRevision].
//
// The floor is deliberately NOT derived from nightAnnouncementHistory:
// that history is scoped to the CURRENT night session record's id, which
// is a fresh uuid every time prepare-site opens a new epoch
// (nightPrepareSiteTx) - so a new night session always found it empty and
// the floor collapsed to the config revision alone, while the node still
// held whatever the previous night session left. audio_sessions is keyed by
// the audio session id instead, so it survives exactly the boundary the
// history-keyed floor did not. Deriving the floor from what the node
// currently reports instead of this coordinator's own persisted record
// was considered and rejected: that computes desired state from observed
// state, and fails exactly when the node is unreachable.
//
// clear = floor+1, apply = floor+2, start = floor+3: the triple advances by
// three per cycle and strictly exceeds the floor regardless of whether the
// clear actually took effect. If it did, the node's RevisionState for this
// session was deleted (audio.Manager.Clear), so the apply would have been
// accepted at any revision - floor+2 still is. If it did not (skipped ahead
// of the show-commit boundary, refused, or simply never reached the node),
// the node still holds its prior revision, which is at most this floor
// (nothing this coordinator has sent for this session exceeds it), so
// floor+2 still strictly exceeds it and the apply is still accepted; start
// at floor+3 then strictly exceeds whatever the apply's own dispatch
// landed at, confirmed or not.
//
// Each of clear/apply/start is normally computed from its OWN fresh read of
// the persisted floor, taken immediately before that step dispatches (see
// nightAdvanceAnnouncementClear, [handlers.nightAnnouncementApplyDispatchRevision],
// nightAdvanceAnnouncementStart) - never a single floor snapshot reused for
// all three - so a later step's floor only ever advances, never falls
// behind an earlier step's own landed revision.
//
// That guarantees the triple can never be refused as stale because of
// anything THIS coordinator has itself previously sent — it does NOT
// guarantee it is never refused at all. If the NODE's own revision counter
// has run ahead of this coordinator's persisted audio_sessions row — for
// example through the swallowed PutAudioSession error in
// persistAudioSessionDesiredState (audiodispatch.go), logged and discarded
// after the node has already applied the command — floor+1 can still land
// at or below the node's own counter, and the node refuses it as stale
// there. A caller must still be prepared to see that refusal; this function
// only computes the floor-relative triple, it does not and cannot see the
// node's own state.
func nightAnnouncementRevisions(persistedRevision int64) (clearRevision, applyRevision, startRevision int64) {
	floor := persistedRevision
	return floor + 1, floor + 2, floor + 3
}

// nightAudioSessionPersistedRevision reads sessionID's own durable
// audio_sessions row and returns its revision, or 0 when no row exists
// yet (a session this coordinator has never dispatched anything against).
// This is [nightAnnouncementRevisions]'s floor input across night
// sessions - see that function's own doc comment. A read error other
// than "not found" costs only the chance to advance past a revision this
// coordinator could not read this time: the apply's own pinned config
// revision is still in the floor, so this can never cause a rewind, only
// fail to include evidence it could not get.
func (h *handlers) nightAudioSessionPersistedRevision(ctx context.Context, sessionID string) int64 {
	rec, err := h.deps.AudioSessions.GetAudioSession(ctx, sessionID)
	switch {
	case err == nil:
		return int64(rec.Revision)
	case errors.Is(err, store.ErrAudioSessionNotFound):
		return 0
	default:
		h.logWarn("night loop: announcement: failed to read persisted audio session revision; the floor may not include it", "sessionId", sessionID, "error", err)
		return 0
	}
}

// nightAnnouncementAppliedThisCycle reports whether cue's own apply
// already has an outbox row for rec's current cycle and phase. Once it
// does, the clear must never run again this cycle: the cue list is
// re-walked every tick, and a clear reached on a later tick would land
// after the apply-then-start sequence, cutting off an announcement that
// is already playing. A lookup error is treated the same as
// "applied": skipping a clear costs only the stale-apply protection for
// this cycle, while running one on a false positive risks cutting off
// live audio.
func (h *handlers) nightAnnouncementAppliedThisCycle(ctx context.Context, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue) bool {
	_, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, cuePhase, cue.Name)
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNightCueOutboxNotFound):
		return false
	default:
		h.logWarn("night loop: announcement: failed to check whether this cycle's apply already ran; skipping the clear so a playing announcement is never cut off", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return true
	}
}

// nightAdvanceAnnouncementClear runs BEFORE an announcement cue's own
// dispatch. See this file's constants for why the clear belongs here
// rather than after. Never gated on anything: clearing a session that
// does not exist is a no-op success on the node.
func (h *handlers) nightAdvanceAnnouncementClear(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue) {
	target, _, ok := h.nightAnnouncementSessionTarget(ctx, cue)
	if !ok {
		return
	}
	history, err := h.nightAnnouncementHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: announcement: failed to read announcement-session history", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return
	}
	persisted := h.nightAudioSessionPersistedRevision(ctx, target.AudioSessionID)
	clearRevision, _, _ := nightAnnouncementRevisions(persisted)
	clear := nightAudioTarget(target.AudioNodeID, target.AudioSessionID, "audio.session.clear", map[string]any{})
	phase := nightPhaseAnnouncementClear + ":" + cuePhase
	if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, clear, clearRevision, history); err != nil {
		h.logWarn("night loop: announcement: clear failed", "sessionId", rec.ID, "cue", cue.Name, "error", err)
	}
}

// nightAdvanceAnnouncementStart runs AFTER an announcement cue's own
// dispatch, and is what actually makes the announcement play - and
// therefore what makes the node resolve its declared duck or interrupt
// policy, which Manager.Start evaluates and Manager.Apply does not.
//
// Gated on the apply row being terminal, not on it having confirmed. An
// apply refused because the node already holds this exact desired state
// is not a reason to leave the announcement silent, and an apply that
// genuinely failed produces a start that fails in its own row, visibly,
// rather than one that is silently never attempted.
func (h *handlers) nightAdvanceAnnouncementStart(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue) {
	target, _, ok := h.nightAnnouncementSessionTarget(ctx, cue)
	if !ok {
		return
	}
	applyRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, cuePhase, cue.Name)
	if err != nil {
		return
	}
	if applyRow.State != nightCueStateResolved && applyRow.State != nightCueStateAmbiguous {
		return // the announcement's own apply has not reached a terminal state yet.
	}
	if applyRow.Outcome != nightCueOutcomeConfirmed {
		h.logWarn("night loop: announcement: the apply did not confirm; starting anyway so a silent announcement is never the quiet outcome", "sessionId", rec.ID, "cue", cue.Name, "outcome", applyRow.Outcome)
	}
	history, err := h.nightAnnouncementHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: announcement: failed to read announcement-session history for start", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return
	}
	persisted := h.nightAudioSessionPersistedRevision(ctx, target.AudioSessionID)
	_, _, startRevision := nightAnnouncementRevisions(persisted)
	start := nightAudioTarget(target.AudioNodeID, target.AudioSessionID, "audio.session.start", map[string]any{})
	phase := nightPhaseAnnouncementStart + ":" + cuePhase
	if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, start, startRevision, history); err != nil {
		h.logWarn("night loop: announcement: start failed", "sessionId", rec.ID, "cue", cue.Name, "error", err)
	}
}

// nightAnnouncementSessionTarget resolves cue's bound show.action and
// reports it only when it is an announcement whose target this
// controller can run the apply-then-start sequence against. Everything
// else returns false and is left entirely alone.
func (h *handlers) nightAnnouncementSessionTarget(ctx context.Context, cue config.NightSessionCue) (config.ShowActionTarget, int64, bool) {
	if cue.Role != config.NightSessionCueRoleAnnouncement {
		return config.ShowActionTarget{}, 0, false
	}
	action, revision, err := nightResolveShowAction(ctx, h.deps.Config, cue.Action)
	if err != nil {
		return config.ShowActionTarget{}, 0, false
	}
	if !nightAnnouncementTargetDeclarable(action.Target) {
		return config.ShowActionTarget{}, 0, false
	}
	return action.Target, revision, true
}

// nightAnnouncementApplyDispatchRevision reports the audio-session dispatch
// revision cue's own apply must carry, when cue is an announcement bound to
// a declarable audio.session.apply target ([nightAnnouncementTargetDeclarable]):
// the SAME audio-session-scoped floor nightAdvanceAnnouncementClear and
// nightAdvanceAnnouncementStart already draw their own clear and start
// revisions from ([nightAnnouncementRevisions]), so all three advance
// together. Before this, the apply carried its own pinned CONFIGURATION
// revision as a session-revision stand-in - a constant fed into a counter
// that only goes up, since nothing about the action changes between cycles
// - which is what let a later cycle's apply be refused stale even though
// the clear and start (already floor-derived) kept advancing.
//
// ok is false for everything else - every non-audio integration, every
// audio action that is not this exact declarable apply shape, and every
// cue whose Role is not announcement - and the caller must keep dispatching
// at the cue's own pinned config revision exactly as before: this never
// changes what a non-announcement or non-audio cue sends.
//
// target must be the cue's bound action's OWN target (before
// [nightAnnouncementDeclaredTarget] adds sourceRole/mixPolicy params -
// those never change Integration or AudioAction, so classification is the
// same either way, but callers already have the pre-wrap target on hand).
func (h *handlers) nightAnnouncementApplyDispatchRevision(ctx context.Context, cue config.NightSessionCue, target config.ShowActionTarget) (revision int64, ok bool) {
	if cue.Role != config.NightSessionCueRoleAnnouncement || !nightAnnouncementTargetDeclarable(target) {
		return 0, false
	}
	persisted := h.nightAudioSessionPersistedRevision(ctx, target.AudioSessionID)
	_, applyRevision, _ := nightAnnouncementRevisions(persisted)
	return applyRevision, true
}
