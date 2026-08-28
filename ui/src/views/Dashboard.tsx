import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { ClockSkewWarning } from '../components/ClockSkewWarning'
import { ControlPlaneBadge, FPPHealthBadge, ResolumeHealthBadge, SeverityBadge, CollectorStatusBadge } from '../components/DomainBadges'
import { StatusBadge, type StatusTone } from '../components/StatusBadge'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { ResolumeRecoveryToggle } from '../components/ResolumeRecoveryToggle'
import { FleetSignalBadge } from '../components/FleetSignalBadge'
import { findObservation } from '../app/fppSignals'
import { summarizeFleetPorts, summarizeFleetWarnings } from '../app/fppDashboard'
import { STATE_ICON, STATE_TONE } from '../app/evidenceState'
import { EvidenceValue } from '../components/EvidenceValue'
import type { CurrentRun, FPPInstance, Node, ResolumeInstance } from '../app/types'
import '../styles/operator-pages.css'

// The default view (spec section 6.4). OBSERVABILITY section 6.2's last
// line: "the default view prioritizes active critical conditions, then
// readiness blockers, warnings, and informational activity." Readiness
// blockers and a persisted alert model do not exist in this coordinator
// yet (BUILD-PLAN Step 4 out-of-scope list), so "active critical
// conditions" here is derived only from what Step 3 actually models:
// FPP instance health and node control-plane liveness. This is not a
// substitute for the eventual alert model -- it is exactly the fraction
// of OBSERVABILITY section 6.2 the coordinator can currently back with
// evidence (spec section 6.1's narrowing).

// 'unknown' is its own tone, distinct from 'warning': ADR-011 forbids
// presenting insufficient or stale evidence as healthy, but "the system
// does not know" is not the same claim as "the system knows and it is
// degraded," so the two must not collapse into one badge or one sort
// bucket. 'suppressed' health is deliberately excluded from attention:
// OBSERVABILITY section 4.2 defines it as a condition expected under a
// maintenance or lifecycle policy, i.e. already accounted for, which is
// what distinguishes it from 'unknown' rather than from 'healthy'.
type AttentionTone = 'critical' | 'warning' | 'unknown'

interface AttentionItem {
  tone: AttentionTone
  text: string
  to: string
}

const ATTENTION_BADGE: Record<AttentionTone, { tone: StatusTone; icon: string }> = {
  critical: { tone: 'bad', icon: '✕' },
  warning: { tone: 'warn', icon: '⚠' },
  unknown: { tone: 'unknown', icon: '?' },
}

function attentionFromFPP(instances: FPPInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    if (instance.health === 'failed') {
      items.push({
        tone: 'critical',
        text: `FPP instance "${instance.instanceId}" is failed`,
        to: `/fpp/${instance.instanceId}`,
      })
    } else if (instance.health === 'degraded') {
      items.push({
        tone: 'warning',
        text: `FPP instance "${instance.instanceId}" is degraded`,
        to: `/fpp/${instance.instanceId}`,
      })
    } else if (instance.health === 'unknown') {
      // D2: insufficient/stale evidence is 'unknown', and ADR-011 forbids
      // rendering that as healthy. Before this branch existed, every
      // instance at "unknown" produced zero attention items and the
      // default view read as fully healthy.
      items.push({
        tone: 'unknown',
        text: `FPP instance "${instance.instanceId}" health is unknown`,
        to: `/fpp/${instance.instanceId}`,
      })
    }
  }
  return items
}

// Track D seam D-4 (build contract §2.1): the identical rule
// attentionFromFPP above already applies, over the identical five-value
// health vocabulary (ResolumeInstance.health is structurally the same
// enum as FPPInstance.health) — 'unknown' gets its own attention item
// rather than reading as fine, and 'suppressed' is deliberately excluded
// for the same reason it is above.
function attentionFromResolume(instances: ResolumeInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    if (instance.health === 'failed') {
      items.push({ tone: 'critical', text: `Resolume instance "${instance.instanceId}" is failed`, to: '/resolume' })
    } else if (instance.health === 'degraded') {
      items.push({ tone: 'warning', text: `Resolume instance "${instance.instanceId}" is degraded`, to: '/resolume' })
    } else if (instance.health === 'unknown') {
      items.push({
        tone: 'unknown',
        text: `Resolume instance "${instance.instanceId}" health is unknown`,
        to: '/resolume',
      })
    }
  }
  return items
}

function attentionFromNodes(nodes: Node[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const node of nodes) {
    // Worded as a control-plane condition, not "the node is down": see
    // components/DomainBadges.tsx's ControlPlaneBadge for the same rule.
    if (node.controlPlane.state === 'offline') {
      items.push({
        tone: 'warning',
        text: `${node.label ?? node.nodeId}: control-plane connection lost`,
        to: `/nodes/${node.nodeId}`,
      })
    }
  }
  return items
}

// Track B seam B2b-front: a surface's pipeline state, from node.render's
// flat per-signal list. 'unknown' (evidence.state !== 'current' — stale,
// unknown_age, not_collected, collection_failed, or unsupported) is kept
// visually and semantically distinct from 'critical'/'warning', the same
// rule attentionFromFPP/attentionFromResolume already apply: "the system
// does not know" is not the same claim as "the system knows and it is
// degraded." A surface this coordinator has simply never heard from
// (not_collected) produces no item at all — that is the ordinary state
// for a node with no surface applied yet, not something to flag.
function attentionFromRender(nodes: Node[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const node of nodes) {
    for (const entry of node.render) {
      if (entry.signal !== 'surface.pipeline.state') continue
      const label = `${node.label ?? node.nodeId} surface "${entry.resource.id}"`
      if (entry.state === 'not_collected') continue
      if (entry.state !== 'current') {
        items.push({ tone: 'unknown', text: `${label} pipeline state is ${entry.state}`, to: `/nodes/${node.nodeId}` })
      } else if (entry.value === 'failed') {
        items.push({ tone: 'critical', text: `${label} pipeline has failed`, to: `/nodes/${node.nodeId}` })
      } else if (entry.value === 'restarting') {
        items.push({ tone: 'warning', text: `${label} pipeline is restarting`, to: `/nodes/${node.nodeId}` })
      } else if (entry.value === 'superseded') {
        // This surface is holding a render authorized by a show
        // that is no longer active (ADR-043 H0.7) — the wall looks
        // identical to a healthy "running" render, so the whole point of
        // this state is that it must not disappear into the same
        // no-attention-item bucket "running" does.
        items.push({ tone: 'warning', text: `${label} is showing content from a show that is no longer active`, to: `/nodes/${node.nodeId}` })
      }
    }
  }
  return items
}

function sortByTone(items: AttentionItem[]): AttentionItem[] {
  const order: Record<AttentionTone, number> = { critical: 0, warning: 1, unknown: 2 }
  return [...items].sort((a, b) => order[a.tone] - order[b.tone])
}

function readiness(model: ReturnType<typeof useModelContext>): { label: string; detail: string; tone: StatusTone; icon: string } {
  if (model.snapshotReceivedAt === null || model.connection.kind === 'connecting') {
    return { label: 'Unknown', detail: 'Waiting for coordinator data.', tone: 'unknown', icon: '?' }
  }
  if (model.connection.kind !== 'live') {
    return { label: 'Stale', detail: 'Last known data is shown while disconnected.', tone: 'unknown', icon: '!' }
  }
  if (model.fpp.length === 0 && model.nodes.length === 0 && model.resolume.length === 0) {
    return { label: 'Unknown', detail: 'No presentation evidence is configured.', tone: 'unknown', icon: '?' }
  }
  const renderEntries = model.nodes.flatMap((node) => node.render.filter((entry) => entry.signal === 'surface.pipeline.state'))
  if (
    model.fpp.some((instance) => instance.health === 'failed') ||
    model.resolume.some((instance) => instance.health === 'failed') ||
    renderEntries.some((entry) => entry.state === 'current' && entry.value === 'failed')
  ) {
    return { label: 'Not ready', detail: 'A presentation resource has failed.', tone: 'bad', icon: '✕' }
  }
  if (
    model.fpp.some((instance) => instance.health === 'degraded' || instance.health === 'unknown') ||
    model.resolume.some((instance) => instance.health === 'degraded' || instance.health === 'unknown') ||
    model.nodes.some((node) => node.controlPlane.state !== 'online') ||
    renderEntries.some((entry) => entry.state !== 'current' || entry.value === 'restarting' || entry.value === 'superseded')
  ) {
    return { label: 'Needs attention', detail: 'One or more resources are degraded or unobserved.', tone: 'warn', icon: '⚠' }
  }
  return { label: 'Ready', detail: 'Current resource evidence is healthy.', tone: 'good', icon: '✓' }
}

const CURRENT_RUN_STATUS_TONE: Record<string, StatusTone> = {
  playing: 'good',
  running: 'good',
  current: 'good',
  failed: 'bad',
  unknown: 'unknown',
  unavailable: 'unknown',
  stopped: 'unknown',
  idle: 'unknown',
  paused: 'unknown',
  ready: 'unknown',
}

function currentRunStatusTone(run: CurrentRun): StatusTone {
  // A known failure remains a failure even when its freshness is stale.
  if (run.status === 'failed' || run.playback.state === 'failed') return 'bad'
  if (run.freshness.state !== 'current') return 'unknown'

  // Keep indeterminate and non-active states out of the healthy tone. There
  // is no neutral badge in the shared vocabulary, so unknown is the honest
  // non-good presentation for stopped, idle, and future status literals.
  const statusTone = CURRENT_RUN_STATUS_TONE[run.status] ?? 'unknown'
  if (statusTone !== 'good') return statusTone
  if (run.reconciliation.state === 'degraded' || run.reconciliation.state === 'conflicted') return 'warn'
  return statusTone
}

function CurrentRunRow({ run }: { run: CurrentRun }) {
  return (
    <li className="current-run" data-runner={run.runner}>
      <div className="current-run__header">
        <div>
          <strong>{run.show}</strong>
          <span className="operator-list__meta"> {run.runner}</span>
        </div>
        <StatusBadge tone={currentRunStatusTone(run)} icon={run.status === 'playing' ? '▶' : '•'} label={run.status} />
      </div>
      <dl className="current-run__facts">
        <dt>Playback</dt>
        <dd>
          {run.playback.state}: {run.playback.media} ({run.playback.itemId})
          {run.playback.positionMs === null ? '' : ` at ${run.playback.positionMs} ms`}
          <span className="current-run__reason">{run.playback.reason}</span>
        </dd>
        <dt>Freshness</dt>
        <dd>{run.freshness.state}: {run.freshness.reason}</dd>
        <dt>Reconciliation</dt>
        <dd>{run.reconciliation.state}: {run.reconciliation.reason}</dd>
        <dt>Activation</dt>
        <dd>
          {run.activation.show} generation {run.activation.generation}, playlist {run.activation.playlistId}{' '}
          revision {run.activation.revision} ({run.activation.runner})
        </dd>
        <dt>Next</dt>
        <dd>
          {run.next === null
            ? 'No authoritative next item reported.'
            : `${run.next.itemId}: ${run.next.media} (item ${run.next.itemIndex}, source ${run.next.source})`}
        </dd>
      </dl>
      {run.targets.length > 0 && (
        <p className="current-run__targets">
          Targets: {run.targets.map((target) => `${target.kind} ${target.id}`).join(', ')}
        </p>
      )}
    </li>
  )
}

function ConnectionLabel({ model }: { model: ReturnType<typeof useModelContext> }): string {
  if (model.connection.kind === 'live') return 'Live'
  if (model.connection.kind === 'reconnecting') return 'Reconnecting'
  if (model.connection.kind === 'connecting') return 'Connecting'
  if (model.connection.kind === 'failed') return 'Failed'
  if (model.connection.kind === 'unauthorized') return 'Unauthorized'
  if (model.connection.kind === 'incompatible') return 'Incompatible'
  return 'Unknown'
}

function renderCount(model: ReturnType<typeof useModelContext>): { value: string; detail: string } {
  const entries = model.nodes.flatMap((node) => node.render.filter((entry) => entry.signal === 'surface.pipeline.state'))
  const current = entries.filter((entry) => entry.state === 'current').length
  if (entries.length === 0) return { value: 'Unknown', detail: 'No render evidence observed.' }
  return { value: `${current} / ${entries.length}`, detail: current === entries.length ? 'Current evidence' : 'Some evidence is not current' }
}

function attentionSummary(attention: AttentionItem[]): string {
  if (attention.length === 0) return 'No attention items are reported.'
  return `${attention.length} attention item${attention.length === 1 ? '' : 's'} reported.`
}

function PresentationPath({ model }: { model: ReturnType<typeof useModelContext> }) {
  const fppRows = model.fpp.map((instance) => ({
    key: `fpp-${instance.instanceId}`,
    to: `/fpp/${instance.instanceId}`,
    icon: 'FPP',
    name: instance.instanceId,
    detail: (
      <>
        <span>FPP instance</span>{' '}
        <FleetSignalBadge label="playback" evidence={findObservation(instance.observations, 'fpp.status')} />
      </>
    ),
    status: <FPPHealthBadge health={instance.health} />,
  }))
  const nodeRows = model.nodes.map((node) => {
    const renderEntries = node.render.filter((entry) => entry.signal === 'surface.pipeline.state')
    return {
      key: `node-${node.nodeId}`,
      to: `/nodes/${node.nodeId}`,
      icon: 'NODE',
      name: node.label ?? node.nodeId,
      detail: (
        <>
          <span>{renderEntries.length === 0 ? 'No render endpoint observed' : `${renderEntries.length} render endpoint${renderEntries.length === 1 ? '' : 's'}`}</span>
          {' · '}
          <span>control plane</span>
        </>
      ),
      status: <ControlPlaneBadge state={node.controlPlane.state} />,
    }
  })
  const resolumeRows = model.resolume.map((instance) => ({
    key: `resolume-${instance.instanceId}`,
    to: '/resolume',
    icon: 'RES',
    name: instance.instanceId,
    detail: <span>{instance.composition === null ? 'No composition uploaded' : `Composition · ${instance.composition.name}`}</span>,
    status: <ResolumeHealthBadge health={instance.health} />,
  }))
  const rows = [...fppRows, ...nodeRows, ...resolumeRows]

  return (
    <section className="dashboard-section dashboard-section--path" aria-labelledby="dashboard-presentation">
      <div className="dashboard-section__heading">
        <div>
          <h2 id="dashboard-presentation">Presentation path</h2>
          <p className="text-muted">Current evidence only</p>
        </div>
        <span className="dashboard-section__meta">{rows.length} observed endpoint{rows.length === 1 ? '' : 's'}</span>
      </div>
      {rows.length === 0 ? (
        <p className="dashboard-empty text-muted">No presentation endpoints are observed.</p>
      ) : (
        <ul className="dashboard-path-list">
          {rows.map((row) => (
            <li key={row.key} className="dashboard-path-row">
              <span className="dashboard-path-row__icon" aria-hidden="true">{row.icon}</span>
              <Link className="dashboard-path-row__name" to={row.to}>
                <strong>{row.name}</strong>
                <span>{row.detail}</span>
              </Link>
              <span className="dashboard-path-row__status">{row.status}</span>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function CurrentRunsSection({ model }: { model: ReturnType<typeof useModelContext> }) {
  if (model.currentRuns === null) {
    return (
      <section className="dashboard-section dashboard-section--runs" aria-labelledby="dashboard-current-run">
        <div className="dashboard-section__heading">
          <div>
            <h2 id="dashboard-current-run">Current run</h2>
            <p className="text-muted">Authoritative playback evidence</p>
          </div>
          <StatusBadge tone="unknown" icon="?" label="unavailable" />
        </div>
        <p className="dashboard-empty text-muted" role="status">
          Authoritative current playback is unavailable{model.currentRunsFetchFailed ? ': the coordinator could not be read.' : ' while the coordinator response is pending.'}
        </p>
      </section>
    )
  }

  const runs = model.currentRuns.runs
  return (
    <section className="dashboard-section dashboard-section--runs" aria-labelledby="dashboard-current-run">
      <div className="dashboard-section__heading">
        <div>
          <h2 id="dashboard-current-run">Current run</h2>
          <p className="text-muted">Authoritative playback evidence</p>
        </div>
        <span className="dashboard-section__meta">{runs.length === 0 ? 'None observed' : `${runs.length} active runner${runs.length === 1 ? '' : 's'}`}</span>
      </div>
      {runs.length === 0 ? (
        <p className="dashboard-empty text-muted">No runner currently reports a run. This is not a claim that no external process is running.</p>
      ) : (
        <ul className="operator-list">
          {runs.map((run) => <CurrentRunRow key={run.id} run={run} />)}
        </ul>
      )}
    </section>
  )
}

function AttentionSection({ attention }: { attention: AttentionItem[] }) {
  return (
    <section className="dashboard-section dashboard-section--attention" aria-labelledby="dashboard-attention">
      <div className="dashboard-section__heading">
        <div>
          <h2 id="dashboard-attention">Attention</h2>
          <p className="text-muted">Conditions that may need an operator</p>
        </div>
        <span className="dashboard-section__meta">{attentionSummary(attention)}</span>
      </div>
      {attention.length === 0 ? (
        <p className="dashboard-empty text-muted">Nothing needs attention: no active critical or warning conditions, and no instances with unknown health, in nodes or FPP instances right now.</p>
      ) : (
        <ul className="dashboard-attention-list">
          {attention.map((item) => (
            <li key={item.to + item.text}>
              <Link className="dashboard-attention-row" to={item.to}>
                <StatusBadge tone={ATTENTION_BADGE[item.tone].tone} icon={ATTENTION_BADGE[item.tone].icon} label={item.tone} />
                <span>{item.text}</span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </section>
  )
}

function RecentActivity({ model }: { model: ReturnType<typeof useModelContext> }) {
  const recentEvents = model.events.slice(0, 5)
  return (
    <section className="dashboard-section dashboard-section--activity" aria-labelledby="dashboard-activity">
      <div className="dashboard-section__heading">
        <div>
          <h2 id="dashboard-activity">Recent activity</h2>
          <p className="text-muted">Latest recorded events</p>
        </div>
        <Link to="/events">View all events →</Link>
      </div>
      {model.eventsGap && (
        <p className="evidence__reason" role="status">Some event history has been permanently lost to retention; this list does not reach back to the beginning.</p>
      )}
      {recentEvents.length === 0 ? (
        <p className="dashboard-empty text-muted">No events recorded yet.</p>
      ) : (
        <div className="dashboard-activity-table-scroll">
          <table className="dashboard-activity-table">
            <thead><tr><th>Event</th><th>Source</th><th>Time</th></tr></thead>
            <tbody>
              {recentEvents.map((event) => (
                <tr key={event.seq}>
                  <td><Link to="/events"><SeverityBadge severity={event.severity} /> {event.summary}</Link></td>
                  <td>{event.resource.id}</td>
                  <td>{event.occurredAt ?? event.recordedAt}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

function AdditionalEvidence({ model }: { model: ReturnType<typeof useModelContext> }) {
  const resolumeInstance = model.resolume[0]
  const onlineNodes = model.nodes.filter((node) => node.controlPlane.state === 'online').length
  const offlineNodes = model.nodes.filter((node) => node.controlPlane.state === 'offline').length
  const unknownNodes = model.nodes.length - onlineNodes - offlineNodes
  const fppUnknownHealth = model.fpp.filter((instance) => instance.health === 'unknown').length
  const fppSuppressed = model.fpp.filter((instance) => instance.health === 'suppressed').length
  const warningsTotal = summarizeFleetWarnings(model.fpp)
  const portsTotal = summarizeFleetPorts(model.fpp)

  return (
    <details className="dashboard-evidence">
      <summary>System evidence details</summary>
      <div className="dashboard-evidence__content">
        <section className="dashboard-evidence__section" aria-labelledby="dashboard-inventory">
          <h2 id="dashboard-inventory">Inventory</h2>
          <dl className="field-list">
            <dt>Nodes with control-plane connected</dt><dd>{onlineNodes}</dd>
            <dt>Nodes with control-plane connection lost</dt><dd>{offlineNodes}</dd>
            <dt>Nodes with control-plane state unknown</dt><dd>{unknownNodes}</dd>
            <dt>FPP instances configured</dt><dd>{model.fpp.length}</dd>
            <dt>FPP instances with health unknown</dt><dd>{fppUnknownHealth}</dd>
            <dt>FPP instances suppressed</dt><dd>{fppSuppressed}</dd>
            <dt>FPP warnings across fleet</dt>
            <dd>
              {warningsTotal.instancesReporting === 0 ? <span className="text-muted">not collected</span> : <>{warningsTotal.total}
                {warningsTotal.instancesStaleOrUnknownAge > 0 && <span className="text-muted"> ({warningsTotal.instancesStaleOrUnknownAge} instance{warningsTotal.instancesStaleOrUnknownAge === 1 ? '' : 's'} stale or age unknown)</span>}
                {warningsTotal.instancesUnknown > 0 && <span className="text-muted"> ({warningsTotal.instancesUnknown} instance{warningsTotal.instancesUnknown === 1 ? '' : 's'} not reporting)</span>}
              </>}
            </dd>
            <dt>Collectors</dt>
            <dd>{model.collectors.length === 0 ? 'none configured' : <ul className="list-plain">{model.collectors.map((collector) => <li key={collector.id}><CollectorStatusBadge state={collector.state} /> <span className="text-muted">{collector.id}</span>{collector.reason !== null && <div className="evidence__reason">{collector.reason}</div>}</li>)}</ul>}</dd>
          </dl>
        </section>

        <section className="dashboard-evidence__section" aria-labelledby="dashboard-resolume">
          <h2 id="dashboard-resolume">Resolume</h2>
          {resolumeInstance === undefined ? <p className="text-muted">Resolume is not configured on this coordinator.</p> : <>
            <Link className="entity-link" to="/resolume"><strong>{resolumeInstance.instanceId}</strong></Link>
            <EvidenceValue label="reachable" evidence={findObservation(resolumeInstance.observations, 'resolume.reachable') ?? { signal: 'resolume.reachable', value: null, unit: null, state: 'not_collected', reason: 'never collected', observedAt: null, collectedAt: null, source: 'resolume', quality: 'direct', validForSeconds: null }} serverTime={model.serverTime} serverTimeReceivedAt={model.serverTimeReceivedAt} connected={model.connection.kind === 'live'} />
            <p className="text-muted">{resolumeInstance.composition === null ? 'No composition uploaded.' : `Loaded composition: ${resolumeInstance.composition.name}`}</p>
          </>}
        </section>

        <section className="dashboard-evidence__section" aria-labelledby="dashboard-playback-state">
          <h2 id="dashboard-playback-state">Playback state</h2>
          {model.fpp.length === 0 ? <p className="text-muted">No FPP instances are configured on this coordinator.</p> : <ul className="list-plain">{model.fpp.map((instance) => <li key={instance.instanceId}><Link className="entity-link" to={`/fpp/${instance.instanceId}`}><strong>{instance.instanceId}</strong>{' '}<FleetSignalBadge evidence={findObservation(instance.observations, 'fpp.status')} /><div className="text-muted"><FleetSignalBadge label="playlist" evidence={findObservation(instance.observations, 'fpp.playlist.name')} /></div></Link></li>)}</ul>}
        </section>
        <section className="dashboard-evidence__section" aria-labelledby="dashboard-controller-health">
          <h2 id="dashboard-controller-health">Controller health</h2>
          {model.fpp.length === 0 ? <p className="text-muted">No FPP instances are configured on this coordinator.</p> : <ul className="list-plain">{model.fpp.map((instance) => <li key={instance.instanceId}><Link className="entity-link" to={`/fpp/${instance.instanceId}`}><strong>{instance.instanceId}</strong>{' '}<FleetSignalBadge label="fppd" evidence={findObservation(instance.observations, 'fpp.fppd.state')} />{' '}<FleetSignalBadge label="power bad" evidence={findObservation(instance.observations, 'fpp.power.bad')} /></Link></li>)}</ul>}
        </section>
        <section className="dashboard-evidence__section" aria-labelledby="dashboard-pixel-current">
          <h2 id="dashboard-pixel-current">Pixel current</h2>
          {model.fpp.length === 0 ? <p className="text-muted">No FPP instances are configured on this coordinator.</p> : <><p className="text-muted">{portsTotal.instancesReporting === 0 ? 'Port inventory not collected for any instance yet.' : `${portsTotal.totalPorts} port element(s) across ${portsTotal.instancesReporting} reporting instance(s), ${portsTotal.totalBlind} of which are smart-receiver blind spots.`}{portsTotal.instancesStaleOrUnknownAge > 0 && <> {portsTotal.instancesStaleOrUnknownAge} instance{portsTotal.instancesStaleOrUnknownAge === 1 ? '' : 's'} contributing port counts that are stale or of unknown age.</>}{portsTotal.instancesBlindCountUnknown > 0 && <> Blind-spot count not reported by {portsTotal.instancesBlindCountUnknown} instance{portsTotal.instancesBlindCountUnknown === 1 ? '' : 's'}, so the blind-spot total above may be incomplete.</>}{portsTotal.instancesUnknown > 0 && <> {portsTotal.instancesUnknown} instance{portsTotal.instancesUnknown === 1 ? '' : 's'} not reporting port inventory.</>}</p><ul className="list-plain">{model.fpp.map((instance) => { const count = findObservation(instance.observations, 'fpp.ports.count'); return <li key={instance.instanceId}><Link className="entity-link" to={`/fpp/${instance.instanceId}`}><strong>{instance.instanceId}</strong>{' '}{typeof count?.value === 'number' && count.value === 0 ? <StatusBadge tone={STATE_TONE[count.state]} icon={STATE_ICON[count.state]} label="reports no pixel output ports" /> : <FleetSignalBadge label="ports" evidence={count} />}</Link></li> })}</ul></>}
        </section>
        <section className="dashboard-evidence__section" aria-labelledby="dashboard-network-state">
          <h2 id="dashboard-network-state">Network / MQTT state</h2>
          {model.fpp.length === 0 ? <p className="text-muted">No FPP instances are configured on this coordinator.</p> : <ul className="list-plain">{model.fpp.map((instance) => <li key={instance.instanceId}><Link className="entity-link" to={`/fpp/${instance.instanceId}`}><strong>{instance.instanceId}</strong>{' '}<FleetSignalBadge label="MQTT configured" evidence={findObservation(instance.observations, 'fpp.mqtt.configured')} />{' '}<FleetSignalBadge label="MQTT connected" evidence={findObservation(instance.observations, 'fpp.mqtt.connected')} /></Link></li>)}</ul>}
        </section>
        <section className="dashboard-evidence__section" aria-labelledby="dashboard-recovery">
          <h2 id="dashboard-recovery">Resolume crash recovery</h2>
          <PanelErrorBoundary panelLabel="Resolume crash recovery"><ResolumeRecoveryToggle /></PanelErrorBoundary>
        </section>
      </div>
    </details>
  )
}

export function Dashboard() {
  const model = useModelContext()
  const attention = sortByTone([...attentionFromFPP(model.fpp), ...attentionFromResolume(model.resolume), ...attentionFromNodes(model.nodes), ...attentionFromRender(model.nodes)])
  const ready = readiness(model)
  const currentRunCount = model.currentRuns?.runs.length ?? null
  const render = renderCount(model)
  const fppHealthy = model.fpp.filter((instance) => instance.health === 'healthy').length
  const nodesOnline = model.nodes.filter((node) => node.controlPlane.state === 'online').length
  const renderEntries = model.nodes.flatMap((node) => node.render.filter((entry) => entry.signal === 'surface.pipeline.state'))
  const fppStatTone: StatusTone = model.fpp.length === 0
    ? 'unknown'
    : model.fpp.some((instance) => instance.health === 'failed')
      ? 'bad'
      : model.fpp.some((instance) => instance.health === 'degraded' || instance.health === 'unknown')
        ? 'warn'
        : 'good'
  const renderStatTone: StatusTone = renderEntries.length === 0
    ? 'unknown'
    : renderEntries.some((entry) => entry.state === 'current' && entry.value === 'failed')
      ? 'bad'
      : renderEntries.some((entry) => entry.state !== 'current' || entry.value === 'restarting' || entry.value === 'superseded')
        ? 'warn'
        : 'good'
  const nodeStatTone: StatusTone = model.nodes.length === 0
    ? 'unknown'
    : model.nodes.some((node) => node.controlPlane.state === 'unknown')
      ? 'unknown'
      : model.nodes.some((node) => node.controlPlane.state === 'offline')
        ? 'warn'
        : 'good'
  const currentRunTones = model.currentRuns?.runs.map(currentRunStatusTone) ?? []
  const currentRunStatTone: StatusTone = currentRunCount === null || currentRunCount === 0
    ? 'unknown'
    : currentRunTones.some((tone) => tone === 'bad')
      ? 'bad'
      : currentRunTones.some((tone) => tone === 'warn')
        ? 'warn'
        : currentRunTones.some((tone) => tone === 'unknown')
          ? 'unknown'
          : 'good'

  return (
    <div className="operator-page dashboard-page">
      <header className="operator-page__header dashboard-page__header">
        <div>
          <p className="dashboard-page__kicker">Operator overview</p>
          <h1 id="dashboard-title" className="operator-page__title">Dashboard</h1>
          <p className="operator-page__lede text-muted">Readiness, current playback, and the presentation path at a glance.</p>
        </div>
        <div className="dashboard-page__actions">
          <Link className="button" to="/night">Open Show Night</Link>
          <Link className="button button--secondary" to="/control">Live Control</Link>
        </div>
      </header>

      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <ClockSkewWarning clockSkewMs={model.clockSkewMs} />

      <section className={`dashboard-readiness dashboard-readiness--${ready.tone}`} aria-label="Show path readiness">
        <div>
          <strong><StatusBadge tone={ready.tone} icon={ready.icon} label={ready.label} /></strong>
          <span>{ready.detail}</span>
        </div>
        <span className="dashboard-readiness__meta">{ConnectionLabel({ model })} · {attentionSummary(attention)}</span>
      </section>

      <section className="dashboard-stat-strip" aria-label="System summary">
        <div className={`dashboard-stat dashboard-stat--${fppStatTone}`}><span>FPP</span><strong>{model.fpp.length === 0 ? 'Unknown' : `${fppHealthy} / ${model.fpp.length} healthy`}</strong><small>{model.fpp.length === 0 ? 'No instance evidence' : 'Instance health'}</small></div>
        <div className={`dashboard-stat dashboard-stat--${renderStatTone}`}><span>Render</span><strong>{render.value}</strong><small>{render.detail}</small></div>
        <div className={`dashboard-stat dashboard-stat--${nodeStatTone}`}><span>Nodes</span><strong>{model.nodes.length === 0 ? 'Unknown' : `${nodesOnline} / ${model.nodes.length} connected`}</strong><small>{model.nodes.length === 0 ? 'No node evidence' : 'Control-plane state'}</small></div>
        <div className={`dashboard-stat dashboard-stat--${currentRunStatTone}`}><span>Current run</span><strong>{currentRunCount === null ? 'Unknown' : currentRunCount === 0 ? 'None observed' : `${currentRunCount} reported`}</strong><small>{currentRunCount === null ? 'Authoritative playback unavailable' : 'Runner projection'}</small></div>
      </section>

      <div className="dashboard-content-grid">
        <PresentationPath model={model} />
        <AttentionSection attention={attention} />
        <CurrentRunsSection model={model} />
        <RecentActivity model={model} />
      </div>
      <AdditionalEvidence model={model} />
    </div>
  )
}
