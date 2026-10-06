package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackhold"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
	"github.com/showmeshsystems/showmesh/pkg/observation"
)

// An FPP plugin reports whether it is running the show from its fallback
// program, and an operator can clear what the coordinator stored about it.
// FPP-PLUGIN-COORDINATOR-CONTRACTS.md sections 5.15 and 5.16.

const (
	auditActionFallbackPlayerStateReport = "fallback.player_state.report"
	auditActionFallbackPlayerStateClear  = "fallback.player_state.clear"
)

const maxFallbackStateReportRequestBodyBytes = 4 << 10 // 4 KiB

const fallbackStateReportSchemaVersion = 1

func fallbackStateReportNotPairedProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeForbidden,
		Title:  "Forbidden",
		Status: http.StatusForbidden,
		Detail: "Only the plugin paired with this FPP player can report its fallback state. Pair the plugin with this player and try again.",
	}
}

func fallbackStateReportIdentityUnknownProblem() v1.Problem {
	return v1.Problem{
		Type:   ProblemTypeConflict,
		Title:  "FPP player identity not read yet",
		Status: http.StatusConflict,
		Detail: "The coordinator has not read this FPP player's identity yet, so it cannot accept the plugin's report. Check that the coordinator can reach the player.",
	}
}

// decodeFallbackStateReport reads and validates a report body. A member
// this coordinator does not know is ignored, so a newer plugin still works.
func decodeFallbackStateReport(r *http.Request, instanceUUID string) (fallbackhold.Report, *v1.Problem) {
	refuse := func(detail string) (fallbackhold.Report, *v1.Problem) {
		p := invalidParameterProblem(detail)
		return fallbackhold.Report{}, &p
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxFallbackStateReportRequestBodyBytes+1))
	if err != nil {
		return refuse("could not read the request body: " + err.Error())
	}
	if len(raw) > maxFallbackStateReportRequestBodyBytes {
		return refuse("the request body is larger than 4096 bytes")
	}
	var req v1.FallbackStateReportRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return refuse("malformed request body: " + err.Error())
	}
	if req.SchemaVersion != fallbackStateReportSchemaVersion {
		return refuse("schemaVersion must be 1")
	}
	if parsed, err := uuid.Parse(req.BootID); err != nil || parsed.String() != req.BootID {
		return refuse("bootId must be a UUID in its 36 character lowercase form")
	}
	if req.Sequence < 1 {
		return refuse("sequence must be at least 1")
	}
	if !fallbackhold.ValidState(req.State) {
		return refuse(`state must be one of "normal", "fallback", "resting"`)
	}
	since, err := time.Parse(time.RFC3339, req.Since)
	if err != nil {
		return refuse("since must be an RFC 3339 time")
	}
	if req.State == fallbackhold.StateNormal {
		if req.PlaylistName != "" || req.PackageID != "" || req.PackageRevision != "" || req.CutoffAt != "" {
			return refuse("playlistName, packageId, packageRevision and cutoffAt must be absent when state is normal")
		}
	} else {
		if req.PlaylistName == "" || req.PackageID == "" || req.PackageRevision == "" || req.CutoffAt == "" {
			return refuse("playlistName, packageId, packageRevision and cutoffAt are required when state is fallback or resting")
		}
		if _, err := time.Parse(time.RFC3339, req.CutoffAt); err != nil {
			return refuse("cutoffAt must be an RFC 3339 time")
		}
	}
	return fallbackhold.Report{
		FPPInstanceUUID: instanceUUID, BootID: req.BootID, Sequence: req.Sequence, State: req.State, Since: since.UTC(),
		PlaylistName: req.PlaylistName, PackageID: req.PackageID, PackageRevision: req.PackageRevision, CutoffAt: req.CutoffAt,
	}, nil
}

// handlePutFallbackState serves
// PUT /api/v1/fallback-programs/{fppInstanceId}/fallback-state.
func (h *handlers) handlePutFallbackState(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)
	instanceUUID := r.PathValue("fppInstanceId")
	if instanceUUID == "" {
		writeProblem(w, h.logger, now, invalidParameterProblem("fppInstanceId is required"))
		return
	}
	report, problem := decodeFallbackStateReport(r, instanceUUID)
	if problem != nil {
		writeProblem(w, h.logger, now, *problem)
		return
	}
	if h.deps.FallbackPrograms == nil || h.deps.FallbackHolds == nil {
		h.writeInternalError(w, now, "record fallback state report", errFallbackProgramStoreNotWired)
		return
	}
	caller, endpointID, err := h.classifyFallbackExecutorKeyCaller(ctx, ac.result.Principal, instanceUUID)
	if err != nil {
		h.writeInternalError(w, now, "resolve the caller's FPP player", err)
		return
	}
	switch caller {
	case fallbackExecutorKeyCallerIdentityUnknown:
		h.logWarn("refused a fallback state report: the coordinator has not read this FPP player's identity yet",
			"fppId", endpointID, "fppInstanceUuid", instanceUUID)
		writeProblem(w, h.logger, now, fallbackStateReportIdentityUnknownProblem())
		return
	case fallbackExecutorKeyCallerOther:
		writeProblem(w, h.logger, now, fallbackStateReportNotPairedProblem())
		return
	}

	result, err := h.deps.FallbackHolds.Record(ctx, report, now)
	if err != nil {
		h.writeInternalError(w, now, "record fallback state report", err)
		return
	}
	if result.Recorded && result.StateChanged {
		h.logWarn("an FPP player's plugin reported a new fallback state",
			"fppId", endpointID, "fppInstanceUuid", instanceUUID, "state", report.State, "previousState", result.PreviousState)
		if h.deps.Identity != nil {
			if err := h.deps.Identity.WriteAudit(ctx, identity.AuditEntry{
				Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
				Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
				Action: auditActionFallbackPlayerStateReport, Target: instanceUUID, Kind: identity.AuditOutcome,
				Params: map[string]any{
					"state": report.State, "previousState": result.PreviousState, "playlistName": report.PlaylistName,
					"packageId": report.PackageID, "cutoffAt": report.CutoffAt,
				},
				OutcomeReason: "reported " + report.State,
			}); err != nil {
				h.logWarn("fallback state report audit write failed", "fppInstanceUuid", instanceUUID, "error", err)
			}
		}
		h.deps.FallbackHolds.WriteSignals(ctx, now)
		// A hand-back lets the Cue loop act again without waiting a tick.
		h.deps.CueActivationNudger.Nudge()
	}
	jsonWrite(w, v1.FallbackStateReportResponse{
		ServerTime: formatTime(now), FPPInstanceUUID: instanceUUID, Recorded: result.Recorded, State: result.Stored.State,
	})
}

// handleDeleteFallbackState serves
// DELETE /api/v1/fallback-programs/{fppInstanceId}/fallback-state: an
// operator's way out of a hold the plugin will not end. A plugin that is
// still reporting stores its state again with its next report. Nothing
// observed before the clear is acted on afterwards.
func (h *handlers) handleDeleteFallbackState(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)
	instanceUUID := r.PathValue("fppInstanceId")

	var deleted bool
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		if h.deps.FallbackHolds != nil {
			var err error
			if deleted, err = h.deps.FallbackHolds.Clear(ctx, tx, instanceUUID, now); err != nil {
				return identity.AuditEntry{}, err
			}
		}
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: auditActionFallbackPlayerStateClear, Target: instanceUUID,
			Params: map[string]any{"deleted": deleted},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "clear fallback player state", writeErr)
		return
	}
	if deleted && h.deps.FallbackHolds != nil {
		h.deps.FallbackHolds.ForgetSignals(ctx, instanceUUID, now)
		h.deps.CueActivationNudger.Nudge()
	}
	w.WriteHeader(http.StatusNoContent)
}

// fallbackVerdict answers whether the player is held. With no hold service
// wired no player is ever held.
func (h *handlers) fallbackVerdict(ctx context.Context, instanceUUID string, now time.Time) (fallbackhold.Verdict, error) {
	if h.deps.FallbackHolds == nil || instanceUUID == "" {
		return fallbackhold.Verdict{}, nil
	}
	return h.deps.FallbackHolds.Evaluate(ctx, instanceUUID, now)
}

// mapFallbackPlayerState renders v for the wire, nil when the player's
// plugin has never reported.
func mapFallbackPlayerState(v fallbackhold.Verdict, now time.Time) *v1.FallbackPlayerState {
	if !v.Reported {
		return nil
	}
	out := &v1.FallbackPlayerState{
		State: v.Record.State, Since: formatTime(v.Record.Since), PlaylistName: v.Record.PlaylistName,
		PackageID: v.Record.PackageID, PackageRevision: v.Record.PackageRevision, CutoffAt: v.Record.CutoffAt,
		ReportedAt: formatTime(v.Record.ReceivedAt), PluginReporting: v.PluginReporting,
		Held: v.Held, HoldReason: string(v.Reason), Message: fallbackhold.Message(v),
	}
	if !v.AckWaitSince.IsZero() {
		waited := int64(now.Sub(v.AckWaitSince).Seconds())
		out.AcknowledgementWaitSeconds = &waited
	}
	return out
}

// fallbackPlayerStateFor is mapFallbackPlayerState for one player, logging
// rather than failing the read it decorates.
func (h *handlers) fallbackPlayerStateFor(ctx context.Context, instanceUUID string, now time.Time) *v1.FallbackPlayerState {
	v, err := h.fallbackVerdict(ctx, instanceUUID, now)
	if err != nil {
		h.logWarn("could not read an FPP player's fallback state", "fppInstanceUuid", instanceUUID, "error", err)
		return nil
	}
	return mapFallbackPlayerState(v, now)
}

// cueActivationHeldForFallback says whether the Cue loop must leave obs
// alone. A state it cannot read holds, as the loop's other gates do.
func (h *handlers) cueActivationHeldForFallback(ctx context.Context, now time.Time, obs store.FPPPlaylistEntryObservationRecord) bool {
	v, err := h.fallbackVerdict(ctx, obs.InstanceUUID, now)
	if err != nil {
		h.logWarn("cue activation loop: failed to read the player's fallback state; holding this player as a precaution",
			"instanceUuid", obs.InstanceUUID, "error", err)
		return true
	}
	return v.IgnoresObservation(obs.ReceivedAt)
}

// nightFallbackHold is the hold standing on an FPP player a night session
// uses, if any.
type nightFallbackHold struct {
	InstanceID   string
	InstanceUUID string
	Verdict      fallbackhold.Verdict
}

// resolveNightFallbackHold finds the first held player among the ones rec
// uses. found is false when none is held or the session cannot be read.
func resolveNightFallbackHold(ctx context.Context, deps Dependencies, rec store.NightSessionRecord, now time.Time) (hold nightFallbackHold, found bool, err error) {
	if deps.FallbackHolds == nil {
		return nightFallbackHold{}, false, nil
	}
	payload, perr := nightPinnedNightSessionPayload(ctx, deps, rec)
	if perr != nil {
		// The state's own advance reports an unreadable session payload.
		return nightFallbackHold{}, false, nil
	}
	views, err := deps.FPP.ListInstances(ctx)
	if err != nil {
		return nightFallbackHold{}, false, err
	}
	seen := make(map[string]bool, 2)
	for _, instanceID := range []string{payload.ShowPlaylist.FPPInstanceID, payload.Resting.FPPInstanceID} {
		if instanceID == "" || seen[instanceID] {
			continue
		}
		seen[instanceID] = true
		for _, view := range views {
			if view.InstanceID != instanceID || view.InstanceUUID == nil {
				continue
			}
			v, err := deps.FallbackHolds.Evaluate(ctx, view.InstanceUUID.UUID, now)
			if err != nil {
				return nightFallbackHold{}, false, err
			}
			if v.Held {
				return nightFallbackHold{InstanceID: instanceID, InstanceUUID: view.InstanceUUID.UUID, Verdict: v}, true, nil
			}
		}
	}
	return nightFallbackHold{}, false, nil
}

// nightExemptFromFallbackHold says whether rec advances whatever is held:
// a shutdown was asked for, or the session is already past its show.
func nightExemptFromFallbackHold(rec store.NightSessionRecord) bool {
	return rec.State == nightStateFadingOut || rec.State == nightStateStopped || rec.ShutdownIntent != ""
}

// nightSessionPlayerHeld says whether a player rec uses is held, whether
// or not rec is exempt. A state it cannot read counts as held.
func (h *handlers) nightSessionPlayerHeld(ctx context.Context, now time.Time, rec store.NightSessionRecord) bool {
	_, found, err := resolveNightFallbackHold(ctx, h.deps, rec, now)
	return found || err != nil
}

// nightDropUnlaunchedShowForHold keeps a shutdown from launching a show on
// a held player. A session committed to a show it has not started yet is
// handed on as uncommitted, so the shutdown fades out now and drops it.
func nightDropUnlaunchedShowForHold(rec *store.NightSessionRecord, heldForFallback bool) *store.NightSessionRecord {
	if rec == nil || !heldForFallback || rec.State != nightStateTransitionToShow || !rec.ShowCommitted {
		return rec
	}
	if a, ok := decodeNightContentAnchor(rec.ContentAnchorJSON); ok && a.Purpose == nightAnchorPurposeShow {
		return rec
	}
	next := *rec
	next.ShowCommitted = false
	return &next
}

// nightDropShowForHeldPlayer is the tick's own form of that rule, for a
// hold that began after the shutdown was asked for. It reports whether it
// moved the session to fading out.
func (h *handlers) nightDropShowForHeldPlayer(ctx context.Context, now time.Time, rec store.NightSessionRecord) bool {
	if rec.ShutdownIntent == "" || rec.State != nightStateTransitionToShow || !rec.ShowCommitted {
		return false
	}
	if nightDropUnlaunchedShowForHold(&rec, true).ShowCommitted {
		// The show was already started; it is left to finish.
		return false
	}
	// Only a hold that was read, never a read that failed, drops a show.
	if _, found, err := resolveNightFallbackHold(ctx, h.deps, rec, now); err != nil || !found {
		return false
	}
	h.logInfo("night loop: a shutdown was asked for and the show's player is held, so the show that had not started is dropped",
		"sessionId", rec.ID)
	h.nightCommit(ctx, now, rec.ID, nightStateTransitionToShow, func(cur store.NightSessionRecord) store.NightSessionRecord {
		next, _ := applyNightShutdownEffect(now, *nightDropUnlaunchedShowForHold(&cur, true), cur.ShutdownIntent, nightShutdownOrdinary)
		return next
	})
	return true
}

// NewFallbackPlayerReader reads an FPP player as the night loop does, from
// the coordinator's own collectors and never from what the plugin posted.
func NewFallbackPlayerReader(lister ObservationLister) fallbackhold.PlayerReader {
	own := notFromPluginLister{lister}
	return func(ctx context.Context, fppInstanceID string, notBefore, now time.Time) fallbackhold.PlayerReading {
		obs := nightObservePlayback(ctx, own, fppInstanceID, notBefore, now)
		return fallbackhold.PlayerReading{
			Current: obs.Current, Status: obs.Status, Playlist: obs.Playlist, PlaylistCurrent: obs.PlaylistCurrent,
		}
	}
}

// fppPluginObservationSource is the source of rows built from the plugin's
// own posts (collector/fppplugin).
const fppPluginObservationSource = "fpp-plugin"

// notFromPluginLister hides every observation the plugin itself supplied.
type notFromPluginLister struct{ inner ObservationLister }

func (l notFromPluginLister) ListObservations(ctx context.Context, filter ObservationFilter) ([]observation.Observation, error) {
	all, err := l.inner.ListObservations(ctx, filter)
	if err != nil {
		return nil, err
	}
	out := make([]observation.Observation, 0, len(all))
	for _, o := range all {
		if o.Source != fppPluginObservationSource {
			out = append(out, o)
		}
	}
	return out, nil
}

// mapNightFallbackHold renders the hold on rec's player for the session
// read, nil when there is none or rec advances regardless.
func mapNightFallbackHold(ctx context.Context, deps Dependencies, rec store.NightSessionRecord, now time.Time) *v1.NightFallbackHold {
	if nightExemptFromFallbackHold(rec) {
		return nil
	}
	hold, found, err := resolveNightFallbackHold(ctx, deps, rec, now)
	if err != nil || !found {
		return nil
	}
	return &v1.NightFallbackHold{
		FPPInstanceID: hold.InstanceID, FPPInstanceUUID: hold.InstanceUUID,
		Reason: string(hold.Verdict.Reason), Message: fallbackhold.Message(hold.Verdict),
	}
}

// fallbackHoldLog remembers which hold each loop last announced, so a hold
// is logged when it begins and when it ends and not on every pass.
type fallbackHoldLog struct {
	mu    sync.Mutex
	night string
	auto  string
}

// changed records key under slot and reports the key it replaced when the
// two differ.
func (l *fallbackHoldLog) changed(slot *string, key string) (previous string, differs bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	previous = *slot
	*slot = key
	return previous, previous != key
}

func (h *handlers) logInfo(msg string, args ...any) {
	if h.logger != nil {
		h.logger.Info("api: "+msg, args...)
	}
}

// nightHeldForFallback says whether a plugin is, or may be, running the
// show on an FPP player this session uses. A session with a shutdown asked
// for is never held: the operator, or FPP's schedule, wins over the hold.
func (h *handlers) nightHeldForFallback(ctx context.Context, now time.Time, rec store.NightSessionRecord) bool {
	if nightExemptFromFallbackHold(rec) {
		h.noteNightFallbackHold(rec, "")
		return false
	}
	hold, found, err := resolveNightFallbackHold(ctx, h.deps, rec, now)
	if err != nil {
		h.logWarn("night loop: failed to read the player's fallback state; holding this tick as a precaution",
			"sessionId", rec.ID, "error", err)
		return true
	}
	if !found {
		h.noteNightFallbackHold(rec, "")
		return false
	}
	h.noteNightFallbackHold(rec, hold.InstanceID+" "+string(hold.Verdict.Reason))
	return true
}

func (h *handlers) noteNightFallbackHold(rec store.NightSessionRecord, key string) {
	previous, differs := h.fallbackHoldLog.changed(&h.fallbackHoldLog.night, key)
	if !differs {
		return
	}
	if key == "" {
		h.logInfo("night loop: the hold for a player's fallback program has ended; the session advances again",
			"sessionId", rec.ID, "was", previous)
		return
	}
	h.logInfo("night loop: not advancing this session while a player is held for its fallback program",
		"sessionId", rec.ID, "hold", key)
}

// configuredFPPInstanceUUIDs is the current instance UUID of every
// configured FPP player that has one.
func (h *handlers) configuredFPPInstanceUUIDs(ctx context.Context) ([]string, error) {
	views, err := h.deps.FPP.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, view := range views {
		if view.InstanceUUID != nil && view.InstanceUUID.UUID != "" {
			out = append(out, view.InstanceUUID.UUID)
		}
	}
	return out, nil
}

// autoDeployHeldForFallback says whether a configured player is held. A
// stored state for an instance no configured player has does not count.
func (h *handlers) autoDeployHeldForFallback(ctx context.Context, now time.Time) bool {
	if h.deps.FallbackHolds == nil {
		return false
	}
	ids, err := h.configuredFPPInstanceUUIDs(ctx)
	var held string
	var v fallbackhold.Verdict
	if err == nil {
		held, v, err = h.deps.FallbackHolds.FirstHeld(ctx, ids, now)
	}
	if err != nil {
		h.logWarn("cue catalog auto-deploy: failed to read the players' fallback state; not deploying this pass", "error", err)
		return true
	}
	key := ""
	if held != "" {
		key = held + " " + string(v.Reason)
	}
	if previous, differs := h.fallbackHoldLog.changed(&h.fallbackHoldLog.auto, key); differs {
		if key == "" {
			h.logInfo("cue catalog auto-deploy: no player is held for its fallback program any more; deploying again", "was", previous)
		} else {
			h.logInfo("cue catalog auto-deploy: waiting while a player is held for its fallback program", "hold", key)
		}
	}
	return held != ""
}
