package identity

import (
	"context"
	"encoding/json"
	"errors"
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
	s.recordAuditWriteOutcome(err)
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
	err := s.st.InTx(ctx, func(ctx context.Context, tx *store.Tx) error {
		entry, ferr := fn(ctx, tx)
		if ferr != nil {
			return ferr
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
	return s.classifyAuditedWriteResult(err)
}

// classifyAuditedWriteResult updates the [Service.AuditWriteStatus] latch
// from AuditedWrite's own [store.Store.InTx] result and returns the error
// AuditedWrite's caller sees. Two review findings on the same task fixed
// together here:
//
//   - the latch used to flip to "usable" INSIDE the InTx closure, before
//     COMMIT had actually run, so a commit failure left the latch
//     claiming health at the exact moment the row was lost. It is set
//     only from InTx's own return value now, which is not known until
//     after COMMIT has actually been attempted.
//   - a commit failure ([store.ErrCommitFailed], returned when the
//     append above already succeeded inside the transaction but COMMIT
//     itself then failed, e.g. a disk that fills between the last write
//     and the fsync COMMIT performs) used to fall through to this
//     package's generic error path, invisible to every caller's
//     `errors.Is(err, identity.ErrAuditWrite)` check, so ADR-024
//     decision 11's own callers (actioninvoke.go and friends) refused the
//     action on this failure mode even after they stopped refusing on a
//     plain append failure. A commit failure after a successful append
//     is the SAME "could not be attributed" fact decision 11 already
//     reasons about, just caught one step later, so it is now wrapped in
//     ErrAuditWrite too.
//
// fn's own business error (anything that is neither of the two cases
// above) never touches the latch: it never reached the append at all, so
// it says nothing about whether the audit store itself is writable; see
// [svc.recordAuditWriteOutcome]'s own doc comment for the identical
// reasoning applied to its two call sites.
func (s *svc) classifyAuditedWriteResult(err error) error {
	switch {
	case err == nil:
		s.recordAuditWriteOutcome(nil)
		return nil
	case errors.Is(err, store.ErrCommitFailed):
		// Two %w verbs, not %v: keeps both ErrAuditWrite and the wrapped
		// store.ErrCommitFailed reachable via errors.Is. Go 1.20+
		// fmt.Errorf supports more than one %w.
		wrapped := fmt.Errorf("%w: %w", ErrAuditWrite, err)
		s.recordAuditWriteOutcome(wrapped)
		return wrapped
	case errors.Is(err, ErrAuditWrite):
		s.recordAuditWriteOutcome(err)
		return err
	default:
		return err
	}
}

// recordAuditWriteOutcome updates the state [Service.AuditWriteStatus]
// reports from the outcome of an actual audit_log append attempt. The
// only two call sites are [svc.WriteAudit] and [svc.AuditedWrite]'s own
// append step above, deliberately never AuditedWrite's fn: fn's error is
// the caller's own business-logic failure, not a fact about whether the
// audit store itself is writable, and conflating the two would report
// "audit unusable" for e.g. a duplicate-key business error that never
// touched audit_log at all.
func (s *svc) recordAuditWriteOutcome(err error) {
	s.auditWriteMu.Lock()
	defer s.auditWriteMu.Unlock()
	if err != nil {
		s.auditWriteState = "unusable"
		s.auditWriteReason = fmt.Sprintf("the coordinator's most recent attempt to write an audit_log entry failed: %v", err)
		return
	}
	s.auditWriteState = "usable"
	s.auditWriteReason = ""
}

// AuditWriteStatus implements [Service.AuditWriteStatus]: a real probe
// write against audit_log (store.Store.ProbeAuditWrite, always rolled
// back), computed fresh on every call rather than read from
// [svc.recordAuditWriteOutcome]'s own latch. A review finding on this
// task's own change named the latch's failure mode directly: with no
// real command traffic to update it, a store that fails between two
// snapshot polls (the middle of the night, an idle coordinator) would
// otherwise keep reporting the LAST latched value indefinitely, an
// ADR-011 "stale evidence read as healthy" case in exactly the surface
// meant to catch it. The probe's own result also updates the latch, so
// [AuditedWrite]/[WriteAudit] callers checking immediately afterward see
// the same answer this method would give right now.
func (s *svc) AuditWriteStatus(ctx context.Context) (state, reason string) {
	err := s.st.ProbeAuditWrite(ctx)
	s.recordAuditWriteOutcome(err)
	s.auditWriteMu.Lock()
	defer s.auditWriteMu.Unlock()
	return s.auditWriteState, s.auditWriteReason
}

// ListAudit returns audit entries after since (the id [AuditEntry.ID]
// carries; the API reports it per entry so a caller can advance this
// cursor from a page it already holds), ordered oldest first, capped at
// limit (store.Store.ListAuditEntries's own defaulting/clamping applies,
// see [store.DefaultAuditPageSize]/[store.MaxAuditPageSize]). This is what
// backs `/api/v1/audit`, behind audit:read (ADR-024 decision 4), a scope
// this package does not itself enforce; see [Role.Has] and the API layer's
// own boundary check.
func (s *svc) ListAudit(ctx context.Context, since int64, limit int) ([]AuditEntry, error) {
	recs, err := s.st.ListAuditEntries(ctx, since, limit)
	if err != nil {
		return nil, fmt.Errorf("identity: list audit: %w", err)
	}
	return auditEntriesFromRecords(recs)
}

// ListAuditNewestFirst returns audit entries before before, ordered newest
// first, capped the same way [svc.ListAudit] is. before <= 0 starts at the
// newest retained entry, so an operator surface opens on the most recent
// activity in one request instead of walking retained history forward.
func (s *svc) ListAuditNewestFirst(ctx context.Context, before int64, limit int) ([]AuditEntry, error) {
	recs, err := s.st.ListAuditEntriesNewestFirst(ctx, before, limit)
	if err != nil {
		return nil, fmt.Errorf("identity: list audit newest first: %w", err)
	}
	return auditEntriesFromRecords(recs)
}

// OldestAuditID reports the lowest audit id still retained, and false when
// nothing is retained at all. A backward-paging caller needs it to tell
// "this is the beginning of the log" from "retention trimmed what I was
// about to read"; see [store.Store.OldestAuditID].
func (s *svc) OldestAuditID(ctx context.Context) (int64, bool, error) {
	oldest, ok, err := s.st.OldestAuditID(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("identity: oldest audit id: %w", err)
	}
	return oldest, ok, nil
}

// auditEntriesFromRecords converts store rows to domain entries in the
// order given, preserving whichever ordering the query chose.
func auditEntriesFromRecords(recs []store.AuditRecord) ([]AuditEntry, error) {
	out := make([]AuditEntry, len(recs))
	for i, rec := range recs {
		entry := AuditEntry{
			ID:             rec.ID,
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
