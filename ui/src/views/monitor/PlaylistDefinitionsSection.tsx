import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listFPPPlaylistDefinitions } from '../../api'
import { useModelContext } from '../../app/ModelContext'
import { describeApiError } from '../../app/session'
import { formatAbsolute, formatAge } from '../../app/time'
import type { FPPPlaylistDefinitionMetadata, Model } from '../../app/types'
import {
  annotateBound,
  findCaptureDrift,
  findHeldGroups,
  groupDefinitions,
  shortDefinitionHash,
} from './fppPlaylistDefinitionsRollup'

// Monitor / Fleet, "Playlist definitions received" (revision-1 design
// answer to the owner's question "where does the FPP playlist-definitions
// inventory live" -- it is a section on Fleet, not its own destination).
// Previously the whole standalone screen at /config/fpp-playlist-definitions
// (FPPPlaylistDefinitions.tsx, still reachable at that address and left
// untouched). This section reuses the same read-only list call but adds
// the causal story the owner asked for: a definition arriving again with a
// new hash while a show stays bound to the older one is WHY that show's
// bindings are held, and that must read as one connected fact, not two
// unrelated rows (see fppPlaylistDefinitionsRollup.ts for the derivation).
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

export function PlaylistDefinitionsSection() {
  const model = useModelContext()
  const state = useDefinitionsList()

  return (
    <section className="monitor-section" aria-labelledby="monitor-playlist-definitions-title">
      <div className="monitor-section__header">
        <h2 id="monitor-playlist-definitions-title">Playlist definitions received</h2>
        <span className="t-small text-muted">Newest received first</span>
      </div>
      <p className="monitor-section__lede">
        Nothing here is authored. FPP captures a definition and sends it, and the coordinator records what arrived and
        when. Two clocks matter: when FPP captured it, and when this coordinator received it.
      </p>

      {state.kind === 'loading' && <p className="t-small text-muted">Loading definitions…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          The stored definitions could not be read: {state.message}
        </p>
      )}
      {state.kind === 'loaded' && state.definitions.length === 0 && (
        <p className="t-small text-muted">No FPP instance has reported a playlist definition yet.</p>
      )}
      {state.kind === 'loaded' && state.definitions.length > 0 && (
        <PlaylistDefinitionsTable definitions={state.definitions} model={model} />
      )}
    </section>
  )
}

function resolveInstanceLabel(instanceUuid: string, model: Model): { label: string; href: string | null } {
  const fpp = model.fpp.find((f) => f.instanceUuid === instanceUuid)
  if (fpp === undefined) return { label: instanceUuid, href: null }
  return { label: fpp.instanceId, href: `/monitor/fleet/fpp/${encodeURIComponent(fpp.instanceId)}` }
}

function PlaylistDefinitionsTable({
  definitions,
  model,
}: {
  definitions: FPPPlaylistDefinitionMetadata[]
  model: Model
}) {
  const groups = groupDefinitions(definitions)
  const sorted = [...definitions].sort((a, b) => Date.parse(b.receivedAt) - Date.parse(a.receivedAt))
  const held = findHeldGroups(definitions)
  const drift = findCaptureDrift(definitions)
  const instanceCount = new Set(definitions.map((d) => d.instanceUuid)).size

  return (
    <div className="table-wrap">
      <table className="table table--full" aria-label="Playlist definitions received">
        <thead>
          <tr>
            <th scope="col">Playlist</th>
            <th scope="col">Instance</th>
            <th scope="col" style={{ textAlign: 'right' }}>Entries</th>
            <th scope="col">Captured</th>
            <th scope="col">Received</th>
            <th scope="col">Bound</th>
          </tr>
        </thead>
        <tbody>
          {sorted.map((def) => {
            const annotation = annotateBound(def, groups)
            const instance = resolveInstanceLabel(def.instanceUuid, model)
            const held = annotation.caption === 'Newer than the bound one'
            return (
              <tr key={`${def.instanceUuid}/${def.playlistHash}`} className={held ? 'monitor-fpp-defs__row--unbound' : undefined}>
                <td>
                  <Link
                    className="entity-link t-body"
                    style={{ fontWeight: 600 }}
                    to={`/monitor/fleet/playlist-definitions/${encodeURIComponent(def.instanceUuid)}/${encodeURIComponent(def.playlistHash)}`}
                  >
                    {def.playlistName}
                  </Link>
                  <span className="monitor-resource-cell__sub">{shortDefinitionHash(def.playlistHash)}</span>
                </td>
                <td className="t-data" style={{ fontSize: 11, color: 'var(--text-muted)' }}>
                  {instance.href !== null ? (
                    <Link className="entity-link" to={instance.href}>
                      {instance.label}
                    </Link>
                  ) : (
                    instance.label
                  )}
                </td>
                <td className="t-data" style={{ textAlign: 'right', fontVariantNumeric: 'tabular-nums', color: 'var(--text-muted)' }}>
                  {def.entryCount}
                </td>
                <td className="t-data" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{formatAbsolute(def.capturedAt)}</td>
                <td className="t-data" style={{ fontSize: 11, color: 'var(--text-muted)' }}>{formatAbsolute(def.receivedAt)}</td>
                <td>
                  <span className={`monitor-fpp-defs__bound monitor-fpp-defs__bound--${annotation.bound ? 'yes' : 'no'}`}>
                    {annotation.bound ? 'Yes' : 'No'}
                  </span>
                  {annotation.caption !== null && <span className="monitor-resource-cell__sub">{annotation.caption}</span>}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>

      {(held.length > 0 || drift.length > 0) && (
        <div className="monitor-fpp-defs__findings">
          {held.map((finding) => (
            <div key={finding.key} className="monitor-fpp-defs__finding">
              <span className="monitor-fpp-defs__finding-label monitor-fpp-defs__finding-label--warn">Newer definition</span>
              <p>
                <span className="t-data">{finding.playlistName}</span> arrived again at{' '}
                <span className="t-data">{formatAbsolute(finding.arrivedAt)}</span>. {finding.playlistName} is still bound to{' '}
                <span className="t-data">{shortDefinitionHash(finding.boundHash)}</span>, which is why its bindings are held.{' '}
                <Link to="/shows">Reconcile in Shows</Link>.
              </p>
            </div>
          ))}
          {drift.map((finding) => (
            <div key={finding.key} className="monitor-fpp-defs__finding">
              <span className="monitor-fpp-defs__finding-label">Capture drift</span>
              <p>
                <span className="t-data">{finding.playlistName}</span> was captured on{' '}
                <span className="t-data">{formatAbsolute(finding.capturedAt)}</span> and only reached this coordinator on{' '}
                <span className="t-data">{formatAbsolute(finding.receivedAt)}</span>, {formatAge(finding.driftMs).replace(/ ago$/, '')} out of
                date, and nothing binds it now.
              </p>
            </div>
          ))}
        </div>
      )}

      <p className="monitor-table-note">
        {definitions.length} definition{definitions.length === 1 ? '' : 's'} across {instanceCount} instance
        {instanceCount === 1 ? '' : 's'}. Re-import is authoring; this list is only evidence of what FPP sent.
      </p>
    </div>
  )
}
