import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ApiError, submitMacroRun } from '../api'
import { PROBLEM_TYPE } from '../api/problem'
import { describeApiError } from '../app/session'
import { ScopedButton } from './ScopedButton'

// The run control (STEP-9-SPEC.md section 9 / OPERATOR-UI section 14):
// "gated on show:macro:run through the existing ScopedButton. A control
// the principal may not use renders disabled with a stated reason and is
// never hidden." ScopedButton itself already implements that rule; this
// component's own job is just the submit/navigate/error-render cycle
// around it, following FPPStartPlaylistControl's own shape (busy state
// via ScopedButton's busy prop, a distinct rendering for the one
// structured refusal this endpoint has that a caller can act on).
export interface RunMacroButtonProps {
  macroId: string
  /** Shown on the button while idle — lets Macros.tsx use "Run" and MacroDetail.tsx use "Run this macro". */
  label?: string
}

type SubmitState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'error'; message: string }
  // ADR-031 decision 6 / STEP-9-SPEC.md section 6.2: three DISTINCT `409`
  // causes, all naming a run in `Problem.conflictingRunId`
  // (errors.ts's ApiError.conflictingRunId) — kept as its OWN state
  // (rather than folded into `error` with the id embedded in `message`)
  // so the render below can offer a direct link to that run rather than
  // making the operator dig its id out of prose, matching this
  // codebase's own "branch on type, read the structured field" posture.
  // FPPStartPlaylistControl.tsx's own history is exactly the defect that
  // posture exists to avoid.
  | { kind: 'conflict'; message: string; conflictingRunId: string }

export function RunMacroButton({ macroId, label = 'Run' }: RunMacroButtonProps) {
  const navigate = useNavigate()
  const [state, setState] = useState<SubmitState>({ kind: 'idle' })

  async function handleRun(): Promise<void> {
    if (state.kind === 'submitting') return
    setState({ kind: 'submitting' })
    try {
      const resp = await submitMacroRun(macroId)
      // 202: the run is ACCEPTED, not complete (ADR-031 decision 1). This
      // control's own job ends at "the coordinator now has this run" —
      // navigating to the run view is how the operator watches it
      // resolve, never by waiting on this response any longer.
      setState({ kind: 'idle' })
      navigate(`/macros/${encodeURIComponent(macroId)}/runs/${encodeURIComponent(resp.run.id)}`)
    } catch (err) {
      const conflictTypes: string[] = [
        PROBLEM_TYPE.macroRunAlreadyInFlight,
        PROBLEM_TYPE.macroRunIdempotencyMacroConflict,
        PROBLEM_TYPE.macroRunIdempotencyRevisionConflict,
      ]
      if (
        err instanceof ApiError &&
        err.status === 409 &&
        err.conflictingRunId !== undefined &&
        err.problemType !== undefined &&
        conflictTypes.includes(err.problemType)
      ) {
        setState({ kind: 'conflict', message: describeApiError(err), conflictingRunId: err.conflictingRunId })
        return
      }
      setState({ kind: 'error', message: describeApiError(err) })
    }
  }

  return (
    <div className="run-macro-button">
      <ScopedButton
        requiredScope="show:macro:run"
        busy={state.kind === 'submitting'}
        busyReason="A run of this macro is being submitted…"
        onClick={() => void handleRun()}
      >
        {state.kind === 'submitting' ? 'Starting…' : label}
      </ScopedButton>
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
      {state.kind === 'conflict' && (
        <div role="alert" className="fpp-command-control__error">
          <p>{state.message}</p>
          <Link to={`/macros/${encodeURIComponent(macroId)}/runs/${encodeURIComponent(state.conflictingRunId)}`}>
            View that run
          </Link>
        </div>
      )}
    </div>
  )
}
