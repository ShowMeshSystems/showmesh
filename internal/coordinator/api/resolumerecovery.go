package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	v1 "github.com/showmeshsystems/showmesh/internal/coordinator/api/v1"
	"github.com/showmeshsystems/showmesh/internal/coordinator/config"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file is Track D seam D-3a's HTTP surface (TRACK-D-D3A-CRASH-RECOVERY-SPEC.md,
// TRACK-D-D3A-BUILD-CONTRACT.md §1.3): the open recovery read, the
// resolume.recovery configuration toggle (mirroring config.go's
// fpp.endpoints shape exactly), and the manual restore. This file owns no
// gate or restore LOGIC of its own — see [ResolumeRecoveryProvider] and
// this package's own resolumerecovery_interfaces.go.

var errResolumeRecoveryNotConfigured = errors.New("api: no ResolumeRecoveryProvider was wired into this API's Dependencies")

// resolumeRecoveryConfigKind/ObjectID mirror config.ResolumeRecoveryConfigKind/
// ObjectID via that package's own exported constants — used directly
// rather than duplicated, unlike resolumewiring.go's own by-VALUE
// duplication of unexported constants in a DIFFERENT package
// (coordinator): this package already imports internal/coordinator/config
// for [config.FPPEndpoint] and friends, so there is no second-package
// coupling being avoided here.

// ResolveResolumeRecoveryToggle reads the resolume.recovery configuration
// kind's current value (build contract §1.1): enabled, and whether a
// revision has ever actually been written ("configured"). ONE function,
// called by every reader of this value — the open GET /resolume/recovery
// read, the config:write-gated GET /config/resolume.recovery read, and
// (via the wiring layer, which calls this identically) the automatic
// recovery gate's own toggle check — so two readers of one value can
// never disagree. The default when nothing has ever been written is
// [config.ResolumeRecoveryDefaultEnabled], reported with configured=false.
func ResolveResolumeRecoveryToggle(ctx context.Context, cs ConfigStore) (enabled bool, configured bool, err error) {
	obj, err := cs.GetConfigObject(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
		return config.ResolumeRecoveryDefaultEnabled, false, nil
	case err != nil:
		return false, false, fmt.Errorf("api: get resolume.recovery config object: %w", err)
	case obj.CurrentRevision == 0:
		return config.ResolumeRecoveryDefaultEnabled, false, nil
	}

	rev, err := cs.GetConfigRevision(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID, obj.CurrentRevision)
	if err != nil {
		return false, false, fmt.Errorf("api: get resolume.recovery config revision %d: %w", obj.CurrentRevision, err)
	}
	enabled, err = config.DecodeResolumeRecoveryPayload(rev.PayloadJSON)
	if err != nil {
		return false, false, fmt.Errorf("api: decode resolume.recovery payload: %w", err)
	}
	return enabled, true, nil
}

// --- GET /api/v1/resolume/recovery: the open read -----------------------

// handleGetResolumeRecovery serves GET /api/v1/resolume/recovery: never
// gated by any scope (build contract §1.3 — "the dashboard renders with
// no session", ADR-024's reads-stay-open posture). Reads the toggle
// through [ResolveResolumeRecoveryToggle] — the identical function the
// config:write-gated read below calls, so the two can never disagree
// about the toggle's own value.
func (h *handlers) handleGetResolumeRecovery(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	enabled, configured, err := ResolveResolumeRecoveryToggle(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve resolume.recovery toggle", err)
		return
	}

	record := h.deps.ResolumeRecovery.Record()
	entries := make([]v1.ResolumeRecoveryRecordEntry, 0, len(record))
	for _, e := range record {
		entries = append(entries, mapResolumeRecoveryRecordEntry(e))
	}

	var lastRestore *v1.ResolumeRecoveryRestoreReport
	if lr := h.deps.ResolumeRecovery.LastReport(); lr != nil {
		mapped := mapResolumeRecoveryRestoreReport(*lr)
		lastRestore = &mapped
	}

	jsonWrite(w, v1.ResolumeRecoveryResponse{
		ServerTime:            formatTime(now),
		ResolumeConfigured:    resolumeRecoveryIsConfigured(h.deps.ResolumeRecovery),
		AutoRestoreEnabled:    enabled,
		AutoRestoreConfigured: configured,
		SettleDelaySeconds:    h.deps.ResolumeRecoverySettleSeconds,
		Record:                entries,
		LastRestore:           lastRestore,
	})
}

// resolumeRecoveryIsConfigured reports whether a real Resolume instance is
// configured on this coordinator at all — false only for
// [noResolumeRecoveryProvider], [Dependencies.ResolumeRecovery]'s nil-safe
// default. Distinct from a toggle's own "configured" (whether a revision
// has ever been written): a client renders "not configured" rather than
// the toggle's default-ON value when this is false, since an operator who
// believes recovery is armed and is wrong is worse off than one who knows
// it is unavailable.
func resolumeRecoveryIsConfigured(p ResolumeRecoveryProvider) bool {
	_, unconfigured := p.(noResolumeRecoveryProvider)
	return !unconfigured
}

func mapResolumeRecoveryRecordEntry(e ResolumeRecoveryRecordEntryView) v1.ResolumeRecoveryRecordEntry {
	return v1.ResolumeRecoveryRecordEntry{
		Layer: e.Layer, LayerNameGenerated: e.LayerNameGenerated, State: e.State,
		Clip: e.Clip, ClipNameGenerated: e.ClipNameGenerated, Deck: e.Deck,
		EstablishedAt: e.EstablishedAt, Source: e.Source, Reason: e.Reason,
	}
}

func mapResolumeRecoveryRestoreReport(rep ResolumeRecoveryRestoreReportView) v1.ResolumeRecoveryRestoreReport {
	layers := make([]v1.ResolumeRecoveryRestoreLayer, 0, len(rep.Layers))
	for _, l := range rep.Layers {
		layers = append(layers, v1.ResolumeRecoveryRestoreLayer{
			Layer: l.Layer, LayerNameGenerated: l.LayerNameGenerated, Result: l.Result,
			Reason: l.Reason, Clip: l.Clip, ActionOutcome: l.ActionOutcome,
		})
	}
	return v1.ResolumeRecoveryRestoreReport{
		StartedAt: rep.StartedAt, FinishedAt: rep.FinishedAt, Trigger: rep.Trigger,
		Outcome: rep.Outcome, Principal: rep.Principal, Layers: layers,
		OmittedLayerCount: rep.OmittedLayerCount,
	}
}

// resolumeRecoveryChangedEventProjection builds a "resolumeRecovery.changed"
// event's own substantive fields, WITHOUT Seq/ServerTime — stream.go's
// pendingFrame.materialize stamps those, exactly as
// macroRunChangedEventProjection's identical split already does for
// macroRun.changed. Used as [Hub.updateRendered]'s change-detection key
// too: byte-identical JSON on two consecutive render passes means nothing
// about the toggle, the record, or the last restore has changed, so no
// frame is sent — the quiet-system property build contract §1.7 requires.
func resolumeRecoveryChangedEventProjection(resolumeConfigured, enabled, configured bool, settleSeconds float64, record []ResolumeRecoveryRecordEntryView, lastReport *ResolumeRecoveryRestoreReportView) v1.ResolumeRecoveryChangedEvent {
	entries := make([]v1.ResolumeRecoveryRecordEntry, 0, len(record))
	for _, e := range record {
		entries = append(entries, mapResolumeRecoveryRecordEntry(e))
	}
	var lastRestore *v1.ResolumeRecoveryRestoreReport
	if lastReport != nil {
		mapped := mapResolumeRecoveryRestoreReport(*lastReport)
		lastRestore = &mapped
	}
	return v1.ResolumeRecoveryChangedEvent{
		ResolumeConfigured: resolumeConfigured, AutoRestoreEnabled: enabled, AutoRestoreConfigured: configured, SettleDelaySeconds: settleSeconds,
		Record: entries, LastRestore: lastRestore,
	}
}

// --- GET/PUT /api/v1/config/resolume.recovery: the toggle ----------------
//
// Mirrors config.go's handleGetFPPEndpointsConfig/handlePutFPPEndpointsConfig
// shape exactly, narrowed to one boolean and with no env-var refusal (no
// SHOWMESH_RESOLUME_RECOVERY env var exists — build contract §1.1: "a
// show-state toggle lives in the store").

const maxResolumeRecoveryConfigRequestBodyBytes = 4 * 1024

// handleGetResolumeRecoveryConfig serves GET
// /api/v1/config/resolume.recovery: revision metadata, behind
// config:write (mirroring GET /config/fpp.endpoints's own always-sensitive
// posture). Unlike fpp.endpoints, "nothing has ever been written" is NOT
// a 404 here — the toggle has a well-defined default (build contract
// §1.1) — so this always answers 200, reporting whether the current value
// is the default or a stored choice via Source/Revision.
func (h *handlers) handleGetResolumeRecoveryConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	enabled, configured, err := ResolveResolumeRecoveryToggle(ctx, h.deps.Config)
	if err != nil {
		h.writeInternalError(w, now, "resolve resolume.recovery toggle", err)
		return
	}
	if !configured {
		jsonWrite(w, v1.ResolumeRecoveryConfigResponse{
			ServerTime: formatTime(now), Kind: config.ResolumeRecoveryConfigKind,
			Revision: 0, Payload: v1.ConfigResolumeRecoveryPayload{AutoRestoreEnabled: enabled},
			UpdatedAt: formatTime(now), Source: "default",
		})
		return
	}

	obj, err := h.deps.Config.GetConfigObject(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "get resolume.recovery config object", err)
		return
	}
	rev, err := h.deps.Config.GetConfigRevision(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID, obj.CurrentRevision)
	if err != nil {
		h.writeInternalError(w, now, "get active resolume.recovery config revision", err)
		return
	}
	jsonWrite(w, mapResolumeRecoveryConfigResponse(now, rev, obj, enabled))
}

func (h *handlers) handleGetResolumeRecoveryConfigRevisions(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()

	activeRevision := int64(0)
	obj, err := h.deps.Config.GetConfigObject(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID)
	switch {
	case errors.Is(err, store.ErrConfigObjectNotFound):
	case err != nil:
		h.writeInternalError(w, now, "get resolume.recovery config object", err)
		return
	default:
		activeRevision = obj.CurrentRevision
	}

	revs, err := h.deps.Config.ListConfigRevisions(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID)
	if err != nil {
		h.writeInternalError(w, now, "list resolume.recovery config revisions", err)
		return
	}
	out := make([]v1.ConfigRevisionMeta, 0, len(revs))
	for i := len(revs) - 1; i >= 0; i-- {
		out = append(out, mapConfigRevisionMeta(revs[i], activeRevision))
	}
	jsonWrite(w, v1.ConfigRevisionsResponse{ServerTime: formatTime(now), Kind: config.ResolumeRecoveryConfigKind, Revisions: out})
}

// decodeResolumeRecoveryConfigPutBody implements the identical
// absent/null-vs-present rule decodeFPPEndpointsConfigPutBody enforces:
// "autoRestoreEnabled" is required and must be a JSON boolean, never
// absent and never null. Any other top-level key is refused.
func decodeResolumeRecoveryConfigPutBody(body io.Reader) (bool, error) {
	var top map[string]json.RawMessage
	if err := json.NewDecoder(body).Decode(&top); err != nil {
		return false, fmt.Errorf(`request body must be a JSON object matching {"autoRestoreEnabled":bool}: %w`, err)
	}
	for key := range top {
		if key != "autoRestoreEnabled" {
			return false, fmt.Errorf(`unknown field %q; the only accepted top-level field is "autoRestoreEnabled"`, key)
		}
	}
	raw, present := top["autoRestoreEnabled"]
	if !present {
		return false, errors.New(`"autoRestoreEnabled" is required and was absent`)
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, errors.New(`"autoRestoreEnabled" must not be null`)
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err != nil {
		return false, errors.New(`"autoRestoreEnabled" must be a boolean`)
	}
	return b, nil
}

// handlePutResolumeRecoveryConfig serves PUT
// /api/v1/config/resolume.recovery: writes a new revision and activates
// it in the SAME transaction as its audit log entry (ADR-024 decision 11
// — config:write fails closed on an audit-write failure, unlike the
// restore endpoint below).
func (h *handlers) handlePutResolumeRecoveryConfig(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ctx := r.Context()
	ac := authFromContext(ctx)

	enabled, err := decodeResolumeRecoveryConfigPutBody(io.LimitReader(r.Body, maxResolumeRecoveryConfigRequestBodyBytes+1))
	if err != nil {
		writeProblem(w, h.logger, now, invalidParameterProblem(err.Error()))
		return
	}

	payloadJSON, err := config.EncodeResolumeRecoveryPayload(enabled)
	if err != nil {
		h.writeInternalError(w, now, "encode resolume.recovery config payload", err)
		return
	}

	var (
		activated      store.ConfigRevisionRecord
		nextRevisionNo int64
	)
	writeErr := h.deps.Identity.AuditedWrite(ctx, func(ctx context.Context, tx *store.Tx) (identity.AuditEntry, error) {
		nextRevisionNo = 1
		if obj, gerr := tx.GetConfigObject(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID); gerr == nil {
			nextRevisionNo = obj.CurrentRevision + 1
		} else if !errors.Is(gerr, store.ErrConfigObjectNotFound) {
			return identity.AuditEntry{}, gerr
		}

		rec, cerr := tx.CreateConfigRevision(ctx, store.ConfigRevisionRecord{
			Kind: config.ResolumeRecoveryConfigKind, ObjectID: config.ResolumeRecoveryConfigObjectID,
			Revision: nextRevisionNo, PayloadJSON: payloadJSON,
			CreatedByPrincipalID: ac.result.Principal.ID, CreatedByPrincipalName: ac.result.Principal.Name,
			Source: config.ResolumeRecoverySourceAPI,
		})
		if cerr != nil {
			return identity.AuditEntry{}, cerr
		}
		if _, aerr := tx.ActivateConfigRevision(ctx, config.ResolumeRecoveryConfigKind, config.ResolumeRecoveryConfigObjectID, nextRevisionNo); aerr != nil {
			return identity.AuditEntry{}, aerr
		}
		activated = rec
		return identity.AuditEntry{
			Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
			Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
			Action: "config.write", Target: config.ResolumeRecoveryConfigKind,
			Params: map[string]any{"revision": nextRevisionNo, "autoRestoreEnabled": enabled},
			Kind:   identity.AuditAdmin,
		}, nil
	})
	if writeErr != nil {
		h.writeInternalError(w, now, "write resolume.recovery config revision", writeErr)
		return
	}

	jsonWrite(w, mapResolumeRecoveryConfigResponse(now, activated, store.ConfigObjectRecord{
		Kind: config.ResolumeRecoveryConfigKind, ID: config.ResolumeRecoveryConfigObjectID,
		CurrentRevision: nextRevisionNo, UpdatedAt: now,
	}, enabled))
}

func mapResolumeRecoveryConfigResponse(now time.Time, rev store.ConfigRevisionRecord, obj store.ConfigObjectRecord, enabled bool) v1.ResolumeRecoveryConfigResponse {
	return v1.ResolumeRecoveryConfigResponse{
		ServerTime: formatTime(now), Kind: config.ResolumeRecoveryConfigKind, Revision: rev.Revision,
		Payload:                v1.ConfigResolumeRecoveryPayload{AutoRestoreEnabled: enabled},
		UpdatedAt:              formatTime(obj.UpdatedAt),
		CreatedByPrincipalID:   nonEmptyStrPtr(rev.CreatedByPrincipalID),
		CreatedByPrincipalName: nonEmptyStrPtr(rev.CreatedByPrincipalName),
		Source:                 rev.Source,
	}
}

// --- POST /api/v1/resolume/recovery/restore: the manual restore ---------

// handlePostResolumeRecoveryRestore serves POST
// /api/v1/resolume/recovery/restore, behind
// writeGuard(&scopeResolumeAction, ...) — the manual path (§7.1). Runs
// the SAME restore as the automatic gate, minus the crash-return gate's
// own settle wait and freshness check (resolume.Recovery.RunManualRestore's
// own doc comment). Always attempts, regardless of the auto-restore
// toggle. The restore's own audit entry ("resolume.recovery_restore") is
// written here, best-effort, AFTER the restore has already run — never
// refused for want of an audit write (build contract §1.5, ADR-035's
// shape: dispatch, then attribute, never gate).
func (h *handlers) handlePostResolumeRecoveryRestore(w http.ResponseWriter, r *http.Request) {
	now := h.now()
	ac := authFromContext(r.Context())

	// Sized from the CURRENT composition's own layer count, clamped to
	// resolumeRecoveryMaxLayers — the same bound a restore itself never
	// exceeds. Held open past net/http.Server's own WriteTimeout, matching
	// handleDispatchResolumeAction's identical reasoning.
	layerCount := len(h.deps.ResolumeRecovery.Record())
	_ = http.NewResponseController(w).SetWriteDeadline(now.Add(resolumeRecoveryRestoreDeadline(layerCount)))

	bgCtx := context.WithoutCancel(r.Context())
	report, err := h.deps.ResolumeRecovery.Restore(bgCtx, ac.result.Principal.Name)
	if err != nil {
		h.writeInternalError(w, now, "manual resolume recovery restore", err)
		return
	}

	h.writeResolumeRecoveryRestoreAuditBounded(bgCtx, now, RestoreTriggerManual(report), ac, r)

	jsonWrite(w, v1.ResolumeRecoveryRestoreResponse{ServerTime: formatTime(h.now()), Restore: mapResolumeRecoveryRestoreReport(report)})
}

// RestoreTriggerManual is a tiny accessor so the audit call site below
// reads Trigger without this file needing to know
// ResolumeRecoveryRestoreReportView's exact field layout twice.
func RestoreTriggerManual(rep ResolumeRecoveryRestoreReportView) string { return rep.Trigger }

// resolumeRecoveryBookkeepingBudget mirrors resolumeActionBookkeepingBudget's
// identical reasoning (resolumeaction.go): each individual piece of
// post-restore bookkeeping this handler does once Restore has already
// returned (LastReport update, the outcome audit entry).
const resolumeRecoveryBookkeepingBudget = 5 * time.Second

// resolumeRecoveryDeadlineMargin mirrors resolumeActionWriteDeadlineMargin's
// identical reasoning: headroom [resolumeRecoveryRestoreDeadline] carries
// on top of its own known worst-case work, never zero.
const resolumeRecoveryDeadlineMargin = 5 * time.Second

// resolumeRecoveryMaxLayers duplicates resolume.MaxRestoreLayers by value
// (this package does not import internal/coordinator/collector/resolume —
// see resolumeActionMaxDispatchDuration's own doc comment for why),
// reconciled by TestResolumeRecoveryMaxLayersEqualsProducerBound.
const resolumeRecoveryMaxLayers = 30

// resolumeRecoveryRestoreDeadline composes the HTTP write deadline for a
// restore attempting layerCount layers. Mirrors
// resolumeActionHTTPWriteDeadline's own composition exactly, per layer:
// resolumeActionMaxDispatchDuration for each layer's own D-3 dispatch,
// TWO rounds of resolumeRecoveryBookkeepingBudget, plus margin.
// layerCount is clamped to resolumeRecoveryMaxLayers first — the SAME
// clamp resolume.Recovery.restore itself applies to what it actually
// attempts (resolume.RestoreReport.OmittedLayerCount), so this deadline
// is never asked to cover more than a restore call can ever do.
func resolumeRecoveryRestoreDeadline(layerCount int) time.Duration {
	if layerCount > resolumeRecoveryMaxLayers {
		layerCount = resolumeRecoveryMaxLayers
	}
	if layerCount < 1 {
		layerCount = 1
	}
	return time.Duration(layerCount)*resolumeActionMaxDispatchDuration + 2*resolumeRecoveryBookkeepingBudget + resolumeRecoveryDeadlineMargin
}

func (h *handlers) writeResolumeRecoveryRestoreAuditBounded(parent context.Context, now time.Time, trigger string, ac authContext, r *http.Request) {
	ctx, cancel := context.WithTimeout(parent, resolumeRecoveryBookkeepingBudget)
	defer cancel()
	h.writeBestEffortAudit(ctx, now, degradedAttributionReasonPostDispatch, identity.AuditEntry{
		Timestamp: now, PrincipalID: ac.result.Principal.ID, PrincipalName: ac.result.Principal.Name,
		Form: ac.result.Form, CredentialID: ac.result.CredentialID, ClientAddr: h.clientAddr(r),
		Action: "resolume.recovery_restore", Target: "resolume", Params: map[string]any{"trigger": trigger},
		Kind: identity.AuditOutcome,
	})
}
