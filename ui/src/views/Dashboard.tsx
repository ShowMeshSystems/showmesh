import { Link } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { ClockSkewWarning } from '../components/ClockSkewWarning'
import { ControlPlaneBadge, FPPHealthBadge, ResolumeHealthBadge, SeverityBadge } from '../components/DomainBadges'
import { StatusBadge, type StatusTone } from '../components/StatusBadge'
import { findObservation } from '../app/fppSignals'
import { AttentionList, AttentionListItem, OperatorPageHeader, StatusStrip, StatusStripItem } from '../components/SharedLayouts'
import type { CurrentRun, FPPInstance, Node, ResolumeInstance } from '../app/types'
import '../styles/operator-pages.css'

type AttentionTone = 'critical' | 'warning' | 'unknown'
type AttentionItem = { tone: AttentionTone; text: string; to: string }

const ATTENTION_BADGE: Record<AttentionTone, { tone: StatusTone; icon: string }> = {
  critical: { tone: 'bad', icon: '✕' }, warning: { tone: 'warn', icon: '⚠' }, unknown: { tone: 'unknown', icon: '?' },
}

function attentionFromFPP(instances: FPPInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    if (instance.health === 'failed') items.push({ tone: 'critical', text: `FPP instance "${instance.instanceId}" is failed`, to: `/fpp/${instance.instanceId}` })
    if (instance.health === 'degraded') items.push({ tone: 'warning', text: `FPP instance "${instance.instanceId}" is degraded`, to: `/fpp/${instance.instanceId}` })
    if (instance.health === 'unknown') items.push({ tone: 'unknown', text: `FPP instance "${instance.instanceId}" health is unknown`, to: `/fpp/${instance.instanceId}` })
  }
  return items
}

function attentionFromResolume(instances: ResolumeInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    if (instance.health === 'failed') items.push({ tone: 'critical', text: `Resolume instance "${instance.instanceId}" is failed`, to: '/resolume' })
    if (instance.health === 'degraded') items.push({ tone: 'warning', text: `Resolume instance "${instance.instanceId}" is degraded`, to: '/resolume' })
    if (instance.health === 'unknown') items.push({ tone: 'unknown', text: `Resolume instance "${instance.instanceId}" health is unknown`, to: '/resolume' })
  }
  return items
}

function attentionFromNodes(nodes: Node[]): AttentionItem[] {
  return nodes.flatMap((node) => node.controlPlane.state === 'offline'
    ? [{ tone: 'warning' as const, text: `${node.label ?? node.nodeId}: control-plane connection lost`, to: `/nodes/${node.nodeId}` }]
    : [])
}

function attentionFromRender(nodes: Node[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const node of nodes) for (const entry of node.render) {
    if (entry.signal !== 'surface.pipeline.state' || entry.state === 'not_collected') continue
    const label = `${node.label ?? node.nodeId} surface "${entry.resource.id}"`
    if (entry.state !== 'current') items.push({ tone: 'unknown', text: `${label} pipeline state is ${entry.state}`, to: `/nodes/${node.nodeId}` })
    else if (entry.value === 'failed') items.push({ tone: 'critical', text: `${label} pipeline has failed`, to: `/nodes/${node.nodeId}` })
    else if (entry.value === 'restarting') items.push({ tone: 'warning', text: `${label} pipeline is restarting`, to: `/nodes/${node.nodeId}` })
    else if (entry.value === 'superseded') items.push({ tone: 'warning', text: `${label} is showing content from a show that is no longer active`, to: `/nodes/${node.nodeId}` })
  }
  return items
}

function readiness(model: ReturnType<typeof useModelContext>): { label: string; detail: string; tone: StatusTone; icon: string } {
  if (model.snapshotReceivedAt === null || model.connection.kind === 'connecting') return { label: 'Unknown', detail: 'Waiting for coordinator data.', tone: 'unknown', icon: '?' }
  if (model.connection.kind !== 'live') return { label: 'Stale', detail: 'Last known data is shown while disconnected.', tone: 'unknown', icon: '!' }
  if (model.fpp.length === 0 && model.nodes.length === 0 && model.resolume.length === 0) return { label: 'Unknown', detail: 'No presentation evidence is configured.', tone: 'unknown', icon: '?' }
  const renderEntries = model.nodes.flatMap((node) => node.render.filter((entry) => entry.signal === 'surface.pipeline.state'))
  if (model.fpp.some((instance) => instance.health === 'failed') || model.resolume.some((instance) => instance.health === 'failed') || renderEntries.some((entry) => entry.state === 'current' && entry.value === 'failed')) return { label: 'Not ready', detail: 'A presentation resource has failed.', tone: 'bad', icon: '✕' }
  if (model.fpp.some((instance) => instance.health === 'degraded' || instance.health === 'unknown') || model.resolume.some((instance) => instance.health === 'degraded' || instance.health === 'unknown') || model.nodes.some((node) => node.controlPlane.state !== 'online') || renderEntries.some((entry) => entry.state !== 'current' || entry.value === 'restarting' || entry.value === 'superseded')) return { label: 'Needs attention', detail: 'One or more resources are degraded or unobserved.', tone: 'warn', icon: '⚠' }
  return { label: 'Ready', detail: 'Current resource evidence is healthy.', tone: 'good', icon: '✓' }
}

function currentRunStatusTone(run: CurrentRun): StatusTone {
  if (run.status === 'failed' || run.playback.state === 'failed') return 'bad'
  if (run.freshness.state !== 'current') return 'unknown'
  if (run.reconciliation.state === 'degraded' || run.reconciliation.state === 'conflicted') return 'warn'
  return run.status === 'playing' || run.status === 'running' || run.status === 'current' ? 'good' : 'unknown'
}

function CurrentRunRow({ run }: { run: CurrentRun }) {
  const next = run.next === null ? 'No authoritative next activity reported.' : `${run.next.itemId}: ${run.next.media} (source ${run.next.source})`
  return <li className="dashboard-current-run" data-runner={run.runner}><div className="dashboard-current-run__heading"><div><strong>{run.show}</strong><span>{run.runner} runner</span></div><StatusBadge tone={currentRunStatusTone(run)} icon={run.status === 'playing' ? '▶' : '•'} label={run.status} /></div><p>{run.playback.state}: {run.playback.media} · {run.playback.reason}</p><dl className="dashboard-current-run__evidence"><div><dt>Source</dt><dd>{run.runner}</dd></div><div><dt>Freshness</dt><dd>{run.freshness.state}: {run.freshness.reason}</dd></div><div><dt>Next activity</dt><dd>{next}</dd></div></dl></li>
}

function CurrentRunsSection({ model }: { model: ReturnType<typeof useModelContext> }) {
  const section = (title: string, detail: string, content: ReactNode, meta: ReactNode) => <section className="dashboard-current-runs" aria-labelledby="dashboard-current-run"><div><h2 id="dashboard-current-run">{title}</h2><p>{detail}</p></div>{meta}{content}</section>
  if (model.currentRuns === null) return section('Current run', 'Authoritative runner playback', <p className="dashboard-current-runs__empty" role="status">Authoritative current playback is unavailable. {model.currentRunsFetchFailed ? 'The coordinator could not be read.' : 'Waiting for the coordinator response.'}</p>, <StatusBadge tone="unknown" icon="?" label="unavailable" />)
  if (model.currentRuns.runs.length === 0) return section('Current run', 'Authoritative runner playback', <p className="dashboard-current-runs__empty">No runner currently reports a run. This does not prove that no external process is running.</p>, <span className="dashboard-section__meta">None observed</span>)
  return section('Current and concurrent runs', 'Authoritative runner playback, with source and freshness', <ul>{model.currentRuns.runs.map((run) => <CurrentRunRow key={run.id} run={run} />)}</ul>, <span className="dashboard-section__meta">{model.currentRuns.runs.length} reported</span>)
}

function PresentationPath({ model }: { model: ReturnType<typeof useModelContext> }) {
  const rows = [
    ...model.fpp.map((instance) => ({ key: `fpp-${instance.instanceId}`, to: `/fpp/${instance.instanceId}`, icon: 'FPP', name: instance.instanceId, detail: <><span>FPP instance</span> <span>{findObservation(instance.observations, 'fpp.status')?.state ?? 'not collected'}</span></>, status: <FPPHealthBadge health={instance.health} /> })),
    ...model.nodes.map((node) => { const count = node.render.filter((entry) => entry.signal === 'surface.pipeline.state').length; return { key: `node-${node.nodeId}`, to: `/nodes/${node.nodeId}`, icon: 'NODE', name: node.label ?? node.nodeId, detail: <span>{count === 0 ? 'No render endpoints' : `${count} render endpoint${count === 1 ? '' : 's'}`} · control plane</span>, status: <ControlPlaneBadge state={node.controlPlane.state} /> } }),
    ...model.resolume.map((instance) => ({ key: `resolume-${instance.instanceId}`, to: '/resolume', icon: 'RES', name: instance.instanceId, detail: <span>{instance.composition === null ? 'No composition uploaded' : `Composition · ${instance.composition.name}`}</span>, status: <ResolumeHealthBadge health={instance.health} /> })),
  ]
  return <section className="dashboard-section dashboard-section--path" aria-labelledby="dashboard-presentation"><div className="dashboard-section__heading"><div><h2 id="dashboard-presentation">Presentation path</h2><p className="text-muted">Current evidence only</p></div><span className="dashboard-section__meta">{rows.length} observed endpoint{rows.length === 1 ? '' : 's'}</span></div>{rows.length === 0 ? <p className="dashboard-empty text-muted">No presentation endpoints are observed.</p> : <ul className="dashboard-path-list">{rows.map((row) => <li key={row.key} className="dashboard-path-row"><span className="dashboard-path-row__icon" aria-hidden="true">{row.icon}</span><Link className="dashboard-path-row__name" to={row.to}><strong>{row.name}</strong><span>{row.detail}</span></Link><span className="dashboard-path-row__status">{row.status}</span></li>)}</ul>}</section>
}

function AttentionSection({ attention }: { attention: AttentionItem[] }) {
  const sorted = [...attention].sort((a, b) => ({ critical: 0, warning: 1, unknown: 2 }[a.tone] - ({ critical: 0, warning: 1, unknown: 2 }[b.tone])))
  return <section className="dashboard-section dashboard-section--attention" aria-labelledby="dashboard-attention"><div className="dashboard-section__heading"><div><h2 id="dashboard-attention">Operator attention</h2><p className="text-muted">Conditions that may need an operator</p></div><span className="dashboard-section__meta">{sorted.length === 0 ? 'None reported' : `${sorted.length} reported`}</span></div>{sorted.length === 0 ? <p className="dashboard-empty text-muted">No current critical, warning, or unknown conditions are reported.</p> : <AttentionList label="Operator attention">{sorted.map((item) => <AttentionListItem key={item.to + item.text}><Link className="dashboard-attention-row" to={item.to}><StatusBadge tone={ATTENTION_BADGE[item.tone].tone} icon={ATTENTION_BADGE[item.tone].icon} label={item.tone} /><span>{item.text}</span></Link></AttentionListItem>)}</AttentionList>}</section>
}

function RecentActivity({ model }: { model: ReturnType<typeof useModelContext> }) {
  const recentEvents = model.events.slice(0, 5)
  return <section className="dashboard-section dashboard-section--activity" aria-labelledby="dashboard-activity"><div className="dashboard-section__heading"><div><h2 id="dashboard-activity">Recent activity</h2><p className="text-muted">Latest recorded events</p></div><Link to="/events">View all events →</Link></div>{model.eventsGap && <p className="evidence__reason" role="status">Some event history has been permanently lost to retention; this list does not reach back to the beginning.</p>}{recentEvents.length === 0 ? <p className="dashboard-empty text-muted">No events recorded yet.</p> : <div className="dashboard-activity-table-scroll" tabIndex={0} role="region" aria-label="Recent activity"><table className="dashboard-activity-table"><thead><tr><th>Event</th><th>Source</th><th>Time</th></tr></thead><tbody>{recentEvents.map((event) => <tr key={event.seq}><td><Link to="/events"><SeverityBadge severity={event.severity} /> {event.summary}</Link></td><td>{event.resource.id}</td><td>{event.occurredAt ?? event.recordedAt}</td></tr>)}</tbody></table></div>}</section>
}

export function Dashboard() {
  const model = useModelContext()
  const attention = [...attentionFromFPP(model.fpp), ...attentionFromResolume(model.resolume), ...attentionFromNodes(model.nodes), ...attentionFromRender(model.nodes)]
  const ready = readiness(model)
  const renderEntries = model.nodes.flatMap((node) => node.render.filter((entry) => entry.signal === 'surface.pipeline.state'))
  const currentRunTones = model.currentRuns?.runs.map(currentRunStatusTone) ?? []
  const fppTone: StatusTone = model.fpp.length === 0 ? 'unknown' : model.fpp.some((instance) => instance.health === 'failed') ? 'bad' : model.fpp.some((instance) => instance.health !== 'healthy' && instance.health !== 'suppressed') ? 'warn' : 'good'
  const renderTone: StatusTone = renderEntries.length === 0 ? 'unknown' : renderEntries.some((entry) => entry.state === 'current' && entry.value === 'failed') ? 'bad' : renderEntries.some((entry) => entry.state !== 'current' || entry.value === 'restarting' || entry.value === 'superseded') ? 'warn' : 'good'
  const nodeTone: StatusTone = model.nodes.length === 0 ? 'unknown' : model.nodes.some((node) => node.controlPlane.state === 'offline') ? 'warn' : model.nodes.some((node) => node.controlPlane.state === 'unknown') ? 'unknown' : 'good'
  const runTone: StatusTone = model.currentRuns === null || currentRunTones.length === 0 ? 'unknown' : currentRunTones.some((tone) => tone === 'bad') ? 'bad' : currentRunTones.some((tone) => tone === 'warn') ? 'warn' : currentRunTones.some((tone) => tone === 'unknown') ? 'unknown' : 'good'
  const connection = model.connection.kind === 'live' ? 'Live' : model.connection.kind === 'reconnecting' ? 'Reconnecting' : model.connection.kind === 'connecting' ? 'Connecting' : model.connection.kind === 'failed' ? 'Failed' : model.connection.kind === 'unauthorized' ? 'Unauthorized' : 'Unknown'
  return <div className="operator-page dashboard-page"><OperatorPageHeader eyebrow="Operator overview" title="Dashboard" lede="Readiness, authoritative playback, and the presentation path at a glance." actions={<div className="dashboard-page__actions"><Link className="button" to="/night">Open Show Night</Link><Link className="button button--secondary" to="/control">Live Control</Link></div>} /><DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} /><ClockSkewWarning clockSkewMs={model.clockSkewMs} /><section className={`dashboard-readiness dashboard-readiness--${ready.tone}`} aria-label="Show path readiness"><div><strong><StatusBadge tone={ready.tone} icon={ready.icon} label={ready.label} /></strong><span>{ready.detail}</span></div><span className="dashboard-readiness__meta">{connection} · {attention.length} attention item{attention.length === 1 ? '' : 's'}</span></section><CurrentRunsSection model={model} /><StatusStrip label="System summary"><StatusStripItem label="FPP" tone={fppTone} detail={model.fpp.length === 0 ? 'No instance evidence' : 'Instance health'}>{model.fpp.length === 0 ? 'Unknown' : `${model.fpp.filter((instance) => instance.health === 'healthy').length} / ${model.fpp.length} healthy`}</StatusStripItem><StatusStripItem label="Render" tone={renderTone} detail={renderEntries.length === 0 ? 'No render evidence' : `${renderEntries.filter((entry) => entry.state === 'current').length} current`}>{renderEntries.length === 0 ? 'Unknown' : `${renderEntries.filter((entry) => entry.state === 'current').length} / ${renderEntries.length}`}</StatusStripItem><StatusStripItem label="Nodes" tone={nodeTone} detail={model.nodes.length === 0 ? 'No node evidence' : 'Control-plane state'}>{model.nodes.length === 0 ? 'Unknown' : `${model.nodes.filter((node) => node.controlPlane.state === 'online').length} / ${model.nodes.length} connected`}</StatusStripItem><StatusStripItem label="Current run" tone={runTone} detail={model.currentRuns === null ? 'Projection unavailable' : 'Runner projection'}>{model.currentRuns === null ? 'Unknown' : model.currentRuns.runs.length === 0 ? 'None observed' : `${model.currentRuns.runs.length} reported`}</StatusStripItem></StatusStrip><div className="dashboard-content-grid"><PresentationPath model={model} /><AttentionSection attention={attention} /><RecentActivity model={model} /></div></div>
}
