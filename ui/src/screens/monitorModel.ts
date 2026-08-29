import type { Event, Model, Node } from '../api'
import type { Tone } from '../kit'
import { countSignals } from '../domain/evidence'
import { ageMs, formatClock, formatDuration } from '../domain/time'

export type FleetKind = 'all' | 'node' | 'fpp' | 'resolume'

export type FleetRow = {
  key: string
  kind: Exclude<FleetKind, 'all'>
  kindLabel: string
  name: string
  detail: string
  tone: Tone
  health: string
  healthNote: string | null
  lastReport: string
  to: string
}

const NODE_TONE: Record<string, Tone> = { online: 'good', offline: 'bad', unknown: 'unknown' }
const HEALTH_TONE: Record<string, Tone> = {
  healthy: 'good',
  degraded: 'warn',
  failed: 'bad',
  unknown: 'unknown',
  suppressed: 'pending',
}

function nodeDetail(node: Node): string {
  const parts: string[] = []
  const surfaces = new Set(node.render.filter((entry) => entry.resource.kind === 'surface').map((entry) => entry.resource.id))
  if (surfaces.size > 0) parts.push(`render · ${surfaces.size} ${surfaces.size === 1 ? 'surface' : 'surfaces'}`)
  if (node.audio.length > 0) parts.push('audio')
  const capabilities = node.capabilities.map((capability) => capability.id)
  if (parts.length === 0 && capabilities.length > 0) parts.push(capabilities.join(' · '))
  return parts.length === 0 ? 'no capability advertised' : parts.join(' · ')
}

function lastReport(iso: string | null, nowIso: string | null): string {
  const age = ageMs(iso, nowIso)
  if (age === null) return 'never'
  return `${formatDuration(age)} ago`
}

/**
 * One table instead of three lists. Kind is a column, because the question an
 * operator asks is about the installation, not about a resource type.
 */
export function fleetRows(model: Model, nowIso: string | null): FleetRow[] {
  const rows: FleetRow[] = []

  for (const instance of model.fpp) {
    rows.push({
      key: `fpp:${instance.instanceId}`,
      kind: 'fpp',
      kindLabel: 'FPP',
      name: instance.instanceId,
      detail: instance.instanceUuidChange === null ? instance.endpoint : `${instance.endpoint} · bindings held`,
      tone: HEALTH_TONE[instance.health] ?? 'unknown',
      health: instance.health,
      healthNote: 'as FPP reports',
      lastReport: lastReport(instance.lastPollAt, nowIso),
      to: `/monitor/fleet/fpp/${instance.instanceId}`,
    })
  }

  for (const node of model.nodes) {
    const heard = node.evidence.heartbeat.observedAt ?? node.evidence.hello.observedAt
    rows.push({
      key: `node:${node.nodeId}`,
      kind: 'node',
      kindLabel: 'Node',
      name: node.label ?? node.nodeId,
      detail: nodeDetail(node),
      tone: NODE_TONE[node.controlPlane.state] ?? 'unknown',
      health: node.controlPlane.state,
      healthNote: node.controlPlane.reason,
      lastReport: lastReport(heard, nowIso),
      to: `/monitor/fleet/node/${node.nodeId}`,
    })
  }

  for (const instance of model.resolume) {
    rows.push({
      key: `resolume:${instance.instanceId}`,
      kind: 'resolume',
      kindLabel: 'Resolume',
      name: instance.instanceId,
      detail: instance.composition?.name ?? 'no composition stored',
      tone: HEALTH_TONE[instance.health] ?? 'unknown',
      health: instance.health,
      healthNote: 'as Arena reports',
      lastReport: lastReport(
        instance.observations.reduce<string | null>(
          (latest, entry) => (entry.observedAt !== null && (latest === null || entry.observedAt > latest) ? entry.observedAt : latest),
          null,
        ),
        nowIso,
      ),
      to: `/monitor/fleet/resolume/${instance.instanceId}`,
    })
  }

  return rows
}

export function fleetSummary(rows: readonly FleetRow[]): string {
  const nodes = rows.filter((row) => row.kind === 'node').length
  const fpp = rows.filter((row) => row.kind === 'fpp').length
  const resolume = rows.filter((row) => row.kind === 'resolume').length
  const parts = [
    `${nodes} ${nodes === 1 ? 'node' : 'nodes'}`,
    `${fpp} FPP ${fpp === 1 ? 'player' : 'players'}`,
    `${resolume} Resolume ${resolume === 1 ? 'instance' : 'instances'}`,
  ]
  return `${rows.length} resources · ${parts.join(', ')}. Health is each resource's own report; binding and import problems are separate signals.`
}

const SEVERITY_TONE: Record<Event['severity'], Tone> = {
  informational: 'pending',
  warning: 'warn',
  critical: 'bad',
}

export type ActivityRow = {
  key: string
  time: string
  summary: string
  source: string
  tone: Tone
}

export function activityRows(events: readonly Event[], limit: number): ActivityRow[] {
  return events.slice(0, limit).map((event) => ({
    key: String(event.seq),
    time: formatClock(event.occurredAt ?? event.recordedAt) ?? 'unrecorded',
    summary: event.summary,
    source: event.source,
    tone: SEVERITY_TONE[event.severity],
  }))
}

export type FacetCounts = { fleet: number; signals: number; capabilities: number }

/** Tab counts are inventory counts of the tab's primary object. */
export function facetCounts(model: Model): FacetCounts {
  const signals = countSignals([
    ...model.nodes.flatMap((node) => [node.render, node.audio, node.fppConnect]),
    ...model.fpp.map((instance) => instance.observations),
    ...model.resolume.map((instance) => instance.observations),
  ])
  const capabilities = new Set(model.nodes.flatMap((node) => node.capabilities.map((capability) => capability.id)))
  return {
    fleet: model.nodes.length + model.fpp.length + model.resolume.length,
    signals: signals.total,
    capabilities: capabilities.size,
  }
}

export type InspectorRow = {
  key: string
  label: string
  /** The value itself, never wrapped in a status chip: a value is not a state. */
  value: string
  /** The state word, only when the value is not currently confirmed. */
  state: string | null
  detail: string | null
  tone: Tone
}

/**
 * What one node is reporting, grouped the way the node reports it. A group
 * with no observations says the capability was never advertised, which is
 * not the same as a path that is failing.
 */
export function nodeInspector(node: Node, nowIso: string | null): { title: string; subtitle: string; groups: { name: string; rows: InspectorRow[]; absent: string | null }[] } {
  const heard = node.evidence.heartbeat.observedAt ?? node.evidence.hello.observedAt
  const age = ageMs(heard, nowIso)
  const control: InspectorRow[] = [
    {
      key: 'control-plane',
      label: 'Control plane',
      value: node.controlPlane.state,
      state: null,
      detail: node.controlPlane.reason,
      tone: NODE_TONE[node.controlPlane.state] ?? 'unknown',
    },
    {
      key: 'agent',
      label: 'Agent build',
      value: node.agentVersion ?? 'not reported',
      state: null,
      detail: 'As of the last report, not now.',
      tone: node.agentVersion === null ? 'unknown' : 'pending',
    },
  ]

  const observationRows = (entries: Node['render'], prefix: string): InspectorRow[] =>
    entries.slice(0, 6).map((entry, index) => ({
      key: `${prefix}:${entry.signal}:${index}`,
      label: entry.signal,
      value: entry.value === null ? 'no value' : String(entry.value),
      state:
        entry.state === 'current'
          ? null
          : `${entry.state.replace('_', ' ')}${entry.observedAt === null ? '' : ` · ${formatClock(entry.observedAt) ?? ''}`}`,
      detail: entry.state === 'current' ? null : entry.reason,
      tone:
        entry.state === 'current'
          ? 'good'
          : entry.state === 'stale'
            ? 'warn'
            : entry.state === 'collection_failed'
              ? 'bad'
              : entry.state === 'not_collected'
                ? 'unknown'
                : 'pending',
    }))

  return {
    title: node.label ?? node.nodeId,
    subtitle:
      node.controlPlane.state === 'online'
        ? `Online · last report ${age === null ? 'never' : `${formatDuration(age)} ago`}`
        : `${node.controlPlane.state} since ${formatClock(heard) ?? 'an unrecorded time'}${age === null ? '' : ` · ${formatDuration(age)}`}`,
    groups: [
      { name: 'Node', rows: control, absent: null },
      {
        name: 'Render',
        rows: observationRows(node.render, 'render'),
        absent:
          node.render.length === 0
            ? 'This node has never published a render observation. That is not the same as a render path that is failing.'
            : null,
      },
      {
        name: 'Audio',
        rows: observationRows(node.audio, 'audio'),
        absent:
          node.audio.length === 0
            ? 'This node has never claimed an audio capability, so there is nothing to observe. Distinct from an audio path that is failing.'
            : null,
      },
    ],
  }
}
