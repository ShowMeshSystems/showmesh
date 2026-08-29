import type { Model, Node, FPPInstance, ResolumeInstance } from '../../app/types'
import { findObservation } from '../../app/fppSignals'
import { ageMs, effectiveServerTimeIso, formatAge } from '../../app/time'
import type { StatusTone } from '../../components/StatusBadge'

// Fleet (UI-DESIGN-GUIDE.md section 3, Monitor.dc.html): ONE table across
// Node, FPP and Resolume, Kind as a column, replacing three separate
// lists (NodesList.tsx, FPPList.tsx, ResolumeView.tsx's own summary).
// `health` here is EXCLUSIVELY what the resource itself reported --
// rule 9 / DESIGN-DECISIONS-AND-API-FACTS.md item 15: "a ShowMesh-side
// binding problem is a separate signal and must never render as FPP's
// health." A node's declaration/discovery state and an FPP import's own
// held-binding state are therefore carried in `annotation`, a second,
// visually distinct line, never folded into `health`.
export type FleetKind = 'Node' | 'FPP' | 'Resolume'

export interface FleetHealthBadgeSpec {
  tone: StatusTone
  icon: string
  label: string
}

export interface FleetRow {
  key: string
  kind: FleetKind
  name: string
  href: string
  /** A second line under the name -- address, surface count, loaded file. Never a health verdict. */
  sub: string | null
  /** A ShowMesh-side annotation distinct from health (e.g. "bindings held"). Never merged into health. */
  annotation: string | null
  health: FleetHealthBadgeSpec
  /** ISO instant of the most recent report this resource has made, or null if none yet. */
  lastReportAt: string | null
}

const HEALTH_SPEC: Record<FPPInstance['health'], FleetHealthBadgeSpec> = {
  healthy: { tone: 'good', icon: '✓', label: 'Healthy' },
  degraded: { tone: 'warn', icon: '⚠', label: 'Degraded' },
  failed: { tone: 'bad', icon: '✕', label: 'Failed' },
  unknown: { tone: 'unknown', icon: '?', label: 'Unknown' },
  suppressed: { tone: 'unknown', icon: '⏸', label: 'Suppressed' },
}

const CONTROL_PLANE_SPEC: Record<Node['controlPlane']['state'], FleetHealthBadgeSpec> = {
  online: { tone: 'good', icon: '✓', label: 'Online' },
  offline: { tone: 'bad', icon: '✕', label: 'Offline' },
  unknown: { tone: 'unknown', icon: '?', label: 'Unknown' },
}

function nodeLastReport(node: Node): string | null {
  const candidates = [node.evidence.heartbeat, node.evidence.hello, node.evidence.lastWill]
    .map((e) => e.collectedAt)
    .filter((v): v is string => v !== null)
  if (candidates.length === 0) return null
  return candidates.sort().at(-1) ?? null
}

function nodeRow(node: Node): FleetRow {
  const surfaceIds = new Set(node.render.map((e) => e.resource.id))
  const hasAudio = node.audio.length > 0
  const sub = surfaceIds.size > 0
    ? `render · ${surfaceIds.size} surface${surfaceIds.size === 1 ? '' : 's'}`
    : hasAudio
      ? 'audio'
      : null
  return {
    key: `node:${node.nodeId}`,
    kind: 'Node',
    name: node.label ?? node.nodeId,
    href: `/monitor/fleet/node/${encodeURIComponent(node.nodeId)}`,
    sub,
    annotation: node.declaration.declared ? null : 'not declared',
    health: CONTROL_PLANE_SPEC[node.controlPlane.state] ?? { tone: 'unknown', icon: '?', label: 'Unknown' },
    lastReportAt: nodeLastReport(node),
  }
}

function fppRow(instance: FPPInstance): FleetRow {
  return {
    key: `fpp:${instance.instanceId}`,
    kind: 'FPP',
    name: instance.instanceId,
    href: `/monitor/fleet/fpp/${encodeURIComponent(instance.instanceId)}`,
    sub: instance.endpoint,
    // FPP's own health badge never carries a binding verdict (rule 9); a
    // pending, unacknowledged instance-uuid change is exactly such a
    // ShowMesh-side signal, so it renders here, separately.
    annotation: instance.instanceUuidChange !== null ? 'instance uuid change pending' : null,
    health: HEALTH_SPEC[instance.health] ?? { tone: 'unknown', icon: '?', label: 'Unknown' },
    lastReportAt: instance.lastPollAt,
  }
}

function resolumeRow(instance: ResolumeInstance): FleetRow {
  return {
    key: `resolume:${instance.instanceId}`,
    kind: 'Resolume',
    name: instance.instanceId,
    href: `/monitor/fleet/resolume/${encodeURIComponent(instance.instanceId)}`,
    sub: instance.composition === null ? 'no composition uploaded' : instance.composition.name,
    annotation: null,
    health: HEALTH_SPEC[instance.health] ?? { tone: 'unknown', icon: '?', label: 'Unknown' },
    lastReportAt: findObservation(instance.observations, 'resolume.reachable')?.collectedAt ?? null,
  }
}

export function buildFleetRows(model: Model): FleetRow[] {
  return [
    ...model.nodes.map(nodeRow),
    ...model.fpp.map(fppRow),
    ...model.resolume.map(resolumeRow),
  ]
}

export function formatLastReport(lastReportAt: string | null, model: Model): string {
  if (lastReportAt === null) return 'never reported'
  const reference = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const age = ageMs(lastReportAt, reference)
  if (age === null) return 'unknown'
  return `${formatAge(age)} ago`
}

// "Needs an operator" (Monitor.dc.html): the ruled strips above the Fleet
// table. Deliberately narrow -- an offline node (control-plane connection
// lost) and a ShowMesh-side binding/import problem held separately from
// FPP's own health, never a generic "not healthy" catch-all that would
// duplicate what the Fleet table and Activity stream already show.
export interface NeedsOperatorItem {
  key: string
  tone: 'bad' | 'warn'
  stateLabel: string
  headline: string
  headlineHref: string
  explanation: string
}

export function buildNeedsOperatorItems(model: Model): NeedsOperatorItem[] {
  const items: NeedsOperatorItem[] = []
  for (const node of model.nodes) {
    if (node.controlPlane.state === 'offline') {
      items.push({
        key: `node-offline:${node.nodeId}`,
        tone: 'bad',
        stateLabel: '✕ Offline',
        headline: `${node.label ?? node.nodeId} stopped reporting`,
        headlineHref: `/monitor/fleet/node/${encodeURIComponent(node.nodeId)}`,
        explanation: node.controlPlane.reason ?? 'Control-plane connection lost.',
      })
    }
  }
  for (const instance of model.fpp) {
    if (instance.instanceUuidChange !== null) {
      items.push({
        key: `fpp-uuid-change:${instance.instanceId}`,
        tone: 'warn',
        stateLabel: '⚠ Instance uuid changed',
        headline: `${instance.instanceId} reported a different hardware identity`,
        headlineHref: `/monitor/fleet/fpp/${encodeURIComponent(instance.instanceId)}`,
        explanation:
          'This looks like a rebuilt or replaced Pi. FPP itself is healthy; an operator must acknowledge before this is cleared.',
      })
    }
  }
  return items
}
