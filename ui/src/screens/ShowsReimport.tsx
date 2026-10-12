import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, listFPPPlaylistDefinitions, republishFPPPlaylistDefinitions } from '../api'
import { Button, StatusPair } from '../kit'
import { describeApiError } from '../domain/session'
import { formatClock } from '../domain/time'
import { latestDefinitionReceivedMs } from './showsModel'

export const REIMPORT_POLL_MS = 3_000
export const REIMPORT_TIMEOUT_MS = 30_000

type ReimportState =
  | { phase: 'idle' }
  | { phase: 'requesting' }
  | { phase: 'sent' }
  | { phase: 'landed'; receivedAt: string }
  | { phase: 'resent'; detail: string; skipped: boolean }
  | { phase: 'unconfirmed'; reason: string }

const NO_ANSWER = 'The FPP host did not answer. Update its ShowMesh plugin, then try again.'
const NO_NEW_COPY = `No new copy arrived in ${REIMPORT_TIMEOUT_MS / 1000} seconds. Check the FPP host, then try again.`

const ALREADY_CURRENT = 'ShowMesh already had this playlist. Nothing changed.'
const SOME_NOT_RESENT = 'The FPP host did not resend some playlists. Restart its ShowMesh plugin.'

function unconfirmedReason(err: unknown): string {
  return err instanceof ApiError && err.status === 502 ? NO_ANSWER : describeApiError(err)
}


/** Sends the republish request, then watches the stored definitions for a copy received after the request. */
function useReimport(instanceId: string | null, instanceUuid: string, playlistName: string, onLanded: () => void) {
  const [state, setState] = useState<ReimportState>({ phase: 'idle' })
  const run = useRef(0)
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null)

  useEffect(
    () => () => {
      run.current += 1
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

  const start = useCallback(() => {
    if (instanceId === null) return
    const mine = ++run.current
    const current = () => run.current === mine
    setState({ phase: 'requesting' })
    const newestReceived = async () => latestDefinitionReceivedMs((await listFPPPlaylistDefinitions()).definitions, instanceUuid, playlistName)
    void (async () => {
      try {
        const baseline = await newestReceived()
        const { republish } = await republishFPPPlaylistDefinitions(instanceId)
        const requestId = republish.requestId
        if (!current()) return
        setState({ phase: 'sent' })
        const deadline = Date.now() + REIMPORT_TIMEOUT_MS
        while (Date.now() < deadline) {
          await sleep(REIMPORT_POLL_MS)
          if (!current()) return
          const progress = await republishFPPPlaylistDefinitions(instanceId, requestId).then(
            (r) => r.republish,
            () => null,
          )
          const latest = await newestReceived().catch(() => null)
          if (!current()) return
          if (latest !== null && (baseline === null || latest > baseline)) {
            setState({ phase: 'landed', receivedAt: new Date(latest).toISOString() })
            onLanded()
            return
          }
          if (progress !== null && !progress.sweepPending) {
            const skipped = progress.definitionsRefusedTerminally > 0
            setState({ phase: 'resent', skipped, detail: skipped ? SOME_NOT_RESENT : ALREADY_CURRENT })
            return
          }
        }
        setState({ phase: 'unconfirmed', reason: NO_NEW_COPY })
      } catch (err) {
        if (current()) setState({ phase: 'unconfirmed', reason: unconfirmedReason(err) })
      }
    })()
  }, [instanceId, instanceUuid, playlistName, onLanded, sleep])

  return { state, start }
}

/** The Re-import button and the plain outcome beside it: sent, landed, or unconfirmed with the reason. */
export function ReimportControl({
  instanceId,
  instanceUuid,
  playlistName,
  blockedReason,
  onLanded,
}: {
  instanceId: string | null
  instanceUuid: string
  playlistName: string
  blockedReason: string | null
  onLanded: () => void
}) {
  const { state, start } = useReimport(instanceId, instanceUuid, playlistName, onLanded)
  const reason = blockedReason ?? (instanceId === null ? 'This FPP host is not connected to the coordinator.' : null)
  const busy = state.phase === 'requesting' || state.phase === 'sent'

  return (
    <div className="sm-stack-3">
      <Button onClick={start} disabled={busy || reason !== null} title={reason ?? undefined}>
        {state.phase === 'requesting' ? 'Requesting…' : 'Re-import'}
      </Button>
      {reason !== null && <p className="sm-small sm-muted">{reason}</p>}
      {state.phase === 'sent' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="pending" label="Sent" />
          <span className="sm-verdict__detail">Asked. Waiting for the new copy from the FPP host.</span>
        </p>
      )}
      {state.phase === 'landed' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="good" label="Landed" />
          <span className="sm-verdict__detail">The coordinator received a new copy of this playlist at {formatClock(state.receivedAt) ?? 'an unrecorded time'}.</span>
        </p>
      )}
      {state.phase === 'resent' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone={state.skipped ? 'warn' : 'pending'} label={state.skipped ? 'Unconfirmed' : 'Resent'} />
          <span className="sm-verdict__detail">{state.detail}</span>
        </p>
      )}
      {state.phase === 'unconfirmed' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="warn" label="Unconfirmed" />
          <span className="sm-verdict__detail">{state.reason}</span>
        </p>
      )}
    </div>
  )
}
