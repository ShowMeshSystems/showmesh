import { useEffect, useState } from 'react'
import { deleteFPPPlaylistEntryObservation, listFPPPlaylistEntryObservations } from '../api'
import type { FPPPlaylistEntryObservation } from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from './ScopedButton'

// TRACK-H-H2-SPEC.md §5.1's show-night recovery path, given a UI home: an
// FPP instance's stored playlist-entry observation and sequence anchor
// can go stale mid-show, and the only fix is deleting it so the next
// plugin report re-establishes it. Previously reachable only from
// `showmeshctl fpp reset-observation-sequence --confirm` -- a terminal an
// operator should not have to reach for at the worst possible moment.
//
// This is deliberately behind the SAME two-click arm-then-confirm pattern
// as ShowActive.tsx / NightSessionActive.tsx, not a browser confirm()
// dialog: destructive, not undoable, and the operator must see exactly
// what is about to be discarded (GET .../playlist-entry-observations'
// entry identity, sequence, and timestamps) before committing.
export interface FPPResetObservationSequenceControlProps {
  /**
   * FPPInstance.instanceUuid, NOT instanceId -- the observation store is
   * keyed on FPP's own reported SystemUUID, which is null until this
   * endpoint has reported one at least once.
   */
  instanceUuid: string | null
}

const REQUIRED_SCOPE = 'fpp:command'

type ObservationFetchState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; observation: FPPPlaylistEntryObservation | undefined }

export function FPPResetObservationSequenceControl({ instanceUuid }: FPPResetObservationSequenceControlProps) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, REQUIRED_SCOPE)

  const [fetchState, setFetchState] = useState<ObservationFetchState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const [armed, setArmed] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  // Set once a clear has actually succeeded, so the fetch that follows is
  // rendered as "confirming the observed result", never as an ordinary
  // reload -- and cleared again the moment the operator arms a new clear,
  // so a second clear does not inherit the first one's messaging.
  const [justCleared, setJustCleared] = useState(false)

  useEffect(() => {
    if (instanceUuid === null) return
    let cancelled = false
    setFetchState({ kind: 'loading' })
    listFPPPlaylistEntryObservations()
      .then((resp) => {
        if (cancelled) return
        const observation = resp.observations.find((o) => o.instanceUuid === instanceUuid)
        setFetchState({ kind: 'loaded', observation })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setFetchState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [instanceUuid, reloadGeneration])

  if (instanceUuid === null) {
    return (
      <p className="text-muted">
        This instance has not yet reported an instance UUID, so no stored playlist-entry
        observation or sequence anchor can exist for it to clear.
      </p>
    )
  }

  function arm(): void {
    setDeleteError(null)
    setJustCleared(false)
    setArmed(true)
  }

  function cancel(): void {
    setArmed(false)
    setDeleteError(null)
  }

  async function confirmClear(): Promise<void> {
    if (deleting) return
    // `instanceUuid === null` returns early above before this handler can
    // ever be wired up (the whole armed/confirm UI is unreachable), but
    // narrow explicitly rather than asserting it away so a future
    // reordering fails safe instead of dereferencing null.
    if (instanceUuid === null) return
    setDeleting(true)
    setDeleteError(null)
    try {
      await deleteFPPPlaylistEntryObservation(instanceUuid)
      setArmed(false)
      setJustCleared(true)
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      // Deliberately does not dismiss the confirmation panel or claim
      // anything about the stored observation: the delete was refused,
      // so the last known observed state (rendered below) stands.
      setDeleteError(describeApiError(err))
    } finally {
      setDeleting(false)
    }
  }

  const hasStoredObservation = fetchState.kind === 'loaded' && fetchState.observation !== undefined

  return (
    <div>
      {justCleared ? (
        <ClearedOutcome fetchState={fetchState} />
      ) : (
        <StoredObservationPanel fetchState={fetchState} />
      )}

      {!gate.allowed && (
        <p className="text-muted" role="status">
          Requires the <code>{REQUIRED_SCOPE}</code> scope. {gate.reason}
        </p>
      )}

      {gate.allowed && !armed && (
        <button
          type="button"
          onClick={arm}
          disabled={fetchState.kind !== 'loaded' || !hasStoredObservation}
        >
          Clear stored observation…
        </button>
      )}

      {gate.allowed && armed && (
        <div className="panel panel--warning" role="alertdialog" aria-label="Confirm clearing stored observation">
          <p>
            <strong>About to clear the stored observation for &ldquo;{instanceUuid}&rdquo;.</strong>
          </p>
          <p>
            This deletes the stored playlist-entry observation and its sequence anchor. It is
            not undoable: the next plugin report re-establishes a fresh anchor from scratch.
          </p>
          {deleteError !== null && (
            <p role="alert" className="session-form__error">
              {deleteError}
            </p>
          )}
          <div style={{ display: 'flex', gap: '0.75rem' }}>
            <ScopedButton
              requiredScope={REQUIRED_SCOPE}
              onClick={() => void confirmClear()}
              busy={deleting}
              busyReason="Clearing…"
            >
              {deleting ? 'Clearing…' : 'Confirm: clear stored observation'}
            </ScopedButton>
            <button type="button" onClick={cancel} disabled={deleting}>
              Cancel
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// What is about to be discarded, rendered so the operator decides
// against evidence rather than blind -- entry identity, sequence, and
// every timestamp FPPPlaylistEntryObservation carries.
function StoredObservationPanel({ fetchState }: { fetchState: ObservationFetchState }) {
  if (fetchState.kind === 'loading') {
    return <p className="text-muted">Loading the stored observation…</p>
  }
  if (fetchState.kind === 'error') {
    return (
      <p role="alert" className="panel panel--error">
        {fetchState.message}
      </p>
    )
  }
  if (fetchState.observation === undefined) {
    return <p className="text-muted">No stored observation is currently held for this instance.</p>
  }
  return (
    <dl className="field-list" role="status">
      <dt>Entry key</dt>
      <dd>{fetchState.observation.entryKey ?? 'unknown'}</dd>
      <dt>Sequence</dt>
      <dd>{fetchState.observation.sequence}</dd>
      <dt>Action</dt>
      <dd>{fetchState.observation.action}</dd>
      <dt>Playlist</dt>
      <dd>{fetchState.observation.playlistName ?? 'unknown'}</dd>
      <dt>Section</dt>
      <dd>{fetchState.observation.section ?? 'unknown'}</dd>
      <dt>Observed at</dt>
      <dd>{formatAbsolute(fetchState.observation.observedAt)}</dd>
      <dt>Received at</dt>
      <dd>{formatAbsolute(fetchState.observation.receivedAt)}</dd>
    </dl>
  )
}

// The observed result of a clear that already succeeded -- rendered from
// what a re-read actually shows, never from the bare fact a DELETE
// returned 2xx (this codebase's ADR-003/ADR-029 posture, applied here to
// a resource read rather than a command outcome).
function ClearedOutcome({ fetchState }: { fetchState: ObservationFetchState }) {
  if (fetchState.kind === 'loading') {
    return <p className="text-muted" role="status">Confirming the stored observation is gone…</p>
  }
  if (fetchState.kind === 'error') {
    return (
      <p role="alert" className="panel panel--error">
        The clear request succeeded, but the stored observation could not be re-read to
        confirm it is gone: {fetchState.message}
      </p>
    )
  }
  if (fetchState.observation === undefined) {
    return <p role="status">Cleared: no stored observation remains for this instance.</p>
  }
  // Should not happen against a correct coordinator (the delete route is
  // documented idempotent and always leaves no row), but if it does, this
  // says so rather than asserting the observation is gone.
  return (
    <p role="alert" className="panel panel--error">
      The clear request succeeded, but a stored observation is still present for this
      instance.
    </p>
  )
}
