import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  assetContentUrl,
  getAssetContent,
  listAssets,
  listConfigObjects,
  uploadAsset,
  type Asset,
  type ConfigObjectSummary,
  type UploadProgress,
} from '../api'
import { Button, ButtonRow, Callout, Field, Input, NotWired, Notice, Panes, RuledStrip, Section, Segmented, Select, SelectableRow, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatDateClock } from '../domain/time'
import { type AssetGroup, assetGroups, assetHistory, assetIdentityKey, formatBytes, hashLabel, targetLabel } from './showsModel'

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

const MEDIA_FILTERS: readonly { value: 'all' | Asset['mediaType']; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'fseq', label: 'FSEQ' },
  { value: 'audio', label: 'Audio' },
  { value: 'media', label: 'Media' },
]

const MEDIA_CHIP: Record<Asset['mediaType'], string> = { fseq: 'FSEQ', audio: 'Audio', media: 'Media' }

export function AssetsSurface({ scope }: { scope: AssetScope }) {
  const model = useModelContext()
  const { state, reload } = useScopedAssets(scope)
  const showOptions = useShowOptions(scope.kind === 'all')
  const [selectedIdentity, setSelectedIdentity] = useState<string | null>(null)
  const [uploading, setUploading] = useState(false)
  const [filterText, setFilterText] = useState('')
  const [filterMedia, setFilterMedia] = useState<'all' | Asset['mediaType']>('all')
  const [filterShow, setFilterShow] = useState('all')

  const sectionTitle = scope.kind === 'show' ? 'Assets in this show' : 'All assets'
  const tableLabel =
    scope.kind === 'show'
      ? "This show's current assets, grouped by sequence, scrollable"
      : "Every show's current assets, grouped by sequence, scrollable"
  const columnCount = 3

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

  const scopedAssets = scope.kind === 'all' && filterShow !== 'all' ? state.assets.filter((a) => a.show === filterShow) : state.assets

  const groups = assetGroups(scopedAssets).filter((group) => {
    if (filterMedia !== 'all' && group.mediaType !== filterMedia) return false
    if (filterText === '') return true
    const needle = filterText.toLowerCase()
    return group.sequence.toLowerCase().includes(needle) || group.current.some((a) => a.target.toLowerCase().includes(needle))
  })

  const selectedIdentityAsset = selectedIdentity === null ? null : state.assets.find((a) => assetIdentityKey(a) === selectedIdentity) ?? null
  const knownSequences = Array.from(new Set(state.assets.map((a) => a.sequence))).sort()
  const knownShows = Array.from(new Set(state.assets.map((a) => a.show))).sort()

  return (
    <div className="sm-assets-surface">
      <Panes>
        <div>
          <Section
            id="asset-list"
            title={sectionTitle}
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
                <Table minWidth={scope.kind === 'all' ? 560 : 520}>
                  <thead>
                    <tr>
                      <th scope="col">Target</th>
                      <th scope="col">Hash</th>
                      <th scope="col" className="sm-table__num">Size</th>
                    </tr>
                  </thead>
                  <tbody>
                    {groups.length === 0 ? (
                      <tr>
                        <td colSpan={columnCount}>
                          <RuledStrip absence="empty" label="None" fact="No asset matches here." />
                        </td>
                      </tr>
                    ) : (
                      groups.map((group) => (
                        <AssetGroupRows
                          key={`${group.show} ${group.sequence} ${group.mediaType}`}
                          group={group}
                          showColumn={scope.kind === 'all'}
                          selectedIdentity={selectedIdentity}
                          onSelect={(identity) => {
                            setSelectedIdentity(identity)
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
              }}
              onCancel={() => setUploading(false)}
            />
          )}
          {!uploading && selectedIdentityAsset !== null && (
            <AssetDetail
              key={selectedIdentity}
              asset={selectedIdentityAsset}
              history={assetHistory(state.assets, selectedIdentity ?? '')}
              model={model}
              onRolledBack={() => reload()}
            />
          )}
        </aside>
      </Panes>
    </div>
  )
}

function AssetGroupRows({
  group,
  showColumn,
  selectedIdentity,
  onSelect,
}: {
  group: AssetGroup
  showColumn: boolean
  selectedIdentity: string | null
  onSelect: (identity: string) => void
}) {
  const columnCount = 3
  return (
    <>
      <tr className="sm-table__group">
        <td colSpan={columnCount}>
          <div className="sm-assets-table__group-heading">
            {showColumn && (
              <>
                <span className="sm-assets-table__show">{group.show}</span>
                <span className="sm-assets-table__separator" aria-hidden="true">/</span>
              </>
            )}
            <span className="sm-subhead">{group.sequence}</span>
            <span className="sm-chip">{MEDIA_CHIP[group.mediaType]}</span>
            <span className="sm-data sm-small sm-faint">
              {group.runtimeFilename} · {group.current.length} {group.current.length === 1 ? 'target shares' : 'targets share'} this filename
            </span>
          </div>
        </td>
      </tr>
      {group.current.map((asset) => {
        const identity = assetIdentityKey(asset)
        return (
          <SelectableRow key={identity} selected={selectedIdentity === identity} onActivate={() => onSelect(identity)} ariaLabel={`View ${group.sequence} for ${targetLabel(asset)}`}>
            <td>
              <strong>{targetLabel(asset)}</strong>
              {selectedIdentity === identity && <span className="sm-viewing">Viewing</span>}
            </td>
            <td className="sm-data sm-small sm-muted">{hashLabel(asset.contentHash)}</td>
            <td className="sm-table__num" title={`${asset.sizeBytes} bytes`}>{formatBytes(asset.sizeBytes)}</td>
          </SelectableRow>
        )
      })}
    </>
  )
}

/**
 * A history entry's own annotation, derived purely from the group's row
 * data (hash equality), never from a persisted "was rolled back" flag,
 * `GET /assets` carries no such field (docs/ui-rebuild/OPEN-DECISIONS.md
 * D-016).
 */
function historyAnnotation(entry: Asset, history: readonly Asset[]): string | null {
  const currentEntry = history.find((e) => e.current)
  if (entry.current) {
    const priorMatch = history.find((e) => e.id !== entry.id && e.contentHash === entry.contentHash)
    if (priorMatch === undefined) return null
    const restoredBefore = priorMatch.supersededAt !== null ? formatDateClock(priorMatch.supersededAt) : null
    return `Rollback, these exact bytes were current before ${restoredBefore ?? 'an unrecorded time'}`
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
  model,
  onRolledBack,
}: {
  asset: Asset
  history: Asset[]
  model: ReturnType<typeof useModelContext>
  onRolledBack: () => void
}) {
  const [rollback, setRollback] = useState<RollbackState>({ kind: 'idle' })
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, 'asset:write')

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
                    <Field label={`Type ${entry.sequence} to confirm the rollback`} help="Re-uploads these exact bytes; the coordinator performs the swap (ADR-028).">
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
                        ? 'These bytes matched a superseded version, which is now current again; the previously current version is now superseded.'
                        : 'Registered, and now the current asset for this identity.'}
                    </span>
                  </p>
                )}

                {failed !== null && <RuledStrip absence="failed" label="Rollback failed" fact={failed.reason} />}
              </div>
            </div>
          )
        })}
      </section>

      <div className="sm-inspector__actions">
        <a className="sm-small" href={assetContentUrl(asset.id)}>
          Download
        </a>
        <NotWired>
          <Button variant="quiet">Re-sync to node</Button>
        </NotWired>
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
      <p className="sm-eyebrow sm-eyebrow--accent">Upload asset</p>
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
          explanation={`These exact bytes are already stored as a superseded version from ${formatDateClock(matchedRollback.createdAt) ?? 'an unrecorded time'}. Uploading makes that version current again and supersedes today's.`}
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
