import { useEffect, useRef, useState } from 'react'
import {
  ApiError,
  CSRFRejectedError,
  ForbiddenError,
  UnauthorizedError,
  getResolumeComposition,
  uploadResolumeComposition,
  type ResolumeCompositionSummary,
  type UploadProgress,
} from '../api'
import { describeApiError } from '../app/session'
import { ScopedButton } from './ScopedButton'

const CONFIG_WRITE_SCOPE = 'config:write'

// Track D seam D-2a (ADR-032 decision 8): the composition upload control.
// Uploading a `.avc` file is the ONLY way this subsystem's id map gets
// configuration, so this component owns two independent pieces of state:
// what is CURRENTLY stored (fetched once, from GET) and the upload
// attempt itself (idle/uploading/error). The two never merge — see
// handleUpload below — because ADR-030 requires "a rejected upload
// changes nothing on screen except the error," and the only way to make
// that true structurally is to let a successful upload's own response
// (which already carries the full ResolumeCompositionSummary — no second
// GET needed) be the ONLY thing that ever moves `stored` forward.

type LoadState =
  | { kind: 'loading' }
  | { kind: 'not_stored'; reason: string }
  // ADR-032's GET is gated behind the SAME config:write scope as POST
  // (internal/coordinator/api/api.go's route registration), not open like
  // an ordinary read. This view (ResolumeView.tsx) renders with no
  // session at all (build contract §2.2), so a session lacking the scope
  // reaches this branch routinely, not just on a revoked-scope race.
  | { kind: 'forbidden'; reason: string }
  | { kind: 'unauthorized'; reason: string }
  | { kind: 'error'; message: string }
  | { kind: 'stored'; composition: ResolumeCompositionSummary; revision: number; activatedAt: string }

type UploadErrorKind = 'rejected' | 'too_large' | 'forbidden' | 'unauthorized' | 'transport'

interface UploadError {
  kind: UploadErrorKind
  message: string
}

type UploadState =
  | { kind: 'idle' }
  | { kind: 'uploading'; progress: UploadProgress }
  | { kind: 'error'; error: UploadError }

/**
 * Dispatches a thrown upload error onto one of the five DISTINGUISHABLE
 * failure states the task spec requires: a rejected file (400, the
 * server's own reason), too large (413), a 403 from a session without
 * the scope, a 401, and a transport failure — never collapsed into one
 * generic "upload failed" message, and never presented as a connectivity
 * problem when the coordinator actually answered.
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

/** "Arena 7.23.2" — the writtenBy the .avc's own header records (ADR-032 decision 7). */
function formatWrittenBy(writtenBy: ResolumeCompositionSummary['writtenBy']): string {
  return `${writtenBy.product} ${writtenBy.major}.${writtenBy.minor}.${writtenBy.micro}.${writtenBy.revision}`
}

function CompositionSummaryView({
  composition,
  revision,
  activatedAt,
}: {
  composition: ResolumeCompositionSummary
  revision: number
  activatedAt: string
}) {
  return (
    <div className="panel" role="status">
      <dl className="field-list">
        <dt>Name</dt>
        <dd>{composition.name}</dd>
        <dt>Uploaded file</dt>
        <dd>
          {composition.sourceFilename} ({formatBytes(composition.sizeBytes)})
        </dd>
        <dt>Written by</dt>
        <dd>{formatWrittenBy(composition.writtenBy)}</dd>
        <dt>Canvas</dt>
        <dd>
          {composition.canvas.width} × {composition.canvas.height}
        </dd>
        <dt>Decks</dt>
        <dd>
          {composition.decks.length === 0 ? (
            'none'
          ) : (
            <ul className="list-plain">
              {composition.decks.map((deck) => (
                <li key={deck.id}>
                  {deck.name}
                  {deck.closed ? ' (closed)' : ''}: {deck.clipCount} clip{deck.clipCount === 1 ? '' : 's'}
                </li>
              ))}
            </ul>
          )}
        </dd>
        <dt>Layers</dt>
        <dd>
          {composition.layerCount} across {composition.layerGroupCount} group
          {composition.layerGroupCount === 1 ? '' : 's'}
        </dd>
        <dt>Columns</dt>
        <dd>{composition.columnCount}</dd>
        <dt>Clips</dt>
        <dd>
          {composition.clipCount} ({composition.persistentClipCount} persistent)
        </dd>
        <dt>Stored revision</dt>
        <dd>
          {revision}, activated {activatedAt}
        </dd>
      </dl>
    </div>
  )
}

export function ResolumeCompositionUpload() {
  const [loadState, setLoadState] = useState<LoadState>({ kind: 'loading' })
  const [uploadState, setUploadState] = useState<UploadState>({ kind: 'idle' })
  // Synchronous, shared-across-closures guard — the same reason
  // Configuration.tsx's own `savingRef` exists (that component's own
  // comment: a React state boolean alone lets two fast clicks both pass
  // the check before the first render commits `saving: true`). An
  // in-flight upload must not be submittable twice.
  const uploadingRef = useRef(false)
  const fileInputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    let cancelled = false
    async function load(): Promise<void> {
      try {
        const resp = await getResolumeComposition()
        if (cancelled) return
        setLoadState({ kind: 'stored', composition: resp.composition, revision: resp.revision, activatedAt: resp.activatedAt })
      } catch (err) {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setLoadState({ kind: 'not_stored', reason: err.message })
          return
        }
        if (err instanceof ForbiddenError) {
          setLoadState({ kind: 'forbidden', reason: err.message })
          return
        }
        if (err instanceof UnauthorizedError) {
          setLoadState({ kind: 'unauthorized', reason: err.message })
          return
        }
        setLoadState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [])

  async function handleUpload(): Promise<void> {
    if (uploadingRef.current) return
    const file = fileInputRef.current?.files?.[0]
    if (file === undefined || file === null) return
    uploadingRef.current = true
    setUploadState({ kind: 'uploading', progress: { loaded: 0, total: null } })
    try {
      const resp = await uploadResolumeComposition(file, (progress) => {
        setUploadState({ kind: 'uploading', progress })
      })
      // The server's own confirmed response replaces `loadState` — never
      // an optimistic guess rendered before this resolves (ADR-030: "a
      // partial upload registers nothing," and the inverse holds too:
      // nothing renders as stored until the coordinator actually says so).
      setLoadState({ kind: 'stored', composition: resp.composition, revision: resp.revision, activatedAt: resp.activatedAt })
      setUploadState({ kind: 'idle' })
      if (fileInputRef.current) fileInputRef.current.value = ''
    } catch (err) {
      // Deliberately does NOT touch loadState: a rejected upload changes
      // nothing on screen except this error (ADR-032 decision 7 — a
      // malformed file persists no revision, no config object, no audit
      // entry — and the task spec's own rule for this control).
      setUploadState({ kind: 'error', error: classifyUploadError(err) })
    } finally {
      uploadingRef.current = false
    }
  }

  const uploading = uploadState.kind === 'uploading'

  return (
    <div>
      <h3 className="panel__title">Resolume composition</h3>
      <p className="text-muted">
        The composition file this coordinator reads clip, layer and deck ids from. Resolume itself is never asked
        to list them; only an uploaded copy of the composition file is. Uploading a new file replaces the stored
        map entirely.
      </p>

      {loadState.kind === 'loading' && <p className="text-muted">Loading the stored composition…</p>}
      {loadState.kind === 'not_stored' && (
        <p className="panel" role="status">
          {loadState.reason}
        </p>
      )}
      {loadState.kind === 'forbidden' && (
        <p className="panel panel--warning" role="alert">
          {loadState.reason}
        </p>
      )}
      {loadState.kind === 'unauthorized' && (
        <p className="panel panel--warning" role="alert">
          {loadState.reason}
        </p>
      )}
      {loadState.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {loadState.message}
        </p>
      )}
      {loadState.kind === 'stored' && (
        <CompositionSummaryView
          composition={loadState.composition}
          revision={loadState.revision}
          activatedAt={loadState.activatedAt}
        />
      )}

      <div style={{ marginTop: '1rem' }}>
        <label htmlFor="resolume-composition-file">Composition file (.avc)</label>
        <br />
        <input
          id="resolume-composition-file"
          ref={fileInputRef}
          type="file"
          accept=".avc"
          disabled={uploading}
        />
        <div style={{ marginTop: '0.5rem' }}>
          {/* Review finding 5: this used to sit inside Configuration.tsx's
              config:write gate and lost that protection when it moved
              here — rendering as an ordinary enabled <button> let a
              session with no credential pick a file, transfer it, and
              only THEN learn it was refused. ADR-024 decision 12 wants it
              disabled with a stated reason instead. */}
          <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} onClick={() => void handleUpload()} busy={uploading}>
            {uploading ? 'Uploading…' : 'Upload composition'}
          </ScopedButton>
        </div>

        {uploadState.kind === 'uploading' && (
          <div role="status" aria-live="polite" style={{ marginTop: '0.5rem' }}>
            {/*
              A native <progress> element, not a hand-built div-and-CSS
              bar: when `total` is known (lengthComputable), value/max
              give a real, browser-rendered determinate bar backed by
              actual bytes sent. When it is NOT known, this renders
              <progress> with no `value` at all, which browsers render as
              a genuinely indeterminate animation (and assistive tech
              announces as indeterminate) — never a fabricated percentage,
              and never a CSS animation dressed up to look like measured
              progress (the "fake progress bar" this task spec explicitly
              forbids).
            */}
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

        {uploadState.kind === 'error' && (
          <p
            role="alert"
            // A 401/403 gets the SAME visually-distinct warning treatment
            // as loadState's own forbidden/unauthorized branches above —
            // never .text-error's red, which this component reserves for
            // an actual rejection or a transport failure (see
            // classifyUploadError's own comment).
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
    </div>
  )
}
