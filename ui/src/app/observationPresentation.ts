import type { Evidence, Model, ResourceRef } from './types'
import type { StatusTone } from '../components/StatusBadge'

/**
 * The operator-facing state vocabulary is intentionally smaller than the
 * wire vocabulary. It keeps the important distinction between a known value,
 * an old value, a failed collection, and no observation at all without
 * inventing a health verdict.
 */
export type ObservationDisplayState = 'current' | 'stale' | 'unknown' | 'failed' | 'unobserved'

export interface ObservationStatePresentation {
  label: ObservationDisplayState
  tone: StatusTone
  icon: string
}

const STATE_PRESENTATION: Record<ObservationDisplayState, ObservationStatePresentation> = {
  current: { label: 'current', tone: 'good', icon: '●' },
  stale: { label: 'stale', tone: 'warn', icon: '⚠' },
  unknown: { label: 'unknown', tone: 'unknown', icon: '?' },
  failed: { label: 'failed', tone: 'bad', icon: '✕' },
  unobserved: { label: 'unobserved', tone: 'unknown', icon: '–' },
}

export function observationDisplayState(evidence: Evidence | undefined): ObservationDisplayState {
  if (evidence === undefined || evidence.state === 'not_collected' || evidence.state === 'unsupported') {
    return 'unobserved'
  }
  if (evidence.state === 'collection_failed') return 'failed'
  if (evidence.state === 'unknown_age') return 'unknown'
  return evidence.state
}

export function observationStatePresentation(evidence: Evidence | undefined): ObservationStatePresentation {
  return STATE_PRESENTATION[observationDisplayState(evidence)]
}

export interface PresentedObservation {
  resource: ResourceRef
  signal: string
  evidence: Evidence
  /** A detail route when this resource has one in the operator UI. */
  href: string | null
  /** Human-readable grouping label, without changing the raw signal id. */
  meaning: string
}

function routeForResource(resource: ResourceRef): string | null {
  if (resource.kind === 'node') return `/nodes/${encodeURIComponent(resource.id)}`
  if (resource.kind === 'fpp') return `/fpp/${encodeURIComponent(resource.id)}`
  if (resource.kind === 'resolume') return '/resolume'
  return null
}

function meaningForResource(resource: ResourceRef): string {
  switch (resource.kind) {
    case 'node':
      return 'Node'
    case 'surface':
      return 'Render surface'
    case 'audio_session':
      return 'Audio session'
    case 'fpp':
      return 'FPP player'
    case 'resolume':
      return 'Resolume'
    case 'coordinator':
      return 'Coordinator'
    default:
      return 'Other'
  }
}

function presented(resource: ResourceRef, evidence: Evidence, meaning = meaningForResource(resource)): PresentedObservation {
  return {
    resource,
    signal: evidence.signal,
    evidence,
    href: routeForResource(resource),
    meaning,
  }
}

/**
 * Flattens the latest model evidence for the read-only Observations view.
 * The model has already resolved duplicate collector sources; this helper
 * only adds resource identity to node-level evidence and preserves API order.
 */
export function presentModelObservations(model: Model): PresentedObservation[] {
  const rows: PresentedObservation[] = []
  for (const node of model.nodes) {
    const resource: ResourceRef = { kind: 'node', id: node.nodeId }
    rows.push(presented(resource, node.evidence.hello))
    rows.push(presented(resource, node.evidence.lastWill))
    rows.push(presented(resource, node.evidence.heartbeat))
    for (const entry of node.render) {
      rows.push(presented(entry.resource, entry, entry.resource.kind === 'node' ? 'Node' : 'Render surface'))
    }
    for (const entry of node.audio) {
      rows.push(presented(entry.resource, entry, entry.resource.kind === 'node' ? 'Node' : 'Audio'))
    }
  }
  for (const instance of model.fpp) {
    const resource: ResourceRef = { kind: 'fpp', id: instance.instanceId }
    for (const observation of instance.observations) rows.push(presented(resource, observation))
  }
  for (const instance of model.resolume) {
    const resource: ResourceRef = { kind: 'resolume', id: instance.instanceId }
    for (const observation of instance.observations) rows.push(presented(resource, observation))
  }
  return rows
}
