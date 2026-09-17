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

// An announcement cue's duck/mix/interrupt policy is declared on the
// session's own audio.session.apply and enforced by the audio node; this controller never drives duck itself, since a
// coordinator-driven fade stacked under the node's own duck strands the bed at duck gain.

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
// announcement policy materialized onto its own AnnouncementPolicy field;
// every other role is returned unchanged.
func nightAnnouncementCueWithResolvedPolicy(cue config.NightSessionCue, payload config.NightSessionPayload) config.NightSessionCue {
	if cue.Role != config.NightSessionCueRoleAnnouncement {
		return cue
	}
	policy := nightAnnouncementPolicy(cue, payload)
	cue.AnnouncementPolicy = &policy
	return cue
}

// nightAnnouncementDeclaredTarget declares source role "announcement" and
// the configured mix policy on the cue's own dispatch, then clamps AudioNodeIDs to its first element: the generic
// per-cue engine only ever dispatches to one target, so this file owns every other listed node's own fan-out.
func nightAnnouncementDeclaredTarget(cue config.NightSessionCue, target config.ShowActionTarget) config.ShowActionTarget {
	target = nightAnnouncementDeclareParams(cue, target)
	if len(target.AudioNodeIDs) > 1 {
		target.AudioNodeIDs = target.AudioNodeIDs[:1]
	}
	return target
}

// nightAnnouncementDeclaredTargetFullNodeList is
// [nightAnnouncementDeclaredTarget]'s own param-declaration logic without
// its first-node clamp, for every additional node's own single-node target.
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

// nightAnnouncementTargetDeclarable reports whether target is a dispatch a
// source role and mix policy can be declared on at all: only
// audio.session.apply carries an ApplyRequest to merge them into.
func nightAnnouncementTargetDeclarable(target config.ShowActionTarget) bool {
	return target.Integration == config.ShowActionIntegrationAudio && target.AudioAction == "audio.session.apply"
}

// The announcement session's own durable steps: this controller's own
// clear runs before the cue's own audio.session.apply, followed by this
// controller's own audio.session.start, since an apply alone never leaves a session Playing.
const (
	nightPhaseAnnouncementSession = "announcementSession"
	nightPhaseAnnouncementClear   = nightPhaseAnnouncementSession + ":clear"
	nightPhaseAnnouncementStart   = nightPhaseAnnouncementSession + ":start"

	// nightPhaseAnnouncementApplyExtra is this controller's own extra apply,
	// for every target node beyond the cue's bound action's own first node;
	// never dispatched when a target names only one node.
	nightPhaseAnnouncementApplyExtra = nightPhaseAnnouncementSession + ":applyExtra"

	// nightPhaseAnnouncementPrepare is ADR-049 decision 3's own per-node
	// prepare step, suffixed ":"+cuePhase+":"+nodeID+":"+attempt so a
	// retried scheduling attempt dispatches fresh rather than replaying a resolved row's discarded evidence.
	nightPhaseAnnouncementPrepare = nightPhaseAnnouncementSession + ":prepare"

	// nightPhaseAnnouncementSchedule holds the one durable decision a
	// multi-node announcement's own scheduling attempt makes (":"+cuePhase,
	// no node suffix); see [handlers.nightAnnouncementSchedule]'s own doc comment.
	nightPhaseAnnouncementSchedule = nightPhaseAnnouncementSession + ":schedule"
)

// nightAnnouncementScheduleAligned and nightAnnouncementScheduleUnaligned
// are the schedule row's own terminal outcomes; the row's own
// OutcomeReason is a JSON-encoded [nightAnnouncementScheduleDecision].
const (
	nightAnnouncementScheduleAligned   = "aligned"
	nightAnnouncementScheduleUnaligned = "unaligned"
)

// nightAnnouncementScheduleDecision is the [nightPhaseAnnouncementSchedule]
// row's own durable payload: a replay tick decodes the identical per-node
// decision back from it, without rereading any clock.
type nightAnnouncementScheduleDecision struct {
	ScheduledAtNs   *int64            `json:"scheduledAtNs,omitempty"`
	UnalignedReason string            `json:"unalignedReason,omitempty"`
	FailedNodes     map[string]string `json:"failedNodes,omitempty"`
}

// The step kinds these phases surface, distinct from background audio's own so an operator reading one step
// list can tell which sequence a failure belongs to. nightAnnouncementStepApply is shared with the generic engine's own row for the cue's first node.
const (
	nightAnnouncementStepClear = "announcementClear"
	nightAnnouncementStepStart = "announcementStart"
	nightAnnouncementStepApply = "announcementApply"
)

// nightAnnouncementKnownCuePhases are the only cuePhase values this
// package ever suffixes an announcement phase string with, so
// nightAnnouncementNodeFromPhase can split a trailing nodeID safely.
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

// nightAnnouncementHistory returns every announcement-session step
// recorded for rec, across every cycle and cue, so revisions stay
// strictly increasing regardless of which cue or session a row belongs to.
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

// nightAnnouncementRevisions computes clear/apply/start as floor+1/+2/+3
// above nodeID's own persisted audio_sessions revision: strictly
// increasing, never stale from anything this coordinator itself already sent.
func nightAnnouncementRevisions(persistedRevision int64) (clearRevision, applyRevision, startRevision int64) {
	floor := persistedRevision
	return floor + 1, floor + 2, floor + 3
}

// nightAudioSessionPersistedRevision reads nodeID's own durable
// audio_sessions row for sessionID, or 0 when none exists yet: keyed by
// (node_id, id), so each target node's own floor is independent.
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
// already has an outbox row for rec's current cycle: once it does, a
// later clear this cycle would cut off an announcement already playing.
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

// nightAdvanceAnnouncementClear runs before an announcement cue's own
// dispatch, for every one of the cue's own target nodes: clearing a
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

// nightAdvanceAnnouncementApplyExtra runs the cue's own announcement apply
// again for every node beyond the bound action's own first, using the
// SAME applyRevision and params the generic engine already dispatched for that first node.
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
// commit-then-dispatch shape rather than nightRunAudioCommand's.
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

// nightAnnouncementApplyRowPhase is the outbox phase holding nodeID's own
// apply step for cue under cuePhase: the generic engine's own row for the
// bound action's first node, or this controller's own extra-node apply phase.
func nightAnnouncementApplyRowPhase(cuePhase string, target config.ShowActionTarget, nodeID string) string {
	if len(target.AudioNodeIDs) > 0 && target.AudioNodeIDs[0] == nodeID {
		return cuePhase
	}
	return nightPhaseAnnouncementApplyExtra + ":" + cuePhase + ":" + nodeID
}

// nightAdvanceAnnouncementStart dispatches every target node's own start.
// A target naming one node keeps [nightAdvanceAnnouncementStartUnscheduled]'s
// pre-ADR-049 behavior; more than one takes the shared-instant path (ADR-049 decisions 3, 4, 6).
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
// own start step, unchanged from before ADR-049 decision 3: no shared
// instant, no schedule row, gated only on that node's own apply being terminal.
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

// nightAdvanceAnnouncementStartScheduled is a multi-node announcement's own start step (ADR-049 decision 3): every
// node starts at the shared instant [handlers.nightAnnouncementSchedule] chose, except a node whose own schedule
// read failed, which starts on arrival with its own reason instead.
func (h *handlers) nightAdvanceAnnouncementStartScheduled(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, target config.ShowActionTarget, history []nightBackgroundAudioHistoryRow) {
	for _, nodeID := range target.AudioNodeIDs {
		applyPhase := nightAnnouncementApplyRowPhase(cuePhase, target, nodeID)
		applyRow, err := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, applyPhase, cue.Name)
		if err != nil || (applyRow.State != nightCueStateResolved && applyRow.State != nightCueStateAmbiguous) {
			return // not every node is ready yet; try again next tick.
		}
	}

	scheduledAtNs, unalignedReason, failedNodes, serr := h.nightAnnouncementSchedule(ctx, now, rec, cuePhase, cue, target)
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
		switch {
		case failedNodes[nodeID] != "":
			// ADR-049 decision 4: a node this coordinator never got fresh
			// evidence from is never told to wait for someone else's
			// instant, even when the rest of the group aligned.
			h.logWarn("night loop: announcement: this node's own schedule reading failed; starting on arrival", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "reason", failedNodes[nodeID])
		case scheduledAtNs != nil:
			// json.Number, never an int64 or a float64: matches
			// alignedstart.go's own identical carrying of ScheduledAtNs -
			// this value is around 1.79e18, and a float64 would round it.
			params[pkgaudio.ParamScheduledAtNs] = json.Number(strconv.FormatInt(*scheduledAtNs, 10))
		case unalignedReason != "":
			h.logWarn("night loop: announcement: no shared start instant; starting on arrival", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "reason", unalignedReason)
		}
		start := nightAudioTarget(nodeID, target.AudioSessionID, "audio.session.start", params)
		phase := nightPhaseAnnouncementStart + ":" + cuePhase + ":" + nodeID
		if _, err := h.nightRunAudioCommand(ctx, now, rec, phase, cue.Name, start, startRevision, history); err != nil {
			h.logWarn("night loop: announcement: start failed", "sessionId", rec.ID, "cue", cue.Name, "nodeId", nodeID, "error", err)
		}
	}
}

// nightAnnouncementScheduleRevisions computes prepare (floor+3) and start
// (floor+4) from persistedRevision, read fresh at each call: a confirmed
// prepare advances that floor first, so start's own floor never falls behind it.
func nightAnnouncementScheduleRevisions(persistedRevision int64) (prepareRevision, startRevision int64) {
	floor := persistedRevision
	return floor + 3, floor + 4
}

// nightRunAnnouncementPrepare dispatches nodeID's own audio.session.prepare against the announcement's real
// session, bounded to scheduleProbeStepTimeout (cueactivationschedule.go): a node that does not answer within it
// is reported as a failed read and left to resolve in the background.
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
	pending := h.dispatchProbeStep(ctx, now, AudioDispatchInput{
		Action: "audio.session.prepare", NodeID: nodeID, SessionID: target.AudioSessionID,
		Params:   map[string]any{"sessionId": target.AudioSessionID, "invocationId": idemKey, "revision": uint64(revision)},
		Revision: uint64(revision), IdempotencyKey: idemKey,
		IssuerID: issuer.PrincipalID, IssuerName: issuer.PrincipalName,
		IssuerForm: issuer.Form, IssuerCredentialID: issuer.CredentialID,
		OnEvidence: func(v map[string]any) { evidence = v },
	})
	out, timedOut := awaitProbeStep(pending)
	if timedOut {
		h.logWarn("night loop: announcement: prepare dispatch timed out; this node is treated as a failed read", "sessionId", rec.ID, "cue", cueName, "nodeId", nodeID, "timeout", scheduleProbeStepTimeout)
		bgCtx := context.WithoutCancel(ctx)
		go h.finishAnnouncementPrepareAfter(bgCtx, rec, phase, cueName, pending)
		return nil, fmt.Errorf("node %q's own prepare did not answer within %s", nodeID, scheduleProbeStepTimeout)
	}
	if uerr := h.persistAnnouncementPrepareOutcome(ctx, rec, phase, cueName, now, out); uerr != nil {
		return evidence, uerr
	}
	return evidence, out.err
}

// persistAnnouncementPrepareOutcome writes out's own dispatch result onto phase's outbox row: a dispatch that
// never published resolves failed, one whose outcome is genuinely unknown is left dispatched for a retry under
// the same idempotency key, and everything else resolves from the node's own result or refusal.
func (h *handlers) persistAnnouncementPrepareOutcome(ctx context.Context, rec store.NightSessionRecord, phase, cueName string, now time.Time, out audioDispatchOutcome) error {
	row, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cueName)
	if rerr != nil {
		return rerr
	}
	resolvedAt := now
	if out.err != nil {
		if !errors.Is(out.err, broker.ErrResponseFailedBeforePublish) {
			return nil
		}
		row.State = nightCueStateResolved
		row.Outcome = nightCueOutcomeFailed
		row.OutcomeReason = "this announcement's own prepare could not be dispatched: " + out.err.Error()
		row.ResolvedAt = &resolvedAt
		return h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row)
	}
	row.State = nightCueStateResolved
	row.ResolvedAt = &resolvedAt
	if out.problem != nil {
		row.Outcome = nightCueOutcomeRefused
		row.OutcomeReason = out.problem.Detail
	} else {
		row.Outcome = out.result.Outcome
		row.OutcomeReason = out.result.Reason
	}
	return h.deps.NightSessions.UpdateNightCueOutboxRow(ctx, row)
}

// finishAnnouncementPrepareAfter is nightRunAnnouncementPrepare's own follow-up for a prepare step it gave up
// waiting for: it waits for that step's real completion, however long that takes, and only then persists phase's
// own outbox row, mirroring finishScheduleProbeAfter's own abandon-and-continue shape.
func (h *handlers) finishAnnouncementPrepareAfter(ctx context.Context, rec store.NightSessionRecord, phase, cueName string, pending <-chan audioDispatchOutcome) {
	defer func() {
		if r := recover(); r != nil {
			h.logWarn("night loop: announcement: panic finishing an abandoned prepare step", "sessionId", rec.ID, "cue", cueName, "panic", r)
		}
	}()
	out := <-pending
	if uerr := h.persistAnnouncementPrepareOutcome(ctx, rec, phase, cueName, time.Now(), out); uerr != nil {
		h.logWarn("night loop: announcement: failed to persist an abandoned prepare's own outcome", "sessionId", rec.ID, "cue", cueName, "error", uerr)
	}
}

// nightAnnouncementSchedule is ADR-049 decision 3's own multi-node
// scheduling step: choose one shared instant from every listed node's
// fresh prepare evidence, and persist the decision once so a replay tick reads it back instead of dispatching anything new.
func (h *handlers) nightAnnouncementSchedule(ctx context.Context, now time.Time, rec store.NightSessionRecord, cuePhase string, cue config.NightSessionCue, target config.ShowActionTarget) (scheduledAtNs *int64, unalignedReason string, failedNodes map[string]string, err error) {
	phase := nightPhaseAnnouncementSchedule + ":" + cuePhase
	if row, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name); rerr == nil {
		return nightAnnouncementScheduleFromRow(row)
	} else if !errors.Is(rerr, store.ErrNightCueOutboxNotFound) {
		return nil, "", nil, rerr
	}

	settings, serr := h.alignedStartSettings(ctx)
	if serr != nil {
		return nil, "", nil, fmt.Errorf("read audio.settings for the announcement's own shared start instant: %w", serr)
	}

	// attempt is fresh for every call that reaches here (no schedule row
	// yet): it namespaces this attempt's own prepare rows so a retry after
	// a failed schedule-row insert dispatches fresh instead of replaying a resolved row's discarded evidence.
	attempt := uuid.NewString()
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
		prepPhase := nightPhaseAnnouncementPrepare + ":" + cuePhase + ":" + nodeID + ":" + attempt
		started := time.Now()
		evidence, perr := h.nightRunAnnouncementPrepare(readCtx, now, rec, prepPhase, cue.Name, nodeID, target, prepareRevision)
		if perr != nil {
			return AudioStartInstantReading{HoldsMediaClock: holdsClock}, perr
		}
		return AudioStartInstantReading{HoldsMediaClock: holdsClock, Evidence: evidence, ProbeElapsed: time.Since(started)}, nil
	}

	sel, nodeErrs, selErr := SelectAudioStartInstant(ctx, target.AudioNodeIDs, settings, read)
	failed := make(map[string]string, len(nodeErrs))
	for _, ne := range nodeErrs {
		h.logWarn("night loop: announcement: node's own prepare reading failed", "sessionId", rec.ID, "cue", cue.Name, "nodeId", ne.NodeID, "error", ne.Err)
		failed[ne.NodeID] = ne.Err.Error()
	}

	row := store.NightCueOutboxRecord{
		ID: uuid.NewString(), SessionID: rec.ID, Cycle: rec.Cycle, Phase: phase, CueName: cue.Name,
		State: nightCueStateResolved,
	}
	resolvedAt := now
	row.ResolvedAt = &resolvedAt
	var decision nightAnnouncementScheduleDecision
	if selErr != nil {
		var noClock *audiosched.ErrNoUsableClock
		if !errors.As(selErr, &noClock) {
			h.logWarn("night loop: announcement: select start instant failed; every node starts on arrival", "sessionId", rec.ID, "cue", cue.Name, "error", selErr)
		}
		row.Outcome = nightAnnouncementScheduleUnaligned
		decision.UnalignedReason = audiosched.DescribeUnscheduled(selErr)
	} else {
		row.Outcome = nightAnnouncementScheduleAligned
		at := sel.ScheduledAtNs
		decision.ScheduledAtNs = &at
		if len(failed) > 0 {
			decision.FailedNodes = failed
		}
	}
	encoded, merr := json.Marshal(decision)
	if merr != nil {
		return nil, "", nil, fmt.Errorf("api: encode announcement schedule decision: %w", merr)
	}
	row.OutcomeReason = string(encoded)

	if ierr := h.deps.NightSessions.InsertNightCueOutboxRow(ctx, row, now); ierr != nil && !errors.Is(ierr, store.ErrNightCueOutboxDuplicate) {
		return nil, "", nil, ierr
	}
	persistedRow, rerr := h.deps.NightSessions.GetNightCueOutboxRow(ctx, rec.ID, rec.Cycle, phase, cue.Name)
	if rerr != nil {
		return nil, "", nil, rerr
	}
	return nightAnnouncementScheduleFromRow(persistedRow)
}

// nightAnnouncementScheduleFromRow decodes a persisted
// [nightPhaseAnnouncementSchedule] row back into
// [handlers.nightAnnouncementSchedule]'s own return shape.
func nightAnnouncementScheduleFromRow(row store.NightCueOutboxRecord) (scheduledAtNs *int64, unalignedReason string, failedNodes map[string]string, err error) {
	var decision nightAnnouncementScheduleDecision
	if derr := json.Unmarshal([]byte(row.OutcomeReason), &decision); derr != nil {
		return nil, "", nil, fmt.Errorf("api: announcement schedule row %q carries an undecodable decision %q: %w", row.ID, row.OutcomeReason, derr)
	}
	if row.Outcome != nightAnnouncementScheduleAligned {
		return nil, decision.UnalignedReason, nil, nil
	}
	return decision.ScheduledAtNs, "", decision.FailedNodes, nil
}

// nightAnnouncementMedia is ADR-049 decision 9: an announcement's
// audio.session.apply target.params carry "media": {assetId, contentHash,
// filename, sizeBytes}, the same shape a bed item's own media reference uses.
type nightAnnouncementMedia struct {
	AssetID     string
	ContentHash string
	Filename    string
	SizeBytes   int64
}

func (m nightAnnouncementMedia) Complete() bool {
	return m.AssetID != "" && m.ContentHash != "" && m.Filename != ""
}

// nightAnnouncementMediaRef reads target.params["media"] back (ADR-049
// decision 9): an absent or incomplete reference decodes to the zero
// value, which the caller (readiness) reports as not_verifiable, never invented.
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
// reports it only when it is an announcement whose target this controller
// can run the apply-then-start sequence against; everything else returns false.
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

// nightAnnouncementApplyDispatchRevision reports the audio-session dispatch revision an announcement apply must
// carry: the same [nightAnnouncementRevisions] floor clear and start already draw from, so all three advance
// together. ok is false for every non-announcement or non-declarable cue.
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
