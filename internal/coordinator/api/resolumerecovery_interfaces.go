package api

import "context"

// This file declares this package's own consumer-side view of Track D
// seam D-3a's recovery controller (internal/coordinator/collector/resolume.Recovery),
// the same pattern resolumeaction_interfaces.go already uses: this
// interface names the narrow shape this package needs without importing
// that producer package directly, and a wiring adapter elsewhere joins
// the two.

// ResolumeRecoveryRecordEntryView and ResolumeRecoveryRestoreView are this
// package's own copies of resolume.RecoveryLayerRecord and
// resolume.RestoreLayerResult/RestoreReport, already ADR-037-labeled by
// the producer — this package never formats a name or renders a label
// itself, only maps field to field onto the wire.

// ResolumeRecoveryRecordEntryView is one layer's row, as this package
// needs it to render [v1.ResolumeRecoveryRecordEntry].
type ResolumeRecoveryRecordEntryView struct {
	Layer              string
	LayerNameGenerated bool
	State              string
	Clip               string
	ClipNameGenerated  bool
	Deck               string
	EstablishedAt      string // RFC3339, "" when no entry has ever been established
	Source             string
	Reason             string
}

// ResolumeRecoveryRestoreLayerView is one layer's row within a restore
// report.
type ResolumeRecoveryRestoreLayerView struct {
	Layer              string
	LayerNameGenerated bool
	Result             string
	Reason             string
	Clip               string
	ActionOutcome      string
}

// ResolumeRecoveryRestoreReportView is one restore's whole outcome,
// already carrying the acting principal's display name — the producer
// package (internal/coordinator/collector/resolume) has no identity
// dependency (see recovery.go's own top comment), so the WIRING layer,
// not this package, is what attaches Principal before this package ever
// sees a report.
type ResolumeRecoveryRestoreReportView struct {
	StartedAt  string
	FinishedAt string
	Trigger    string
	Outcome    string
	Principal  string
	Layers     []ResolumeRecoveryRestoreLayerView
}

// ResolumeRecoveryProvider is what this package needs from the recovery
// controller: the current record, the most recent restore (nil if none
// has ever run), and the manual restore path (§7.1's showmeshctl command,
// exposed here as POST /resolume/recovery/restore).
type ResolumeRecoveryProvider interface {
	Record() []ResolumeRecoveryRecordEntryView
	LastReport() *ResolumeRecoveryRestoreReportView

	// Restore runs the manual restore and returns its own report,
	// already carrying principalName as Principal — the caller (the
	// wiring adapter) is the one place that knows who is acting.
	Restore(ctx context.Context, principalName string) (ResolumeRecoveryRestoreReportView, error)
}

// noResolumeRecoveryProvider is [Dependencies.ResolumeRecovery]'s nil-safe
// default: Record answers empty, LastReport answers nil, and Restore
// refuses loudly — matching this package's standing "an unwired
// dependency is not this API failing" posture, EXCEPT that Restore is a
// write and this package's own standing rule for an unwired write
// dependency is to refuse loudly rather than fabricate success (matching
// noCommandStore/noResolumeActionDispatcher).
type noResolumeRecoveryProvider struct{}

func (noResolumeRecoveryProvider) Record() []ResolumeRecoveryRecordEntryView { return nil }
func (noResolumeRecoveryProvider) LastReport() *ResolumeRecoveryRestoreReportView {
	return nil
}
func (noResolumeRecoveryProvider) Restore(context.Context, string) (ResolumeRecoveryRestoreReportView, error) {
	return ResolumeRecoveryRestoreReportView{}, errResolumeRecoveryNotConfigured
}
