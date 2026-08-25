import type { FPPCommandResult } from '../app/types'

// Shared outcome rendering for every FPP primitive control BESIDES
// FPPStopPlaylistControl (Step 7's own control, left byte-for-byte
// unchanged — see that file's own comment; this component generalizes
// its inline `FPPCommandOutcome` function rather than replacing it, so a
// change here cannot regress Step 7's already-shipped acceptance
// criteria).
//
// Three rules every caller depends on, all load-bearing per CLAUDE.md's
// own Step 7 lessons:
//
//  1. `outcome` is rendered literally — "confirmed" and "unconfirmed" are
//     visibly distinct, and an empty outcome (the one accepted replay
//     race — FPPCommandResult.outcome's own doc comment) renders as
//     pending, never silently treated as either (ADR-003).
//
//  2. On "confirmed", `result.outcomeReason` — the SERVER's own words —
//     wins whenever the server sent one. `confirmedSummary` is only a
//     fallback used when the server left the reason empty, which several
//     predicates (stopPlaylist, pausePlaylist, resumePlaylist,
//     resumePlaylist, startPlaylist, setVolume) do deliberately, exactly
//     when there is nothing more honest to add
//     (internal/coordinator/api/fppcommand_evidence.go). Two predicates —
//     stopPlaylistGracefully and nextPlaylistItem — NEVER leave it empty
//     on confirm, specifically because "confirmed" alone would read as
//     "the show stopped" or "advanced to the next item" when the truth
//     might be "still winding down" or "the playlist just ended." This
//     component never invents a summary that could paper over that
//     server-stated nuance — see CLAUDE.md item 4 (stopPlaylistGracefully)
//     and item 3 (nextPlaylistItem) for the capture findings this
//     encodes.
//
//  3. `replay` and `attributionDegraded` are their own, separate notices
//     — never folded into the outcome text — matching
//     FPPStopPlaylistControl's own precedent exactly.
export interface FPPCommandOutcomeProps {
  result: FPPCommandResult
  /**
   * A short, honest label for what "confirmed" means for THIS primitive
   * (e.g. "playback paused"), used only when `result.outcomeReason` is
   * empty. Must never claim an effect the primitive's own predicate does
   * not actually check.
   */
  confirmedSummary: string
}

export function FPPCommandOutcome({ result, confirmedSummary }: FPPCommandOutcomeProps) {
  return (
    <div role="status">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used); nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {result.outcome === 'confirmed' && (
        <p className="fpp-command-control__confirmed">
          Confirmed: {result.outcomeReason || confirmedSummary}
        </p>
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
