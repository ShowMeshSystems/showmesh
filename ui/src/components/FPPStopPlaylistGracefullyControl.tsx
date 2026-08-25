import { useState } from 'react'
import { stopFPPPlaylistGracefully } from '../api'
import { useFPPCommandCall } from '../app/useFPPCommand'
import { FPPCommandOutcome } from './FPPCommandOutcome'
import { ScopedButton } from './ScopedButton'

export interface FPPStopPlaylistGracefullyControlProps {
  instanceId: string
}

// CLAUDE.md item 4 / capture section 3.3: stopPlaylistGracefully can
// report CONFIRMED while the show is still running. Its confirmation
// predicate (evaluateFPPStopGracefullyEvidence) accepts EITHER a
// stopping state or idle as success, because a graceful stop's terminal
// state is bounded by the currently playing item's own remaining
// runtime — measured holding "stopping gracefully" indefinitely against
// a 120-second item — not by any deadline ShowMesh could pick. The
// server's own outcomeReason says explicitly, even on a confirmed
// result, whether playback has actually ended or is only winding down.
//
// This control therefore NEVER renders its own "stopped" text on
// confirm: FPPCommandOutcome always prefers result.outcomeReason (which
// this predicate never leaves empty on confirm — see
// evaluateFPPStopGracefullyEvidence's own doc comment), and this
// component's confirmedSummary fallback is deliberately worded as
// "accepted", not "stopped", for the one race where the reason could
// somehow be empty.
export function FPPStopPlaylistGracefullyControl({ instanceId }: FPPStopPlaylistGracefullyControlProps) {
  const [afterLoop, setAfterLoop] = useState(false)
  const { state, run } = useFPPCommandCall()
  const submitting = state.kind === 'submitting'

  return (
    <div className="fpp-command-control">
      <label>
        <input
          type="checkbox"
          checked={afterLoop}
          disabled={submitting}
          onChange={(e) => setAfterLoop(e.target.checked)}
        />{' '}
        Wait for the current loop to finish first (afterLoop)
      </label>
      <ScopedButton
        requiredScope="fpp:command"
        busy={submitting}
        onClick={() => run(() => stopFPPPlaylistGracefully(instanceId, afterLoop))}
      >
        {submitting ? 'Stopping…' : 'Stop Gracefully'}
      </ScopedButton>
      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm FPP accepted the graceful stop; a
          "confirmed" result below can still mean the show is winding down, not stopped;
          read its own reason.
        </p>
      )}
      {state.kind === 'result' && (
        <FPPCommandOutcome
          result={state.result}
          confirmedSummary="FPP accepted the graceful stop request"
        />
      )}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}
