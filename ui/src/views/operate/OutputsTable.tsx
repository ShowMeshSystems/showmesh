import { Link } from 'react-router-dom'
import { StatusBadge } from '../../components/StatusBadge'
import { UnavailableBlock, UnobservedBlock } from '../../components/SharedLayouts'
import { STATE_ICON, STATE_LABEL, STATE_TONE, formatValue } from '../../app/evidenceState'
import { ageMs, effectiveServerTimeIso, formatAge } from '../../app/time'
import type { Node, ObservationEntry } from '../../app/types'

// "What each output is doing" (Live Control.dc.html): a per-output row of
// doing-what plus evidence, with a roll-up sentence. The mock names six
// fixed outputs (front projection, side wall, garage door, program audio,
// LTC, background bed) by role; this coordinator's API does not label a
// render or audio observation with a functional role, only a resource id
// and a signal name (Node.render / Node.audio, both ObservationEntry[]
// per node — see this coordinator's schema.d.ts Node.render/.audio
// description). So this table is data-driven from every currently
// observed render and audio signal, one row per resource, rather than six
// invented named rows — the resource id and node are the identifying
// fact the API actually gives.
type OutputRow = { key: string; nodeLabel: string; resourceId: string; kind: 'Render' | 'Audio'; entry: ObservationEntry }

function rowsForNode(node: Node): OutputRow[] {
  const label = node.label ?? node.nodeId
  const render = node.render
    .filter((entry) => entry.signal === 'surface.pipeline.state')
    .map((entry) => ({ key: `${node.nodeId}-render-${entry.resource.id}`, nodeLabel: label, resourceId: entry.resource.id, kind: 'Render' as const, entry }))
  const audio = node.audio.map((entry) => ({
    key: `${node.nodeId}-audio-${entry.resource.id}-${entry.signal}`,
    nodeLabel: label,
    resourceId: entry.resource.id,
    kind: 'Audio' as const,
    entry,
  }))
  return [...render, ...audio]
}

function EvidenceCell({ entry, serverTime, serverTimeReceivedAt }: { entry: ObservationEntry; serverTime: string | null; serverTimeReceivedAt: number | null }) {
  const reference = effectiveServerTimeIso(serverTime, serverTimeReceivedAt, Date.now())
  const age = ageMs(entry.observedAt, reference)
  return (
    <div className="outputs-table__evidence">
      <StatusBadge tone={STATE_TONE[entry.state]} icon={STATE_ICON[entry.state]} label={STATE_LABEL[entry.state]} />
      <span className="t-small text-muted">{age !== null ? formatAge(age) : entry.observedAt === null ? 'never observed' : 'age unknown'}</span>
    </div>
  )
}

export function OutputsTable({
  nodes,
  serverTime,
  serverTimeReceivedAt,
  snapshotReceivedAt,
}: {
  nodes: Node[]
  serverTime: string | null
  serverTimeReceivedAt: number | null
  snapshotReceivedAt: number | null
}) {
  if (snapshotReceivedAt === null) {
    return <UnobservedBlock title="What each output is doing" reason="No coordinator snapshot has been received yet." headingLevel={3} />
  }
  const rows = nodes.flatMap(rowsForNode)
  if (rows.length === 0) {
    return <UnavailableBlock title="What each output is doing" reason="No node currently reports a render or audio observation." headingLevel={3} />
  }
  const confirming = rows.filter((row) => row.entry.state === 'current').length
  return (
    <div className="outputs-table">
      <div className="table-wrap">
        <table className="table">
          <thead>
            <tr>
              <th scope="col">Node</th>
              <th scope="col">Output</th>
              <th scope="col">Doing what</th>
              <th scope="col">Evidence</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((row) => (
              <tr key={row.key}>
                <td className="t-data">{row.nodeLabel}</td>
                <td>
                  <strong>{row.resourceId}</strong>
                  <span className="t-small text-muted"> · {row.kind}</span>
                </td>
                <td className="t-data">
                  {row.entry.value === null ? 'no value' : formatValue(row.entry.value, row.entry.unit)}
                </td>
                <td>
                  <EvidenceCell entry={row.entry} serverTime={serverTime} serverTimeReceivedAt={serverTimeReceivedAt} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      <p className="outputs-table__rollup t-small text-muted">
        {confirming} of {rows.length} output{rows.length === 1 ? '' : 's'} confirm current evidence.{' '}
        <Link to="/monitor/fleet">Open Fleet →</Link>
      </p>
    </div>
  )
}
