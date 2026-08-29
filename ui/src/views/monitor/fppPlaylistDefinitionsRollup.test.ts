import { describe, expect, it } from 'vitest'
import type { FPPPlaylistDefinitionMetadata } from '../../app/types'
import {
  CAPTURE_DRIFT_THRESHOLD_MS,
  annotateBound,
  findCaptureDrift,
  findHeldGroups,
  groupDefinitions,
  shortDefinitionHash,
} from './fppPlaylistDefinitionsRollup'

function def(overrides: Partial<FPPPlaylistDefinitionMetadata> = {}): FPPPlaylistDefinitionMetadata {
  return {
    instanceUuid: 'uuid-barn',
    playlistName: 'WR26 Main Show',
    playlistHash: '9f2caaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa41d',
    capturedAt: '2026-08-25T18:02:11Z',
    receivedAt: '2026-08-25T18:02:26Z',
    entryCount: 6,
    referenced: true,
    ...overrides,
  }
}

describe('shortDefinitionHash', () => {
  it('keeps the first four and last four hex characters', () => {
    expect(shortDefinitionHash('4c1f000000000000000000000000000000000000000000000000000000009e02')).toBe('4c1f…9e02')
  })

  it('returns a short hash unchanged', () => {
    expect(shortDefinitionHash('abcd')).toBe('abcd')
  })
})

describe('the held-bindings story (owner ruling: causal, not two unrelated rows)', () => {
  it('finds a group whose newest definition is unreferenced while an older sibling stays bound: WR26 Main Show / barn-player', () => {
    const bound = def({ playlistHash: '9f2c...a41d', referenced: true, receivedAt: '2026-08-25T18:02:26Z' })
    const newest = def({ playlistHash: '4c1f...9e02', referenced: false, receivedAt: '2026-08-28T20:54:03Z' })
    const definitions = [bound, newest]

    const held = findHeldGroups(definitions)
    expect(held).toHaveLength(1)
    const [finding] = held
    expect(finding?.playlistName).toBe('WR26 Main Show')
    expect(finding?.boundHash).toBe('9f2c...a41d')
    expect(finding?.newHash).toBe('4c1f...9e02')

    const groups = groupDefinitions(definitions)
    expect(annotateBound(newest, groups)).toEqual({ bound: false, caption: 'Newer than the bound one' })
    expect(annotateBound(bound, groups)).toEqual({ bound: true, caption: null })
  })

  it('does not call an unreferenced definition "held" when nothing in its group is bound at all: Garage Loop', () => {
    const garageLoop = def({ playlistName: 'Garage Loop', instanceUuid: 'uuid-garage', playlistHash: '61ae...c7d3', referenced: false })
    const definitions = [garageLoop]

    expect(findHeldGroups(definitions)).toHaveLength(0)
    const groups = groupDefinitions(definitions)
    expect(annotateBound(garageLoop, groups)).toEqual({ bound: false, caption: 'No playlist binds it' })
  })

  it('does not call an older unreferenced sibling "held" (it was superseded, not withheld)', () => {
    const bound = def({ playlistHash: 'bound-hash', referenced: true, receivedAt: '2026-08-25T18:02:26Z' })
    const older = def({ playlistHash: 'older-hash', referenced: false, receivedAt: '2026-08-24T00:00:00Z' })
    const definitions = [bound, older]

    expect(findHeldGroups(definitions)).toHaveLength(0)
    const groups = groupDefinitions(definitions)
    expect(annotateBound(older, groups)).toEqual({ bound: false, caption: 'Not the bound definition' })
  })
})

describe('capture drift', () => {
  it('flags an unreferenced definition whose received time trails captured time past the threshold', () => {
    const capturedAt = '2026-08-24T16:20:11Z'
    const receivedAt = '2026-08-26T09:41:02Z'
    expect(Date.parse(receivedAt) - Date.parse(capturedAt)).toBeGreaterThan(CAPTURE_DRIFT_THRESHOLD_MS)
    const definitions = [def({ playlistName: 'Garage Loop', referenced: false, capturedAt, receivedAt })]

    const drift = findCaptureDrift(definitions)
    expect(drift).toHaveLength(1)
    expect(drift[0]?.playlistName).toBe('Garage Loop')
  })

  it('never flags a referenced (bound) definition, however old its capture', () => {
    const definitions = [
      def({ referenced: true, capturedAt: '2026-08-01T00:00:00Z', receivedAt: '2026-08-28T00:00:00Z' }),
    ]
    expect(findCaptureDrift(definitions)).toHaveLength(0)
  })

  it('does not flag an unreferenced definition under the threshold', () => {
    const definitions = [
      def({ referenced: false, capturedAt: '2026-08-25T18:02:11Z', receivedAt: '2026-08-25T18:02:26Z' }),
    ]
    expect(findCaptureDrift(definitions)).toHaveLength(0)
  })
})
