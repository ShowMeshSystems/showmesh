import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import {
  ApiError,
  getResolumeComposition,
  getResolumeInstancesConfig,
  getResolumeRecovery,
  getResolumeRecoveryConfig,
  getResolumeRecoveryConfigRevisions,
  putResolumeRecoveryConfig,
  restoreResolumeRecovery,
  uploadResolumeComposition,
  type ResolumeCompositionResponse,
  type ResolumeRecoveryConfigResponse,
  type ResolumeRecoveryResponse,
  type UploadProgress,
} from '../api'
import {
  BlankingPlate,
  Button,
  ButtonRow,
  DefinitionStrip,
  NotWired,
  NotWiredBanner,
  RevisionHistory,
  RuledStrip,
  Section,
  Segmented,
  StatusPair,
  Table,
  TableWrap,
  type Tone,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { ageMs, effectiveServerTimeIso, formatClock, formatDateClock, formatDuration } from '../domain/time'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { formatBytes, hashLabel } from './showsModel'
import {
  actionOutcomeDescription,
  ambiguousClips,
  columnForClip,
  deckForClip,
  describeIdentified,
  findResolumeInstance,
  findSignal,
  formatDurationAgo,
  groupLayerReadiness,
  lastAnswerAgeMs,
  layerForClip,
  layerName,
  RESOLUME_HEALTH_LABEL,
  RESOLUME_HEALTH_TONE,
  RESTORE_OUTCOME_LABEL,
  RESTORE_OUTCOME_TONE,
  RESTORE_RESULT_LABEL,
  RESTORE_RESULT_TONE,
  restoreSummary,
} from './resolumeModel'

type CompositionState =
  | { kind: 'loading' }
  | { kind: 'none' }
  | { kind: 'loaded'; response: ResolumeCompositionResponse }
  | { kind: 'failed'; reason: string }

function useResolumeComposition(): { state: CompositionState; reload: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<CompositionState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getResolumeComposition()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'none' })
          return
        }
        setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  return { state, reload: () => setAttempt((n) => n + 1) }
}

type RecoveryState = { kind: 'loading' } | { kind: 'loaded'; response: ResolumeRecoveryResponse } | { kind: 'failed'; reason: string }

/** `GET /resolume/recovery` is an open read (no scope), unlike the config it writes through. */
function useResolumeRecovery(): { state: RecoveryState; reload: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<RecoveryState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getResolumeRecovery()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  return { state, reload: () => setAttempt((n) => n + 1) }
}

type AddressState = { kind: 'loading' } | { kind: 'found'; url: string } | { kind: 'unavailable' }

/** `GET /config/resolume.instances` needs `config:write`; a device without it simply does not learn the address. */
function useResolumeAddress(instanceId: string, gateAllowed: boolean): AddressState {
  const [state, setState] = useState<AddressState>({ kind: 'loading' })

  useEffect(() => {
    if (!gateAllowed) {
      setState({ kind: 'unavailable' })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    getResolumeInstancesConfig()
      .then((response) => {
        if (cancelled) return
        const match = response.payload.instances.find((instance) => instance.id === instanceId)
        setState(match === undefined ? { kind: 'unavailable' } : { kind: 'found', url: match.url })
      })
      .catch(() => {
        if (!cancelled) setState({ kind: 'unavailable' })
      })
    return () => {
      cancelled = true
    }
  }, [instanceId, gateAllowed])

  return state
}

const RECORD_TONE = { clip: 'good', dark: 'pending', unknown: 'unknown' } as const
const RECORD_LABEL = { clip: 'Clip connected', dark: 'Dark', unknown: 'Unknown' } as const

/** A labelled-pair fact row: the state word and its colour together (guide §4), never colour alone. */
function ObservationRow({ tone, label, fact, detail }: { tone: Tone; label: string; fact: ReactNode; detail?: ReactNode }) {
  return (
    <div className="sm-strip">
      <StatusPair tone={tone} label={label} />
      <div>
        <p className="sm-strip__fact">{fact}</p>
        {detail !== undefined && <p className="sm-strip__detail">{detail}</p>}
      </div>
    </div>
  )
}

export function ResolumeConfig() {
  const { instanceId = '' } = useParams<{ instanceId: string }>()
  const model = useModelContext()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const instance = findResolumeInstance(model, instanceId)
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const actionGate = evaluateScope(model.session, model.sessionFetchFailed, 'resolume:action')

  const { state: compositionState, reload: reloadComposition } = useResolumeComposition()
  const { state: restRecoveryState, reload: reloadRecovery } = useResolumeRecovery()
  // `model.resolumeRecovery` is kept live by `resolumeRecovery.changed`
  // frames (store.ts's applyResolumeRecoveryChanged) and reflects a
  // restore run by the coordinator or another operator without a reload.
  // It stays `null` until the first live frame arrives this connection
  // (Model.resolumeRecovery's own comment), so the REST read above is
  // what establishes ground truth on first load and after a reconnect.
  const recoveryState: RecoveryState =
    model.resolumeRecovery !== null ? { kind: 'loaded', response: model.resolumeRecovery } : restRecoveryState
  const address = useResolumeAddress(instanceId, gate.allowed)

  const [uploading, setUploading] = useState(false)
  const [uploadProgress, setUploadProgress] = useState<UploadProgress | null>(null)
  const [uploadError, setUploadError] = useState<string | null>(null)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const [recoveryConfigState, setRecoveryConfigState] = useState<
    { kind: 'idle' } | { kind: 'loading' } | { kind: 'loaded'; response: ResolumeRecoveryConfigResponse } | { kind: 'failed'; reason: string }
  >({ kind: 'idle' })
  const [recoveryConfigAttempt, setRecoveryConfigAttempt] = useState(0)
  const [pendingEnabled, setPendingEnabled] = useState<boolean | null>(null)
  const [savingRecovery, setSavingRecovery] = useState(false)
  const [saveRecoveryError, setSaveRecoveryError] = useState<string | null>(null)
  const [staleRecovery, setStaleRecovery] = useState<Extract<SaveOutcome<ResolumeRecoveryConfigResponse>, { kind: 'stale' }> | null>(null)
  const [restoring, setRestoring] = useState(false)
  const [restoreError, setRestoreError] = useState<string | null>(null)

  // Anyone can see the toggle's value (the open `GET /resolume/recovery`
  // read); only a `config:write` device also loads the gated config it
  // needs for a revision, so a viewer without that scope still sees it.
  const loadedAutoRestoreEnabled = recoveryState.kind === 'loaded' ? recoveryState.response.autoRestoreEnabled : null
  useEffect(() => {
    if (loadedAutoRestoreEnabled !== null && pendingEnabled === null) {
      setPendingEnabled(loadedAutoRestoreEnabled)
    }
  }, [loadedAutoRestoreEnabled, pendingEnabled])

  useEffect(() => {
    if (!gate.allowed) {
      setRecoveryConfigState({ kind: 'idle' })
      return
    }
    let cancelled = false
    setRecoveryConfigState({ kind: 'loading' })
    getResolumeRecoveryConfig()
      .then((response) => {
        if (cancelled) return
        setRecoveryConfigState({ kind: 'loaded', response })
        setPendingEnabled(response.payload.autoRestoreEnabled)
      })
      .catch((err: unknown) => {
        if (!cancelled) setRecoveryConfigState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [gate.allowed, recoveryConfigAttempt])

  if (instance === null) {
    return (
      <>
        <BlankingPlate
          absence="empty"
          stamp="Not found"
          eyebrow={`${instanceId} · not found`}
          title="This coordinator has no record of this Resolume instance"
          detail="No instance in the current snapshot carries this id. It may never have reported in, or it may no longer be configured."
          actions={
            <Link className="sm-btn sm-btn--primary" to="/monitor/fleet">
              Back to Fleet
            </Link>
          }
        />
      </>
    )
  }

  const age = lastAnswerAgeMs(instance, nowIso)
  const healthLabel = age === null ? RESOLUME_HEALTH_LABEL[instance.health] : `${RESOLUME_HEALTH_LABEL[instance.health]} ${formatDuration(age)}`

  const identifiedSignal = findSignal(instance, 'resolume.composition.identified')
  const selectedDeckSignal = findSignal(instance, 'resolume.composition.selected_deck')
  const compositionNameSignal = findSignal(instance, 'resolume.composition.name')
  const reachableSignal = findSignal(instance, 'resolume.reachable')
  const identifiedVerdict =
    identifiedSignal !== undefined && identifiedSignal.state === 'current' ? describeIdentified(String(identifiedSignal.value)) : null

  const runUpload = (file: File) => {
    setUploading(true)
    setUploadError(null)
    setUploadProgress(null)
    uploadResolumeComposition(file, setUploadProgress)
      .then(() => {
        reloadComposition()
      })
      .catch((err: unknown) => setUploadError(describeApiError(err)))
      .finally(() => {
        setUploading(false)
        setUploadProgress(null)
      })
  }

  const saveRecovery = () => {
    if (recoveryConfigState.kind !== 'loaded' || pendingEnabled === null) return
    setSavingRecovery(true)
    setSaveRecoveryError(null)
    setStaleRecovery(null)
    guardedSave({
      loaded: recoveryConfigState.response,
      read: getResolumeRecoveryConfig,
      write: () => putResolumeRecoveryConfig({ autoRestoreEnabled: pendingEnabled }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setRecoveryConfigState({ kind: 'loaded', response: outcome.response })
          reloadRecovery()
          return
        }
        if (outcome.kind === 'stale') {
          setStaleRecovery(outcome)
          return
        }
        setSaveRecoveryError(outcome.reason)
      })
      .catch((err: unknown) => setSaveRecoveryError(describeApiError(err)))
      .finally(() => setSavingRecovery(false))
  }

  const restoreRecovery = () => {
    setRestoring(true)
    setRestoreError(null)
    restoreResolumeRecovery()
      .then(() => reloadRecovery())
      .catch((err: unknown) => setRestoreError(describeApiError(err)))
      .finally(() => setRestoring(false))
  }

  const recoveryDirty =
    recoveryConfigState.kind === 'loaded' && pendingEnabled !== null && pendingEnabled !== recoveryConfigState.response.payload.autoRestoreEnabled

  return (
    <div className="sm-resolume-config">
      <p className="sm-small sm-muted">
        <Link to="/monitor/fleet" className="sm-muted">
          Monitor
        </Link>{' '}
        <span className="sm-faint">/</span>{' '}
        <Link to="/monitor/fleet" className="sm-muted">
          Fleet
        </Link>{' '}
        <span className="sm-faint">/</span> {instance.instanceId}
      </p>

      <div className="sm-page__head">
        <div>
          <div className="sm-inline-row">
            <h1 className="sm-page__title">{instance.instanceId}</h1>
            <StatusPair tone={RESOLUME_HEALTH_TONE[instance.health]} label={healthLabel} />
          </div>
          <p className="sm-page__lede">
            {address.kind === 'found' ? (
              <>
                Resolume Arena at <span className="sm-data">{address.url}</span>, timecode-driven, never frame-synced by ShowMesh.
              </>
            ) : (
              'Resolume Arena, timecode-driven, never frame-synced by ShowMesh. Its configured address is not available to this device.'
            )}
          </p>
        </div>
        <ButtonRow>
          <NotWired>
            <Button>Test connection</Button>
          </NotWired>
        </ButtonRow>
      </div>
      <NotWiredBanner
        what="Testing this Resolume address"
        missing="way to test a configured Resolume address on demand"
      />

      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(520px, 1fr))', gap: 'var(--s-5)', alignItems: 'start' }}>
        <div>
      <Section id="rz-comp" title="Stored composition" aside={<span className="sm-small sm-muted">Uploaded, not read live</span>}>
        <p className="sm-small sm-muted">
          ShowMesh stores an id map of the composition so actions can name a layer or clip instead of an object id. Re-export and re-upload
          whenever the composition changes in Arena, or names will resolve to the wrong objects.
        </p>

        {compositionState.kind === 'loading' && (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the stored composition." />
        )}
        {compositionState.kind === 'failed' && (
          <RuledStrip
            absence="failed"
            label="Read failed"
            fact={compositionState.reason}
            detail={
              <button type="button" className="sm-linkbutton" onClick={reloadComposition}>
                Try again
              </button>
            }
          />
        )}
        {(compositionState.kind === 'none' || compositionState.kind === 'loaded') && (
          <>
            {compositionState.kind === 'none' && (
              <RuledStrip absence="empty" label="None" fact="No composition has been uploaded to this coordinator yet." />
            )}
            {compositionState.kind === 'loaded' && (
              <div className="sm-panel">
                <div className="sm-inline-row" style={{ justifyContent: 'space-between' }}>
                  <div>
                    <p className="sm-data">{compositionState.response.composition.sourceFilename}</p>
                    <p className="sm-small sm-muted">
                      Activated {formatDateClock(compositionState.response.activatedAt) ?? 'at an unrecorded time'}.
                    </p>
                  </div>
                  <div className="sm-inline-row">
                    {identifiedVerdict !== null ? (
                      <StatusPair tone={identifiedVerdict.tone} label={identifiedVerdict.label} />
                    ) : (
                      <StatusPair tone="unknown" label="Unobserved" />
                    )}
                    <Button onClick={() => fileInputRef.current?.click()} disabled={uploading || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
                      {uploading ? 'Uploading…' : 'Re-upload'}
                    </Button>
                    <input
                      ref={fileInputRef}
                      type="file"
                      className="sm-sr-only"
                      aria-label="Choose a composition file"
                      onChange={(e) => {
                        const file = e.target.files?.[0]
                        e.target.value = ''
                        if (file !== undefined) runUpload(file)
                      }}
                    />
                  </div>
                </div>

                <DefinitionStrip
                  items={[
                    { term: 'Decks', value: String(compositionState.response.decks.length) },
                    { term: 'Layers', value: String(compositionState.response.composition.layerCount) },
                    { term: 'Layer groups', value: String(compositionState.response.composition.layerGroupCount) },
                    { term: 'Columns', value: String(compositionState.response.composition.columnCount) },
                    { term: 'Clips', value: String(compositionState.response.composition.clipCount) },
                    { term: 'Persistent', value: String(compositionState.response.composition.persistentClipCount) },
                  ]}
                />

                <DefinitionStrip
                  items={[
                    { term: 'Name', value: compositionState.response.composition.name },
                    { term: 'Source file', value: <span className="sm-data">{compositionState.response.composition.sourceFilename}</span> },
                    { term: 'Content hash', value: <span className="sm-data sm-small">{hashLabel(compositionState.response.composition.contentHash)}</span> },
                    {
                      term: 'Size',
                      value: (
                        <span title={`${compositionState.response.composition.sizeBytes} bytes`}>
                          {formatBytes(compositionState.response.composition.sizeBytes)}
                        </span>
                      ),
                    },
                    {
                      term: 'Written by',
                      value: `${compositionState.response.composition.writtenBy.product} ${compositionState.response.composition.writtenBy.major}.${compositionState.response.composition.writtenBy.minor}.${compositionState.response.composition.writtenBy.micro} r${compositionState.response.composition.writtenBy.revision}`,
                    },
                  ]}
                />

                {identifiedVerdict !== null ? (
                  <p className="sm-section__footnote">
                    {identifiedVerdict.label === 'Identified' ? (
                      <>
                        Arena currently has deck{' '}
                        {selectedDeckSignal !== undefined && selectedDeckSignal.state === 'current' ? String(selectedDeckSignal.value) : 'an unreported deck'}{' '}
                        selected, which matches the stored map. A different selected deck would report{' '}
                        <span className="sm-data">deck_mismatch</span> rather than guessing.
                      </>
                    ) : (
                      identifiedVerdict.detail
                    )}
                  </p>
                ) : (
                  <p className="sm-section__footnote">Arena has not reported whether the stored map still identifies the current composition.</p>
                )}

                {uploadError !== null && <RuledStrip absence="failed" label="Upload refused" fact={uploadError} />}
                {uploading && (
                  <p className="sm-small sm-muted">
                    {uploadProgress === null
                      ? 'Sending…'
                      : uploadProgress.total === null
                        ? `${formatBytes(uploadProgress.loaded)} sent.`
                        : `${formatBytes(uploadProgress.loaded)} of ${formatBytes(uploadProgress.total)} sent.`}
                  </p>
                )}
              </div>
            )}
            {compositionState.kind === 'none' && (
              <ButtonRow>
                <Button onClick={() => fileInputRef.current?.click()} disabled={uploading || !gate.allowed} title={gate.allowed ? undefined : gate.reason}>
                  {uploading ? 'Uploading…' : 'Upload composition'}
                </Button>
                <input
                  ref={fileInputRef}
                  type="file"
                  className="sm-sr-only"
                  aria-label="Choose a composition file"
                  onChange={(e) => {
                    const file = e.target.files?.[0]
                    e.target.value = ''
                    if (file !== undefined) runUpload(file)
                  }}
                />
                {uploadError !== null && <RuledStrip absence="failed" label="Upload refused" fact={uploadError} />}
              </ButtonRow>
            )}
          </>
        )}
      </Section>

      <Section id="rz-amb" title="Clips that cannot be named">
        {compositionState.kind === 'loaded' ? (
          (() => {
            const composition = compositionState.response
            const ambiguous = ambiguousClips(composition)
            const total = composition.composition.clipCount + composition.composition.persistentClipCount
            return (
              <>
                <p className="sm-small sm-muted">
                  Actions reference clips by name, so two clips sharing a name on the same layer and deck (or two persistent clips sharing a name
                  and layer) cannot be told apart. The coordinator computes this and refuses to guess: an action naming one of these will not
                  resolve.
                </p>
                {ambiguous.length === 0 ? (
                  <RuledStrip absence="empty" label="None" fact={`0 of ${total} clips are ambiguous. Every action naming a clip resolves to exactly one object.`} />
                ) : (
                  <>
                    <StatusPair tone="warn" label={`${ambiguous.length} of ${total}`} />
                    <TableWrap label="Clips that cannot be named, scrollable">
                      <Table minWidth={680}>
                        <thead>
                          <tr>
                            <th scope="col">Clip</th>
                            <th scope="col">Layer</th>
                            <th scope="col">Deck</th>
                            <th scope="col">Clip id</th>
                            <th scope="col">Column</th>
                          </tr>
                        </thead>
                        <tbody>
                          {ambiguous.map(({ clip, persistent }) => {
                            const layer = layerForClip(composition.layers, clip.layerIndex)
                            const deck = deckForClip(composition.decks, clip.deckId)
                            const column = columnForClip(composition.columns, clip.deckId, clip.columnIndex)
                            return (
                              <tr key={clip.id}>
                                <td>{clip.name}</td>
                                <td>
                                  {layer === undefined
                                    ? `Layer index ${clip.layerIndex}`
                                    : layer.nameGenerated
                                      ? `${layer.name} (generated name)`
                                      : layer.name}
                                </td>
                                <td className="sm-small sm-muted">{persistent ? 'Persistent' : (deck?.name ?? 'Unresolved deck')}</td>
                                <td className="sm-data sm-small sm-muted">{clip.id}</td>
                                <td className="sm-small sm-muted">{column?.name ?? `Column index ${clip.columnIndex}`}</td>
                              </tr>
                            )
                          })}
                        </tbody>
                      </Table>
                      <div className="sm-section__footnote">
                        Fix in Arena: rename one of them (the column tells you which is which), then re-upload. A clip sharing a name across{' '}
                        <em>different</em> layers is fine, because the layer name disambiguates it. Clip ids are shown only here, where names cannot
                        do the job.
                      </div>
                    </TableWrap>
                  </>
                )}
              </>
            )
          })()
        ) : compositionState.kind === 'none' ? (
          <RuledStrip absence="empty" label="None" fact="No composition is stored, so there is nothing to check for name collisions." />
        ) : (
          <RuledStrip absence="unobserved" label="Unknown" fact="The stored composition has not been read, so ambiguous clips cannot be listed." />
        )}
      </Section>

      <Section
        id="rz-rec"
        title="Recovery"
        aside={
          recoveryState.kind === 'loaded' && recoveryState.response.resolumeConfigured && pendingEnabled !== null ? (
            <Segmented
              label="Auto-restore"
              value={pendingEnabled ? 'on' : 'off'}
              onChange={(value) => setPendingEnabled(value === 'on')}
              disabled={!gate.allowed || savingRecovery || recoveryConfigState.kind !== 'loaded'}
              options={[
                { value: 'off', label: 'Off' },
                { value: 'on', label: 'On' },
              ]}
            />
          ) : undefined
        }
      >
        <p className="sm-small sm-muted">
          When on, ShowMesh records which clip each layer had connected, so it can put them back after Arena restarts. It restores layers it
          recorded, it does not reconstruct a composition it never saw. Changing this creates a coordinator revision attributed to you.
        </p>

        {recoveryState.kind === 'loading' && <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for recovery state." />}
        {recoveryState.kind === 'failed' && (
          <RuledStrip
            absence="failed"
            label="Read failed"
            fact={recoveryState.reason}
            detail={
              <button type="button" className="sm-linkbutton" onClick={reloadRecovery}>
                Try again
              </button>
            }
          />
        )}
        {recoveryState.kind === 'loaded' && !recoveryState.response.resolumeConfigured && (
          <RuledStrip absence="unavailable" label="Unavailable" fact="Resolume is not configured on this coordinator, so recovery cannot run." />
        )}

        {recoveryState.kind === 'loaded' && recoveryState.response.resolumeConfigured && (
          <>
            {!gate.allowed && (
              <p className="sm-small sm-muted">{gate.reason}</p>
            )}
            <ButtonRow>
              <Button
                disabled={!actionGate.allowed || restoring}
                title={actionGate.allowed ? undefined : actionGate.reason}
                onClick={restoreRecovery}
              >
                {restoring ? 'Restoring…' : 'Run restore now'}
              </Button>
              <Button
                variant="primary"
                onClick={saveRecovery}
                disabled={!recoveryDirty || savingRecovery || !gate.allowed || recoveryConfigState.kind !== 'loaded'}
                title={gate.allowed ? undefined : gate.reason}
              >
                {savingRecovery ? 'Saving…' : 'Save recovery'}
              </Button>
              {recoveryConfigState.kind === 'loaded' && (
                <div className="sm-push-end sm-inline-row sm-stack-3">
                  <RevisionHistory id="st-recovery-rev" fetch={getResolumeRecoveryConfigRevisions} reloadKey={recoveryConfigAttempt} />
                  <span className="sm-small sm-muted">{recoveryDirty ? 'unsaved change' : 'no unsaved changes'}</span>
                </div>
              )}
              {recoveryConfigState.kind === 'failed' && (
                <span className="sm-small sm-muted sm-push-end">{recoveryConfigState.reason}</span>
              )}
            </ButtonRow>
            {staleRecovery !== null && (
              <StaleWriteStrip
                stale={staleRecovery}
                onReload={() => {
                  setStaleRecovery(null)
                  setRecoveryConfigAttempt((n) => n + 1)
                }}
              />
            )}
            {saveRecoveryError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveRecoveryError} />}
            {restoreError !== null && <RuledStrip absence="failed" label="Restore refused" fact={restoreError} />}

            <h3 className="sm-subsection__title">Last record</h3>
            {recoveryState.response.record.length === 0 ? (
              <RuledStrip absence="empty" label="None" fact="Nothing has been recorded yet." />
            ) : (
              <>
                <p className="sm-small sm-muted">
                  {recoveryState.response.record.filter((entry) => entry.state !== 'unknown').length} of {recoveryState.response.record.length}{' '}
                  layers recorded.
                </p>
                <TableWrap label="Recovery record, scrollable">
                  <Table minWidth={520}>
                    <thead>
                      <tr>
                        <th scope="col">Layer</th>
                        <th scope="col">State</th>
                        <th scope="col">Detail</th>
                      </tr>
                    </thead>
                    <tbody>
                      {recoveryState.response.record.map((entry, index) => (
                        <tr key={`${entry.layer}:${index}`}>
                          <td>
                            {entry.layer}
                            {entry.layerNameGenerated ? ' (generated name)' : ''}
                          </td>
                          <td>
                            <StatusPair tone={RECORD_TONE[entry.state]} label={RECORD_LABEL[entry.state]} />
                          </td>
                          <td className="sm-small sm-muted sm-table__wrap">
                            {entry.state === 'clip' &&
                              `${entry.clip ?? 'an unnamed clip'}${entry.clipNameGenerated ? ' (generated name)' : ''}${
                                entry.deck !== undefined ? ` on deck ${entry.deck}` : ''
                              }`}
                            {entry.state === 'dark' && 'No clip was connected on this layer.'}
                            {entry.state === 'unknown' && (entry.reason ?? 'No record could be established.')}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </Table>
                </TableWrap>
              </>
            )}
          </>
        )}
      </Section>
        </div>

        <div>
      <Section id="rz-last-restore" title="Last restore">
        {recoveryState.kind === 'loading' && (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for recovery state." />
        )}
        {recoveryState.kind === 'failed' && (
          <RuledStrip
            absence="failed"
            label="Read failed"
            fact={recoveryState.reason}
            detail={
              <button type="button" className="sm-linkbutton" onClick={reloadRecovery}>
                Try again
              </button>
            }
          />
        )}
        {recoveryState.kind === 'loaded' && !recoveryState.response.resolumeConfigured && (
          <RuledStrip absence="unavailable" label="Unavailable" fact="Resolume is not configured on this coordinator, so recovery cannot run." />
        )}
        {recoveryState.kind === 'loaded' &&
          recoveryState.response.resolumeConfigured &&
          (recoveryState.response.lastRestore === null ? (
            <RuledStrip absence="empty" label="None" fact="No restore has run yet." />
          ) : (
            <RestoreReportSection report={recoveryState.response.lastRestore} />
          ))}
      </Section>

      <ResolumeSignalsSection
        instance={instance}
        reachableSignal={reachableSignal}
        compositionNameSignal={compositionNameSignal}
        compositionLayers={compositionState.kind === 'loaded' ? compositionState.response.layers : []}
        nowIso={nowIso}
      />
        </div>
      </div>
    </div>
  )
}

function RestoreReportSection({ report }: { report: NonNullable<ResolumeRecoveryResponse['lastRestore']> }) {
  return (
    <section aria-labelledby="rz-restore">
      <div className="sm-inline-row" style={{ justifyContent: 'space-between' }}>
        <h3 id="rz-restore" className="sm-subsection__title">
          {formatClock(report.startedAt) ?? 'an unrecorded time'}
        </h3>
        <StatusPair tone={RESTORE_OUTCOME_TONE[report.outcome]} label={RESTORE_OUTCOME_LABEL[report.outcome]} />
      </div>
      <p className="sm-small sm-muted">
        Finished {formatClock(report.finishedAt) ?? 'at an unrecorded time'}. Triggered {report.trigger === 'automatic' ? 'automatically' : 'manually'}
        {report.trigger === 'manual' ? ` by ${report.principal}` : ''}.
      </p>
      <TableWrap label="Last restore, scrollable">
        <Table minWidth={520}>
          <thead>
            <tr>
              <th scope="col">Layer</th>
              <th scope="col">Clip put back</th>
              <th scope="col">Result</th>
            </tr>
          </thead>
          <tbody>
            {report.layers.map((layer, index) => (
              <tr key={`${layer.layer}:${index}`}>
                <td>
                  {layer.layer}
                  {layer.layerNameGenerated ? ' (generated name)' : ''}
                </td>
                <td className="sm-small sm-muted">{layer.clip ?? 'no clip'}</td>
                <td className="sm-table__wrap">
                  <StatusPair tone={RESTORE_RESULT_TONE[layer.result]} label={RESTORE_RESULT_LABEL[layer.result]} />
                  {layer.reason !== undefined && (
                    <>
                      <br />
                      <span className="sm-small sm-faint">{layer.reason}</span>
                    </>
                  )}
                  {layer.actionOutcome !== undefined && (
                    <>
                      <br />
                      <StatusPair {...actionOutcomeDescription(layer.actionOutcome)} />
                    </>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </Table>
        <div className="sm-section__footnote">
          {restoreSummary(report.layers)}
          {report.omittedLayerCount > 0 &&
            ` ${report.omittedLayerCount} more ${report.omittedLayerCount === 1 ? 'layer was' : 'layers were'} not attempted this run. Run the restore again to continue.`}
        </div>
      </TableWrap>
    </section>
  )
}

function ResolumeSignalsSection({
  instance,
  reachableSignal,
  compositionNameSignal,
  compositionLayers,
  nowIso,
}: {
  instance: ReturnType<typeof findResolumeInstance>
  reachableSignal: ReturnType<typeof findSignal>
  compositionNameSignal: ReturnType<typeof findSignal>
  compositionLayers: Parameters<typeof layerName>[0]
  nowIso: string | null
}) {
  if (instance === null) return null
  const readiness = groupLayerReadiness(instance)
  const readyCount = readiness.filter((entry) => entry.ready?.state === 'current' && entry.ready.value === 'ready').length
  const readyTone: Tone = readyCount === 0 ? 'bad' : readyCount === readiness.length ? 'good' : 'warn'

  const showingLine = readiness
    .filter((entry) => entry.activeClip !== undefined && entry.activeClip.state === 'current')
    .map((entry) => {
      const resolved = layerName(compositionLayers, entry.layerId)
      const label = resolved === null ? `Layer ${entry.layerId}` : resolved.generated ? `${resolved.name} (generated name)` : resolved.name
      return `${label} is showing ${String(entry.activeClip?.value)}`
    })
    .join('; ')

  return (
    <Section id="rz-obs" title="What Arena is reporting" aside={<Link to="/monitor/signals">All signals</Link>}>
      {reachableSignal === undefined || reachableSignal.state !== 'current' ? (
        <RuledStrip
          absence={reachableSignal === undefined || reachableSignal.state === 'not_collected' ? 'unobserved' : 'failed'}
          label={reachableSignal === undefined ? 'Unobserved' : 'Not reachable'}
          fact={reachableSignal?.reason ?? 'This instance has never reported whether it is reachable.'}
        />
      ) : (
        <ObservationRow
          tone={reachableSignal.value === true ? 'good' : 'bad'}
          label={reachableSignal.value === true ? 'Reachable' : 'Unreachable'}
          fact={
            reachableSignal.observedAt === null
              ? 'Answered at an unrecorded time.'
              : `Answered ${formatDurationAgo(nowIso === null ? null : ageMs(reachableSignal.observedAt, nowIso))}.`
          }
        />
      )}

      {readiness.length === 0 ? (
        <RuledStrip absence="unobserved" label="Unobserved" fact="No layer readiness has ever been collected from Arena." />
      ) : (
        <ObservationRow
          tone={readyTone}
          label="Layers ready"
          fact={`${readyCount} of ${readiness.length}.`}
          detail={showingLine === '' ? 'No layer currently reports an active clip.' : showingLine}
        />
      )}

      <RuledStrip
        absence="unavailable"
        label="Unavailable"
        fact="The composition's own name cannot be read from Arena."
        detail={compositionNameSignal?.reason ?? 'Composition identity comes from the stored map above, not from a live read.'}
      />

      <RuledStrip
        absence="unavailable"
        label="Unavailable"
        fact="Timecode lock is not something Arena reports, so there is nothing to collect and nothing to retry."
        detail="Only the audio node's side of the LTC path is observable."
      />
    </Section>
  )
}
