import { Link } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { ClockSkewWarning } from '../components/ClockSkewWarning'
import { ControlPlaneBadge, FPPHealthBadge, ResolumeHealthBadge, SeverityBadge } from '../components/DomainBadges'
import { StatusBadge, type StatusTone } from '../components/StatusBadge'
import { findObservation } from '../app/fppSignals'
import { ageMs, effectiveServerTimeIso, formatAge } from '../app/time'
import { AttentionList, AttentionListItem, OperatorPageHeader, StatusStrip, StatusStripItem } from '../components/SharedLayouts'
import type { CurrentRun, FPPInstance, Node, ResolumeInstance } from '../app/types'
import { LifecycleTimeline } from './operate/LifecycleTimeline'
import { ReadinessSplit, type ReadinessCardProps } from './operate/ReadinessSplit'
import { useNightSessionState } from './operate/useNightSessionState'
import '../styles/operator-pages.css'
import '../styles/operate.css'

type AttentionTone = 'critical' | 'warning' | 'unknown'
type AttentionItem = { tone: AttentionTone; text: string; to: string }

const ATTENTION_BADGE: Record<AttentionTone, { tone: StatusTone; icon: string }> = {
  critical: { tone: 'bad', icon: '✕' }, warning: { tone: 'warn', icon: '⚠' }, unknown: { tone: 'unknown', icon: '?' },
}

function attentionFromFPP(instances: FPPInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    if (instance.health === 'failed') items.push({ tone: 'critical', text: `FPP instance "${instance.instanceId}" is failed`, to: `/monitor/fleet/fpp/${instance.instanceId}` })
    if (instance.health === 'degraded') items.push({ tone: 'warning', text: `FPP instance "${instance.instanceId}" is degraded`, to: `/monitor/fleet/fpp/${instance.instanceId}` })
    if (instance.health === 'unknown') items.push({ tone: 'unknown', text: `FPP instance "${instance.instanceId}" health is unknown`, to: `/monitor/fleet/fpp/${instance.instanceId}` })
  }
  return items
}

function attentionFromResolume(instances: ResolumeInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    if (instance.health === 'failed') items.push({ tone: 'critical', text: `Resolume instance "${instance.instanceId}" is failed`, to: `/monitor/fleet/resolume/${instance.instanceId}` })
    if (instance.health === 'degraded') items.push({ tone: 'warning', text: `Resolume instance "${instance.instanceId}" is degraded`, to: `/monitor/fleet/resolume/${instance.instanceId}` })
    if (instance.health === 'unknown') items.push({ tone: 'unknown', text: `Resolume instance "${instance.instanceId}" health is unknown`, to: `/monitor/fleet/resolume/${instance.instanceId}` })
  }
  return items
}

function attentionFromNodes(nodes: Node[]): AttentionItem[] {
  return nodes.flatMap((node) => node.controlPlane.state === 'offline'
    ? [{ tone: 'warning' as const, text: `${node.label ?? node.nodeId}: control-plane connection lost`, to: `/monitor/fleet/node/${node.nodeId}` }]
    : [])
}

function attentionFromRender(nodes: Node[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const node of nodes) for (const entry of node.render) {
    if (entry.signal !== 'surface.pipeline.state' || entry.state === 'not_collected') continue
    const label = `${node.label ?? node.nodeId} surface "${entry.resource.id}"`
    if (entry.state !== 'current') items.push({ tone: 'unknown', text: `${label} pipeline state is ${entry.state}`, to: `/monitor/fleet/node/${node.nodeId}` })
    else if (entry.value === 'failed') items.push({ tone: 'critical', text: `${label} pipeline has failed`, to: `/monitor/fleet/node/${node.nodeId}` })
    else if (entry.value === 'restarting') items.push({ tone: 'warning', text: `${label} pipeline is restarting`, to: `/monitor/fleet/node/${node.nodeId}` })
    else if (entry.value === 'superseded') items.push({ tone: 'warning', text: `${label} is showing content from a show that is no longer active`, to: `/monitor/fleet/node/${node.nodeId}` })
  }
  return items
}

function attentionFromCurrentRuns(runs: CurrentRun[] | null): AttentionItem[] {
  if (runs === null) return []
  return runs.flatMap((run) => (run.status === 'failed' || run.playback.state === 'failed')
    ? [{
        tone: 'critical' as const,
        text: `${run.runner} runner (source ${run.runner}) reports failed for "${run.show}"; freshness ${run.freshness.state}: ${run.freshness.reason}`,
        to: '/control',
      }]
    : [])
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
    ...model.fpp.map((instance) => ({ key: `fpp-${instance.instanceId}`, to: `/monitor/fleet/fpp/${instance.instanceId}`, icon: 'FPP', name: instance.instanceId, detail: <><span>FPP instance</span> <span>{findObservation(instance.observations, 'fpp.status')?.state ?? 'not collected'}</span></>, status: <FPPHealthBadge health={instance.health} /> })),
    ...model.nodes.map((node) => { const count = node.render.filter((entry) => entry.signal === 'surface.pipeline.state').length; return { key: `node-${node.nodeId}`, to: `/monitor/fleet/node/${node.nodeId}`, icon: 'NODE', name: node.label ?? node.nodeId, detail: <span>{count === 0 ? 'No render endpoints' : `${count} render endpoint${count === 1 ? '' : 's'}`} · control plane</span>, status: <ControlPlaneBadge state={node.controlPlane.state} /> } }),
    ...model.resolume.map((instance) => ({ key: `resolume-${instance.instanceId}`, to: `/monitor/fleet/resolume/${instance.instanceId}`, icon: 'RES', name: instance.instanceId, detail: <span>{instance.composition === null ? 'No composition uploaded' : `Composition · ${instance.composition.name}`}</span>, status: <ResolumeHealthBadge health={instance.health} /> })),
  ]
  return <section className="dashboard-section dashboard-section--path" aria-labelledby="dashboard-presentation"><div className="dashboard-section__heading"><div><h2 id="dashboard-presentation">Presentation path</h2><p className="text-muted">Current evidence only</p></div><span className="dashboard-section__meta">{rows.length} observed endpoint{rows.length === 1 ? '' : 's'}</span></div>{rows.length === 0 ? <p className="dashboard-empty text-muted">No presentation endpoints are observed.</p> : <ul className="dashboard-path-list">{rows.map((row) => <li key={row.key} className="dashboard-path-row"><span className="dashboard-path-row__icon" aria-hidden="true">{row.icon}</span><Link className="dashboard-path-row__name" to={row.to}><strong>{row.name}</strong><span>{row.detail}</span></Link><span className="dashboard-path-row__status">{row.status}</span></li>)}</ul>}</section>
}

function AttentionSection({ attention }: { attention: AttentionItem[] }) {
  const sorted = [...attention].sort((a, b) => ({ critical: 0, warning: 1, unknown: 2 }[a.tone] - ({ critical: 0, warning: 1, unknown: 2 }[b.tone])))
  return <section className="dashboard-section dashboard-section--attention" aria-labelledby="dashboard-attention"><div className="dashboard-section__heading"><div><h2 id="dashboard-attention">Needs you</h2><p className="text-muted">{sorted.length === 0 ? 'Nothing needs you' : `${sorted.length} item${sorted.length === 1 ? '' : 's'} · none is stopping tonight's show unless stated`}</p></div><span className="dashboard-section__meta">{sorted.length === 0 ? 'None reported' : `${sorted.length} reported`}</span></div>{sorted.length === 0 ? <p className="dashboard-empty text-muted">That is not proof the show looks right, only that nothing has asked for you.</p> : <AttentionList label="Needs you">{sorted.map((item) => <AttentionListItem key={item.to + item.text}><Link className="dashboard-attention-row" to={item.to}><StatusBadge tone={ATTENTION_BADGE[item.tone].tone} icon={ATTENTION_BADGE[item.tone].icon} label={item.tone} /><span>{item.text}</span></Link></AttentionListItem>)}</AttentionList>}</section>
}

function RecentActivity({ model }: { model: ReturnType<typeof useModelContext> }) {
  const recentEvents = model.events.slice(0, 5)
  return <section className="dashboard-section dashboard-section--activity" aria-labelledby="dashboard-activity"><div className="dashboard-section__heading"><div><h2 id="dashboard-activity">Recent activity</h2><p className="text-muted">Latest recorded events</p></div><Link to="/monitor/activity">View all activity →</Link></div>{model.eventsGap && <p className="evidence__reason" role="status">Some event history has been permanently lost to retention; this list does not reach back to the beginning.</p>}{recentEvents.length === 0 ? <p className="dashboard-empty text-muted">No events recorded yet.</p> : <div className="dashboard-activity-table-scroll" tabIndex={0} role="region" aria-label="Recent activity"><table className="dashboard-activity-table"><thead><tr><th>Event</th><th>Source</th><th>Time</th></tr></thead><tbody>{recentEvents.map((event) => <tr key={event.seq}><td><Link to="/monitor/activity"><SeverityBadge severity={event.severity} /> {event.summary}</Link></td><td>{event.resource.id}</td><td>{event.occurredAt ?? event.recordedAt}</td></tr>)}</tbody></table></div>}</section>
}

export function Dashboard() {
  const model = useModelContext()
  const [nightState] = useNightSessionState()
  const attention = [...attentionFromCurrentRuns(model.currentRuns?.runs ?? null), ...attentionFromFPP(model.fpp), ...attentionFromResolume(model.resolume), ...attentionFromNodes(model.nodes), ...attentionFromRender(model.nodes)]
  const renderEntries = model.nodes.flatMap((node) => node.render.filter((entry) => entry.signal === 'surface.pipeline.state'))
  const currentRunTones = model.currentRuns?.runs.map(currentRunStatusTone) ?? []
  const fppTone: StatusTone = model.fpp.length === 0 ? 'unknown' : model.fpp.some((instance) => instance.health === 'failed') ? 'bad' : model.fpp.some((instance) => instance.health !== 'healthy' && instance.health !== 'suppressed') ? 'warn' : 'good'
  const renderTone: StatusTone = renderEntries.length === 0 ? 'unknown' : renderEntries.some((entry) => entry.state === 'current' && entry.value === 'failed') ? 'bad' : renderEntries.some((entry) => entry.state !== 'current' || entry.value === 'restarting' || entry.value === 'superseded') ? 'warn' : 'good'
  const nodeTone: StatusTone = model.nodes.length === 0 ? 'unknown' : model.nodes.some((node) => node.controlPlane.state === 'offline') ? 'warn' : model.nodes.some((node) => node.controlPlane.state === 'unknown') ? 'unknown' : 'good'
  const runTone: StatusTone = model.currentRuns === null || currentRunTones.length === 0 ? 'unknown' : currentRunTones.some((tone) => tone === 'bad') ? 'bad' : currentRunTones.some((tone) => tone === 'warn') ? 'warn' : currentRunTones.some((tone) => tone === 'unknown') ? 'unknown' : 'good'
  const connection = model.connection.kind === 'live' ? 'Live' : model.connection.kind === 'reconnecting' ? 'Reconnecting' : model.connection.kind === 'connecting' ? 'Connecting' : model.connection.kind === 'failed' ? 'Failed' : model.connection.kind === 'unauthorized' ? 'Unauthorized' : 'Unknown'

  // Any reported run counts as "attempting to run" for this card, not
  // only a 'playing' one: an authoritative failed run is exactly the
  // case that must escalate here, not read as "nothing is running".
  const isPlaying = model.currentRuns !== null && model.currentRuns.runs.length > 0
  const runningTones = [fppTone, renderTone, ...currentRunTones]
  const running: ReadinessCardProps = !isPlaying
    ? { heading: 'Running', tone: 'unknown', icon: '–', label: 'Not running', detail: 'No runner currently reports a run in progress.' }
    : runningTones.some((tone) => tone === 'bad')
      ? { heading: 'Running', tone: 'bad', icon: '✕', label: 'Not confirmed', detail: 'The show in progress is missing an output or evidence it needs.' }
      : runningTones.some((tone) => tone === 'warn' || tone === 'unknown')
        ? { heading: 'Running', tone: 'warn', icon: '⚠', label: 'Running, degraded evidence', detail: 'The show in progress is playing, but at least one output is stale or unobserved.' }
        : { heading: 'Running', tone: 'good', icon: '✓', label: 'Running', detail: 'The show in progress has every output it needs. Nothing on this page will interrupt it.' }

  const reference = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const readiness = nightState.kind === 'loaded' ? nightState.session.readiness : null
  const checks = readiness?.state === 'recorded' ? readiness.checks : []
  const healthyChecks = checks.filter((c) => c.state === 'healthy').length
  let nextStart: ReadinessCardProps
  if (nightState.kind === 'loading') {
    nextStart = { heading: 'Next start gated', tone: 'unknown', icon: '?', label: 'Loading', detail: 'Waiting for the night session state.' }
  } else if (readiness === null || readiness.state !== 'recorded' || readiness.outcome === undefined) {
    nextStart = {
      heading: 'Next start gated',
      tone: 'unknown',
      icon: '?',
      label: 'Unknown',
      detail: readiness?.reason ?? 'No readiness evidence has been recorded yet.',
      actions: <Link className="btn btn--secondary btn--compact" to="/night">Run readiness</Link>,
    }
  } else {
    const age = readiness.completedAt !== undefined ? ageMs(readiness.completedAt, reference) : null
    const gated = readiness.outcome !== 'ready' || !readiness.sameEpoch || !readiness.fresh
    const staleness = !readiness.sameEpoch ? 'from an earlier epoch' : !readiness.fresh ? 'stale' : null
    nextStart = {
      heading: 'Next start gated',
      tone: gated ? 'warn' : 'good',
      icon: gated ? '⚠' : '✓',
      label: gated ? 'Next start gated' : 'Next start clear',
      detail: (
        <>
          Readiness {readiness.outcome === 'ready' ? 'passed' : 'did not pass'} {age !== null ? formatAge(age) : 'at an unknown time'}
          {staleness !== null ? `, and is ${staleness}` : ''}.{' '}
          {checks.length > 0 ? `${healthyChecks} of ${checks.length} checks healthy. ` : ''}
          {gated ? 'A start command would be withheld until it runs again.' : ''}
        </>
      ),
      actions: (
        <>
          <Link className="btn btn--secondary btn--compact" to="/night">Run readiness</Link>
          <Link className="btn btn--quiet btn--compact" to="/monitor/readiness">See readiness detail →</Link>
        </>
      ),
    }
  }

  return <div className="operator-page dashboard-page"><OperatorPageHeader eyebrow="Operator overview" title="Dashboard" lede="Readiness, authoritative playback, and the presentation path at a glance." actions={<div className="dashboard-page__actions"><Link className="button" to="/night">Open Show Night</Link><Link className="button button--secondary" to="/control">Live Control</Link></div>} /><DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} /><ClockSkewWarning clockSkewMs={model.clockSkewMs} />
    <section aria-labelledby="dashboard-lifecycle" className="dashboard-section">
      <div className="dashboard-section__heading"><div><h2 id="dashboard-lifecycle">Tonight&rsquo;s lifecycle</h2><p className="text-muted">{connection} · {attention.length} attention item{attention.length === 1 ? '' : 's'}</p></div></div>
      <LifecycleTimeline loadState={nightState} />
    </section>
    <ReadinessSplit running={running} nextStart={nextStart} />
    <CurrentRunsSection model={model} />
    <section className="dashboard-section" aria-labelledby="dashboard-health">
      <div className="dashboard-section__heading"><div><h2 id="dashboard-health">System health</h2><p className="text-muted">Each resource&rsquo;s own report</p></div><Link to="/monitor">Open Monitor →</Link></div>
      <StatusStrip label="System summary"><StatusStripItem label="FPP" tone={fppTone} detail={model.fpp.length === 0 ? 'No instance evidence' : 'Instance health'}>{model.fpp.length === 0 ? 'Unknown' : `${model.fpp.filter((instance) => instance.health === 'healthy').length} / ${model.fpp.length} healthy`}</StatusStripItem><StatusStripItem label="Render" tone={renderTone} detail={renderEntries.length === 0 ? 'No render evidence' : `${renderEntries.filter((entry) => entry.state === 'current').length} current`}>{renderEntries.length === 0 ? 'Unknown' : `${renderEntries.filter((entry) => entry.state === 'current').length} / ${renderEntries.length}`}</StatusStripItem><StatusStripItem label="Nodes" tone={nodeTone} detail={model.nodes.length === 0 ? 'No node evidence' : 'Control-plane state'}>{model.nodes.length === 0 ? 'Unknown' : `${model.nodes.filter((node) => node.controlPlane.state === 'online').length} / ${model.nodes.length} connected`}</StatusStripItem><StatusStripItem label="Current run" tone={runTone} detail={model.currentRuns === null ? 'Projection unavailable' : 'Runner projection'}>{model.currentRuns === null ? 'Unknown' : model.currentRuns.runs.length === 0 ? 'None observed' : `${model.currentRuns.runs.length} reported`}</StatusStripItem></StatusStrip>
    </section>
    <div className="dashboard-content-grid"><AttentionSection attention={attention} /><PresentationPath model={model} /><RecentActivity model={model} /></div>
  </div>
}
