import type { Evidence, EvidenceState, Model } from '../api'
import type { Absence, Tone } from '../kit'

/**
 * The wire's seven evidence states, mapped onto the four absences. Only
 * `not_collected` is never-collected, and only it takes the dashed edge.
 * `not_applicable` is a settled fact: there is nothing to measure.
 */
export const EVIDENCE_ABSENCE: Record<EvidenceState, Absence> = {
  current: 'empty',
  stale: 'stale',
  unknown_age: 'unavailable',
  not_collected: 'unobserved',
  collection_failed: 'failed',
  unsupported: 'unavailable',
  not_applicable: 'empty',
}

export const EVIDENCE_TONE: Record<EvidenceState, Tone> = {
  current: 'good',
  stale: 'warn',
  unknown_age: 'unknown',
  not_collected: 'unknown',
  collection_failed: 'bad',
  unsupported: 'unknown',
  not_applicable: 'pending',
}

export const EVIDENCE_LABEL: Record<EvidenceState, string> = {
  current: 'Current',
  stale: 'Stale',
  unknown_age: 'Age unknown',
  not_collected: 'Unobserved',
  collection_failed: 'Collection failed',
  unsupported: 'Unavailable',
  not_applicable: 'N/A',
}

/** An empty string is a known empty value and renders as None. */
export function displayValue(value: string | number | boolean): string {
  return value === '' ? 'None' : String(value)
}

export type SignalCounts = {
  total: number
  /** Signals with something to measure: the total without `notApplicable`. */
  measurable: number
  notApplicable: number
  current: number
  stale: number
  unobserved: number
  failed: number
  /** `unsupported` and `unknown_age`: no usable value, nothing to retry. */
  unavailable: number
}

export function countStates(states: Iterable<EvidenceState>): SignalCounts {
  const counts: SignalCounts = { total: 0, measurable: 0, notApplicable: 0, current: 0, stale: 0, unobserved: 0, failed: 0, unavailable: 0 }
  for (const state of states) {
    counts.total += 1
    if (state === 'current') counts.current += 1
    else if (state === 'stale') counts.stale += 1
    else if (state === 'not_collected') counts.unobserved += 1
    else if (state === 'collection_failed') counts.failed += 1
    else if (state === 'not_applicable') counts.notApplicable += 1
    else counts.unavailable += 1
  }
  counts.measurable = counts.total - counts.notApplicable
  return counts
}

export function countSignals(groups: readonly (readonly Evidence[])[]): SignalCounts {
  return countStates(groups.flatMap((group) => group.map((evidence) => evidence.state)))
}

export type SignalTally = { key: keyof SignalCounts; word: string; count: number }

/** Every state a signal can be in, in reading order. The counts always sum to `total`. */
export function signalTally(counts: SignalCounts): SignalTally[] {
  return [
    { key: 'current', word: 'current', count: counts.current },
    { key: 'stale', word: 'stale', count: counts.stale },
    { key: 'unobserved', word: 'unobserved', count: counts.unobserved },
    { key: 'failed', word: 'failed', count: counts.failed },
    { key: 'unavailable', word: 'unavailable', count: counts.unavailable },
    { key: 'notApplicable', word: 'N/A', count: counts.notApplicable },
  ]
}

export type TilePart = { text: string; failed: boolean }

/** The tile line as parts: counted states sum to measurable minus current, N/A is named as outside that total. */
export function signalTileParts(counts: SignalCounts): TilePart[] {
  const parts: TilePart[] = signalTally(counts)
    .filter((entry) => entry.key !== 'current' && entry.key !== 'notApplicable' && entry.count > 0)
    .map((entry) => ({ text: `${entry.count} ${entry.word}`, failed: entry.key === 'failed' }))
  if (parts.length === 0 && counts.current > 0) parts.push({ text: 'All current', failed: false })
  if (counts.notApplicable > 0) parts.push({ text: `${counts.notApplicable} N/A not counted`, failed: false })
  return parts
}

/** Every evidence group a signal count covers, so the tile, the tab count and the footer read the same signals. */
export function signalGroups(model: Model): Evidence[][] {
  return [
    ...model.nodes.flatMap((node) => [node.render, node.audio, node.clock, node.fppConnect]),
    ...model.fpp.map((instance) => instance.observations),
    ...model.resolume.map((instance) => instance.observations),
  ]
}
