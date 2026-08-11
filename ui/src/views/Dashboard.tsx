import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { ClockSkewWarning } from '../components/ClockSkewWarning'
import { SeverityBadge, CollectorStatusBadge } from '../components/DomainBadges'
import { StatusBadge, type StatusTone } from '../components/StatusBadge'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import type { FPPInstance, Node } from '../app/types'

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

function sortByTone(items: AttentionItem[]): AttentionItem[] {
  const order: Record<AttentionTone, number> = { critical: 0, warning: 1, unknown: 2 }
  return [...items].sort((a, b) => order[a.tone] - order[b.tone])
}

export function Dashboard() {
  const model = useModelContext()

  const attention = sortByTone([...attentionFromFPP(model.fpp), ...attentionFromNodes(model.nodes)])

  const onlineNodes = model.nodes.filter((node) => node.controlPlane.state === 'online').length
  const offlineNodes = model.nodes.filter((node) => node.controlPlane.state === 'offline').length
  const unknownNodes = model.nodes.length - onlineNodes - offlineNodes

  // D2: a bare "FPP instances configured: N" said nothing about whether
  // the coordinator actually knows those instances' health. Mirrors the
  // node online/offline/unknown breakdown immediately above.
  const fppUnknownHealth = model.fpp.filter((instance) => instance.health === 'unknown').length
  const fppSuppressed = model.fpp.filter((instance) => instance.health === 'suppressed').length

  const recentEvents = model.events.slice(0, 5)

  return (
    <div>
      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />
      <ClockSkewWarning clockSkewMs={model.clockSkewMs} />

      <PanelErrorBoundary panelLabel="Attention">
        <section className="panel">
          <h2 className="panel__title">Attention</h2>
          {attention.length === 0 ? (
            <p className="text-muted">
              Nothing needs attention: no active critical or warning conditions, and no
              instances with unknown health, in nodes or FPP instances right now.
            </p>
          ) : (
            <ul className="list-plain">
              {attention.map((item) => (
                <li key={item.to + item.text}>
                  <Link className="entity-link" to={item.to}>
                    <StatusBadge
                      tone={ATTENTION_BADGE[item.tone].tone}
                      icon={ATTENTION_BADGE[item.tone].icon}
                      label={item.tone}
                    />{' '}
                    {item.text}
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </section>
      </PanelErrorBoundary>

      <PanelErrorBoundary panelLabel="Inventory summary">
        <section className="panel">
          <h2 className="panel__title">Inventory</h2>
          <dl className="field-list">
            <dt>Nodes with control-plane connected</dt>
            <dd>{onlineNodes}</dd>
            <dt>Nodes with control-plane connection lost</dt>
            <dd>{offlineNodes}</dd>
            <dt>Nodes with control-plane state unknown</dt>
            <dd>{unknownNodes}</dd>
            <dt>FPP instances configured</dt>
            <dd>{model.fpp.length}</dd>
            <dt>FPP instances with health unknown</dt>
            <dd>{fppUnknownHealth}</dd>
            <dt>FPP instances suppressed</dt>
            <dd>{fppSuppressed}</dd>
            <dt>Collectors</dt>
            <dd>
              {/* D1: a collector's state and reason previously never reached
                  the operator at all -- this rendered as the bare count
                  `model.collectors.length`, so a collector reporting a
                  failure state and a reason explaining it looked identical
                  to a healthy one. Each collector's own run state (not the
                  health of what it collects -- see CollectorStatus's Go doc
                  comment) is rendered with its reason alongside it. */}
              {model.collectors.length === 0 ? (
                'none configured'
              ) : (
                <ul className="list-plain">
                  {model.collectors.map((collector) => (
                    <li key={collector.id}>
                      <CollectorStatusBadge state={collector.state} />{' '}
                      <span className="text-muted">{collector.id}</span>
                      {collector.reason !== null && (
                        <div className="evidence__reason">{collector.reason}</div>
                      )}
                    </li>
                  ))}
                </ul>
              )}
            </dd>
          </dl>
        </section>
      </PanelErrorBoundary>

      <PanelErrorBoundary panelLabel="Recent events">
        <section className="panel">
          <h2 className="panel__title">Recent events</h2>
          {model.eventsGap && (
            <p className="evidence__reason" role="status">
              Some event history has been permanently lost to retention; this list does
              not reach back to the beginning.
            </p>
          )}
          {recentEvents.length === 0 ? (
            <p className="text-muted">No events recorded yet.</p>
          ) : (
            <ul className="list-plain">
              {recentEvents.map((event) => (
                <li key={event.seq}>
                  <Link className="entity-link" to="/events">
                    <SeverityBadge severity={event.severity} /> {event.summary}
                  </Link>
                </li>
              ))}
            </ul>
          )}
          <p>
            <Link to="/events">View all events</Link>
          </p>
        </section>
      </PanelErrorBoundary>
    </div>
  )
}
