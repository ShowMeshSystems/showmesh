import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ApiError,
  PROBLEM_TYPE,
  getShowPlaylistDefinitionMovePreview,
  putShowPlaylist,
  type ConfigObjectSummary,
  type ShowPlaylistConfigResponse,
  type ShowPlaylistMovePreviewResponse,
} from '../api'
import { Button, ButtonRow, RuledStrip, StatusPair, Table, TableWrap } from '../kit'
import { describeApiError } from '../domain/session'
import { formatClock } from '../domain/time'
import { cueLabel } from './showsModel'

type Preview = ShowPlaylistMovePreviewResponse

type Failure = { reason: string; refused: boolean }
type Availability = { kind: 'checking' } | { kind: 'ready'; preview: Preview } | { kind: 'failed'; failure: Failure }
type Review = { kind: 'closed' } | { kind: 'loading' } | { kind: 'open'; preview: Preview } | { kind: 'failed'; failure: Failure }

const CHANGED_AFTER_REVIEW = 'This playlist was changed after you opened the review, so nothing was saved. Review the changes again.'

/** A 409 here is the coordinator declining to preview, not a read that failed. */
function failureOf(err: unknown): Failure {
  return { reason: describeApiError(err), refused: err instanceof ApiError && err.status === 409 }
}

/** Asks the coordinator, once per saved revision, whether FPP has a newer playlist; the review itself re-reads so it never shows a stale answer. */
export function usePlaylistMove(playlist: ShowPlaylistConfigResponse, onSaved: (response: ShowPlaylistConfigResponse) => void) {
  const [availability, setAvailability] = useState<Availability>({ kind: 'checking' })
  const [review, setReview] = useState<Review>({ kind: 'closed' })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [changedSinceReview, setChangedSinceReview] = useState(false)
  const [recheck, setRecheck] = useState(0)
  const reviewRun = useRef(0)

  useEffect(() => {
    let cancelled = false
    reviewRun.current += 1
    setAvailability({ kind: 'checking' })
    setReview({ kind: 'closed' })
    setSaveError(null)
    setChangedSinceReview(false)
    getShowPlaylistDefinitionMovePreview(playlist.id)
      .then((preview) => {
        if (!cancelled) setAvailability({ kind: 'ready', preview })
      })
      .catch((err: unknown) => {
        if (!cancelled) setAvailability({ kind: 'failed', failure: failureOf(err) })
      })
    return () => {
      cancelled = true
    }
  }, [playlist.id, playlist.revision, playlist.payload.fpp?.playlistHash, recheck])

  const open = useCallback(() => {
    const run = ++reviewRun.current
    setReview({ kind: 'loading' })
    setSaveError(null)
    setChangedSinceReview(false)
    getShowPlaylistDefinitionMovePreview(playlist.id)
      .then((preview) => {
        if (reviewRun.current === run) setReview({ kind: 'open', preview })
      })
      .catch((err: unknown) => {
        if (reviewRun.current === run) setReview({ kind: 'failed', failure: failureOf(err) })
      })
  }, [playlist.id])

  const close = useCallback(() => {
    reviewRun.current += 1
    setReview({ kind: 'closed' })
    setSaveError(null)
    setChangedSinceReview(false)
  }, [])

  const confirm = useCallback(() => {
    if (review.kind !== 'open' || review.preview.proposed === null) return
    const { proposed, revision } = review.preview
    setSaving(true)
    setSaveError(null)
    putShowPlaylist(playlist.id, proposed, revision)
      .then((response) => {
        setReview({ kind: 'closed' })
        onSaved(response)
      })
      .catch((err: unknown) => {
        if (err instanceof ApiError && err.problemType === PROBLEM_TYPE.configRevisionPreconditionFailed) {
          setChangedSinceReview(true)
          setSaveError(CHANGED_AFTER_REVIEW)
        } else {
          setSaveError(describeApiError(err))
        }
      })
      .finally(() => setSaving(false))
  }, [review, playlist.id, onSaved])

  const newer = availability.kind === 'ready' && availability.preview.newerAvailable ? availability.preview : null
  return { availability, newer, review, saving, saveError, changedSinceReview, open, close, confirm, recheck: () => setRecheck((n) => n + 1) }
}

export type PlaylistMove = ReturnType<typeof usePlaylistMove>

const OUTCOME: Record<string, { tone: 'good' | 'pending' | 'warn'; label: string }> = {
  kept: { tone: 'good', label: 'Kept' },
  moved: { tone: 'pending', label: 'Moved' },
  dropped: { tone: 'warn', label: 'Removed' },
}
const NEEDS_CHECK = { tone: 'warn' as const, label: 'Check' }

function refusalLabel(failure: Failure): string {
  return failure.refused ? 'Cannot review' : 'Read failed'
}

/** The notice and the button that open the review, or the plain reason the review cannot start. */
export function PlaylistMoveNotice({ move, blockedReason }: { move: PlaylistMove; blockedReason: string | null }) {
  if (move.availability.kind === 'failed') {
    return <RuledStrip absence="failed" label={refusalLabel(move.availability.failure)} fact={move.availability.failure.reason} />
  }
  if (move.newer === null) return null
  const busy = move.review.kind === 'loading' || move.review.kind === 'open'
  return (
    <div className="sm-stack-3">
      <p className="sm-small sm-muted">
        FPP&rsquo;s playlist changed at {formatClock(move.newer.newest?.receivedAt ?? '') ?? 'an unrecorded time'}. Review the changes to keep your cues on the right
        sequences.
      </p>
      <Button onClick={move.open} disabled={busy || blockedReason !== null} title={blockedReason ?? undefined}>
        {move.review.kind === 'loading' ? 'Reading…' : 'Review changes'}
      </Button>
      {blockedReason !== null && <p className="sm-small sm-muted">{blockedReason}</p>}
    </div>
  )
}

/** Every saved entry as kept, moved or removed, then the new entries with no cue. Nothing is written until the confirm button. */
export function PlaylistMoveReview({
  move,
  cues,
  confirmBlockedReason,
}: {
  move: PlaylistMove
  cues: readonly ConfigObjectSummary[]
  confirmBlockedReason: string | null
}) {
  const { review } = move
  if (review.kind === 'closed' || review.kind === 'loading') return null
  if (review.kind === 'failed') return <RuledStrip absence="failed" label={refusalLabel(review.failure)} fact={review.failure.reason} />
  const { preview } = review
  const canConfirm = preview.canConfirm && confirmBlockedReason === null

  return (
    <div className="sm-stack-3" role="region" aria-label="Changes in FPP's playlist">
      {!preview.canConfirm && <p className="sm-small sm-muted">{preview.summary}</p>}
      {preview.newerAvailable && (
        <>
          <TableWrap label="Saved cues and what happens to them, scrollable">
            <Table minWidth={520}>
              <thead>
                <tr>
                  <th scope="col">Cue</th>
                  <th scope="col">Sequence</th>
                  <th scope="col">What happens</th>
                </tr>
              </thead>
              <tbody>
                {preview.entries.map((entry) => {
                  const outcome = entry.needsCheck ? NEEDS_CHECK : (OUTCOME[entry.outcome] ?? { tone: 'warn' as const, label: entry.outcome })
                  return (
                    <tr key={entry.entryId}>
                      <td>{cueLabel(cues, entry.cue)}</td>
                      <td>{entry.filename !== '' ? entry.filename : <span className="sm-faint">No sequence name saved</span>}</td>
                      <td className="sm-table__wrap">
                        <StatusPair tone={outcome.tone} label={outcome.label} />
                        <br />
                        <span className="sm-small sm-muted">{entry.summary}</span>
                      </td>
                    </tr>
                  )
                })}
              </tbody>
            </Table>
          </TableWrap>
          {preview.newEntries.length > 0 && (
            <div className="sm-stack-3">
              <TableWrap label="New in FPP's playlist, with no cue, scrollable">
                <Table minWidth={520}>
                  <thead>
                    <tr>
                      <th scope="col">New in FPP&rsquo;s playlist</th>
                      <th scope="col">Cue</th>
                    </tr>
                  </thead>
                  <tbody>
                    {preview.newEntries.map((entry) => (
                      <tr key={`${entry.section}:${entry.position}`}>
                        <td>
                          <span className="sm-data sm-small sm-faint">
                            {entry.section} · {entry.position}
                          </span>
                          <br />
                          {entry.name !== '' ? entry.name : '(no filename)'}
                        </td>
                        <td className="sm-table__wrap">
                          <StatusPair tone="pending" label="No cue" />
                          <br />
                          <span className="sm-small sm-muted">{entry.summary}</span>
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </TableWrap>
            </div>
          )}
        </>
      )}
      <ButtonRow>
        {move.changedSinceReview ? (
          <Button variant="primary" onClick={move.open}>
            Review again
          </Button>
        ) : (
          <Button variant="primary" onClick={move.confirm} disabled={!canConfirm || move.saving} title={confirmBlockedReason ?? undefined}>
            {move.saving ? 'Saving…' : 'Move to FPP’s newest playlist'}
          </Button>
        )}
        <Button variant="quiet" onClick={move.close} disabled={move.saving}>
          Cancel
        </Button>
      </ButtonRow>
      {confirmBlockedReason !== null && preview.canConfirm && <p className="sm-small sm-muted">{confirmBlockedReason}</p>}
      {move.saveError !== null && <RuledStrip absence="failed" label="Not saved" fact={move.saveError} />}
    </div>
  )
}
