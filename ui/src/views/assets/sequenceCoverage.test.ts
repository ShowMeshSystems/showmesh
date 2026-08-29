import { describe, expect, it } from 'vitest'
import type { NodeAssetManifest } from '../../api'
import { buildCoverageFindings, splitJudgedNodes } from './sequenceCoverage'

function manifest(node: string, overrides: Partial<NodeAssetManifest> = {}): NodeAssetManifest {
  return {
    node,
    state: 'ready',
    reason: null,
    missing: [],
    gaps: [],
    extra: [],
    observedAt: '2026-08-28T20:41:07Z',
    ...overrides,
  }
}

describe('splitJudgedNodes', () => {
  it('never counts an unknown-state node as judged: the four-absences rule applied to coverage', () => {
    const nodes = [
      manifest('media-front'),
      manifest('media-side'),
      manifest('media-garage', { state: 'unknown', reason: 'No manifest reported since last observed.', observedAt: '2026-08-28T20:41:07Z' }),
    ]
    const byNode = new Map(nodes.map((n) => [n.node, n]))

    const { judged, unjudged } = splitJudgedNodes(['media-front', 'media-side', 'media-garage'], byNode)
    expect(judged).toEqual(['media-front', 'media-side'])
    expect(unjudged).toEqual([{ node: 'media-garage', reason: 'No manifest reported since last observed.', observedAt: '2026-08-28T20:41:07Z' }])
  })

  it('treats a node with no manifest evidence at all the same as unknown, never as covered', () => {
    const byNode = new Map<string, NodeAssetManifest>()
    const { judged, unjudged } = splitJudgedNodes(['media-front'], byNode)
    expect(judged).toEqual([])
    expect(unjudged).toHaveLength(1)
    expect(unjudged[0]?.node).toBe('media-front')
  })
})

describe('buildCoverageFindings: the mock scenario (yard-arch uncovered on both judged nodes, garage-wash short on one, media-garage stated as not judged)', () => {
  const nodes = [
    manifest('media-front', { gaps: [{ sequence: 'yard-arch', surfaces: ['front-arch'] }] }),
    manifest('media-side', { gaps: [{ sequence: 'yard-arch', surfaces: ['side-arch'] }, { sequence: 'garage-wash', surfaces: ['side-wash'] }] }),
    manifest('media-garage', { state: 'unknown', reason: 'Stale.', observedAt: '2026-08-28T20:41:07Z' }),
  ]
  const byNode = new Map(nodes.map((n) => [n.node, n]))
  const surfaceNodeIds = ['media-front', 'media-side', 'media-garage']

  it('reports "no coverage" for yard-arch: every JUDGED node (2 of 2) reports the gap', () => {
    const findings = buildCoverageFindings(['yard-arch', 'garage-wash'], surfaceNodeIds, byNode)
    const yardArch = findings.find((f) => f.sequence === 'yard-arch')
    expect(yardArch?.severity).toBe('no_coverage')
    expect(yardArch?.gapNodes.sort()).toEqual(['media-front', 'media-side'])
    expect(yardArch?.coveredNodes).toEqual([])
    expect(yardArch?.judgedTotal).toBe(2)
    expect(yardArch?.totalSurfaceNodes).toBe(3)
  })

  it('states media-garage as its own not-judged fact on the yard-arch finding, never silently counted as covered', () => {
    const findings = buildCoverageFindings(['yard-arch'], surfaceNodeIds, byNode)
    const yardArch = findings.find((f) => f.sequence === 'yard-arch')
    expect(yardArch?.unjudgedNodes).toEqual([{ node: 'media-garage', reason: 'Stale.', observedAt: '2026-08-28T20:41:07Z' }])
  })

  it('reports "short" for garage-wash: only one of two judged nodes reports the gap', () => {
    const findings = buildCoverageFindings(['garage-wash'], surfaceNodeIds, byNode)
    const garageWash = findings.find((f) => f.sequence === 'garage-wash')
    expect(garageWash?.severity).toBe('short')
    expect(garageWash?.gapNodes).toEqual(['media-side'])
    expect(garageWash?.coveredNodes).toEqual(['media-front'])
  })

  it('never produces a finding for a sequence no judged node flags a gap for', () => {
    const findings = buildCoverageFindings(['fully-covered-sequence'], surfaceNodeIds, byNode)
    expect(findings).toHaveLength(0)
  })

  it('sorts no_coverage findings ahead of short findings', () => {
    const findings = buildCoverageFindings(['garage-wash', 'yard-arch'], surfaceNodeIds, byNode)
    expect(findings.map((f) => f.sequence)).toEqual(['yard-arch', 'garage-wash'])
  })
})
