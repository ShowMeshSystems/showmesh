import { useEffect, useState } from 'react'
import { Link, NavLink, useNavigate, useSearchParams } from 'react-router-dom'
import {
  AttentionRow,
  Button,
  ConnectionPill,
  Panes,
  RuledStrip,
  Section,
  SelectableRow,
  StatTile,
  StatusPair,
  Table,
  TableWrap,
  Tiles,
  type Connection,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { effectiveServerTimeIso } from '../domain/time'
import { acknowledgeFPPInstanceUUIDChange, deleteFPPPlaylistEntryObservation, getFPPPlaylistEntryReconciliation, type FPPInstance, type Model } from '../api'
import { describeApiError, evaluateScope } from '../domain/session'
import { attentionItems, fleetCounts, fppDetail, nodesDetail } from './dashboardModel'
import {
  activityRows,
  facetCounts,
  fleetRows,
  fleetSummary,
  fppInspector,
  monitorConnection,
  nodeInspector,
  type FleetKind,
  type FleetRow,
} from './monitorModel'

const KINDS: readonly { value: FleetKind; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'node', label: 'Nodes' },
  { value: 'fpp', label: 'FPP' },
  { value: 'resolume', label: 'Resolume' },
]

const CONNECTION_LABEL: Record<Connection, string> = {
  live: 'Coordinator live',
  degraded: 'Coordinator degraded',
  lost: 'Coordinator lost',
  unknown: 'Coordinator unknown',
}

export function MonitorFacets({ counts }: { counts: { fleet: number; signals: number; capabilities: number } }) {
  return (
    <nav className="sm-facets" aria-label="Monitor facets">
      <NavLink to="/monitor/fleet" className="sm-facets__tab">
        Fleet<span className="sm-facets__count">{counts.fleet}</span>
      </NavLink>
      <NavLink to="/monitor/signals" className="sm-facets__tab">
        Signals<span className="sm-facets__count">{counts.signals}</span>
      </NavLink>
      <NavLink to="/monitor/activity" className="sm-facets__tab">
        Activity
      </NavLink>
      <NavLink to="/monitor/capabilities" className="sm-facets__tab">
        Capabilities<span className="sm-facets__count">{counts.capabilities}</span>
      </NavLink>
      <NavLink to="/monitor/manifest" className="sm-facets__tab">
        Manifest
      </NavLink>
    </nav>
  )
}

/** The page head and facet tabs every Monitor facet shares. */
export function MonitorHead({ model }: { model: Model }) {
  const connection = monitorConnection(model)
  return (
    <>
      <div className="sm-page__head">
        <div>
          <h1 className="sm-page__title">Monitor</h1>
          <p className="sm-page__lede">
            Every resource the coordinator observes, in one place. Health is what was reported, never what was assumed.
          </p>
        </div>
        <ConnectionPill state={connection} label={CONNECTION_LABEL[connection]} />
      </div>

      <MonitorFacets counts={facetCounts(model)} />
    </>
  )
}

export function Monitor() {
  const model = useModelContext()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const [kind, setKind] = useState<FleetKind>('all')
  const [searchParams, setSearchParams] = useSearchParams()
  const [selected, setSelected] = useState<string | null>(() => searchParams.get('resource'))

  const counts = fleetCounts(model)
  const rows = fleetRows(model, nowIso)
  const shown = kind === 'all' ? rows : rows.filter((row) => row.kind === kind)
  const items = attentionItems(model, nowIso)
  const activity = activityRows(model.events, 5)

  const selectedNode = model.nodes.find((node) => `node:${node.nodeId}` === selected)
  const selectedFpp = model.fpp.find((instance) => `fpp:${instance.instanceId}` === selected)
  const select = (key: string) => {
    const next = key === selected ? null : key
    setSelected(next)
    setSearchParams(next === null ? {} : { resource: next })
  }

  return (
    <div className="sm-monitor">
      <MonitorHead model={model} />

      <Panes>
        <div>
          <Tiles>
            <StatTile label="Nodes" value={`${counts.nodesOnline} / ${counts.nodesTotal}`} detail={nodesDetail(counts)} />
            <StatTile label="FPP players" value={`${counts.fppHealthy} / ${counts.fppTotal}`} detail={fppDetail(model.fpp)} />
            <StatTile
              label="Resolume"
              value={`${counts.resolumeHealthy} / ${counts.resolumeTotal}`}
              detail={counts.resolumeTotal === 0 ? 'none configured' : 'healthy'}
            />
            <StatTile
              label="Signals current"
              value={`${counts.signals.current} / ${counts.signals.total}`}
              detail={
                counts.signals.total === 0
                  ? 'nothing collected yet'
                  : `${counts.signals.stale} stale · ${counts.signals.unobserved} unobserved`
              }
              to="/monitor/signals"
            />
          </Tiles>

          <Section id="mo-attention" title="Needs an operator">
            {items.length === 0 ? (
              <RuledStrip
                absence="empty"
                label="Clear"
                fact="Nothing has asked for an operator."
                detail="That is not proof the show looks right, only that nothing has asked for you."
              />
            ) : (
              items.map((item) => (
                <AttentionRow
                  key={item.key}
                  tone={item.tone}
                  state={item.state}
                  fact={
                    <>
                      <Link to={item.to}>{item.subject}</Link> {item.fact}
                    </>
                  }
                  detail={item.detail}
                />
              ))
            )}
          </Section>

          <Section
            id="mo-fleet"
            title="Fleet"
            aside={
              <div className="sm-segmented" role="group" aria-label="Fleet kind">
                {KINDS.map((option) => (
                  <button
                    key={option.value}
                    type="button"
                    className="sm-segmented__item"
                    aria-pressed={option.value === kind}
                    onClick={() => setKind(option.value)}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
            }
            detail="One table instead of three lists. Kind is a column, because the question is about the installation, not about a resource type."
          >
            {shown.length === 0 ? (
              <RuledStrip
                absence="empty"
                label="None"
                fact={kind === 'all' ? 'No resource is configured or declared.' : `No ${kind} resource is configured or declared.`}
              />
            ) : (
              <>
                <TableWrap label="Fleet resources, scrollable">
                  <Table>
                    <thead>
                      <tr>
                        <th scope="col">Resource</th>
                        <th scope="col">Kind</th>
                        <th scope="col">Health</th>
                        <th scope="col">Last report</th>
                      </tr>
                    </thead>
                    <tbody>
                      {shown.map((row) => (
                        <FleetTableRow
                          key={row.key}
                          row={row}
                          selected={row.key === selected}
                          onSelect={() => select(row.key)}
                        />
                      ))}
                    </tbody>
                  </Table>
                </TableWrap>
                <p className="sm-section__footnote">{fleetSummary(rows)}</p>
              </>
            )}
          </Section>

          <Section
            id="mo-activity"
            title="Activity"
            aside={<Link to="/monitor/activity">Full history →</Link>}
            detail="System events and operator actions in one stream: you usually need to know both, in order."
          >
            {activity.length === 0 ? (
              <RuledStrip
                absence={model.snapshotReceivedAt === null ? 'loading' : 'empty'}
                label={model.snapshotReceivedAt === null ? 'Reading' : 'Empty'}
                fact={model.snapshotReceivedAt === null ? 'No event history has arrived yet.' : 'No event has been recorded.'}
              />
            ) : (
              <>
                <TableWrap label="Recent activity, scrollable">
                  <Table>
                    <thead>
                      <tr>
                        <th scope="col">Time</th>
                        <th scope="col">What happened</th>
                        <th scope="col">Source</th>
                      </tr>
                    </thead>
                    <tbody>
                      {activity.map((row) => (
                        <tr key={row.key}>
                          <td className="sm-data">{row.time}</td>
                          <td>
                            {row.state !== null && <StatusPair tone={row.tone} label={row.state} />}
                            {row.summary}
                          </td>
                          <td>{row.source}</td>
                        </tr>
                      ))}
                    </tbody>
                  </Table>
                </TableWrap>
                <p className="sm-section__footnote">
                  Operator actions are audit records and need an audit-read scope; system events do not.
                  {model.eventsGap && ' Some history is permanently lost to retention, so this stream has a gap.'}
                </p>
              </>
            )}
          </Section>
        </div>

        <aside>
          {selectedNode !== undefined ? (
            <Inspector nodeKey={selected ?? ''} node={selectedNode} nowIso={nowIso} />
          ) : selectedFpp !== undefined ? (
            <FppInspector instance={selectedFpp} nowIso={nowIso} />
          ) : (
            <RuledStrip
              absence="empty"
              label="Nothing selected"
              fact="Select a resource for its full evidence."
              detail="FPP transport stays in Live Control. Resolume configuration has its own screen."
            />
          )}
        </aside>
      </Panes>
    </div>
  )
}

function FleetTableRow({ row, selected, onSelect }: { row: FleetRow; selected: boolean; onSelect: () => void }) {
  const navigate = useNavigate()
  if (row.kind !== 'node' && row.kind !== 'fpp') {
    return (
      <SelectableRow onActivate={() => navigate(row.to)} ariaLabel={`Open ${row.name}`}>
        <td><strong>{row.name}</strong><br /><span className="sm-small sm-faint">{row.detail}</span></td>
        <td>{row.kindLabel}</td>
        <td><StatusPair tone={row.tone} label={row.health} />{row.healthNote !== null && row.healthNote !== '' && <><br /><span className="sm-small sm-faint">{row.healthNote}</span></>}</td>
        <td className="sm-data">{row.lastReport}</td>
      </SelectableRow>
    )
  }
  return (
    <SelectableRow selected={selected} onActivate={onSelect} ariaLabel={`View ${row.name}`}>
      <td>
        <strong>{row.name}</strong>
        {selected && <span className="sm-viewing">Viewing</span>}
        <br />
        <span className="sm-small sm-faint">{row.detail}</span>
      </td>
      <td>{row.kindLabel}</td>
      <td>
        <StatusPair tone={row.tone} label={row.health} />
        {row.healthNote !== null && row.healthNote !== '' && (
          <>
            <br />
            <span className="sm-small sm-faint">{row.healthNote}</span>
          </>
        )}
      </td>
      <td className="sm-data">{row.lastReport}</td>
    </SelectableRow>
  )
}

function FppInspector({ instance, nowIso }: { instance: FPPInstance; nowIso: string | null }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const recoveryGate = evaluateScope(model.session, model.sessionFetchFailed, 'fpp:command')
  const inspector = fppInspector(instance, nowIso)
  const [acknowledging, setAcknowledging] = useState(false)
  const [ackError, setAckError] = useState<string | null>(null)
  const [clearingObservation, setClearingObservation] = useState(false)
  const [clearError, setClearError] = useState<string | null>(null)
  const [reconciliation, setReconciliation] = useState<{ outcome: string; reason: string } | null>(null)
  const [reconciliationError, setReconciliationError] = useState<string | null>(null)
  useEffect(() => {
    if (instance.instanceUuid === null) { setReconciliation(null); return }
    let cancelled = false
    getFPPPlaylistEntryReconciliation(instance.instanceUuid).then((response) => { if (!cancelled) setReconciliation(response) }).catch((err: unknown) => { if (!cancelled) setReconciliationError(describeApiError(err)) })
    return () => { cancelled = true }
  }, [instance.instanceUuid])
  const acknowledge = () => {
    setAcknowledging(true)
    setAckError(null)
    acknowledgeFPPInstanceUUIDChange(instance.instanceId)
      .catch((err: unknown) => setAckError(describeApiError(err)))
      .finally(() => setAcknowledging(false))
  }
  const clearObservation = () => {
    if (instance.instanceUuid === null) return
    setClearingObservation(true); setClearError(null)
    deleteFPPPlaylistEntryObservation(instance.instanceUuid).catch((err: unknown) => setClearError(describeApiError(err))).finally(() => setClearingObservation(false))
  }
  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow">FPP</p>
      <h2 className="sm-inspector__title">{inspector.title}</h2>
      <p className="sm-small sm-muted">{inspector.subtitle}</p>
      {inspector.groups.map((group) => (
        <section key={group.name} aria-labelledby={`inspect-fpp-${group.name}`} className="sm-inspector__group">
          <h3 id={`inspect-fpp-${group.name}`} className="sm-subsection__title">
            {group.name}
          </h3>
          {group.absent !== null ? (
            <RuledStrip absence="unobserved" label="Never reported" fact="Nothing to observe" detail={group.absent} />
          ) : (
            group.rows.map((row) => (
              <div key={row.key} className="sm-inspector__row">
                <span className="sm-inspector__label sm-data">{row.label}</span>
                <div>
                  <p className="sm-inspector__value sm-data">{row.value}</p>
                  {row.state !== null && <p className="sm-inspector__state"><StatusPair tone={row.tone} label={row.state} /></p>}
                  {row.detail !== null && <p className="sm-inspector__detail">{row.detail}</p>}
                </div>
              </div>
            ))
          )}
        </section>
      ))}
      <div className="sm-inspector__actions">
        <Link to="/control">Open Live Control</Link>
        <Link to="/monitor/signals">All signals</Link>
        {instance.instanceUuidChange !== null && (
          <Button disabled={!gate.allowed || acknowledging} title={gate.allowed ? undefined : gate.reason} onClick={acknowledge}>
            {acknowledging ? 'Acknowledging…' : 'Acknowledge replacement'}
          </Button>
        )}
        <Button disabled={!recoveryGate.allowed || clearingObservation || instance.instanceUuid === null} title={!recoveryGate.allowed ? recoveryGate.reason : instance.instanceUuid === null ? 'FPP has not reported an instance UUID.' : 'Clears only this FPP UUID’s stored playlist-entry observation and sequence anchor.'} onClick={clearObservation}>
          {clearingObservation ? 'Clearing observation…' : 'Clear playlist observation'}
        </Button>
      </div>
      {ackError !== null && <RuledStrip absence="failed" label="Acknowledgement refused" fact={ackError} />}
      {clearError !== null && <RuledStrip absence="failed" label="Observation reset refused" fact={clearError} />}
      {reconciliation !== null && <div className="sm-outcome"><StatusPair tone={reconciliation.outcome === 'resolved' ? 'good' : 'warn'} label={reconciliation.outcome.replaceAll('-', ' ')} /><p className="sm-outcome__detail">{reconciliation.reason}</p></div>}
      {reconciliationError !== null && <RuledStrip absence="failed" label="Reconciliation unavailable" fact={reconciliationError} />}
    </div>
  )
}

function Inspector({ nodeKey, node, nowIso }: { nodeKey: string; node: Parameters<typeof nodeInspector>[0]; nowIso: string | null }) {
  const inspector = nodeInspector(node, nowIso)
  const signals = node.render.length + node.audio.length + node.fppConnect.length

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow">Node</p>
      <h2 className="sm-inspector__title" id={`inspect-${nodeKey}`}>
        {inspector.title}
      </h2>
      <p className="sm-small sm-muted">{inspector.subtitle}</p>
      {inspector.groups.map((group) => (
        <section key={group.name} aria-labelledby={`inspect-${nodeKey}-${group.name}`} className="sm-inspector__group">
          <h3 id={`inspect-${nodeKey}-${group.name}`} className="sm-subsection__title">
            {group.name}
          </h3>
          {group.absent !== null ? (
            <RuledStrip absence="unobserved" label="Never advertised" fact="Nothing to observe" detail={group.absent} />
          ) : (
            group.rows.map((row) => (
              <div key={row.key} className="sm-inspector__row">
                <span className="sm-inspector__label sm-data">{row.label}</span>
                <div>
                  <p className="sm-inspector__value sm-data">{row.value}</p>
                  {row.state !== null && (
                    <p className="sm-inspector__state">
                      <StatusPair tone={row.tone} label={row.state} />
                    </p>
                  )}
                  {row.detail !== null && <p className="sm-inspector__detail">{row.detail}</p>}
                </div>
              </div>
            ))
          )}
        </section>
      ))}
      <div className="sm-inspector__actions">
        <Link to={`/monitor/fleet/node/${node.nodeId}`}>All {signals} signals</Link>
        <Button disabled title="This coordinator advertises no discovery command for a single node.">
          Run discovery
        </Button>
      </div>
    </div>
  )
}
