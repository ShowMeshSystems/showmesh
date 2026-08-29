import type { Evidence, EvidenceState } from '../api'
import type { Absence, Tone } from '../kit'

/**
 * The wire's six evidence states, mapped onto the four absences. Only
 * `not_collected` is never-collected, and only it takes the dashed edge.
 */
export const EVIDENCE_ABSENCE: Record<EvidenceState, Absence> = {
  current: 'empty',
  stale: 'stale',
  unknown_age: 'unavailable',
  not_collected: 'unobserved',
  collection_failed: 'failed',
  unsupported: 'unavailable',
}

export const EVIDENCE_TONE: Record<EvidenceState, Tone> = {
  current: 'good',
  stale: 'warn',
  unknown_age: 'unknown',
  not_collected: 'unknown',
  collection_failed: 'bad',
  unsupported: 'unknown',
}

export const EVIDENCE_LABEL: Record<EvidenceState, string> = {
  current: 'Current',
  stale: 'Stale',
  unknown_age: 'Age unknown',
  not_collected: 'Unobserved',
  collection_failed: 'Collection failed',
  unsupported: 'Unavailable',
}

export type SignalCounts = {
  total: number
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
      }
    }
  }
  return counts
}
