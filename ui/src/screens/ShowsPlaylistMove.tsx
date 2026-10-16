import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ApiError,
  PROBLEM_TYPE,
  getShowPlaylistDefinitionMovePreview,
  listFPPPlaylistDefinitions,
  putShowPlaylist,
  republishFPPPlaylistDefinitions,
  type ConfigObjectSummary,
  type ShowPlaylistConfigResponse,
  type ShowPlaylistMovePreviewResponse,
} from '../api'
import { Button, ButtonRow, RuledStrip, StatusPair, Table, TableWrap } from '../kit'
import { describeApiError } from '../domain/session'
import { formatClock } from '../domain/time'
import { cueLabel, latestDefinitionReceivedMs } from './showsModel'

export const UPDATE_POLL_MS = 3_000
export const UPDATE_TIMEOUT_MS = 30_000

type Preview = ShowPlaylistMovePreviewResponse

type Failure = { reason: string; refused: boolean }
type Availability = { kind: 'checking' } | { kind: 'ready'; preview: Preview } | { kind: 'failed'; failure: Failure }
type Review = { kind: 'closed' } | { kind: 'loading' } | { kind: 'open'; preview: Preview } | { kind: 'failed'; failure: Failure }
type UpdatePhase =
  | { kind: 'idle' }
  | { kind: 'checking' }
  | { kind: 'sent' }
  | { kind: 'unchanged'; since: string | null }
  | { kind: 'updated'; receivedAt: string | null }
  | { kind: 'unconfirmed'; reason: string }

/** What the Update from FPP flow needs from the page: the FPP host the playlist is bound to, and whether this principal may ask it to resend. */
export type UpdateSource = {
  instanceId: string | null
  instanceUuid: string
  playlistName: string
  /** Why a resend cannot be asked for, or null when it can. The review and move need only config:write. */
  resendBlockedReason: string | null
}

const CHANGED_AFTER_REVIEW = 'This playlist changed during the review. Nothing was saved. Review again.'
const NO_ANSWER = 'The FPP host did not answer. Update its ShowMesh plugin, then try again.'
const NO_NEW_COPY = `FPP did not send a new copy in ${UPDATE_TIMEOUT_MS / 1000} seconds. Check the FPP host, then try again.`
const SOME_NOT_RESENT = 'The FPP host did not resend some playlists. Restart its ShowMesh plugin, then try again.'
const NOT_CONNECTED = 'This FPP host is not connected to the coordinator.'

function unconfirmedReason(err: unknown): string {
  return err instanceof ApiError && err.status === 502 ? NO_ANSWER : describeApiError(err)
}

/** A 409 from the preview means the coordinator holds no copy yet; that is a reason to ask FPP, not a failure. */
function previewOrNull(id: string): Promise<Preview | null> {
  return getShowPlaylistDefinitionMovePreview(id).catch((err: unknown) => {
    if (err instanceof ApiError && err.status === 409) return null
    throw err
  })
}

/** A 409 here is the coordinator declining to preview, not a read that failed. */
function failureOf(err: unknown): Failure {
  return { reason: describeApiError(err), refused: err instanceof ApiError && err.status === 409 }
}

/**
 * Asks the coordinator, once per saved revision, whether FPP has a newer playlist, and runs the one
 * Update from FPP flow: use the newer copy the coordinator already holds, or ask FPP to resend and
 * wait for it, then open the review. The review itself re-reads so it never shows a stale answer.
 */
export function usePlaylistMove(playlist: ShowPlaylistConfigResponse, source: UpdateSource, onSaved: (response: ShowPlaylistConfigResponse) => void) {
  const [availability, setAvailability] = useState<Availability>({ kind: 'checking' })
  const [review, setReview] = useState<Review>({ kind: 'closed' })
  const [phase, setPhase] = useState<UpdatePhase>({ kind: 'idle' })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [changedSinceReview, setChangedSinceReview] = useState(false)
  const [recheck, setRecheck] = useState(0)
  const reviewRun = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      reviewRun.current += 1
      if (timer.current !== null) clearTimeout(timer.current)
    },
    [],
  )

  const sleep = useCallback(
    (ms: number) =>
      new Promise<void>((resolve) => {
        timer.current = setTimeout(resolve, ms)
      }),
    [],
  )

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
    setPhase({ kind: 'idle' })
    setSaveError(null)
    setChangedSinceReview(false)
  }, [])

  const { instanceId, instanceUuid, playlistName, resendBlockedReason } = source

  /** Update from FPP: the newer copy the coordinator holds, or a resend from FPP, then the review. */
  const start = useCallback(() => {
    const run = ++reviewRun.current
    const current = () => reviewRun.current === run
    setReview({ kind: 'closed' })
    setSaveError(null)
    setChangedSinceReview(false)
    setPhase({ kind: 'checking' })
    const newestReceived = async () => latestDefinitionReceivedMs((await listFPPPlaylistDefinitions()).definitions, instanceUuid, playlistName)
    const openReview = (preview: Preview) => {
      setAvailability({ kind: 'ready', preview })
      setReview({ kind: 'open', preview })
      setPhase({ kind: 'idle' })
    }
    void (async () => {
      try {
        const held = await previewOrNull(playlist.id)
        if (!current()) return
        if (held !== null && held.newerAvailable) {
          openReview(held)
          return
        }
        if (instanceId === null) {
          setPhase({ kind: 'unconfirmed', reason: NOT_CONNECTED })
          return
        }
        if (resendBlockedReason !== null) {
          setPhase({ kind: 'unconfirmed', reason: resendBlockedReason })
          return
        }
        const baseline = await newestReceived()
        const { republish } = await republishFPPPlaylistDefinitions(instanceId)
        if (!current()) return
        setPhase({ kind: 'sent' })
        const deadline = Date.now() + UPDATE_TIMEOUT_MS
        while (Date.now() < deadline) {
          await sleep(UPDATE_POLL_MS)
          if (!current()) return
          const progress = await republishFPPPlaylistDefinitions(instanceId, republish.requestId).then(
            (r) => r.republish,
            () => null,
          )
          const latest = await newestReceived().catch(() => null)
          if (!current()) return
          if (latest !== null && (baseline === null || latest > baseline)) {
            const preview = await previewOrNull(playlist.id)
            if (!current()) return
            if (preview !== null && preview.newerAvailable) {
              openReview(preview)
            } else {
              setPhase({ kind: 'unchanged', since: new Date(latest).toISOString() })
            }
            return
          }
          if (progress !== null && !progress.sweepPending) {
            if (progress.definitionsRefusedTerminally > 0) {
              setPhase({ kind: 'unconfirmed', reason: SOME_NOT_RESENT })
            } else {
              setPhase({ kind: 'unchanged', since: baseline === null ? null : new Date(baseline).toISOString() })
            }
            return
          }
        }
        setPhase({ kind: 'unconfirmed', reason: NO_NEW_COPY })
      } catch (err) {
        if (current()) setPhase({ kind: 'unconfirmed', reason: unconfirmedReason(err) })
      }
    })()
  }, [playlist.id, instanceId, instanceUuid, playlistName, resendBlockedReason, sleep])

  const confirm = useCallback(() => {
    if (review.kind !== 'open' || review.preview.proposed === null) return
    const { proposed, revision } = review.preview
    setSaving(true)
    setSaveError(null)
    const receivedAt = review.preview.newest?.receivedAt ?? null
    putShowPlaylist(playlist.id, proposed, revision)
      .then((response) => {
        setReview({ kind: 'closed' })
        setPhase({ kind: 'updated', receivedAt })
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
  return { availability, newer, review, phase, saving, saveError, changedSinceReview, open, close, start, confirm, recheck: () => setRecheck((n) => n + 1) }
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

/** The one Update from FPP button and the plain outcome beside it; the review opens below when there is something to move. */
export function PlaylistUpdateControl({ move, blockedReason }: { move: PlaylistMove; blockedReason: string | null }) {
  const busy = move.phase.kind === 'checking' || move.phase.kind === 'sent' || move.review.kind === 'loading' || move.review.kind === 'open'
  return (
    <div className="sm-stack-3">
      {move.availability.kind === 'failed' && (
        <RuledStrip absence="failed" label={refusalLabel(move.availability.failure)} fact={move.availability.failure.reason} />
      )}
      {move.newer !== null && move.review.kind === 'closed' && (
        <p className="sm-small sm-muted">FPP&rsquo;s playlist changed at {formatClock(move.newer.newest?.receivedAt ?? '') ?? 'an unrecorded time'}. Update to review the changes.</p>
      )}
      <Button onClick={move.start} disabled={busy || blockedReason !== null} title={blockedReason ?? undefined}>
        {move.phase.kind === 'checking' ? 'Checking…' : 'Update from FPP'}
      </Button>
      {blockedReason !== null && <p className="sm-small sm-muted">{blockedReason}</p>}
      {move.phase.kind === 'sent' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="pending" label="Sent" />
          <span className="sm-verdict__detail">Asked FPP to send its playlists again. Waiting for the copy.</span>
        </p>
      )}
      {move.phase.kind === 'unchanged' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="good" label="Unchanged" />
          <span className="sm-verdict__detail">
            FPP&rsquo;s playlist is unchanged{move.phase.since !== null ? ` since ${formatClock(move.phase.since) ?? 'an unrecorded time'}` : ''}.
          </span>
        </p>
      )}
      {move.phase.kind === 'updated' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="good" label="Updated" />
          <span className="sm-verdict__detail">
            This playlist now follows FPP&rsquo;s copy{move.phase.receivedAt !== null ? ` from ${formatClock(move.phase.receivedAt) ?? 'an unrecorded time'}` : ''}.
          </span>
        </p>
      )}
      {move.phase.kind === 'unconfirmed' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="warn" label="Unconfirmed" />
          <span className="sm-verdict__detail">{move.phase.reason}</span>
        </p>
      )}
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
                      <td>{entry.filename !== '' ? entry.filename : entry.matchedBy === 'previousSequence' ? entry.previousSequence : <span className="sm-faint">No sequence name saved</span>}</td>
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
