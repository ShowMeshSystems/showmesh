package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/showmeshsystems/showmesh/internal/coordinator/audiosched"
	"github.com/showmeshsystems/showmesh/internal/coordinator/broker"
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
//
// Also clamps AudioNodeIDs to its own first element. The generic
// per-cue engine (nightRunCue) can only ever dispatch to and report on
// ONE target - that invariant predates this change and stays
// unchanged, including for barrier satisfaction, which gates on this
// exact row. A multi-node announcement's remaining nodes are this file's
// own controller-owned fan-out (nightAdvanceAnnouncementApplyExtra,
// nightAdvanceAnnouncementClear, nightAdvanceAnnouncementStart), never
// the generic engine. Applied to every audio-integration target, not
// only role "announcement": nothing else this coordinator dispatches
// through the generic engine is multi-node aware, so silently sending
// only the first configured node (rather than one arbitrarily chosen, or
// refusing outright) is the same "never guess, never drop silently"
// posture as the rest of this package, made loud in this comment because
// there is no validation-time check for it (a cue's bound action is
// decoded independently of the cue that binds it).
func nightAnnouncementDeclaredTarget(cue config.NightSessionCue, target config.ShowActionTarget) config.ShowActionTarget {
	target = nightAnnouncementDeclareParams(cue, target)
	if len(target.AudioNodeIDs) > 1 {
		target.AudioNodeIDs = target.AudioNodeIDs[:1]
	}
	return target
}

// nightAnnouncementDeclaredTargetFullNodeList is
// [nightAnnouncementDeclaredTarget]'s own param-declaration logic without
// its first-node clamp, for [handlers.nightAdvanceAnnouncementApplyExtra]
// - which needs the SAME declared params (source role, mix policy) for
// every additional node, each under its own single-node target.
func nightAnnouncementDeclaredTargetFullNodeList(cue config.NightSessionCue, target config.ShowActionTarget) config.ShowActionTarget {
	return nightAnnouncementDeclareParams(cue, target)
}

// nightAnnouncementDeclareParams is nightAnnouncementDeclaredTarget's own
// param-declaration step, factored out so it can run once, before AudioNodeIDs
// is clamped or replaced per node.
func nightAnnouncementDeclareParams(cue config.NightSessionCue, target config.ShowActionTarget) config.ShowActionTarget {
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

	// nightPhaseAnnouncementApplyExtra is this controller's own extra
	// apply, for every target node BEYOND the cue's bound action's own
	// first (which the generic engine already applies and reports -
	// nightAnnouncementDeclaredTarget's own doc comment). Never dispatched
	// when a target names one node, so a single-node installation writes
	// no row under this phase at all.
	nightPhaseAnnouncementApplyExtra = nightPhaseAnnouncementSession + ":applyExtra"

	// nightPhaseAnnouncementPrepare is ADR-049 decision 3's own (R3, the
	// frozen ruling) per-node prepare step a MULTI-NODE announcement adds
	// ahead of its start, one row per target node
	// (":"+cuePhase+":"+nodeID, matching clear/start's own suffixing):
	// this is what reads the media-clock evidence
	// [handlers.nightAnnouncementSchedule] selects the shared start
	// instant from. A single-node announcement never writes a row under
	// this phase (R5's own regression requirement - nothing about its
	// dispatch sequence changes).
	nightPhaseAnnouncementPrepare = nightPhaseAnnouncementSession + ":prepare"

	// nightPhaseAnnouncementSchedule holds the ONE durable decision a
	// multi-node announcement's own scheduling attempt makes
	// (":"+cuePhase, no node suffix: the instant, or the reason it could
	// not be selected, is shared by every listed node) - see
	// [handlers.nightAnnouncementSchedule]'s own doc comment for why this
	// is what lets a replay tick read no clock and dispatch nothing new.
	nightPhaseAnnouncementSchedule = nightPhaseAnnouncementSession + ":schedule"
)

// nightAnnouncementScheduleAligned and nightAnnouncementScheduleUnaligned
// are the [nightPhaseAnnouncementSchedule] row's own terminal Outcome
// vocabulary - deliberately NOT drawn from this file's nightCueOutcome*
// vocabulary (confirmed/refused/...), since this row never records a
// dispatch outcome, only a coordinator-local SELECTION decision. Aligned's
// own OutcomeReason carries the chosen ScheduledAtNs, formatted as a
// plain base-10 integer ([nightAnnouncementScheduleFromRow]'s own parse);
// unaligned's carries [audiosched.DescribeUnscheduled]'s own reason text.
const (
	nightAnnouncementScheduleAligned   = "aligned"
	nightAnnouncementScheduleUnaligned = "unaligned"
)

// The step kinds these phases surface, distinct from background audio's
// own so an operator reading one step list can tell which sequence a
// failure belongs to (ADR-039: a durable step nothing can read is not
// evidence). A refused clear in particular means the PREVIOUS
// announcement may still be playing and still holding the bed ducked,
// which is exactly the kind of thing that must not live only in a log
// line. nightAnnouncementStepApply is shared with the generic engine's
// own row for the cue's first node (mapNightAnnouncementPrimaryApplySteps,
// nightsessioncontrol.go) - owner ruling: every target node, including
// the first, reports through this ONE array, uniformly.
const (
	nightAnnouncementStepClear = "announcementClear"
	nightAnnouncementStepStart = "announcementStart"
	nightAnnouncementStepApply = "announcementApply"
)

// nightAnnouncementKnownCuePhases are the only cuePhase values this
// package ever suffixes an announcement phase string with -
// nightAnnouncementNodeFromPhase's own closed vocabulary to split a
// phase's trailing ":"+nodeID from the coordinator-chosen cuePhase
// segment before it, safely regardless of what characters an
// operator-chosen node id itself contains.
var nightAnnouncementKnownCuePhases = []string{nightPhaseEnterShow, nightPhaseEnterResting, nightPhaseFadeOut}

// nightAnnouncementNodeFromPhase recovers the target node id from a
// phase built as kindPrefix+":"+cuePhase+":"+nodeID.
func nightAnnouncementNodeFromPhase(phase, kindPrefix string) (nodeID string, ok bool) {
	rest, ok := strings.CutPrefix(phase, kindPrefix+":")
	if !ok {
		return "", false
	}
	for _, cuePhase := range nightAnnouncementKnownCuePhases {
		if nodeID, ok := strings.CutPrefix(rest, cuePhase+":"); ok {
			return nodeID, true
		}
	}
	return "", false
}

// nightParseAnnouncementRow classifies one outbox row under this
// controller's announcement-session phase family and recovers the node
// it addressed. false means the row matches no shape this build writes.
func nightParseAnnouncementRow(row store.NightCueOutboxRecord) (kind, nodeID string, ok bool) {
	switch {
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementClear+":"):
		if n, ok := nightAnnouncementNodeFromPhase(row.Phase, nightPhaseAnnouncementClear); ok {
			return nightAnnouncementStepClear, n, true
		}
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementStart+":"):
		if n, ok := nightAnnouncementNodeFromPhase(row.Phase, nightPhaseAnnouncementStart); ok {
			return nightAnnouncementStepStart, n, true
		}
	case strings.HasPrefix(row.Phase, nightPhaseAnnouncementApplyExtra+":"):
		if n, ok := nightAnnouncementNodeFromPhase(row.Phase, nightPhaseAnnouncementApplyExtra); ok {
			return nightAnnouncementStepApply, n, true
		}
	}
	return "", "", false
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

// nightAudioSessionPersistedRevision reads nodeID's own durable
// audio_sessions row for sessionID and returns its revision, or 0 when no
// row exists yet (a session this coordinator has never dispatched
// anything against on that node). audio_sessions is keyed by (node_id,
// id) (schemaV21), so the same sessionID on two different nodes is two
// independent rows and two independent floors - a bed or announcement
// with more than one target node must read each node's own row, never a
// single shared one. This is [nightAnnouncementRevisions]'s floor input
// across night sessions - see that function's own doc comment. A read
// error other than "not found" costs only the chance to advance past a
// revision this coordinator could not read this time: the apply's own
// pinned config revision is still in the floor, so this can never cause a
// rewind, only fail to include evidence it could not get.
func (h *handlers) nightAudioSessionPersistedRevision(ctx context.Context, nodeID, sessionID string) int64 {
	rec, err := h.deps.AudioSessions.GetAudioSession(ctx, nodeID, sessionID)
	switch {
	case err == nil:
		return int64(rec.Revision)
	case errors.Is(err, store.ErrAudioSessionNotFound):
		return 0
	default:
		h.logWarn("night loop: announcement: failed to read persisted audio session revision; the floor may not include it", "nodeId", nodeID, "sessionId", sessionID, "error", err)
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
// dispatch, for EVERY one of the cue's own target nodes (not only the
// generic engine's own first node - clear was already
// controller-owned for every node, so this is a plain fan-out with no
// special first-node case). See this file's constants for why the clear
// belongs here rather than after. Never gated on anything: clearing a
// session that does not exist is a no-op success on the node.
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
	for _, nodeID := range target.AudioNodeIDs {
		persisted := h.nightAudioSessionPersistedRevision(ctx, nodeID, target.AudioSessionID)
		clearRevision, _, _ := nightAnnouncementRevisions(persisted)
		clear := nightAudioTarget(nodeID, target.AudioSessionID, "audio.session.clear", map[string]any{})
		phase := nightPhaseAnnouncementClear + ":" + cuePhase + ":" + nodeID
		if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, clear, clearRevision, history); err != nil {
			h.logWarn("night loop: announcement: clear failed", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "error", err)
		}
	}
}

// nightAdvanceAnnouncementApplyExtra runs the SAME announcement apply the
// generic per-cue engine already applies to the cue's bound action's own
// first node (nightRunCue, via nightAnnouncementDeclaredTarget's clamp),
// again for every ADDITIONAL configured node - this controller's own
// multi-node fan-out for the one step the generic engine cannot itself
// repeat. Uses
// the exact same applyRevision the generic engine pins (never a fresh
// one): every node must apply the identical show.action revision, and
// this is the same params the bound action's own dispatch would send,
// down to source role and mix policy declaration.
//
// Dispatched via [handlers.nightRunAnnouncementApply], NOT
// [handlers.nightRunAudioCommand]: applyRevision is a show.action config
// revision, a different, smaller number space than clear/start's own
// floor+1/floor+2 counter over this SAME announcement history
// (nightAnnouncementRevisions) - reusing nightRunAudioCommand's local
// RevisionState pre-check against that shared history would refuse
// apply's own legitimate, smaller revision as stale. The generic engine's
// own apply for node 0 carries no such local pre-check either (it trusts
// the node's own RevisionState), so nightRunAnnouncementApply mirrors
// that, not nightRunAudioCommand.
func (h *handlers) nightAdvanceAnnouncementApplyExtra(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue) {
	target, applyRevision, ok := h.nightAnnouncementSessionTarget(ctx, cue)
	if !ok || len(target.AudioNodeIDs) <= 1 {
		return
	}
	declared := nightAnnouncementDeclaredTargetFullNodeList(cue, target)
	for _, nodeID := range target.AudioNodeIDs[1:] {
		applyTarget := declared
		applyTarget.AudioNodeIDs = config.AudioNodeIDList{nodeID}
		phase := nightPhaseAnnouncementApplyExtra + ":" + cuePhase + ":" + nodeID
		if err := h.nightRunAnnouncementApply(ctx, now, rec, phase, cue.Name, applyTarget, applyRevision); err != nil {
			h.logWarn("night loop: announcement: extra-node apply failed", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "error", err)
		}
	}
}

// nightRunAnnouncementApply commits (if needed) and dispatches one
// extra-node announcement apply step, mirroring nightRunCue's own
// commit-then-dispatch shape (nightcuerun.go) rather than
// nightRunAudioCommand's (see nightAdvanceAnnouncementApplyExtra's own
// doc comment for why).
func (h *handlers) nightRunAnnouncementApply(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName string, target config.ShowActionTarget, revision int64) error {
	issuer := nightControllerIssuer(rec)
	row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	switch {
	case err == nil:
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return nil
		}
	case errors.Is(err, store.ErrNightCueOutboxNotFound):
		if cerr := h.nightCommitCueRow(ctx, now, rec, phase, cueName, revision); cerr != nil && !errors.Is(cerr, store.ErrNightCueOutboxDuplicate) {
			return cerr
		}
		row, err = h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
		if err != nil {
			return err
		}
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return nil
		}
	default:
		return err
	}
	idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cueName)
	// nil fade: this step is an announcement audio apply, not a lighting cue.
	_, err = h.nightDispatchAndPersistCue(ctx, now, rec, phase, cueName, target, idemKey, issuer, revision, nil)
	return err
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
// nightAnnouncementApplyRowPhase is the outbox phase holding nodeID's own
// apply step for cue under cuePhase: the generic engine's own row
// (cuePhase, unsuffixed) for the bound action's first node, or this
// controller's own extra-node apply phase for every other node.
func nightAnnouncementApplyRowPhase(cuePhase string, target config.ShowActionTarget, nodeID string) string {
	if len(target.AudioNodeIDs) > 0 && target.AudioNodeIDs[0] == nodeID {
		return cuePhase
	}
	return nightPhaseAnnouncementApplyExtra + ":" + cuePhase + ":" + nodeID
}

// nightAdvanceAnnouncementStart dispatches every target node's own start,
// exactly as before ADR-049 decision 3 for a single node (R5's own
// regression requirement): no shared instant is ever selected for one
// node, so [nightAdvanceAnnouncementStartUnscheduled] is untouched. A
// target naming more than one node instead takes
// [nightAdvanceAnnouncementStartScheduled]'s own shared-instant path
// (R3, the frozen ruling).
func (h *handlers) nightAdvanceAnnouncementStart(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue) {
	target, _, ok := h.nightAnnouncementSessionTarget(ctx, cue)
	if !ok {
		return
	}
	history, err := h.nightAnnouncementHistory(ctx, rec)
	if err != nil {
		h.logWarn("night loop: announcement: failed to read announcement-session history for start", "sessionId", rec.ID, "cue", cue.Name, "error", err)
		return
	}
	if len(target.AudioNodeIDs) <= 1 {
		h.nightAdvanceAnnouncementStartUnscheduled(ctx, now, rec, cuePhase, cue, target, history)
		return
	}
	h.nightAdvanceAnnouncementStartScheduled(ctx, now, rec, cuePhase, cue, target, history)
}

// nightAdvanceAnnouncementStartUnscheduled is a single-node announcement's
// own start step, byte-for-byte what this file dispatched before ADR-049
// decision 3 existed: no [pkgaudio.ParamScheduledAtNs], no schedule row,
// gated only on that one node's own apply being terminal.
//
// Gated on the apply row being terminal, not on it having confirmed. An
// apply refused because the node already holds this exact desired state
// is not a reason to leave the announcement silent, and an apply that
// genuinely failed produces a start that fails in its own row, visibly,
// rather than one that is silently never attempted.
func (h *handlers) nightAdvanceAnnouncementStartUnscheduled(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, target config.ShowActionTarget, history []nightBackgroundAudioHistoryRow) {
	for _, nodeID := range target.AudioNodeIDs {
		applyPhase := nightAnnouncementApplyRowPhase(cuePhase, target, nodeID)
		applyRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, applyPhase, cue.Name)
		if err != nil {
			continue
		}
		if applyRow.State != nightCueStateResolved && applyRow.State != nightCueStateAmbiguous {
			continue // this node's own apply has not reached a terminal state yet.
		}
		if applyRow.Outcome != nightCueOutcomeConfirmed {
			h.logWarn("night loop: announcement: the apply did not confirm; starting anyway so a silent announcement is never the quiet outcome", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "outcome", applyRow.Outcome)
		}
		persisted := h.nightAudioSessionPersistedRevision(ctx, nodeID, target.AudioSessionID)
		_, _, startRevision := nightAnnouncementRevisions(persisted)
		start := nightAudioTarget(nodeID, target.AudioSessionID, "audio.session.start", map[string]any{})
		phase := nightPhaseAnnouncementStart + ":" + cuePhase + ":" + nodeID
		if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, start, startRevision, history); err != nil {
			h.logWarn("night loop: announcement: start failed", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "error", err)
		}
	}
}

// nightAdvanceAnnouncementStartScheduled is a multi-node announcement's
// own start step (ADR-049 decision 3, R3, the frozen ruling): every
// listed node starts at ONE shared instant, selected exactly once from
// this announcement's OWN apply/prepare evidence
// ([handlers.nightAnnouncementSchedule], never a separate probe session)
// and durably recorded so a replay tick reads no clock and dispatches
// nothing new.
//
// Gated on EVERY listed node's own apply being terminal first, unlike
// [nightAdvanceAnnouncementStartUnscheduled]'s per-node independent gate:
// a shared instant needs every node's own prepare evidence at once, so
// this tick does nothing at all until every apply has landed, rather
// than starting some nodes ahead of the rest.
func (h *handlers) nightAdvanceAnnouncementStartScheduled(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, target config.ShowActionTarget, history []nightBackgroundAudioHistoryRow) {
	for _, nodeID := range target.AudioNodeIDs {
		applyPhase := nightAnnouncementApplyRowPhase(cuePhase, target, nodeID)
		applyRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, applyPhase, cue.Name)
		if err != nil || (applyRow.State != nightCueStateResolved && applyRow.State != nightCueStateAmbiguous) {
			return // not every node is ready yet; try again next tick.
		}
	}

	scheduledAtNs, unalignedReason, serr := h.nightAnnouncementSchedule(ctx, now, rec, cuePhase, cue, target)
	if serr != nil {
		h.logWarn("night loop: announcement: could not select a shared start instant; trying again next tick", "sessionId", rec.ID, "cue", cue.Name, "error", serr)
		return
	}

	for _, nodeID := range target.AudioNodeIDs {
		applyPhase := nightAnnouncementApplyRowPhase(cuePhase, target, nodeID)
		if applyRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, applyPhase, cue.Name); err == nil && applyRow.Outcome != nightCueOutcomeConfirmed {
			h.logWarn("night loop: announcement: the apply did not confirm; starting anyway so a silent announcement is never the quiet outcome", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "outcome", applyRow.Outcome)
		}
		persisted := h.nightAudioSessionPersistedRevision(ctx, nodeID, target.AudioSessionID)
		_, startRevision := nightAnnouncementScheduleRevisions(persisted)
		params := map[string]any{}
		if scheduledAtNs != nil {
			// json.Number, never an int64 or a float64: matches
			// alignedstart.go's own identical carrying of ScheduledAtNs -
			// this value is around 1.79e18, and a float64 would round it.
			params[pkgaudio.ParamScheduledAtNs] = json.Number(strconv.FormatInt(*scheduledAtNs, 10))
		} else if unalignedReason != "" {
			h.logWarn("night loop: announcement: no shared start instant; starting on arrival", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "reason", unalignedReason)
		}
		start := nightAudioTarget(nodeID, target.AudioSessionID, "audio.session.start", params)
		phase := nightPhaseAnnouncementStart + ":" + cuePhase + ":" + nodeID
		if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, start, startRevision, history); err != nil {
			h.logWarn("night loop: announcement: start failed", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "error", err)
		}
	}
}

// nightAnnouncementScheduleRevisions returns the prepare and start
// revisions a MULTI-NODE announcement's own scheduling attempt mints from
// persistedRevision, continuing directly after [nightAnnouncementRevisions]'s
// own clear (floor+1) and apply (floor+2): prepare = floor+3, start =
// floor+4. A single-node announcement never calls this - it keeps
// nightAnnouncementRevisions' own floor+3 start unchanged (R5's own
// regression requirement), so this never shifts what a single-node
// announcement's start carries.
func nightAnnouncementScheduleRevisions(persistedRevision int64) (prepareRevision, startRevision int64) {
	floor := persistedRevision
	return floor + 3, floor + 4
}

// nightRunAnnouncementPrepare dispatches nodeID's own audio.session.prepare
// against the announcement's REAL session (R3's own "no separate probe
// session" rule - unlike the Cue path's throwaway probe,
// cueactivationschedule.go, an announcement reads its OWN apply/prepare
// evidence), as one durable outbox row under phase, exactly once: a row
// already resolved or ambiguous returns (nil, nil) without dispatching
// again, since [handlers.nightAnnouncementSchedule] - this function's
// only caller - never re-enters once its own schedule row is persisted.
//
// evidence is the node's own prepare result evidence map, present only on
// a FRESH dispatch (nil on a replay hit, or on any resolved outcome that
// carried none): the schedule DECISION this evidence feeds is itself the
// durable record a later tick reads back, not this row's own resolved
// state, so a second caller finding this row already resolved correctly
// gets nothing back to build a new decision from.
func (h *handlers) nightRunAnnouncementPrepare(ctx context.Context, now time.Time, rec store.NightSessionRecord, phase, cueName, nodeID string, target config.ShowActionTarget, revision int64) (map[string]any, error) {
	issuer := nightControllerIssuer(rec)
	row, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	switch {
	case err == nil:
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return nil, nil
		}
	case errors.Is(err, store.ErrNightCueOutboxNotFound):
		if cerr := h.nightCommitCueRow(ctx, now, rec, phase, cueName, revision); cerr != nil && !errors.Is(cerr, store.ErrNightCueOutboxDuplicate) {
			return nil, cerr
		}
		row, err = h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
		if err != nil {
			return nil, err
		}
		if row.State == nightCueStateResolved || row.State == nightCueStateAmbiguous {
			return nil, nil
		}
	default:
		return nil, err
	}

	if row.State == nightCueStatePending {
		t := now
		row.State = nightCueStateDispatched
		row.DispatchedAt = &t
		if uerr := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); uerr != nil {
			return nil, uerr
		}
	}

	idemKey := nightCueIdempotencyKey(rec.ID, rec.Cycle, phase, cueName)
	var evidence map[string]any
	result, problem, derr := h.executeAudioSessionDispatch(ctx, now, AudioDispatchInput{
		Action: "audio.session.prepare", NodeID: nodeID, SessionID: target.AudioSessionID,
		Params:   map[string]any{"sessionId": target.AudioSessionID, "invocationId": idemKey, "revision": uint64(revision)},
		Revision: uint64(revision), IdempotencyKey: idemKey,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
		OnEvidence: func(v map[string]any) { evidence = v },
	})

	row, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	if rerr != nil {
		return evidence, rerr
	}
	if derr != nil {
		if !errors.Is(derr, broker.ErrResponseFailedBeforePublish) {
			// Published, but the outcome is genuinely unknown: leave the
			// row dispatched for a later attempt to retry under the SAME
			// idempotency key, mirroring nightDispatchCueAudio's own
			// identical branch (audiodispatch.go's own await-result error
			// case).
			return evidence, derr
		}
		row.State = nightCueStateResolved
		row.Outcome = nightCueOutcomeFailed
		row.OutcomeReason = "this announcement's own prepare could not be dispatched: " + derr.Error()
		resolvedAt := now
		row.ResolvedAt = &resolvedAt
		if uerr := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); uerr != nil {
			return evidence, uerr
		}
		return evidence, derr
	}
	row.State = nightCueStateResolved
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	if problem != nil {
		row.Outcome = nightCueOutcomeRefused
		row.OutcomeReason = problem.Detail
	} else {
		row.Outcome = result.Outcome
		row.OutcomeReason = result.Reason
	}
	if uerr := h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row); uerr != nil {
		return evidence, uerr
	}
	return evidence, nil
}

// nightAnnouncementSchedule is ADR-049 decision 3's own multi-node
// scheduling step for an announcement (R3, the frozen ruling): read every
// listed node's OWN fresh apply/prepare evidence, choose ONE shared
// instant through [SelectAudioStartInstant], and record the decision as
// its own durable [nightPhaseAnnouncementSchedule] row so a later tick
// reads it back instead of reading any clock or dispatching anything new.
//
// Called once per announcement run: every subsequent call for the SAME
// (rec.Cycle, cuePhase, cue.Name) finds that row already resolved and
// returns straight from it, via [nightAnnouncementScheduleFromRow],
// without dispatching a fresh prepare on any node.
func (h *handlers) nightAnnouncementSchedule(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, target config.ShowActionTarget) (scheduledAtNs *int64, unalignedReason string, err error) {
	phase := nightPhaseAnnouncementSchedule + ":" + cuePhase
	if row, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name); rerr == nil {
		return nightAnnouncementScheduleFromRow(row)
	} else if !errors.Is(rerr, store.ErrNightCueOutboxNotFound) {
		return nil, "", rerr
	}

	settings, serr := h.alignedStartSettings(ctx)
	if serr != nil {
		return nil, "", fmt.Errorf("read audio.settings for the announcement's own shared start instant: %w", serr)
	}

	read := func(readCtx context.Context, nodeID string) (reading AudioStartInstantReading, err error) {
		holdsClock, herr := h.nodeHoldsMediaClock(readCtx, nodeID)
		if herr != nil {
			return AudioStartInstantReading{}, herr
		}
		defer func() {
			if r := recover(); r != nil {
				reading = AudioStartInstantReading{HoldsMediaClock: holdsClock}
				err = fmt.Errorf("panic reading node %q's own announcement prepare: %v", nodeID, r)
			}
		}()
		persisted := h.nightAudioSessionPersistedRevision(readCtx, nodeID, target.AudioSessionID)
		prepareRevision, _ := nightAnnouncementScheduleRevisions(persisted)
		prepPhase := nightPhaseAnnouncementPrepare + ":" + cuePhase + ":" + nodeID
		started := time.Now()
		evidence, perr := h.nightRunAnnouncementPrepare(readCtx, now, rec, prepPhase, cue.Name, nodeID, target, prepareRevision)
		if perr != nil {
			return AudioStartInstantReading{HoldsMediaClock: holdsClock}, perr
		}
		return AudioStartInstantReading{HoldsMediaClock: holdsClock, Evidence: evidence, ProbeElapsed: time.Since(started)}, nil
	}

	sel, nodeErrs, selErr := SelectAudioStartInstant(ctx, target.AudioNodeIDs, settings, read)
	for _, ne := range nodeErrs {
		h.logWarn("night loop: announcement: node's own prepare reading failed", "sessionId", rec.ID, "cue", cue.Name, "nodeId", ne.NodeID, "error", ne.Err)
	}

	row := store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: phase, CueName: cue.Name,
		State: nightCueStateResolved,
	}
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	if selErr != nil {
		var noClock *audiosched.ErrNoUsableClock
		if !errors.As(selErr, &noClock) {
			h.logWarn("night loop: announcement: select start instant failed; every node starts on arrival", "sessionId", rec.ID, "cue", cue.Name, "error", selErr)
		}
		row.Outcome = nightAnnouncementScheduleUnaligned
		row.OutcomeReason = audiosched.DescribeUnscheduled(selErr)
	} else {
		row.Outcome = nightAnnouncementScheduleAligned
		row.OutcomeReason = strconv.FormatInt(sel.ScheduledAtNs, 10)
	}

	if ierr := h.deps.NightSessions.InsertNightCueOutboxRow(ctx, row, now); ierr != nil && !errors.Is(ierr, store.ErrNightCueOutboxDuplicate) {
		return nil, "", ierr
	}
	persistedRow, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name)
	if rerr != nil {
		return nil, "", rerr
	}
	return nightAnnouncementScheduleFromRow(persistedRow)
}

// nightAnnouncementScheduleFromRow decodes a persisted
// [nightPhaseAnnouncementSchedule] row back into
// [handlers.nightAnnouncementSchedule]'s own return shape.
func nightAnnouncementScheduleFromRow(row store.NightCueOutboxRecord) (scheduledAtNs *int64, unalignedReason string, err error) {
	if row.Outcome != nightAnnouncementScheduleAligned {
		return nil, row.OutcomeReason, nil
	}
	ns, perr := strconv.ParseInt(row.OutcomeReason, 10, 64)
	if perr != nil {
		return nil, "", fmt.Errorf("api: announcement schedule row %q carries an unparsable instant %q: %w", row.ID, row.OutcomeReason, perr)
	}
	return &ns, "", nil
}

// nightAnnouncementMedia is R6, the frozen ruling: an announcement's
// audio.session.apply target.params already carry "media": {assetId,
// contentHash, filename, sizeBytes}, the same shape a bed item's own
// media reference uses (nightBuildBackgroundPlaylistItems,
// nightbackgroundaudio.go). Complete reports whether every field a
// caller needs to trust this reference was actually present.
type nightAnnouncementMedia struct {
	AssetID     string
	ContentHash string
	Filename    string
	SizeBytes   int64
}

func (m nightAnnouncementMedia) Complete() bool {
	return m.AssetID != "" && m.ContentHash != "" && m.Filename != ""
}

// nightAnnouncementMediaRef reads target.params["media"] back (R6): no
// resolution through a (show, sequence, target) lookup, since an
// announcement has no sequence identity, only this one pinned file. An
// absent or incomplete reference decodes to the zero value; the caller
// (readiness) reports that as not_verifiable, never invented.
func nightAnnouncementMediaRef(params map[string]any) nightAnnouncementMedia {
	media, _ := params["media"].(map[string]any)
	assetID, _ := media["assetId"].(string)
	contentHash, _ := media["contentHash"].(string)
	filename, _ := media["filename"].(string)
	var sizeBytes int64
	switch v := media["sizeBytes"].(type) {
	case float64:
		sizeBytes = int64(v)
	case json.Number:
		sizeBytes, _ = v.Int64()
	case int64:
		sizeBytes = v
	}
	return nightAnnouncementMedia{AssetID: assetID, ContentHash: contentHash, Filename: filename, SizeBytes: sizeBytes}
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
	if len(target.AudioNodeIDs) == 0 {
		return 0, false
	}
	persisted := h.nightAudioSessionPersistedRevision(ctx, target.AudioNodeIDs[0], target.AudioSessionID)
	_, applyRevision, _ := nightAnnouncementRevisions(persisted)
	return applyRevision, true
}
