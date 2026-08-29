import type { Asset } from '../../api'

/**
 * One xLights sequence produces a different file per target, and every
 * target's artifact shares the same runtime filename (ADR-028 decision 1).
 * Filename therefore belongs to the GROUP (show + sequence), never to a
 * row: the row's identity is show + sequence + target + content hash.
 */
export interface AssetSequenceGroup {
  show: string
  sequence: string
  mediaType: Asset['mediaType']
  /** The filename every current target in this group shares. */
  filename: string
  /** Current rows only, one per target (or one show-wide row). */
  rows: Asset[]
}

export function groupAssetsBySequence(assets: Asset[]): AssetSequenceGroup[] {
  const currentByKey = new Map<string, { show: string; sequence: string; rows: Asset[] }>()
  for (const asset of assets) {
    if (!asset.current) continue
    const key = `${asset.show} ${asset.sequence}`
    const entry = currentByKey.get(key)
    if (entry) entry.rows.push(asset)
    else currentByKey.set(key, { show: asset.show, sequence: asset.sequence, rows: [asset] })
  }
  const groups: AssetSequenceGroup[] = []
  for (const { show, sequence, rows } of currentByKey.values()) {
    rows.sort((a, b) => (a.targetKind === b.targetKind ? a.target.localeCompare(b.target) : a.targetKind.localeCompare(b.targetKind)))
    const first = rows[0]
    if (first === undefined) continue
    groups.push({
      show,
      sequence,
      mediaType: first.mediaType,
      filename: first.runtimeFilename,
      rows,
    })
  }
  groups.sort((a, b) => (a.show === b.show ? a.sequence.localeCompare(b.sequence) : a.show.localeCompare(b.show)))
  return groups
}

/**
 * Every asset that ever superseded, or was superseded by, the given
 * current row for the same (show, sequence, targetKind, target) identity
 * -- the events that make up its rollback history, newest first.
 */
export function historyForIdentity(assets: Asset[], row: Asset): Asset[] {
  return assets
    .filter(
      (a) =>
        a.show === row.show &&
        a.sequence === row.sequence &&
        a.targetKind === row.targetKind &&
        a.target === row.target,
    )
    .sort((a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt))
}

/**
 * A superseded asset in this identity's history whose content hash
 * matches the given hash: uploading it again would be a rollback
 * (ADR-028 decision 10). Stated at authoring time, before the bytes are
 * ever sent.
 */
export function findRollbackMatch(history: Asset[], contentHash: string): Asset | null {
  return history.find((a) => !a.current && a.contentHash === contentHash) ?? null
}

export async function sha256ContentHash(file: File): Promise<string | null> {
  if (typeof crypto === 'undefined' || typeof crypto.subtle === 'undefined') return null
  try {
    const buffer = await file.arrayBuffer()
    const digest = await crypto.subtle.digest('SHA-256', buffer)
    const hex = Array.from(new Uint8Array(digest))
      .map((b) => b.toString(16).padStart(2, '0'))
      .join('')
    return `sha256:${hex}`
  } catch {
    return null
  }
}

export function shortHash(contentHash: string): string {
  const hex = contentHash.startsWith('sha256:') ? contentHash.slice('sha256:'.length) : contentHash
  if (hex.length <= 6) return hex
  return `${hex.slice(0, 4)}…${hex.slice(-2)}`
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const kib = bytes / 1024
  if (kib < 1024) return `${kib.toFixed(1)} KiB`
  const mib = kib / 1024
  if (mib < 1024) return `${mib.toFixed(1)} MB`
  return `${(mib / 1024).toFixed(2)} GB`
}
