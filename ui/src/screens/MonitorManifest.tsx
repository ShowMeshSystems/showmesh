import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getAssetManifest, type NodeAssetManifest } from '../api'
import { Button, Panes, RuledStrip, Section, SelectableRow, StatusPair, Table, TableWrap } from '../kit'
import type { Tone } from '../kit'
import { formatBytes } from './showsModel'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../domain/session'
import { formatClock } from '../domain/time'
import { MonitorHead } from './Monitor'

type ManifestState =
  | { kind: 'loading' }
  | { kind: 'loaded'; nodes: NodeAssetManifest[]; receivedAt: number }
  | { kind: 'failed'; reason: string; nodes: NodeAssetManifest[]; receivedAt: number | null }

function useAssetManifest(): { state: ManifestState; refresh: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ManifestState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    getAssetManifest()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', nodes: response.nodes, receivedAt: Date.now() })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) => ({
          kind: 'failed',
          reason: describeApiError(err),
          nodes: prev.kind === 'loaded' || prev.kind === 'failed' ? prev.nodes : [],
          receivedAt: prev.kind === 'loaded' ? prev.receivedAt : prev.kind === 'failed' ? prev.receivedAt : null,
        }))
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  return { state, refresh: () => setAttempt((n) => n + 1) }
}

const STATE_TONE: Record<NodeAssetManifest['state'], Tone> = { ready: 'good', not_ready: 'warn', unknown: 'unknown' }
const STATE_LABEL: Record<NodeAssetManifest['state'], string> = { ready: 'Ready', not_ready: 'Not ready', unknown: 'Unknown' }

export function MonitorManifest() {
  const model = useModelContext()
  const { state, refresh } = useAssetManifest()
  const [selected, setSelected] = useState<string | null>(null)

  const nodes = state.kind === 'loading' ? [] : state.nodes
  const selectedManifest = nodes.find((manifest) => manifest.node === selected) ?? null

  return (
    <>
      <MonitorHead model={model} />

      <Section
        id="mo-manifest"
        title="Manifest"
        aside={
          <Button onClick={refresh} disabled={state.kind === 'loading'}>
            Refresh
          </Button>
        }
        detail="What each node should hold against what it reported holding, from GET /assets/manifest. Not part of the live model: read on demand."
      >
        {state.kind === 'failed' && (
          <RuledStrip
            absence={state.receivedAt === null ? 'failed' : 'stale'}
            label={state.receivedAt === null ? 'Read failed' : 'Stale'}
            fact={state.reason}
            detail={
              state.receivedAt === null
                ? 'No manifest read has ever succeeded on this device.'
                : `Showing the manifest last read at ${formatClock(new Date(state.receivedAt).toISOString()) ?? 'an unrecorded time'}.`
            }
          />
        )}

        {state.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for every node's asset manifest." />
        ) : nodes.length === 0 && state.kind === 'loaded' ? (
          <RuledStrip absence="empty" label="None" fact="No node is declared, so there is nothing to check." />
        ) : (
          <Panes
            inspectorOpen={selectedManifest !== null}
            onInspectorClose={() => setSelected(null)}
            inspectorLabelledBy={selected === null ? '' : `mo-manifest-inspect-${selected}`}
          >
            <div>
              <TableWrap label="Node asset manifest, scrollable">
                <Table minWidth={640}>
                  <thead>
                    <tr>
                      <th scope="col">Node</th>
                      <th scope="col">State</th>
                      <th scope="col">Missing</th>
                      <th scope="col">Gaps</th>
                      <th scope="col">Extra</th>
                      <th scope="col">Observed</th>
                    </tr>
                  </thead>
                  <tbody>
                    {nodes.map((manifest) => (
                      <ManifestTableRow
                        key={manifest.node}
                        manifest={manifest}
                        selected={manifest.node === selected}
                        onSelect={() => setSelected(manifest.node === selected ? null : manifest.node)}
                      />
                    ))}
                  </tbody>
                </Table>
              </TableWrap>
              <p className="sm-section__footnote">
                An asset gap is inferred from the show's own asset rows, not from a stored link. An extra asset is never
                an error and never a basis for deletion.
              </p>
            </div>
            <aside>
              {selectedManifest === null ? (
                <RuledStrip absence="empty" label="Nothing selected" fact="Select a node for its full manifest detail." />
              ) : (
                <ManifestDetail manifest={selectedManifest} />
              )}
            </aside>
          </Panes>
        )}
      </Section>
    </>
  )
}

function ManifestTableRow({
  manifest,
  selected,
  onSelect,
}: {
  manifest: NodeAssetManifest
  selected: boolean
  onSelect: () => void
}) {
  return (
    <SelectableRow selected={selected} onActivate={onSelect} ariaLabel={`View manifest for ${manifest.node}`}>
      <td>
        <strong>{manifest.node}</strong>
        {selected && <span className="sm-viewing">Viewing</span>}
      </td>
      <td>
        <StatusPair tone={STATE_TONE[manifest.state]} label={STATE_LABEL[manifest.state]} />
        {manifest.reason !== null && (
          <>
            <br />
            <span className="sm-small sm-faint">{manifest.reason}</span>
          </>
        )}
      </td>
      <td className="sm-data">{manifest.missing.length}</td>
      <td className="sm-data">{manifest.gaps.length}</td>
      <td className="sm-data">{manifest.extra.length}</td>
      <td className="sm-data">{manifest.observedAt === null ? 'never' : (formatClock(manifest.observedAt) ?? 'unrecorded')}</td>
    </SelectableRow>
  )
}

function EmptyList({ manifest, fact }: { manifest: NodeAssetManifest; fact: string }) {
  if (manifest.state === 'unknown') {
    return (
      <RuledStrip
        absence="unobserved"
        label="No verdict"
        fact="This node has no asset evidence to judge."
        detail={manifest.reason ?? 'Nothing has been observed, so an empty list here would not mean an empty result.'}
      />
    )
  }
  return <RuledStrip absence="empty" label="None" fact={fact} />
}

function ManifestDetail({ manifest }: { manifest: NodeAssetManifest }) {
  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow">Node</p>
      <h2 className="sm-inspector__title" id={`mo-manifest-inspect-${manifest.node}`}>
        <Link to={`/monitor/fleet/node/${manifest.node}`}>{manifest.node}</Link>
      </h2>
      <p className="sm-small sm-muted">
        <StatusPair tone={STATE_TONE[manifest.state]} label={STATE_LABEL[manifest.state]} />
        {manifest.reason !== null && ` · ${manifest.reason}`}
      </p>

      <section className="sm-inspector__group">
        <h3 className="sm-subsection__title">Missing</h3>
        {manifest.missing.length === 0 ? (
          <EmptyList manifest={manifest} fact="Nothing this node should hold is missing." />
        ) : (
          manifest.missing.map((asset) => (
            <div key={asset.assetId} className="sm-inspector__row">
              <span className="sm-inspector__label sm-data">sequence {asset.sequence}</span>
              <div>
                <p className="sm-inspector__value sm-data">{asset.filename}</p>
                <p className="sm-inspector__detail" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)} · {asset.contentHash}</p>
              </div>
            </div>
          ))
        )}
      </section>

      <section className="sm-inspector__group">
        <h3 className="sm-subsection__title">Gaps</h3>
        {manifest.gaps.length === 0 ? (
          <EmptyList manifest={manifest} fact="No sequence the active show holds an asset for is uncovered." />
        ) : (
          manifest.gaps.map((gap) => (
            <div key={gap.sequence} className="sm-inspector__row">
              <span className="sm-inspector__label sm-data">sequence {gap.sequence}</span>
              <div>
                <p className="sm-inspector__value sm-data">surfaces {gap.surfaces.join(', ')}</p>
                <p className="sm-inspector__detail">
                  Inferred from the show's own asset rows, not from a stored surface-to-sequence link.
                </p>
              </div>
            </div>
          ))
        )}
      </section>

      <section className="sm-inspector__group">
        <h3 className="sm-subsection__title">Extra</h3>
        {manifest.extra.length === 0 ? (
          <EmptyList manifest={manifest} fact="This node holds nothing it was not expected to." />
        ) : (
          manifest.extra.map((asset) => (
            <div key={asset.contentHash} className="sm-inspector__row">
              <span className="sm-inspector__label sm-data">{asset.filename}</span>
              <div>
                <p className="sm-inspector__value sm-data" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</p>
                <p className="sm-inspector__detail">Never an error and never a basis for deletion. No delete control is offered here.</p>
              </div>
            </div>
          ))
        )}
      </section>
    </div>
  )
}
