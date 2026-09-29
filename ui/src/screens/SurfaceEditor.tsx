import { useState } from 'react'
import {
  deleteShowSurface,
  getShowSurface,
  getShowSurfaceRevisions,
  putShowSurface,
  type ConfigShowSurface,
  type ShowSurfaceConfigResponse,
} from '../api'
import { Button, DeletePanel, Field, Input, RevisionHistory, RuledStrip, Segmented, Select } from '../kit'
import type { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { channelsPerPixel, renderCapableNodes, slugify } from './showsModel'

type Transport = 'ndi' | 'hdmi'
type PixelFormat = 'rgb' | 'rgbw'

export type ShowChoice = { id: string; label: string }

/**
 * `showChoices` adds a show picker for a new surface (the node page has no
 * show of its own); `fixedNode` pins the node instead of offering a picker.
 */
export function SurfaceEditor({
  showId,
  showChoices,
  fixedNode,
  surface,
  existingIds,
  model,
  onSaved,
  onDeleted,
  onCancel,
}: {
  showId: string
  showChoices?: readonly ShowChoice[]
  fixedNode?: string
  surface: ShowSurfaceConfigResponse | null
  existingIds: readonly string[]
  model: ReturnType<typeof useModelContext>
  onSaved: (response: ShowSurfaceConfigResponse) => void
  onDeleted: () => void
  onCancel: () => void
}) {
  const isNew = surface === null
  const [name, setName] = useState(surface?.payload.name ?? '')
  const [id, setId] = useState(surface?.id ?? '')
  const [idTouched, setIdTouched] = useState(!isNew)
  const [node, setNode] = useState(surface?.payload.node ?? fixedNode ?? '')
  const [chosenShow, setChosenShow] = useState(showId)
  const targetShow = surface?.payload.show ?? chosenShow
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
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)

  const saveGate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const nodeOptions = renderCapableNodes(model.nodes)

  const remove = () => {
    if (surface === null) return
    setDeleting(true)
    setDeleteError(null)
    deleteShowSurface(surface.id)
      .then(onDeleted)
      .catch((err: unknown) => setDeleteError(describeApiError(err)))
      .finally(() => setDeleting(false))
  }

  const widthN = Number(width)
  const heightN = Number(height)
  const channelCount = Number.isFinite(widthN) && Number.isFinite(heightN) && widthN > 0 && heightN > 0 ? widthN * heightN * channelsPerPixel(pixelFormat) : 0

  let blockReason: string | null = null
  if (name.trim() === '') blockReason = 'A surface needs a name.'
  else if (id.trim() === '') blockReason = 'A surface needs an id.'
  else if (isNew && existingIds.includes(id)) blockReason = `The id "${id}" already names another surface in this show; edit that surface instead or choose a different id.`
  else if (targetShow === '') blockReason = 'Choose the show this surface belongs to.'
  else if (node.trim() === '') blockReason = 'A surface needs a node.'
  else if (!Number.isInteger(widthN) || widthN < 1) blockReason = 'Width must be a whole number, at least 1.'
  else if (!Number.isInteger(heightN) || heightN < 1) blockReason = 'Height must be a whole number, at least 1.'
  else if (!Number.isInteger(Number(startChannel)) || Number(startChannel) < 1) blockReason = 'Start channel must be a whole number, at least 1.'
  else if (transport === 'ndi' && ndiSourceName.trim() === '') blockReason = 'NDI output needs a source name.'
  else if (transport === 'hdmi' && hdmiDisplay.trim() === '') blockReason = 'HDMI output needs a display identifier.'
  else if (!Number.isInteger(Number(frameRate)) || Number(frameRate) < 1 || Number(frameRate) > 120) blockReason = 'Frame rate must be a whole number, 1 to 120.'

  const discard = () => {
    setName(surface?.payload.name ?? '')
    setNode(surface?.payload.node ?? fixedNode ?? '')
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
      show: targetShow,
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
      <p className="sm-eyebrow sm-eyebrow--accent" id="ps-surface-editor-heading">{isNew ? 'New surface' : `Editing ${surface.payload.name}`}</p>

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
        {showChoices !== undefined && (
          <Field label="Show" help={isNew ? undefined : 'A surface stays in the show it was created in.'}>
            {(props) => (
              <Select {...props} value={targetShow} disabled={!isNew} onChange={(e) => setChosenShow(e.target.value)}>
                <option value="">Choose a show…</option>
                {showChoices.map((choice) => (
                  <option key={choice.id} value={choice.id}>
                    {choice.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        {fixedNode !== undefined ? (
          <Field label="Node">{(props) => <Input {...props} className="sm-data" value={fixedNode} disabled readOnly />}</Field>
        ) : (
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
        )}
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
        <p className="sm-small sm-muted">Exactly one transport. NDI working does not mean HDMI also works on the same node.</p>
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

      {surface !== null && (
        <DeletePanel
          title="Delete this surface"
          confirmNoun="the surface's own name"
          confirmValue={surface.payload.name}
          actionLabel="Delete surface"
          deleting={deleting}
          error={deleteError}
          allowed={saveGate.allowed}
          disallowedReason={saveGate.allowed ? undefined : saveGate.reason}
          onDelete={remove}
        >
          <p className="sm-small sm-muted">Deleting a surface stops the node from resolving it; the node's own output configuration is not touched.</p>
        </DeletePanel>
      )}
    </div>
  )
}
