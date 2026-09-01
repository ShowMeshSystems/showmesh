import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  applyRenderSurface,
  clearRenderSurface,
  declareNode,
  deleteNodeDeclaration,
  deployNodeCueCatalog,
  getShowSurface,
  getNodeAssetManifest,
  getNodeCueCatalog,
  listShowSurfacesForNode,
  probeRenderTransport,
  restartRenderPipeline,
  runDiscovery,
  type Capability,
  type CueCatalogDeployResult,
  type CueCatalogResponse,
  type NodeAssetManifest,
  type RenderCommandResult,
  type ShowSurfaceConfigResponse,
} from '../api'
import {
  BlankingPlate,
  Button,
  ButtonRow,
  DefinitionStrip,
  Field,
  Input,
  NotWired,
  NotWiredBanner,
  PageTitle,
  RuledStrip,
  Section,
  SelectableRow,
  StatusPair,
  Table,
  TableWrap,
  type Tone,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { ageMs, effectiveServerTimeIso, formatClock, formatDateClock, formatDuration } from '../domain/time'
import { signalRows, signalSummary } from './monitorModel'
import { formatBytes, hashLabel, surfaceRenderStatus } from './showsModel'
import type { Node } from '../api'

const CONTROL_TONE: Record<Node['controlPlane']['state'], Tone> = { online: 'good', offline: 'bad', unknown: 'unknown' }
const CONTROL_WORD: Record<Node['controlPlane']['state'], string> = { online: 'Online', offline: 'Offline', unknown: 'Unknown' }

const MANIFEST_TONE: Record<NodeAssetManifest['state'], Tone> = { ready: 'good', not_ready: 'warn', unknown: 'unknown' }
const MANIFEST_LABEL: Record<NodeAssetManifest['state'], string> = { ready: 'Ready', not_ready: 'Not ready', unknown: 'Unknown' }

/** This node's surfaces and its asset manifest: on-demand reads, not part of the live model. */
type SurfacesState =
  | { kind: 'loading' }
  | { kind: 'loaded'; surfaces: ShowSurfaceConfigResponse[] }
  | { kind: 'failed'; reason: string; surfaces: ShowSurfaceConfigResponse[] }

function useNodeSurfaces(nodeId: string): { state: SurfacesState; reload: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<SurfacesState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState((prev) => (prev.kind === 'loaded' ? prev : { kind: 'loading' }))
    listShowSurfacesForNode(nodeId)
      .then((response) => Promise.all(response.objects.map((object) => getShowSurface(object.id))))
      .then((surfaces) => {
        if (!cancelled) setState({ kind: 'loaded', surfaces })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) => ({ kind: 'failed', reason: describeApiError(err), surfaces: prev.kind === 'loading' ? [] : prev.surfaces }))
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, attempt])

  return { state, reload: () => setAttempt((n) => n + 1) }
}

type ManifestState =
  | { kind: 'loading' }
  | { kind: 'loaded'; manifest: NodeAssetManifest; receivedAt: number }
  | { kind: 'failed'; reason: string; manifest: NodeAssetManifest | null; receivedAt: number | null }

function useNodeAssetManifest(nodeId: string): { state: ManifestState; reload: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ManifestState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState((prev) => (prev.kind === 'loaded' ? prev : { kind: 'loading' }))
    getNodeAssetManifest(nodeId)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', manifest: response.manifest, receivedAt: Date.now() })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) => ({
          kind: 'failed',
          reason: describeApiError(err),
          manifest: prev.kind === 'loading' ? null : prev.manifest,
          receivedAt: prev.kind === 'loaded' ? prev.receivedAt : prev.kind === 'failed' ? prev.receivedAt : null,
        }))
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, attempt])

  return { state, reload: () => setAttempt((n) => n + 1) }
}

function advertisedText(capability: Capability): string {
  const attrs = capability.attributes
  if (attrs === undefined || Object.keys(attrs).length === 0) return 'no attributes reported'
  return Object.entries(attrs)
    .map(([key, value]) => `${key}: ${typeof value === 'string' ? value : JSON.stringify(value)}`)
    .join(' · ')
}

/** A count with no accompanying list of names, derived only from the shapes actually reported. */
function reportsCountNotNames(capability: Capability): boolean {
  const attrs = capability.attributes
  if (attrs === undefined) return false
  const values = Object.values(attrs)
  return values.some((value) => typeof value === 'number') && !values.some((value) => Array.isArray(value))
}

function surfaceDisplay(surface: ShowSurfaceConfigResponse): string {
  const output = surface.payload.output
  if (output.transport === 'hdmi') return output.hdmi?.display ?? ''
  return output.ndi?.sourceName ?? ''
}

function ManifestEmptyRow({ manifest, fact }: { manifest: NodeAssetManifest; fact: string }) {
  if (manifest.state === 'unknown') {
    return (
      <RuledStrip
        absence="unobserved"
        label="No verdict"
        fact="This node has no asset evidence to judge."
        detail={manifest.reason ?? 'Nothing has been observed, so an empty list here would not mean an empty result.'}
      />
    )
  }
  return <RuledStrip absence="empty" label="None" fact={fact} />
}

type RenderOutcome = { tone: Tone; label: string; detail: string }

function renderOutcome(result: RenderCommandResult): RenderOutcome {
  if (result.outcome === 'confirmed') return { tone: 'good', label: 'Confirmed', detail: result.outcomeReason }
  if (result.outcome === 'unconfirmed') return { tone: result.pipelineFailed ? 'bad' : 'warn', label: result.pipelineFailed ? 'Pipeline failed' : 'Not confirmed', detail: result.outcomeReason }
  return { tone: 'pending', label: 'Still resolving', detail: result.outcomeReason }
}

/** A surface's four real render commands stay beside the assignment they address. */
function RenderSurfaceControls({ nodeId, surfaceId, gate }: { nodeId: string; surfaceId: string; gate: ReturnType<typeof evaluateScope> }) {
  const [sequenceId, setSequenceId] = useState('')
  const [running, setRunning] = useState<string | null>(null)
  const [outcome, setOutcome] = useState<RenderOutcome | null>(null)

  const run = (label: string, command: () => Promise<RenderCommandResult>) => {
    setRunning(label)
    setOutcome(null)
    command()
      .then((result) => setOutcome(renderOutcome(result)))
      .catch((err: unknown) => setOutcome({ tone: 'bad', label: 'Refused', detail: `${label}: ${describeApiError(err)}` }))
      .finally(() => setRunning(null))
  }

  return (
    <div className="sm-stack-2">
      <Field label="Sequence id" help="The active show resolves this sequence’s current FSEQ asset; FPP’s catalog is not inferred here.">
        {(field) => <Input {...field} value={sequenceId} onChange={(event) => setSequenceId(event.target.value)} placeholder="e.g. preshow-loop" />}
      </Field>
      <ButtonRow>
        <Button
          variant="primary"
          disabled={!gate.allowed || running !== null || sequenceId.trim() === ''}
          title={!gate.allowed ? gate.reason : sequenceId.trim() === '' ? 'Enter the sequence id to apply.' : undefined}
          onClick={() => run('Apply', () => applyRenderSurface(nodeId, surfaceId, sequenceId.trim()))}
        >
          {running === 'Apply' ? 'Applying…' : 'Apply'}
        </Button>
        <Button variant="danger" disabled={!gate.allowed || running !== null} title={gate.allowed ? undefined : gate.reason} onClick={() => run('Clear', () => clearRenderSurface(nodeId, surfaceId))}>
          {running === 'Clear' ? 'Clearing…' : 'Clear'}
        </Button>
        <Button disabled={!gate.allowed || running !== null} title={gate.allowed ? undefined : gate.reason} onClick={() => run('Restart pipeline', () => restartRenderPipeline(nodeId, surfaceId))}>
          {running === 'Restart pipeline' ? 'Restarting…' : 'Restart pipeline'}
        </Button>
        <Button disabled={!gate.allowed || running !== null} title={gate.allowed ? undefined : gate.reason} onClick={() => run('Probe transport', () => probeRenderTransport(nodeId, surfaceId))}>
          {running === 'Probe transport' ? 'Probing…' : 'Probe transport'}
        </Button>
      </ButtonRow>
      {outcome !== null && (
        <div className="sm-outcome" role="status">
          <StatusPair tone={outcome.tone} label={outcome.label} />
          <p className="sm-outcome__detail">{outcome.detail}</p>
        </div>
      )}
    </div>
  )
}

/** The node's copy is a deployable projection; it is never fabricated from capability advertisements. */
function CueCatalogControls({ nodeId, gate }: { nodeId: string; gate: ReturnType<typeof evaluateScope> }) {
  const [catalog, setCatalog] = useState<CueCatalogResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [result, setResult] = useState<CueCatalogDeployResult | null>(null)
  const [deploying, setDeploying] = useState(false)
  const reload = () => getNodeCueCatalog(nodeId).then(setCatalog).catch((err: unknown) => setError(describeApiError(err)))
  useEffect(() => { reload() }, [nodeId])
  const deploy = () => { setDeploying(true); setError(null); deployNodeCueCatalog(nodeId).then((response) => { setResult(response); reload() }).catch((err: unknown) => setError(describeApiError(err))).finally(() => setDeploying(false)) }
  const label = result === null || result.outcome === '' ? 'Still resolving' : result.outcome.charAt(0).toUpperCase() + result.outcome.slice(1)
  return <div className="sm-stack-3"><h3 className="sm-subsection__title">Cue catalog</h3>{catalog === null ? <RuledStrip absence={error === null ? 'loading' : 'failed'} label={error === null ? 'Reading' : 'Read failed'} fact={error ?? 'Reading this node’s resolved cue catalog.'} /> : <><p className="sm-small sm-muted">{catalog.configured ? `${catalog.entries.length} entries · ${catalog.acknowledgedStatus.replace('catalog-', '').replace('-', ' ')}` : 'No active-show cue catalog is configured.'}</p><Button disabled={!gate.allowed || deploying || !catalog.configured} title={gate.allowed ? undefined : gate.reason} onClick={deploy}>{deploying ? 'Deploying…' : 'Deploy cue catalog'}</Button></>}{result !== null && <div className="sm-outcome"><StatusPair tone={result.outcome === 'confirmed' ? 'good' : result.outcome === 'unconfirmed' ? 'warn' : result.outcome === '' ? 'pending' : 'bad'} label={label} /><p className="sm-outcome__detail">{result.reason ?? `Catalog revision ${result.revision} was dispatched.`}</p></div>}</div>
}

export function NodeDetail() {
  const { nodeId = '' } = useParams<{ nodeId: string }>()
  const model = useModelContext()
  const navigate = useNavigate()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const node = model.nodes.find((candidate) => candidate.nodeId === nodeId)

  const { state: surfacesState, reload: reloadSurfaces } = useNodeSurfaces(nodeId)
  const { state: manifestState, reload: reloadManifest } = useNodeAssetManifest(nodeId)

  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const renderGate = evaluateScope(model.session, model.sessionFetchFailed, 'render:command')

  const [labelValue, setLabelValue] = useState(node?.label ?? '')
  const [savingLabel, setSavingLabel] = useState(false)
  const [labelError, setLabelError] = useState<string | null>(null)

  useEffect(() => {
    setLabelValue(node?.label ?? '')
    setLabelError(null)
  }, [node?.nodeId, node?.label])

  const [confirmText, setConfirmText] = useState('')
  const [removing, setRemoving] = useState(false)
  const [removeError, setRemoveError] = useState<string | null>(null)

  const [runningDiscovery, setRunningDiscovery] = useState(false)
  const [discoveryError, setDiscoveryError] = useState<string | null>(null)
  const [discoveryNote, setDiscoveryNote] = useState<string | null>(null)

  if (node === undefined) {
    return (
      <>
        <PageTitle title="Node" />
        <BlankingPlate
          absence="empty"
          stamp="Not found"
          eyebrow={`${nodeId} · not found`}
          title="This coordinator has no record of this node"
          detail="No node in the current snapshot carries this id. It may never have reported in, or it may have been removed from inventory."
          actions={
            <Link className="sm-btn sm-btn--primary" to="/monitor/fleet">
              Back to Fleet
            </Link>
          }
        />
      </>
    )
  }

  const heard = node.evidence.heartbeat.observedAt ?? node.evidence.hello.observedAt
  const age = ageMs(heard, nowIso)
  const controlLabel = age === null ? CONTROL_WORD[node.controlPlane.state] : `${CONTROL_WORD[node.controlPlane.state]} ${formatDuration(age)}`

  const notReporting = node.controlPlane.state !== 'online'
  const lastWill = node.evidence.lastWill
  // The signal's value is the node's own online flag from its last-will topic,
  // so a departure is a collected record whose value is false, not any record.
  const lastWillCollected = lastWill.state !== 'not_collected'
  const lastWillSaysGone = lastWillCollected && lastWill.value === false

  const nodeSignalRows = signalRows(model, nowIso).filter((row) => row.resourceTo === `/monitor/fleet/node/${node.nodeId}`)
  const signalsLine = signalSummary(nodeSignalRows)

  const hasAudioCapability = node.capabilities.some((capability) => capability.id.startsWith('audio.'))
  const countNotNamesCapability = node.capabilities.find(reportsCountNotNames)

  const saveLabel = () => {
    setSavingLabel(true)
    setLabelError(null)
    declareNode(node.nodeId, labelValue, node.declaration.notes ?? '')
      .then(() => setSavingLabel(false))
      .catch((err: unknown) => {
        setLabelError(describeApiError(err))
        setSavingLabel(false)
      })
  }

  const runDiscoveryClick = () => {
    setRunningDiscovery(true)
    setDiscoveryError(null)
    setDiscoveryNote(null)
    runDiscovery()
      .then((response) => {
        setRunningDiscovery(false)
        setDiscoveryNote(`Requested discovery run ${response.run.id}.`)
      })
      .catch((err: unknown) => {
        setRunningDiscovery(false)
        setDiscoveryError(describeApiError(err))
      })
  }

  const remove = () => {
    setRemoving(true)
    setRemoveError(null)
    deleteNodeDeclaration(node.nodeId)
      .then(() => navigate('/monitor/fleet'))
      .catch((err: unknown) => {
        setRemoving(false)
        setRemoveError(describeApiError(err))
      })
  }

  const manifestData = manifestState.kind === 'loading' ? null : manifestState.manifest
  const surfaces = surfacesState.kind === 'loading' ? [] : surfacesState.surfaces
  const canRemove = confirmText === node.nodeId && gate.allowed && !removing
  const orphanText =
    surfacesState.kind !== 'loading' && surfaces.length > 0
      ? ` and leaves ${surfaces.map((surface) => surface.payload.name).join(', ')} rendering nowhere until ${
          surfaces.length === 1 ? 'it is' : 'they are'
        } pointed at another node`
      : ''

  return (
    <div className="sm-node-detail">
      <p className="sm-small sm-muted">
        <Link to="/monitor/fleet" className="sm-muted">
          Monitor
        </Link>{' '}
        <span className="sm-faint">/</span>{' '}
        <Link to="/monitor/fleet" className="sm-muted">
          Fleet
        </Link>{' '}
        <span className="sm-faint">/</span> {node.nodeId}
      </p>

      <div className="sm-page__head">
        <div>
          <div className="sm-inline-row">
            <h1 className="sm-page__title">{node.label ?? node.nodeId}</h1>
            <StatusPair tone={CONTROL_TONE[node.controlPlane.state]} label={controlLabel} />
          </div>
          <p className="sm-page__lede">Everything configured about this box, in one place.</p>
        </div>
        <ButtonRow>
          <Button onClick={runDiscoveryClick} disabled={runningDiscovery || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
            {runningDiscovery ? 'Running…' : 'Run discovery'}
          </Button>
        </ButtonRow>
      </div>
      {discoveryError !== null && <RuledStrip absence="failed" label="Refused" fact={discoveryError} />}
      {discoveryNote !== null && <p className="sm-small sm-muted">{discoveryNote}</p>}

      {notReporting && (
        <RuledStrip
          absence="failed"
          label="Not reporting"
          fact={
            lastWillSaysGone
              ? `Last will received at ${formatClock(lastWill.observedAt) ?? 'an unrecorded time'}, so the agent went away rather than the network dropping.`
              : lastWillCollected
                ? 'This node is not reporting. Its last-will record still says the agent was online, so nothing here explains the absence.'
                : 'This node is not reporting. No last will was received, so nothing here distinguishes the agent going away from the network dropping.'
          }
          detail="Everything below is its last known state: configuration is still stored and still valid."
        />
      )}

      <Section id="nd-ident" title="Identity">
        <DefinitionStrip
          items={[
            { term: 'Node id', value: <span className="sm-data">{node.nodeId}</span> },
            {
              term: 'Label',
              value: (
                <div className="sm-inline-row">
                  <Input
                    aria-label="Label"
                    value={labelValue}
                    onChange={(e) => setLabelValue(e.target.value)}
                    style={{ maxWidth: 320 }}
                  />
                  <Button
                    onClick={saveLabel}
                    disabled={savingLabel || labelValue === (node.label ?? '') || !gate.allowed}
                    title={gate.allowed ? undefined : gate.reason}
                  >
                    {savingLabel ? 'Saving…' : 'Save label'}
                  </Button>
                </div>
              ),
              detail: labelError !== null ? <span className="sm-field__error">{labelError}</span> : undefined,
            },
            {
              term: 'Declared',
              value: node.declaration.declared
                ? `${formatDateClock(node.declaration.declaredAt) ?? 'an unrecorded time'} by ${node.declaration.declaredByPrincipalName ?? 'an unknown principal'}`
                : 'Not declared',
            },
            {
              term: 'Agent build',
              value: <span className="sm-data">{node.agentVersion ?? 'not reported'}</span>,
              detail: 'As of its last report, not now.',
            },
            {
              term: 'Signals',
              value: (
                <>
                  <Link to="/monitor/signals">{nodeSignalRows.length} signals</Link>
                </>
              ),
              detail: signalsLine,
            },
          ]}
        />
      </Section>

      <Section
        id="nd-caps"
        title="Capabilities"
        aside={<span className="sm-small sm-muted">What the node says it can do · not what we assume</span>}
      >
        {node.capabilities.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="This node advertises no capability." />
        ) : (
          <TableWrap label="Capabilities, scrollable">
            <Table minWidth={520}>
              <thead>
                <tr>
                  <th scope="col">Capability</th>
                  <th scope="col">Ver</th>
                  <th scope="col">Advertised</th>
                </tr>
              </thead>
              <tbody>
                {node.capabilities.map((capability) => (
                  <tr key={capability.id}>
                    <td className="sm-data">{capability.id}</td>
                    <td className="sm-data sm-small sm-muted">v{capability.version}</td>
                    <td className="sm-small sm-muted">{advertisedText(capability)}</td>
                  </tr>
                ))}
              </tbody>
            </Table>
          </TableWrap>
        )}
        {(!hasAudioCapability || countNotNamesCapability !== undefined) && (
          <p className="sm-section__footnote">
            {!hasAudioCapability && (
              <>
                This node advertises no <span className="sm-data">audio.*</span> capability, so it has no audio routing to configure, which is
                different from an audio path that is failing.{' '}
              </>
            )}
            {countNotNamesCapability !== undefined && (
              <>
                <span className="sm-data">{countNotNamesCapability.id}</span> reports a count but no names, which is why the related field has to
                be typed by hand.
              </>
            )}
          </p>
        )}
        <CueCatalogControls nodeId={node.nodeId} gate={gate} />
      </Section>

      <Section
        id="nd-surf"
        title="Surfaces on this node"
        aside={<span className="sm-small sm-muted">Authored per show, not per node</span>}
        detail="A surface belongs to a show, so this list is a view across shows rather than something you configure here. Editing one opens it in that show."
      >
        {surfacesState.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this node's assigned surfaces." />
        ) : surfacesState.kind === 'failed' && surfacesState.surfaces.length === 0 ? (
          <RuledStrip
            absence="failed"
            label="Read failed"
            fact={surfacesState.reason}
            detail={
              <button type="button" className="sm-linkbutton" onClick={reloadSurfaces}>
                Try again
              </button>
            }
          />
        ) : (
          <>
            {surfacesState.kind === 'failed' && (
              <RuledStrip absence="stale" label="Stale" fact={surfacesState.reason} detail="Showing the surfaces last read." />
            )}
            {surfaces.length === 0 ? (
              <RuledStrip absence="empty" label="None" fact="No surface is assigned to this node." />
            ) : (
              <TableWrap label="Surfaces on this node, scrollable">
                <Table minWidth={540}>
                  <thead>
                    <tr>
                      <th scope="col">Surface</th>
                      <th scope="col">Show</th>
                      <th scope="col">Rendering</th>
                    </tr>
                  </thead>
                  <tbody>
                    {surfaces.map((surface) => {
                      const status = surfaceRenderStatus(model.nodes, surface.payload.node, surface.id, nowIso)
                      const display = surfaceDisplay(surface)
                      const end = surface.payload.channelRange.startChannel + surface.payload.channelRange.channelCount - 1
                      return (
                        <SelectableRow key={surface.id} onActivate={() => navigate(`/shows/${encodeURIComponent(surface.payload.show)}/presentation?surface=${encodeURIComponent(surface.id)}`)} ariaLabel={`Edit ${surface.payload.name}`}>
                          <td>
                            <strong>{surface.payload.name}</strong>
                            <br />
                            <span className="sm-data sm-small sm-faint">
                              {surface.payload.geometry.width}×{surface.payload.geometry.height} {surface.payload.geometry.pixelFormat} ·{' '}
                              {display !== '' ? display : surface.payload.output.transport.toUpperCase()} · ch{' '}
                              {surface.payload.channelRange.startChannel.toLocaleString()}–{end.toLocaleString()}
                            </span>
                          </td>
                          <td className="sm-small sm-muted">{surface.payload.show}</td>
                          <td>
                            <StatusPair tone={status.tone} label={status.label} />
                            <RenderSurfaceControls nodeId={node.nodeId} surfaceId={surface.id} gate={renderGate} />
                          </td>
                        </SelectableRow>
                      )
                    })}
                  </tbody>
                </Table>
              </TableWrap>
            )}
          </>
        )}
      </Section>

      <Section id="nd-assets" title="Assets held locally">
        <p className="sm-small sm-muted">
          This reports what is missing, uncovered or unexpected, not what this node correctly holds, since the manifest reports absence, never
          presence.
        </p>

        {manifestState.kind === 'loading' && (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this node's asset manifest." />
        )}
        {manifestState.kind === 'failed' && manifestState.manifest === null && (
          <RuledStrip
            absence="failed"
            label="Read failed"
            fact={manifestState.reason}
            detail={
              <>
                No manifest read has ever succeeded on this device.{' '}
                <button type="button" className="sm-linkbutton" onClick={reloadManifest}>
                  Try again
                </button>
              </>
            }
          />
        )}
        {manifestData !== null && (
          <>
            {manifestState.kind === 'failed' && (
              <RuledStrip
                absence="stale"
                label="Stale"
                fact={manifestState.reason}
                detail={`Showing the manifest last read at ${
                  manifestState.receivedAt === null ? 'an unrecorded time' : (formatClock(new Date(manifestState.receivedAt).toISOString()) ?? 'an unrecorded time')
                }.`}
              />
            )}
            <p className="sm-small sm-muted">
              <StatusPair tone={MANIFEST_TONE[manifestData.state]} label={MANIFEST_LABEL[manifestData.state]} />
              {manifestData.reason !== null && ` · ${manifestData.reason}`}
            </p>
            {manifestData.observedAt === null ? (
              <RuledStrip absence="unobserved" label="Never observed" fact="This node's asset manifest has never been read." />
            ) : (
              <p className="sm-small sm-muted">Observed {formatClock(manifestData.observedAt) ?? 'at an unrecorded time'}.</p>
            )}

            <h3 className="sm-subsection__title">Missing</h3>
            {manifestData.missing.length === 0 ? (
              <ManifestEmptyRow manifest={manifestData} fact="Nothing this node should hold is missing." />
            ) : (
              <TableWrap label="Missing assets, scrollable">
                <Table minWidth={540}>
                  <thead>
                    <tr>
                      <th scope="col">Sequence</th>
                      <th scope="col">Filename</th>
                      <th scope="col">Hash</th>
                      <th scope="col">Size</th>
                      <th scope="col">State</th>
                    </tr>
                  </thead>
                  <tbody>
                    {manifestData.missing.map((asset) => (
                      <tr key={asset.assetId}>
                        <td className="sm-data">{asset.sequence}</td>
                        <td>{asset.filename}</td>
                        <td className="sm-data sm-small sm-muted">{hashLabel(asset.contentHash)}</td>
                        <td className="sm-data sm-small sm-muted" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</td>
                        <td>
                          <StatusPair tone="bad" label="Not held" />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </TableWrap>
            )}

            <h3 className="sm-subsection__title">Gaps</h3>
            {manifestData.gaps.length === 0 ? (
              <ManifestEmptyRow manifest={manifestData} fact="No sequence the active show holds an asset for is uncovered." />
            ) : (
              <TableWrap label="Uncovered sequences, scrollable">
                <Table minWidth={520}>
                  <thead>
                    <tr>
                      <th scope="col">Sequence</th>
                      <th scope="col">Uncovered surfaces</th>
                    </tr>
                  </thead>
                  <tbody>
                    {manifestData.gaps.map((gap) => (
                      <tr key={gap.sequence}>
                        <td className="sm-data">{gap.sequence}</td>
                        <td className="sm-small sm-muted">{gap.surfaces.join(', ')}</td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </TableWrap>
            )}

            <h3 className="sm-subsection__title">Extra</h3>
            {manifestData.extra.length === 0 ? (
              <ManifestEmptyRow manifest={manifestData} fact="This node holds nothing it was not expected to." />
            ) : (
              <TableWrap label="Extra assets, scrollable">
                <Table minWidth={540}>
                  <thead>
                    <tr>
                      <th scope="col">Filename</th>
                      <th scope="col">Hash</th>
                      <th scope="col">Size</th>
                      <th scope="col">Note</th>
                    </tr>
                  </thead>
                  <tbody>
                    {manifestData.extra.map((asset) => (
                      <tr key={asset.contentHash}>
                        <td>{asset.filename}</td>
                        <td className="sm-data sm-small sm-muted">{hashLabel(asset.contentHash)}</td>
                        <td className="sm-data sm-small sm-muted" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</td>
                        <td className="sm-small sm-muted">Never an error and never a basis for deletion.</td>
                      </tr>
                    ))}
                  </tbody>
                </Table>
              </TableWrap>
            )}

            {node.controlPlane.state === 'offline' && (
              <p className="sm-section__footnote">Sync cannot run while the node is offline.</p>
            )}
            <NotWiredBanner what="Re-syncing every asset on this node" missing="way to trigger an asset sync to a node" />
            <ButtonRow>
              <NotWired>
                <Button>Re-sync all</Button>
              </NotWired>
            </ButtonRow>
          </>
        )}
      </Section>

      <Section id="nd-danger" title="Remove this node">
        <div className="sm-panel">
          <p className="sm-small sm-muted">
            Undeclaring drops this node from inventory{orphanText}. Files already on the node&rsquo;s disk are not removed.
          </p>
          <Field label={`Type ${node.nodeId} to confirm`} help="Asks for the node id before it proceeds. This is not the unsaved-changes dialog.">
            {(props) => <Input {...props} value={confirmText} onChange={(e) => setConfirmText(e.target.value)} />}
          </Field>
          {removeError !== null && <RuledStrip absence="failed" label="Refused" fact={removeError} />}
          <ButtonRow>
            <Button
              variant="danger"
              disabled={!canRemove}
              title={!gate.allowed ? gate.reason : confirmText !== node.nodeId ? 'Type the node id exactly to enable this.' : undefined}
              onClick={remove}
            >
              {removing ? 'Removing…' : 'Remove declaration'}
            </Button>
          </ButtonRow>
        </div>
      </Section>
    </div>
  )
}
