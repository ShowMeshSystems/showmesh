package api

import (
	"context"
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

// nightAnnouncementRevisions computes this cycle's clear and start
// revisions for one announcement cue. Both must strictly exceed every
// revision this controller has ever sent at the session AND the apply's
// own pinned config revision, since the apply lands between them.
//
// clear = floor+1 and start = floor+2, so the pair advances by two per
// cycle and neither can ever be refused as stale, whether or not the
// clear actually took effect: if it did, the apply meets a fresh session
// at revision zero and start still outranks it; if it did not, start
// still outranks the surviving session's own current revision.
func nightAnnouncementRevisions(history []nightBackgroundAudioHistoryRow, applyRevision int64) (clearRevision, startRevision int64) {
	floor := applyRevision
	for _, row := range history {
		if row.Row.ActionRevision > floor {
			floor = row.Row.ActionRevision
		}
	}
	return floor + 1, floor + 2
}

// nightAdvanceAnnouncementClear runs BEFORE an announcement cue's own
// dispatch. See this file's constants for why the clear belongs here
// rather than after. Never gated on anything: clearing a session that
// does not exist is a no-op success on the node.
func (h *handlers) nightAdvanceAnnouncementClear(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue) {
	target, applyRevision, ok := h.nightAnnouncementSessionTarget(ctx, cue)
	if !ok {
		return
	}
	history, err := h.nightAnnouncementHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: announcement: failed to read announcement-session history", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return
	}
	clearRevision, _ := nightAnnouncementRevisions(history, applyRevision)
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
	target, applyRevision, ok := h.nightAnnouncementSessionTarget(ctx, cue)
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
	_, startRevision := nightAnnouncementRevisions(history, applyRevision)
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
