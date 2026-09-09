import { describe, expect, it } from 'vitest'
import type { FPPInstance, Node, ResolumeInstance } from '../api'
import { initialModel } from '../api/domain'
import {
  attentionItems,
  fppAttention,
  nodeAttention,
  participationLabel,
  resolumeAttention,
  type ParticipationState,
} from './dashboardModel'

const evidence = (state: string) => ({
  signal: 's',
  value: 1,
  unit: null,
  state,
  reason: null,
  observedAt: '2026-08-28T21:00:00Z',
  collectedAt: '2026-08-28T21:00:00Z',
  source: 'agent',
  quality: 'reported',
}) as unknown as Node['evidence']['heartbeat']

function node(
  nodeId: string,
  controlPlaneState: 'offline' | 'unknown',
  showParticipation: Node['showParticipation'] | undefined,
): Node {
  const base = {
    nodeId,
    label: nodeId,
    platform: null,
    agentVersion: null,
    bootId: null,
    startedAt: null,
    firstSeenAt: '2026-08-28T12:00:00Z',
    updatedAt: '2026-08-28T21:00:00Z',
    capabilities: [],
    controlPlane: { state: controlPlaneState, reason: null },
    evidence: {
      hello: evidence('current'),
      lastWill: evidence('not_collected'),
      heartbeat: evidence(controlPlaneState === 'offline' ? 'stale' : 'current'),
    },
    declaration: {} as Node['declaration'],
    showParticipation,
    render: [],
    audio: [],
    fppConnect: [],
    clock: [],
  }
  if (showParticipation === undefined) {
    delete (base as Partial<typeof base>).showParticipation
  }
  return base as unknown as Node
}

function fpp(instanceId: string, health: FPPInstance['health']): FPPInstance {
  return {
    instanceId,
    endpoint: 'http://198.51.100.1',
    health,
    observations: [],
    lastPollAt: null,
    lastPollError: null,
    instanceUuid: 'u',
    instanceUuidFirstObservedAt: null,
    instanceUuidChange: null,
    duplicateInstanceUuidEndpointIds: [],
  } as unknown as FPPInstance
}

function resolumeInstance(instanceId: string, health: ResolumeInstance['health']): ResolumeInstance {
  return {
    instanceId,
    health,
    observations: [],
    composition: null,
  } as unknown as ResolumeInstance
}

describe('nodeAttention participation', () => {
  const cases: Array<{ state: Node['showParticipation']['state']; show: string; reason: string | null }> = [
    { state: 'participating', show: 'halloween-2026', reason: null },
    { state: 'not_participating', show: 'halloween-2026', reason: null },
    { state: 'unknown', show: '', reason: 'store error' },
    { state: 'not_configured', show: '', reason: 'no show active' },
  ]

  for (const showParticipation of cases) {
    it(`carries ${showParticipation.state} through unchanged for an offline node`, () => {
      const items = nodeAttention([node('n1', 'offline', showParticipation)], null)
      expect(items).toHaveLength(1)
      expect(items[0]?.participation).toBe(showParticipation.state)
    })
  }

  it('carries the absent case when an older coordinator omits showParticipation entirely', () => {
    const items = nodeAttention([node('n1', 'offline', undefined)], null)
    expect(items).toHaveLength(1)
    expect(items[0]?.participation).toBe<ParticipationState>('absent')
  })

  it('keeps absent distinct from unknown and not_participating for an unknown-control-plane node', () => {
    const absent = nodeAttention([node('n1', 'unknown', undefined)], null)
    const unknown = nodeAttention([node('n2', 'unknown', { state: 'unknown', show: '', reason: 'store error' })], null)
    const notParticipating = nodeAttention(
      [node('n3', 'unknown', { state: 'not_participating', show: 'halloween-2026', reason: null })],
      null,
    )
    expect(absent[0]?.participation).toBe('absent')
    expect(unknown[0]?.participation).toBe('unknown')
    expect(notParticipating[0]?.participation).toBe('not_participating')
    expect(new Set([absent[0]?.participation, unknown[0]?.participation, notParticipating[0]?.participation]).size).toBe(3)
  })
})

describe('fppAttention participation', () => {
  it('carries the absent case for a degraded instance, never a guessed value', () => {
    const items = fppAttention([fpp('fpp-1', 'degraded')])
    expect(items).toHaveLength(1)
    expect(items[0]?.participation).toBe<ParticipationState>('absent')
  })

  it('carries the absent case for a failed instance', () => {
    const items = fppAttention([fpp('fpp-1', 'failed')])
    expect(items).toHaveLength(1)
    expect(items[0]?.participation).toBe<ParticipationState>('absent')
  })
})

describe('resolumeAttention participation', () => {
  it('carries the absent case for a degraded instance, never a guessed value', () => {
    const items = resolumeAttention([resolumeInstance('res-1', 'degraded')])
    expect(items).toHaveLength(1)
    expect(items[0]?.participation).toBe<ParticipationState>('absent')
  })

  it('carries the absent case for a failed instance', () => {
    const items = resolumeAttention([resolumeInstance('res-1', 'failed')])
    expect(items).toHaveLength(1)
    expect(items[0]?.participation).toBe<ParticipationState>('absent')
  })
})

describe('attentionItems ordering', () => {
  it('sorts a participating item above a non-participating item whose tone would otherwise put it first', () => {
    const participant = node('participant', 'unknown', { state: 'participating', show: 'halloween-2026', reason: null })
    const nonParticipant = node('offline-node', 'offline', { state: 'not_participating', show: 'halloween-2026', reason: null })
    const items = attentionItems({ ...initialModel(), nodes: [nonParticipant, participant] }, null)
    expect(items.map((item) => item.key)).toEqual(['node:participant', 'node:offline-node'])
  })

  it('keeps the existing tone order within non-participants, absent items included', () => {
    const bad = node('bad-node', 'offline', undefined)
    const unknownParticipation = node('unknown-node', 'unknown', { state: 'unknown', show: '', reason: 'store error' })
    const items = attentionItems({ ...initialModel(), nodes: [unknownParticipation, bad] }, null)
    expect(items.map((item) => item.key)).toEqual(['node:bad-node', 'node:unknown-node'])
  })
})

describe('participationLabel', () => {
  it('renders the coordinator’s own word, spaced, for each reported state', () => {
    expect(participationLabel('participating')).toBe('participating')
    expect(participationLabel('not_participating')).toBe('not participating')
    expect(participationLabel('unknown')).toBe('unknown')
    expect(participationLabel('not_configured')).toBe('not configured')
  })

  it('says nothing for absent, the older-coordinator case', () => {
    expect(participationLabel('absent')).toBeNull()
  })
})
