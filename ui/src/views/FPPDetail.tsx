import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { ApiError, acknowledgeFPPInstanceUUIDChange, getFPPPlaylistEntryReconciliation } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../app/session'
import { FPPHealthBadge, FPPPlaylistReconciliationOutcomeBadge } from '../components/DomainBadges'
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
import { ScopedButton } from '../components/ScopedButton'
import { formatAbsolute } from '../app/time'
import { findObservation, groupFppObservations } from '../app/fppSignals'
import type { FPPInstance, FPPPlaylistEntryReconciliationResponse } from '../app/types'

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

          {/* The resolved answer to "what is this instance actually
              playing, and which ShowMesh cue does that resolve to" was
              previously only reachable from the Playlist readiness page
              (views/PlaylistReadiness.tsx), keyed separately by
              instanceUuid -- an operator watching THIS page, the one with
              the transport controls, had to leave it to find out. Its own
              panel, placed here (right after the summary, before the
              transport Commands below) rather than folded into Recovery
              further down: reading what is playing and clearing a wedged
              observation are different actions with different
              consequences and should not share a panel. Reuses
              FPPPlaylistReconciliationOutcomeBadge and the same verbatim
              "never reworded" reason rendering PlaylistReadiness.tsx's own
              ReconciliationRow already uses, rather than inventing a
              second copy of either. */}
          <PanelErrorBoundary panelLabel="Current playback">
            <FPPCurrentEntryPanel instanceUuid={instance.instanceUuid} snapshotReceivedAt={model.snapshotReceivedAt} />
          </PanelErrorBoundary>

          {/* `FPPInstance.instanceUuidChange` (api/openapi.yaml): non-null
              exactly when this endpoint has a PENDING, unacknowledged uuid
              change -- a rebuilt or replaced Pi, from the coordinator's
              side. Its own panel, self-contained (renders nothing at all
              for an instance with no pending change), so it never crowds
              the summary panel above with a conflict most instances never
              have. */}
          <PanelErrorBoundary panelLabel="Pending instance uuid change">
            <FPPInstanceUuidChangeNotice instance={instance} />
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

type CurrentEntryState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: FPPPlaylistEntryReconciliationResponse }
  // No accepted playlist-entry observation for this instanceUuid yet
  // (openapi's own 404): the same "normal afternoon state, not a fault"
  // posture PlaylistReadiness.tsx's own ReconciliationRow already takes.
  | { kind: 'no-observation'; detail: string }
  | { kind: 'error'; message: string }

// Same fetch, generation-guard, and reconnect-refetch discipline as
// PlaylistReadiness.tsx's own useReconciliation, plus that same hook's
// third trigger: a fresh fppPlaylistEntry.changed observation for THIS
// instance also retriggers the fetch, keyed by `sequence` (a plain
// monotonically increasing integer) rather than by the observation
// object itself, which is a new identity every snapshot, or by
// `receivedAt`, a string this task's own instruction says to prefer
// `sequence` over.
function useCurrentEntryReconciliation(
  instanceUuid: string | null,
  snapshotReceivedAt: number | null,
  latestObservationSequence: number | null,
): CurrentEntryState {
  const [state, setState] = useState<CurrentEntryState>({ kind: 'loading' })

  useEffect(() => {
    if (instanceUuid === null) return
    let cancelled = false
    setState({ kind: 'loading' })
    getFPPPlaylistEntryReconciliation(instanceUuid)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'no-observation', detail: err.message })
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // latestObservationSequence is not read inside the effect body; it
    // exists purely to retrigger the fetch when FPP itself reports a new
    // entry for this instance.
  }, [instanceUuid, snapshotReceivedAt, latestObservationSequence])

  return state
}

// What this instance is actually playing, and which ShowMesh cue
// that resolves to -- previously only visible from
// views/PlaylistReadiness.tsx, keyed by instanceUuid there too. Renders
// nothing distinctive for an instance that has never reported a uuid,
// same posture FPPResetObservationSequenceControl already takes for the
// identical precondition.
function FPPCurrentEntryPanel({
  instanceUuid,
  snapshotReceivedAt,
}: {
  instanceUuid: string | null
  snapshotReceivedAt: number | null
}) {
  const model = useModelContext()
  // Keyed by instanceUuid, matching PlaylistReadiness.tsx's own
  // ReconciliationRow -- this instance's own latest accepted
  // playlist-entry observation, not the whole list.
  const latestObservation = model.fppPlaylistEntryObservations.find((o) => o.instanceUuid === instanceUuid)
  const reconciliation = useCurrentEntryReconciliation(
    instanceUuid,
    snapshotReceivedAt,
    latestObservation?.sequence ?? null,
  )

  if (instanceUuid === null) {
    return (
      <section className="panel">
        <h3 className="panel__title">Current playback</h3>
        <p className="text-muted">
          This instance has not yet reported an instance UUID, so no reconciliation verdict can
          exist for it yet.
        </p>
      </section>
    )
  }

  return (
    <section className="panel">
      <h3 className="panel__title">Current playback</h3>
      {reconciliation.kind === 'loading' && <p className="text-muted">Checking...</p>}
      {reconciliation.kind === 'error' && (
        <p role="alert" className="render-surface__error">
          Could not check: {reconciliation.message}
        </p>
      )}
      {reconciliation.kind === 'no-observation' && (
        <p className="text-muted">{reconciliation.detail}</p>
      )}
      {reconciliation.kind === 'loaded' && (
        <dl className="field-list" role="status">
          <dt>Current entry</dt>
          <dd>{reconciliation.response.observedEntryKey ?? 'unknown'}</dd>
          <dt>Outcome</dt>
          <dd>
            <FPPPlaylistReconciliationOutcomeBadge outcome={reconciliation.response.outcome} />
          </dd>
          {/* ADR-011, same "As of" freshness rule PlaylistReadiness.tsx's
              own rows already use: `serverTime` is the coordinator's own
              clock at the moment it computed THIS verdict, required on
              every 200 response, so an operator can tell a verdict read
              hours ago from one read a moment ago. */}
          <dt>As of</dt>
          <dd>{formatAbsolute(reconciliation.response.serverTime)}</dd>
          <dt>Reason</dt>
          {/* Verbatim, never reworded: this task's own instruction, matching
              PlaylistReadiness.tsx's own ReconciliationRow. */}
          <dd>{reconciliation.response.reason}</dd>
          {reconciliation.response.outcome === 'resolved' && (
            <>
              <dt>Entry id</dt>
              <dd>{reconciliation.response.entryId}</dd>
              <dt>Cue id</dt>
              <dd>{reconciliation.response.cueId}</dd>
            </>
          )}
        </dl>
      )}
    </section>
  )
}

const REQUIRED_CONFIG_WRITE_SCOPE = 'config:write'

// A human asserting a configured FPP endpoint's hardware really was
// replaced (an SD card clone, a restored backup, a swapped controller) --
// worded that way, never as a "dismiss" -- clears
// `FPPInstance.instanceUuidChange`'s ONE conflict marker via
// `POST /fpp/{instanceId}/instance-uuid/acknowledge`. Renders NOTHING for
// an instance with no pending change and no just-completed acknowledgement,
// so instances that have never had this conflict are not crowded with an
// empty panel. Once acknowledged, renders the OBSERVED post-acknowledge
// state straight from the response body (never the bare fact the POST
// returned), the same posture FPPResetObservationSequenceControl's
// ClearedOutcome uses for its own delete.
function FPPInstanceUuidChangeNotice({ instance }: { instance: FPPInstance }) {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [ackResult, setAckResult] = useState<{ instance: FPPInstance | null } | null>(null)

  if (instance.instanceUuidChange === null && ackResult === null) {
    return null
  }

  if (ackResult !== null) {
    return (
      <section className="panel" aria-label="Pending instance uuid change">
        <h3 className="panel__title">Pending instance uuid change</h3>
        {ackResult.instance === null ? (
          <p role="status">
            Acknowledged. This instance is no longer configured, so there is nothing further
            to show for it.
          </p>
        ) : ackResult.instance.instanceUuidChange === null ? (
          <p role="status">
            Acknowledged: no pending instance uuid change remains for this instance.
          </p>
        ) : (
          // Should not happen against a correct coordinator (the
          // acknowledge route always clears the marker or 409s before
          // ever committing), but if it does, this says so rather than
          // asserting the conflict is gone.
          <p role="alert" className="panel panel--error">
            The acknowledgement request succeeded, but a pending instance uuid change is
            still present for this instance.
          </p>
        )}
      </section>
    )
  }

  // Non-null, guaranteed by the early return above.
  const change = instance.instanceUuidChange!

  async function handleAcknowledge(): Promise<void> {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const resp = await acknowledgeFPPInstanceUUIDChange(instance.instanceId)
      setAckResult({ instance: resp.instance })
    } catch (err) {
      setError(describeApiError(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <section className="panel panel--warning" aria-label="Pending instance uuid change">
      <h3 className="panel__title">Pending instance uuid change</h3>
      <p>
        <strong>This endpoint&rsquo;s reported hardware identity changed.</strong> This looks
        like a rebuilt or replaced Pi: the coordinator observed a different instanceUuid than
        the one it had on record, and is holding this as an unresolved conflict until an
        operator confirms it.
      </p>
      <dl className="field-list">
        <dt>Previous uuid</dt>
        <dd>{change.previousUuid}</dd>
        <dt>Current uuid</dt>
        <dd>{instance.instanceUuid ?? 'unknown'}</dd>
        <dt>Change first seen</dt>
        <dd>{formatAbsolute(change.changedAt)}</dd>
      </dl>
      {error !== null && (
        <p role="alert" className="session-form__error">
          {error}
        </p>
      )}
      <ScopedButton
        requiredScope={REQUIRED_CONFIG_WRITE_SCOPE}
        onClick={() => void handleAcknowledge()}
        busy={busy}
        busyReason="Acknowledging…"
      >
        {busy ? 'Acknowledging…' : 'Acknowledge: this hardware was replaced'}
      </ScopedButton>
    </section>
  )
}
