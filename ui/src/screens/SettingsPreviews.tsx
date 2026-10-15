import { useCallback, useEffect, useState } from 'react'
import {
  ApiError,
  deletePreviewCamera,
  getPreviewCamera,
  getPreviewGatewayConfig,
  listConfigObjects,
  putPreviewCamera,
  putPreviewGatewayConfig,
  type PreviewCameraConfigResponse,
  type PreviewGatewayConfigResponse,
} from '../api'
import { Button, ButtonRow, Choice, ConfirmDialog, Field, Input, RuledStrip, Section } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave } from '../domain/save'
import { StaleWriteStrip, type StaleWrite } from './StaleWrite'
import { cameraDraftError, cameraRequest, gatewayRequest, gatewayUrlError, type CameraDraft } from './previewSettingsModel'

type Outcome = { saved: true } | { stale: StaleWrite } | { error: string } | null

function useScopeGate() {
  const model = useModelContext()
  return evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
}

function OutcomeStrip({ outcome, onReload }: { outcome: Outcome; onReload: () => void }) {
  if (outcome === null) return null
  if ('stale' in outcome) return <StaleWriteStrip stale={outcome.stale} onReload={onReload} />
  if ('error' in outcome) return <RuledStrip absence="failed" label="Save failed" fact={outcome.error} />
  return null
}

type GatewayLoad = { kind: 'loading' } | { kind: 'loaded'; response: PreviewGatewayConfigResponse } | { kind: 'notConfigured' } | { kind: 'failed'; reason: string }

function PreviewGatewaySection() {
  const gate = useScopeGate()
  const [attempt, setAttempt] = useState(0)
  const [load, setLoad] = useState<GatewayLoad>({ kind: 'loading' })
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [outcome, setOutcome] = useState<Outcome>(null)

  useEffect(() => {
    let cancelled = false
    setLoad({ kind: 'loading' })
    getPreviewGatewayConfig()
      .then((response) => {
        if (cancelled) return
        setLoad({ kind: 'loaded', response })
        setUrl(response.payload.url)
        setToken('')
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setLoad({ kind: 'notConfigured' })
          setUrl('')
          setToken('')
          setDirty(false)
          return
        }
        setLoad({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const urlError = gatewayUrlError(url)

  const save = async () => {
    if (urlError !== null) {
      setOutcome({ error: urlError })
      return
    }
    setSaving(true)
    setOutcome(null)
    const request = gatewayRequest(url, token)
    try {
      const result =
        load.kind === 'loaded'
          ? await guardedSave({ loaded: load.response, read: getPreviewGatewayConfig, write: () => putPreviewGatewayConfig(request) })
          : await guardedCreate({ read: getPreviewGatewayConfig, write: () => putPreviewGatewayConfig(request) })
      if (result.kind === 'saved' || result.kind === 'created') {
        setOutcome({ saved: true })
        setAttempt((n) => n + 1)
      } else if (result.kind === 'stale') {
        setOutcome({ stale: result })
      } else if (result.kind === 'taken') {
        setOutcome({ error: 'Someone else configured the preview gateway while this was open. Reload and start again.' })
      } else {
        setOutcome({ error: result.reason })
      }
    } catch (err: unknown) {
      setOutcome({ error: describeApiError(err) })
    } finally {
      setSaving(false)
    }
  }

  const tokenSet = load.kind === 'loaded' && load.response.payload.tokenSet

  return (
    <Section id="st-preview-gateway" title="Preview gateway">
      {load.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the preview gateway." />
      ) : load.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={load.reason} />
      ) : (
        <>
          {load.kind === 'notConfigured' && (
            <RuledStrip absence="unavailable" label="Not configured" fact="No preview gateway is set, so the Dashboard shows no previews. Enter its address and token to turn them on." />
          )}
          <div className="sm-grid sm-form-column">
            <Field label="Gateway address" error={urlError !== null && dirty ? urlError : undefined}>
              {(props) => (
                <Input {...props} value={url} placeholder="http://192.168.1.10:8095" onChange={(e) => { setUrl(e.target.value); setDirty(true) }} />
              )}
            </Field>
            <Field label={tokenSet ? 'A token is set. Set a new one' : 'No token is set. Set one'}>
              {(props) => (
                <Input {...props} type="password" value={token} autoComplete="new-password" onChange={(e) => { setToken(e.target.value); setDirty(true) }} />
              )}
            </Field>
          </div>
          <ButtonRow>
            <Button variant="primary" onClick={() => void save()} disabled={!dirty || saving || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
              {saving ? 'Saving…' : 'Save preview gateway'}
            </Button>
          </ButtonRow>
        </>
      )}
      <OutcomeStrip outcome={outcome} onReload={() => { setOutcome(null); setAttempt((n) => n + 1) }} />
    </Section>
  )
}

type CameraRow = { id: string; response: PreviewCameraConfigResponse }
type CameraLoad = { kind: 'loading' } | { kind: 'loaded'; rows: CameraRow[] } | { kind: 'failed'; reason: string }

async function readCameras(): Promise<CameraRow[]> {
  let ids: string[]
  try {
    ids = (await listConfigObjects('preview.camera')).objects.map((object) => object.id)
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 404) return []
    throw err
  }
  return Promise.all(ids.map(async (id) => ({ id, response: await getPreviewCamera(id) })))
}

function PreviewCamerasSection() {
  const gate = useScopeGate()
  const [attempt, setAttempt] = useState(0)
  const [load, setLoad] = useState<CameraLoad>({ kind: 'loading' })
  const reload = useCallback(() => setAttempt((n) => n + 1), [])

  useEffect(() => {
    let cancelled = false
    readCameras()
      .then((rows) => !cancelled && setLoad({ kind: 'loaded', rows }))
      .catch((err: unknown) => !cancelled && setLoad({ kind: 'failed', reason: describeApiError(err) }))
    return () => {
      cancelled = true
    }
  }, [attempt])

  const rows = load.kind === 'loaded' ? load.rows : []

  return (
    <Section id="st-preview-cameras" title="Preview cameras">
      {load.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the configured cameras." />
      ) : load.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={load.reason} />
      ) : (
        <>
          {rows.length === 0 && <RuledStrip absence="empty" label="No cameras" fact="No camera is set. Add one to show its picture on the Dashboard." />}
          <div className="sm-panel sm-stack-4">
            {rows.map((row) => (
              <CameraEditor key={`${row.id}:${row.response.revision}`} row={row} gate={gate} onChanged={reload} />
            ))}
          </div>
          <AddCamera takenIds={rows.map((row) => row.id)} gate={gate} onAdded={reload} />
        </>
      )}
    </Section>
  )
}

type Gate = ReturnType<typeof useScopeGate>

function CameraEditor({ row, gate, onChanged }: { row: CameraRow; gate: Gate; onChanged: () => void }) {
  const { payload } = row.response
  const [draft, setDraft] = useState<CameraDraft>({ id: row.id, name: payload.name, enabled: payload.enabled, url: '' })
  const [busy, setBusy] = useState(false)
  const [outcome, setOutcome] = useState<Outcome>(null)
  const [confirmRemove, setConfirmRemove] = useState(false)
  const dirty = draft.name !== payload.name || draft.enabled !== payload.enabled || draft.url !== ''
  const problem = cameraDraftError(draft, 'edit', [])

  const save = async () => {
    if (problem !== null) {
      setOutcome({ error: problem })
      return
    }
    setBusy(true)
    setOutcome(null)
    try {
      const result = await guardedSave({
        loaded: row.response,
        read: () => getPreviewCamera(row.id),
        write: () => putPreviewCamera(row.id, cameraRequest(draft)),
      })
      if (result.kind === 'saved') onChanged()
      else if (result.kind === 'stale') setOutcome({ stale: result })
      else setOutcome({ error: result.reason })
    } catch (err: unknown) {
      setOutcome({ error: describeApiError(err) })
    } finally {
      setBusy(false)
    }
  }

  const remove = async () => {
    setBusy(true)
    try {
      await deletePreviewCamera(row.id)
      setConfirmRemove(false)
      onChanged()
    } catch (err: unknown) {
      setConfirmRemove(false)
      setOutcome({ error: describeApiError(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="sm-panel">
      <p className="sm-small sm-muted">
        <span className="sm-data">{row.id}</span> · {payload.urlSet ? <span className="sm-data">{payload.urlDisplay}</span> : 'No address is set.'}
      </p>
      <div className="sm-inline-row sm-inline-row--end">
        <Field label="Name">
          {(props) => <Input {...props} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />}
        </Field>
        <Field label="Set a new address">
          {(props) => (
            <Input {...props} type="password" value={draft.url} placeholder="rtsp://" autoComplete="off" className="sm-input--address" onChange={(e) => setDraft({ ...draft, url: e.target.value })} />
          )}
        </Field>
        <Choice type="checkbox" checked={draft.enabled} onChange={(e) => setDraft({ ...draft, enabled: e.target.checked })} label="Show on the Dashboard" />
      </div>
      <ButtonRow>
        <Button variant="primary" onClick={() => void save()} disabled={!dirty || busy || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
          Save {payload.name}
        </Button>
        <Button variant="quiet" aria-label={`Remove ${payload.name}`} onClick={() => setConfirmRemove(true)} disabled={busy || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
          Remove
        </Button>
      </ButtonRow>
      <OutcomeStrip outcome={outcome} onReload={onChanged} />
      <ConfirmDialog
        open={confirmRemove}
        title={`Remove ${payload.name}?`}
        detail="The tile leaves the Dashboard and the stored address is deleted. Add the camera again to bring it back."
        confirmLabel="Remove camera"
        busy={busy}
        onConfirm={() => void remove()}
        onCancel={() => setConfirmRemove(false)}
      />
    </div>
  )
}

function AddCamera({ takenIds, gate, onAdded }: { takenIds: readonly string[]; gate: Gate; onAdded: () => void }) {
  const empty: CameraDraft = { id: '', name: '', enabled: true, url: '' }
  const [draft, setDraft] = useState<CameraDraft>(empty)
  const [busy, setBusy] = useState(false)
  const [outcome, setOutcome] = useState<Outcome>(null)

  const add = async () => {
    const problem = cameraDraftError(draft, 'new', takenIds)
    if (problem !== null) {
      setOutcome({ error: problem })
      return
    }
    const id = draft.id.trim()
    setBusy(true)
    setOutcome(null)
    try {
      const result = await guardedCreate({ read: () => getPreviewCamera(id), write: () => putPreviewCamera(id, cameraRequest(draft)) })
      if (result.kind === 'created') {
        setDraft(empty)
        onAdded()
      } else if (result.kind === 'taken') {
        setOutcome({ error: `A camera with the id ${id} already exists. Choose another id.` })
      } else {
        setOutcome({ error: result.reason })
      }
    } catch (err: unknown) {
      setOutcome({ error: describeApiError(err) })
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="sm-stack-3">
      <p className="sm-field__label">Add a camera</p>
      <div className="sm-inline-row sm-inline-row--end">
        <Field label="Camera id">
          {(props) => <Input {...props} value={draft.id} placeholder="driveway" className="sm-input--id" onChange={(e) => setDraft({ ...draft, id: e.target.value })} />}
        </Field>
        <Field label="Camera name">
          {(props) => <Input {...props} value={draft.name} onChange={(e) => setDraft({ ...draft, name: e.target.value })} />}
        </Field>
        <Field label="Camera address">
          {(props) => (
            <Input {...props} type="password" value={draft.url} placeholder="rtsp://" autoComplete="off" className="sm-input--address" onChange={(e) => setDraft({ ...draft, url: e.target.value })} />
          )}
        </Field>
      </div>
      <ButtonRow>
        <Button variant="primary" onClick={() => void add()} disabled={busy || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
          {busy ? 'Adding…' : 'Add camera'}
        </Button>
      </ButtonRow>
      <OutcomeStrip outcome={outcome} onReload={onAdded} />
    </div>
  )
}

/** Settings › Connections: the preview gateway and its cameras. */
export function SettingsPreviews() {
  return (
    <>
      <PreviewGatewaySection />
      <PreviewCamerasSection />
    </>
  )
}
