/**
 * Fetches the full stored Resolume composition id map (`GET
 * /config/resolume/composition`, ADR-032) for the views that need the
 * inventory itself — the Resolume view's inventory/ambiguous-clips panels,
 * observation-label resolution, and the controller/macro-authoring
 * pickers (build contract §2.2/§2.3). Distinct from
 * ResolumeCompositionUpload.tsx's own local load state (which tracks only
 * the display SUMMARY plus its own upload lifecycle, and is left
 * unchanged per build contract §2.2 — moved, not modified): this hook is
 * read-only, has no upload affordance, and is meant to be mounted by
 * several call sites at once.
 *
 * `reloadKey` re-fetches whenever it changes — callers pass
 * `model.resolume[0]?.composition?.name ?? null`, so an upload elsewhere
 * on the page (or from another client) is picked up the next time this
 * store's own live model says a different composition is loaded, without
 * this hook polling or subscribing to anything itself.
 */
import { useEffect, useState } from 'react'
import { ApiError, ForbiddenError, UnauthorizedError, getResolumeComposition } from '../api'
import { describeApiError } from './session'
import type { ResolumeCompositionResponse } from './types'

export type ResolumeCompositionState =
  | { kind: 'loading' }
  | { kind: 'not_stored'; reason: string }
  | { kind: 'forbidden'; reason: string }
  | { kind: 'unauthorized'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; composition: ResolumeCompositionResponse }

export function useResolumeComposition(reloadKey: string | null): ResolumeCompositionState {
  const [state, setState] = useState<ResolumeCompositionState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const resp = await getResolumeComposition()
        if (cancelled) return
        setState({ kind: 'loaded', composition: resp })
      } catch (err) {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not_stored', reason: err.message })
          return
        }
        if (err instanceof ForbiddenError) {
          setState({ kind: 'forbidden', reason: err.message })
          return
        }
        if (err instanceof UnauthorizedError) {
          setState({ kind: 'unauthorized', reason: err.message })
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [reloadKey])

  return state
}

/** `state.composition` when loaded, `null` otherwise — the common case for a caller that only needs the id map for lookups and renders its own message for every other state. */
export function resolumeCompositionOrNull(state: ResolumeCompositionState): ResolumeCompositionResponse | null {
  return state.kind === 'loaded' ? state.composition : null
}
