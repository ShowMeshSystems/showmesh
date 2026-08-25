import { Link } from 'react-router-dom'
import { useModelContext } from '../app/ModelContext'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import { ClockSkewWarning } from '../components/ClockSkewWarning'
import { SeverityBadge, CollectorStatusBadge } from '../components/DomainBadges'
import { StatusBadge, type StatusTone } from '../components/StatusBadge'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { ResolumeRecoveryToggle } from '../components/ResolumeRecoveryToggle'
import { FleetSignalBadge } from '../components/FleetSignalBadge'
import { findObservation } from '../app/fppSignals'
import { summarizeFleetPorts, summarizeFleetWarnings } from '../app/fppDashboard'
import { STATE_ICON, STATE_TONE } from '../app/evidenceState'
import { EvidenceValue } from '../components/EvidenceValue'
import type { FPPInstance, Node, ResolumeInstance } from '../app/types'

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
      }
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

  const attention = sortByTone([
    ...attentionFromFPP(model.fpp),
    ...attentionFromResolume(model.resolume),
    ...attentionFromNodes(model.nodes),
    ...attentionFromRender(model.nodes),
  ])
  const resolumeInstance = model.resolume[0]

  const onlineNodes = model.nodes.filter((node) => node.controlPlane.state === 'online').length
  const offlineNodes = model.nodes.filter((node) => node.controlPlane.state === 'offline').length
  const unknownNodes = model.nodes.length - onlineNodes - offlineNodes

  // D2: a bare "FPP instances configured: N" said nothing about whether
  // the coordinator actually knows those instances' health. Mirrors the
  // node online/offline/unknown breakdown immediately above.
  const fppUnknownHealth = model.fpp.filter((instance) => instance.health === 'unknown').length
  const fppSuppressed = model.fpp.filter((instance) => instance.health === 'suppressed').length

  // Fleet-wide counts for the four newly modeled signal groups (spec
  // section 6 "Dashboard"). Counted, never verdict-ed: see
  // fppDashboard.ts's header comment.
  const warningsTotal = summarizeFleetWarnings(model.fpp)
  const portsTotal = summarizeFleetPorts(model.fpp)

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

      {/* Attention stays full width above: it is the page's banner-like
          surfacing of critical/warning conditions, not a peer data panel.
          Everything below is a genuine peer, so it can share the wide-display
          two-column grid (.panel-grid, global.css). */}
      <div className="panel-grid">
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
              <dt>FPP warnings across fleet</dt>
              <dd>
                {warningsTotal.instancesReporting === 0 ? (
                  <span className="text-muted">not collected</span>
                ) : (
                  <>
                    {warningsTotal.total}
                    {/* Step 5 review finding 6: a total built partly from
                        stale/unknown_age evidence must say so -- it is still
                        a legitimate value (EvidenceValue.tsx's contract), not
                        folded into instancesUnknown, but it must not read as
                        equally fresh as a total built entirely from current
                        evidence. */}
                    {warningsTotal.instancesStaleOrUnknownAge > 0 && (
                      <span className="text-muted">
                        {' '}
                        ({warningsTotal.instancesStaleOrUnknownAge} instance
                        {warningsTotal.instancesStaleOrUnknownAge === 1 ? '' : 's'} stale or age unknown)
                      </span>
                    )}
                    {warningsTotal.instancesUnknown > 0 && (
                      <span className="text-muted">
                        {' '}
                        ({warningsTotal.instancesUnknown} instance
                        {warningsTotal.instancesUnknown === 1 ? '' : 's'} not reporting)
                      </span>
                    )}
                  </>
                )}
              </dd>
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

        {/* Track D seam D-4 (build contract §2.1): reachability with
            provenance/freshness through the shared EvidenceValue, the loaded
            composition's name or a stated "no composition uploaded," and an
            "unconfigured" render rather than an error or an empty box when
            GET /resolume/instances answers with an empty array by design. */}
        <PanelErrorBoundary panelLabel="Resolume">
          <section className="panel">
            <h2 className="panel__title">Resolume</h2>
            {resolumeInstance === undefined ? (
              <p className="text-muted">Resolume is not configured on this coordinator.</p>
            ) : (
              <>
                <Link className="entity-link" to="/resolume">
                  <strong>{resolumeInstance.instanceId}</strong>
                </Link>
                <EvidenceValue
                  label="reachable"
                  evidence={
                    findObservation(resolumeInstance.observations, 'resolume.reachable') ?? {
                      signal: 'resolume.reachable',
                      value: null,
                      unit: null,
                      state: 'not_collected',
                      reason: 'never collected',
                      observedAt: null,
                      collectedAt: null,
                      source: 'resolume',
                      quality: 'direct',
                      validForSeconds: null,
                    }
                  }
                  serverTime={model.serverTime}
                  serverTimeReceivedAt={model.serverTimeReceivedAt}
                  connected={model.connection.kind === 'live'}
                />
                <p className="text-muted">
                  {resolumeInstance.composition === null
                    ? 'No composition uploaded.'
                    : `Loaded composition: ${resolumeInstance.composition.name}`}
                </p>
              </>
            )}
          </section>
        </PanelErrorBoundary>

        {/* Track D seam D-3a §7.1/§2.6: the auto-restore toggle's own read
            and write control — the one exception to "everything else of
            Track D's UI is D-4's own work" (see ResolumeRecoveryToggle.tsx's
            own top comment for why this ships here rather than waiting). */}
        <PanelErrorBoundary panelLabel="Resolume crash recovery">
          <ResolumeRecoveryToggle />
        </PanelErrorBoundary>

        {/* Step 5: four newly modeled signal groups each get a panel (spec
            section 6 "Dashboard"). Every panel renders unconditionally when
            FPP instances are configured -- ShowMesh models all four
            subsystems now, so there is never a "this subsystem is not
            modeled" reason to omit one -- and each instance row states an
            absence via FleetSignalBadge rather than going blank when a
            particular signal was never collected. None of these panels
            colours or recomputes instance.health; they are the same
            Evidence envelopes FPPDetail shows, just fleet-wide and compact. */}
        <PanelErrorBoundary panelLabel="Playback state">
          <section className="panel">
            <h2 className="panel__title">Playback state</h2>
            {model.fpp.length === 0 ? (
              <p className="text-muted">No FPP instances are configured on this coordinator.</p>
            ) : (
              <ul className="list-plain">
                {model.fpp.map((instance) => (
                  <li key={instance.instanceId}>
                    <Link className="entity-link" to={`/fpp/${instance.instanceId}`}>
                      <strong>{instance.instanceId}</strong>{' '}
                      <FleetSignalBadge evidence={findObservation(instance.observations, 'fpp.status')} />
                      <div className="text-muted">
                        <FleetSignalBadge
                          label="playlist"
                          evidence={findObservation(instance.observations, 'fpp.playlist.name')}
                        />
                      </div>
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </PanelErrorBoundary>

        <PanelErrorBoundary panelLabel="Controller health">
          <section className="panel">
            <h2 className="panel__title">Controller health</h2>
            {model.fpp.length === 0 ? (
              <p className="text-muted">No FPP instances are configured on this coordinator.</p>
            ) : (
              <ul className="list-plain">
                {model.fpp.map((instance) => (
                  <li key={instance.instanceId}>
                    <Link className="entity-link" to={`/fpp/${instance.instanceId}`}>
                      <strong>{instance.instanceId}</strong>{' '}
                      <FleetSignalBadge
                        label="fppd"
                        evidence={findObservation(instance.observations, 'fpp.fppd.state')}
                      />{' '}
                      <FleetSignalBadge
                        label="power bad"
                        evidence={findObservation(instance.observations, 'fpp.power.bad')}
                      />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </section>
        </PanelErrorBoundary>

        <PanelErrorBoundary panelLabel="Pixel current">
          <section className="panel">
            <h2 className="panel__title">Pixel current</h2>
            {model.fpp.length === 0 ? (
              <p className="text-muted">No FPP instances are configured on this coordinator.</p>
            ) : (
              <>
                <p className="text-muted">
                  {portsTotal.instancesReporting === 0
                    ? 'Port inventory not collected for any instance yet.'
                    : `${portsTotal.totalPorts} port element(s) across ${portsTotal.instancesReporting} reporting instance(s), ${portsTotal.totalBlind} of which are smart-receiver blind spots.`}
                  {/* Step 5 review finding 6/7: both counts below are stated
                      explicitly rather than silently folded into the numbers
                      above -- a stale/unknown_age contribution is still a
                      real value, and an unanswered blind_count means
                      totalBlind is a partial sum, not a confirmed total. */}
                  {portsTotal.instancesStaleOrUnknownAge > 0 && (
                    <>
                      {' '}
                      {portsTotal.instancesStaleOrUnknownAge} instance{portsTotal.instancesStaleOrUnknownAge === 1 ? '' : 's'} contributing
                      port counts that are stale or of unknown age.
                    </>
                  )}
                  {portsTotal.instancesBlindCountUnknown > 0 && (
                    <>
                      {' '}
                      Blind-spot count not reported by {portsTotal.instancesBlindCountUnknown} instance
                      {portsTotal.instancesBlindCountUnknown === 1 ? '' : 's'}, so the blind-spot total above may be
                      incomplete.
                    </>
                  )}
                  {portsTotal.instancesUnknown > 0 && (
                    <>
                      {' '}
                      {portsTotal.instancesUnknown} instance{portsTotal.instancesUnknown === 1 ? '' : 's'} not
                      reporting port inventory.
                    </>
                  )}
                </p>
                <ul className="list-plain">
                  {model.fpp.map((instance) => {
                    const count = findObservation(instance.observations, 'fpp.ports.count')
                    return (
                      <li key={instance.instanceId}>
                        <Link className="entity-link" to={`/fpp/${instance.instanceId}`}>
                          <strong>{instance.instanceId}</strong>{' '}
                          {typeof count?.value === 'number' && count.value === 0 ? (
                            // Step 5 review finding 6: this used to be a bare
                            // <span> with no state marker, so a zero-port
                            // reading of unknown age (the fpp-ghost ghost shape,
                            // one modelling decision away from this exact
                            // signal) rendered as confidently as a fresh one.
                            // A StatusBadge carries count.state's icon/tone
                            // alongside the same wording, matching
                            // FleetSignalBadge/PortGrid's established pattern
                            // for the same distinction.
                            <StatusBadge
                              tone={STATE_TONE[count.state]}
                              icon={STATE_ICON[count.state]}
                              label="reports no pixel output ports"
                            />
                          ) : (
                            <FleetSignalBadge label="ports" evidence={count} />
                          )}
                        </Link>
                      </li>
                    )
                  })}
                </ul>
              </>
            )}
          </section>
        </PanelErrorBoundary>

        <PanelErrorBoundary panelLabel="Network and MQTT state">
          <section className="panel">
            <h2 className="panel__title">Network / MQTT state</h2>
            {model.fpp.length === 0 ? (
              <p className="text-muted">No FPP instances are configured on this coordinator.</p>
            ) : (
              <ul className="list-plain">
                {model.fpp.map((instance) => (
                  <li key={instance.instanceId}>
                    <Link className="entity-link" to={`/fpp/${instance.instanceId}`}>
                      <strong>{instance.instanceId}</strong>{' '}
                      <FleetSignalBadge
                        label="MQTT configured"
                        evidence={findObservation(instance.observations, 'fpp.mqtt.configured')}
                      />{' '}
                      <FleetSignalBadge
                        label="MQTT connected"
                        evidence={findObservation(instance.observations, 'fpp.mqtt.connected')}
                      />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
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
    </div>
  )
}
