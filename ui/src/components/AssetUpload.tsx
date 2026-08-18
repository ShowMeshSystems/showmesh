import { useRef, useState } from 'react'
import {
  ApiError,
  CSRFRejectedError,
  ForbiddenError,
  UnauthorizedError,
  uploadAsset,
  type Asset,
  type UploadProgress,
} from '../api'
import { describeApiError } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { ScopedButton } from './ScopedButton'
import type { AssetMediaType, AssetTargetKind } from '../app/types'

const ASSET_WRITE_SCOPE = 'asset:write'
const MEDIA_TYPES: AssetMediaType[] = ['fseq', 'audio', 'media']

type UploadErrorKind = 'rejected' | 'too_large' | 'storage_full' | 'forbidden' | 'unauthorized' | 'transport'

interface UploadError {
  kind: UploadErrorKind
  message: string
}

type UploadState =
  | { kind: 'idle' }
  | { kind: 'uploading'; progress: UploadProgress }
  | { kind: 'error'; error: UploadError }
  | { kind: 'rolledBack'; assetId: string }

/**
 * Dispatches a thrown upload error onto a DISTINGUISHABLE failure state —
 * same posture as ResolumeCompositionUpload.tsx's classifyUploadError,
 * extended with 507 (storage-full, ADR-030/api/openapi.yaml's own POST
 * /assets response set).
 */
function classifyUploadError(err: unknown): UploadError {
  if (err instanceof CSRFRejectedError || err instanceof ForbiddenError) {
    return { kind: 'forbidden', message: describeApiError(err) }
  }
  if (err instanceof UnauthorizedError) {
    return { kind: 'unauthorized', message: describeApiError(err) }
  }
  if (err instanceof ApiError && err.status === 413) {
    return { kind: 'too_large', message: describeApiError(err) }
  }
  if (err instanceof ApiError && err.status === 507) {
    return { kind: 'storage_full', message: describeApiError(err) }
  }
  if (err instanceof ApiError && err.status === 400) {
    return { kind: 'rejected', message: describeApiError(err) }
  }
  return { kind: 'transport', message: describeApiError(err) }
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  const kib = bytes / 1024
  if (kib < 1024) return `${kib.toFixed(1)} KiB`
  return `${(kib / 1024).toFixed(2)} MiB`
}

export interface AssetUploadProps {
  /**
   * Called with the registered (or idempotently pre-existing) asset once
   * the coordinator confirms it — never optimistically before that.
   * `rolledBack` is ADR-028 decision 10's own field: true only when this
   * upload matched a SUPERSEDED identity and un-superseded it.
   */
  onUploaded: (asset: Asset, rolledBack: boolean) => void
}

/**
 * The asset upload control (ADR-028, ADR-030). Same "state progress and
 * failure, never infer them" shape as ResolumeCompositionUpload.tsx, with
 * one addition that component does not need: `show`, `sequence`,
 * `mediaType`, and `targetKind` are required fields the operator must
 * fill in before uploading, and `target` (a declared node) is REQUIRED,
 * not defaulted, whenever `targetKind` is "node" — a defaulted target
 * would produce a confidently mislabelled artifact, since the target is
 * part of the asset's own identity (ADR-028 decision 1).
 */
export function AssetUpload({ onUploaded }: AssetUploadProps) {
  const model = useModelContext()
  const [show, setShow] = useState('')
  const [sequence, setSequence] = useState('')
  const [mediaType, setMediaType] = useState<AssetMediaType | ''>('')
  const [targetKind, setTargetKind] = useState<AssetTargetKind | ''>('')
  const [target, setTarget] = useState('')
  const [uploadState, setUploadState] = useState<UploadState>({ kind: 'idle' })
  const [fieldError, setFieldError] = useState<string | null>(null)
  const uploadingRef = useRef(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const declaredNodes = model.nodes.filter((n) => n.declaration.declared)

  async function handleUpload(): Promise<void> {
    if (uploadingRef.current) return
    setFieldError(null)
    const file = fileInputRef.current?.files?.[0]
    if (file === undefined || file === null) {
      setFieldError('Choose a file first.')
      return
    }
    if (show.trim() === '') return setFieldError('Show is required.')
    if (sequence.trim() === '') return setFieldError('Sequence is required.')
    if (mediaType === '') return setFieldError('Media type is required and has no default.')
    if (targetKind === '') return setFieldError('Target kind is required and has no default.')
    if (targetKind === 'node' && target.trim() === '') {
      return setFieldError('Target node is required for a node-targeted asset — this is part of the asset’s own identity.')
    }

    uploadingRef.current = true
    setUploadState({ kind: 'uploading', progress: { loaded: 0, total: null } })
    try {
      const resp = await uploadAsset(
        file,
        {
          show: show.trim(),
          sequence: sequence.trim(),
          mediaType,
          targetKind,
          ...(targetKind === 'node' ? { target: target.trim() } : {}),
        },
        (progress) => {
          setUploadState({ kind: 'uploading', progress })
        },
      )
      // The coordinator's own confirmed response is what registers this
      // asset — never an optimistic guess rendered before this resolves
      // (ADR-030: "a partial upload registers nothing"). A rollback is
      // stated as its own state, not left for the operator to infer from
      // the reloaded list (ADR-028 decision 10, ADR-030 decision 5).
      setUploadState(resp.rolledBack ? { kind: 'rolledBack', assetId: resp.asset.id } : { kind: 'idle' })
      if (fileInputRef.current) fileInputRef.current.value = ''
      onUploaded(resp.asset, resp.rolledBack)
    } catch (err) {
      // Deliberately does not touch anything else on screen: a rejected
      // upload registers no asset (ADR-028's own transaction rule) and
      // this component asserts nothing changed except this error.
      setUploadState({ kind: 'error', error: classifyUploadError(err) })
    } finally {
      uploadingRef.current = false
    }
  }

  const uploading = uploadState.kind === 'uploading'

  return (
    <div className="panel">
      <h3 className="panel__title">Upload an asset</h3>
      <fieldset disabled={uploading}>
        <label className="form-field">
          Show
          <input type="text" value={show} onChange={(e) => setShow(e.target.value)} />
        </label>
        <label className="form-field">
          Sequence
          <input type="text" value={sequence} onChange={(e) => setSequence(e.target.value)} />
        </label>
        <label className="form-field">
          Media type
          <select value={mediaType} onChange={(e) => setMediaType(e.target.value as AssetMediaType)}>
            <option value="" disabled>
              Choose one — never defaulted
            </option>
            {MEDIA_TYPES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <label className="form-field">
          Target
          <select value={targetKind} onChange={(e) => setTargetKind(e.target.value as AssetTargetKind)}>
            <option value="" disabled>
              Choose one — never defaulted
            </option>
            <option value="node">One specific node</option>
            <option value="show">Show-wide (every node that needs this sequence)</option>
          </select>
        </label>
        {targetKind === 'node' && (
          <label className="form-field">
            {/* The target is part of this asset's own identity (ADR-028 decision 1) — never defaulted. */}
            Target node (required — part of this asset&rsquo;s own identity)
            {declaredNodes.length > 0 ? (
              <select value={target} onChange={(e) => setTarget(e.target.value)}>
                <option value="" disabled>
                  Choose a declared node
                </option>
                {declaredNodes.map((n) => (
                  <option key={n.nodeId} value={n.nodeId}>
                    {n.declaration.label ?? n.nodeId} ({n.nodeId})
                  </option>
                ))}
              </select>
            ) : (
              <input
                type="text"
                placeholder="declared node id"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
              />
            )}
          </label>
        )}
        <label htmlFor="asset-upload-file" className="form-field">
          File
          <input id="asset-upload-file" ref={fileInputRef} type="file" />
        </label>
      </fieldset>

      {fieldError !== null && (
        <p role="alert" className="session-form__error">
          {fieldError}
        </p>
      )}

      <div style={{ marginTop: '0.5rem' }}>
        <ScopedButton requiredScope={ASSET_WRITE_SCOPE} onClick={() => void handleUpload()} busy={uploading}>
          {uploading ? 'Uploading…' : 'Upload asset'}
        </ScopedButton>
      </div>

      {uploadState.kind === 'uploading' && (
        <div role="status" aria-live="polite" style={{ marginTop: '0.5rem' }}>
          {uploadState.progress.total !== null ? (
            <progress value={uploadState.progress.loaded} max={uploadState.progress.total} />
          ) : (
            <progress />
          )}
          <p className="text-muted">
            {uploadState.progress.total !== null
              ? `Uploading… ${formatBytes(uploadState.progress.loaded)} of ${formatBytes(uploadState.progress.total)}`
              : `Uploading… ${formatBytes(uploadState.progress.loaded)} sent so far.`}
          </p>
        </div>
      )}

      {uploadState.kind === 'rolledBack' && (
        <p role="status" className="panel panel--warning" style={{ marginTop: '0.5rem' }}>
          Rollback: the re-uploaded bytes matched a superseded asset. {uploadState.assetId} is current again, and the
          asset that superseded it is now superseded.
        </p>
      )}

      {uploadState.kind === 'error' && (
        <p
          role="alert"
          className={
            uploadState.error.kind === 'forbidden' || uploadState.error.kind === 'unauthorized'
              ? 'panel panel--warning'
              : 'text-error'
          }
          style={{ marginTop: '0.5rem' }}
        >
          {uploadState.error.message}
        </p>
      )}
    </div>
  )
}
