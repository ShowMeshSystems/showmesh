package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/fallbackhold"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
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
// still reporting stores its state again with its next report.
func (h *handlers) handleDeleteFallbackState(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)
	instanceUUID := r.PathValue("fppInstanceId")

	var deleted bool
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		var err error
		deleted, err = tx.DeleteFallbackPlayerState(ctx, instanceUUID)
		if err != nil {
			return identity.AuditEntry{}, err
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

// nightHeldForFallback says whether a plugin is, or may be, running the
// show on an FPP player this session uses.
func (h *handlers) nightHeldForFallback(ctx context.Context, now time.Time, rec store.NightSessionRecord) bool {
	if h.deps.FallbackHolds == nil {
		return false
	}
	payload, err := h.getPinnedNightSessionPayload(ctx, rec)
	if err != nil {
		// The state's own advance reports an unreadable session payload.
		return false
	}
	seen := make(map[string]bool, 2)
	for _, instanceID := range []string{payload.ShowPlaylist.FPPInstanceID, payload.Resting.FPPInstanceID} {
		if instanceID == "" || seen[instanceID] {
			continue
		}
		seen[instanceID] = true
		instanceUUID, ok, err := h.nightResolveInstanceUUID(ctx, instanceID)
		if err != nil || !ok {
			continue
		}
		v, err := h.fallbackVerdict(ctx, instanceUUID, now)
		if err != nil {
			h.logWarn("night loop: failed to read the player's fallback state; holding this tick as a precaution",
				"sessionId", rec.ID, "instanceId", instanceID, "error", err)
			return true
		}
		if v.Held {
			h.logDebug("night loop: holding while this player's plugin runs the show from its fallback program",
				"sessionId", rec.ID, "instanceId", instanceID, "reason", string(v.Reason))
			return true
		}
	}
	return false
}
