import { Fragment } from 'react'
import { Link } from 'react-router-dom'
import type { Asset, NodeAssetManifest } from '../../api'
import { formatAbsolute } from '../../app/time'
import { PlannedFeature } from '../../components/SharedLayouts'
import { manifestByNode } from './nodeVerdict'
import { buildCoverageFindings, type CoverageFinding } from './sequenceCoverage'
import { useShowSurfaceNodeIds } from './showSurfaceNodes'

// Shows / Assets workspace tab, "Sequence coverage" (Show Assets.dc.html,
// revision-1: the owner's answer to the asset manifest's `gaps` and
// `extra`). A different question from the AssetStatsStrip counters above
// it: those count asset ROWS, this asks whether any node can actually
// render a sequence at all. The owner's rule, restated in code: a node
// that could not be judged is never rendered as a passing node -- see
// sequenceCoverage.ts's own split of judged vs unjudged nodes.
function NodeList({ nodes }: { nodes: string[] }) {
  return (
    <>
      {nodes.map((n, i) => (
        <Fragment key={n}>
          {i > 0 && ', '}
          <span className="t-data">{n}</span>
        </Fragment>
      ))}
    </>
  )
}

function CoverageFindingRow({ finding, onUploadFor }: { finding: CoverageFinding; onUploadFor: (sequence: string) => void }) {
  const label = finding.severity === 'no_coverage' ? 'No coverage' : 'One node short'
  return (
    <div className={`asset-coverage__row asset-coverage__row--${finding.severity}`}>
      <span className="asset-coverage__row-label">{label}</span>
      <div className="asset-coverage__row-body">
        {finding.severity === 'no_coverage' ? (
          <>
            <p className="t-body">
              <span className="t-data">{finding.sequence}</span> has a current asset in this show and{' '}
              <strong>no node holds anything for it</strong>: not on <NodeList nodes={finding.gapNodes} />.
            </p>
            <p className="t-small text-muted">
              {`${finding.judgedTotal} of ${finding.judgedTotal} node${finding.judgedTotal === 1 ? '' : 's'} carrying a surface in this show that this coordinator could check reported no coverage for it. Nothing will render it tonight.`}
            </p>
          </>
        ) : (
          <>
            <p className="t-body">
              <span className="t-data">{finding.sequence}</span> is covered on <NodeList nodes={finding.coveredNodes} /> and missing on{' '}
              <NodeList nodes={finding.gapNodes} />{' '}
              <span className="text-muted">
                ({finding.gapNodes.length} node{finding.gapNodes.length === 1 ? '' : 's'})
              </span>
              .
            </p>
            <p className="t-small text-muted">
              A delivery problem on one node, not an authoring hole. The show has the asset.
              {finding.gapNodes.length === 1 && finding.gapNodes[0] !== undefined && (
                <>
                  {' '}
                  <Link to={`/monitor/fleet/node/${encodeURIComponent(finding.gapNodes[0])}`}>Open {finding.gapNodes[0]}</Link>.
                </>
              )}
            </p>
          </>
        )}

        {finding.unjudgedNodes.map((u) => (
          <p key={u.node} className="t-small text-faint">
            <span className="t-data">{u.node}</span> carries a surface in this show and{' '}
            {u.observedAt !== null ? (
              <>
                has not reported since <span className="t-data">{formatAbsolute(u.observedAt)}</span>
              </>
            ) : (
              'has never reported'
            )}
            {`, so it was not judged either way. ${finding.judgedTotal} of ${finding.totalSurfaceNodes}, not ${finding.totalSurfaceNodes} of ${finding.totalSurfaceNodes}.`}
          </p>
        ))}

        <div className="asset-coverage__actions">
          <button type="button" className="btn btn--secondary btn--compact" onClick={() => onUploadFor(finding.sequence)}>
            Upload for {finding.sequence}
          </button>
        </div>
      </div>
    </div>
  )
}

export function SequenceCoverageSection({
  showId,
  assets,
  manifestNodes,
  allowed,
  onUploadFor,
}: {
  showId: string
  assets: Asset[]
  manifestNodes: NodeAssetManifest[]
  allowed: boolean
  onUploadFor: (sequence: string) => void
}) {
  const surfaceNodes = useShowSurfaceNodeIds(showId, allowed)

  return (
    <section className="asset-coverage" aria-labelledby="asset-coverage-title">
      {(surfaceNodes.kind === 'idle' || surfaceNodes.kind === 'loading') && (
        <>
          <h3 id="asset-coverage-title" className="asset-coverage__title">Sequence coverage</h3>
          <p className="t-small text-muted" role="status" aria-busy="true">
            Loading which nodes carry a surface in this show…
          </p>
        </>
      )}

      {surfaceNodes.kind === 'error' && (
        <>
          <h3 id="asset-coverage-title" className="asset-coverage__title">Sequence coverage</h3>
          <p className="panel panel--error" role="alert">
            {surfaceNodes.message}
          </p>
        </>
      )}

      {surfaceNodes.kind === 'loaded' && surfaceNodes.nodeIds.length === 0 && (
        <>
          <h3 id="asset-coverage-title" className="asset-coverage__title">Sequence coverage</h3>
          <p className="t-small text-muted">No node carries a surface in this show yet, so there is nothing to check coverage against.</p>
        </>
      )}

      {surfaceNodes.kind === 'loaded' && surfaceNodes.nodeIds.length > 0 && (
        <SequenceCoverageBody
          surfaceNodeIds={surfaceNodes.nodeIds}
          assets={assets}
          manifestNodes={manifestNodes}
          onUploadFor={onUploadFor}
        />
      )}
    </section>
  )
}

function SequenceCoverageBody({
  surfaceNodeIds,
  assets,
  manifestNodes,
  onUploadFor,
}: {
  surfaceNodeIds: string[]
  assets: Asset[]
  manifestNodes: NodeAssetManifest[]
  onUploadFor: (sequence: string) => void
}) {
  const sequences = Array.from(new Set(assets.filter((a) => a.current).map((a) => a.sequence))).sort()
  const byNode = manifestByNode(manifestNodes)
  const findings = buildCoverageFindings(sequences, surfaceNodeIds, byNode)

  return (
    <>
      <div className="asset-coverage__header">
        <h3 id="asset-coverage-title" className="asset-coverage__title">
          Sequence coverage
          {findings.length > 0 && (
            <span className="asset-coverage__count">
              {' '}
              · {findings.length} finding{findings.length === 1 ? '' : 's'}
            </span>
          )}
        </h3>
        <span className="t-small text-faint">
          Across the {surfaceNodeIds.length} node{surfaceNodeIds.length === 1 ? '' : 's'} carrying a surface in this show
        </span>
      </div>
      <p className="asset-coverage__lede">
        A different question from the counters above. Those count asset rows; this asks whether any node can actually render a sequence at all.
      </p>

      {findings.length === 0 ? (
        <p className="t-small text-muted">No judged node reports a sequence with no coverage at all.</p>
      ) : (
        <>
          {findings.map((finding) => (
            <CoverageFindingRow key={finding.sequence} finding={finding} onUploadFor={onUploadFor} />
          ))}
          <PlannedFeature
            title="Run sync from here"
            why="There is no manual asset-sync trigger in this API. Sync runs on upload and on assets.settings' own timer (syncIntervalSeconds), never as an operator-invoked command, so there is nothing this button could call."
            preview={
              <button type="button" className="btn btn--quiet" tabIndex={-1}>
                Run sync
              </button>
            }
          />
        </>
      )}

      <p className="asset-coverage__footnote">
        Coverage is only listed for a node whose manifest says it is not ready. A node that has never reported one is not counted as covered or
        uncovered.
      </p>
    </>
  )
}
