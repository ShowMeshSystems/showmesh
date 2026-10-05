import type { Evidence, EvidenceState } from '../api'
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
  unavailable: number
  unknownAge: number
}

export function countSignals(groups: readonly (readonly Evidence[])[]): SignalCounts {
  const counts: SignalCounts = {
    total: 0,
    measurable: 0,
    notApplicable: 0,
    current: 0,
    stale: 0,
    unobserved: 0,
    failed: 0,
    unavailable: 0,
    unknownAge: 0,
  }
  for (const group of groups) {
    for (const evidence of group) {
      counts.total += 1
      switch (evidence.state) {
        case 'current':
          counts.current += 1
          break
        case 'stale':
          counts.stale += 1
          break
        case 'not_collected':
          counts.unobserved += 1
          break
        case 'collection_failed':
          counts.failed += 1
          break
        case 'unsupported':
          counts.unavailable += 1
          break
        case 'unknown_age':
          counts.unknownAge += 1
          break
        case 'not_applicable':
          counts.notApplicable += 1
          break
      }
    }
  }
  counts.measurable = counts.total - counts.notApplicable
  return counts
}
