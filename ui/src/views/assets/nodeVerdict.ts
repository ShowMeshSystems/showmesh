import type { Asset, NodeAssetManifest } from '../../api'

/**
 * Per-target-row match verdict against `/assets/manifest` evidence
 * (NodeAssetManifest.missing / .extra), never fabricated: the manifest
 * only ever states exceptions (an asset it expected but does not hold,
 * and bytes it holds but did not expect), never a positive confirmation.
 * "matches" is therefore the ABSENCE of a missing-entry for this asset
 * id on a node whose manifest evidence we actually have -- and "unknown"
 * is reserved for a node this coordinator has no manifest evidence for
 * at all, per the four absences (never a soft "matches").
 */
export type NodeVerdict =
  | { kind: 'matches' }
  | { kind: 'hash_mismatch'; extraFilename: string; extraSizeBytes: number }
  | { kind: 'not_synced' }
  | { kind: 'unknown'; reason: string | null }

export function manifestByNode(nodes: NodeAssetManifest[]): Map<string, NodeAssetManifest> {
  return new Map(nodes.map((n) => [n.node, n]))
}

export function verdictForNodeTarget(row: Asset, byNode: Map<string, NodeAssetManifest>): NodeVerdict {
  const manifest = byNode.get(row.target)
  if (manifest === undefined) return { kind: 'unknown', reason: 'This coordinator has never reported this node’s asset inventory.' }
  if (manifest.state === 'unknown') return { kind: 'unknown', reason: manifest.reason }
  const missingEntry = manifest.missing.find((m) => m.assetId === row.id)
  if (missingEntry === undefined) return { kind: 'matches' }
  const extraEntry = manifest.extra.find((e) => e.filename === row.runtimeFilename)
  if (extraEntry !== undefined) return { kind: 'hash_mismatch', extraFilename: extraEntry.filename, extraSizeBytes: extraEntry.sizeBytes }
  return { kind: 'not_synced' }
}

/**
 * A show-wide asset names no single target (ADR-028: `target` is empty
 * when `targetKind` is "show"), so there is no per-node link to walk the
 * way `verdictForNodeTarget` does. The manifest still lets this asset's
 * id turn up in some node's `missing` list when that node needed it and
 * does not have it -- so this reports every node with EVIDENCE of a gap
 * for this asset id, and states plainly that a node reporting no gap is
 * not proof it holds the file, only that nothing has flagged it missing.
 */
export function showWideGapNodes(assetId: string, runtimeFilename: string, nodes: NodeAssetManifest[]): Array<{ node: string; verdict: NodeVerdict }> {
  const hits: Array<{ node: string; verdict: NodeVerdict }> = []
  for (const manifest of nodes) {
    const missingEntry = manifest.missing.find((m) => m.assetId === assetId)
    if (missingEntry === undefined) continue
    const extraEntry = manifest.extra.find((e) => e.filename === runtimeFilename)
    hits.push({
      node: manifest.node,
      verdict: extraEntry !== undefined
        ? { kind: 'hash_mismatch', extraFilename: extraEntry.filename, extraSizeBytes: extraEntry.sizeBytes }
        : { kind: 'not_synced' },
    })
  }
  return hits
}
