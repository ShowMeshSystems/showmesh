import { useSyncExternalStore } from 'react'
import { listConfigObjects } from '../api'
import { describeApiError } from '../app/session'
import type { ConfigObjectSummary } from '../app/types'

export type ShowListState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; shows: ConfigObjectSummary[] }

/**
 * Module-level singleton, mirroring the `useSyncExternalStore` +
 * singleton-store shape `api/useModel.ts` uses for the polled `Model`
 * (see that file's header comment): `GET /config/show` is a one-shot
 * side call rather than part of the polled model, so it does not belong
 * in `ApiStore`, but every `ShowSelect` on screen names the same
 * resource and must not each issue it independently. `NightSessionDetail`
 * alone can mount a dozen `ShowSelect`s at once (its own show, the
 * resting timeline, and one per background-audio item); without this,
 * that is a dozen identical requests on mount, one more per item added,
 * and a dozen independent loading/error states that can disagree with
 * each other about the same list.
 *
 * The cache resets the moment the last subscriber unmounts (never left
 * to go stale in the background across navigations), and de-dupes
 * concurrent mounts onto a single in-flight request.
 */
let state: ShowListState = { kind: 'loading' }
let inflight: Promise<void> | null = null
const listeners = new Set<() => void>()

function notify() {
  for (const listener of listeners) listener()
}

function ensureFetchStarted() {
  if (inflight) return
  inflight = listConfigObjects('show')
    .then((resp) => {
      state = { kind: 'loaded', shows: resp.objects }
    })
    .catch((err: unknown) => {
      state = { kind: 'error', message: describeApiError(err) }
    })
    .finally(() => {
      inflight = null
      notify()
    })
}

function subscribe(listener: () => void) {
  listeners.add(listener)
  ensureFetchStarted()
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) {
      // Nobody is looking at the list any more; drop it rather than
      // serving an ever-staler snapshot to whoever mounts next.
      state = { kind: 'loading' }
      inflight = null
    }
  }
}

function getSnapshot(): ShowListState {
  return state
}

/** Shared `GET /config/show` result: every `ShowSelect` calls this instead of fetching its own copy. */
export function useShowList(): ShowListState {
  return useSyncExternalStore(subscribe, getSnapshot)
}
