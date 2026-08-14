import { nextFPPPlaylistItem, pauseFPPPlaylist, prevFPPPlaylistItem, resumeFPPPlaylist } from '../api'
import { useFPPCommandCall } from '../app/useFPPCommand'
import { evaluateNextItemHazard } from '../app/fppSignals'
import type { Evidence } from '../app/types'
import { FPPCommandOutcome } from './FPPCommandOutcome'
import { ScopedButton } from './ScopedButton'

// Four of the eight Step 8 primitives (docs/bench/fpp-command-vocabulary.md
// section 4): pausePlaylist, resumePlaylist, prevPlaylistItem, and
// nextPlaylistItem. Grouped in one file because three of the four are
// otherwise-identical zero-argument dispatches — same idiom as
// FPPStopPlaylistControl (Step 7), generalized through useFPPCommandCall
// and FPPCommandOutcome rather than copy-pasted four times.
// nextPlaylistItem is the one exception: capture section 3.5 measured
// "Next Playlist Item" at the last item of a playlist ENDING it rather
// than no-opping or wrapping (identical on a one-item playlist — a
// single Next stops the show), and FPP answers "Next Item Playing" in
// BOTH cases, so its own response text cannot warn the operator. This
// file's own FPPNextPlaylistItemControl is what does — see its own
// comment.

export interface FPPPausePlaylistControlProps {
  instanceId: string
}

export function FPPPausePlaylistControl({ instanceId }: FPPPausePlaylistControlProps) {
  const { state, run } = useFPPCommandCall()
  const submitting = state.kind === 'submitting'
  return (
    <div className="fpp-command-control">
      <ScopedButton
        requiredScope="fpp:command"
        busy={submitting}
        onClick={() => run(() => pauseFPPPlaylist(instanceId))}
      >
        {submitting ? 'Pausing…' : 'Pause'}
      </ScopedButton>
      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm playback actually paused…
        </p>
      )}
      {state.kind === 'result' && (
        <FPPCommandOutcome result={state.result} confirmedSummary="playback paused" />
      )}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

export interface FPPResumePlaylistControlProps {
  instanceId: string
}

export function FPPResumePlaylistControl({ instanceId }: FPPResumePlaylistControlProps) {
  const { state, run } = useFPPCommandCall()
  const submitting = state.kind === 'submitting'
  return (
    <div className="fpp-command-control">
      <ScopedButton
        requiredScope="fpp:command"
        busy={submitting}
        onClick={() => run(() => resumeFPPPlaylist(instanceId))}
      >
        {submitting ? 'Resuming…' : 'Resume'}
      </ScopedButton>
      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm playback actually resumed…
        </p>
      )}
      {state.kind === 'result' && (
        <FPPCommandOutcome result={state.result} confirmedSummary="playback resumed" />
      )}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

export interface FPPPrevPlaylistItemControlProps {
  instanceId: string
}

// Prev carries no end-of-playlist hazard: capture section 3.5 exercised
// Next past the last item and measured it ending the playlist, but did
// NOT exercise (and this control does not claim) the equivalent for
// Prev before the first item — see fppcommand_evidence.go's
// evaluatePrevItemEvidence doc comment, "no idle fallback, unlike
// evaluateNextItemEvidence."
export function FPPPrevPlaylistItemControl({ instanceId }: FPPPrevPlaylistItemControlProps) {
  const { state, run } = useFPPCommandCall()
  const submitting = state.kind === 'submitting'
  return (
    <div className="fpp-command-control">
      <ScopedButton
        requiredScope="fpp:command"
        busy={submitting}
        onClick={() => run(() => prevFPPPlaylistItem(instanceId))}
      >
        {submitting ? 'Going back…' : 'Previous Item'}
      </ScopedButton>
      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm the item actually changed…
        </p>
      )}
      {state.kind === 'result' && (
        <FPPCommandOutcome
          result={state.result}
          confirmedSummary="moved to the previous item"
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

export interface FPPNextPlaylistItemControlProps {
  instanceId: string
  /** This FPP instance's current observations — needed to evaluate whether the NEXT press would end the show (fpp.playlist.index/fpp.playlist.count), never fetched separately (ADR-014/ADR-022: this UI reaches ShowMesh only through the API's own snapshot/stream, never a second request of its own invention). */
  observations: readonly Evidence[]
}

// The one primitive this document (CLAUDE.md item 3) singles out by
// name: "nextPlaylistItem CAN STOP THE SHOW, and the control must say
// so." Captured, measured, both on a 3-item and a 1-item bench
// playlist: at the last item, one more Next Playlist Item ends the
// playlist (status -> idle, index -> 0/0), and FPP's own response text
// ("Next Item Playing") is identical whether it skipped forward or
// ended the show. A control labelled only "Next" would be a stop button
// whenever the playlist is on its last item, so this one always renders
// a stated warning (never silence) when the position is unknown, and a
// stronger, unmissable one when it is known to be the last item —
// evaluateNextItemHazard (fppSignals.ts) is what tells those two states
// apart, and it defaults to "unknown" rather than "safe" whenever the
// evidence is missing or stale (CLAUDE.md: absence of evidence is not
// evidence of absence).
export function FPPNextPlaylistItemControl({ instanceId, observations }: FPPNextPlaylistItemControlProps) {
  const { state, run } = useFPPCommandCall()
  const submitting = state.kind === 'submitting'
  const hazard = evaluateNextItemHazard(observations)

  return (
    <div className="fpp-command-control">
      {hazard.knownLastItem && (
        <p role="alert" className="fpp-command-control__warning">
          Last item ({String(hazard.index?.value)}/{String(hazard.count?.value)}). Next Item
          will END the show, not skip within it.
        </p>
      )}
      {hazard.unknown && (
        <p className="text-muted">
          The current playlist position is not currently known, so whether the next press
          would end the show cannot be determined in advance.
        </p>
      )}
      <ScopedButton
        requiredScope="fpp:command"
        busy={submitting}
        onClick={() => run(() => nextFPPPlaylistItem(instanceId))}
      >
        {submitting ? 'Advancing…' : hazard.knownLastItem ? 'Next Item (ends show)' : 'Next Item'}
      </ScopedButton>
      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm the item actually changed (or the playlist
          ended)…
        </p>
      )}
      {state.kind === 'result' && (
        <FPPCommandOutcome result={state.result} confirmedSummary="the command was accepted" />
      )}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}
