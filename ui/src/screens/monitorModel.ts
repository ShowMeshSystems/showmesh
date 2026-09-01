import type { AuditEntry, Evidence, Event, FallbackProgramListEntry, FallbackProgramResponse, FPPInstance, Model, Node } from '../api'
import type { Connection, Tone } from '../kit'
import { countSignals, EVIDENCE_LABEL, EVIDENCE_TONE } from '../domain/evidence'
import { ageMs, formatClock, formatDuration, parseIsoMs } from '../domain/time'

/** Connection state, in the terms Monitor's own pill labels use. */
export function monitorConnection(model: Model): Connection {
  return model.connection.kind === 'live'
    ? 'live'
    : model.connection.kind === 'reconnecting'
      ? 'degraded'
      : model.connection.kind === 'connecting'
        ? 'unknown'
        : 'lost'
}

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
      to: `/settings/resolume/${instance.instanceId}`,
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

const SEVERITY_LABEL: Record<Event['severity'], string> = {
  informational: 'Informational',
  warning: 'Warning',
  critical: 'Critical',
}

export type ActivityRow = {
  key: string
  time: string
  summary: string
  source: string
  /** The state word, only when there is one worth showing. Colour never travels alone. */
  state: string | null
  tone: Tone
}

export function activityRows(events: readonly Event[], limit: number): ActivityRow[] {
  return mergedActivityRows(events.slice(0, limit), [])
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

/**
 * FPP remains a row in Fleet, not a separate Monitor destination. Its
 * inspector deliberately reports coordinator-held evidence without deriving
 * an FPP verdict or moving transport controls out of Live Control.
 */
export function fppInspector(instance: FPPInstance, nowIso: string | null): { title: string; subtitle: string; groups: { name: string; rows: InspectorRow[]; absent: string | null }[] } {
  const pollAge = ageMs(instance.lastPollAt, nowIso)
  const observations = instance.observations.slice(0, 12).map((entry, index) => ({
    key: `fpp:${entry.signal}:${index}`,
    label: entry.signal,
    value: signalValue(entry),
    state: entry.state === 'current' ? null : EVIDENCE_LABEL[entry.state],
    detail: entry.state === 'current' ? null : entry.reason,
    tone: EVIDENCE_TONE[entry.state],
  }))

  const identity: InspectorRow[] = [
    { key: 'endpoint', label: 'Endpoint', value: instance.endpoint, state: null, detail: null, tone: 'pending' },
    {
      key: 'last-poll',
      label: 'Last poll',
      value: instance.lastPollAt === null ? 'never reported' : `${formatClock(instance.lastPollAt) ?? 'unrecorded'}${pollAge === null ? '' : ` · ${formatDuration(pollAge)} ago`}`,
      state: instance.lastPollError === null ? null : 'Collection failed',
      detail: instance.lastPollError,
      tone: instance.lastPollError === null ? 'pending' : 'bad',
    },
    {
      key: 'instance-uuid',
      label: 'Instance UUID',
      value: instance.instanceUuid ?? 'not observed',
      state: instance.instanceUuidChange === null ? null : 'Bindings held',
      detail:
        instance.instanceUuidChange === null
          ? instance.instanceUuid === null
            ? 'The endpoint has not reported a SystemUUID yet.'
            : null
          : `Changed from ${instance.instanceUuidChange.previousUuid} at ${formatClock(instance.instanceUuidChange.changedAt) ?? 'an unrecorded time'}. Re-association is held for an explicit inventory decision.`,
      tone: instance.instanceUuidChange === null ? (instance.instanceUuid === null ? 'unknown' : 'pending') : 'warn',
    },
  ]

  return {
    title: instance.instanceId,
    subtitle: `FPP player · ${instance.health} as FPP reports`,
    groups: [
      { name: 'Instance', rows: identity, absent: null },
      {
        name: 'Reported observations',
        rows: observations,
        absent:
          observations.length === 0
            ? 'This endpoint has not reported an FPP observation. That is not the same as an FPP path that is failing.'
            : null,
      },
    ],
  }
}

// ---------------------------------------------------------------------
// FPP inspector: the fallback-program readiness group (ADR-048, Track
// J's J1 / SM-460). Read-only: acknowledgement is the installed FPP
// plugin's own evidence, never a value an operator credential may type.
// ---------------------------------------------------------------------

export type FallbackProgramRow = { key: string; label: string; value: string }

const FALLBACK_ACKNOWLEDGED_LABEL: Record<FallbackProgramResponse['acknowledgedStatus'], string> = {
  'fallback-program-current': 'Current',
  'fallback-program-stale': 'Stale',
  'fallback-program-rejected': 'Rejected',
  'fallback-program-unacknowledged': 'Not acknowledged',
}

const FALLBACK_ACKNOWLEDGED_TONE: Record<FallbackProgramResponse['acknowledgedStatus'], Tone> = {
  'fallback-program-current': 'good',
  'fallback-program-stale': 'warn',
  'fallback-program-rejected': 'bad',
  'fallback-program-unacknowledged': 'unknown',
}

export function fallbackAcknowledgedLabel(status: FallbackProgramResponse['acknowledgedStatus']): string {
  return FALLBACK_ACKNOWLEDGED_LABEL[status]
}

export function fallbackAcknowledgedTone(status: FallbackProgramResponse['acknowledgedStatus']): Tone {
  return FALLBACK_ACKNOWLEDGED_TONE[status]
}

/**
 * The package/revision/show/generation/timestamp metadata `GET
 * /fallback-programs` reports for one host — everything the operator
 * reads before a show that does not require the admin/scheduler-only
 * `fpp:fallback` scope. Never the signed program body itself.
 */
export function fallbackProgramMetadataRows(entry: FallbackProgramListEntry): FallbackProgramRow[] {
  return [
    { key: 'package', label: 'Package', value: entry.packageId },
    { key: 'revision', label: 'Revision', value: entry.revision },
    { key: 'show', label: 'Show', value: entry.show },
    { key: 'generation', label: 'Generation', value: String(entry.generation) },
    { key: 'compiled', label: 'Compiled', value: formatClock(entry.compiledAt) ?? 'unrecorded' },
    { key: 'expires', label: 'Expires', value: formatClock(entry.expiresAt) ?? 'unrecorded' },
  ]
}

export type FallbackProgramAcknowledgement = {
  tone: Tone
  label: string
  /** `null` when `acknowledgedPackageId` is absent (unacknowledged). */
  acknowledgedPackage: string | null
  /** `null` when `acknowledgedAt` is absent (unacknowledged). */
  acknowledgedAt: string | null
  signaturePresent: boolean
}

/**
 * The `GET /fallback-programs/{fppInstanceId}` acknowledgement fields —
 * behind `fpp:fallback`, so a caller only reaches this once that read
 * has actually succeeded. Signature *presence* only, never the
 * signature itself (the guide's "never invent" rule cuts both ways:
 * rendering a truncated signature would invent legibility the value
 * never had).
 */
export function fallbackProgramAcknowledgement(detail: FallbackProgramResponse): FallbackProgramAcknowledgement {
  return {
    tone: fallbackAcknowledgedTone(detail.acknowledgedStatus),
    label: fallbackAcknowledgedLabel(detail.acknowledgedStatus),
    acknowledgedPackage: detail.acknowledgedPackageId ?? null,
    acknowledgedAt: detail.acknowledgedAt !== undefined ? (formatClock(detail.acknowledgedAt) ?? 'unrecorded') : null,
    signaturePresent: detail.signatureBase64 !== undefined,
  }
}

// ---------------------------------------------------------------------
// Signals facet: every observation across every resource, in one table.
// ---------------------------------------------------------------------

export type SignalRow = {
  key: string
  resource: string
  resourceTo: string
  kind: Exclude<FleetKind, 'all'>
  signal: string
  value: string
  tone: Tone
  state: string
  observed: string
}

function signalValue(entry: Evidence): string {
  if (entry.value === null) return 'not reported'
  return entry.unit === null || entry.unit === '' ? String(entry.value) : `${entry.value} ${entry.unit}`
}

function signalObserved(entry: Evidence, nowIso: string | null): string {
  if (entry.observedAt === null) return 'never'
  const age = ageMs(entry.observedAt, nowIso)
  return age === null ? (formatClock(entry.observedAt) ?? 'unrecorded') : `${formatDuration(age)} ago`
}

function evidenceRows(
  entries: readonly Evidence[],
  keyPrefix: string,
  resource: string,
  resourceTo: string,
  kind: Exclude<FleetKind, 'all'>,
  nowIso: string | null,
): SignalRow[] {
  return entries.map((entry, index) => ({
    key: `${keyPrefix}:${index}`,
    resource,
    resourceTo,
    kind,
    signal: entry.signal,
    value: signalValue(entry),
    tone: EVIDENCE_TONE[entry.state],
    state: EVIDENCE_LABEL[entry.state],
    observed: signalObserved(entry, nowIso),
  }))
}

/**
 * Every observation the coordinator holds, across nodes, FPP and Resolume,
 * in the Fleet table's own idiom: resource, kind and last report as
 * columns, kind as the narrowing dimension.
 */
export function signalRows(model: Model, nowIso: string | null): SignalRow[] {
  const rows: SignalRow[] = []
  for (const node of model.nodes) {
    const resource = node.label ?? node.nodeId
    const to = `/monitor/fleet/node/${node.nodeId}`
    rows.push(...evidenceRows(node.render, `node:${node.nodeId}:render`, resource, to, 'node', nowIso))
    rows.push(...evidenceRows(node.audio, `node:${node.nodeId}:audio`, resource, to, 'node', nowIso))
    rows.push(...evidenceRows(node.fppConnect, `node:${node.nodeId}:fppConnect`, resource, to, 'node', nowIso))
  }
  for (const instance of model.fpp) {
    rows.push(
      ...evidenceRows(
        instance.observations,
        `fpp:${instance.instanceId}`,
        instance.instanceId,
        `/monitor/fleet/fpp/${instance.instanceId}`,
        'fpp',
        nowIso,
      ),
    )
  }
  for (const instance of model.resolume) {
    rows.push(
      ...evidenceRows(
        instance.observations,
        `resolume:${instance.instanceId}`,
        instance.instanceId,
        `/monitor/fleet/resolume/${instance.instanceId}`,
        'resolume',
        nowIso,
      ),
    )
  }
  return rows
}

export function signalSummary(rows: readonly SignalRow[]): string {
  const byLabel = (label: string) => rows.filter((row) => row.state === label).length
  const current = byLabel(EVIDENCE_LABEL.current)
  const stale = byLabel(EVIDENCE_LABEL.stale)
  const unobserved = byLabel(EVIDENCE_LABEL.not_collected)
  const failed = byLabel(EVIDENCE_LABEL.collection_failed)
  const unavailable = byLabel(EVIDENCE_LABEL.unsupported) + byLabel(EVIDENCE_LABEL.unknown_age)
  return `${rows.length} signals · ${current} current, ${stale} stale, ${unobserved} unobserved, ${failed} failed, ${unavailable} unavailable.`
}

// ---------------------------------------------------------------------
// Capabilities facet: what each node has advertised, grouped by node.
// ---------------------------------------------------------------------

export type CapabilityGroup = {
  key: string
  node: string
  nodeTo: string
  capabilities: readonly { id: string; version: number }[]
}

export function capabilityGroups(model: Model): CapabilityGroup[] {
  return model.nodes.map((node) => ({
    key: node.nodeId,
    node: node.label ?? node.nodeId,
    nodeTo: `/monitor/fleet/node/${node.nodeId}`,
    capabilities: node.capabilities,
  }))
}

// ---------------------------------------------------------------------
// Activity facet: system events (open) and operator actions (need
// audit:read), merged into one time-ordered stream.
// ---------------------------------------------------------------------

type TimedRow = ActivityRow & { atMs: number }

const AUDIT_TONE: Record<string, Tone> = {
  current: 'good',
  stale: 'warn',
  unknown_age: 'unknown',
  not_collected: 'unknown',
  collection_failed: 'bad',
  unsupported: 'unknown',
}

function eventRow(event: Event): TimedRow {
  const at = event.occurredAt ?? event.recordedAt
  return {
    key: `event:${event.seq}`,
    time: formatClock(at) ?? 'unrecorded',
    summary: event.summary,
    source: event.source,
    state: event.severity === 'informational' ? null : SEVERITY_LABEL[event.severity],
    tone: SEVERITY_TONE[event.severity],
    atMs: parseIsoMs(at) ?? 0,
  }
}

function auditSummary(entry: AuditEntry): string {
  const fact = entry.outcomeReason !== '' ? entry.outcomeReason : `${entry.action} on ${entry.target}`
  return entry.outcome === '' ? fact : `${fact} (${entry.outcome})`
}

function auditRow(entry: AuditEntry): TimedRow {
  return {
    key: `audit:${entry.id}`,
    time: formatClock(entry.timestamp) ?? 'unrecorded',
    summary: auditSummary(entry),
    source: entry.principalName,
    state: entry.outcomeState === '' ? null : (EVIDENCE_LABEL[entry.outcomeState as Evidence['state']] ?? entry.outcomeState),
    tone: entry.outcomeState === '' ? 'pending' : (AUDIT_TONE[entry.outcomeState] ?? 'unknown'),
    atMs: parseIsoMs(entry.timestamp) ?? 0,
  }
}

/**
 * System events and operator actions, in one time-ordered table. Audit
 * entries are supplied only when the caller could read them; an empty
 * `audit` array never means "no operator acted", only "not merged in".
 */
export function mergedActivityRows(events: readonly Event[], audit: readonly AuditEntry[]): ActivityRow[] {
  const rows = [...events.map(eventRow), ...audit.map(auditRow)]
  rows.sort((a, b) => b.atMs - a.atMs)
  return rows.map((row) => ({ key: row.key, time: row.time, summary: row.summary, source: row.source, state: row.state, tone: row.tone }))
}
