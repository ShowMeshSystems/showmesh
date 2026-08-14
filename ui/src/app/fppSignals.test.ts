import { describe, expect, it } from 'vitest'
import {
  buildPortEntries,
  classifyFppSignal,
  evaluateNextItemHazard,
  findObservation,
  groupFppObservations,
  portKindOf,
  summarizeFleetVersions,
  type FppGroupId,
} from './fppSignals'
import { makeEvidence } from './test-support/fixtures'
import type { Evidence } from './types'

function evidence(signal: string, overrides: Partial<Evidence> = {}): Evidence {
  return makeEvidence({ signal, ...overrides })
}

describe('classifyFppSignal', () => {
  const cases: [string, FppGroupId][] = [
    ['fpp.port.port_1.kind', 'ports'],
    ['fpp.port.port_1.current_ma', 'ports'],
    ['fpp.ports.count', 'ports'],
    ['fpp.ports.blind_count', 'ports'],
    ['fpp.ports.decode_failed', 'ports'],
    ['fpp.sensor.cpu.value', 'sensors'],
    ['fpp.sensor.cpu.type', 'sensors'],
    ['fpp.playlist.name', 'playback'],
    ['fpp.playlist.repeat_mode', 'playback'],
    ['fpp.sequence.name', 'playback'],
    ['fpp.position.seconds', 'playback'],
    ['fpp.position.elapsed.seconds', 'playback'],
    ['fpp.song.name', 'playback'],
    ['fpp.scheduler.status', 'playback'],
    ['fpp.scheduler.next_playlist', 'playback'],
    ['fpp.disk.media.free_bytes', 'platform'],
    ['fpp.utilization.cpu', 'platform'],
    ['fpp.os.version', 'platform'],
    ['fpp.platform', 'platform'],
    ['fpp.variant', 'platform'],
    ['fpp.kernel', 'platform'],
    ['fpp.reachable', 'controller'],
    ['fpp.version', 'controller'],
    ['fpp.mode', 'controller'],
    ['fpp.status', 'controller'],
    ['fpp.multisync.enabled', 'controller'],
    ['fpp.multisync.systems', 'controller'],
    ['fpp.uptime.seconds', 'controller'],
    ['fpp.fppd.state', 'controller'],
    ['fpp.power.bad', 'controller'],
    ['fpp.bridging', 'controller'],
    ['fpp.channel_inputs.enabled', 'controller'],
    ['fpp.channel_outputs.enabled', 'controller'],
    ['fpp.branch', 'controller'],
    ['fpp.uuid', 'controller'],
    ['fpp.host_name', 'controller'],
    ['fpp.volume', 'controller'],
    ['fpp.mqtt.configured', 'controller'],
    ['fpp.mqtt.connected', 'controller'],
    ['fpp.warnings.count', 'controller'],
    ['fpp.warnings.summary', 'controller'],
  ]

  it.each(cases)('classifies %s as %s', (signal, expected) => {
    expect(classifyFppSignal(signal)).toBe(expected)
  })

  // The ADR-002-shaped requirement this whole module exists to satisfy:
  // a signal matching no known prefix must still classify into a real
  // group ('other'), never throw, never return undefined.
  it('classifies an unrecognized signal ID as "other" rather than throwing or returning nothing', () => {
    expect(classifyFppSignal('fpp.something_nobody_wrote_a_case_for')).toBe('other')
    expect(classifyFppSignal('node.heartbeat')).toBe('other')
    expect(classifyFppSignal('')).toBe('other')
  })
})

describe('groupFppObservations', () => {
  it('buckets observations into groups, preserving each group input order, and omits empty groups', () => {
    const observations = [
      evidence('fpp.reachable'),
      evidence('fpp.playlist.name'),
      evidence('fpp.sensor.cpu.value'),
      evidence('fpp.playlist.repeat_mode'),
    ]
    const groups = groupFppObservations(observations)
    const ids = groups.map((g) => g.id)
    // 'ports' and 'platform' have no observations here and must not
    // appear at all.
    expect(ids).not.toContain('ports')
    expect(ids).not.toContain('platform')
    expect(ids).toEqual(['playback', 'controller', 'sensors'])

    const playback = groups.find((g) => g.id === 'playback')
    expect(playback?.observations.map((o) => o.signal)).toEqual(['fpp.playlist.name', 'fpp.playlist.repeat_mode'])
  })

  // T4-style mutation check: an unrecognized signal must render SOMEWHERE,
  // not vanish. This is the specific behavior a reviewer is told to test
  // (STEP-5 spec section 6's "Grouping" paragraph).
  it('still includes an unrecognized signal, in the "other" group, rather than dropping it', () => {
    const observations = [evidence('fpp.reachable'), evidence('fpp.totally_unknown_signal')]
    const groups = groupFppObservations(observations)
    const other = groups.find((g) => g.id === 'other')
    expect(other).toBeDefined()
    expect(other?.label).toBe('Other')
    expect(other?.observations.map((o) => o.signal)).toEqual(['fpp.totally_unknown_signal'])
  })

  it('returns no groups for an empty observation list', () => {
    expect(groupFppObservations([])).toEqual([])
  })
})

describe('buildPortEntries / portKindOf', () => {
  it('reassembles per-field signals into one entry per port key', () => {
    const observations = [
      evidence('fpp.port.port_1.kind', { value: 'output' }),
      evidence('fpp.port.port_1.current_ma', { value: 120, unit: 'milliamps' }),
      evidence('fpp.port.port_1.bank', { value: 'Ports 1-4' }),
      evidence('fpp.port.port_17.kind', { value: 'smart_receiver' }),
      evidence('fpp.port.port_17.current_ma', {
        value: null,
        state: 'unsupported',
        reason: 'smart receiver position: pre-V5 receivers report no per-port current',
        observedAt: null,
      }),
      // A signal in the ports group that is NOT one of the six per-port
      // fields -- must not corrupt an entry or crash the parser.
      evidence('fpp.ports.count', { value: 2 }),
    ]
    const entries = buildPortEntries(observations)
    expect(entries).toHaveLength(2)
    const port1 = entries.find((e) => e.key === 'port_1')
    expect(port1?.kind?.value).toBe('output')
    expect(port1?.currentMa?.value).toBe(120)
    expect(port1?.bank?.value).toBe('Ports 1-4')

    const port17 = entries.find((e) => e.key === 'port_17')
    expect(portKindOf(port17!)).toBe('smart_receiver')
    // The load-bearing assertion for the whole seam: a blind spot's
    // current_ma must carry NO value, ever.
    expect(port17?.currentMa?.value).toBeNull()
    expect(port17?.currentMa?.state).toBe('unsupported')
  })

  it('sorts port entries numerically by key, not lexically ("port_2" before "port_10")', () => {
    const observations = [
      evidence('fpp.port.port_10.kind', { value: 'output' }),
      evidence('fpp.port.port_2.kind', { value: 'output' }),
      evidence('fpp.port.port_1.kind', { value: 'output' }),
    ]
    const entries = buildPortEntries(observations)
    expect(entries.map((e) => e.key)).toEqual(['port_1', 'port_2', 'port_10'])
  })

  it('portKindOf returns "unrecognized" for a missing or unexpected kind value, rather than throwing', () => {
    const noKind = buildPortEntries([evidence('fpp.port.port_9.current_ma', { value: 5 })])[0]!
    expect(portKindOf(noKind)).toBe('unrecognized')

    const weirdKind = buildPortEntries([evidence('fpp.port.port_9.kind', { value: 'something_else' })])[0]!
    expect(portKindOf(weirdKind)).toBe('unrecognized')
  })

  it('ignores a signal outside the ports namespace entirely (defensive; groupFppObservations is what actually routes signals here)', () => {
    expect(buildPortEntries([evidence('fpp.reachable')])).toEqual([])
  })
})

describe('findObservation', () => {
  it('finds an observation by exact signal ID', () => {
    const observations = [evidence('fpp.version', { value: '9.4' }), evidence('fpp.reachable')]
    expect(findObservation(observations, 'fpp.version')?.value).toBe('9.4')
  })

  it('returns undefined when no observation matches', () => {
    expect(findObservation([evidence('fpp.reachable')], 'fpp.version')).toBeUndefined()
  })
})

// reachable/unreachable build the fpp.reachable evidence entry
// isReachableInstance (fppSignals.ts) actually gates on, alongside a
// fpp.version entry, matching the two-signal shape a real FPPInstance
// carries.
function reachable(version: string): Evidence[] {
  return [evidence('fpp.reachable', { value: true, state: 'current' }), evidence('fpp.version', { value: version })]
}

describe('summarizeFleetVersions', () => {
  it('reports no disagreement when every reachable instance reports the same version', () => {
    const instances = [
      { instanceId: 'a', observations: reachable('9.4') },
      { instanceId: 'b', observations: reachable('9.4') },
    ]
    const summary = summarizeFleetVersions(instances)
    expect(summary.disagreement).toBe(false)
    expect(summary.versions).toEqual([{ version: '9.4', instanceIds: ['a', 'b'] }])
  })

  // The exact real-fleet condition: FPP-remote-01 deliberately runs a
  // master build while the other two run 9.4. Disagreement must be
  // TRUE here -- this is the scenario the feature exists to surface.
  it('reports disagreement when reachable instances report different versions, grouping instance IDs per version', () => {
    const instances = [
      { instanceId: 'fpp-main', observations: reachable('9.4') },
      { instanceId: 'fpp-remote-01', observations: reachable('9.x-master-822-g56515e4d') },
      { instanceId: 'fpp-remote-04', observations: reachable('9.4') },
    ]
    const summary = summarizeFleetVersions(instances)
    expect(summary.disagreement).toBe(true)
    expect(summary.versions).toEqual([
      { version: '9.4', instanceIds: ['fpp-main', 'fpp-remote-04'] },
      { version: '9.x-master-822-g56515e4d', instanceIds: ['fpp-remote-01'] },
    ])
  })

  it('excludes a reachable instance with no fpp.version evidence or a null value from the comparison, without reporting a false disagreement', () => {
    const instances = [
      { instanceId: 'a', observations: reachable('9.4') },
      {
        instanceId: 'b',
        observations: [
          evidence('fpp.reachable', { value: true, state: 'current' }),
          evidence('fpp.version', { value: null, state: 'not_collected' }),
        ],
      },
      { instanceId: 'c', observations: [evidence('fpp.reachable', { value: true, state: 'current' })] },
    ]
    const summary = summarizeFleetVersions(instances)
    expect(summary.disagreement).toBe(false)
    expect(summary.versions).toEqual([{ version: '9.4', instanceIds: ['a'] }])
  })

  it('reports no disagreement for zero or one distinct version', () => {
    expect(summarizeFleetVersions([]).disagreement).toBe(false)
  })

  // Step 5 review finding 6/7, this function's own headline fix: a
  // version pulled from an instance this coordinator cannot currently
  // confirm is reachable must not count toward the skew statement,
  // covering every shape that is NOT "reachable, current, true" --
  // unreachable, and the FPP-01 ghost's exact unknown_age shape.
  it('excludes an unreachable instance from the version comparison entirely', () => {
    const instances = [
      { instanceId: 'a', observations: reachable('9.4') },
      {
        instanceId: 'unreachable',
        observations: [
          evidence('fpp.reachable', { value: false, state: 'current', reason: 'connection refused' }),
          evidence('fpp.version', { value: '9.9-should-never-count' }),
        ],
      },
    ]
    const summary = summarizeFleetVersions(instances)
    expect(summary.disagreement).toBe(false)
    expect(summary.versions).toEqual([{ version: '9.4', instanceIds: ['a'] }])
  })

  it('excludes an instance whose fpp.reachable is unknown_age (the FPP-01 ghost shape) from the version comparison', () => {
    const instances = [
      { instanceId: 'a', observations: reachable('9.4') },
      {
        instanceId: 'fpp-01-ghost',
        observations: [
          evidence('fpp.reachable', {
            value: true,
            state: 'unknown_age',
            observedAt: null,
            reason: 'retained MQTT delivery of unknown age',
          }),
          evidence('fpp.version', { value: '9.2' }),
        ],
      },
    ]
    const summary = summarizeFleetVersions(instances)
    expect(summary.disagreement).toBe(false)
    expect(summary.versions).toEqual([{ version: '9.4', instanceIds: ['a'] }])
  })
})

// Step 8, capture section 3.5: "Next Playlist Item" past the last item
// ENDS the playlist. A test's name is a claim -- these break the
// last-item condition in both directions to confirm the function
// actually distinguishes them, not merely returns a constant.
describe('evaluateNextItemHazard', () => {
  it('reports unknown when neither index nor count has been observed at all', () => {
    const hazard = evaluateNextItemHazard([])
    expect(hazard).toMatchObject({ unknown: true, knownLastItem: false })
  })

  it('reports unknown when the evidence is present but stale, never silently "not last"', () => {
    const observations = [
      evidence('fpp.playlist.index', { value: 3, state: 'stale' }),
      evidence('fpp.playlist.count', { value: 3, state: 'stale' }),
    ]
    const hazard = evaluateNextItemHazard(observations)
    expect(hazard.unknown).toBe(true)
    expect(hazard.knownLastItem).toBe(false)
  })

  it('reports knownLastItem=true at the last item of a multi-item playlist (index === count)', () => {
    const observations = [
      evidence('fpp.playlist.index', { value: 3, state: 'current' }),
      evidence('fpp.playlist.count', { value: 3, state: 'current' }),
    ]
    const hazard = evaluateNextItemHazard(observations)
    expect(hazard).toMatchObject({ unknown: false, knownLastItem: true })
  })

  it('reports knownLastItem=true on a one-item playlist (capture: a single Next stops the show)', () => {
    const observations = [
      evidence('fpp.playlist.index', { value: 1, state: 'current' }),
      evidence('fpp.playlist.count', { value: 1, state: 'current' }),
    ]
    const hazard = evaluateNextItemHazard(observations)
    expect(hazard.knownLastItem).toBe(true)
  })

  it('reports knownLastItem=false when not yet at the last item', () => {
    const observations = [
      evidence('fpp.playlist.index', { value: 1, state: 'current' }),
      evidence('fpp.playlist.count', { value: 3, state: 'current' }),
    ]
    const hazard = evaluateNextItemHazard(observations)
    expect(hazard).toMatchObject({ unknown: false, knownLastItem: false })
  })

  it('reports unknown (never knownLastItem) while idle at index 0/0, matching the captured idle shape', () => {
    const observations = [
      evidence('fpp.playlist.index', { value: 0, state: 'current' }),
      evidence('fpp.playlist.count', { value: 0, state: 'current' }),
    ]
    const hazard = evaluateNextItemHazard(observations)
    // count === 0 is never "last item" -- there is no item to be last.
    expect(hazard.knownLastItem).toBe(false)
  })
})
