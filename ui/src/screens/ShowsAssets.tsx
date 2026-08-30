import { useEffect, useState } from 'react'
import { Link, useParams } from 'react-router-dom'
import { assetContentUrl, listAssets, uploadAsset, type Asset, type UploadProgress } from '../api'
import { Button, Callout, Field, Input, Panes, RuledStrip, Section, Segmented, Select, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatDateClock } from '../domain/time'
import { type AssetGroup, assetGroups, assetHistory, assetIdentityKey, formatBytes, hashLabel, targetLabel } from './showsModel'

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; assets: Asset[] }
  | { kind: 'failed'; reason: string }

function useShowAssets(showId: string): { state: ListState; reload: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    listAssets({ show: showId })
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', assets: response.assets })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId, attempt])

  return { state, reload: () => setAttempt((n) => n + 1) }
}

const MEDIA_FILTERS: readonly { value: 'all' | Asset['mediaType']; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'fseq', label: 'FSEQ' },
  { value: 'audio', label: 'Audio' },
  { value: 'media', label: 'Media' },
]

const MEDIA_CHIP: Record<Asset['mediaType'], string> = { fseq: 'FSEQ', audio: 'Audio', media: 'Media' }

export function ShowsAssets() {
  const { id: showId = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const { state, reload } = useShowAssets(showId)
  const [selectedIdentity, setSelectedIdentity] = useState<string | null>(null)
  const [uploading, setUploading] = useState(false)
  const [filterText, setFilterText] = useState('')
  const [filterMedia, setFilterMedia] = useState<'all' | Asset['mediaType']>('all')

  if (state.kind === 'loading') {
    return (
      <Section id="asset-list" title="Assets in this show">
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this show's assets." />
      </Section>
    )
  }

  if (state.kind === 'failed') {
    return (
      <Section id="asset-list" title="Assets in this show">
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

  const groups = assetGroups(state.assets).filter((group) => {
    if (filterMedia !== 'all' && group.mediaType !== filterMedia) return false
    if (filterText === '') return true
    const needle = filterText.toLowerCase()
    return group.sequence.toLowerCase().includes(needle) || group.current.some((a) => a.target.toLowerCase().includes(needle))
  })

  const selectedIdentityAsset = selectedIdentity === null ? null : state.assets.find((a) => assetIdentityKey(a) === selectedIdentity) ?? null
  const knownSequences = Array.from(new Set(state.assets.map((a) => a.sequence))).sort()

  return (
    <Panes>
      <div>
        <Section
          id="asset-list"
          title="Assets in this show"
          aside={
            <Button
              variant="primary"
              onClick={() => {
                setUploading(true)
                setSelectedIdentity(null)
              }}
            >
              Upload
            </Button>
          }
        >
          <Callout>
            Sync runs on upload and on a timer, never because a show started. Nodes always play from their own disk, so a node missing an asset is a
            readiness fault found before a show, not during one. Node-by-node sync state is Monitor &rsaquo; Manifest's own facet, not this tab; see{' '}
            <Link to="/monitor/manifest">Monitor &rsaquo; Manifest</Link> and docs/ui-rebuild/OPEN-DECISIONS.md D-016.
          </Callout>

          <p className="sm-small sm-muted sm-stack-4">
            Grouped by logical sequence, because one sequence produces a different file per target and xLights gives them all the same name. The
            filename belongs to the group; identity belongs to the row.
          </p>

          <div className="sm-inline-row sm-stack-3">
            <Input aria-label="Filter assets" placeholder="Filter assets…" value={filterText} onChange={(e) => setFilterText(e.target.value)} />
            <Segmented label="Filter by media type" value={filterMedia} options={MEDIA_FILTERS} onChange={setFilterMedia} />
          </div>

          <TableWrap label="This show's current assets, grouped by sequence, scrollable">
            <Table>
              <thead>
                <tr>
                  <th scope="col">Target</th>
                  <th scope="col">Hash</th>
                  <th scope="col">Size</th>
                </tr>
              </thead>
              <tbody>
                {groups.length === 0 ? (
                  <tr>
                    <td colSpan={3}>
                      <RuledStrip absence="empty" label="None" fact="No asset matches here." />
                    </td>
                  </tr>
                ) : (
                  groups.map((group) => <AssetGroupRows key={`${group.sequence} ${group.mediaType}`} group={group} selectedIdentity={selectedIdentity} onSelect={(identity) => {
                    setSelectedIdentity(identity)
                    setUploading(false)
                  }} />)
                )}
              </tbody>
            </Table>
          </TableWrap>
        </Section>
      </div>

      <aside>
        {uploading && (
          <AssetUploadForm
            showId={showId}
            nodes={model.nodes}
            knownSequences={knownSequences}
            model={model}
            onUploaded={() => {
              reload()
            }}
            onCancel={() => setUploading(false)}
          />
        )}
        {!uploading && selectedIdentityAsset !== null && <AssetDetail asset={selectedIdentityAsset} history={assetHistory(state.assets, selectedIdentity ?? '')} />}
      </aside>
    </Panes>
  )
}

function AssetGroupRows({
  group,
  selectedIdentity,
  onSelect,
}: {
  group: AssetGroup
  selectedIdentity: string | null
  onSelect: (identity: string) => void
}) {
  return (
    <>
      <tr className="sm-table__group">
        <td colSpan={3}>
          <span className="sm-subhead">{group.sequence}</span>{' '}
          <span className="sm-chip">{MEDIA_CHIP[group.mediaType]}</span>{' '}
          <span className="sm-data sm-small sm-faint">
            {group.runtimeFilename} · {group.current.length} {group.current.length === 1 ? 'target shares' : 'targets share'} this filename
          </span>
        </td>
      </tr>
      {group.current.map((asset) => {
        const identity = assetIdentityKey(asset)
        return (
          <tr key={identity} aria-current={selectedIdentity === identity ? 'true' : undefined} className={selectedIdentity === identity ? 'sm-table__row--current' : undefined}>
            <td>
              <button type="button" className="sm-linkbutton" onClick={() => onSelect(identity)} aria-pressed={selectedIdentity === identity}>
                {targetLabel(asset)}
              </button>
              {selectedIdentity === identity && <span className="sm-viewing">Viewing</span>}
            </td>
            <td className="sm-data sm-small sm-muted">{hashLabel(asset.contentHash)}</td>
            <td className="sm-data sm-small sm-muted" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</td>
          </tr>
        )
      })}
    </>
  )
}

function AssetDetail({ asset, history }: { asset: Asset; history: Asset[] }) {
  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow">Asset variant</p>
      <h2 className="sm-inspector__title">{asset.sequence}</h2>
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
        <h3 className="sm-subsection__title">History</h3>
        <p className="sm-small sm-muted">A version can become current more than once, so this reads as events, not a one-way list.</p>
        {history.map((entry) => (
          <div key={`${entry.id} ${entry.createdAt}`} className="sm-readout">
            <span className={entry.current ? 'sm-eyebrow sm-eyebrow--accent sm-flat' : 'sm-eyebrow sm-flat'}>{entry.current ? 'Current' : 'Superseded'}</span>
            <div>
              <p className="sm-data sm-flat">{hashLabel(entry.contentHash)}</p>
              <p className="sm-readout__fact">
                Uploaded {formatDateClock(entry.createdAt) ?? 'at an unrecorded time'} by {entry.createdByPrincipalName ?? 'an unknown principal'}
              </p>
            </div>
          </div>
        ))}
      </section>

      <div className="sm-inspector__actions">
        <a className="sm-small" href={assetContentUrl(asset.id)}>
          Download
        </a>
      </div>
    </div>
  )
}

function AssetUploadForm({
  showId,
  nodes,
  knownSequences,
  model,
  onUploaded,
  onCancel,
}: {
  showId: string
  nodes: readonly { nodeId: string; label: string | null }[]
  knownSequences: readonly string[]
  model: ReturnType<typeof useModelContext>
  onUploaded: () => void
  onCancel: () => void
}) {
  const [file, setFile] = useState<File | null>(null)
  const [sequence, setSequence] = useState('')
  const [mediaType, setMediaType] = useState<'fseq' | 'audio' | 'media'>('fseq')
  const [targetKind, setTargetKind] = useState<'node' | 'show'>('show')
  const [target, setTarget] = useState('')
  const [progress, setProgress] = useState<UploadProgress | null>(null)
  const [uploading, setUploading] = useState(false)
  const [result, setResult] = useState<{ rolledBack: boolean; asset: Asset } | null>(null)
  const [error, setError] = useState<string | null>(null)

  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, 'asset:write')

  let blockReason: string | null = null
  if (file === null) blockReason = 'Choose a file.'
  else if (sequence.trim() === '') blockReason = 'Name the logical sequence this file belongs to.'
  else if (targetKind === 'node' && target === '') blockReason = 'Identity needs a target. There is no default.'

  const submit = () => {
    if (blockReason !== null || file === null) return
    setUploading(true)
    setError(null)
    setResult(null)
    uploadAsset(file, { show: showId, sequence: sequence.trim(), mediaType, targetKind, ...(targetKind === 'node' ? { target } : {}) }, setProgress)
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
      <p className="sm-eyebrow sm-eyebrow--accent">Upload asset</p>
      <p className="sm-small sm-muted">Into this show. Manual upload is a permanent path, not a stopgap.</p>

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
            {uploading ? 'Uploading…' : 'Upload'}
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
