import { Link, useParams } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { FPPHealthBadge } from '../components/DomainBadges'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { EvidenceValue } from '../components/EvidenceValue'
import { FPPStopPlaylistControl } from '../components/FPPStopPlaylistControl'
import { FPPStartPlaylistControl } from '../components/FPPStartPlaylistControl'
import { FPPStopPlaylistGracefullyControl } from '../components/FPPStopPlaylistGracefullyControl'
import {
  FPPNextPlaylistItemControl,
  FPPPausePlaylistControl,
  FPPPrevPlaylistItemControl,
  FPPResumePlaylistControl,
} from '../components/FPPPlaylistTransportControls'
import { FPPSetVolumeControl } from '../components/FPPSetVolumeControl'
import { FPPResetObservationSequenceControl } from '../components/FPPResetObservationSequenceControl'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { PortGrid } from '../components/PortGrid'
import { formatAbsolute } from '../app/time'
import { findObservation, groupFppObservations } from '../app/fppSignals'

// FPP instance detail. Not named in spec section 6.4's view list, which
// enumerates Dashboard/Nodes/Capabilities/Events, but OBSERVABILITY
// section 6.1 requires that "every aggregate health indicator must allow
// drill-down to its contributing evidence," and an FPPInstance's `health`
// is exactly such an aggregate over its `observations` list -- this view
// is that drill-down, in the same shape as the node detail view.
export function FPPDetail() {
  const { instanceId } = useParams<{ instanceId: string }>()
  const model = useModelContext()
  const instance = model.fpp.find((candidate) => candidate.instanceId === instanceId)
  const connected = model.connection.kind === 'live'

  // Grouped once per render, from whatever observations actually arrived
  // (spec section 6 "Grouping"). "ports" is pulled out and rendered by
  // PortGrid, not the generic per-signal list, because the point of
  // grouping is to make a K16-Max's 48 port elements scannable instead of
  // repeating the flat-list problem inside its own group.
  const groups = instance ? groupFppObservations(instance.observations) : []
  const portsGroup = groups.find((group) => group.id === 'ports')
  const otherGroups = groups.filter((group) => group.id !== 'ports')

  // Surfaced prominently, per spec section 6 "Warnings" -- but never
  // recomputed into a verdict of this UI's own, and never allowed to
  // colour instance.health, which stays exactly what the coordinator
  // sent (FPPHealthBadge above, untouched by anything below).
  const warningsSummary = instance ? findObservation(instance.observations, 'fpp.warnings.summary') : undefined
  const warningsCount = instance ? findObservation(instance.observations, 'fpp.warnings.count') : undefined

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <p>
        <Link to="/fpp">← All FPP instances</Link>
      </p>

      {!instance ? (
        <p className="text-muted">
          No FPP instance with ID "{instanceId}" is currently configured. It may have
          been removed, or the snapshot this view is showing is out of date.
        </p>
      ) : (
        <>
          <h2 className="panel__title">{instance.instanceId}</h2>

          <PanelErrorBoundary panelLabel="FPP instance summary">
            <section className="panel">
              <div style={{ marginBottom: '0.75rem' }}>
                <FPPHealthBadge health={instance.health} />
              </div>
              <dl className="field-list">
                <dt>Endpoint</dt>
                <dd>{instance.endpoint}</dd>
                <dt>Last poll</dt>
                <dd>{formatAbsolute(instance.lastPollAt)}</dd>
                <dt>Last poll error</dt>
                <dd>{instance.lastPollError ?? 'none'}</dd>
              </dl>
            </section>
          </PanelErrorBoundary>

          {/* Step 7 seam C / Step 8, ADR-001/ADR-003: the full primitive
              command vocabulary (docs/bench/fpp-command-vocabulary.md
              section 4's eight-member registry). Every control here owns
              its own scope-gating (ScopedButton, ADR-024 decision 12) and
              its own outcome rendering (confirmed/unconfirmed/pending,
              never a bare "done" on a 200 alone) — FPPStopPlaylistControl
              is Step 7's original, unchanged; the rest are Step 8's own
              addition, each in its own panel so one primitive's own
              caveats (nextPlaylistItem's end-of-show hazard,
              stopPlaylistGracefully's "confirmed is not the same as
              stopped", startPlaylist's ifBusy guard) stay visually
              scoped to the control they belong to rather than blurring
              together in one shared block. */}
          <PanelErrorBoundary panelLabel="Commands">
            <section className="panel">
              <h3 className="panel__title">Commands</h3>
              {/* Each command's heading and control are grouped together and
                  laid out as a compact, wrapping row (.command-groups,
                  global.css) rather than one full-width button per command
                  stacked in a column: an operator hunting for Pause no
                  longer scrolls past Stop to find it. */}
              <div className="command-groups">
                <div className="command-group">
                  <h4 className="panel__title">Stop</h4>
                  <FPPStopPlaylistControl instanceId={instance.instanceId} />
                </div>
                <div className="command-group">
                  <h4 className="panel__title">Stop gracefully</h4>
                  <FPPStopPlaylistGracefullyControl instanceId={instance.instanceId} />
                </div>
                <div className="command-group">
                  <h4 className="panel__title">Pause / resume</h4>
                  <FPPPausePlaylistControl instanceId={instance.instanceId} />
                  <FPPResumePlaylistControl instanceId={instance.instanceId} />
                </div>
                <div className="command-group">
                  <h4 className="panel__title">Item navigation</h4>
                  <FPPPrevPlaylistItemControl instanceId={instance.instanceId} />
                  <FPPNextPlaylistItemControl
                    instanceId={instance.instanceId}
                    observations={instance.observations}
                  />
                </div>
                <div className="command-group">
                  <h4 className="panel__title">Start playlist</h4>
                  <FPPStartPlaylistControl instanceId={instance.instanceId} />
                </div>
                <div className="command-group">
                  <h4 className="panel__title">Volume</h4>
                  <FPPSetVolumeControl instanceId={instance.instanceId} />
                </div>
              </div>
            </section>
          </PanelErrorBoundary>

          {/* TRACK-H-H2-SPEC.md §5.1: the show-night recovery path for a
              wedged sequence anchor, previously reachable only from
              `showmeshctl fpp reset-observation-sequence --confirm`. Its
              own panel, not folded into Commands above, because this is
              recovery from a stuck evidence store, not a playback
              primitive -- and it renders what it is about to discard
              before allowing the (arm-then-confirm, not undoable) clear. */}
          <PanelErrorBoundary panelLabel="Recovery">
            <section className="panel">
              <h3 className="panel__title">Recovery</h3>
              <h4 className="panel__title">Reset observation sequence</h4>
              <FPPResetObservationSequenceControl instanceUuid={instance.instanceUuid} />
            </section>
          </PanelErrorBoundary>

          {warningsSummary !== undefined && (
            <PanelErrorBoundary panelLabel="Warnings">
              {/* Prominent, but never a verdict: this is FPP's own warning
                  list rendered through the same shared EvidenceValue every
                  other signal uses -- it does not colour instance.health
                  above, and this component adds no severity classification
                  of its own (spec section 6 "Warnings" / STEP-5 section
                  5.3's reasoning: FPP's warning strings mix a debug-log
                  notice with a real connectivity fault, and ShowMesh does
                  not understand FPP's own text well enough to rank them). */}
              <section className="panel">
                <h3 className="panel__title">Warnings</h3>
                <div className="table-scroll">
                  <table className="config-table" aria-label="Warnings">
                    <thead>
                      <tr>
                        <th scope="col">Signal</th>
                        <th scope="col">Value</th>
                      </tr>
                    </thead>
                    <tbody>
                      <tr>
                        <th scope="row">fpp.warnings.summary</th>
                        <td>
                          <EvidenceValue
                            evidence={warningsSummary}
                            serverTime={model.serverTime}
                            serverTimeReceivedAt={model.serverTimeReceivedAt}
                            connected={connected}
                          />
                        </td>
                      </tr>
                      {warningsCount !== undefined && (
                        <tr>
                          <th scope="row">fpp.warnings.count</th>
                          <td>
                            <EvidenceValue
                              evidence={warningsCount}
                              serverTime={model.serverTime}
                              serverTimeReceivedAt={model.serverTimeReceivedAt}
                              connected={connected}
                            />
                          </td>
                        </tr>
                      )}
                    </tbody>
                  </table>
                </div>
              </section>
            </PanelErrorBoundary>
          )}

          <h3 className="section-title">Pixel ports</h3>
          <PanelErrorBoundary panelLabel="Pixel ports">
            <section className="panel">
              <PortGrid observations={portsGroup?.observations ?? []} />
            </section>
          </PanelErrorBoundary>

          <h3 className="section-title">Observations</h3>
          {instance.observations.length === 0 ? (
            <p className="text-muted">This instance has no recorded observations.</p>
          ) : otherGroups.length === 0 ? (
            <p className="text-muted">
              This instance has no observations outside pixel ports (see above).
            </p>
          ) : (
            otherGroups.map((group) => (
              <PanelErrorBoundary key={group.id} panelLabel={group.label}>
                <section className="panel">
                  <h4 className="panel__title">{group.label}</h4>
                  <div className="table-scroll">
                    <table className="config-table" aria-label={group.label}>
                      <thead>
                        <tr>
                          <th scope="col">Signal</th>
                          <th scope="col">Value</th>
                        </tr>
                      </thead>
                      <tbody>
                        {group.observations.map((observation) => (
                          <tr key={observation.signal}>
                            <th scope="row">{observation.signal}</th>
                            <td>
                              <EvidenceValue
                                evidence={observation}
                                serverTime={model.serverTime}
                                serverTimeReceivedAt={model.serverTimeReceivedAt}
                                connected={connected}
                              />
                            </td>
                          </tr>
                        ))}
                      </tbody>
                    </table>
                  </div>
                </section>
              </PanelErrorBoundary>
            ))
          )}
        </>
      )}
    </div>
  )
}
