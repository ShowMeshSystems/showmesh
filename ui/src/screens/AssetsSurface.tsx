import { useEffect, useState } from 'react'
import {
  assetContentUrl,
  getAssetContent,
  getAssetManifest,
  listAssets,
  listConfigObjects,
  resyncNodeAssets,
  uploadAsset,
  type Asset,
  type AssetLastFetch,
  type AssetSyncVerdict,
  type AssetVerdictSource,
  type ConfigObjectSummary,
  type NodeAssetManifest,
  type UploadProgress,
} from '../api'
import { Button, ButtonRow, Field, Input, Notice, Panes, RuledStrip, Section, Segmented, Select, SelectableRow, StatusPair, Table, TableWrap, type Tone } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatDateClock } from '../domain/time'
import { assetHistory, assetIdentityKey, formatBytes, hashLabel, renditionSummary, targetLabel } from './showsModel'

/**
 * A rehashed identity, used to decide when re-uploading a file would
 * perform ADR-028 decision 10's rollback. Reads via `FileReader` rather
 * than `File.prototype.arrayBuffer`, not every runtime this renders in
 * implements the latter on a `File`.
 */
async function fileArrayBuffer(file: File): Promise<ArrayBuffer> {
  return await new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => resolve(reader.result as ArrayBuffer)
    reader.onerror = () => reject(reader.error ?? new Error('Could not read the chosen file.'))
    reader.readAsArrayBuffer(file)
  })
}

async function sha256Hex(file: File): Promise<string> {
  const digest = await crypto.subtle.digest('SHA-256', await fileArrayBuffer(file))
  const hex = Array.from(new Uint8Array(digest))
    .map((b) => b.toString(16).padStart(2, '0'))
    .join('')
  return `sha256:${hex}`
}

/** Either one show's assets or every show's assets, shared by the show tab and the /assets library. */
export type AssetScope = { kind: 'show'; showId: string } | { kind: 'all' }

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; assets: Asset[] }
  | { kind: 'failed'; reason: string }

function scopeFilter(scope: AssetScope): { show?: string } | undefined {
  return scope.kind === 'show' ? { show: scope.showId } : undefined
}

function useScopedAssets(scope: AssetScope): { state: ListState; reload: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })
  const scopeKey = scope.kind === 'show' ? scope.showId : ''

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    listAssets(scopeFilter(scope))
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', assets: response.assets })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [scope.kind, scopeKey, attempt])

  return { state, reload: () => setAttempt((n) => n + 1) }
}

/** Every show the library's filter and upload form can offer, fetched only when the scope spans every show. */
function useShowOptions(enabled: boolean): ConfigObjectSummary[] {
  const [shows, setShows] = useState<ConfigObjectSummary[]>([])

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    listConfigObjects('show').then((response) => {
      if (!cancelled) setShows(response.objects)
    })
    return () => {
      cancelled = true
    }
  }, [enabled])

  return shows
}

type ManifestListState =
  | { kind: 'loading'; nodes: NodeAssetManifest[] }
  | { kind: 'loaded'; nodes: NodeAssetManifest[] }
  | { kind: 'failed'; reason: string; nodes: NodeAssetManifest[] }

/**
 * GET /assets/manifest, fleet-wide (it carries no per-show filter): the
 * readiness evidence behind every row's badge and the inspector's Nodes
 * section. A read failure degrades every badge to Unknown rather than
 * blocking the file list itself, which GET /assets already answered.
 */
function useAssetManifestList(): { state: ManifestListState; reload: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ManifestListState>({ kind: 'loading', nodes: [] })

  useEffect(() => {
    let cancelled = false
    getAssetManifest()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', nodes: response.nodes })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState((prev) => ({ kind: 'failed', reason: describeApiError(err), nodes: prev.nodes }))
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  return { state, reload: () => setAttempt((n) => n + 1) }
}

const MEDIA_FILTERS: readonly { value: 'all' | Asset['mediaType']; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'fseq', label: 'FSEQ' },
  { value: 'audio', label: 'Audio' },
  { value: 'media', label: 'Media' },
]

const MEDIA_CHIP: Record<Asset['mediaType'], string> = { fseq: 'FSEQ', audio: 'Audio', media: 'Media' }

/** One node's readiness for one file, folding the manifest's own verdict (when fresh evidence exists) onto a directly-known expectation (a node-targeted or show-wide row needs no fresh report to be "expected"). */
type NodeEntry = {
  nodeId: string
  label: string | null
  source: AssetVerdictSource
  tone: Tone
  stateLabel: string
  lastFetch: AssetLastFetch | null
  observedAt: string | null
  nodeReason: string | null
}

/** A verdict's own state (plus lastFetch for "absent") to a tone and word, per the badge rule: unknown never renders green or red. */
function verdictTone(verdict: AssetSyncVerdict): { tone: Tone; label: string } {
  if (verdict.state === 'held') return { tone: 'good', label: 'Held' }
  if (verdict.state === 'superseded') return { tone: 'warn', label: 'Behind' }
  if (verdict.lastFetch?.state === 'in_flight') return { tone: 'warn', label: 'Sending' }
  if (verdict.lastFetch?.state === 'failed') return { tone: 'bad', label: 'Failed' }
  return { tone: 'bad', label: 'Missing' }
}

const NODE_SOURCE: AssetVerdictSource = { kind: 'node', registeredTarget: '', referencedBy: [] }
const SHOW_SOURCE: AssetVerdictSource = { kind: 'show', registeredTarget: '', referencedBy: [] }

/**
 * Every node expected to hold `asset`, or that a fresh verdict names for
 * it: a node-targeted or show-wide row is expected directly from the
 * asset's own fields, evidence or not, and renders Unknown rather than
 * being silently dropped when that node has no fresh report; a borrowed
 * (cue_copy/bed_copy) copy is knowable only through another node's own
 * fresh verdict naming this exact assetId, so it appears only when one
 * does.
 */
function nodeEntriesForAsset(
  asset: Asset,
  declaredNodes: readonly { nodeId: string; label: string | null }[],
  manifestByNode: Map<string, NodeAssetManifest>,
  verdictIndex: Map<string, Map<string, AssetSyncVerdict>>,
): NodeEntry[] {
  const nodeIds = new Set<string>()
  if (asset.targetKind === 'node') {
    nodeIds.add(asset.target)
  } else {
    for (const n of declaredNodes) nodeIds.add(n.nodeId)
  }
  for (const [nodeId, byAsset] of verdictIndex) {
    if (byAsset.has(asset.id)) nodeIds.add(nodeId)
  }

  const impliedSource: AssetVerdictSource = asset.targetKind === 'node' ? { ...NODE_SOURCE, registeredTarget: asset.target } : SHOW_SOURCE

  return Array.from(nodeIds)
    .sort((a, b) => a.localeCompare(b))
    .map((nodeId) => {
      const manifest = manifestByNode.get(nodeId) ?? null
      const verdict = verdictIndex.get(nodeId)?.get(asset.id) ?? null
      const label = declaredNodes.find((n) => n.nodeId === nodeId)?.label ?? null
      if (verdict !== null) {
        const { tone, label: stateLabel } = verdictTone(verdict)
        return {
          nodeId, label, source: verdict.source, tone, stateLabel,
          lastFetch: verdict.lastFetch ?? null, observedAt: manifest?.observedAt ?? null, nodeReason: manifest?.reason ?? null,
        }
      }
      return {
        nodeId, label, source: impliedSource, tone: 'unknown' as Tone, stateLabel: 'Unknown',
        lastFetch: null, observedAt: manifest?.observedAt ?? null, nodeReason: manifest?.reason ?? 'This node has not reported.',
      }
    })
}

const TONE_RANK: Record<Tone, number> = { bad: 3, warn: 2, good: 1, unknown: 0, pending: 0 }
const SUMMARY_LABEL: Record<Tone, string> = { good: 'Ready', warn: 'Behind', bad: 'Missing', unknown: 'Unknown', pending: 'Unknown' }

/** The row's own compact badge: the worst of its nodes, grey only when every node is unknown. */
function summarizeEntries(entries: NodeEntry[]): { tone: Tone; label: string } {
  if (entries.length === 0 || entries.every((e) => e.tone === 'unknown')) return { tone: 'unknown', label: 'Unknown' }
  const worst = entries.reduce((acc, e) => (TONE_RANK[e.tone] > TONE_RANK[acc.tone] ? e : acc))
  return { tone: worst.tone, label: SUMMARY_LABEL[worst.tone] }
}

/** The plain-words "why" line for one node entry's source. */
function sourceReason(source: AssetVerdictSource): string {
  const referencedBy = source.referencedBy.join(', ')
  switch (source.kind) {
    case 'node':
      return 'Uploaded for this node.'
    case 'show':
      return 'Uploaded for the whole show.'
    case 'cue_copy':
      return `Copied from ${source.registeredTarget || 'another node'} because Cue ${referencedBy || 'an unnamed cue'} plays on this node.`
    case 'bed_copy':
      return `Copied from ${source.registeredTarget || 'another node'} because night bed ${referencedBy || 'an unnamed session'} plays on this node.`
    default:
      return 'Expected for an unrecorded reason.'
  }
}

/** The last-transfer line, or null when this process has nothing on record: never rendered as a failure in that case. */
function lastFetchLine(lastFetch: AssetLastFetch | null): string | null {
  if (lastFetch === null) return null
  if (lastFetch.state === 'in_flight') {
    const since = lastFetch.dispatchedAt !== null ? formatDateClock(lastFetch.dispatchedAt) ?? 'an unrecorded time' : null
    return since !== null ? `Sending since ${since} (since the coordinator started).` : 'Sending; the dispatch time is not recorded.'
  }
  const at = lastFetch.failedAt !== null ? formatDateClock(lastFetch.failedAt) ?? 'an unrecorded time' : 'an unrecorded time'
  return `Failed at ${at}: ${lastFetch.failureReason ?? 'no reason recorded'} (since the coordinator started).`
}

export function AssetsSurface({ scope }: { scope: AssetScope }) {
  const model = useModelContext()
  const { state, reload } = useScopedAssets(scope)
  const manifest = useAssetManifestList()
  const showOptions = useShowOptions(scope.kind === 'all')
  const [selectedAssetId, setSelectedAssetId] = useState<string | null>(null)
  const [uploading, setUploading] = useState(false)
  const [filterText, setFilterText] = useState('')
  const [filterMedia, setFilterMedia] = useState<'all' | Asset['mediaType']>('all')
  const [filterShow, setFilterShow] = useState('all')

  const sectionTitle = scope.kind === 'show' ? 'Assets in this show' : 'All assets'
  const tableLabel = scope.kind === 'show' ? "This show's current assets, one row per file, scrollable" : "Every show's current assets, one row per file, scrollable"
  const columnCount = 7

  if (state.kind === 'loading') {
    return (
      <Section id="asset-list" title={sectionTitle}>
        <RuledStrip absence="loading" label="Reading" fact={scope.kind === 'show' ? "Asking the coordinator for this show's assets." : 'Asking the coordinator for every show’s assets.'} />
      </Section>
    )
  }

  if (state.kind === 'failed') {
    return (
      <Section id="asset-list" title={sectionTitle}>
        <RuledStrip
          absence="failed"
          label="Read failed"
          fact={state.reason}
          detail={
            <button type="button" className="sm-linkbutton" onClick={reload}>
              Try again
            </button>
          }
        />
      </Section>
    )
  }

  const currentAssets = state.assets.filter((a) => a.current)
  const scopedAssets = scope.kind === 'all' && filterShow !== 'all' ? currentAssets.filter((a) => a.show === filterShow) : currentAssets

  const rows = scopedAssets
    .filter((a) => filterMedia === 'all' || a.mediaType === filterMedia)
    .filter((a) => {
      if (filterText === '') return true
      const needle = filterText.toLowerCase()
      return a.sequence.toLowerCase().includes(needle) || a.runtimeFilename.toLowerCase().includes(needle) || targetLabel(a).toLowerCase().includes(needle)
    })
    .sort((a, b) => a.show.localeCompare(b.show) || a.sequence.localeCompare(b.sequence) || targetLabel(a).localeCompare(targetLabel(b)))

  const manifestNodes = manifest.state.nodes
  const manifestByNode = new Map(manifestNodes.map((m) => [m.node, m]))
  const verdictIndex = new Map<string, Map<string, AssetSyncVerdict>>()
  for (const m of manifestNodes) {
    if (m.verdicts === undefined) continue
    verdictIndex.set(m.node, new Map(m.verdicts.map((v) => [v.assetId, v])))
  }

  const selectedAsset = selectedAssetId === null ? null : state.assets.find((a) => a.id === selectedAssetId) ?? null
  const knownSequences = Array.from(new Set(state.assets.map((a) => a.sequence))).sort()
  const knownShows = Array.from(new Set(state.assets.map((a) => a.show))).sort()
  const closeInspector = () => {
    setUploading(false)
    setSelectedAssetId(null)
  }

  return (
    <div className="sm-assets-surface">
      <Panes
        inspectorOpen={uploading || selectedAsset !== null}
        onInspectorClose={closeInspector}
        inspectorLabelledBy={uploading ? 'assets-upload-heading' : 'assets-detail-heading'}
        inspectorWidth="wide"
      >
        <div>
          <Section
            id="asset-list"
            title={sectionTitle}
            aside={
              <Button
                variant="primary"
                onClick={() => {
                  setUploading(true)
                  setSelectedAssetId(null)
                }}
              >
                Upload
              </Button>
            }
          >
            {manifest.state.kind === 'failed' && (
              <RuledStrip absence="stale" label="Readiness unread" fact={manifest.state.reason} detail="Status shows as Unknown below until this succeeds." />
            )}

            <div className="sm-assets-filters sm-stack-3">
              <Input aria-label="Filter assets" placeholder="Filter assets…" value={filterText} onChange={(e) => setFilterText(e.target.value)} />
              {scope.kind === 'all' && (
                <div className="sm-assets-filters__show">
                  <Field label="Filter by show">
                    {(props) => (
                      <Select {...props} value={filterShow} onChange={(e) => setFilterShow(e.target.value)}>
                        <option value="all">All shows</option>
                        {knownShows.map((show) => (
                          <option key={show} value={show}>
                            {show}
                          </option>
                        ))}
                      </Select>
                    )}
                  </Field>
                </div>
              )}
              <Segmented label="Filter by media type" value={filterMedia} options={MEDIA_FILTERS} onChange={setFilterMedia} />
            </div>

            <div className="sm-assets-table sm-stack-3">
              <TableWrap label={tableLabel}>
                <Table minWidth={scope.kind === 'all' ? 720 : 680}>
                  <thead>
                    <tr>
                      <th scope="col">Show</th>
                      <th scope="col">Internal name</th>
                      <th scope="col">Runtime filename</th>
                      <th scope="col">Type</th>
                      <th scope="col" className="sm-table__num">Size</th>
                      <th scope="col">Hash</th>
                      <th scope="col">Status</th>
                    </tr>
                  </thead>
                  <tbody>
                    {rows.length === 0 ? (
                      <tr>
                        <td colSpan={columnCount}>
                          <RuledStrip absence="empty" label="None" fact="No asset matches here." />
                        </td>
                      </tr>
                    ) : (
                      rows.map((asset) => (
                        <AssetRow
                          key={asset.id}
                          asset={asset}
                          entries={nodeEntriesForAsset(asset, model.nodes, manifestByNode, verdictIndex)}
                          selected={selectedAssetId === asset.id}
                          onSelect={() => {
                            setSelectedAssetId(asset.id)
                            setUploading(false)
                          }}
                        />
                      ))
                    )}
                  </tbody>
                </Table>
              </TableWrap>
            </div>
          </Section>
        </div>

        <aside>
          {uploading && (
            <AssetUploadForm
              scope={scope}
              showOptions={showOptions}
              nodes={model.nodes}
              knownSequences={knownSequences}
              assets={state.assets}
              model={model}
              onUploaded={() => {
                reload()
                manifest.reload()
              }}
              onCancel={() => setUploading(false)}
            />
          )}
          {!uploading && selectedAsset !== null && (
            <AssetDetail
              key={selectedAssetId}
              asset={selectedAsset}
              history={assetHistory(state.assets, assetIdentityKey(selectedAsset))}
              entries={nodeEntriesForAsset(selectedAsset, model.nodes, manifestByNode, verdictIndex)}
              model={model}
              onRolledBack={() => {
                reload()
                manifest.reload()
              }}
            />
          )}
        </aside>
      </Panes>
    </div>
  )
}

function AssetRow({
  asset,
  entries,
  selected,
  onSelect,
}: {
  asset: Asset
  entries: NodeEntry[]
  selected: boolean
  onSelect: () => void
}) {
  const summary = summarizeEntries(entries)
  return (
    <SelectableRow selected={selected} onActivate={onSelect} ariaLabel={`View ${asset.sequence} for ${targetLabel(asset)}`}>
      <td className="sm-data">{asset.show}</td>
      <td>
        <strong>{asset.sequence}</strong>
        <br />
        <span className="sm-data sm-small sm-faint">for {targetLabel(asset)}</span>
        {selected && <span className="sm-viewing">Viewing</span>}
      </td>
      <td className="sm-data sm-small sm-muted">{asset.runtimeFilename}</td>
      <td>
        <span className="sm-chip">{MEDIA_CHIP[asset.mediaType]}</span>
      </td>
      <td className="sm-table__num" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</td>
      <td className="sm-data sm-small sm-muted">{hashLabel(asset.contentHash)}</td>
      <td>
        <StatusPair tone={summary.tone} label={summary.label} />
      </td>
    </SelectableRow>
  )
}

/**
 * A history entry's own annotation, derived purely from the group's row
 * data (hash equality), never from a persisted "was rolled back" flag:
 * `GET /assets` carries no such field.
 */
function historyAnnotation(entry: Asset, history: readonly Asset[]): string | null {
  const currentEntry = history.find((e) => e.current)
  if (entry.current) {
    const priorMatch = history.find((e) => e.id !== entry.id && e.contentHash === entry.contentHash)
    if (priorMatch === undefined) return null
    const restoredBefore = priorMatch.supersededAt !== null ? formatDateClock(priorMatch.supersededAt) : null
    return `Rolled back to bytes current before ${restoredBefore ?? 'an unrecorded time'}`
  }
  if (currentEntry !== undefined && entry.contentHash === currentEntry.contentHash) return 'Same bytes as current'
  return null
}

type RollbackState =
  | { kind: 'idle' }
  | { kind: 'confirming'; entryId: string; confirmText: string }
  | { kind: 'busy'; entryId: string }
  | { kind: 'done'; entryId: string; rolledBack: boolean; asset: Asset }
  | { kind: 'failed'; entryId: string; reason: string }

function AssetDetail({
  asset,
  history,
  entries,
  model,
  onRolledBack,
}: {
  asset: Asset
  history: Asset[]
  entries: NodeEntry[]
  model: ReturnType<typeof useModelContext>
  onRolledBack: () => void
}) {
  const [rollback, setRollback] = useState<RollbackState>({ kind: 'idle' })
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, 'asset:write')

  const [resyncing, setResyncing] = useState(false)
  const [resyncError, setResyncError] = useState<string | null>(null)
  const [resyncNote, setResyncNote] = useState<string | null>(null)

  const resync = () => {
    if (asset.targetKind !== 'node') return
    setResyncing(true)
    setResyncError(null)
    setResyncNote(null)
    resyncNodeAssets(asset.target)
      .then(() => setResyncNote('Re-sync accepted. This node’s manifest reflects the result once it reports back in.'))
      .catch((err: unknown) => setResyncError(describeApiError(err)))
      .finally(() => setResyncing(false))
  }

  const confirmRollback = (entry: Asset) => {
    setRollback({ kind: 'busy', entryId: entry.id })
    getAssetContent(entry.id)
      .then((blob) => {
        const file = new File([blob], entry.runtimeFilename, { type: blob.type || 'application/octet-stream' })
        return uploadAsset(
          file,
          {
            show: entry.show,
            sequence: entry.sequence,
            mediaType: entry.mediaType,
            targetKind: entry.targetKind,
            ...(entry.targetKind === 'node' ? { target: entry.target } : {}),
          },
          () => {},
        )
      })
      .then((response) => {
        setRollback({ kind: 'done', entryId: entry.id, rolledBack: response.rolledBack, asset: response.asset })
        onRolledBack()
      })
      .catch((err: unknown) => setRollback({ kind: 'failed', entryId: entry.id, reason: describeApiError(err) }))
  }

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow">Asset variant</p>
      <h2 className="sm-inspector__title" id="assets-detail-heading">{asset.sequence}</h2>
      <p className="sm-small sm-muted">
        for <span className="sm-data">{targetLabel(asset)}</span>
      </p>

      <section className="sm-inspector__group">
        <h3 className="sm-subsection__title">Identity</h3>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Show</span>
          <p className="sm-inspector__value sm-data">{asset.show}</p>
        </div>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Sequence</span>
          <p className="sm-inspector__value sm-data">{asset.sequence}</p>
        </div>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Target</span>
          <p className="sm-inspector__value sm-data">{targetLabel(asset)}</p>
        </div>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Hash</span>
          <p className="sm-inspector__value sm-data">{asset.contentHash}</p>
        </div>
        <p className="sm-callout">
          Identity is those four facts. The runtime filename <span className="sm-data">{asset.runtimeFilename}</span> is not one of them; a different
          asset can carry the same filename.
        </p>
      </section>

      <section className="sm-inspector__group">
        <h3 className="sm-subsection__title">File details</h3>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Runtime filename</span>
          <p className="sm-inspector__value sm-data">{asset.runtimeFilename}</p>
        </div>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Type</span>
          <p className="sm-inspector__value sm-data">{MEDIA_CHIP[asset.mediaType]}</p>
        </div>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Size</span>
          <p className="sm-inspector__value sm-data" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</p>
        </div>
        <div className="sm-inspector__row">
          <span className="sm-inspector__label">Uploaded</span>
          <p className="sm-inspector__value sm-data">
            {formatDateClock(asset.createdAt) ?? 'at an unrecorded time'} by {asset.createdByPrincipalName ?? 'an unknown principal'}
          </p>
        </div>
        {asset.mediaType === 'audio' && (
          <div className="sm-inspector__row">
            <span className="sm-inspector__label">Node copy</span>
            <p className="sm-inspector__value sm-data">{renditionSummary(asset.rendition)}</p>
          </div>
        )}
      </section>

      <section className="sm-inspector__group">
        <h3 className="sm-subsection__title">Nodes</h3>
        <p className="sm-small sm-muted">Every node expected to hold this file, why, and its last transfer.</p>
        {entries.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="No node is declared." />
        ) : (
          entries.map((entry) => {
            const transfer = lastFetchLine(entry.lastFetch)
            return (
              <div key={entry.nodeId} className="sm-readout">
                <StatusPair tone={entry.tone} label={entry.stateLabel} />
                <div>
                  <p className="sm-data sm-flat">{entry.label ?? entry.nodeId}</p>
                  <p className="sm-readout__fact">{sourceReason(entry.source)}</p>
                  {transfer !== null && <p className="sm-readout__fact">{transfer}</p>}
                  <p className="sm-readout__fact sm-small sm-muted">
                    {entry.observedAt !== null ? `Reported ${formatDateClock(entry.observedAt) ?? 'at an unrecorded time'}.` : entry.nodeReason ?? 'This node has not reported.'}
                  </p>
                </div>
              </div>
            )
          })
        )}
      </section>

      <section className="sm-inspector__group">
        <h3 className="sm-subsection__title">History</h3>
        <p className="sm-small sm-muted">A version can become current more than once, so this reads as events, not a one-way list.</p>
        {history.map((entry) => {
          const annotation = historyAnnotation(entry, history)
          const canMakeCurrent = !entry.current && annotation !== 'Same bytes as current'
          const isConfirming = rollback.kind === 'confirming' && rollback.entryId === entry.id
          const isBusy = rollback.kind === 'busy' && rollback.entryId === entry.id
          const done = rollback.kind === 'done' && rollback.entryId === entry.id ? rollback : null
          const failed = rollback.kind === 'failed' && rollback.entryId === entry.id ? rollback : null
          return (
            <div key={`${entry.id} ${entry.createdAt}`} className="sm-readout">
              <span className={entry.current ? 'sm-eyebrow sm-eyebrow--accent sm-flat' : 'sm-eyebrow sm-flat'}>{entry.current ? 'Current' : 'Superseded'}</span>
              <div>
                <p className="sm-data sm-flat">{hashLabel(entry.contentHash)}</p>
                <p className="sm-readout__fact">
                  Uploaded {formatDateClock(entry.createdAt) ?? 'at an unrecorded time'} by {entry.createdByPrincipalName ?? 'an unknown principal'}
                </p>
                {annotation !== null && <p className="sm-readout__fact sm-eyebrow--accent sm-flat">{annotation}</p>}

                {canMakeCurrent && !isConfirming && !isBusy && done === null && (
                  <Button
                    variant="quiet"
                    size="compact"
                    onClick={() => setRollback({ kind: 'confirming', entryId: entry.id, confirmText: '' })}
                    disabled={!writeGate.allowed}
                    title={writeGate.allowed ? undefined : writeGate.reason}
                  >
                    Make current
                  </Button>
                )}

                {isConfirming && (
                  <div className="sm-inspector__group">
                    <Field label={`Type ${entry.sequence} to confirm the rollback`} help="Re-uploads these bytes; the coordinator swaps them in as current.">
                      {(props) => (
                        <Input
                          {...props}
                          value={rollback.kind === 'confirming' ? rollback.confirmText : ''}
                          onChange={(e) => setRollback({ kind: 'confirming', entryId: entry.id, confirmText: e.target.value })}
                        />
                      )}
                    </Field>
                    <ButtonRow>
                      <Button variant="quiet" onClick={() => setRollback({ kind: 'idle' })}>
                        Cancel
                      </Button>
                      <Button
                        variant="primary"
                        disabled={!writeGate.allowed || (rollback.kind === 'confirming' && rollback.confirmText !== entry.sequence)}
                        title={writeGate.allowed ? undefined : writeGate.reason}
                        onClick={() => confirmRollback(entry)}
                      >
                        Confirm rollback
                      </Button>
                    </ButtonRow>
                  </div>
                )}

                {isBusy && <p className="sm-small sm-muted">Rolling back…</p>}

                {done !== null && (
                  <p className="sm-verdict">
                    <StatusPair tone={done.rolledBack ? 'warn' : 'good'} label={done.rolledBack ? 'Rolled back' : 'Uploaded'} />
                    <span className="sm-verdict__detail">
                      {done.rolledBack
                        ? 'This superseded version is current again; the previous current version is now superseded.'
                        : 'Registered as the current asset for this identity.'}
                    </span>
                  </p>
                )}

                {failed !== null && <RuledStrip absence="failed" label="Rollback failed" fact={failed.reason} />}
              </div>
            </div>
          )
        })}
      </section>

      {resyncError !== null && <RuledStrip absence="failed" label="Refused" fact={resyncError} />}
      {resyncNote !== null && <p className="sm-small sm-muted">{resyncNote}</p>}
      <div className="sm-inspector__actions">
        <a className="sm-small" href={assetContentUrl(asset.id)}>
          Download
        </a>
        <Button
          variant="quiet"
          disabled={asset.targetKind !== 'node' || !writeGate.allowed || resyncing}
          title={asset.targetKind !== 'node' ? 'This asset is show-wide, not targeted at one node.' : writeGate.allowed ? undefined : writeGate.reason}
          onClick={resync}
        >
          {resyncing ? 'Re-syncing…' : 'Re-sync to node'}
        </Button>
      </div>
    </div>
  )
}

function AssetUploadForm({
  scope,
  showOptions,
  nodes,
  knownSequences,
  assets,
  model,
  onUploaded,
  onCancel,
}: {
  scope: AssetScope
  showOptions: readonly ConfigObjectSummary[]
  nodes: readonly { nodeId: string; label: string | null }[]
  knownSequences: readonly string[]
  assets: readonly Asset[]
  model: ReturnType<typeof useModelContext>
  onUploaded: () => void
  onCancel: () => void
}) {
  const [file, setFile] = useState<File | null>(null)
  const [fileHash, setFileHash] = useState<string | null>(null)
  const [sequence, setSequence] = useState('')
  const [mediaType, setMediaType] = useState<'fseq' | 'audio' | 'media'>('fseq')
  const [targetKind, setTargetKind] = useState<'node' | 'show'>('show')
  const [target, setTarget] = useState('')
  const [showId, setShowId] = useState(scope.kind === 'show' ? scope.showId : '')
  const [progress, setProgress] = useState<UploadProgress | null>(null)
  const [uploading, setUploading] = useState(false)
  const [result, setResult] = useState<{ rolledBack: boolean; asset: Asset } | null>(null)
  const [error, setError] = useState<string | null>(null)

  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, 'asset:write')

  const effectiveShowId = scope.kind === 'show' ? scope.showId : showId

  useEffect(() => {
    if (file === null) {
      setFileHash(null)
      return
    }
    let cancelled = false
    sha256Hex(file).then((hash) => {
      if (!cancelled) setFileHash(hash)
    })
    return () => {
      cancelled = true
    }
  }, [file])

  // A superseded entry sharing this exact identity and hash: uploading would perform ADR-028 decision 10's rollback.
  const matchedRollback =
    fileHash === null
      ? null
      : assets.find(
          (a) =>
            !a.current &&
            a.contentHash === fileHash &&
            a.show === effectiveShowId &&
            a.sequence === sequence.trim() &&
            a.targetKind === targetKind &&
            (targetKind === 'show' || a.target === target),
        ) ?? null

  let blockReason: string | null = null
  if (file === null) blockReason = 'Choose a file.'
  else if (sequence.trim() === '') blockReason = 'Name the logical sequence this file belongs to.'
  else if (scope.kind === 'all' && showId === '') blockReason = 'Identity needs a show. There is no default.'
  else if (targetKind === 'node' && target === '') blockReason = 'Identity needs a target. There is no default.'

  const submit = () => {
    if (blockReason !== null || file === null) return
    setUploading(true)
    setError(null)
    setResult(null)
    uploadAsset(file, { show: effectiveShowId, sequence: sequence.trim(), mediaType, targetKind, ...(targetKind === 'node' ? { target } : {}) }, setProgress)
      .then((response) => {
        setResult({ rolledBack: response.rolledBack, asset: response.asset })
        onUploaded()
      })
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => {
        setUploading(false)
        setProgress(null)
      })
  }

  return (
    <div className="sm-inspector">
      <h2 className="sm-eyebrow sm-eyebrow--accent" id="assets-upload-heading">Upload asset</h2>
      <p className="sm-small sm-muted">{scope.kind === 'show' ? 'Into this show. Manual upload is a permanent path, not a stopgap.' : 'Manual upload is a permanent path, not a stopgap.'}</p>

      <div className="sm-inspector__group">
        <label className="sm-dropzone" htmlFor="asset-upload-file">
          <span className="sm-body">{file === null ? 'Choose a file' : file.name}</span>
          <span className="sm-data sm-small sm-faint">FSEQ · WAV · MP3 · MP4 · PNG</span>
          <input id="asset-upload-file" type="file" onChange={(e) => setFile(e.target.files?.[0] ?? null)} />
        </label>
        {file !== null && (
          <p className="sm-small sm-muted sm-stack-2">
            <span className="sm-data">{file.name}</span> · {file.size} bytes
          </p>
        )}
      </div>

      <div className="sm-inspector__group">
        {scope.kind === 'all' && (
          <Field label="Show" error={showId === '' ? 'Identity needs a show. There is no default.' : undefined}>
            {(props) => (
              <Select {...props} value={showId} onChange={(e) => setShowId(e.target.value)}>
                <option value="">Choose a show…</option>
                {showOptions.map((show) => (
                  <option key={show.id} value={show.id}>
                    {show.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}

        <Field label="Logical sequence" help="What the show calls this content, across every target.">
          {(props) => (
            <>
              <Input {...props} list="asset-upload-sequences" value={sequence} onChange={(e) => setSequence(e.target.value)} />
              <datalist id="asset-upload-sequences">
                {knownSequences.map((s) => (
                  <option key={s} value={s} />
                ))}
              </datalist>
            </>
          )}
        </Field>

        <Field label="Media type">
          {(props) => (
            <Select {...props} value={mediaType} onChange={(e) => setMediaType(e.target.value as 'fseq' | 'audio' | 'media')}>
              <option value="fseq">FSEQ</option>
              <option value="audio">Audio</option>
              <option value="media">Media</option>
            </Select>
          )}
        </Field>

        <Segmented
          label="Target kind"
          value={targetKind}
          onChange={(value) => {
            setTargetKind(value)
            setTarget('')
          }}
          options={[
            { value: 'show', label: 'Show-wide' },
            { value: 'node', label: 'One node' },
          ]}
        />

        {targetKind === 'node' && (
          <Field label="Target" error={target === '' ? 'Identity needs a target. There is no default; picking wrong sends a node another node’s content.' : undefined}>
            {(props) => (
              <Select {...props} value={target} onChange={(e) => setTarget(e.target.value)}>
                <option value="">Choose a node…</option>
                {nodes.map((node) => (
                  <option key={node.nodeId} value={node.nodeId}>
                    {node.label ?? node.nodeId}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
      </div>

      {matchedRollback !== null && (
        <Notice
          tone="warn"
          headline="This will be a rollback"
          explanation={`Already stored as a superseded version from ${formatDateClock(matchedRollback.createdAt) ?? 'an unrecorded time'}; uploading makes it current again and supersedes today's.`}
          live="status"
        />
      )}

      <div className="sm-inspector__actions">
        <span className="sm-small sm-muted">{uploading && progress !== null ? `${progress.loaded} / ${progress.total} bytes` : 'Then syncs to the target'}</span>
        <div className="sm-btn-row">
          <Button variant="quiet" onClick={onCancel} disabled={uploading}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={submit}
            disabled={uploading || !writeGate.allowed || blockReason !== null}
            title={!writeGate.allowed ? writeGate.reason : (blockReason ?? undefined)}
          >
            {uploading ? (matchedRollback !== null ? 'Rolling back…' : 'Uploading…') : matchedRollback !== null ? 'Roll back' : 'Upload'}
          </Button>
        </div>
      </div>

      {result !== null && (
        <p className="sm-verdict">
          <StatusPair tone={result.rolledBack ? 'warn' : 'good'} label={result.rolledBack ? 'Rolled back' : 'Uploaded'} />
          <span className="sm-verdict__detail">
            {result.rolledBack
              ? 'These bytes matched a superseded version, which is now current again; the previously current version is now superseded.'
              : 'Registered, and now the current asset for this identity. Whether a node holds it yet is a separate observation, in Monitor › Manifest.'}
          </span>
        </p>
      )}
      {error !== null && <RuledStrip absence="failed" label="Upload failed" fact={error} />}
    </div>
  )
}
