package identity

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/showmeshsystems/showmesh/internal/coordinator/store"
)

// This file holds [svc]'s [Service.WriteAudit]/[Service.ListAudit]
// implementation: the identity <-> store.AuditRecord conversion. See
// migrations.go's schemaV5 doc comment (store package) for the
// append-only guarantee this converts INTO, never around — WriteAudit
// calls store.Store.AppendAuditEntry, the same single write path every
// other caller of this file uses, and there is deliberately no
// UpdateAudit-shaped method anywhere in this package for the same reason
// there is none in store.

// WriteAudit appends entry as the next append-only audit_log row.
// ADR-024 decision 11's dispatch/outcome/replay split, and the blackout/
// stop/power-off exemption from the "a write that cannot be attributed
// does not proceed" rule, are BOTH the caller's responsibility, not this
// method's: WriteAudit only ever appends exactly what it is given, once,
// synchronously. A coordinator-local write's audit entry belongs in the
// SAME transaction as the state change per decision 11 ("the audit entry
// is written in the same transaction, so the two succeed or fail
// together") — this package cannot enforce that from here, because the
// state change itself lives in whichever package owns that resource, not
// in identity; the caller must open its own transaction and call this
// method (or, more likely, a lower-level store append) inside it. What
// THIS method guarantees is only that append itself is atomic and
// append-only, matching store.Store.AppendAuditEntry exactly.
func (s *svc) WriteAudit(ctx context.Context, entry AuditEntry) error {
	paramsJSON := "{}"
	if len(entry.Params) > 0 {
		b, err := json.Marshal(entry.Params)
		if err != nil {
			return fmt.Errorf("identity: write audit: encode params: %w", err)
		}
		paramsJSON = string(b)
	}

	_, err := s.st.AppendAuditEntry(ctx, store.AuditRecord{
		// RecordedAt: entry.Timestamp, honored by store.AppendAuditEntry
		// only when non-zero (falling back to the store's own clock
		// otherwise) — see that field's doc comment. Every production
		// caller in this codebase already sets AuditEntry.Timestamp (to
		// h.now() or an equivalent request-scoped clock read), so this was
		// previously a field callers set that had no effect: the store
		// silently re-stamped its own "now" regardless. Step 7 seam A
		// review defect 5.
		RecordedAt:     entry.Timestamp,
		PrincipalID:    entry.PrincipalID,
		PrincipalName:  entry.PrincipalName,
		Form:           string(entry.Form),
		CredentialID:   entry.CredentialID,
		ClientAddr:     entry.ClientAddr,
		Action:         entry.Action,
		Target:         entry.Target,
		ParamsJSON:     paramsJSON,
		IdempotencyKey: entry.IdempotencyKey,
		Kind:           string(entry.Kind),
		CommandID:      entry.CommandID,
		Outcome:        entry.Outcome,
		OutcomeState:   entry.OutcomeState,
		OutcomeReason:  entry.OutcomeReason,
	})
	if err != nil {
		return fmt.Errorf("identity: write audit: %w", err)
	}
	return nil
}

// AuditedWrite implements [Service.AuditedWrite]: opens one transaction
// via [store.Store.InTx], runs fn, and appends the [AuditEntry] fn returns
// via [store.Tx.AppendAuditEntry] — both inside that same transaction, so
// [store.Store.InTx] commits or rolls back the state change and the audit
// row together. This is Step 7 seam 0's closure of ADR-024 decision 11's
// same-transaction rule, which the record itself states was NOT achieved
// as of Step 6 ("the API package cannot reach the transaction boundary,
// which identity and store own").
//
// fn's own error is returned UNWRAPPED (a caller's errors.Is against its
// own sentinel — e.g. store.ErrBootstrapClaimedRace — still works
// unchanged); an audit-append failure is wrapped in [ErrAuditWrite]
// instead, so the two failure modes stay distinguishable to every caller,
// which is the entire reason AuditedWrite exists rather than a caller
// composing store.Store.InTx and a raw store.Tx.AppendAuditEntry call
// itself each time.
func (s *svc) AuditedWrite(ctx context.Context, fn func(ctx context.Context, tx *store.Tx) (AuditEntry, error)) error {
	return s.st.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		entry, err := fn(ctx, tx)
		if err != nil {
			return err
		}

		paramsJSON := "{}"
		if len(entry.Params) > 0 {
			b, jerr := json.Marshal(entry.Params)
			if jerr != nil {
				return fmt.Errorf("identity: audited write: encode audit params: %w", jerr)
			}
			paramsJSON = string(b)
		}

		if _, aerr := tx.AppendAuditEntry(ctx, store.AuditRecord{
			// See WriteAudit's identical field above for why this is now
			// passed through rather than dropped (Step 7 seam A review
			// defect 5).
			RecordedAt:     entry.Timestamp,
			PrincipalID:    entry.PrincipalID,
			PrincipalName:  entry.PrincipalName,
			Form:           string(entry.Form),
			CredentialID:   entry.CredentialID,
			ClientAddr:     entry.ClientAddr,
			Action:         entry.Action,
			Target:         entry.Target,
			ParamsJSON:     paramsJSON,
			IdempotencyKey: entry.IdempotencyKey,
			Kind:           string(entry.Kind),
			CommandID:      entry.CommandID,
			Outcome:        entry.Outcome,
			OutcomeState:   entry.OutcomeState,
			OutcomeReason:  entry.OutcomeReason,
		}); aerr != nil {
			return fmt.Errorf("%w: %v", ErrAuditWrite, aerr)
		}
		return nil
	})
}

// ListAudit returns audit entries after since (an opaque cursor —
// store.AuditRecord.ID under the hood, but callers should treat it as
// opaque exactly the way the events API treats its since cursor), capped
// at limit (store.Store.ListAuditEntries's own defaulting/clamping
// applies — see [store.DefaultAuditPageSize]/[store.MaxAuditPageSize]).
// This is what backs `/api/v1/audit`, behind audit:read (ADR-024 decision
// 4) — a scope this package does not itself enforce; see [Role.Has] and
// the API layer's own boundary check.
func (s *svc) ListAudit(ctx context.Context, since int64, limit int) ([]AuditEntry, error) {
	recs, err := s.st.ListAuditEntries(ctx, since, limit)
	if err != nil {
		return nil, fmt.Errorf("identity: list audit: %w", err)
	}
	out := make([]AuditEntry, len(recs))
	for i, rec := range recs {
		entry := AuditEntry{
			Timestamp:      rec.RecordedAt,
			PrincipalID:    rec.PrincipalID,
			PrincipalName:  rec.PrincipalName,
			Form:           CredentialForm(rec.Form),
			CredentialID:   rec.CredentialID,
			ClientAddr:     rec.ClientAddr,
			Action:         rec.Action,
			Target:         rec.Target,
			IdempotencyKey: rec.IdempotencyKey,
			Kind:           AuditKind(rec.Kind),
			CommandID:      rec.CommandID,
			Outcome:        rec.Outcome,
			OutcomeState:   rec.OutcomeState,
			OutcomeReason:  rec.OutcomeReason,
		}
		if rec.ParamsJSON != "" && rec.ParamsJSON != "{}" {
			var params map[string]any
			if err := json.Unmarshal([]byte(rec.ParamsJSON), &params); err != nil {
				return nil, fmt.Errorf("identity: list audit: decode params for entry %d: %w", rec.ID, err)
			}
			entry.Params = params
		}
		out[i] = entry
	}
	return out, nil
}
