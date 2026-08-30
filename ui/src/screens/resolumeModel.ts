import type {
  Evidence,
  Model,
  ResolumeCompositionClip,
  ResolumeCompositionColumn,
  ResolumeCompositionDeckSummary,
  ResolumeCompositionLayer,
  ResolumeCompositionResponse,
  ResolumeInstance,
  ResolumeRecoveryRestoreLayer,
  ResolumeRecoveryRestoreReport,
} from '../api'
import type { Tone } from '../kit'
import { ageMs, formatDuration } from '../domain/time'

/** This screen's Resolume instance, matched by id, straight from the live model. */
export function findResolumeInstance(model: Model, instanceId: string): ResolumeInstance | null {
  return model.resolume.find((instance) => instance.instanceId === instanceId) ?? null
}

export const RESOLUME_HEALTH_TONE: Record<ResolumeInstance['health'], Tone> = {
  healthy: 'good',
  degraded: 'warn',
  failed: 'bad',
  unknown: 'unknown',
  suppressed: 'pending',
}

export const RESOLUME_HEALTH_LABEL: Record<ResolumeInstance['health'], string> = {
  healthy: 'Healthy',
  degraded: 'Degraded',
  failed: 'Failed',
  unknown: 'Unknown',
  suppressed: 'Suppressed',
}

/** Every signal this screen may read carries `internal/coordinator/collector/resolume/signals.go`'s exact name. */
export function findSignal(instance: ResolumeInstance, signal: string): Evidence | undefined {
  return instance.observations.find((entry) => entry.signal === signal)
}

/** The instance's own most recent observation time, for the page head's "age of its last answer". */
export function lastAnswerAgeMs(instance: ResolumeInstance, nowIso: string | null): number | null {
  const latest = instance.observations.reduce<string | null>(
    (acc, entry) => (entry.observedAt !== null && (acc === null || entry.observedAt > acc) ? entry.observedAt : acc),
    null,
  )
  return ageMs(latest, nowIso)
}

export type IdentifiedVerdict = {
  tone: Tone
  label: string
  detail: string
}

/**
 * `resolume.composition.identified`'s value is already the coordinator's own
 * finished sentence (ADR-032 amendment): this only picks a tone and a short
 * label from its own reported prefix. It never computes the mismatch itself.
 */
export function describeIdentified(value: string): IdentifiedVerdict {
  if (value === 'identified') {
    return { tone: 'good', label: 'Identified', detail: value }
  }
  if (value.startsWith('deck_mismatch: ')) {
    return { tone: 'warn', label: 'Deck mismatch', detail: value.slice('deck_mismatch: '.length) }
  }
  if (value.startsWith('not_identified: ')) {
    return { tone: 'bad', label: 'Not identified', detail: value.slice('not_identified: '.length) }
  }
  if (value.startsWith('unknown: ')) {
    return { tone: 'unknown', label: 'Unknown', detail: value.slice('unknown: '.length) }
  }
  return { tone: 'unknown', label: 'Unknown', detail: value }
}

export type LayerReadiness = {
  layerId: string
  ready: Evidence | undefined
  activeClip: Evidence | undefined
}

const LAYER_READY_RE = /^resolume\.layer\.([^.]+)\.ready$/
const LAYER_ACTIVE_CLIP_RE = /^resolume\.layer\.([^.]+)\.active_clip$/

/**
 * Groups the per-layer `resolume.layer.<id>.ready` / `.active_clip` signals
 * by their own id, rather than the flat list `instance.observations` reports
 * them in. One entry per layer id either signal named.
 */
export function groupLayerReadiness(instance: ResolumeInstance): LayerReadiness[] {
  const byId = new Map<string, LayerReadiness>()
  const get = (id: string): LayerReadiness => {
    let entry = byId.get(id)
    if (entry === undefined) {
      entry = { layerId: id, ready: undefined, activeClip: undefined }
      byId.set(id, entry)
    }
    return entry
  }
  for (const observation of instance.observations) {
    const readyMatch = LAYER_READY_RE.exec(observation.signal)
    if (readyMatch !== null && readyMatch[1] !== undefined) {
      get(readyMatch[1]).ready = observation
      continue
    }
    const activeClipMatch = LAYER_ACTIVE_CLIP_RE.exec(observation.signal)
    if (activeClipMatch !== null && activeClipMatch[1] !== undefined) {
      get(activeClipMatch[1]).activeClip = observation
    }
  }
  return [...byId.values()]
}

/** A layer's own authored name, or its generated positional one, resolved by id (never a bare index). */
export function layerName(layers: readonly ResolumeCompositionLayer[], layerId: string): { name: string; generated: boolean } | null {
  const layer = layers.find((candidate) => candidate.id === layerId)
  return layer === undefined ? null : { name: layer.name, generated: layer.nameGenerated }
}

/**
 * `ResolumeCompositionClip.layerIndex` is the layer's own `index` attribute
 * (`internal/coordinator/collector/resolume/idmap.go`'s `resolveLayerID`),
 * never a position in the `layers` array, so this resolves by that field.
 */
export function layerForClip(layers: readonly ResolumeCompositionLayer[], layerIndex: number): ResolumeCompositionLayer | undefined {
  return layers.find((layer) => layer.index === layerIndex)
}

export function deckForClip(decks: readonly ResolumeCompositionDeckSummary[], deckId: string | undefined): ResolumeCompositionDeckSummary | undefined {
  if (deckId === undefined) return undefined
  return decks.find((deck) => deck.id === deckId)
}

export function columnForClip(
  columns: readonly ResolumeCompositionColumn[],
  deckId: string | undefined,
  columnIndex: number,
): ResolumeCompositionColumn | undefined {
  return columns.find((column) => column.index === columnIndex && (deckId === undefined || column.deckId === deckId))
}

export type AmbiguousClip = { clip: ResolumeCompositionClip; persistent: boolean }

/** Every clip and persistent clip the coordinator itself flagged `ambiguous`, in the order the response listed them. */
export function ambiguousClips(composition: ResolumeCompositionResponse): AmbiguousClip[] {
  return [
    ...composition.clips.filter((clip) => clip.ambiguous).map((clip) => ({ clip, persistent: false })),
    ...composition.persistentClips.filter((clip) => clip.ambiguous).map((clip) => ({ clip, persistent: true })),
  ]
}

/** `4 layers had a recorded clip, 3 restored, 1 skipped`: derived by counting the reported layers, never hardcoded. */
export function restoreSummary(layers: readonly ResolumeRecoveryRestoreLayer[]): string {
  const withClip = layers.filter((layer) => layer.clip !== undefined).length
  const restored = layers.filter((layer) => layer.result === 'restored').length
  const skipped = layers.filter((layer) => layer.result === 'skipped').length
  const failed = layers.filter((layer) => layer.result === 'failed').length
  const parts: string[] = []
  if (restored > 0) parts.push(`${restored} restored`)
  if (skipped > 0) parts.push(`${skipped} skipped`)
  if (failed > 0) parts.push(`${failed} failed`)
  const clause = parts.length === 0 ? 'none restored' : parts.join(', ')
  return `${withClip} ${withClip === 1 ? 'layer' : 'layers'} had a recorded clip. ${clause}.`
}

export const RESTORE_OUTCOME_TONE: Record<ResolumeRecoveryRestoreReport['outcome'], Tone> = {
  restored: 'good',
  partial: 'warn',
  nothing_to_do: 'pending',
  failed: 'bad',
}

export const RESTORE_OUTCOME_LABEL: Record<ResolumeRecoveryRestoreReport['outcome'], string> = {
  restored: 'Restored',
  partial: 'Partial',
  nothing_to_do: 'Nothing to do',
  failed: 'Failed',
}

export const RESTORE_RESULT_TONE: Record<ResolumeRecoveryRestoreLayer['result'], Tone> = {
  restored: 'good',
  skipped: 'warn',
  failed: 'bad',
}

export const RESTORE_RESULT_LABEL: Record<ResolumeRecoveryRestoreLayer['result'], string> = {
  restored: 'Restored',
  skipped: 'Skipped',
  failed: 'Failed',
}

/** `unconfirmable` reports every run, by design, and is never a failure (guide §4). */
export function actionOutcomeDescription(outcome: NonNullable<ResolumeRecoveryRestoreLayer['actionOutcome']>): { tone: Tone; label: string } {
  switch (outcome) {
    case 'confirmed':
      return { tone: 'good', label: 'Confirmed' }
    case 'unconfirmed':
      return { tone: 'warn', label: 'Unconfirmed' }
    case 'unconfirmable':
      return { tone: 'unknown', label: 'Unavailable' }
    case 'refused':
      return { tone: 'bad', label: 'Refused' }
    case 'failed':
      return { tone: 'bad', label: 'Failed' }
  }
}

export function formatDurationAgo(ms: number | null): string {
  return ms === null ? 'at an unrecorded time' : `${formatDuration(ms)} ago`
}
