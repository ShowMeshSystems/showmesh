import { describe, expect, it } from 'vitest'
import type { Evidence, Node } from '../api'
import { makeEvidence } from '../api/test-support/fixtures'
import { nodeSyncStatus } from './nodeSyncModel'

function nodeWith(audio: Evidence[], clock: Evidence[] = []): Node {
  return { audio, clock } as unknown as Node
}

function ev(signal: string, overrides: Partial<Evidence> = {}): Evidence {
  return makeEvidence({ signal, ...overrides })
}

const FOLLOWS = 'PTP 000fd4fffe06553a:0'

describe('nodeSyncStatus', () => {
  it('locked with an offset under 1 ms renders µs to one decimal', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.sync.state', { value: 'locked' }),
        ev('node.audio.sync.follows', { value: FOLLOWS }),
        ev('node.audio.sync.offset_ns', { value: 200 }),
        ev('node.audio.sync.rate_ppm', { state: 'not_collected', value: null, reason: 'not measured' }),
      ]),
    )
    expect(status.syncLine).toEqual({ kind: 'value', value: `Sync: Locked. Follows ${FOLLOWS}, δ 0.2 µs` })
  })

  it('locked with no offset collected omits the offset clause rather than inventing zero', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.sync.state', { value: 'locked' }),
        ev('node.audio.sync.follows', { value: FOLLOWS }),
        ev('node.audio.sync.offset_ns', { state: 'not_collected', value: null, reason: 'no offset reported' }),
        ev('node.audio.sync.rate_ppm', { state: 'not_collected', value: null, reason: 'not measured' }),
      ]),
    )
    expect(status.syncLine).toEqual({ kind: 'value', value: `Sync: Locked. Follows ${FOLLOWS}` })
  })

  it('acquiring reads as acquiring, following the same grandmaster', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.sync.state', { value: 'acquiring' }),
        ev('node.audio.sync.follows', { value: FOLLOWS }),
        ev('node.audio.sync.offset_ns', { state: 'not_collected', value: null, reason: 'no offset reported' }),
        ev('node.audio.sync.rate_ppm', { state: 'not_collected', value: null, reason: 'not measured' }),
      ]),
    )
    expect(status.syncLine).toEqual({ kind: 'value', value: `Sync: Acquiring. Follows ${FOLLOWS}` })
  })

  it('free-running reads as free-running on the local clock, with no follows or offset clause', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.sync.state', { value: 'free_running' }),
        ev('node.audio.sync.follows', { value: '' }),
        ev('node.audio.sync.offset_ns', { state: 'not_collected', value: null, reason: 'this node is not following a global clock' }),
        ev('node.audio.sync.rate_ppm', { state: 'not_collected', value: null, reason: 'not measured' }),
      ]),
    )
    expect(status.syncLine).toEqual({ kind: 'value', value: 'Sync: Free-running on the local clock' })
  })

  it('a sync state that was never collected reports the absence and the node\'s reason, never a guessed line', () => {
    const status = nodeSyncStatus(
      nodeWith([ev('node.audio.sync.state', { state: 'not_collected', value: null, reason: 'this node has never reported its clock status' })]),
    )
    expect(status.syncLine).toEqual({ kind: 'absent', absence: 'unobserved', label: 'Unobserved', fact: 'this node has never reported its clock status' })
  })

  it('a sync state this build does not know never borrows a known state\'s name', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.sync.state', { value: 'holdover' }),
        ev('node.audio.sync.follows', { value: FOLLOWS }),
        ev('node.audio.sync.offset_ns', { value: 200 }),
        ev('node.audio.sync.rate_ppm', { state: 'not_collected', value: null, reason: 'not measured' }),
      ]),
    )
    expect(status.syncLine).toEqual({
      kind: 'absent',
      absence: 'unavailable',
      label: 'Unavailable',
      fact: 'This node reported a sync state this version does not know: holdover.',
    })
  })

  it('a stale sync state reports stale, not a value read from before it went stale', () => {
    const status = nodeSyncStatus(nodeWith([ev('node.audio.sync.state', { state: 'stale', value: 'locked', reason: 'no fresher clock report has arrived' })]))
    expect(status.syncLine).toEqual({ kind: 'absent', absence: 'stale', label: 'Stale', fact: 'no fresher clock report has arrived' })
  })

  it('an offset at or above 1 ms renders ms to two decimals, negative sign kept', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.sync.state', { value: 'locked' }),
        ev('node.audio.sync.follows', { value: FOLLOWS }),
        ev('node.audio.sync.offset_ns', { value: -1_234_000 }),
        ev('node.audio.sync.rate_ppm', { state: 'not_collected', value: null, reason: 'not measured' }),
      ]),
    )
    expect(status.syncLine).toEqual({ kind: 'value', value: `Sync: Locked. Follows ${FOLLOWS}, δ -1.23 ms` })
  })

  it('a measured rate is appended signed, with the sign kept for zero', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.sync.state', { value: 'locked' }),
        ev('node.audio.sync.follows', { value: FOLLOWS }),
        ev('node.audio.sync.offset_ns', { value: 200 }),
        ev('node.audio.sync.rate_ppm', { value: 0 }),
      ]),
    )
    expect(status.syncLine).toEqual({ kind: 'value', value: `Sync: Locked. Follows ${FOLLOWS}, δ 0.2 µs, rate +0.00 ppm` })
  })

  it('a measured clock steer renders signed and two decimals', () => {
    const status = nodeSyncStatus(nodeWith([], [ev('node.clock.ptp.frequency_ppm', { value: 15.294 })]))
    expect(status.steer).toEqual({ kind: 'value', value: 'Clock steered +15.29 ppm' })
  })

  it('a clock steer that has never been measured carries the node\'s own reason', () => {
    const status = nodeSyncStatus(
      nodeWith([], [ev('node.clock.ptp.frequency_ppm', { state: 'not_collected', value: null, reason: 'this interface has no PHC' })]),
    )
    expect(status.steer).toEqual({ kind: 'absent', absence: 'unobserved', label: 'Unobserved', fact: 'this interface has no PHC' })
  })

  it('an override local clock is marked set by the operator', () => {
    const status = nodeSyncStatus(
      nodeWith([
        ev('node.audio.clock.local', { value: 'house word clock' }),
        ev('node.audio.clock.local.source', { value: 'override' }),
      ]),
    )
    expect(status.localClock).toEqual({ kind: 'value', value: { name: 'house word clock', setByOperator: true } })
  })

  it('a derived local clock is not marked set by the operator', () => {
    const status = nodeSyncStatus(
      nodeWith([ev('node.audio.clock.local', { value: 'usb-audio-0' }), ev('node.audio.clock.local.source', { value: 'derived' })]),
    )
    expect(status.localClock).toEqual({ kind: 'value', value: { name: 'usb-audio-0', setByOperator: false } })
  })
})
