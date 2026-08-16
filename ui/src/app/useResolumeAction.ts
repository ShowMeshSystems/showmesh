import { useState } from 'react'
import { describeApiError } from './session'
import type { ResolumeActionResult } from './types'

// The Resolume sibling of app/useFPPCommand.ts's useFPPCommandCall — same
// in-flight/result/error state machine, typed against ResolumeActionResult
// instead of FPPCommandResult. A command in flight, and every one of the
// five outcome words, must all be VISIBLE states (build contract §2.3/§3):
// a resolved network call is never rendered as unqualified success.
export type ResolumeActionCallState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'result'; result: ResolumeActionResult }
  | { kind: 'error'; message: string }

export interface ResolumeActionCall {
  state: ResolumeActionCallState
  /** Runs `dispatch`, ignored if a call is already in flight. */
  run: (dispatch: () => Promise<ResolumeActionResult>) => void
}

export function useResolumeActionCall(): ResolumeActionCall {
  const [state, setState] = useState<ResolumeActionCallState>({ kind: 'idle' })

  function run(dispatch: () => Promise<ResolumeActionResult>): void {
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
