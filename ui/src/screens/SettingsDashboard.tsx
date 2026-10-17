import { useEffect, useState } from 'react'
import {
  ApiError,
  deletePreviewCamera,
  deletePreviewNDI,
  getPreviewCamera,
  getPreviewNDI,
  getPreviewGatewayConfig,
  getPreviewGatewayConfigRevisions,
  listConfigObjects,
  putPreviewCamera,
  putPreviewGatewayConfig,
  putPreviewNDI,
  type PreviewCameraConfigResponse,
  type PreviewGatewayConfigResponse,
  type PreviewNDIConfigResponse,
} from '../api'
import { Button, ButtonRow, Choice, ConfirmDialog, Field, FieldGrid, Input, RevisionHistory, RuledStrip, Section, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave } from '../domain/save'
import { StaleWriteStrip, type StaleWrite } from './StaleWrite'
import { cameraDraftError, cameraRequest, gatewayRequest, gatewayUrlError, ndiDraftError, ndiRequest } from './previewSettingsModel'

type CameraRow = { key: number; id: string; name: string; enabled: boolean; url: string; loaded: PreviewCameraConfigResponse | null }
type NDIRow = { key: number; id: string; label: string; ndiName: string; address: string; enabled: boolean; loaded: PreviewNDIConfigResponse | null }

type Loaded = {
  gateway: PreviewGatewayConfigResponse | null
  cameras: { id: string; response: PreviewCameraConfigResponse }[]
  ndis: { id: string; response: PreviewNDIConfigResponse }[]
}

type Load = { kind: 'loading' } | { kind: 'loaded'; data: Loaded } | { kind: 'failed'; reason: string }

/** A refused save, with what had already saved before it stopped. */
type Outcome = ({ stale: StaleWrite } | { error: string }) & { saved: string[] }

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

async function readNDISources(): Promise<Loaded['ndis']> {
  let ids: string[]
  try {
    ids = (await listConfigObjects('preview.ndi')).objects.map((object) => object.id)
  } catch (err: unknown) {
    if (err instanceof ApiError && err.status === 404) return []
    throw err
  }
  return Promise.all(ids.map(async (id) => ({ id, response: await getPreviewNDI(id) })))
}

function ndiRowsFrom(ndis: Loaded['ndis'], firstKey: number): NDIRow[] {
  return ndis.map((ndi, index) => ({ key: firstKey + index, id: ndi.id, ...ndi.response.payload, loaded: ndi.response }))
}

function ndiRowDirty(row: NDIRow): boolean {
  if (row.loaded === null) return true
  const p = row.loaded.payload
  return row.label !== p.label || row.ndiName !== p.ndiName || row.address !== p.address || row.enabled !== p.enabled
}

function ndiRowName(row: NDIRow): string {
  const p = row.loaded?.payload
  if (p === undefined) return 'New NDI source'
  return p.label !== '' ? p.label : p.ndiName
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

function removalTitle(cameras: CameraRow[], ndis: NDIRow[]): string {
  const only = [...cameras.map((row) => row.name), ...ndis.map(ndiRowName)]
  if (only.length === 1) return `Remove ${only[0] ?? 'this item'}?`
  if (ndis.length === 0) return `Remove ${cameras.length} cameras?`
  if (cameras.length === 0) return `Remove ${ndis.length} NDI sources?`
  return `Remove ${cameras.length} cameras and ${ndis.length} NDI sources?`
}

/** Settings › Dashboard: the preview gateway, the cameras and the NDI sources added by hand. */
export function SettingsDashboard() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [load, setLoad] = useState<Load>({ kind: 'loading' })
  const [url, setUrl] = useState('')
  const [token, setToken] = useState('')
  const [rows, setRows] = useState<CameraRow[]>([])
  const [removed, setRemoved] = useState<CameraRow[]>([])
  const [ndiRows, setNDIRows] = useState<NDIRow[]>([])
  const [removedNDI, setRemovedNDI] = useState<NDIRow[]>([])
  const [nextKey, setNextKey] = useState(0)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [confirmRemoval, setConfirmRemoval] = useState(false)
  const [outcome, setOutcome] = useState<Outcome | null>(null)

  useEffect(() => {
    let cancelled = false
    setLoad({ kind: 'loading' })
    Promise.all([readGateway(), readCameras(), readNDISources()])
      .then(([gateway, cameras, ndis]) => {
        if (cancelled) return
        setLoad({ kind: 'loaded', data: { gateway, cameras, ndis } })
        setUrl(gateway?.payload.url ?? '')
        setToken('')
        setRows(rowsFrom(cameras))
        setRemoved([])
        setNDIRows(ndiRowsFrom(ndis, cameras.length))
        setRemovedNDI([])
        setNextKey(cameras.length + ndis.length)
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

  const patchNDIRow = (key: number, patch: Partial<NDIRow>) => {
    setNDIRows(ndiRows.map((row) => (row.key === key ? { ...row, ...patch } : row)))
    setDirty(true)
  }

  const removeNDIRow = (row: NDIRow) => {
    setNDIRows(ndiRows.filter((r) => r.key !== row.key))
    if (row.loaded !== null) setRemovedNDI([...removedNDI, row])
    setDirty(true)
  }

  const addNDIRow = () => {
    setNDIRows([...ndiRows, { key: nextKey, id: '', label: '', ndiName: '', address: '', enabled: true, loaded: null }])
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
    const takenNDIIds = ndiRows.filter((row) => row.loaded !== null).map((row) => row.id)
    for (const row of ndiRows) {
      const isNew = row.loaded === null
      const problem = ndiDraftError(row, isNew ? 'new' : 'edit', isNew ? takenNDIIds : [])
      if (problem !== null) return problem
      if (isNew) takenNDIIds.push(row.id.trim())
    }
    return null
  }

  /** Writes each changed part in turn. Every part that saves updates the loaded state at once, so a retry starts from what is stored. */
  const write = async (data: Loaded) => {
    const stored: Loaded = { gateway: data.gateway, cameras: [...data.cameras], ndis: [...data.ndis] }
    const saved: string[] = []
    const savedRows = new Map<number, PreviewCameraConfigResponse>()
    const savedNDIRows = new Map<number, PreviewNDIConfigResponse>()
    const deletedIds = new Set<string>()
    const deletedNDIIds = new Set<string>()
    let gatewaySaved = false
    const result: { refusal: Outcome | null } = { refusal: null }
    const refuse = (outcome: { stale: StaleWrite } | { error: string }) => {
      result.refusal = { ...outcome, saved }
    }

    const run = async () => {
      const gatewayChanged = url.trim() !== (data.gateway?.payload.url ?? '') || token !== ''
      if (gatewayChanged) {
        const request = gatewayRequest(url, token)
        if (data.gateway !== null) {
          const result = await guardedSave({ loaded: data.gateway, read: getPreviewGatewayConfig, write: () => putPreviewGatewayConfig(request) })
          if (result.kind === 'stale') return refuse({ stale: result })
          if (result.kind === 'unreadable') return refuse({ error: result.reason })
          stored.gateway = result.response
        } else {
          const result = await guardedCreate({ read: getPreviewGatewayConfig, write: () => putPreviewGatewayConfig(request) })
          if (result.kind === 'taken') return refuse({ error: 'Someone else set the preview gateway while this page was open. Reload and enter it again.' })
          if (result.kind === 'unreadable') return refuse({ error: result.reason })
          stored.gateway = result.response
        }
        gatewaySaved = true
        saved.push('the gateway')
      }
      for (const row of removed) {
        await deletePreviewCamera(row.id)
        deletedIds.add(row.id)
        stored.cameras = stored.cameras.filter((camera) => camera.id !== row.id)
        saved.push(`removing ${row.loaded?.payload.name ?? row.id}`)
      }
      for (const row of rows.filter(rowDirty)) {
        const id = row.id.trim()
        if (row.loaded === null) {
          const result = await guardedCreate({ read: () => getPreviewCamera(id), write: () => putPreviewCamera(id, cameraRequest(row)) })
          if (result.kind === 'taken') return refuse({ error: `A camera with the id ${id} already exists. Choose another id.` })
          if (result.kind === 'unreadable') return refuse({ error: result.reason })
          savedRows.set(row.key, result.response)
          stored.cameras.push({ id, response: result.response })
        } else {
          const result = await guardedSave({ loaded: row.loaded, read: () => getPreviewCamera(id), write: () => putPreviewCamera(id, cameraRequest(row)) })
          if (result.kind === 'stale') return refuse({ stale: result })
          if (result.kind === 'unreadable') return refuse({ error: result.reason })
          savedRows.set(row.key, result.response)
          stored.cameras = stored.cameras.map((camera) => (camera.id === id ? { id, response: result.response } : camera))
        }
        saved.push(row.name.trim())
      }
      for (const row of removedNDI) {
        await deletePreviewNDI(row.id)
        deletedNDIIds.add(row.id)
        stored.ndis = stored.ndis.filter((ndi) => ndi.id !== row.id)
        saved.push(`removing ${ndiRowName(row)}`)
      }
      for (const row of ndiRows.filter(ndiRowDirty)) {
        const id = row.id.trim()
        if (row.loaded === null) {
          const result = await guardedCreate({ read: () => getPreviewNDI(id), write: () => putPreviewNDI(id, ndiRequest(row)) })
          if (result.kind === 'taken') return refuse({ error: `An NDI source with the id ${id} already exists. Choose another id.` })
          if (result.kind === 'unreadable') return refuse({ error: result.reason })
          savedNDIRows.set(row.key, result.response)
          stored.ndis.push({ id, response: result.response })
        } else {
          const result = await guardedSave({ loaded: row.loaded, read: () => getPreviewNDI(id), write: () => putPreviewNDI(id, ndiRequest(row)) })
          if (result.kind === 'stale') return refuse({ stale: result })
          if (result.kind === 'unreadable') return refuse({ error: result.reason })
          savedNDIRows.set(row.key, result.response)
          stored.ndis = stored.ndis.map((ndi) => (ndi.id === id ? { id, response: result.response } : ndi))
        }
        saved.push(row.label.trim() !== '' ? row.label.trim() : row.ndiName.trim())
      }
    }

    try {
      await run()
    } catch (err: unknown) {
      refuse({ error: describeApiError(err) })
    }
    if (result.refusal === null) {
      setAttempt((n) => n + 1)
      return
    }
    setOutcome(result.refusal)
    if (saved.length === 0) return
    setLoad({ kind: 'loaded', data: stored })
    if (gatewaySaved) setToken('')
    setRemoved((list) => list.filter((row) => !deletedIds.has(row.id)))
    setRows((list) =>
      list.map((row) => {
        const response = savedRows.get(row.key)
        return response === undefined ? row : { ...row, id: row.id.trim(), name: response.payload.name, enabled: response.payload.enabled, url: '', loaded: response }
      }),
    )
    setRemovedNDI((list) => list.filter((row) => !deletedNDIIds.has(row.id)))
    setNDIRows((list) =>
      list.map((row) => {
        const response = savedNDIRows.get(row.key)
        return response === undefined ? row : { ...row, id: row.id.trim(), ...response.payload, loaded: response }
      }),
    )
  }

  const save = () => {
    if (load.kind !== 'loaded') return
    const problem = firstProblem()
    if (problem !== null) {
      setOutcome({ error: problem, saved: [] })
      return
    }
    setConfirmRemoval(false)
    setSaving(true)
    setOutcome(null)
    void write(load.data).finally(() => setSaving(false))
  }

  const requestSave = () => {
    if (removed.length + removedNDI.length > 0) setConfirmRemoval(true)
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
          <Section id="st-dash-gateway" title="Preview gateway">
            {load.data.gateway === null && (
              <RuledStrip absence="unavailable" label="Not configured" fact="No preview gateway is set, so the Dashboard shows no previews. Enter its address and token to turn them on." />
            )}
            <FieldGrid>
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
            </FieldGrid>
          </Section>

          <Section id="st-dash-cameras" title="Cameras" detail="A camera's address is stored and never shown again.">
            {rows.length === 0 ? (
              <RuledStrip absence="empty" label="No cameras" fact="No camera is set. Add one to show its picture on the Dashboard." />
            ) : (
              <div className="sm-panel sm-stack-4">
                {rows.map((row) => (
                  <div key={row.key} className="sm-panel sm-stack-4">
                    <h3 className="sm-subsection__title">{row.loaded !== null ? row.loaded.payload.name : 'New camera'}</h3>
                    <div className="sm-inline-row sm-inline-row--end">
                      <Field label="Camera id">
                        {(props) => <Input {...props} value={row.id} disabled={row.loaded !== null} onChange={(e) => patchRow(row.key, { id: e.target.value })} className="sm-input--id" />}
                      </Field>
                      <Field label="Name">
                        {(props) => <Input {...props} value={row.name} onChange={(e) => patchRow(row.key, { name: e.target.value })} />}
                      </Field>
                      <Field
                        label={row.loaded === null ? 'Address' : 'Set a new address'}
                        help={row.loaded === null ? undefined : row.loaded.payload.urlSet ? `Stored address ${row.loaded.payload.urlDisplay}` : 'No address is stored. Set one to show this camera.'}
                      >
                        {(props) => <Input {...props} type="password" value={row.url} autoComplete="off" onChange={(e) => patchRow(row.key, { url: e.target.value })} className="sm-input--address" />}
                      </Field>
                      <Button variant="quiet" aria-label={`Remove ${row.name === '' ? 'this camera' : row.name}`} onClick={() => removeRow(row)}>
                        Remove
                      </Button>
                      {rowDirty(row) && <StatusPair tone="warn" label="Unsaved" />}
                    </div>
                    <Choice type="checkbox" checked={row.enabled} onChange={(e) => patchRow(row.key, { enabled: e.target.checked })} label="Stream this camera" />
                  </div>
                ))}
              </div>
            )}
            <Button onClick={addRow}>Add a camera</Button>
          </Section>

          <Section id="st-dash-ndi" title="NDI sources" detail="Add a source that discovery cannot find, such as one on another network.">
            {ndiRows.length === 0 ? (
              <RuledStrip absence="empty" label="No NDI sources" fact="No NDI source is added by hand. Discovered sources still show on the Dashboard." />
            ) : (
              <div className="sm-panel sm-stack-4">
                {ndiRows.map((row) => (
                  <div key={row.key} className="sm-panel sm-stack-4">
                    <h3 className="sm-subsection__title">{ndiRowName(row)}</h3>
                    <div className="sm-inline-row sm-inline-row--end">
                      <Field label="Source id">
                        {(props) => <Input {...props} value={row.id} disabled={row.loaded !== null} onChange={(e) => patchNDIRow(row.key, { id: e.target.value })} className="sm-input--id" />}
                      </Field>
                      <Field label="Label">
                        {(props) => <Input {...props} value={row.label} onChange={(e) => patchNDIRow(row.key, { label: e.target.value })} />}
                      </Field>
                      <Field label="NDI name">
                        {(props) => <Input {...props} value={row.ndiName} onChange={(e) => patchNDIRow(row.key, { ndiName: e.target.value })} />}
                      </Field>
                      <Field label="Address" help="Optional host:port. Leave it empty to find the source by name.">
                        {(props) => <Input {...props} value={row.address} onChange={(e) => patchNDIRow(row.key, { address: e.target.value })} className="sm-input--address" />}
                      </Field>
                      <Button variant="quiet" aria-label={`Remove ${row.loaded === null ? 'this NDI source' : ndiRowName(row)}`} onClick={() => removeNDIRow(row)}>
                        Remove
                      </Button>
                      {ndiRowDirty(row) && <StatusPair tone="warn" label="Unsaved" />}
                    </div>
                    <Choice type="checkbox" checked={row.enabled} onChange={(e) => patchNDIRow(row.key, { enabled: e.target.checked })} label="Stream this source" />
                  </div>
                ))}
              </div>
            )}
            <Button onClick={addNDIRow}>Add an NDI source</Button>
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
        <div className="sm-push-end">
          <p className="sm-small sm-faint">Preview gateway</p>
          <RevisionHistory id="st-dash-gateway-rev" fetch={getPreviewGatewayConfigRevisions} reloadKey={attempt} />
        </div>
      </ButtonRow>
      {outcome !== null && 'stale' in outcome && <StaleWriteStrip stale={outcome.stale} onReload={reload} />}
      {outcome !== null && 'error' in outcome && <RuledStrip absence="failed" label="Save failed" fact={outcome.error} />}
      {outcome !== null && outcome.saved.length > 0 && <p className="sm-small sm-muted">Already saved: {outcome.saved.join(', ')}. The rest is still unsaved.</p>}

      <ConfirmDialog
        open={confirmRemoval}
        title={removalTitle(removed, removedNDI)}
        detail={
          removedNDI.length === 0
            ? 'Their tiles leave the Dashboard and their stored addresses are deleted. Add a camera again to bring it back.'
            : 'Their tiles leave the Dashboard unless discovery still finds them. Add them again to bring them back.'
        }
        confirmLabel="Remove and save"
        busy={saving}
        onConfirm={save}
        onCancel={() => setConfirmRemoval(false)}
      />
    </>
  )
}
