import { Fragment } from 'react'
import { Link } from 'react-router-dom'
import type { Asset, NodeAssetManifest } from '../../api'
import { showWorkspacePath } from '../../components/showWorkspacePaths'
import { formatBytes, groupAssetsBySequence, historyForIdentity } from './assetGrouping'
import { manifestByNode, showWideGapNodes, verdictForNodeTarget, type NodeVerdict } from './nodeVerdict'

function VerdictLabel({ verdict }: { verdict: NodeVerdict }) {
  if (verdict.kind === 'matches') return <span className="asset-verdict asset-verdict--good">✓ Matches</span>
  if (verdict.kind === 'hash_mismatch') return <span className="asset-verdict asset-verdict--warn">⚠ Hash mismatch</span>
  if (verdict.kind === 'not_synced') return <span className="asset-verdict asset-verdict--faint">Not synced</span>
  return <span className="asset-verdict asset-verdict--unknown">Unknown</span>
}

function rowClass(verdict: NodeVerdict): string {
  if (verdict.kind === 'hash_mismatch') return 'asset-row--warn'
  if (verdict.kind === 'not_synced') return 'asset-row--unobserved'
  return ''
}

/**
 * The grouped-by-sequence asset table (Show Assets.dc.html). Shared by the
 * show-scoped workspace tab and the cross-show `/assets` rail destination
 * -- the owner's ruling for the cross-show table is "the same table with a
 * Show column added", so `showColumn` is the only structural difference.
 */
export function AssetsSequenceTable({
  assets,
  manifestNodes,
  showColumn = false,
  selectedRowId,
  onSelectRow,
}: {
  assets: Asset[]
  manifestNodes: NodeAssetManifest[]
  showColumn?: boolean
  selectedRowId?: string | null
  onSelectRow?: (row: Asset) => void
}) {
  const groups = groupAssetsBySequence(assets)
  const byNode = manifestByNode(manifestNodes)

  if (groups.length === 0) {
    return <p className="t-small text-muted">No assets match this filter.</p>
  }

  return (
    <div className="card table-wrap">
      <table className="table table--full">
        <thead>
          <tr>
            {showColumn && <th>Show</th>}
            <th>Target</th>
            <th>Hash</th>
            <th>On node</th>
            <th style={{ textAlign: 'right' }}>Size</th>
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => {
            const sharedFilenameCount = new Set(assets.filter((a) => a.show === group.show && a.sequence === group.sequence).map((a) => a.target)).size
            return (
              <Fragment key={`${group.show}:${group.sequence}`}>
                <tr className="asset-group-row">
                  <td colSpan={showColumn ? 5 : 4}>
                    <div className="asset-group-row__title">
                      <span className="asset-group-row__name">{group.sequence}</span>
                      <span className="asset-group-row__kind">{group.mediaType === 'fseq' ? 'FSEQ' : group.mediaType === 'audio' ? 'Audio' : 'Media'}</span>
                      <span className="asset-group-row__detail">
                        {group.filename}
                        {sharedFilenameCount > 1 ? ` · ${sharedFilenameCount} targets share this filename` : ' · show-wide'}
                      </span>
                      {showColumn && (
                        <Link className="entity-link t-small" to={showWorkspacePath(group.show)}>
                          {group.show}
                        </Link>
                      )}
                    </div>
                  </td>
                </tr>
                {group.rows.map((row) => {
                  if (row.targetKind === 'show') {
                    const gaps = showWideGapNodes(row.id, row.runtimeFilename, manifestNodes)
                    return (
                      <tr
                        key={row.id}
                        data-clickable={onSelectRow !== undefined ? '' : undefined}
                        aria-current={selectedRowId === row.id ? 'true' : undefined}
                        className={selectedRowId === row.id ? 'asset-row--current' : gaps.length > 0 ? 'asset-row--warn' : ''}
                        onClick={onSelectRow !== undefined ? () => onSelectRow(row) : undefined}
                      >
                        {showColumn && <td>{row.show}</td>}
                        <td>
                          {onSelectRow !== undefined ? (
                            <button type="button" className="entity-link asset-target-cell__name" onClick={() => onSelectRow(row)}>
                              Show-wide
                            </button>
                          ) : (
                            <span className="asset-target-cell__name">Show-wide</span>
                          )}
                          {gaps.length > 0 && (
                            <span className="asset-target-cell__note asset-target-cell__note--warn">
                              Gap reported on {gaps.map((g) => g.node).join(', ')}
                            </span>
                          )}
                        </td>
                        <td className="asset-hash">{row.contentHash.replace('sha256:', '').slice(0, 4)}…{row.contentHash.slice(-2)}</td>
                        <td>
                          {gaps.length === 0 || gaps[0] === undefined ? (
                            <span className="asset-verdict asset-verdict--faint">No gaps reported</span>
                          ) : (
                            <VerdictLabel verdict={gaps[0].verdict} />
                          )}
                        </td>
                        <td style={{ textAlign: 'right' }} className="asset-hash">
                          {formatBytes(row.sizeBytes)}
                        </td>
                      </tr>
                    )
                  }
                  const verdict = verdictForNodeTarget(row, byNode)
                  const history = historyForIdentity(assets, row)
                  const wasRolledBack = history.some(
                    (h, i) => h.current && i + 1 < history.length && history.slice(i + 1).some((older) => !older.current && older.contentHash === h.contentHash),
                  )
                  return (
                    <tr
                      key={row.id}
                      data-clickable={onSelectRow !== undefined ? '' : undefined}
                      aria-current={selectedRowId === row.id ? 'true' : undefined}
                      className={[selectedRowId === row.id ? 'asset-row--current' : rowClass(verdict)].filter(Boolean).join(' ')}
                      onClick={onSelectRow !== undefined ? () => onSelectRow(row) : undefined}
                    >
                      {showColumn && <td>{row.show}</td>}
                      <td>
                        {onSelectRow !== undefined ? (
                          <button type="button" className="entity-link asset-target-cell__name" onClick={() => onSelectRow(row)}>
                            {row.target}
                          </button>
                        ) : (
                          <span className="asset-target-cell__name">{row.target}</span>
                        )}
                        {verdict.kind === 'hash_mismatch' && <span className="asset-target-cell__note asset-target-cell__note--warn">Node holds older bytes</span>}
                        {verdict.kind === 'not_synced' && <span className="asset-target-cell__note asset-target-cell__note--faint">Never delivered</span>}
                        {wasRolledBack && <span className="asset-group-row__rollback">Rolled back</span>}
                      </td>
                      <td className="asset-hash">
                        {row.contentHash.replace('sha256:', '').slice(0, 4)}…{row.contentHash.slice(-2)}
                      </td>
                      <td>
                        <VerdictLabel verdict={verdict} />
                      </td>
                      <td style={{ textAlign: 'right' }} className="asset-hash">
                        {formatBytes(row.sizeBytes)}
                      </td>
                    </tr>
                  )
                })}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
