import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowSurface, getShowSurfaceRevisions, putShowSurface, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { ShowSelect } from '../components/ShowSelect'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showSurfacePath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type { ConfigShowSurface, ShowSurfaceConfigResponse, SurfacePixelFormat, SurfaceTransport } from '../app/types'

// Show Presentation.dc.html's inspector. Two derivations kept: channel
// count is SHOWN as `32 x 32 x 4 = 4,096` rather than typed (the
// coordinator requires the count to equal geometry exactly, so it is
// derived here rather than asked for and risking disagreement), and the
// HDMI display field is typed by hand because `display.hdmi`
// (pkg/capability/id.go) advertises an outputs COUNT, not display
// names - there is no list to pick from.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

const PIXEL_FORMATS: SurfacePixelFormat[] = ['rgb', 'rgbw']
const TRANSPORTS: SurfaceTransport[] = ['ndi', 'hdmi']

function channelsPerPixel(format: SurfacePixelFormat | ''): number {
  return format === 'rgbw' ? 4 : format === 'rgb' ? 3 : 0
}

interface FormState {
  show: string
  name: string
  node: string
  startChannel: string
  width: string
  height: string
  pixelFormat: SurfacePixelFormat | ''
  frameRate: string
  transport: SurfaceTransport | ''
  ndiSourceName: string
  hdmiDisplay: string
}

function emptyForm(show: string): FormState {
  return {
    show,
    name: '',
    node: '',
    startChannel: '1',
    width: '',
    height: '',
    pixelFormat: '',
    frameRate: '30',
    transport: '',
    ndiSourceName: '',
    hdmiDisplay: '',
  }
}

function formFromPayload(payload: ConfigShowSurface): FormState {
  return {
    show: payload.show,
    name: payload.name,
    node: payload.node,
    startChannel: String(payload.channelRange.startChannel),
    width: String(payload.geometry.width),
    height: String(payload.geometry.height),
    pixelFormat: payload.geometry.pixelFormat,
    frameRate: String(payload.frameRate),
    transport: payload.output.transport,
    ndiSourceName: payload.output.ndi?.sourceName ?? '',
    hdmiDisplay: payload.output.hdmi?.display ?? '',
  }
}

function buildPayload(form: FormState): { payload: ConfigShowSurface } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.name.trim() === '') return { error: 'Name is required.' }
  if (form.node.trim() === '') return { error: 'Node is required.' }

  const startChannel = Number(form.startChannel)
  if (!Number.isInteger(startChannel) || startChannel < 1) return { error: 'Start channel must be a whole number of at least 1.' }
  const width = Number(form.width)
  if (!Number.isInteger(width) || width < 1) return { error: 'Width must be a whole number of at least 1.' }
  const height = Number(form.height)
  if (!Number.isInteger(height) || height < 1) return { error: 'Height must be a whole number of at least 1.' }
  if (form.pixelFormat === '') return { error: 'Pixel format is required and has no default.' }
  const frameRate = Number(form.frameRate)
  if (!Number.isInteger(frameRate) || frameRate < 1 || frameRate > 120) return { error: 'Frame rate must be a whole number from 1 to 120.' }
  if (form.transport === '') return { error: 'Transport is required and has no default.' }
  if (form.transport === 'ndi' && form.ndiSourceName.trim() === '') return { error: 'NDI source name is required when transport is NDI.' }
  if (form.transport === 'hdmi' && form.hdmiDisplay.trim() === '') return { error: 'Display is required when transport is HDMI.' }

  const channelCount = width * height * channelsPerPixel(form.pixelFormat)

  return {
    payload: {
      show: form.show.trim(),
      name: form.name.trim(),
      node: form.node.trim(),
      channelRange: { startChannel, channelCount },
      geometry: { width, height, pixelFormat: form.pixelFormat },
      frameRate,
      output:
        form.transport === 'ndi'
          ? { transport: 'ndi', ndi: { sourceName: form.ndiSourceName.trim() } }
          : { transport: 'hdmi', hdmi: { display: form.hdmiDisplay.trim() } },
    },
  }
}

export interface ShowSurfaceDetailProps {
  isNew?: boolean
}

export function ShowSurfaceDetail({ isNew = false }: ShowSurfaceDetailProps) {
  // App.tsx's route is `shows/:showId/presentation/:id` (matching
  // showSurfacePath's own address), so the object id param is `id`, not
  // `surfaceId`.
  const params = useParams<{ showId: string; id: string }>()
  const showId = params.showId ?? ''
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id
  const workspaceData = useShowWorkspaceData(showId)

  type LoadState =
    | { kind: 'new' }
    | { kind: 'loading' }
    | { kind: 'error'; message: string }
    | { kind: 'loaded'; config: ShowSurfaceConfigResponse; revisions: ConfigRevisionMeta[] }

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm(showId))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)

  useEffect(() => {
    if (isNew) setForm((f) => ({ ...f, show: showId }))
  }, [isNew, showId])

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getShowSurface(existingId), getShowSurfaceRevisions(existingId)])
      .then(([config, revisionsResp]) => {
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setForm(formFromPayload(config.payload))
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [existingId, readGate.allowed, isNew])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A surface id is required.')
      return
    }
    const built = buildPayload(form)
    if ('error' in built) {
      setSaveError(built.error)
      return
    }
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      const resp = await putShowSurface(id, built.payload)
      if (isNew) {
        navigate(showSurfacePath(showId, id))
        return
      }
      if (built.payload.show !== showId) {
        // Saved into a different show than the route names: this URL's
        // :showId no longer matches the surface's own show, so land on
        // the object at its new show-scoped address rather than leaving
        // the operator on a stale one.
        navigate(showSurfacePath(built.payload.show, id))
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      const revisionsResp = await getShowSurfaceRevisions(id)
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, revisions: revisionsResp.revisions } : prev))
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  const pageGate = isNew ? writeGate : readGate
  const editable = writeGate.allowed
  const cpp = channelsPerPixel(form.pixelFormat)
  const derivedChannelCount = Number(form.width || 0) * Number(form.height || 0) * cpp

  const content = (() => {
    if (!pageGate.allowed) {
      return (
        <p className="ruled-strip ruled-strip--no-permission" role="status">
          <span className="ruled-strip__state t-meta">No permission</span>
          <span className="ruled-strip__explanation">{pageGate.reason}</span>
        </p>
      )
    }
    if (!isNew && state.kind === 'loading') {
      return (
        <p className="ruled-strip ruled-strip--loading" role="status">
          <span className="ruled-strip__state t-meta">Loading</span>
          <span className="ruled-strip__explanation">Reading this surface.</span>
        </p>
      )
    }
    if (!isNew && state.kind === 'error') {
      return (
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">{state.message}</span>
        </p>
      )
    }

    return (
      <div className="card" style={{ maxWidth: 480 }}>
        <div style={{ padding: 14, borderBottom: '1px solid var(--border)', background: 'var(--raised)' }}>
          <p className="t-meta shows-faint">Surface</p>
          <h2 className="t-heading">{isNew ? 'New surface' : form.name || existingId}</h2>
        </div>

        {!editable && (
          <p className="t-small shows-muted" role="status" style={{ padding: '0 14px' }}>
            Viewing only: editing requires the <code>config:write</code> scope.
          </p>
        )}

        <fieldset disabled={!editable} style={{ border: 0, padding: 14, margin: 0, display: 'grid', gap: 14 }}>
          {isNew && (
            <label className="field">
              <span className="field__label">Surface id</span>
              <input className="field__input" type="text" value={newId} onChange={(e) => setNewId(e.target.value)} />
            </label>
          )}
          <label className="field">
            <span className="field__label">Name</span>
            <input className="field__input" type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </label>
          <label className="field">
            <span className="field__label">Node (must be a declared node)</span>
            <input className="field__input" type="text" value={form.node} onChange={(e) => setForm({ ...form, node: e.target.value })} />
          </label>

          <section aria-labelledby="sp-geom">
            <h3 id="sp-geom" className="t-meta shows-faint">
              Geometry
            </h3>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginTop: 12 }}>
              <label className="field">
                <span className="field__label">Width</span>
                <input className="field__input field__input--data" type="number" min={1} value={form.width} onChange={(e) => setForm({ ...form, width: e.target.value })} />
              </label>
              <label className="field">
                <span className="field__label">Height</span>
                <input className="field__input field__input--data" type="number" min={1} value={form.height} onChange={(e) => setForm({ ...form, height: e.target.value })} />
              </label>
            </div>
            <div style={{ marginTop: 12 }}>
              <span className="t-small shows-muted">Pixel format</span>
              <div className="segmented" role="group" aria-label="Pixel format" style={{ marginTop: 6 }}>
                {PIXEL_FORMATS.map((f) => (
                  <button
                    key={f}
                    type="button"
                    className="segmented__option"
                    aria-pressed={form.pixelFormat === f}
                    disabled={!editable}
                    onClick={() => setForm({ ...form, pixelFormat: f })}
                  >
                    {f} &middot; {channelsPerPixel(f)} ch
                  </button>
                ))}
              </div>
            </div>
          </section>

          <section aria-labelledby="sp-chan">
            <h3 id="sp-chan" className="t-meta shows-faint">
              Channel range
            </h3>
            <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginTop: 12 }}>
              <label className="field">
                <span className="field__label">Start channel</span>
                <input
                  className="field__input field__input--data"
                  type="number"
                  min={1}
                  value={form.startChannel}
                  onChange={(e) => setForm({ ...form, startChannel: e.target.value })}
                />
              </label>
              <div className="field">
                <span className="field__label">Channel count</span>
                <p className="derived-value t-data">{derivedChannelCount.toLocaleString()}</p>
              </div>
            </div>
            <p className="derived-note">
              <span className="t-data" style={{ color: 'var(--text)' }}>
                {form.width || '0'} &times; {form.height || '0'} &times; {cpp} = {derivedChannelCount.toLocaleString()}
              </span>{' '}
              The coordinator requires the count to equal the geometry exactly, so it is
              derived here rather than typed.
            </p>
          </section>

          <section aria-labelledby="sp-out">
            <h3 id="sp-out" className="t-meta shows-faint">
              Output
            </h3>
            <p className="t-small shows-muted">Exactly one transport. NDI support is never evidence HDMI works on the same node.</p>
            <div className="segmented" role="group" aria-label="Transport" style={{ marginTop: 12 }}>
              {TRANSPORTS.map((t) => (
                <button
                  key={t}
                  type="button"
                  className="segmented__option"
                  aria-pressed={form.transport === t}
                  disabled={!editable}
                  onClick={() => setForm({ ...form, transport: t })}
                >
                  {t.toUpperCase()}
                </button>
              ))}
            </div>
            {form.transport === 'ndi' && (
              <label className="field" style={{ marginTop: 12 }}>
                <span className="field__label">NDI source name</span>
                <input className="field__input" type="text" value={form.ndiSourceName} onChange={(e) => setForm({ ...form, ndiSourceName: e.target.value })} />
              </label>
            )}
            {form.transport === 'hdmi' && (
              <div className="unobserved-note">
                <p className="t-meta shows-faint">Displays unobserved</p>
                <p className="t-small shows-muted">
                  A node&rsquo;s <code>display.hdmi</code> capability advertises an outputs count,
                  not display names, so there is no list to choose from. Enter the identifier by
                  hand.
                </p>
                <label className="field" style={{ marginTop: 10 }}>
                  <span className="field__label">Display</span>
                  <input className="field__input field__input--data" type="text" value={form.hdmiDisplay} onChange={(e) => setForm({ ...form, hdmiDisplay: e.target.value })} />
                </label>
              </div>
            )}
          </section>

          <label className="field" style={{ maxWidth: 160 }}>
            <span className="field__label">Frame rate</span>
            <input
              className="field__input field__input--data"
              type="number"
              min={1}
              max={120}
              value={form.frameRate}
              onChange={(e) => setForm({ ...form, frameRate: e.target.value })}
            />
            <span className="field__help">1&ndash;120. The 40 fps NDI target is unvalidated design intent, not a supported profile.</span>
          </label>

          {!isNew && (
            <section aria-labelledby="sp-move" style={{ borderTop: '1px solid var(--border)', paddingTop: 14 }}>
              <h3 id="sp-move" className="t-meta shows-faint">
                Move to another show
              </h3>
              <ShowSelect ariaLabel="Move to another show" value={form.show} onChange={(show) => setForm({ ...form, show })} />
              <p className="field__help">
                Moving this surface out of {showId} removes it from render readiness for playlists
                there: any cue that renders on this node will no longer find a surface assignment,
                until a new one is added in {showId}.
              </p>
            </section>
          )}
        </fieldset>

        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, padding: '12px 14px', background: 'var(--raised)' }}>
          <span className="t-small shows-muted">
            {isNew ? 'Creates revision 1' : !isNew && state.kind === 'loaded' ? `Rev ${state.config.revision}` : ''}
          </span>
          {editable && (
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              className="btn btn--primary"
              onClick={() => void handleSave()}
              busy={saving}
              busyReason="Saving this surface revision…"
            >
              {saving ? 'Saving…' : isNew ? 'Create surface' : 'Save surface'}
            </ScopedButton>
          )}
        </div>
        {saveError !== null && (
          <p role="alert" className="field__error" style={{ padding: '0 14px 12px' }}>
            {saveError}
          </p>
        )}

        {!isNew && state.kind === 'loaded' && state.revisions.length > 0 && (
          <div style={{ padding: 14, borderTop: '1px solid var(--border)' }}>
            <h3 className="t-subhead">Revision history</h3>
            <div className="table-wrap">
              <table className="table" aria-label="Revision history">
                <thead>
                  <tr>
                    <th scope="col">Revision</th>
                    <th scope="col">Active</th>
                    <th scope="col">Created at</th>
                    <th scope="col">Created by</th>
                  </tr>
                </thead>
                <tbody>
                  {state.revisions.map((rev) => (
                    <tr key={rev.revision}>
                      <td className="t-data">{rev.revision}</td>
                      <td>{rev.active ? 'active' : ''}</td>
                      <td className="t-small">{formatAbsolute(rev.createdAt)}</td>
                      <td className="t-small">{rev.createdByPrincipalName ?? '-'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          </div>
        )}
      </div>
    )
  })()

  return (
    <ShowWorkspaceFrame showId={showId} active="presentation" data={workspaceData}>
      {content}
    </ShowWorkspaceFrame>
  )
}
