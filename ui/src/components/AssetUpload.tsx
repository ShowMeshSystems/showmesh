import { useEffect, useRef, useState } from 'react'
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
import { findRollbackMatch, historyForIdentity, sha256ContentHash } from '../views/assets/assetGrouping'
import type { AssetMediaType, AssetTargetKind } from '../app/types'
import '../styles/assets.css'

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
   * Called once the coordinator confirms the upload — never optimistically
   * before that. `rolledBack` is ADR-028 decision 10's own field.
   */
  onUploaded: (asset: Asset, rolledBack: boolean) => void
  /** When set, Show is fixed (a workspace-tab upload into a known show) rather than typed. */
  lockedShow?: string
  /** Known logical sequences for this show, offered as a picker instead of a bare text field. */
  knownSequences?: string[]
  /**
   * Every asset row already known for this scope (the show's own list, or
   * whatever the cross-show list currently holds) — used ONLY to state
   * "this will be a rollback" BEFORE the bytes are sent, by hashing the
   * chosen file client-side and comparing it against a superseded row
   * sharing this exact identity (ADR-028 decision 10). Never sent to the
   * server; the coordinator makes its own authoritative rollback
   * determination on POST.
   */
  identityCandidates?: Asset[]
  /** Renders a Cancel action beside the submit button, for the inspector-pane variant. */
  onCancel?: () => void
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
export function AssetUpload({ onUploaded, lockedShow, knownSequences, identityCandidates, onCancel }: AssetUploadProps) {
  const model = useModelContext()
  const [show, setShow] = useState(lockedShow ?? '')
  const [sequence, setSequence] = useState('')
  const [mediaType, setMediaType] = useState<AssetMediaType | ''>('')
  const [targetKind, setTargetKind] = useState<AssetTargetKind | ''>('')
  const [target, setTarget] = useState('')
  const [uploadState, setUploadState] = useState<UploadState>({ kind: 'idle' })
  const [fieldError, setFieldError] = useState<string | null>(null)
  const [chosenFile, setChosenFile] = useState<File | null>(null)
  const [rollbackMatch, setRollbackMatch] = useState<Asset | null>(null)
  const uploadingRef = useRef(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  const declaredNodes = model.nodes.filter((n) => n.declaration.declared)

  // Rollback is stated BEFORE the bytes are ever sent (ADR-028 decision
  // 10 / the design's own rule): hash the chosen file locally and compare
  // it to a superseded row sharing this exact identity. `sha256ContentHash`
  // returns null when the browser has no SubtleCrypto (an old or
  // non-secure context); the banner simply stays absent rather than guess.
  useEffect(() => {
    let cancelled = false
    if (chosenFile === null || show.trim() === '' || sequence.trim() === '' || targetKind === '' || (identityCandidates ?? []).length === 0) {
      setRollbackMatch(null)
      return
    }
    if (targetKind === 'node' && target.trim() === '') {
      setRollbackMatch(null)
      return
    }
    sha256ContentHash(chosenFile).then((hash) => {
      if (cancelled || hash === null) return
      const identity: Asset = {
        id: '',
        show: show.trim(),
        sequence: sequence.trim(),
        targetKind,
        target: targetKind === 'node' ? target.trim() : '',
        mediaType: mediaType === '' ? 'fseq' : mediaType,
        contentHash: hash,
        runtimeFilename: chosenFile.name,
        sizeBytes: chosenFile.size,
        createdAt: '',
        createdByPrincipalId: null,
        createdByPrincipalName: null,
        supersededAt: null,
        current: false,
      }
      const history = historyForIdentity(identityCandidates ?? [], identity)
      setRollbackMatch(findRollbackMatch(history, hash))
    })
    return () => {
      cancelled = true
    }
  }, [chosenFile, show, sequence, targetKind, target, mediaType, identityCandidates])

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
      return setFieldError('Target node is required for a node-targeted asset: this is part of the asset’s own identity.')
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
      // A rollback is stated as its own state, never inferred from the
      // reloaded list (ADR-028 decision 10).
      setUploadState(resp.rolledBack ? { kind: 'rolledBack', assetId: resp.asset.id } : { kind: 'idle' })
      if (fileInputRef.current) fileInputRef.current.value = ''
      setChosenFile(null)
      setRollbackMatch(null)
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
  const submitLabel = uploading ? 'Uploading…' : rollbackMatch !== null ? 'Roll back' : 'Upload asset'

  return (
    <div className="card asset-upload">
      <div className="asset-upload__head">
        <h3 className="t-meta asset-upload__eyebrow">Upload asset</h3>
        {lockedShow !== undefined && <p className="t-small text-muted">Into {lockedShow}. Manual upload is a permanent path, not a stopgap.</p>}
      </div>
      <fieldset disabled={uploading} className="asset-upload__fields">
        {lockedShow === undefined && (
          <label className="field">
            <span className="field__label">Show</span>
            <input className="field__input" type="text" value={show} onChange={(e) => setShow(e.target.value)} />
          </label>
        )}
        {knownSequences !== undefined && knownSequences.length > 0 ? (
          <>
            <label className="field">
              <span className="field__label">Logical sequence</span>
              <select
                className="field__input"
                value={knownSequences.includes(sequence) ? sequence : sequence === '' ? '' : '__new__'}
                onChange={(e) => setSequence(e.target.value === '__new__' ? '' : e.target.value)}
              >
                <option value="" disabled>
                  Choose one, never defaulted
                </option>
                {knownSequences.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
                <option value="__new__">New sequence…</option>
              </select>
              <span className="field__help">What the show calls this content, across every target.</span>
            </label>
            {!knownSequences.includes(sequence) && (
              <label className="field">
                <span className="field__label">New sequence id</span>
                <input className="field__input" type="text" value={sequence} onChange={(e) => setSequence(e.target.value)} />
              </label>
            )}
          </>
        ) : (
          <label className="field">
            <span className="field__label">Sequence</span>
            <input className="field__input" type="text" value={sequence} onChange={(e) => setSequence(e.target.value)} />
          </label>
        )}
        <label className="field">
          <span className="field__label">Media type</span>
          <select className="field__input" value={mediaType} onChange={(e) => setMediaType(e.target.value as AssetMediaType)}>
            <option value="" disabled>
              Choose one, never defaulted
            </option>
            {MEDIA_TYPES.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="field__label">Target</span>
          <select className="field__input" value={targetKind} onChange={(e) => setTargetKind(e.target.value as AssetTargetKind)}>
            <option value="" disabled>
              Choose one, never defaulted
            </option>
            <option value="node">One specific node</option>
            <option value="show">Show-wide (every node that needs this sequence)</option>
          </select>
        </label>
        {targetKind === 'node' && (
          <label className="field field--invalid">
            {/* The target is part of this asset's own identity (ADR-028 decision 1) — never defaulted. */}
            <span className="field__label">Target node (required, part of this asset&rsquo;s own identity)</span>
            {declaredNodes.length > 0 ? (
              <select className="field__input" value={target} onChange={(e) => setTarget(e.target.value)}>
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
                className="field__input"
                type="text"
                placeholder="declared node id"
                value={target}
                onChange={(e) => setTarget(e.target.value)}
              />
            )}
            <span className="field__error" role="alert">
              Identity needs a target. There is no default. Two targets&rsquo; files for one sequence share this filename, and picking wrong sends a node another node&rsquo;s content.
            </span>
          </label>
        )}
        <label htmlFor="asset-upload-file" className="field">
          <span className="field__label">File</span>
          <input
            id="asset-upload-file"
            ref={fileInputRef}
            type="file"
            onChange={(e) => setChosenFile(e.target.files?.[0] ?? null)}
          />
        </label>
      </fieldset>

      {rollbackMatch !== null && (
        <p className="asset-upload__rollback-banner" role="status">
          <span className="t-meta">This will be a rollback</span>
          <span className="t-small">
            These exact bytes are already stored as a superseded version from {rollbackMatch.createdAt.slice(0, 10)}. Uploading makes that version
            current again and supersedes today&rsquo;s.
          </span>
        </p>
      )}

      {fieldError !== null && (
        <p role="alert" className="field__error asset-upload__field-error">
          {fieldError}
        </p>
      )}

      <div className="asset-upload__actions">
        {onCancel !== undefined && (
          <button type="button" className="btn btn--quiet" onClick={onCancel}>
            Cancel
          </button>
        )}
        <ScopedButton requiredScope={ASSET_WRITE_SCOPE} onClick={() => void handleUpload()} busy={uploading} className="btn btn--primary">
          {submitLabel}
        </ScopedButton>
      </div>

      {uploadState.kind === 'uploading' && (
        <div role="status" aria-live="polite" className="asset-upload__progress">
          {uploadState.progress.total !== null ? (
            <progress value={uploadState.progress.loaded} max={uploadState.progress.total} />
          ) : (
            <progress />
          )}
          <p className="t-small text-muted">
            {uploadState.progress.total !== null
              ? `Uploading… ${formatBytes(uploadState.progress.loaded)} of ${formatBytes(uploadState.progress.total)}`
              : `Uploading… ${formatBytes(uploadState.progress.loaded)} sent so far.`}
          </p>
        </div>
      )}

      {uploadState.kind === 'rolledBack' && (
        <p role="status" className="asset-upload__rollback-banner">
          Rollback: the re-uploaded bytes matched a superseded asset. {uploadState.assetId} is current again, and the asset that superseded it is
          now superseded.
        </p>
      )}

      {uploadState.kind === 'error' && (
        <p
          role="alert"
          className={
            uploadState.error.kind === 'forbidden' || uploadState.error.kind === 'unauthorized'
              ? 'asset-upload__rollback-banner'
              : 'field__error asset-upload__field-error'
          }
        >
          {uploadState.error.message}
        </p>
      )}
    </div>
  )
}
