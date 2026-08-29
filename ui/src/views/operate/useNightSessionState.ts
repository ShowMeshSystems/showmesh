import { useEffect, useState } from 'react'
import { getCurrentNightSession } from '../../api'
import { describeApiError } from '../../app/session'
import { useModelContext } from '../../app/ModelContext'
import type { NightSessionState } from '../../app/types'

// Shared by Dashboard (the lifecycle double-timeline) and NightSession
// (the full Show Night operating view) — both need the same running
// night-session state, reconciled the same way. Extracted rather than
// duplicated so the reconciliation rule below is decided in exactly one
// place, per this codebase's own "if it exists in more than one place,
// the two will diverge" posture (EvidenceValue.tsx's header comment).
export type NightSessionLoadState =
  | { kind: 'loading' }
  // Only reachable when this hook has NEVER successfully loaded a
  // session at all (the very first GET fails). Once any session has
  // loaded, a later failure degrades the 'loaded' state in place instead
  // (see `stale`/`staleError` below) — a transient read failure must
  // never cost the operator visibility of the lifecycle state.
  | { kind: 'error'; message: string }
  | {
      kind: 'loaded'
      session: NightSessionState
      // True when the MOST RECENT background refresh (a reload, or the
      // periodic GET this hook's own mount effect re-runs) failed —
      // `session` is then a stale, possibly-outdated last-known value.
      stale: boolean
      staleError: string | null
    }

/**
 * Whichever of `current` (already on screen) and `incoming` (a fresh GET
 * response, a live `nightSession.changed` frame, or a command's own
 * response) is actually newer, compared by `updatedAt` rather than by
 * arrival order — a live frame landing while a slower GET is still in
 * flight must not be rolled back by the GET's `.then` overwriting it.
 * `current === null` (nothing loaded yet) always adopts `incoming`. A
 * `NaN` from an unparseable timestamp is treated as "not newer", never
 * as newer, which would let a malformed value evict good data.
 */
function newerSession(current: NightSessionState | null, incoming: NightSessionState): NightSessionState {
  if (current === null) return incoming
  const currentMs = Date.parse(current.updatedAt)
  const incomingMs = Date.parse(incoming.updatedAt)
  if (Number.isNaN(incomingMs)) return current
  if (Number.isNaN(currentMs)) return incoming
  return incomingMs > currentMs ? incoming : current
}

export function useNightSessionState(): [NightSessionLoadState, () => void] {
  const model = useModelContext()
  const [state, setState] = useState<NightSessionLoadState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)

  function adoptSession(session: NightSessionState): void {
    setState((prev) => ({
      kind: 'loaded',
      session: newerSession(prev.kind === 'loaded' ? prev.session : null, session),
      stale: false,
      staleError: null,
    }))
  }

  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((resp) => {
        if (!cancelled) adoptSession(resp.session)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) =>
          prev.kind === 'loaded'
            ? { ...prev, stale: true, staleError: describeApiError(err) }
            : { kind: 'error', message: describeApiError(err) },
        )
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reloadGeneration])

  // `model.nightSession` is not seeded from Snapshot — it only ever
  // updates via a live `nightSession.changed` frame, and the store
  // clears it to null on every resnapshot. Routed through
  // [adoptSession] rather than a blind replace — see that function's
  // own comment.
  useEffect(() => {
    if (model.nightSession === null) return
    adoptSession(model.nightSession)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [model.nightSession])

  return [state, () => setReloadGeneration((g) => g + 1)]
}
