package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/audioconfigpush"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is the HTTP surface for the "audio.settings" singleton
// (ADR-039): GET/PUT /api/v1/config/audio.settings and its
// revisions list. Mirrors rendersettings.go's GET/PUT/revisions handlers
// exactly, narrowed to a payload with a well-defined default so GET never
// 404s. Reads and writes are both gated by config:write, matching every
// other always-sensitive configuration kind in this package.

const maxAudioSettingsConfigRequestBodyBytes = 4 * 1024

// audioConfigPushTimeout bounds a single node's best-effort
// audio.node/audio.settings push (pushAudioSettingsToAllNodes here, and
// handlePutAudioNode's own push in audionode.go), so one unreachable node
// cannot hold its push goroutine open indefinitely.
const audioConfigPushTimeout = 5 * time.Second

// resolveAudioSettings reads the audio.settings configuration kind's
// current value, mirroring resolveRenderSettings exactly.
func resolveAudioSettings(ctx context.Context, cs ConfigStore) (payload config.AudioSettingsPayload, configured bool, err error) {
	obj, err := cs.GetConfigObject(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.AudioSettingsDefaultPayload, false, nil
	case err != nil:
		return config.AudioSettingsPayload{}, false, fmt.Errorf("api: get audio.settings config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.AudioSettingsDefaultPayload, false, nil
	}

	rev, err := cs.GetConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return config.AudioSettingsPayload{}, false, fmt.Errorf("api: get audio.settings config revision %d: %w", obj.CurrentRevision, err)
	}
	payload, verr := config.DecodeAudioSettingsPayload(rev.PayloadJSON)
	if verr != nil {
		return config.AudioSettingsPayload{}, false, fmt.Errorf("api: decode audio.settings payload: %s", verr.Error())
	}
	return payload, true, nil
}

// audioConfigPushStatus reports whether the coordinator can decode its
// stored audio.settings revision right now: the same read-and-decode
// [audioconfigpush.pushSettings] performs (not [audioconfigpush.ToNode],
// which decodes a NODE's own separate audio.node binding first and
// returns before ever reaching audio.settings on that failure — a stale
// audio.node revision strands ITS node on both pushes without this field
// moving, because audio.node is per-node configuration this coordinator-
// wide singleton was never scoped to cover). So this can never claim
// "usable" while a real push's own audio.settings decode would refuse and
// vice versa, but "usable" here answers only "is the engine-wide
// audio.settings revision itself readable" — never "will every node's
// next push actually land," which also depends on that node's own
// audio.node binding and reachability. err is non-nil only for a genuine
// store failure (not for a decode failure, which is reported as
// state=unusable instead — this function's whole job is to turn that case
// into a value the caller can show rather than propagate as a request
// failure). See [audioConfigPushStatusDegradeOnError] for the store-error
// case's own resolution.
func audioConfigPushStatus(ctx context.Context, cs ConfigStore) (state AudioConfigPushRunState, reason *string, err error) {
	obj, err := cs.GetConfigObject(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return AudioConfigPushUsable, nil, nil
	case err != nil:
		return "", nil, fmt.Errorf("api: get audio.settings config object: %w", err)
	case obj.CurrentRevision == 0:
		return AudioConfigPushUsable, nil, nil
	}

	rev, err := cs.GetConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return "", nil, fmt.Errorf("api: get audio.settings config revision %d: %w", obj.CurrentRevision, err)
	}
	if _, verr := config.DecodeAudioSettingsPayload(rev.PayloadJSON); verr != nil {
		detail := fmt.Sprintf("the engine-wide audio.settings revision %d does not decode: %s", obj.CurrentRevision, verr.Error())
		return AudioConfigPushUnusable, &detail, nil
	}
	return AudioConfigPushUsable, nil, nil
}

// audioConfigPushStatusDegradeOnError reads [audioConfigPushStatus],
// logging and reporting state=unknown on a genuine config-store error
// rather than propagating it — the same "you cannot act, never you
// cannot see" resolution [resolumeCompositionDegradeOnError] already
// establishes for this same handler and the same ConfigStore dependency
// (owner review finding 3, 2026-08-16): GET /api/v1/snapshot has real
// node/FPP/collector/resolume evidence to render regardless of whether
// this ONE field's own store read succeeds, and a storage hiccup reading
// it must not cost the operator that whole view. A store failure is
// reported "unknown", never "unusable": the stored revision itself may be
// perfectly fine, this coordinator just could not read it right now.
func audioConfigPushStatusDegradeOnError(ctx context.Context, cs ConfigStore, logger *slog.Logger, action string) (state AudioConfigPushRunState, reason *string) {
	state, reason, err := audioConfigPushStatus(ctx, cs)
	if err != nil {
		if logger != nil {
			logger.Warn("audio.settings config push status read failed; reporting unknown", "action", action, "error", err)
		}
		detail := "the coordinator could not read its stored audio.settings configuration; see the coordinator log"
		return AudioConfigPushUnknown, &detail
	}
	return state, reason
}

// handleGetAudioSettingsConfig serves GET /api/v1/config/audio.settings.
// "Nothing has ever been written" is never a 404 here — the payload has a
// well-defined default — so this always answers 200.
func (h *handlers) handleGetAudioSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	payload, configured, err := resolveAudioSettings(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve audio.settings", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.AudioSettingsConfigResponse{
			ServerTime: formatTime(now), Kind: config.AudioSettingsConfigKind,
			Revision: 0, Payload: mapAudioSettingsPayload(payload),
			UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}

	obj, err := h.deps.Config.GetConfigObject(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "get audio.settings config object", err)
		return
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, obj.CurrentRevision)
	if err != nil {
		h.writeInternalError(w, now, "get active audio.settings config revision", err)
		return
	}
	jsonWrite(w, mapAudioSettingsConfigResponse(now, rev, obj, payload))
}

// handleGetAudioSettingsConfigRevisions serves
// GET /api/v1/config/audio.settings/revisions: every revision's metadata,
// newest first.
func (h *handlers) handleGetAudioSettingsConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get audio.settings config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list audio.settings config revisions", err)
		return
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.AudioSettingsConfigKind, Revisions: out})
}

// handlePutAudioSettingsConfig serves PUT /api/v1/config/audio.settings: a
// full replacement, appends an immutable revision, and activates it in the
// SAME transaction as its audit log entry (ADR-024 decision 11).
func (h *handlers) handlePutAudioSettingsConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	body, err := io.ReadAll(io.LimitReader(r.Body, maxAudioSettingsConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(fmt.Sprintf("reading request body: %v", err)))
		return
	}
	if len(body) > maxAudioSettingsConfigRequestBodyBytes {
		writeProblem(w, h.logger, now, invalidParameterProblem("request body too large"))
		return
	}

	payload, verr := config.DecodeAudioSettingsPayload(string(body))
	if verr != nil {
		writeProblem(w, h.logger, now, mapValidationError(verr))
		return
	}

	payloadJSON, err := config.EncodeAudioSettingsPayload(payload)
	if err != nil {
		h.writeInternalError(w, now, "encode audio.settings config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.AudioSettingsConfigKind, ObjectID: config.AudioSettingsConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.AudioSettingsSourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.AudioSettingsConfigKind, config.AudioSettingsConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.AudioSettingsConfigKind,
			Params: map[string]any{"revision": nextRevisionNo},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "write audio.settings config revision", writeErr)
		return
	}

	// ADR-039/ADR-036: audio.settings is engine-wide, so every node in
	// inventory is pushed the new revision without waiting for its next
	// hello — best-effort per node, matching handlePutAudioNode.
	h.pushAudioSettingsToAllNodes(ctx, now)

	jsonWrite(w, mapAudioSettingsConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.AudioSettingsConfigKind, ID: config.AudioSettingsConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, payload))
}

// pushAudioSettingsToAllNodes best-effort pushes the current
// audio.settings revision to every node currently in inventory. A node
// this coordinator does not yet know about (never sent a hello) is
// reached instead by its own hello-triggered push once it does — this
// call never fails the write that triggered it.
//
// Each node's push runs in its own goroutine on a context detached from
// the request (context.WithoutCancel) and individually bounded by
// audioConfigPushTimeout: fanned out concurrently, not serially, so one
// slow or unreachable node cannot delay every other node's push behind
// it, and this function returns without waiting for any push to finish,
// so a client that disconnects mid-request no longer determines how many
// of N nodes get pushed (ADR-036's "applies without a restart" with no
// window this write would otherwise leave for the remaining nodes).
func (h *handlers) pushAudioSettingsToAllNodes(ctx context.Context, now time.Time) {
	if h.deps.Nodes == nil {
		return
	}
	views, err := h.deps.Nodes.Snapshot(ctx, now)
	if err != nil {
		h.logWarn("failed to list nodes for audio.settings push", "error", err)
		return
	}
	detached := context.WithoutCancel(ctx)
	for _, nv := range views {
		nodeID := nv.NodeID
		go func() {
			pushCtx, cancel := context.WithTimeout(detached, audioConfigPushTimeout)
			defer cancel()
			audioconfigpush.BestEffort(pushCtx, h.deps.Config, h.deps.RenderPublisher, h.now, nodeID, h.logger)
		}()
	}
}

func mapAudioSettingsPayload(p config.AudioSettingsPayload) v1.ConfigAudioSettingsPayload {
	return v1.ConfigAudioSettingsPayload{
		DriftIgnoreThresholdMs:     p.DriftIgnoreThresholdMs,
		DefaultFadeCurve:           p.DefaultFadeCurve,
		DefaultFadeDurationMs:      p.DefaultFadeDurationMs,
		DefaultMaxBackgroundGainDb: p.DefaultMaxBackgroundGainDb,
		DuckTargetGainDb:           p.DuckTargetGainDb,
		LTCFrameRate:               p.LTCFrameRate,
		LTCDefaultStartOffset:      p.LTCDefaultStartOffset,
	}
}

func mapAudioSettingsConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, payload config.AudioSettingsPayload) v1.AudioSettingsConfigResponse {
	return v1.AudioSettingsConfigResponse{
		ServerTime: formatTime(now), Kind: config.AudioSettingsConfigKind, Revision: rev.Revision,
		Payload:                mapAudioSettingsPayload(payload),
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}
