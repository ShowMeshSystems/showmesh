import { useState } from 'react'
import { describeApiError } from './session'
import type { ActionInvocationResult } from './types'

// Mirrors useResolumeActionCall's identical shape: a call in flight, and
// every one of the five outcome words, must all be VISIBLE states — a
// resolved network call is never rendered as unqualified success
// (ADR-003).
export type ActionCallState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'result'; result: ActionInvocationResult }
  | { kind: 'error'; message: string }

export interface ActionCall {
  state: ActionCallState
  /** Runs `dispatch`, ignored if a call is already in flight. */
  run: (dispatch: () => Promise<ActionInvocationResult>) => void
}

export function useActionCall(): ActionCall {
  const [state, setState] = useState<ActionCallState>({ kind: 'idle' })

  function run(dispatch: () => Promise<ActionInvocationResult>): void {
    if (state.kind === 'submitting') return
    setState({ kind: 'submitting' })
    void (async () => {
      try {
        const result = await dispatch()
        setState({ kind: 'result', result })
      } catch (err) {
        setState({ kind: 'error', message: describeApiError(err) })
      }
    })()
  }

  return { state, run }
}
