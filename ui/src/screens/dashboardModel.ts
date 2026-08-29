import type { FPPInstance, Model, Node, NightSessionState, ResolumeInstance } from '../api'
import type { Tone } from '../kit'
import { countSignals, type SignalCounts } from '../domain/evidence'
import { ageMs, formatClock, formatDuration } from '../domain/time'

export type AttentionItem = {
  key: string
  tone: Tone
  state: string
  fact: string
  subject: string
  to: string
  detail: string
}

/**
 * Health is each resource's own report. This never invents a verdict: an
 * instance the coordinator calls `unknown` is reported as unknown, never
 * folded into healthy.
 */
export function nodeAttention(nodes: readonly Node[], nowIso: string | null): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const node of nodes) {
    const to = `/monitor/fleet/node/${node.nodeId}`
    const lastHeard = node.evidence.heartbeat.observedAt ?? node.evidence.hello.observedAt
    const age = ageMs(lastHeard, nowIso)
    if (node.controlPlane.state === 'offline') {
      items.push({
        key: `node:${node.nodeId}`,
        tone: 'bad',
        state: age === null ? 'Offline' : `Offline · ${formatDuration(age)}`,
        subject: node.label ?? node.nodeId,
        fact: lastHeard === null ? 'stopped reporting' : `stopped reporting at ${formatClock(lastHeard) ?? 'an unknown time'}`,
        to,
        detail: node.controlPlane.reason ?? 'The coordinator has heard nothing from this node since.',
      })
    } else if (node.controlPlane.state === 'unknown') {
      items.push({
        key: `node:${node.nodeId}`,
        tone: 'unknown',
        state: 'Unknown',
        subject: node.label ?? node.nodeId,
        fact: 'is neither online nor confirmed offline',
        to,
        detail:
          node.controlPlane.reason ??
          'The coordinator cannot currently say whether this node is reporting. Unknown is not a soft online.',
      })
    }
  }
  return items
}

const HEALTH_TONE: Record<string, Tone> = { failed: 'bad', degraded: 'warn', unknown: 'unknown' }
const HEALTH_STATE: Record<string, string> = { failed: 'Failed', degraded: 'Degraded', unknown: 'Unknown' }

export function fppAttention(instances: readonly FPPInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    const to = `/monitor/fleet/fpp/${instance.instanceId}`
    const tone = HEALTH_TONE[instance.health]
    if (tone !== undefined) {
      items.push({
        key: `fpp:${instance.instanceId}`,
        tone,
        state: HEALTH_STATE[instance.health] ?? instance.health,
        subject: instance.instanceId,
        fact: `is reporting ${instance.health}`,
        to,
        detail: instance.lastPollError ?? 'This is the FPP instance’s own health, as the coordinator last read it.',
      })
    }
    if (instance.instanceUuidChange !== null) {
      items.push({
        key: `fpp-uuid:${instance.instanceId}`,
        tone: 'warn',
        state: 'Bindings held',
        subject: instance.instanceId,
        fact: 'changed its instance identity',
        to,
        detail:
          'Bindings that name this instance are held rather than guessed until the change is acknowledged. FPP itself may be healthy.',
      })
    }
  }
  return items
}

export function resolumeAttention(instances: readonly ResolumeInstance[]): AttentionItem[] {
  const items: AttentionItem[] = []
  for (const instance of instances) {
    const tone = HEALTH_TONE[instance.health]
    if (tone === undefined) continue
    items.push({
      key: `resolume:${instance.instanceId}`,
      tone,
      state: HEALTH_STATE[instance.health] ?? instance.health,
      subject: instance.instanceId,
      fact: `is reporting ${instance.health}`,
      to: '/monitor/fleet/resolume',
      detail: 'This is what Arena reports about itself, not a ShowMesh-side verdict.',
    })
  }
  return items
}

const TONE_ORDER: Record<Tone, number> = { bad: 0, warn: 1, unknown: 2, pending: 3, good: 4 }

export function attentionItems(model: Model, nowIso: string | null): AttentionItem[] {
  return [...nodeAttention(model.nodes, nowIso), ...fppAttention(model.fpp), ...resolumeAttention(model.resolume)].sort(
    (a, b) => TONE_ORDER[a.tone] - TONE_ORDER[b.tone],
  )
}

export type FleetCounts = {
  nodesOnline: number
  nodesTotal: number
  nodesUnknown: number
  fppHealthy: number
  fppTotal: number
  resolumeHealthy: number
  resolumeTotal: number
  signals: SignalCounts
}

export function fleetCounts(model: Model): FleetCounts {
  const signalGroups = [
    ...model.nodes.flatMap((node) => [node.render, node.audio, node.fppConnect]),
    ...model.fpp.map((instance) => instance.observations),
    ...model.resolume.map((instance) => instance.observations),
  ]
  return {
    nodesOnline: model.nodes.filter((node) => node.controlPlane.state === 'online').length,
    nodesTotal: model.nodes.length,
    nodesUnknown: model.nodes.filter((node) => node.controlPlane.state === 'unknown').length,
    fppHealthy: model.fpp.filter((instance) => instance.health === 'healthy').length,
    fppTotal: model.fpp.length,
    resolumeHealthy: model.resolume.filter((instance) => instance.health === 'healthy').length,
    resolumeTotal: model.resolume.length,
    signals: countSignals(signalGroups),
  }
}

export type ReadinessVerdict = {
  tone: Tone
  state: string
  fact: string
  detail: string | null
  /** The night command this verdict withholds, rendered as the identifier it is. */
  gated?: boolean
  action: boolean
}

/**
 * The next start's readiness, as the coordinator recorded it. A readiness
 * run from an earlier epoch, or one that is not fresh, gates the start: it
 * is not a soft pass.
 */
export function nextStartVerdict(session: NightSessionState | null, nowIso: string | null): ReadinessVerdict | null {
  if (session === null) return null
  const readiness = session.readiness
  if (readiness.state === 'not_configured') {
    return {
      tone: 'unknown',
      state: 'Not configured',
      fact: 'No night session is configured, so nothing has readiness to report.',
      detail: readiness.reason !== '' ? readiness.reason : null,
      action: false,
    }
  }
  if (readiness.state !== 'recorded' || readiness.outcome === undefined) {
    return {
      tone: 'unknown',
      state: 'Readiness unknown',
      fact: 'No readiness result has been recorded for this session.',
      detail: readiness.reason !== '' ? readiness.reason : 'Unknown is never a pass. start-night is withheld.',
      action: true,
    }
  }
  const passed = readiness.checks.filter((check) => check.state === 'healthy').length
  const at = formatClock(readiness.completedAt ?? null)
  const age = ageMs(readiness.completedAt ?? null, nowIso)
  const when = at === null ? 'at an unrecorded time' : `at ${at}`
  const ago = age === null ? '' : `, ${formatDuration(age)} ago`

  if (readiness.outcome === 'ready' && readiness.fresh && readiness.sameEpoch) {
    return {
      tone: 'good',
      state: 'Next start clear',
      fact: `Readiness passed ${when}${ago}, from this epoch. ${passed} of ${readiness.checks.length} checks.`,
      detail: null,
      action: false,
    }
  }
  const why: string[] = []
  if (readiness.outcome !== 'ready') why.push(`the last run reported ${readiness.outcome}`)
  if (!readiness.sameEpoch) why.push('it ran in an earlier epoch')
  if (!readiness.fresh) why.push('it is no longer fresh')
  return {
    tone: 'warn',
    state: 'Next start gated',
    fact: `Readiness last ran ${when}${ago}, and ${why.join(', ')}.`,
    detail: null,
    gated: true,
    action: true,
  }
}

const RUNNING_STATES = new Set(['live', 'transition-to-show', 'fading-out'])

export function showInProgress(session: NightSessionState | null): ReadinessVerdict | null {
  if (session === null) return null
  if (session.degraded) {
    return {
      tone: 'warn',
      state: 'Degraded',
      fact: 'The show is running with something missing.',
      detail: session.degradedReason ?? 'The controller reported degraded without a reason.',
      action: false,
    }
  }
  if (RUNNING_STATES.has(session.state)) {
    return {
      tone: 'good',
      state: 'Running',
      fact: 'The show in progress has every output it needs. Nothing on this page will interrupt it.',
      detail: `Cycle ${session.cycle} · ${session.state}`,
      action: false,
    }
  }
  return {
    tone: 'unknown',
    state: session.state,
    fact: 'No show is in progress.',
    detail: `The night session has been ${session.state} since ${formatClock(session.stateEnteredAt) ?? 'an unrecorded time'}.`,
    action: false,
  }
}

export function nodesDetail(counts: FleetCounts): string {
  if (counts.nodesTotal === 0) return 'none declared'
  const offline = counts.nodesTotal - counts.nodesOnline - counts.nodesUnknown
  const parts: string[] = []
  if (offline > 0) parts.push(`${offline} offline`)
  if (counts.nodesUnknown > 0) parts.push(`${counts.nodesUnknown} unknown`)
  return parts.length === 0 ? 'all online' : parts.join(' · ')
}

export function fppDetail(instances: readonly FPPInstance[]): string {
  if (instances.length === 0) return 'none configured'
  const held = instances.filter((instance) => instance.instanceUuidChange !== null).length
  const unhealthy = instances.filter((instance) => instance.health !== 'healthy').length
  const parts = [unhealthy === 0 ? 'healthy' : `${unhealthy} not healthy`]
  if (held > 0) parts.push(`${held} import held`)
  return parts.join(' · ')
}

/**
 * One offline node moves the whole fleet's freshness, so the tile's count
 * is misread without naming where the staleness sits.
 */
export function staleSignalLeader(model: Model): { label: string; count: number } | null {
  let best: { label: string; count: number } | null = null
  for (const node of model.nodes) {
    const count = countSignals([node.render, node.audio, node.fppConnect]).stale
    if (count > 0 && (best === null || count > best.count)) {
      best = { label: node.label ?? node.nodeId, count }
    }
  }
  const total = countSignals([
    ...model.nodes.flatMap((node) => [node.render, node.audio, node.fppConnect]),
    ...model.fpp.map((instance) => instance.observations),
    ...model.resolume.map((instance) => instance.observations),
  ]).stale
  if (best === null || total === 0 || best.count < total / 2) return null
  return best
}
