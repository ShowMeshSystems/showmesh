import type { NodeAssetManifest } from '../../api'

// Shows / Assets, "Sequence coverage" (revision-1 answer to the owner's
// question about the asset manifest's `gaps` and `extra`): pure derivation
// over already-fetched manifest evidence, kept apart from rendering so the
// four-absences rule stays enforceable at the type level.
//
// A node whose manifest could not be judged (`state: 'unknown'`) is NEVER
// folded into "covered" -- that would be the four-absences rule broken in
// the most dangerous direction, telling an operator a sequence is
// delivered everywhere when one node was simply never checked. It is
// reported separately, as `unjudgedNodes`, on every finding it applies to.

export interface CoverageUnjudgedNode {
  node: string
  reason: string | null
  observedAt: string | null
}

export type CoverageSeverity = 'no_coverage' | 'short'

export interface CoverageFinding {
  sequence: string
  severity: CoverageSeverity
  /** Judged nodes (state ready or not_ready) that reported no coverage for this sequence at all. */
  gapNodes: string[]
  /** Judged nodes that did NOT report a gap for this sequence. */
  coveredNodes: string[]
  /** judgedTotal === gapNodes.length + coveredNodes.length. */
  judgedTotal: number
  /** Every node carrying a surface in this show, judged or not. */
  totalSurfaceNodes: number
  /** Nodes carrying a surface in this show whose manifest state is unknown: not judged either way. */
  unjudgedNodes: CoverageUnjudgedNode[]
}

/**
 * Splits the nodes carrying a surface in this show into judged (an actual
 * ready/not_ready verdict) and unjudged (`unknown`, or no manifest
 * evidence for this node at all) -- the same split every finding reports.
 */
export function splitJudgedNodes(
  surfaceNodeIds: string[],
  manifestByNodeId: Map<string, NodeAssetManifest>,
): { judged: string[]; unjudged: CoverageUnjudgedNode[] } {
  const judged: string[] = []
  const unjudged: CoverageUnjudgedNode[] = []
  for (const nodeId of surfaceNodeIds) {
    const manifest = manifestByNodeId.get(nodeId)
    if (manifest === undefined) {
      unjudged.push({ node: nodeId, reason: 'This coordinator has no manifest evidence for this node.', observedAt: null })
    } else if (manifest.state === 'unknown') {
      unjudged.push({ node: nodeId, reason: manifest.reason, observedAt: manifest.observedAt })
    } else {
      judged.push(nodeId)
    }
  }
  return { judged, unjudged }
}

/**
 * One finding per sequence that at least one JUDGED node reports a gap
 * (`NodeAssetManifest.gaps`) for. `severity` is `'no_coverage'` when every
 * judged node reports the gap (nothing will render it tonight) and
 * `'short'` when only some do (a delivery problem on one node, not an
 * authoring hole). A sequence no judged node flags is not a finding here,
 * whatever the unjudged nodes' state -- this list is only ever built from
 * evidence the coordinator actually has.
 */
export function buildCoverageFindings(
  sequences: string[],
  surfaceNodeIds: string[],
  manifestByNodeId: Map<string, NodeAssetManifest>,
): CoverageFinding[] {
  const { judged, unjudged } = splitJudgedNodes(surfaceNodeIds, manifestByNodeId)
  const findings: CoverageFinding[] = []

  for (const sequence of sequences) {
    const gapNodes = judged.filter((nodeId) => {
      const manifest = manifestByNodeId.get(nodeId)
      return manifest !== undefined && manifest.gaps.some((g) => g.sequence === sequence)
    })
    if (gapNodes.length === 0) continue
    findings.push({
      sequence,
      severity: gapNodes.length === judged.length ? 'no_coverage' : 'short',
      gapNodes,
      coveredNodes: judged.filter((n) => !gapNodes.includes(n)),
      judgedTotal: judged.length,
      totalSurfaceNodes: surfaceNodeIds.length,
      unjudgedNodes: unjudged,
    })
  }

  findings.sort((a, b) => {
    if (a.severity !== b.severity) return a.severity === 'no_coverage' ? -1 : 1
    return a.sequence.localeCompare(b.sequence)
  })
  return findings
}
