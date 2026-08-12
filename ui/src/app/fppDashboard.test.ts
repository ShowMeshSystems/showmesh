import { describe, expect, it } from 'vitest'
import { summarizeFleetPorts, summarizeFleetWarnings } from './fppDashboard'
import { makeEvidence } from './test-support/fixtures'
import type { Evidence } from './types'

function evidence(signal: string, overrides: Partial<Evidence> = {}): Evidence {
  return makeEvidence({ signal, ...overrides })
}

describe('summarizeFleetWarnings', () => {
  it('sums fpp.warnings.count across instances that reported a numeric value', () => {
    const instances = [
      { instanceId: 'a', observations: [evidence('fpp.warnings.count', { value: 1 })] },
      { instanceId: 'b', observations: [evidence('fpp.warnings.count', { value: 3 })] },
    ]
    const summary = summarizeFleetWarnings(instances)
    expect(summary.total).toBe(4)
    expect(summary.instancesReporting).toBe(2)
    expect(summary.instancesUnknown).toBe(0)
  })

  // The specific defect this function exists to avoid: an instance whose
  // warnings count is absent (never collected, collection_failed,
  // unsupported...) must NOT silently contribute 0 to the total, and must
  // be counted separately so an operator can tell "zero warnings" apart
  // from "we don't know".
  it('excludes an instance with no numeric fpp.warnings.count from the total, counting it as unknown instead', () => {
    const instances = [
      { instanceId: 'a', observations: [evidence('fpp.warnings.count', { value: 2 })] },
      { instanceId: 'b', observations: [evidence('fpp.warnings.count', { value: null, state: 'collection_failed' })] },
      { instanceId: 'c', observations: [] },
    ]
    const summary = summarizeFleetWarnings(instances)
    expect(summary.total).toBe(2)
    expect(summary.instancesReporting).toBe(1)
    expect(summary.instancesUnknown).toBe(2)
  })

  it('reports zero total and zero reporting instances for an empty fleet', () => {
    expect(summarizeFleetWarnings([])).toEqual({
      total: 0,
      instancesReporting: 0,
      instancesStaleOrUnknownAge: 0,
      instancesUnknown: 0,
    })
  })

  // Step 5 review finding 6: a stale or unknown_age fpp.warnings.count
  // still legitimately carries a value, but the fleet total must let a
  // caller tell "N warnings, all freshly confirmed" apart from "N
  // warnings, some stale or of unknown age" rather than summing the two
  // in with no distinction.
  it('tracks instancesStaleOrUnknownAge separately from instancesReporting, without excluding the value from the total', () => {
    const instances = [
      { instanceId: 'fresh', observations: [evidence('fpp.warnings.count', { value: 1, state: 'current' })] },
      { instanceId: 'stale', observations: [evidence('fpp.warnings.count', { value: 2, state: 'stale' })] },
      {
        instanceId: 'ghost',
        observations: [evidence('fpp.warnings.count', { value: 0, state: 'unknown_age', observedAt: null })],
      },
    ]
    const summary = summarizeFleetWarnings(instances)
    expect(summary.total).toBe(3)
    expect(summary.instancesReporting).toBe(3)
    expect(summary.instancesStaleOrUnknownAge).toBe(2)
  })
})

describe('summarizeFleetPorts', () => {
  it('sums fpp.ports.count and fpp.ports.blind_count across reporting instances', () => {
    const instances = [
      {
        instanceId: 'remote01',
        observations: [
          evidence('fpp.ports.count', { value: 32 }),
          evidence('fpp.ports.blind_count', { value: 16 }),
        ],
      },
      {
        instanceId: 'remote04',
        observations: [
          evidence('fpp.ports.count', { value: 48 }),
          evidence('fpp.ports.blind_count', { value: 32 }),
        ],
      },
    ]
    const summary = summarizeFleetPorts(instances)
    expect(summary.totalPorts).toBe(80)
    expect(summary.totalBlind).toBe(48)
    expect(summary.instancesReporting).toBe(2)
    expect(summary.instancesWithNoPorts).toBe(0)
  })

  // FPP-Main's real, exact condition: fpp.ports.count === 0 is a
  // measured fact (a Pi with no cape), not an absence -- it must count as
  // "reporting", contribute to instancesWithNoPorts, and never be
  // conflated with instancesUnknown (an instance whose count could not be
  // read at all).
  it('counts a reported zero as reporting-with-no-ports, distinct from an unknown/failed instance', () => {
    const instances = [
      { instanceId: 'fpp-main', observations: [evidence('fpp.ports.count', { value: 0 })] },
      { instanceId: 'fpp-broken', observations: [evidence('fpp.ports.count', { value: null, state: 'collection_failed' })] },
    ]
    const summary = summarizeFleetPorts(instances)
    expect(summary.instancesReporting).toBe(1)
    expect(summary.instancesWithNoPorts).toBe(1)
    expect(summary.instancesUnknown).toBe(1)
    expect(summary.totalPorts).toBe(0)
  })

  // Step 5 review finding 7: this used to be named/asserted as though a
  // missing fpp.ports.blind_count correctly contributed 0 -- the exact
  // "absent becomes zero" shape this step exists to eliminate. totalBlind
  // is still numerically 0 here (there is nothing numeric to sum), but the
  // fix is that the function now also reports the absence explicitly via
  // instancesBlindCountUnknown, rather than a caller having no way to tell
  // "confirmed zero blind spots" apart from "blind-spot count not
  // collected".
  it('does not throw on a missing fpp.ports.blind_count, and reports the absence via instancesBlindCountUnknown rather than silently treating it as a confirmed zero', () => {
    const instances = [{ instanceId: 'a', observations: [evidence('fpp.ports.count', { value: 5 })] }]
    const summary = summarizeFleetPorts(instances)
    expect(summary.totalPorts).toBe(5)
    expect(summary.totalBlind).toBe(0)
    expect(summary.instancesBlindCountUnknown).toBe(1)
  })

  it('does not count instancesBlindCountUnknown when blind_count is present and numeric', () => {
    const instances = [
      {
        instanceId: 'a',
        observations: [evidence('fpp.ports.count', { value: 5 }), evidence('fpp.ports.blind_count', { value: 2 })],
      },
    ]
    const summary = summarizeFleetPorts(instances)
    expect(summary.totalBlind).toBe(2)
    expect(summary.instancesBlindCountUnknown).toBe(0)
  })

  // Step 5 review finding 6: a stale or unknown_age fpp.ports.count still
  // legitimately carries a value (EvidenceValue.tsx's own contract), but a
  // fleet total built partly from one must let a caller distinguish that
  // from a total built entirely from fresh evidence.
  it('tracks instancesStaleOrUnknownAge separately from instancesReporting, without excluding the value from the total', () => {
    const instances = [
      { instanceId: 'fresh', observations: [evidence('fpp.ports.count', { value: 10, state: 'current' })] },
      { instanceId: 'stale', observations: [evidence('fpp.ports.count', { value: 5, state: 'stale' })] },
      {
        instanceId: 'ghost',
        observations: [evidence('fpp.ports.count', { value: 3, state: 'unknown_age', observedAt: null })],
      },
    ]
    const summary = summarizeFleetPorts(instances)
    expect(summary.totalPorts).toBe(18)
    expect(summary.instancesReporting).toBe(3)
    expect(summary.instancesStaleOrUnknownAge).toBe(2)
  })
})
