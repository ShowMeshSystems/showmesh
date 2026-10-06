import { describe, expect, it } from 'vitest'
import type { Evidence, EvidenceState } from '../api'
import { countSignals, EVIDENCE_ABSENCE, EVIDENCE_LABEL, EVIDENCE_TONE, signalTally, signalTileParts } from './evidence'

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

describe('signal tally', () => {
  const everyState = Object.keys(EVIDENCE_LABEL) as EvidenceState[]
  const mixed = [...everyState, 'collection_failed', 'unsupported', 'current'] as EvidenceState[]

  it('sums every state to the total', () => {
    const counts = countSignals([mixed.map(evidence)])
    const sum = signalTally(counts).reduce((acc, entry) => acc + entry.count, 0)
    expect(sum).toBe(counts.total)
    expect(counts).toMatchObject({ total: 10, current: 2, stale: 1, unobserved: 1, failed: 2, unavailable: 3, notApplicable: 1 })
  })

  it('makes the counted states sum to measurable minus current, with N/A named as outside', () => {
    const counts = countSignals([mixed.map(evidence)])
    const parts = signalTileParts(counts)
    expect(parts.map((part) => part.text)).toEqual(['1 stale', '1 unobserved', '2 failed', '3 unavailable', '1 N/A not counted'])
    expect(counts.measurable - counts.current).toBe(1 + 1 + 2 + 3)
    expect(parts.filter((part) => part.failed).map((part) => part.text)).toEqual(['2 failed'])
  })

  it('says All current rather than render nothing, and omits states with none', () => {
    expect(signalTileParts(countSignals([[evidence('current')]])).map((part) => part.text)).toEqual(['All current'])
    expect(signalTileParts(countSignals([[evidence('current'), evidence('stale')]])).map((part) => part.text)).toEqual(['1 stale'])
    expect(signalTileParts(countSignals([[evidence('not_applicable')]])).map((part) => part.text)).toEqual(['1 N/A not counted'])
  })
})
