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
  | { phase: 'unconfirmed'; reason: string }

const NO_ANSWER = 'The FPP host did not answer the request. Update the ShowMesh plugin on that host if it is out of date, then try again.'
const NO_NEW_COPY = `No new copy of this playlist arrived within ${REIMPORT_TIMEOUT_MS / 1000} seconds. The stored copy may already be current; if the playlist still looks wrong, check the FPP host and try again.`

function unconfirmedReason(err: unknown): string {
  return err instanceof ApiError && err.status === 502 ? NO_ANSWER : describeApiError(err)
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms))
}

/** Sends the republish request, then watches the stored definitions for a copy received after the request. */
function useReimport(instanceId: string | null, instanceUuid: string, playlistName: string, onLanded: () => void) {
  const [state, setState] = useState<ReimportState>({ phase: 'idle' })
  const run = useRef(0)

  useEffect(
    () => () => {
      run.current += 1
    },
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
        await republishFPPPlaylistDefinitions(instanceId)
        if (!current()) return
        setState({ phase: 'sent' })
        const deadline = Date.now() + REIMPORT_TIMEOUT_MS
        while (Date.now() < deadline) {
          await sleep(REIMPORT_POLL_MS)
          if (!current()) return
          const latest = await newestReceived().catch(() => null)
          if (!current()) return
          if (latest !== null && (baseline === null || latest > baseline)) {
            setState({ phase: 'landed', receivedAt: new Date(latest).toISOString() })
            onLanded()
            return
          }
        }
        setState({ phase: 'unconfirmed', reason: NO_NEW_COPY })
      } catch (err) {
        if (current()) setState({ phase: 'unconfirmed', reason: unconfirmedReason(err) })
      }
    })()
  }, [instanceId, instanceUuid, playlistName, onLanded])

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
  const reason = blockedReason ?? (instanceId === null ? 'This FPP host is not connected to the coordinator, so it cannot be asked to send the playlist again.' : null)
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
          <span className="sm-verdict__detail">The FPP host agreed to send this playlist again. Waiting for the new copy to arrive.</span>
        </p>
      )}
      {state.phase === 'landed' && (
        <p className="sm-verdict" role="status">
          <StatusPair tone="good" label="Landed" />
          <span className="sm-verdict__detail">The coordinator received a new copy of this playlist at {formatClock(state.receivedAt) ?? 'an unrecorded time'}.</span>
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
