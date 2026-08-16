package coordinator

// This file closes Track D seam D-3a's own wiring gap, the same role
// resolumeactionwiring.go plays for D-3: joins a real *resolume.Recovery
// (internal/coordinator/collector/resolume) to api.ResolumeRecoveryProvider,
// which api's own resolumerecovery_interfaces.go declares at the
// consumer. Every semantic decision — the record, the gate, the restore —
// lives in resolume.Recovery and is read here, never reimplemented. The
// one thing this file decides for itself is attribution: resolume.Recovery
// has no identity/audit dependency (recovery.go's own top comment), so
// this is the layer that attaches a principal to a restore report and
// writes its audit entry.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/showmeshsystems/showmesh/internal/coordinator/api"
	"github.com/showmeshsystems/showmesh/internal/coordinator/collector/resolume"
	"github.com/showmeshsystems/showmesh/internal/coordinator/identity"
	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// resolumeRecoveryAuditAction is the new audit action build contract §1.5
// mints.
const resolumeRecoveryAuditAction = "resolume.recovery_restore"

// resolumeRecoveryToggleReader adapts *store.Store to
// [api.ResolveResolumeRecoveryToggle]'s own ConfigStore parameter — used
// directly by resolume.RecoveryOptions.AutoRestoreEnabled, so the
// automatic gate's own toggle check calls the IDENTICAL function the HTTP
// read handlers call (api.ResolveResolumeRecoveryToggle's own doc
// comment: "two readers of one value can never disagree").
func resolumeRecoveryToggleReader(st *store.Store) func(ctx context.Context) (bool, error) {
	return func(ctx context.Context) (bool, error) {
		enabled, _, err := api.ResolveResolumeRecoveryToggle(ctx, st)
		return enabled, err
	}
}

// resolumeRecoveryAdapter joins a real *resolume.Recovery to
// api.ResolumeRecoveryProvider. It is the one place a [resolume.RestoreReport]
// (no principal) becomes an [api.ResolumeRecoveryRestoreReportView] (with
// one): the manual path attributes to whichever principal's request
// called Restore; the automatic path attributes to
// [identity.ReservedResolumeRecoveryPrincipalID] via onRestoreComplete,
// which resolume.Recovery calls for EVERY restore (manual included) —
// filtered here by Trigger, so a manual restore is never transiently
// misattributed to the system principal (see this file's own report for
// why that filter, not a second callback, is what keeps this race-free).
type resolumeRecoveryAdapter struct {
	recovery    *resolume.Recovery
	identitySvc identity.Service
	logger      *slog.Logger

	mu   sync.Mutex
	last *api.ResolumeRecoveryRestoreReportView
}

var _ api.ResolumeRecoveryProvider = (*resolumeRecoveryAdapter)(nil)

func (a *resolumeRecoveryAdapter) Record() []api.ResolumeRecoveryRecordEntryView {
	rec := a.recovery.Record()
	out := make([]api.ResolumeRecoveryRecordEntryView, 0, len(rec))
	for _, e := range rec {
		out = append(out, mapRecoveryLayerRecord(e))
	}
	return out
}

func (a *resolumeRecoveryAdapter) LastReport() *api.ResolumeRecoveryRestoreReportView {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.last
}

func (a *resolumeRecoveryAdapter) Restore(ctx context.Context, principalName string) (api.ResolumeRecoveryRestoreReportView, error) {
	report := a.recovery.RunManualRestore(ctx)
	view := mapRestoreReport(report, principalName)
	a.setLast(view)
	return view, nil
}

// onRestoreComplete is [resolume.RecoveryOptions.OnRestoreComplete]:
// called once, synchronously, for EVERY restore this Recovery runs
// (automatic or manual — resolume.Recovery.finish's own doc comment).
// Only the AUTOMATIC case is handled here; a manual restore is already
// fully attributed and stored by [resolumeRecoveryAdapter.Restore] itself,
// which knows the acting principal this hook never does.
func (a *resolumeRecoveryAdapter) onRestoreComplete(report resolume.RestoreReport) {
	if report.Trigger != resolume.RestoreTriggerAutomatic {
		return
	}
	view := mapRestoreReport(report, identity.ReservedResolumeRecoveryPrincipalID)
	a.setLast(view)
	a.writeAuditEntry(view)
}

func (a *resolumeRecoveryAdapter) setLast(view api.ResolumeRecoveryRestoreReportView) {
	a.mu.Lock()
	a.last = &view
	a.mu.Unlock()
}

// writeAuditEntry records the automatic restore's own audit entry
// (build contract §1.5), best-effort, AFTER the restore has already run —
// per §7.3, never refused for want of an audit write: the dispatch(es)
// already happened, so there is nothing left for a refusal to protect,
// and this project's own ADR-024 decision 11 exemption reasoning applies
// (refusing to attribute an event that already happened only denies the
// operator the record of it). The reserved principal holds no credential
// of any form, so Form/CredentialID are left at their zero values rather
// than a fabricated one.
func (a *resolumeRecoveryAdapter) writeAuditEntry(view api.ResolumeRecoveryRestoreReportView) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.identitySvc.WriteAudit(ctx, identity.AuditEntry{
		Timestamp: time.Now(), PrincipalID: identity.ReservedResolumeRecoveryPrincipalID, PrincipalName: identity.ReservedResolumeRecoveryPrincipalID,
		Action: resolumeRecoveryAuditAction, Target: "resolume",
		Params:        map[string]any{"trigger": "automatic"},
		Kind:          identity.AuditOutcome,
		Outcome:       view.Outcome,
		OutcomeReason: fmt.Sprintf("%d layer(s) in report", len(view.Layers)),
	})
	if err != nil {
		a.logger.Warn("failed to write audit entry for automatic resolume recovery restore", "error", err)
	}
}

func mapRecoveryLayerRecord(e resolume.RecoveryLayerRecord) api.ResolumeRecoveryRecordEntryView {
	return api.ResolumeRecoveryRecordEntryView{
		Layer: e.Layer, LayerNameGenerated: e.LayerNameGenerated, State: string(e.State),
		Clip: e.Clip, ClipNameGenerated: e.ClipNameGenerated, Deck: e.Deck,
		EstablishedAt: formatTimeOrEmpty(e.EstablishedAt), Source: string(e.Source), Reason: e.Reason,
	}
}

func mapRestoreReport(rep resolume.RestoreReport, principalName string) api.ResolumeRecoveryRestoreReportView {
	layers := make([]api.ResolumeRecoveryRestoreLayerView, 0, len(rep.Layers))
	for _, l := range rep.Layers {
		layers = append(layers, api.ResolumeRecoveryRestoreLayerView{
			Layer: l.Layer, LayerNameGenerated: l.LayerNameGenerated, Result: string(l.Result),
			Reason: l.Reason, Clip: l.Clip, ActionOutcome: l.ActionOutcome,
		})
	}
	return api.ResolumeRecoveryRestoreReportView{
		StartedAt: formatTimeOrEmpty(rep.StartedAt), FinishedAt: formatTimeOrEmpty(rep.FinishedAt),
		Trigger: string(rep.Trigger), Outcome: string(rep.Outcome), Principal: principalName, Layers: layers,
	}
}

// formatTimeOrEmpty renders t as RFC 3339, or "" for the zero time — this
// file's own copy of api.formatTime's identical convention (unexported in
// that package, so this package cannot call it directly).
func formatTimeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339Nano)
}

// newResolumeRecoveryWiring constructs Track D seam D-3a's Recovery
// controller over collector and dispatcher, both of which must be
// non-nil (the identical cfg.ResolumeURL != "" gate resolumeWire.collector
// and resolumeActions are already built under — see this function's own
// call site in coordinator.go).
func newResolumeRecoveryWiring(st *store.Store, identitySvc identity.Service, collector *resolume.Collector, dispatcher *resolume.ActionDispatcher, settle time.Duration, logger *slog.Logger) (*resolume.Recovery, *resolumeRecoveryAdapter) {
	adapter := &resolumeRecoveryAdapter{identitySvc: identitySvc, logger: logger}
	recovery := resolume.NewRecovery(collector, dispatcher, resolume.RecoveryOptions{
		Settle:             settle,
		AutoRestoreEnabled: resolumeRecoveryToggleReader(st),
		OnRestoreComplete:  adapter.onRestoreComplete,
	})
	adapter.recovery = recovery
	return recovery, adapter
}

// ensureResolumeRecoveryPrincipal creates the built-in automatic-recovery
// principal at startup if it does not already exist (build contract
// §1.2). Never fatal: matching this file's own posture for every other
// startup-time store read in this package, a failure here has no
// principal to hold accountable for refusing to boot (the fail-closed
// inversion CLAUDE.md's Step 7 lesson names), so it is logged and this
// coordinator still starts — the automatic path simply cannot act until a
// later attempt (the next restart, or an operator noticing) succeeds.
func ensureResolumeRecoveryPrincipal(ctx context.Context, identitySvc identity.Service, logger *slog.Logger) {
	if _, err := identitySvc.EnsureReservedRecoveryPrincipal(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Warn("failed to ensure the built-in resolume recovery principal exists", "error", err)
	}
}
