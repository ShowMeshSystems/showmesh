import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { ApiError, getShowActive, putShow } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { PlannedFeature } from '../components/SharedLayouts'
import { useShowWorkspaceData } from '../components/ShowWorkspace'
import { showWorkspacePath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type { ConfigShow, ConfigShowWrite } from '../app/types'

// Shows.dc.html "detail" tweak: identity and notes only. What the show
// CONTAINS lives in its five workspace tabs (ShowWorkspace.tsx); this
// page states that split explicitly and links out to each tab's count
// rather than re-deriving anything.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

interface FormState {
  name: string
  notes: string
}

function formFromPayload(payload: ConfigShow): FormState {
  return { name: payload.name, notes: payload.notes }
}

function emptyForm(): FormState {
  return { name: '', notes: '' }
}

function buildPayload(form: FormState): { payload: ConfigShowWrite } | { error: string } {
  if (form.name.trim() === '') return { error: 'Name is required.' }
  return { payload: { name: form.name.trim(), notes: form.notes } }
}

export interface ShowDetailProps {
  isNew?: boolean
}

export function ShowDetail({ isNew = false }: ShowDetailProps) {
  const params = useParams<{ showId: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.showId

  const workspace = useShowWorkspaceData(existingId ?? '', !isNew)

  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm())
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  const [isActive, setIsActive] = useState<boolean | null>(null)

  useEffect(() => {
    if (workspace.kind === 'loaded') setForm(formFromPayload(workspace.show.payload))
  }, [workspace])

  useEffect(() => {
    if (isNew || existingId === undefined || !readGate.allowed) return
    let cancelled = false
    getShowActive()
      .then((resp) => {
        if (!cancelled) setIsActive(resp.payload.show === existingId)
      })
      .catch((err: unknown) => {
        if (!cancelled && err instanceof ApiError && err.status === 404) setIsActive(false)
      })
    return () => {
      cancelled = true
    }
  }, [isNew, existingId, readGate.allowed])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A show id is required.')
      return
    }
    const built = buildPayload(form)
    if ('error' in built) {
      setSaveError(built.error)
      return
    }
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      await putShow(id, built.payload)
      if (isNew) {
        navigate(`/shows/${encodeURIComponent(id)}`)
        return
      }
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  const pageGate = isNew ? writeGate : readGate
  if (!pageGate.allowed) {
    return (
      <div className="operator-page page-body">
        <h1 className="t-heading">{isNew ? 'New show' : 'Show'}</h1>
        <p className="ruled-strip ruled-strip--no-permission" role="status">
          <span className="ruled-strip__state t-meta">No permission</span>
          <span className="ruled-strip__explanation">{pageGate.reason}</span>
        </p>
      </div>
    )
  }

  if (!isNew && workspace.kind === 'loading') {
    return (
      <div className="operator-page page-body">
        <p className="ruled-strip ruled-strip--loading" role="status">
          <span className="ruled-strip__state t-meta">Loading</span>
          <span className="ruled-strip__explanation">Reading this show.</span>
        </p>
      </div>
    )
  }
  if (!isNew && workspace.kind === 'error') {
    return (
      <div className="operator-page page-body">
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">{workspace.message}</span>
        </p>
      </div>
    )
  }

  const editable = writeGate.allowed
  const contents = !isNew && workspace.kind === 'loaded' ? workspace.counts : undefined

  return (
    <div className="operator-page">
      <header className="page-header">
        {!isNew && (
          <p className="page-header__breadcrumb">
            <Link to="/shows">Shows</Link> <span aria-hidden="true">/</span> {form.name || existingId} <span aria-hidden="true">/</span> Details
          </p>
        )}
        <div className="page-header__row">
          <div style={{ minWidth: 0 }}>
            <div style={{ display: 'flex', alignItems: 'center', gap: 12, flexWrap: 'wrap' }}>
              <h1 className="t-display page-header__title">{isNew ? 'New show' : form.name || existingId}</h1>
              {isActive === true && <span className="status-pair status-pair--good">Active</span>}
            </div>
            <p className="page-header__meta">
              Identity and notes only. What the show contains lives in its workspace tabs.
            </p>
          </div>
          {!isNew && existingId !== undefined && (
            <div className="page-header__actions">
              <Link className="btn btn--secondary" to={showWorkspacePath(existingId, 'playlists')}>
                Open workspace
              </Link>
            </div>
          )}
        </div>
      </header>

      <div className="page-body">
        {!editable && (
          <p className="t-small shows-muted" role="status">
            Viewing only: editing requires the <code>config:write</code> scope.
          </p>
        )}

        <section aria-labelledby="sh-ident" className="shows-detail-section">
          <h2 id="sh-ident" className="t-heading">
            Identity
          </h2>
          <div className="shows-detail-fields">
            {isNew && (
              <label className="field">
                <span className="field__label">Show id</span>
                <input
                  className="field__input"
                  type="text"
                  value={newId}
                  disabled={!editable}
                  onChange={(e) => setNewId(e.target.value)}
                />
              </label>
            )}
            <label className="field">
              <span className="field__label">Name</span>
              <input
                className="field__input"
                type="text"
                value={form.name}
                disabled={!editable}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
              />
            </label>
            {!isNew && (
              <div className="field">
                <span className="field__label">Id</span>
                <p className="field__input shows-fixed-value t-data">{existingId}</p>
                <span className="field__help">
                  Fixed at creation. Every cue, surface and asset in this show is keyed on it, so it
                  cannot change.
                </span>
              </div>
            )}
            <label className="field">
              <span className="field__label">Notes</span>
              <textarea
                className="field__input"
                rows={4}
                value={form.notes}
                disabled={!editable}
                onChange={(e) => setForm({ ...form, notes: e.target.value })}
              />
              <span className="field__help">For whoever operates this next, including you in eleven months.</span>
            </label>
          </div>
        </section>

        {!isNew && contents !== undefined && (
          <section aria-labelledby="sh-contents" className="shows-detail-section">
            <h2 id="sh-contents" className="t-heading">
              What this show contains
            </h2>
            <div className="shows-contents-grid">
              <Link className="shows-contents-card" to={showWorkspacePath(existingId!, 'playlists')}>
                <span className="t-meta shows-faint">Playlists</span>
                <strong className="t-heading">{contents.playlists}</strong>
              </Link>
              <Link className="shows-contents-card" to={showWorkspacePath(existingId!, 'cues')}>
                <span className="t-meta shows-faint">Cues</span>
                <strong className="t-heading">{contents.cues}</strong>
              </Link>
              <Link className="shows-contents-card" to={showWorkspacePath(existingId!, 'presentation')}>
                <span className="t-meta shows-faint">Surfaces</span>
                <strong className="t-heading">{contents.presentation}</strong>
              </Link>
              <Link className="shows-contents-card" to={showWorkspacePath(existingId!, 'assets')}>
                <span className="t-meta shows-faint">Assets</span>
                <strong className="t-heading">{contents.assets}</strong>
              </Link>
            </div>
            <p className="t-small shows-muted">
              Each of these is its own revisioned object, so editing a cue creates a cue revision,
              not an opaque revision of the whole show.
            </p>
          </section>
        )}

        {editable && (
          <div className="shows-detail-actions">
            {saveError !== null && (
              <p role="alert" className="field__error">
                {saveError}
              </p>
            )}
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              className="btn btn--primary"
              onClick={() => void handleSave()}
              busy={saving}
              busyReason="Saving this show revision…"
            >
              {saving ? 'Saving…' : isNew ? 'Create show' : 'Save show'}
            </ScopedButton>
            {!isNew && workspace.kind === 'loaded' && (
              <span className="t-small shows-muted">
                Active revision <span className="t-data">{workspace.show.revision}</span>
                {workspace.show.createdByPrincipalName !== null && ` · ${workspace.show.createdByPrincipalName}`}{' '}
                {formatAbsolute(workspace.show.updatedAt)}
              </span>
            )}
          </div>
        )}

        {!isNew && (
          <PlannedFeature
            headingLevel={2}
            title="Delete this show"
            why={
              isActive === true
                ? `No delete endpoint exists for show configuration in this API, and this is the active show: deleting it would orphan ${
                    contents ? `${contents.cues} cues, ${contents.playlists} playlists and ${contents.presentation} surfaces` : 'its contents'
                  } and leave the installation with no authority for tonight regardless.`
                : 'No delete endpoint exists for show configuration in this API. Its cues, playlists, surfaces and assets remain reachable, and assets are never removed either way, since they are content, not configuration.'
            }
            preview={
              <button type="button" className="btn btn--destructive">
                Delete show
              </button>
            }
          />
        )}
      </div>
    </div>
  )
}
