import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { ApiError, getFPPPlaylistDefinition, getFPPPlaylistDefinitionEntries } from '../api'
import { describeApiError } from '../app/session'
import { formatAbsolute } from '../app/time'
import type { FPPPlaylistDefinitionEntry, FPPPlaylistDefinitionResponse } from '../app/types'

// TRACK-H-H2-SPEC.md §3.6/§4: one stored FPP playlist definition and its
// parsed entries, in the same fixed order the coordinator reads them
// (leadIn, then mainPlaylist, then leadOut, each section positioned from
// zero independently), the same order the entries route itself returns
// them in, so this view never re-sorts what the coordinator already
// ordered.

type DefinitionState =
  | { kind: 'loading' }
  | { kind: 'not-found'; detail: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; response: FPPPlaylistDefinitionResponse }

function useDefinition(instanceUuid: string, playlistHash: string): DefinitionState {
  const [state, setState] = useState<DefinitionState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getFPPPlaylistDefinition(instanceUuid, playlistHash)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not-found', detail: err.message })
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [instanceUuid, playlistHash])

  return state
}

type EntriesState =
  | { kind: 'loading' }
  | { kind: 'not-found'; detail: string }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; entries: FPPPlaylistDefinitionEntry[] }

function useDefinitionEntries(instanceUuid: string, playlistHash: string): EntriesState {
  const [state, setState] = useState<EntriesState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getFPPPlaylistDefinitionEntries(instanceUuid, playlistHash)
      .then((resp) => {
        if (!cancelled) setState({ kind: 'loaded', entries: resp.entries })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not-found', detail: err.message })
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [instanceUuid, playlistHash])

  return state
}

export function FPPPlaylistDefinitionDetail() {
  const params = useParams<{ instanceUuid: string; playlistHash: string }>()
  const instanceUuid = params.instanceUuid ?? ''
  const playlistHash = params.playlistHash ?? ''

  const definition = useDefinition(instanceUuid, playlistHash)
  const entries = useDefinitionEntries(instanceUuid, playlistHash)

  return (
    <div>
      <p>
        <Link className="entity-link" to="/monitor/fleet">
          Back to Monitor, Fleet
        </Link>
      </p>
      <h2 className="panel__title">FPP playlist definition</h2>

      {definition.kind === 'loading' && <p className="text-muted">Loading definition…</p>}
      {definition.kind === 'not-found' && (
        <p className="text-muted">No stored definition for this instance and hash. {definition.detail}</p>
      )}
      {definition.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          The definition could not be read: {definition.message}
        </p>
      )}
      {definition.kind === 'loaded' && (
        <dl className="field-list">
          <div>
            <dt>Playlist</dt>
            <dd>{definition.response.playlistName}</dd>
          </div>
          <div>
            <dt>Instance</dt>
            <dd>{definition.response.instanceUuid}</dd>
          </div>
          <div>
            <dt>Hash</dt>
            <dd>
              <code>{definition.response.playlistHash}</code>
            </dd>
          </div>
          <div>
            <dt>Captured</dt>
            <dd>{formatAbsolute(definition.response.capturedAt)}</dd>
          </div>
          <div>
            <dt>Received</dt>
            <dd>{formatAbsolute(definition.response.receivedAt)}</dd>
          </div>
        </dl>
      )}

      <h3 className="section-title">Entries</h3>
      {entries.kind === 'loading' && <p className="text-muted">Loading entries…</p>}
      {entries.kind === 'not-found' && (
        <p className="text-muted">No stored definition for this instance and hash. {entries.detail}</p>
      )}
      {entries.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          The entries could not be read: {entries.message}
        </p>
      )}
      {entries.kind === 'loaded' && entries.entries.length === 0 && (
        <p className="text-muted">This definition has no entries in any section.</p>
      )}
      {entries.kind === 'loaded' && entries.entries.length > 0 && (
        <div className="table-scroll">
          <table className="config-table" aria-label="Playlist definition entries">
            <thead>
              <tr>
                <th scope="col">Section</th>
                <th scope="col">Position</th>
                <th scope="col">Type</th>
                <th scope="col">Sequence</th>
                <th scope="col">Media</th>
              </tr>
            </thead>
            <tbody>
              {entries.entries.map((entry, index) => (
                <tr key={`${entry.section}-${entry.position}-${index}`}>
                  <th scope="row">{entry.section}</th>
                  <td>{entry.position}</td>
                  <td>{entry.type || '-'}</td>
                  <td>{entry.sequenceName || '-'}</td>
                  <td>{entry.mediaName || '-'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
