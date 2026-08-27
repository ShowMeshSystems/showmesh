import { describe, expect, it } from 'vitest'
import { makeEvidence, makeModel, makeNode, makeFPPInstance, makeResolumeInstance } from './test-support/fixtures'
import { observationDisplayState, presentModelObservations } from './observationPresentation'

describe('observationPresentation', () => {
  it.each([
    ['current', 'current'],
    ['stale', 'stale'],
    ['unknown_age', 'unknown'],
    ['collection_failed', 'failed'],
    ['not_collected', 'unobserved'],
    ['unsupported', 'unobserved'],
  ] as const)('maps wire state %s to %s', (state, expected) => {
    expect(observationDisplayState(makeEvidence({ state, value: state === 'not_collected' || state === 'collection_failed' || state === 'unsupported' ? null : true }))).toBe(expected)
  })

  it('flattens node, render, audio, FPP, and Resolume evidence with resource identity', () => {
    const node = makeNode('node-1', {
      render: [{ ...makeEvidence({ signal: 'surface.pipeline.state' }), resource: { kind: 'surface', id: 'front' } }],
      audio: [{ ...makeEvidence({ signal: 'audio_session.state' }), resource: { kind: 'audio_session', id: 'main' } }],
    })
    const model = makeModel({
      nodes: [node],
      fpp: [makeFPPInstance('fpp-1', { observations: [makeEvidence({ signal: 'fpp.status' })] })],
      resolume: [makeResolumeInstance('arena-1', { observations: [makeEvidence({ signal: 'resolume.reachable' })] })],
    })

    const rows = presentModelObservations(model)
    expect(rows.map((row) => row.meaning)).toEqual([
      'Node',
      'Node',
      'Node',
      'Render surface',
      'Audio',
      'FPP player',
      'Resolume',
    ])
    expect(rows.find((row) => row.signal === 'fpp.status')?.href).toBe('/fpp/fpp-1')
    expect(rows.find((row) => row.signal === 'surface.pipeline.state')?.href).toBeNull()
  })
})
