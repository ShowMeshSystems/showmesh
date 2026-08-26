import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listFPPPlaylistDefinitions } from '../api'
import { describeApiError } from '../app/session'
import { formatAbsolute } from '../app/time'
import type { FPPPlaylistDefinitionMetadata } from '../app/types'

// TRACK-H-H2-SPEC.md §3.6/§4: the stored FPP playlist-definition import
// evidence, the remaining half of the show-night verdicts PlaylistReadiness
// already covers. An author binding a show.playlist entry to an FPP
// playlist needs to see what FPP actually reported, including its
// canonical hash, because a changed FPP playlist invalidates a binding rather than
// silently remapping it (readiness's own "definition available" and
// "playlistHash" checks). This is an authoring question, not a show-night
// one, so it lives under Configure rather than beside Playlist readiness:
// an author browses stored imports before or between shows, not in the
// run-up to one. Read-only: `POST /integrations/fpp/playlist-definitions`
// and `POST /integrations/fpp/playlist-entry-observations` are the
// plugin's own machine-facing ingest routes and get no operator control
// here or anywhere in this view.
const READ_ONLY_NOTICE_NONE_REPORTED = 'No FPP instance has reported a playlist definition yet.'

type ListState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; definitions: FPPPlaylistDefinitionMetadata[] }

function useDefinitionsList(): ListState {
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    listFPPPlaylistDefinitions()
      .then((resp) => {
        if (!cancelled) setState({ kind: 'loaded', definitions: resp.definitions })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])

  return state
}

export function FPPPlaylistDefinitions() {
  const state = useDefinitionsList()

  return (
    <div>
      <h2 className="panel__title">FPP playlist definitions</h2>
      <p className="text-muted">
        Every FPP playlist definition the coordinator has ever accepted, newest received first.
        This is the import evidence for authoring a Playlist binding: the canonical hash tells you
        whether what a binding names is still what FPP will actually play. Read-only: nothing here
        imports, binds, or plays anything.
      </p>

      {state.kind === 'loading' && <p className="text-muted">Loading definitions…</p>}

      {/* Never rendered as absence: a failed fetch and "nothing has been
          reported" are different facts, per this view's own rule. */}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          The stored definitions could not be read: {state.message}
        </p>
      )}

      {state.kind === 'loaded' && state.definitions.length === 0 && (
        <p className="text-muted">{READ_ONLY_NOTICE_NONE_REPORTED}</p>
      )}

      {state.kind === 'loaded' && state.definitions.length > 0 && (
        <div className="table-scroll">
          <table className="config-table" aria-label="FPP playlist definitions">
            <thead>
              <tr>
                <th scope="col">Playlist</th>
                <th scope="col">Instance</th>
                <th scope="col">Hash</th>
                <th scope="col">Entries</th>
                <th scope="col">Captured</th>
                <th scope="col">Received</th>
                <th scope="col">Referenced</th>
              </tr>
            </thead>
            <tbody>
              {state.definitions.map((def) => (
                <tr key={`${def.instanceUuid}/${def.playlistHash}`}>
                  <th scope="row">
                    <Link
                      className="entity-link"
                      to={`/config/fpp-playlist-definitions/${encodeURIComponent(def.instanceUuid)}/${encodeURIComponent(
                        def.playlistHash,
                      )}`}
                    >
                      {def.playlistName}
                    </Link>
                  </th>
                  <td>{def.instanceUuid}</td>
                  <td>
                    <code>{def.playlistHash}</code>
                  </td>
                  <td>{def.entryCount}</td>
                  <td>{formatAbsolute(def.capturedAt)}</td>
                  <td>{formatAbsolute(def.receivedAt)}</td>
                  <td>{def.referenced ? 'yes' : 'no'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
