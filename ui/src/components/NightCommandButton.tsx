import { useRef, useState } from 'react'
import { ApiError, dispatchNightCommand } from '../api'
import { PROBLEM_TYPE } from '../api/problem'
import { describeApiError } from '../app/session'
import type { NightCommandName, NightSessionState } from '../app/types'
import { ScopedButton } from './ScopedButton'

// Track F seam F2 (ADR-038): the one lifecycle command dispatch control,
// reused for all eight commands. `night:command` scoped through
// ScopedButton, exactly like RunMacroButton's own `show:macro:run`
// control — the difference this component adds is the FOUR
// distinguishable failure shapes `POST /night/commands/{command}` can
// answer with (three 409 `type`s plus one 503), each surfaced with its
// own wording rather than collapsed into one generic error message,
// matching RunMacroButton's own "branch on `type`, never parse `detail`
// prose" posture for its own single 409 family.
export interface NightCommandButtonProps {
  command: NightCommandName
  /** Shown on the button while idle. */
  label: string
  /** Called with the resulting session once the command is accepted (applied or idempotent_no_op). */
  onApplied?: (session: NightSessionState) => void
  /** Honored only by `prepare-site` — every other command ignores it (api/openapi.yaml). */
  idempotencyKey?: string
}

type DispatchState =
  | { kind: 'idle' }
  | { kind: 'dispatching' }
  | { kind: 'applied'; outcome: 'applied' | 'idempotent_no_op' }
  | { kind: 'not_ready'; message: string }
  | { kind: 'state_rejected'; message: string }
  | { kind: 'ambiguous'; message: string }
  | { kind: 'audit_unavailable'; message: string }
  | { kind: 'error'; message: string }

export function NightCommandButton({
  command,
  label,
  onApplied,
  idempotencyKey,
}: NightCommandButtonProps) {
  const [state, setState] = useState<DispatchState>({ kind: 'idle' })
  const dispatchingRef = useRef(false)

  async function handleClick(): Promise<void> {
    if (dispatchingRef.current) return
    dispatchingRef.current = true
    setState({ kind: 'dispatching' })
    try {
      const resp = await dispatchNightCommand(command, idempotencyKey)
      setState({ kind: 'applied', outcome: resp.command.outcome })
      onApplied?.(resp.session)
    } catch (err) {
      const message = describeApiError(err)
      if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightNotReady) {
        setState({ kind: 'not_ready', message })
      } else if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightStateRejected) {
        setState({ kind: 'state_rejected', message })
      } else if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightAmbiguous) {
        setState({ kind: 'ambiguous', message })
      } else if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.nightCommandRefusedAuditUnavailable) {
        setState({ kind: 'audit_unavailable', message })
      } else {
        setState({ kind: 'error', message })
      }
    } finally {
      dispatchingRef.current = false
    }
  }

  return (
    <div className="night-command-button">
      <ScopedButton
        requiredScope="night:command"
        busy={state.kind === 'dispatching'}
        busyReason={`"${label}" is already being dispatched.`}
        onClick={() => void handleClick()}
      >
        {label}
      </ScopedButton>
      {state.kind === 'applied' && (
        <p role="status" className="night-command-button__success">
          {state.outcome === 'applied'
            ? `${label} accepted and applied.`
            : `${label} was already applied; no duplicate dispatch was made.`}
        </p>
      )}
      {state.kind === 'not_ready' && (
        <p role="alert" className="fpp-command-control__error">
          Not ready yet: {messageOr(state.message)}
        </p>
      )}
      {state.kind === 'state_rejected' && (
        <p role="alert" className="fpp-command-control__error">
          Refused for the session&rsquo;s current state: {messageOr(state.message)}
        </p>
      )}
      {state.kind === 'ambiguous' && (
        <p role="alert" className="fpp-command-control__error">
          The session is degraded and ambiguous: {messageOr(state.message)} Run &ldquo;End
          session&rdquo; and then &ldquo;Prepare site&rdquo; to recover.
        </p>
      )}
      {state.kind === 'audit_unavailable' && (
        <p role="alert" className="fpp-command-control__error">
          Refused, the audit store is unavailable, so nothing was dispatched or recorded:{' '}
          {messageOr(state.message)}
        </p>
      )}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

// The coordinator's own `detail` text is already the full actionable
// message (matching problem.ts's own "listed individually... the
// coordinator's detail is already the full actionable message" posture
// for the sibling problem types this component dispatches on) — this
// exists only so the four alert paragraphs above read as one sentence
// rather than "prefix: undefined" if a caught error somehow carried no
// message at all.
function messageOr(message: string): string {
  return message === '' ? 'no further detail was given.' : message
}
