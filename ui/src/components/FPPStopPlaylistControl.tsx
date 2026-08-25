import { useState } from 'react'
import { stopFPPPlaylist } from '../api'
import type { FPPCommandResult } from '../api'
import { describeApiError } from '../app/session'
import { ScopedButton } from './ScopedButton'

// The Operator UI's own first write (Step 7 seam C, ADR-001/ADR-003):
// `POST /api/v1/fpp/{instanceId}/commands` behind `fpp:command`. This is
// the real caller [ScopedButton] was built (Step 6) with none yet — see
// that component's own doc comment.
//
// Three states this component must be able to show, none of them
// optional per this step's own acceptance criteria: a command in flight,
// and an unconfirmed outcome, must both be visible states — a `200` from
// the coordinator is never rendered as unqualified success (ADR-003), the
// same rule showmeshctl's own `reportFPPCommandResult` and store.ts's own
// `stopFPPPlaylist` doc comment both spell out independently. The FOURTH
// state — the control rendering `unknown`/disabled rather than enabled
// when the scope list is stale or unavailable — is [ScopedButton]'s own
// job (ADR-024 decision 12), inherited here for free by wrapping it
// rather than rendering a bare `<button>`.
export interface FPPStopPlaylistControlProps {
  instanceId: string
}

type CallState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  | { kind: 'result'; result: FPPCommandResult }
  | { kind: 'error'; message: string }

export function FPPStopPlaylistControl({ instanceId }: FPPStopPlaylistControlProps) {
  const [state, setState] = useState<CallState>({ kind: 'idle' })

  async function handleClick(): Promise<void> {
    if (state.kind === 'submitting') return
    setState({ kind: 'submitting' })
    try {
      const result = await stopFPPPlaylist(instanceId)
      setState({ kind: 'result', result })
    } catch (err) {
      setState({ kind: 'error', message: describeApiError(err) })
    }
  }

  return (
    <div className="fpp-command-control">
      <ScopedButton requiredScope="fpp:command" onClick={() => void handleClick()}>
        {state.kind === 'submitting' ? 'Stopping…' : 'Stop Playlist'}
      </ScopedButton>
      {state.kind === 'submitting' && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm playback actually stopped…
        </p>
      )}
      {state.kind === 'result' && <FPPCommandOutcome result={state.result} />}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

// FPPCommandOutcome renders result.outcome literally rather than
// inferring anything from the fact a response arrived at all: "confirmed"
// and "unconfirmed" are rendered distinctly, and an empty outcome (the
// one accepted race a replay response can return — see
// v1.FPPCommandResult.outcome's own doc comment) is rendered as pending,
// never silently treated as either.
function FPPCommandOutcome({ result }: { result: FPPCommandResult }) {
  return (
    <div role="status">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used); nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {result.outcome === 'confirmed' && (
        <p className="fpp-command-control__confirmed">Confirmed: playback stopped.</p>
      )}
      {result.outcome === 'unconfirmed' && (
        <p role="alert" className="fpp-command-control__unconfirmed">
          Unconfirmed: {result.outcomeReason}
        </p>
      )}
      {result.outcome !== 'confirmed' && result.outcome !== 'unconfirmed' && (
        <p className="text-muted">Pending: this command has not yet resolved.</p>
      )}
      {result.attributionDegraded && (
        <p className="text-muted">
          Note: the coordinator could not record this command in its audit log; it ran
          anyway.
        </p>
      )}
    </div>
  )
}
