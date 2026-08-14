import { useState } from 'react'
import { describeApiError } from './session'
import type { FPPCommandResult } from './types'

// Shared in-flight/result/error state machine for the zero-and-simple
// argument FPP primitive controls (Step 8) — FPPStopPlaylistControl
// (Step 7, unchanged) carries its own copy of this exact shape inline;
// this hook exists so the other primitives do not each reimplement it
// independently and risk drifting on this step's own acceptance
// criterion: a command in flight, and an unconfirmed outcome, must both
// be VISIBLE states, and a resolved network call is never rendered as
// unqualified success (ADR-003). FPPStartPlaylistControl does NOT use
// this hook — its own 409/ifBusy branching needs a state shape this one
// does not have, and forcing it through here would make that branching
// harder to read, not easier.
export type FPPCommandCallState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'result'; result: FPPCommandResult }
  | { kind: 'error'; message: string }

export interface FPPCommandCall {
  state: FPPCommandCallState
  /** Runs `dispatch`, ignored if a call is already in flight — matches FPPStopPlaylistControl's own `if (state.kind === 'submitting') return` guard. */
  run: (dispatch: () => Promise<FPPCommandResult>) => void
}

export function useFPPCommandCall(): FPPCommandCall {
  const [state, setState] = useState<FPPCommandCallState>({ kind: 'idle' })

  function run(dispatch: () => Promise<FPPCommandResult>): void {
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
