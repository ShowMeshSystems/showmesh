import { describe, expect, it } from 'vitest'
import type { Evidence, EvidenceState } from '../api'
import { countSignals, EVIDENCE_ABSENCE, EVIDENCE_LABEL, EVIDENCE_TONE } from './evidence'

const evidence = (state: EvidenceState) => ({ state }) as unknown as Evidence

describe('evidence states', () => {
  it('shows a signal with nothing to measure as N/A, a settled fact and not a problem', () => {
    expect(EVIDENCE_LABEL.not_applicable).toBe('N/A')
    expect(EVIDENCE_ABSENCE.not_applicable).toBe('empty')
    expect(EVIDENCE_TONE.not_applicable).toBe('pending')
  })

  it('keeps the dashed never-collected treatment for not_collected alone', () => {
    const dashed = (Object.keys(EVIDENCE_ABSENCE) as EvidenceState[]).filter((state) => EVIDENCE_ABSENCE[state] === 'unobserved')
    expect(dashed).toEqual(['not_collected'])
  })

  it('counts N/A separately from unobserved and leaves it out of the measurable total', () => {
    const counts = countSignals([[evidence('current'), evidence('not_applicable')], [evidence('not_collected'), evidence('stale')]])
    expect(counts).toMatchObject({ total: 4, measurable: 3, notApplicable: 1, unobserved: 1, current: 1, stale: 1 })
  })
})
