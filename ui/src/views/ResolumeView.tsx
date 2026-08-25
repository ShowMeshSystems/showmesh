import { useEffect, useRef, useState } from 'react'
import { getResolumeRecovery, listResolumeActions, restoreResolumeRecovery } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../app/session'
import { findObservation } from '../app/fppSignals'
import {
  ambiguousClips,
  resolumeObservationLabel,
  sanitizeResolumeEvidence,
  sanitizeResolumeValueString,
} from '../app/resolumeComposition'
import { resolumeCompositionOrNull, useResolumeComposition } from '../app/useResolumeComposition'
import type {
  ResolumeAction,
  ResolumeCompositionResponse,
  ResolumeRecoveryResponse,
  ResolumeRecoveryRestoreReport,
} from '../app/types'
import { EvidenceValue } from '../components/EvidenceValue'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { ScopedButton } from '../components/ScopedButton'
import { ResolumeHealthBadge, ResolumeRecoveryLayerStateBadge, ResolumeRestoreResultBadge } from '../components/DomainBadges'
import { ResolumeActionController } from '../components/ResolumeActionController'
import { ResolumeCompositionUpload } from '../components/ResolumeCompositionUpload'

// Build contract §2.2/§2.3/§2.4: the Resolume detail/control view. Four
// things per §2.2 (the observation list, the composition inventory, the
// ambiguous clips, the crash-recovery record) plus the controller page
// (§2.3) on the same route, since the spec places it "on the Resolume
// view" rather than a route of its own.

type ActionsState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; actions: ResolumeAction[] }

function useResolumeActions(): ActionsState {
  const [state, setState] = useState<ActionsState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    listResolumeActions()
      .then((resp) => {
        if (!cancelled) setState({ kind: 'loaded', actions: resp.actions })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])
  return state
}

type RecoveryState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; recovery: ResolumeRecoveryResponse }

function RestoreReportView({
  report,
  composition,
}: {
  report: ResolumeRecoveryRestoreReport
  composition: ResolumeCompositionResponse | null
}) {
  return (
    <div className="panel" role="status">
      <dl className="field-list">
        <dt>Trigger</dt>
        <dd>{report.trigger}</dd>
        <dt>Outcome</dt>
        <dd>{report.outcome}</dd>
        <dt>Principal</dt>
        <dd>{report.principal}</dd>
        <dt>Omitted layers</dt>
        <dd>{report.omittedLayerCount}</dd>
      </dl>
      <table className="config-table">
        <thead>
          <tr>
            <th>Layer</th>
            <th>Result</th>
            <th>Clip</th>
            <th>Action outcome</th>
            <th>Reason</th>
          </tr>
        </thead>
        <tbody>
          {report.layers.map((layer, i) => (
            <tr key={`${layer.layer}-${i}`}>
              <td>
                {layer.layer}
                {layer.layerNameGenerated ? ' (generated)' : ''}
              </td>
              <td>
                <ResolumeRestoreResultBadge result={layer.result} />
              </td>
              <td>{layer.clip ?? '—'}</td>
              <td>{layer.actionOutcome ?? '—'}</td>
              {/* Review finding 3: reason is server-built free text and can
                  embed a raw Arena object id — sanitize before rendering. */}
              <td>{layer.reason === undefined ? '—' : sanitizeResolumeValueString(layer.reason, composition)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

export function ResolumeView() {
  const model = useModelContext()
  const connected = model.connection.kind === 'live'
  const instance = model.resolume[0]
  const reachable = instance === undefined ? undefined : findObservation(instance.observations, 'resolume.reachable')
  const actionsState = useResolumeActions()
  const compositionState = useResolumeComposition(instance?.composition?.name ?? null)
  const composition = resolumeCompositionOrNull(compositionState)

  const [recoveryState, setRecoveryState] = useState<RecoveryState>({ kind: 'loading' })
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const [restoring, setRestoring] = useState(false)
  const [restoreError, setRestoreError] = useState<string | null>(null)
  const restoringRef = useRef(false)

  useEffect(() => {
    let cancelled = false
    setRecoveryState({ kind: 'loading' })
    getResolumeRecovery()
      .then((resp) => {
        if (!cancelled) setRecoveryState({ kind: 'loaded', recovery: resp })
      })
      .catch((err: unknown) => {
        if (!cancelled) setRecoveryState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [reloadGeneration])

  async function handleRestoreNow(): Promise<void> {
    if (restoringRef.current) return
    restoringRef.current = true
    setRestoring(true)
    setRestoreError(null)
    try {
      await restoreResolumeRecovery()
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setRestoreError(describeApiError(err))
    } finally {
      restoringRef.current = false
      setRestoring(false)
    }
  }

  const ambiguous = ambiguousClips(composition)

  return (
    <div>
      <h2 className="panel__title">Resolume</h2>

      {instance === undefined ? (
        <p className="text-muted" role="status">
          Resolume is not configured on this coordinator.
        </p>
      ) : (
        <>
          <PanelErrorBoundary panelLabel="Resolume instance summary">
            <section className="panel">
              <div style={{ marginBottom: '0.75rem' }}>
                <ResolumeHealthBadge health={instance.health} />
              </div>
              <dl className="field-list">
                <dt>Instance</dt>
                <dd>{instance.instanceId}</dd>
                <dt>Loaded composition</dt>
                <dd>{instance.composition === null ? 'no composition uploaded' : instance.composition.name}</dd>
              </dl>
              {reachable !== undefined && (
                <EvidenceValue
                  label="reachable"
                  evidence={reachable}
                  serverTime={model.serverTime}
                  serverTimeReceivedAt={model.serverTimeReceivedAt}
                  connected={connected}
                />
              )}
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Observations</h3>
          <PanelErrorBoundary panelLabel="Resolume observations">
            <section className="panel">
              {instance.observations.length === 0 ? (
                <p className="text-muted">This instance has no recorded observations.</p>
              ) : (
                instance.observations.map((observation) => (
                  <EvidenceValue
                    key={observation.signal}
                    label={resolumeObservationLabel(observation.signal, composition)}
                    evidence={sanitizeResolumeEvidence(observation, composition)}
                    serverTime={model.serverTime}
                    serverTimeReceivedAt={model.serverTimeReceivedAt}
                    connected={connected}
                  />
                ))
              )}
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Composition inventory</h3>
          <PanelErrorBoundary panelLabel="Composition inventory">
            <section className="panel">
              {compositionState.kind === 'loading' && <p className="text-muted">Loading composition…</p>}
              {compositionState.kind === 'not_stored' && (
                <p className="text-muted" role="status">
                  {compositionState.reason}
                </p>
              )}
              {compositionState.kind === 'forbidden' && (
                <p className="panel panel--warning" role="alert">
                  {compositionState.reason}
                </p>
              )}
              {compositionState.kind === 'unauthorized' && (
                <p className="panel panel--warning" role="alert">
                  {compositionState.reason}
                </p>
              )}
              {compositionState.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {compositionState.message}
                </p>
              )}
              {composition !== null && (
                <>
                  <h4 className="panel__title">Decks</h4>
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Closed</th>
                        <th>Clips</th>
                      </tr>
                    </thead>
                    <tbody>
                      {composition.decks.map((deck) => (
                        <tr key={deck.id}>
                          <td>
                            {deck.name}
                            {deck.nameGenerated ? ' (generated)' : ''}
                          </td>
                          <td>{deck.closed ? 'yes' : 'no'}</td>
                          <td>{deck.clipCount}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>

                  <h4 className="panel__title">Layers</h4>
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Name</th>
                      </tr>
                    </thead>
                    <tbody>
                      {composition.layers.map((layer) => (
                        <tr key={layer.id}>
                          <td>
                            {layer.name}
                            {layer.nameGenerated ? ' (generated)' : ''}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>

                  <h4 className="panel__title">Columns</h4>
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Deck</th>
                      </tr>
                    </thead>
                    <tbody>
                      {composition.columns.map((column) => (
                        <tr key={column.id}>
                          <td>
                            {column.name}
                            {column.nameGenerated ? ' (generated)' : ''}
                          </td>
                          <td>{composition.decks.find((d) => d.id === column.deckId)?.name ?? 'unknown deck'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>

                  <h4 className="panel__title">Clips</h4>
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Name</th>
                        <th>Deck</th>
                        <th>Ambiguous</th>
                      </tr>
                    </thead>
                    <tbody>
                      {[...composition.clips, ...composition.persistentClips].map((clip) => (
                        <tr key={clip.id}>
                          <td>
                            {clip.name}
                            {clip.nameGenerated ? ' (generated)' : ''}
                          </td>
                          <td>
                            {clip.deckId === undefined
                              ? 'persistent'
                              : (composition.decks.find((d) => d.id === clip.deckId)?.name ?? 'unknown deck')}
                          </td>
                          <td>{clip.ambiguous ? 'yes, see below' : 'no'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </>
              )}
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Ambiguous clips</h3>
          <PanelErrorBoundary panelLabel="Ambiguous clips">
            <section className="panel">
              {/* Review finding 4: ambiguousClips(null) is [] just like a
                  genuinely empty composition, so an absence claim here must
                  gate on compositionState.kind — the same states the
                  inventory panel above already handles — never on
                  `ambiguous.length === 0` alone, which is also true while
                  loading, on a 403/401, or before anything is uploaded. */}
              {compositionState.kind === 'loading' && <p className="text-muted">Loading…</p>}
              {compositionState.kind === 'not_stored' && (
                <p className="text-muted" role="status">
                  {compositionState.reason}
                </p>
              )}
              {compositionState.kind === 'forbidden' && (
                <p className="panel panel--warning" role="alert">
                  {compositionState.reason}
                </p>
              )}
              {compositionState.kind === 'unauthorized' && (
                <p className="panel panel--warning" role="alert">
                  {compositionState.reason}
                </p>
              )}
              {compositionState.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {compositionState.message}
                </p>
              )}
              {compositionState.kind === 'loaded' &&
                (ambiguous.length === 0 ? (
                  <p className="text-muted">
                    No ambiguous clips in the stored composition: every clip's (deck-or-persistent,
                    layer, label) triple is unique and can be resolved by name.
                  </p>
                ) : (
                  <>
                    <p className="text-muted" role="status">
                      These clips share the same layer and label as another clip, so no reference,
                      including one naming their layer, can ever resolve them. Rename one of each
                      colliding pair in Resolume and re-upload the composition file.
                    </p>
                    <table className="config-table">
                      <thead>
                        <tr>
                          <th>Name</th>
                          <th>Layer</th>
                          <th>Deck</th>
                        </tr>
                      </thead>
                      <tbody>
                        {ambiguous.map((clip) => (
                          <tr key={clip.id}>
                            <td>{clip.name}</td>
                            <td>{clip.layerName}</td>
                            <td>{clip.deckName ?? 'persistent'}</td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </>
                ))}
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Crash recovery</h3>
          <PanelErrorBoundary panelLabel="Crash recovery record">
            <section className="panel">
              {recoveryState.kind === 'loading' && <p className="text-muted">Loading…</p>}
              {recoveryState.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {recoveryState.message}
                </p>
              )}
              {recoveryState.kind === 'loaded' && (
                <>
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Layer</th>
                        <th>State</th>
                        <th>Clip</th>
                        <th>Established</th>
                        <th>Source</th>
                      </tr>
                    </thead>
                    <tbody>
                      {recoveryState.recovery.record.map((entry, i) => (
                        <tr key={`${entry.layer}-${i}`}>
                          <td>
                            {entry.layer}
                            {entry.layerNameGenerated ? ' (generated)' : ''}
                          </td>
                          <td>
                            {/* A layer whose state is "unknown" renders its
                                OWN badge plus its reason below — never dark
                                or blank (D-3a criterion 14). */}
                            <ResolumeRecoveryLayerStateBadge state={entry.state} />
                            {entry.state === 'unknown' && entry.reason !== undefined && (
                              <div className="evidence__reason">
                                {sanitizeResolumeValueString(entry.reason, composition)}
                              </div>
                            )}
                          </td>
                          <td>{entry.clip ?? '—'}</td>
                          <td>{entry.establishedAt ?? 'never established'}</td>
                          <td>{entry.source ?? '—'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>

                  <h4 className="panel__title">Last restore</h4>
                  {recoveryState.recovery.lastRestore === null ? (
                    <p className="text-muted">No restore has run yet.</p>
                  ) : (
                    <RestoreReportView report={recoveryState.recovery.lastRestore} composition={composition} />
                  )}

                  {restoreError !== null && (
                    <p role="alert" className="session-form__error">
                      {restoreError}
                    </p>
                  )}
                  <ScopedButton
                    requiredScope="resolume:action"
                    onClick={() => void handleRestoreNow()}
                    busy={restoring}
                    busyReason="A restore may dispatch a sequential command per layer and can take a while…"
                  >
                    {restoring ? 'Restoring…' : 'Restore now'}
                  </ScopedButton>
                </>
              )}
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Controller</h3>
          <PanelErrorBoundary panelLabel="Resolume controller">
            <section className="panel">
              {actionsState.kind === 'loading' && <p className="text-muted">Loading actions…</p>}
              {actionsState.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {actionsState.message}
                </p>
              )}
              {actionsState.kind === 'loaded' && (
                <ResolumeActionController actions={actionsState.actions} composition={composition} />
              )}
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Composition file</h3>
          <PanelErrorBoundary panelLabel="Resolume composition upload">
            <section className="panel">
              <ResolumeCompositionUpload />
            </section>
          </PanelErrorBoundary>
        </>
      )}
    </div>
  )
}
