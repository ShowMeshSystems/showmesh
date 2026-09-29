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
  it('keeps the alignment state row visible regardless of its position among other audio rows', () => {
    const padding = Array.from({ length: 20 }, (_, i) => ({
      ...alignmentStateEvidence('within_threshold'),
      signal: `node.audio.padding.${i}`,
    })) as Node['audio']
    const audio = padding.concat([alignmentStateEvidence('beyond_threshold')])
    const groups = nodeSignalGroups(nodeWithAudio(audio))
    const row = groups.find((g) => g.name === 'Audio')?.rows.find((r) => r.label === 'node.audio.clock.alignment.state')
    expect(row).toBeDefined()
    expect(row?.value).toBe('beyond_threshold')
  })

  it('renders a real, current empty-string value as "None", never a blank cell', () => {
    const audio = [{ ...alignmentStateEvidence(''), signal: 'node.audio.settings.reason' }] as Node['audio']
    const groups = nodeSignalGroups(nodeWithAudio(audio))
    const row = groups.find((g) => g.name === 'Audio')?.rows.find((r) => r.label === 'node.audio.settings.reason')
    expect(row).toBeDefined()
    expect(row?.value).toBe('None')
  })
})

describe('nodeSignalGroups · audio setting substitution', () => {
  const entry = (signal: string, value: string): Node['audio'][number] => ({ ...alignmentStateEvidence(value), signal })
  const audioRows = (state: string, fields: string, reason: string) =>
    nodeSignalGroups(
      nodeWithAudio([
        entry('node.audio.settings.state', state),
        entry('node.audio.settings.substituted_fields', fields),
        entry('node.audio.settings.reason', reason),
      ]),
    ).find((g) => g.name === 'Audio')!.rows

  it('renders a substituted state as one warning row naming the field in operator words', () => {
    const rows = audioRows('substituted', 'DuckFadeDurationMs; LTCFrameRate', 'DuckFadeDurationMs 0 is not positive')
    expect(rows).toHaveLength(1)
    expect(rows[0]!.tone).toBe('warn')
    expect(rows[0]!.value).toContain('Duck fade duration and LTC frame rate')
    expect(rows[0]!.value).not.toContain('DuckFadeDurationMs')
    expect(rows[0]!.detail).toContain('DuckFadeDurationMs 0 is not positive')
  })

  it('leaves an accepted state as the quiet good row', () => {
    const rows = audioRows('accepted', '', '')
    expect(rows.find((r) => r.label === 'node.audio.settings.state')?.tone).toBe('good')
    expect(rows.find((r) => r.label === 'node.audio.settings.state')?.state).toBeNull()
  })
})
