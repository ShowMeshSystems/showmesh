import { useState } from 'react'
import { ApiError, startFPPPlaylist } from '../api'
import { PROBLEM_TYPE } from '../api/problem'
import { describeApiError } from '../app/session'
import type { FPPCommandResult } from '../app/types'
import { FPPCommandOutcome } from './FPPCommandOutcome'
import { ScopedButton } from './ScopedButton'

export interface FPPStartPlaylistControlProps {
  instanceId: string
}

// Step 8 client-side review finding 5, fixed on the wire: this endpoint's
// ifBusy="refuse" guard (the default) produces TWO distinct 409s
// (internal/coordinator/api/problem.go), not one:
//
//   - fppStartPlaylistBusyProblem — a DIFFERENT playlist is CONFIRMED
//     playing, `type` `fpp-start-playlist-busy` (PROBLEM_TYPE
//     .fppStartPlaylistBusy — originally `conflict`, then, per a LATER
//     review finding's "half-applied" catch, its own dedicated type; see
//     that constant's own comment in problem.ts), and detail names it
//     ("instance %q is currently playing %q...").
//   - fppStartPlaylistEvidenceNotCurrentProblem — the coordinator could
//     not tell what is playing because the evidence it would need is not
//     current (CLAUDE.md's own recurring rule: absence of evidence is not
//     evidence of absence), `type`
//     `fpp-start-playlist-evidence-not-current` (PROBLEM_TYPE
//     .fppStartPlaylistEvidenceNotCurrent).
//
// Before the first fix both cases carried the SAME `type` (`conflict`), so
// this component told them apart by matching a substring of the server's
// own English `detail` text — a client parsing prose across a versioned
// contract boundary, invisible to every test, and one `detail` reword away
// from silently reverting to a button that claims "replace what is
// currently playing" in the case where the coordinator just said it does
// not know what is playing. This now branches on `err.problemType` (the
// wire `type`, surfaced by ApiError — see errors.ts): the ONLY thing that
// changed is what decides which CTA renders. `message` (and therefore the
// alert text an operator reads) still comes verbatim from the
// coordinator's own `detail`, exactly as before. Both known types are
// matched explicitly (never "anything that is not evidenceNotCurrent"):
// an UNRECOGNIZED 409 type falls through this control's own generic
// `error` state below instead of being silently mislabeled as "a
// different playlist is playing".
function classifyStartPlaylistConflict(problemType: string | undefined): 'differentPlaylistPlaying' | 'evidenceNotCurrent' | 'unknown' {
  if (problemType === PROBLEM_TYPE.fppStartPlaylistEvidenceNotCurrent) return 'evidenceNotCurrent'
  if (problemType === PROBLEM_TYPE.fppStartPlaylistBusy) return 'differentPlaylistPlaying'
  return 'unknown'
}

type CallState =
  | { kind: 'idle' }
  | { kind: 'submitting' }
  // Finding 6: dispatchedPlaylist is captured from the input AT DISPATCH
  // TIME, never re-read from the live `playlist` state below — the input
  // is re-enabled once a result arrives (nothing here disables it after
  // success), so reading `playlist` at RENDER time would let an operator
  // who edits the name after a confirmed dispatch retroactively rewrite
  // what the confirmation claims happened. Reachable on the ordinary path
  // because evaluateStartPlaylistEvidence leaves outcomeReason empty on
  // every clean confirm, so FPPCommandOutcome falls back to
  // confirmedSummary for the text an operator actually reads.
  | { kind: 'result'; result: FPPCommandResult; dispatchedPlaylist: string }
  // Capture section 5: FPP's own "Start Playlist" always replaces
  // whatever is running, silently, and its own "ifNotRunning" argument
  // does not protect against that (it only suppresses a RESTART of the
  // SAME playlist that is already running). ShowMesh's own decision,
  // recorded there, is the opposite default: startPlaylist here refuses
  // rather than interrupting a DIFFERENT running playlist, unless the
  // request explicitly says ifBusy="replace". This state is that
  // refusal (a 409), carrying the coordinator's own `detail` text — never
  // retried automatically. `reason` is [classifyStartPlaylistConflict]'s
  // own output, used only to choose the CTA's wording (see that
  // function's own doc comment); the message text itself always comes
  // from the coordinator, never invented here.
  | { kind: 'busy'; message: string; reason: 'differentPlaylistPlaying' | 'evidenceNotCurrent' }
  | { kind: 'error'; message: string }

// No playlist listing endpoint exists on this API (api/openapi.yaml has
// nothing resembling "list playlists"), and ADR-014/ADR-022 forbid this
// UI reaching an FPP host directly to fetch one — so a plain text input
// is the honest answer here, not a picker this coordinator cannot back.
export function FPPStartPlaylistControl({ instanceId }: FPPStartPlaylistControlProps) {
  const [playlist, setPlaylist] = useState('')
  const [repeat, setRepeat] = useState(false)
  const [state, setState] = useState<CallState>({ kind: 'idle' })

  async function dispatch(ifBusy: 'refuse' | 'replace'): Promise<void> {
    if (state.kind === 'submitting') return
    const dispatchedPlaylist = playlist
    if (dispatchedPlaylist.trim() === '') {
      setState({ kind: 'error', message: 'Enter a playlist name.' })
      return
    }
    setState({ kind: 'submitting' })
    try {
      const result = await startFPPPlaylist(instanceId, dispatchedPlaylist, repeat, ifBusy)
      setState({ kind: 'result', result, dispatchedPlaylist })
    } catch (err) {
      // A 409 here is ALWAYS the ifBusy="refuse" guard (a replayed
      // idempotency key against different params is also a 409, but this
      // control mints a fresh key on every dispatch — see
      // ApiStore.dispatchFPPCommand — so that case cannot occur from this
      // control's own two call sites, and would surface as
      // classifyStartPlaylistConflict's 'unknown' if it somehow did). No
      // dedicated error CLASS exists for either of the guard's two
      // `type`s (problem.ts's own comment: the coordinator's `detail` is
      // already the full actionable message), so this is the same
      // generic-ApiError-plus-status pattern Configuration.tsx uses for
      // its own 404 — but see [classifyStartPlaylistConflict] for why the
      // CTA still branches on `err.problemType`, not on this status check
      // alone.
      if (err instanceof ApiError && err.status === 409) {
        const reason = classifyStartPlaylistConflict(err.problemType)
        const message = describeApiError(err)
        if (reason === 'unknown') {
          // A 409 `type` this control does not recognize — never invent a
          // busy/replace CTA for a cause we cannot actually name; fall
          // through to the same generic error rendering every other
          // unrecognized failure uses.
          setState({ kind: 'error', message })
          return
        }
        setState({ kind: 'busy', message, reason })
        return
      }
      setState({ kind: 'error', message: describeApiError(err) })
    }
  }

  const submitting = state.kind === 'submitting'

  return (
    <div className="fpp-command-control">
      <label>
        Playlist name{' '}
        <input
          type="text"
          value={playlist}
          disabled={submitting}
          onChange={(e) => setPlaylist(e.target.value)}
        />
      </label>
      <label>
        <input
          type="checkbox"
          checked={repeat}
          disabled={submitting}
          onChange={(e) => setRepeat(e.target.checked)}
        />{' '}
        Repeat
      </label>
      <ScopedButton requiredScope="fpp:command" busy={submitting} onClick={() => void dispatch('refuse')}>
        {submitting ? 'Starting…' : 'Start Playlist'}
      </ScopedButton>
      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm the playlist is actually playing…
        </p>
      )}
      {state.kind === 'result' && (
        <FPPCommandOutcome
          result={state.result}
          confirmedSummary={`playlist "${state.dispatchedPlaylist}" is playing`}
        />
      )}
      {state.kind === 'busy' && (
        <div role="alert" className="fpp-command-control__error">
          <p>{state.message}</p>
          {/* The operator's own extra click is the interruption decision —
              this control never resends with ifBusy="replace" on its own.
              The label depends on `state.reason` (classifyStartPlaylistConflict):
              only the "a different playlist is confirmed playing" case may
              say "replace what is currently playing" — the
              evidence-not-current case never claims to know what, if
              anything, is currently playing, because the coordinator's own
              detail text above just said it could not tell. */}
          <ScopedButton requiredScope="fpp:command" onClick={() => void dispatch('replace')}>
            {state.reason === 'differentPlaylistPlaying'
              ? 'Start anyway (replace what is currently playing)'
              : 'Start anyway (override the busy check)'}
          </ScopedButton>
        </div>
      )}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}
