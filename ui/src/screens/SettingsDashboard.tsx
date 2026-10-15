import { useEffect, useState } from 'react'
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
import { Button, ButtonRow, Choice, ConfirmDialog, Field, Input, RuledStrip, Section, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave } from '../domain/save'
import { StaleWriteStrip, type StaleWrite } from './StaleWrite'
import { cameraDraftError, cameraRequest, gatewayRequest, gatewayUrlError } from './previewSettingsModel'

type CameraRow = { key: number; id: string; name: string; enabled: boolean; url: string; loaded: PreviewCameraConfigResponse | null }

type Loaded = {
  gateway: PreviewGatewayConfigResponse | null
  cameras: { id: string; response: PreviewCameraConfigResponse }[]
}

type Load = { kind: 'loading' } | { kind: 'loaded'; data: Loaded } | { kind: 'failed'; reason: string }

type Outcome = { stale: StaleWrite } | { error: string } | null

async function readGateway(): Promise<PreviewGatewayConfigResponse | null> {
  try {
    return await getPreviewGatewayConfig()
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 404) return null
    throw err
  }
}

async function readCameras(): Promise<Loaded['cameras']> {
  let ids: string[]
  try {
    ids = (await listConfigObjects('preview.camera')).objects.map((object) => object.id)
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 404) return []
    throw err
  }
  return Promise.all(ids.map(async (id) => ({ id, response: await getPreviewCamera(id) })))
}

function rowsFrom(cameras: Loaded['cameras']): CameraRow[] {
  return cameras.map((camera, index) => ({
    key: index,
    id: camera.id,
    name: camera.response.payload.name,
    enabled: camera.response.payload.enabled,
    url: '',
    loaded: camera.response,
  }))
}

function rowDirty(row: CameraRow): boolean {
  if (row.loaded === null) return true
  return row.name !== row.loaded.payload.name || row.enabled !== row.loaded.payload.enabled || row.url !== ''
}

/** Settings › Dashboard: the preview gateway and the cameras the Dashboard shows. */
export function SettingsDashboard() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [load, setLoad] = useState<Load>({ kind: 'loading' })
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [rows, setRows] = useState<CameraRow[]>([])
  const [removed, setRemoved] = useState<CameraRow[]>([])
  const [nextKey, setNextKey] = useState(0)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [confirmRemoval, setConfirmRemoval] = useState(false)
  const [outcome, setOutcome] = useState<Outcome>(null)

  useEffect(() => {
    let cancelled = false
    setLoad({ kind: 'loading' })
    Promise.all([readGateway(), readCameras()])
      .then(([gateway, cameras]) => {
        if (cancelled) return
        setLoad({ kind: 'loaded', data: { gateway, cameras } })
        setUrl(gateway?.payload.url ?? '')
        setToken('')
        setRows(rowsFrom(cameras))
        setRemoved([])
        setNextKey(cameras.length)
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (!cancelled) setLoad({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const reload = () => {
    setOutcome(null)
    setAttempt((n) => n + 1)
  }

  const patchRow = (key: number, patch: Partial<CameraRow>) => {
    setRows(rows.map((row) => (row.key === key ? { ...row, ...patch } : row)))
    setDirty(true)
  }

  const removeRow = (row: CameraRow) => {
    setRows(rows.filter((r) => r.key !== row.key))
    if (row.loaded !== null) setRemoved([...removed, row])
    setDirty(true)
  }

  const addRow = () => {
    setRows([...rows, { key: nextKey, id: '', name: '', enabled: true, url: '', loaded: null }])
    setNextKey(nextKey + 1)
    setDirty(true)
  }

  const firstProblem = (): string | null => {
    const urlProblem = gatewayUrlError(url)
    if (urlProblem !== null) return urlProblem
    const takenIds = [...rows.filter((row) => row.loaded !== null).map((row) => row.id)]
    for (const row of rows) {
      const isNew = row.loaded === null
      const problem = cameraDraftError(row, isNew ? 'new' : 'edit', isNew ? takenIds : [])
      if (problem !== null) return problem
      if (isNew) takenIds.push(row.id.trim())
    }
    return null
  }

  const write = async (data: Loaded) => {
    const gatewayChanged = url.trim() !== (data.gateway?.payload.url ?? '') || token !== ''
    if (gatewayChanged) {
      const request = gatewayRequest(url, token)
      if (data.gateway !== null) {
        const result = await guardedSave({ loaded: data.gateway, read: getPreviewGatewayConfig, write: () => putPreviewGatewayConfig(request) })
        if (result.kind === 'stale') return setOutcome({ stale: result })
        if (result.kind === 'unreadable') return setOutcome({ error: result.reason })
      } else {
        const result = await guardedCreate({ read: getPreviewGatewayConfig, write: () => putPreviewGatewayConfig(request) })
        if (result.kind === 'taken') return setOutcome({ error: 'Someone else set the preview gateway while this page was open. Reload and enter it again.' })
        if (result.kind === 'unreadable') return setOutcome({ error: result.reason })
      }
    }
    for (const row of removed) {
      await deletePreviewCamera(row.id)
    }
    for (const row of rows.filter(rowDirty)) {
      const id = row.id.trim()
      if (row.loaded === null) {
        const result = await guardedCreate({ read: () => getPreviewCamera(id), write: () => putPreviewCamera(id, cameraRequest(row)) })
        if (result.kind === 'taken') return setOutcome({ error: `A camera with the id ${id} already exists. Choose another id.` })
        if (result.kind === 'unreadable') return setOutcome({ error: result.reason })
      } else {
        const result = await guardedSave({ loaded: row.loaded, read: () => getPreviewCamera(id), write: () => putPreviewCamera(id, cameraRequest(row)) })
        if (result.kind === 'stale') return setOutcome({ stale: result })
        if (result.kind === 'unreadable') return setOutcome({ error: result.reason })
      }
    }
    setAttempt((n) => n + 1)
  }

  const save = () => {
    if (load.kind !== 'loaded') return
    const problem = firstProblem()
    if (problem !== null) {
      setOutcome({ error: problem })
      return
    }
    setConfirmRemoval(false)
    setSaving(true)
    setOutcome(null)
    write(load.data)
      .catch((err: unknown) => setOutcome({ error: describeApiError(err) }))
      .finally(() => setSaving(false))
  }

  const requestSave = () => {
    if (removed.length > 0) setConfirmRemoval(true)
    else save()
  }

  const gatewayTokenSet = load.kind === 'loaded' && load.data.gateway !== null && load.data.gateway.payload.tokenSet
  const urlError = dirty ? gatewayUrlError(url) : null

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Dashboard</p>
      <h2 className="sm-section__title">Video previews on the Dashboard</h2>
      {load.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the preview gateway and cameras." />
      ) : load.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={load.reason} />
      ) : (
        <>
          <Section id="st-dash-gateway" title="Preview gateway" detail="The gateway turns NDI sources and cameras into the Dashboard's video tiles.">
            {load.data.gateway === null && (
              <RuledStrip absence="unavailable" label="Not configured" fact="No preview gateway is set, so the Dashboard shows no previews. Enter its address and token to turn them on." />
            )}
            <div className="sm-grid sm-form-column">
              <Field label="Gateway address" error={urlError ?? undefined}>
                {(props) => (
                  <Input {...props} value={url} onChange={(e) => { setUrl(e.target.value); setDirty(true) }} />
                )}
              </Field>
              <Field label={gatewayTokenSet ? 'A token is set. Set a new one' : 'No token is set. Set one'}>
                {(props) => (
                  <Input {...props} type="password" value={token} autoComplete="new-password" onChange={(e) => { setToken(e.target.value); setDirty(true) }} />
                )}
              </Field>
            </div>
          </Section>

          <Section id="st-dash-cameras" title="Cameras" detail="A camera's address is stored and never shown again.">
            {rows.length === 0 ? (
              <RuledStrip absence="empty" label="No cameras" fact="No camera is set. Add one to show its picture on the Dashboard." />
            ) : (
              <div className="sm-panel sm-stack-4">
                {rows.map((row) => (
                  <div key={row.key} className="sm-panel">
                    <div className="sm-inline-row sm-inline-row--end">
                      <Field label="Camera id">
                        {(props) => (
                          <Input {...props} value={row.id} disabled={row.loaded !== null} className="sm-input--id" onChange={(e) => patchRow(row.key, { id: e.target.value })} />
                        )}
                      </Field>
                      <Field label="Name">
                        {(props) => <Input {...props} value={row.name} onChange={(e) => patchRow(row.key, { name: e.target.value })} />}
                      </Field>
                      <Field label={row.loaded === null ? 'Address' : 'Set a new address'}>
                        {(props) => (
                          <Input {...props} type="password" value={row.url} autoComplete="off" className="sm-input--address" onChange={(e) => patchRow(row.key, { url: e.target.value })} />
                        )}
                      </Field>
                      <Choice type="checkbox" checked={row.enabled} onChange={(e) => patchRow(row.key, { enabled: e.target.checked })} label="Show on the Dashboard" />
                      <Button variant="quiet" aria-label={`Remove ${row.name === '' ? 'this camera' : row.name}`} onClick={() => removeRow(row)}>
                        Remove
                      </Button>
                      {rowDirty(row) && <StatusPair tone="warn" label="Unsaved" />}
                    </div>
                    {row.loaded !== null && (
                      <p className="sm-small sm-muted">
                        {row.loaded.payload.urlSet ? (
                          <>Stored address <span className="sm-data">{row.loaded.payload.urlDisplay}</span></>
                        ) : (
                          'No address is stored. Set one to show this camera.'
                        )}
                      </p>
                    )}
                  </div>
                ))}
              </div>
            )}
            <Button onClick={addRow}>Add a camera</Button>
          </Section>
        </>
      )}

      <ButtonRow>
        <Button variant="primary" onClick={requestSave} disabled={!dirty || saving || load.kind !== 'loaded' || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
          {saving ? 'Saving…' : 'Save dashboard settings'}
        </Button>
        <Button variant="quiet" onClick={reload} disabled={!dirty || saving}>
          Discard changes
        </Button>
      </ButtonRow>
      {outcome !== null && 'stale' in outcome && <StaleWriteStrip stale={outcome.stale} onReload={reload} />}
      {outcome !== null && 'error' in outcome && <RuledStrip absence="failed" label="Save failed" fact={outcome.error} />}

      <ConfirmDialog
        open={confirmRemoval}
        title={removed.length === 1 ? `Remove ${removed[0]?.name ?? 'this camera'}?` : `Remove ${removed.length} cameras?`}
        detail="Their tiles leave the Dashboard and their stored addresses are deleted. Add a camera again to bring it back."
        confirmLabel="Remove and save"
        busy={saving}
        onConfirm={save}
        onCancel={() => setConfirmRemoval(false)}
      />
    </>
  )
}
