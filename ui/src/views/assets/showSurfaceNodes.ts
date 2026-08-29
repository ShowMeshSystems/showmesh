import { useEffect, useState } from 'react'
import { getShowSurface, listConfigObjects } from '../../api'
import { describeApiError } from '../../app/session'

// Which nodes carry a surface in this show -- the denominator the sequence
// coverage roll-up needs to tell "every judged node covers this" apart
// from "every node WE COULD CHECK covers this, and one was never judged".
// GET /config/show.surface?show= only returns id/label/revision
// (ConfigObjectSummary), never the payload's `node` field, so this fans
// out one GET /config/show.surface/{id} per surface the show has. A show
// carries a small, human-authored number of surfaces (the mock's own
// example is three), so this mirrors listConfigObjects + getShowSurface's
// earlier-retired per-node fan-out shape for the OPPOSITE direction (show
// -> nodes) that has no reverse-index endpoint to replace it with.
export type ShowSurfaceNodesState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'loaded'; nodeIds: string[] }
  | { kind: 'error'; message: string }

export function useShowSurfaceNodeIds(showId: string, allowed: boolean): ShowSurfaceNodesState {
  const [state, setState] = useState<ShowSurfaceNodesState>({ kind: 'idle' })

  useEffect(() => {
    if (!allowed) {
      setState({ kind: 'idle' })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.surface', showId)
      .then((resp) => Promise.all(resp.objects.map((obj) => getShowSurface(obj.id))))
      .then((surfaces) => {
        if (cancelled) return
        const nodeIds = Array.from(new Set(surfaces.map((s) => s.payload.node)))
        setState({ kind: 'loaded', nodeIds })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId, allowed])

  return state
}
