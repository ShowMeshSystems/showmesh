import { describe, expect, it } from 'vitest'
import type { Node } from '../api'
import { nodeSignalGroups } from './monitorModel'

// node.audio.clock.alignment.state must be legible in the node
// drawer beside the other audio evidence, with the tone carrying the
// threshold verdict, not just a plain current/stale reading.

const alignmentStateEvidence = (value: string): Node['audio'][number] =>
  ({
    resource: { kind: 'node', id: 'audio-01' },
    signal: 'node.audio.clock.alignment.state',
    value,
    unit: null,
    state: 'current',
    reason: null,
    observedAt: '2026-09-11T10:00:00Z',
    collectedAt: '2026-09-11T10:01:00Z',
    source: 'node-audio:audio-01',
    quality: 'reported',
  }) as unknown as Node['audio'][number]

function nodeWithAudio(audio: Node['audio']): Node {
  return {
    nodeId: 'audio-01',
    label: 'audio-01',
    agentVersion: '0.9.4',
    capabilities: [],
    controlPlane: { state: 'online', reason: null },
    evidence: {},
    declaration: {},
    render: [],
    audio,
    clock: [],
    fppConnect: [],
  } as unknown as Node
}

describe('nodeSignalGroups · Audio', () => {
  it('renders the alignment state row with the warn tone when beyond threshold', () => {
    const groups = nodeSignalGroups(nodeWithAudio([alignmentStateEvidence('beyond_threshold')]))
    const audio = groups.find((g) => g.name === 'Audio')
    const row = audio?.rows.find((r) => r.label === 'node.audio.clock.alignment.state')
    expect(row).toBeDefined()
    expect(row?.value).toBe('beyond_threshold')
    expect(row?.tone).toBe('warn')
  })

  it('renders the alignment state row with the good tone when within threshold', () => {
    const groups = nodeSignalGroups(nodeWithAudio([alignmentStateEvidence('within_threshold')]))
    const audio = groups.find((g) => g.name === 'Audio')
    const row = audio?.rows.find((r) => r.label === 'node.audio.clock.alignment.state')
    expect(row).toBeDefined()
    expect(row?.value).toBe('within_threshold')
    expect(row?.tone).toBe('good')
  })

  it('keeps the alignment state row visible past the shared 6-row default', () => {
    const padding = Array.from({ length: 11 }, (_, i) => ({
      ...alignmentStateEvidence('within_threshold'),
      signal: `node.audio.padding.${i}`,
    })) as Node['audio']
    const audio = padding.concat([alignmentStateEvidence('beyond_threshold')])
    const groups = nodeSignalGroups(nodeWithAudio(audio))
    const row = groups.find((g) => g.name === 'Audio')?.rows.find((r) => r.label === 'node.audio.clock.alignment.state')
    expect(row).toBeDefined()
  })
})
