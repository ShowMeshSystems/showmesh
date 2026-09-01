import { useEffect, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import {
  getShowSurface,
  getShowSurfaceRevisions,
  putShowSurface,
  type ConfigShowSurface,
  type ShowSurfaceConfigResponse,
} from '../api'
import { Button, Field, Input, Panes, RevisionHistory, RuledStrip, Section, Segmented, Select, SelectableRow, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso } from '../domain/time'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { fetchShowContents, fetchShowSurfaces } from './showsData'
import { channelsPerPixel, channelSpans, renderCapableNodes, slugify, surfaceRenderStatus } from './showsModel'

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
  const { spans, overlapping } = channelSpans(surfaces.map((s) => ({ id: s.id, label: s.payload.name, startChannel: s.payload.channelRange.startChannel, channelCount: s.payload.channelRange.channelCount })))
  const totalClaimed = spans.reduce((sum, span) => sum + (span.end - span.start + 1), 0)

  return (
    <Panes>
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
              <Table>
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
                <StatusPair tone={status.tone} label={`${surface.payload.name} · ${status.label}`} />
                <div>
                  <p className="sm-attn__fact">
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

type Transport = 'ndi' | 'hdmi'
type PixelFormat = 'rgb' | 'rgbw'

function SurfaceEditor({
  showId,
  surface,
  existingIds,
  model,
  onSaved,
  onCancel,
}: {
  showId: string
  surface: ShowSurfaceConfigResponse | null
  existingIds: readonly string[]
  model: ReturnType<typeof useModelContext>
  onSaved: (response: ShowSurfaceConfigResponse) => void
  onCancel: () => void
}) {
  const isNew = surface === null
  const [name, setName] = useState(surface?.payload.name ?? '')
  const [id, setId] = useState(surface?.id ?? '')
  const [idTouched, setIdTouched] = useState(!isNew)
  const [node, setNode] = useState(surface?.payload.node ?? '')
  const [width, setWidth] = useState(String(surface?.payload.geometry.width ?? 32))
  const [height, setHeight] = useState(String(surface?.payload.geometry.height ?? 32))
  const [pixelFormat, setPixelFormat] = useState<PixelFormat>(surface?.payload.geometry.pixelFormat ?? 'rgb')
  const [startChannel, setStartChannel] = useState(String(surface?.payload.channelRange.startChannel ?? 1))
  const [transport, setTransport] = useState<Transport>(surface?.payload.output.transport ?? 'ndi')
  const [ndiSourceName, setNdiSourceName] = useState(surface?.payload.output.ndi?.sourceName ?? '')
  const [hdmiDisplay, setHdmiDisplay] = useState(surface?.payload.output.hdmi?.display ?? '')
  const [frameRate, setFrameRate] = useState(String(surface?.payload.frameRate ?? 40))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<ShowSurfaceConfigResponse>, { kind: 'stale' }> | null>(null)

  const saveGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const nodeOptions = renderCapableNodes(model.nodes)

  const widthN = Number(width)
  const heightN = Number(height)
  const channelCount = Number.isFinite(widthN) && Number.isFinite(heightN) && widthN > 0 && heightN > 0 ? widthN * heightN * channelsPerPixel(pixelFormat) : 0

  let blockReason: string | null = null
  if (name.trim() === '') blockReason = 'A surface needs a name.'
  else if (id.trim() === '') blockReason = 'A surface needs an id.'
  else if (isNew && existingIds.includes(id)) blockReason = `The id "${id}" already names another surface in this show; edit that surface instead or choose a different id.`
  else if (node.trim() === '') blockReason = 'A surface needs a node.'
  else if (!Number.isInteger(widthN) || widthN < 1) blockReason = 'Width must be a whole number, at least 1.'
  else if (!Number.isInteger(heightN) || heightN < 1) blockReason = 'Height must be a whole number, at least 1.'
  else if (!Number.isInteger(Number(startChannel)) || Number(startChannel) < 1) blockReason = 'Start channel must be a whole number, at least 1.'
  else if (transport === 'ndi' && ndiSourceName.trim() === '') blockReason = 'NDI output needs a source name.'
  else if (transport === 'hdmi' && hdmiDisplay.trim() === '') blockReason = 'HDMI output needs a display identifier.'
  else if (!Number.isInteger(Number(frameRate)) || Number(frameRate) < 1 || Number(frameRate) > 120) blockReason = 'Frame rate must be a whole number, 1 to 120.'

  const discard = () => {
    setName(surface?.payload.name ?? '')
    setNode(surface?.payload.node ?? '')
    setWidth(String(surface?.payload.geometry.width ?? 32))
    setHeight(String(surface?.payload.geometry.height ?? 32))
    setPixelFormat(surface?.payload.geometry.pixelFormat ?? 'rgb')
    setStartChannel(String(surface?.payload.channelRange.startChannel ?? 1))
    setTransport(surface?.payload.output.transport ?? 'ndi')
    setNdiSourceName(surface?.payload.output.ndi?.sourceName ?? '')
    setHdmiDisplay(surface?.payload.output.hdmi?.display ?? '')
    setFrameRate(String(surface?.payload.frameRate ?? 40))
    setSaveError(null)
  }

  const save = () => {
    if (blockReason !== null) return
    const payload: ConfigShowSurface = {
      show: showId,
      name: name.trim(),
      node: node.trim(),
      channelRange: { startChannel: Number(startChannel), channelCount },
      geometry: { width: widthN, height: heightN, pixelFormat },
      frameRate: Number(frameRate),
      output:
        transport === 'ndi'
          ? { transport: 'ndi', ndi: { sourceName: ndiSourceName.trim() } }
          : { transport: 'hdmi', hdmi: { display: hdmiDisplay.trim() } },
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    if (surface === null) {
      guardedCreate({ read: () => getShowSurface(id.trim()), write: () => putShowSurface(id.trim(), payload) })
        .then((outcome) => {
          if (outcome.kind === 'created') {
            onSaved(outcome.response)
            return
          }
          setSaveError(
            outcome.kind === 'taken'
              ? `${id.trim()} already names a surface in this show. Creating it here would write over that one.`
              : outcome.reason,
          )
        })
        .catch((err: unknown) => setSaveError(describeApiError(err)))
        .finally(() => setSaving(false))
      return
    }
    guardedSave({
      loaded: surface,
      read: () => getShowSurface(surface.id),
      write: () => putShowSurface(surface.id, payload),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          onSaved(outcome.response)
          return
        }
        if (outcome.kind === 'stale') {
          setStale(outcome)
          return
        }
        setSaveError(outcome.reason)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow sm-eyebrow--accent">{isNew ? 'New surface' : `Editing ${surface.payload.name}`}</p>

      <div className="sm-inspector__group">
        <Field label="Name">
          {(props) => (
            <Input
              {...props}
              value={name}
              onChange={(e) => {
                setName(e.target.value)
                if (!idTouched) setId(slugify(e.target.value))
              }}
            />
          )}
        </Field>
        {isNew && (
          <Field label="Id" help="From the name, editable.">
            {(props) => (
              <Input
                {...props}
                className="sm-data"
                value={id}
                onChange={(e) => {
                  setId(e.target.value)
                  setIdTouched(true)
                }}
              />
            )}
          </Field>
        )}
        <Field label="Node" help="Declared nodes advertising a render capability. The coordinator does not check that this node is currently online.">
          {(props) =>
            nodeOptions.length === 0 ? (
              <RuledStrip absence="empty" label="None" fact="No declared node advertises a render capability yet." />
            ) : (
              <Select {...props} value={node} onChange={(e) => setNode(e.target.value)}>
                <option value="">Choose a node…</option>
                {nodeOptions.map((n) => (
                  <option key={n.nodeId} value={n.nodeId}>
                    {n.label ?? n.nodeId}
                  </option>
                ))}
                {node !== '' && !nodeOptions.some((n) => n.nodeId === node) && <option value={node}>{node}</option>}
              </Select>
            )
          }
        </Field>
      </div>

      <div className="sm-inspector__group">
        <h3 className="sm-subsection__title">Geometry</h3>
        <div className="sm-grid sm-grid--auto sm-stack-3">
          <Field label="Width">{(props) => <Input {...props} className="sm-data" value={width} onChange={(e) => setWidth(e.target.value)} />}</Field>
          <Field label="Height">{(props) => <Input {...props} className="sm-data" value={height} onChange={(e) => setHeight(e.target.value)} />}</Field>
        </div>
        <Segmented
          label="Pixel format"
          value={pixelFormat}
          onChange={setPixelFormat}
          options={[
            { value: 'rgb', label: 'rgb · 3 ch' },
            { value: 'rgbw', label: 'rgbw · 4 ch' },
          ]}
        />
      </div>

      <div className="sm-inspector__group">
        <h3 className="sm-subsection__title">Channel range</h3>
        <div className="sm-grid sm-grid--auto sm-stack-3">
          <Field label="Start channel">
            {(props) => <Input {...props} className="sm-data" value={startChannel} onChange={(e) => setStartChannel(e.target.value)} />}
          </Field>
          <Field label="Channel count" help="Derived from geometry; the coordinator requires it to equal width × height × channels-per-pixel exactly.">
            {(props) => <Input {...props} className="sm-data" value={channelCount.toLocaleString()} disabled readOnly />}
          </Field>
        </div>
      </div>

      <div className="sm-inspector__group">
        <h3 className="sm-subsection__title">Output</h3>
        <p className="sm-small sm-muted">Exactly one transport. NDI support is never evidence HDMI works on the same node.</p>
        <Segmented
          label="Transport"
          value={transport}
          onChange={setTransport}
          options={[
            { value: 'ndi', label: 'NDI' },
            { value: 'hdmi', label: 'HDMI' },
          ]}
        />
        {transport === 'ndi' ? (
          <Field label="NDI source name">{(props) => <Input {...props} value={ndiSourceName} onChange={(e) => setNdiSourceName(e.target.value)} />}</Field>
        ) : (
          <Field
            label="HDMI display"
            help="This node's display.hdmi capability reports an outputs count, not display names, so this is typed by hand."
          >
            {(props) => <Input {...props} value={hdmiDisplay} onChange={(e) => setHdmiDisplay(e.target.value)} />}
          </Field>
        )}
      </div>

      <div className="sm-inspector__group">
        <Field label="Frame rate" help="1–120. A target profile is unvalidated design intent, not a supported guarantee.">
          {(props) => <Input {...props} className="sm-data" value={frameRate} onChange={(e) => setFrameRate(e.target.value)} />}
        </Field>
      </div>

      <div className="sm-inspector__actions">
        <span className="sm-small sm-muted">{isNew ? 'Creates revision 1' : `Active revision ${surface?.revision}`}</span>
        <div className="sm-btn-row">
          {isNew ? (
            <Button variant="quiet" onClick={onCancel} disabled={saving}>
              Cancel
            </Button>
          ) : (
            <Button variant="quiet" onClick={discard} disabled={saving}>
              Discard changes
            </Button>
          )}
          <Button
            variant="primary"
            onClick={save}
            disabled={saving || !saveGate.allowed || blockReason !== null}
            title={!saveGate.allowed ? saveGate.reason : (blockReason ?? undefined)}
          >
            {saving ? 'Saving…' : isNew ? 'Create surface' : 'Save surface'}
          </Button>
        </div>
      </div>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            if (surface !== null) getShowSurface(surface.id).then(onSaved).catch((err: unknown) => setSaveError(describeApiError(err)))
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
      {surface !== null && <RevisionHistory fetch={() => getShowSurfaceRevisions(surface.id)} reloadKey={`${surface.id}:${surface.revision}`} />}
    </div>
  )
}
