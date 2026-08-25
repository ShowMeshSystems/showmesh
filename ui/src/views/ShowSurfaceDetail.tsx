import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import {
  getShowSurface,
  getShowSurfaceRevisions,
  putShowSurface,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import type {
  ConfigShowSurface,
  ShowSurfaceConfigResponse,
  SurfacePixelFormat,
  SurfaceTransport,
} from '../app/types'

// Track G seam G-8 (TRACK-G-surface-parity.md "G-8"): surface authoring.
// Every field on ConfigShowSurface is required on every write (no
// optional/defaulted key), so this is a FULL-REPLACEMENT form like
// MacroDetail.tsx and ShowActionDetail.tsx, never a partial-patch one.
// The manual channelRange fields below are rendered plainly, at the same
// level as every other field — ADR-027 decision 4 makes manual channel
// ranges a PERMANENT first-class path, not a fallback hidden behind an
// "advanced" affordance, and named-xLights-model selection does not exist
// as a path yet (FPP Connect compatibility has not landed), so manual is
// simply the only way to set this today.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

const PIXEL_FORMATS: SurfacePixelFormat[] = ['rgb', 'rgbw']
const TRANSPORTS: SurfaceTransport[] = ['ndi', 'hdmi']

interface FormState {
  show: string
  name: string
  node: string
  startChannel: string
  channelCount: string
  width: string
  height: string
  pixelFormat: SurfacePixelFormat | ''
  frameRate: string
  transport: SurfaceTransport | ''
  ndiSourceName: string
  hdmiDisplay: string
}

function emptyForm(): FormState {
  return {
    show: '',
    name: '',
    node: '',
    startChannel: '',
    channelCount: '',
    width: '',
    height: '',
    pixelFormat: '',
    frameRate: '',
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
    channelCount: String(payload.channelRange.channelCount),
    width: String(payload.geometry.width),
    height: String(payload.geometry.height),
    pixelFormat: payload.geometry.pixelFormat,
    frameRate: String(payload.frameRate),
    transport: payload.output.transport,
    ndiSourceName: payload.output.ndi?.sourceName ?? '',
    hdmiDisplay: payload.output.hdmi?.display ?? '',
  }
}

/**
 * Mirrors, not enforces (ADR-030): every check here also exists
 * server-side, and every distinct server refusal (an absent channelRange,
 * an explicit null, and an explicitly empty one are three DIFFERENT
 * refusals per api/openapi.yaml's own description of PUT
 * /config/show.surface/{id}) is a case this form structurally cannot
 * produce, because every field below is always sent with a real,
 * non-empty value or the save is refused client-side first.
 */
function buildPayload(form: FormState): { payload: ConfigShowSurface } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.name.trim() === '') return { error: 'Name is required.' }
  if (form.node.trim() === '') return { error: 'Node is required.' }

  const startChannel = Number(form.startChannel)
  if (!Number.isInteger(startChannel) || startChannel < 1) {
    return { error: 'Start channel must be a whole number of at least 1.' }
  }
  const channelCount = Number(form.channelCount)
  if (!Number.isInteger(channelCount) || channelCount < 1) {
    return { error: 'Channel count must be a whole number of at least 1; an empty range is refused server-side.' }
  }
  const width = Number(form.width)
  if (!Number.isInteger(width) || width < 1) return { error: 'Width must be a whole number of at least 1.' }
  const height = Number(form.height)
  if (!Number.isInteger(height) || height < 1) return { error: 'Height must be a whole number of at least 1.' }
  if (form.pixelFormat === '') return { error: 'Pixel format is required and has no default.' }
  const frameRate = Number(form.frameRate)
  if (!Number.isInteger(frameRate) || frameRate < 1 || frameRate > 120) {
    return { error: 'Frame rate must be a whole number from 1 to 120.' }
  }
  if (form.transport === '') return { error: 'Transport is required and has no default.' }
  if (form.transport === 'ndi' && form.ndiSourceName.trim() === '') {
    return { error: 'NDI source name is required when transport is NDI.' }
  }
  if (form.transport === 'hdmi' && form.hdmiDisplay.trim() === '') {
    return { error: 'Display is required when transport is HDMI.' }
  }

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

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowSurfaceConfigResponse; revisions: ConfigRevisionMeta[] }

export interface ShowSurfaceDetailProps {
  isNew?: boolean
}

export function ShowSurfaceDetail({ isNew = false }: ShowSurfaceDetailProps) {
  const params = useParams<{ id: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm())
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)

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
        navigate(`/config/show.surface/${encodeURIComponent(id)}`)
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
  if (!pageGate.allowed) {
    return (
      <div>
        <h2 className="panel__title">{isNew ? 'New surface' : 'Surface'}</h2>
        <p className="panel panel--error" role="status">
          {pageGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading surface…</p>
  }
  if (!isNew && state.kind === 'error') {
    return (
      <p className="panel panel--error" role="alert">
        {state.message}
      </p>
    )
  }

  const editable = writeGate.allowed

  return (
    <div>
      <h2 className="panel__title">{isNew ? 'New surface' : form.name || existingId}</h2>

      {!editable && (
        <p className="text-muted" role="status">
          Viewing only: editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="form-field">
          Surface id
          <input type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable}>
        <label className="form-field">
          Show
          <input type="text" value={form.show} onChange={(e) => setForm({ ...form, show: e.target.value })} />
        </label>
        <label className="form-field">
          Name
          <input type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        </label>
        <label className="form-field">
          Node (must be a declared node)
          <input type="text" value={form.node} onChange={(e) => setForm({ ...form, node: e.target.value })} />
        </label>

        <h3 className="panel__title">Channel range (manual, a permanent path, not a fallback)</h3>
        {/* Manual channel ranges are a permanent first-class path (ADR-027 decision 4), not an
            escape hatch — named xLights-model selection has no path here until FPP Connect
            compatibility lands. */}
        <p className="text-muted">
          The channel range this surface extracts from the show&rsquo;s virtual matrix. Named
          xLights-model selection is not offered here: it has no path in this deployment yet, so
          manual entry is simply how every surface is configured today.
        </p>
        <label className="form-field">
          Start channel
          <input
            type="number"
            min={1}
            value={form.startChannel}
            onChange={(e) => setForm({ ...form, startChannel: e.target.value })}
          />
        </label>
        <label className="form-field">
          Channel count
          <input
            type="number"
            min={1}
            value={form.channelCount}
            onChange={(e) => setForm({ ...form, channelCount: e.target.value })}
          />
        </label>

        <h3 className="panel__title">Geometry</h3>
        <p className="text-muted">
          width × height × channels-per-pixel(pixel format) must equal the channel count above
          exactly; enforced server-side.
        </p>
        <label className="form-field">
          Width (pixels)
          <input type="number" min={1} value={form.width} onChange={(e) => setForm({ ...form, width: e.target.value })} />
        </label>
        <label className="form-field">
          Height (pixels)
          <input type="number" min={1} value={form.height} onChange={(e) => setForm({ ...form, height: e.target.value })} />
        </label>
        <label className="form-field">
          Pixel format
          <select
            value={form.pixelFormat}
            onChange={(e) => setForm({ ...form, pixelFormat: e.target.value as SurfacePixelFormat })}
          >
            <option value="" disabled>
              Choose one, never defaulted
            </option>
            {PIXEL_FORMATS.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
        </label>
        <label className="form-field">
          Frame rate (fps, 1&ndash;120)
          <input
            type="number"
            min={1}
            max={120}
            value={form.frameRate}
            onChange={(e) => setForm({ ...form, frameRate: e.target.value })}
          />
        </label>
        {/* The day-0 profile of 40 fps over NDI on OptiPlex 7040-class hardware (ADR-026) is L0
            design intent — a target to validate, not a supported profile. */}
        <p className="text-muted">
          The day-0 target of 40 fps over NDI is a value to validate, not a guaranteed profile;
          treat any number here as a starting point, not a promise.
        </p>

        <h3 className="panel__title">Output</h3>
        <p className="text-muted">
          Exactly one transport. Support for NDI is never evidence HDMI also works on this node,
          and vice versa; nothing here defaults or infers one from the other.
        </p>
        <label className="form-field">
          Transport
          <select
            value={form.transport}
            onChange={(e) => setForm({ ...form, transport: e.target.value as SurfaceTransport })}
          >
            <option value="" disabled>
              Choose one, never defaulted
            </option>
            {TRANSPORTS.map((t) => (
              <option key={t} value={t}>
                {t.toUpperCase()}
              </option>
            ))}
          </select>
        </label>
        {form.transport === 'ndi' && (
          <label className="form-field">
            NDI source name
            <input
              type="text"
              value={form.ndiSourceName}
              onChange={(e) => setForm({ ...form, ndiSourceName: e.target.value })}
            />
          </label>
        )}
        {form.transport === 'hdmi' && (
          <label className="form-field">
            Display
            <input
              type="text"
              value={form.hdmiDisplay}
              onChange={(e) => setForm({ ...form, hdmiDisplay: e.target.value })}
            />
          </label>
        )}
      </fieldset>

      {editable && (
        <div style={{ marginTop: '1rem' }}>
          {saveError !== null && (
            <p role="alert" className="session-form__error">
              {saveError}
            </p>
          )}
          <ScopedButton
            requiredScope={CONFIG_WRITE_SCOPE}
            onClick={() => void handleSave()}
            busy={saving}
            busyReason="Saving this surface revision…"
          >
            {saving ? 'Saving…' : isNew ? 'Create surface' : 'Save surface'}
          </ScopedButton>
        </div>
      )}

      {!isNew && state.kind === 'loaded' && (
        <>
          <p className="panel" role="status">
            Active revision {state.config.revision}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
          </p>
          {state.revisions.length > 0 && (
            <>
              <h3 className="panel__title">Revision history</h3>
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Revision</th>
                    <th>Active</th>
                    <th>Created at</th>
                    <th>Created by</th>
                  </tr>
                </thead>
                <tbody>
                  {state.revisions.map((rev) => (
                    <tr key={rev.revision}>
                      <td>{rev.revision}</td>
                      <td>{rev.active ? 'active' : ''}</td>
                      <td>{formatAbsolute(rev.createdAt)}</td>
                      <td>{rev.createdByPrincipalName ?? '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </>
          )}
        </>
      )}
    </div>
  )
}
