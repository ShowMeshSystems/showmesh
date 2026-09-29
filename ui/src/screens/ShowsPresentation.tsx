import { useEffect, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { type ShowSurfaceConfigResponse } from '../api'
import { Button, Panes, RuledStrip, Section, SelectableRow, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../domain/session'
import { effectiveServerTimeIso } from '../domain/time'
import { SurfaceEditor } from './SurfaceEditor'
import { fetchShowContents, fetchShowSurfaces } from './showsData'
import { channelSpans, surfaceRenderStatus } from './showsModel'

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; surfaces: ShowSurfaceConfigResponse[] }
  | { kind: 'failed'; reason: string }

function useSurfaces(showId: string): { state: ListState; reload: () => void; upsertSurface: (response: ShowSurfaceConfigResponse) => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    fetchShowContents(showId)
      .then(async (contents) => {
        const surfaces = await fetchShowSurfaces(contents.surfaces)
        if (!cancelled) setState({ kind: 'loaded', surfaces })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId, attempt])

  const upsertSurface = (response: ShowSurfaceConfigResponse) => {
    setState((prev) => {
      if (prev.kind !== 'loaded') return prev
      const exists = prev.surfaces.some((s) => s.id === response.id)
      return { ...prev, surfaces: exists ? prev.surfaces.map((s) => (s.id === response.id ? response : s)) : [...prev.surfaces, response] }
    })
  }

  return { state, reload: () => setAttempt((n) => n + 1), upsertSurface }
}

export function ShowsPresentation() {
  const { id: showId = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const { state, reload, upsertSurface } = useSurfaces(showId)
  const [searchParams, setSearchParams] = useSearchParams()
  const selectedId = searchParams.get('surface')
  const setSelectedId = (surfaceId: string | null) => setSearchParams(surfaceId === null ? {} : { surface: surfaceId })
  const [creating, setCreating] = useState(false)
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())

  if (state.kind === 'loading') {
    return (
      <Section id="ps-list" title="Surfaces">
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this show's surfaces." />
      </Section>
    )
  }

  if (state.kind === 'failed') {
    return (
      <Section id="ps-list" title="Surfaces">
        <RuledStrip
          absence="failed"
          label="Read failed"
          fact={state.reason}
          detail={
            <button type="button" className="sm-linkbutton" onClick={reload}>
              Try again
            </button>
          }
        />
      </Section>
    )
  }

  const { surfaces } = state
  const selected = selectedId === null ? null : surfaces.find((s) => s.id === selectedId) ?? null
  const existingIds = surfaces.map((s) => s.id)
  const { spans, overlapping } = channelSpans(surfaces.map((s) => ({ id: s.id, label: s.payload.name, node: s.payload.node, startChannel: s.payload.channelRange.startChannel, channelCount: s.payload.channelRange.channelCount })))
  const totalClaimed = spans.reduce((sum, span) => sum + (span.end - span.start + 1), 0)

  const closeInspector = () => { setCreating(false); setSelectedId(null) }

  return (
    <Panes inspectorOpen={creating || selected !== null} onInspectorClose={closeInspector} inspectorLabelledBy="ps-surface-editor-heading">
      <div>
        <Section
          id="ps-list"
          title="Surfaces"
          aside={
            <Button
              variant="primary"
              onClick={() => {
                setCreating(true)
                setSelectedId(null)
              }}
            >
              New surface
            </Button>
          }
        >
          <p className="sm-small sm-muted">
            Each surface extracts one channel range from the show&rsquo;s virtual matrix and renders it to one node, over exactly one transport. Select a
            surface to edit it.
          </p>

          {spans.length > 0 && (
            <p className="sm-small sm-muted sm-stack-3">
              {spans.length} {spans.length === 1 ? 'surface claims' : 'surfaces claim'} {totalClaimed.toLocaleString()} channels across{' '}
              {spans.length === 1 ? 'its own range' : 'their ranges'}.{' '}
              {overlapping.size > 0 ? (
                <StatusPair tone="bad" label="Overlap" />
              ) : (
                <StatusPair tone="good" label="No overlaps" />
              )}
            </p>
          )}

          {surfaces.length === 0 ? (
            <RuledStrip absence="empty" label="None" fact="This show has no surface configured." />
          ) : (
            <TableWrap label="Surfaces, scrollable">
              <Table minWidth={540}>
                <thead>
                  <tr>
                    <th scope="col">Surface</th>
                    <th scope="col">Geometry</th>
                    <th scope="col">Output</th>
                    <th scope="col">Rendering</th>
                  </tr>
                </thead>
                <tbody>
                  {surfaces.map((surface) => {
                    const status = surfaceRenderStatus(model.nodes, surface.payload.node, surface.id, nowIso)
                    const overlapped = overlapping.has(surface.id)
                    return (
                      <SelectableRow
                        key={surface.id}
                        selected={selectedId === surface.id}
                        onActivate={() => { setSelectedId(surface.id); setCreating(false) }}
                        ariaLabel={`Edit ${surface.payload.name}`}
                      >
                        <td>
                          <strong>{surface.payload.name}</strong>
                          {selectedId === surface.id && <span className="sm-viewing">Editing</span>}
                          <br />
                          <span className="sm-data sm-small sm-faint">
                            {surface.id} · ch {surface.payload.channelRange.startChannel.toLocaleString()}&ndash;
                            {(surface.payload.channelRange.startChannel + surface.payload.channelRange.channelCount - 1).toLocaleString()}
                          </span>
                          {overlapped && (
                            <>
                              <br />
                              <StatusPair tone="bad" label="Overlaps another surface" />
                            </>
                          )}
                        </td>
                        <td className="sm-data sm-small sm-muted">
                          {surface.payload.geometry.width}&times;{surface.payload.geometry.height} {surface.payload.geometry.pixelFormat}
                        </td>
                        <td>
                          <span className="sm-chip">{surface.payload.output.transport.toUpperCase()}</span>
                        </td>
                        <td>
                          <StatusPair tone={status.tone} label={status.label} />
                        </td>
                      </SelectableRow>
                    )
                  })}
                </tbody>
              </Table>
            </TableWrap>
          )}
          <p className="sm-section__footnote">Rendering is what the node reports, not what this configuration asks for.</p>

          {surfaces
            .map((surface) => ({ surface, status: surfaceRenderStatus(model.nodes, surface.payload.node, surface.id, nowIso) }))
            .filter(({ status }) => status.tone === 'warn' || status.tone === 'bad')
            .map(({ surface, status }) => (
              <div key={surface.id} className="sm-attn">
                <strong className="sm-attn__name">{surface.payload.name}</strong>
                <div>
                  <p className="sm-attn__fact">
                    <StatusPair tone={status.tone} label={status.label} />{' '}
                    {status.detail ?? `${surface.payload.node} reports this surface's pipeline as ${status.label.toLowerCase()}.`}
                  </p>
                  <p className="sm-attn__detail">
                    The configuration is stored and valid; this is only what the node last reported.{' '}
                    <Link to={`/monitor/fleet/node/${surface.payload.node}`}>Open node</Link>
                  </p>
                </div>
              </div>
            ))}
        </Section>
      </div>

      <aside>
        {(creating || selected !== null) && (
          <SurfaceEditor
            key={selected?.id ?? 'new'}
            showId={showId}
            surface={selected}
            existingIds={existingIds}
            model={model}
            onSaved={(response) => {
              upsertSurface(response)
              setSelectedId(response.id)
              setCreating(false)
            }}
            onDeleted={() => {
              reload()
              setCreating(false)
              setSelectedId(null)
            }}
            onCancel={() => {
              setCreating(false)
              setSelectedId(null)
            }}
          />
        )}
      </aside>
    </Panes>
  )
}
