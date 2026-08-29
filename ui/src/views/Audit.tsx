import { useCallback, useEffect, useState } from 'react'
import { listAudit } from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import type { AuditEntry } from '../app/types'

// Track G seam G-8: the audit log (ADR-024 decision 11), behind the
// audit:read scope always: GET /audit is never one of the open-by-default
// reads (api/openapi.yaml's own description). Monitor's Activity facet
// (Events.tsx) merges audit rows into the one event stream, gating ONLY
// the audit rows -- system events need no scope at all, and their
// presence must never depend on this one. This file used to be its own
// routed page; it is now the data layer Events.tsx consumes (useAuditLog),
// so the audit-specific fetch/pagination logic keeps exactly one home
// instead of being duplicated into the merged view.
export const AUDIT_READ_SCOPE = 'audit:read'
const PAGE_SIZE = 100

export type AuditLoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'unconfirmed-order'; message: string }
  | { kind: 'loaded'; entries: AuditEntry[]; oldestRetainedId: number | null; atBeginning: boolean }

// olderState is deliberately separate from LoadState: a failure while
// paging further back must not discard the entries already on screen, and
// must stay distinguishable from "there is nothing older".
export type AuditOlderState = { kind: 'idle' } | { kind: 'loading' } | { kind: 'error'; message: string }

export function auditOutcomeLabel(entry: AuditEntry): string {
  if (entry.kind !== 'outcome') return '-'
  return entry.outcome === '' ? '(no evidence-bearing outcome recorded)' : entry.outcome
}

// The contract for GET /audit is explicit: "the ordering actually used is
// echoed as AuditResponse.order", never inferred (api/openapi.yaml). This
// view asks for `order=desc` but the UI and coordinator are separate
// images on independent release cadence (ADR-014, ADR-015; see
// deploy/docker-compose.yml's SHOWMESH_COORDINATOR_HOST), so an older
// coordinator that predates `order`/`id`/`oldestRetainedId` is a real,
// supported pairing. Such a coordinator ignores the parameter and answers
// oldest-first with none of those three fields present. `order` missing
// entirely is that specific, checkable signal, not a generic malformed
// response: it is the one field whose absence tells us WHY the response
// cannot be trusted, so it gets a named, actionable message. Any other
// value than the one requested (`desc`) is not a version-skew case this
// view can explain, so it gets a plain mismatch message instead.
function orderMismatchReason(order: 'asc' | 'desc' | undefined): string | null {
  if (order === 'desc') return null
  if (order === undefined) {
    return 'This coordinator did not echo an order for the audit log, which is what a coordinator built before newest-first paging does.'
  }
  return `This coordinator echoed order "${order}" for a request made with order=desc.`
}

// atBeginningOfHistory is true only when the coordinator's own
// oldestRetainedId says so, or when a backward page came back empty. A
// short page alone never proves it: retention can trim below the cursor
// between two requests.
function atBeginningOfHistory(entries: AuditEntry[], oldestRetainedId: number | null): boolean {
  const oldestOnScreen = entries.at(-1)
  if (oldestOnScreen === undefined) return true
  if (oldestRetainedId === null) return false
  return oldestOnScreen.id <= oldestRetainedId
}

// pagingCursor is the backward cursor `loadOlder` would send, or null when
// the last entry on screen carries no usable id to page from. Generated
// types mark AuditEntry.id required but nothing validates that at
// runtime, so a response that reached `loaded` state with a missing or
// non-finite id must not turn into a `before` that repeats the same page
// forever: refuse to offer another page instead.
export function auditPagingCursor(entries: AuditEntry[]): number | null {
  const oldestOnScreen = entries.at(-1)
  if (oldestOnScreen === undefined) return null
  const id = oldestOnScreen.id
  return typeof id === 'number' && Number.isFinite(id) ? id : null
}

export interface AuditLog {
  scopeGate: { allowed: boolean; reason: string }
  state: AuditLoadState
  older: AuditOlderState
  loadOlder: () => void
}

// The one fetch site for GET /audit -- Events.tsx (Monitor's Activity
// facet) calls this and merges `state.entries` into the combined stream
// by timestamp. Gated on audit:read internally (never on a page-level
// read scope any other Class 3 view would use), matching the wire
// contract's own posture that this endpoint is never open-by-default.
export function useAuditLog(): AuditLog {
  const model = useModelContext()
  const scopeGateResult = evaluateScope(model.session, model.sessionFetchFailed, AUDIT_READ_SCOPE)
  const scopeGate = { allowed: scopeGateResult.allowed, reason: scopeGateResult.allowed ? '' : scopeGateResult.reason }
  const [state, setState] = useState<AuditLoadState>({ kind: 'loading' })
  const [older, setOlder] = useState<AuditOlderState>({ kind: 'idle' })

  useEffect(() => {
    if (!scopeGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    setOlder({ kind: 'idle' })
    listAudit({ order: 'desc', limit: PAGE_SIZE })
      .then((resp) => {
        if (cancelled) return
        const orderProblem = orderMismatchReason(resp.order)
        if (orderProblem !== null) {
          setState({
            kind: 'unconfirmed-order',
            message: `${orderProblem} Nothing is shown here rather than present its oldest retained entries as recent activity.`,
          })
          return
        }
        const entries = resp.entries
        setState({
          kind: 'loaded',
          entries,
          oldestRetainedId: resp.oldestRetainedId,
          atBeginning: atBeginningOfHistory(entries, resp.oldestRetainedId),
        })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scopeGate.allowed])

  const loadOlder = useCallback(() => {
    if (state.kind !== 'loaded') return
    const before = auditPagingCursor(state.entries)
    if (before === null) return
    setOlder({ kind: 'loading' })
    listAudit({ order: 'desc', before, limit: PAGE_SIZE })
      .then((resp) => {
        const orderProblem = orderMismatchReason(resp.order)
        if (orderProblem !== null) {
          setOlder({ kind: 'error', message: orderProblem })
          return
        }
        setOlder({ kind: 'idle' })
        setState((prev) => {
          if (prev.kind !== 'loaded') return prev
          const entries = [...prev.entries, ...resp.entries]
          return {
            kind: 'loaded',
            entries,
            oldestRetainedId: resp.oldestRetainedId,
            atBeginning: resp.entries.length === 0 || atBeginningOfHistory(entries, resp.oldestRetainedId),
          }
        })
      })
      .catch((err: unknown) => {
        setOlder({ kind: 'error', message: describeApiError(err) })
      })
  }, [state])

  return { scopeGate, state, older, loadOlder }
}
