import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  getShowPlaylist,
  getShowPlaylistRevisions,
  listConfigObjects,
  putShowPlaylist,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { ShowSelect } from '../components/ShowSelect'
import type {
  ConfigShowPlaylist,
  PlaylistMismatchPolicy,
  PlaylistRunner,
  PlaylistShowmeshAudioRepeat,
  ShowPlaylistConfigResponse,
} from '../app/types'
import { showWorkspacePath } from '../components/showWorkspacePaths'

// Track H seam H6 (TRACK-H-cues-and-playlists.md "H6"): show.playlist
// authoring. Same "mirror, not authority" posture as ShowActionDetail.tsx
// and ShowSurfaceDetail.tsx (ADR-030): every check this form makes also
// exists server-side, and a refused `PUT` renders through
// describeApiError exactly like those two, never a client-invented
// message. This is a FULL REPLACEMENT form: `entries` is sent as exactly
// what the table below holds, never merged with what the server already
// holds.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'
const MAX_ENTRIES = 200

const RUNNERS: PlaylistRunner[] = ['fpp', 'showmesh-audio']
const MISMATCH_POLICIES: PlaylistMismatchPolicy[] = ['hold', 'blackAndSilence', 'safeCue']
const REPEAT_MODES: PlaylistShowmeshAudioRepeat[] = ['none', 'all']

interface EntryForm {
  id: string
  cue: string
  fppEnabled: boolean
  fppSection: string
  fppPosition: string
  fppExpectedSequenceFilename: string
  fppExpectedMediaFilename: string
}

interface FormState {
  show: string
  name: string
  runner: PlaylistRunner | ''
  mismatchPolicy: PlaylistMismatchPolicy | ''
  safeCueRef: string
  fppInstanceUuid: string
  fppPlaylistName: string
  fppPlaylistHash: string
  showmeshAudioEnabled: boolean
  showmeshAudioRepeat: PlaylistShowmeshAudioRepeat | ''
  entries: EntryForm[]
}

function newEntryForm(): EntryForm {
  return {
    id: '',
    cue: '',
    fppEnabled: false,
    fppSection: '',
    fppPosition: '',
    fppExpectedSequenceFilename: '',
    fppExpectedMediaFilename: '',
  }
}

function emptyForm(): FormState {
  return {
    show: '',
    name: '',
    runner: '',
    mismatchPolicy: '',
    safeCueRef: '',
    fppInstanceUuid: '',
    fppPlaylistName: '',
    fppPlaylistHash: '',
    showmeshAudioEnabled: false,
    showmeshAudioRepeat: '',
    entries: [newEntryForm()],
  }
}

function entryFromPayload(entry: ConfigShowPlaylist['entries'][number]): EntryForm {
  return {
    id: entry.id,
    cue: entry.cue,
    fppEnabled: entry.fpp !== undefined,
    fppSection: entry.fpp?.section ?? '',
    fppPosition: entry.fpp === undefined ? '' : String(entry.fpp.position),
    fppExpectedSequenceFilename: entry.fpp?.expectedSequenceFilename ?? '',
    fppExpectedMediaFilename: entry.fpp?.expectedMediaFilename ?? '',
  }
}

function formFromPayload(payload: ConfigShowPlaylist): FormState {
  return {
    show: payload.show,
    name: payload.name,
    runner: payload.runner,
    mismatchPolicy: payload.mismatchPolicy ?? '',
    safeCueRef: payload.safeCueRef ?? '',
    fppInstanceUuid: payload.fpp?.instanceUuid ?? '',
    fppPlaylistName: payload.fpp?.playlistName ?? '',
    fppPlaylistHash: payload.fpp?.playlistHash ?? '',
    showmeshAudioEnabled: payload.showmeshAudio !== undefined,
    showmeshAudioRepeat: payload.showmeshAudio?.repeat ?? '',
    entries: payload.entries.length > 0 ? payload.entries.map(entryFromPayload) : [newEntryForm()],
  }
}

/**
 * Mirrors, not enforces (ADR-030): every check here also exists
 * server-side (api/openapi.yaml's own description of PUT
 * /config/show.playlist/{id}): `show` is immutable (checked server-side,
 * not here — this form never diffs against the stored show), `runner`
 * has no default, `fpp`/`showmeshAudio`/`mismatchPolicy` are each
 * permitted only under the matching runner, `safeCueRef` is required iff
 * `mismatchPolicy` is `safeCue`, `entries` is required and non-empty,
 * entry ids must be unique, and two entries sharing the same
 * `fpp.section`/`fpp.position` pair are refused.
 */
function buildPayload(form: FormState): { payload: ConfigShowPlaylist } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.name.trim() === '') return { error: 'Name is required.' }
  if (form.name.trim().length > 200) return { error: 'Name must be 200 characters or fewer.' }
  if (form.runner === '') return { error: 'Runner is required and has no default; pick fpp or showmesh-audio.' }

  if (form.entries.length === 0) return { error: 'A playlist needs at least one entry.' }
  if (form.entries.length > MAX_ENTRIES) return { error: `A playlist may have at most ${MAX_ENTRIES} entries.` }

  const entries: ConfigShowPlaylist['entries'] = []
  const seenIds = new Set<string>()
  const seenFppKeys = new Set<string>()
  for (const [index, entry] of form.entries.entries()) {
    if (entry.id.trim() === '') return { error: `Entry ${index + 1} needs an id.` }
    if (entry.cue.trim() === '') return { error: `Entry ${index + 1} needs a cue.` }
    if (seenIds.has(entry.id.trim())) return { error: `Entry id "${entry.id.trim()}" is used more than once; entry ids must be unique.` }
    seenIds.add(entry.id.trim())

    const built: ConfigShowPlaylist['entries'][number] = { id: entry.id.trim(), cue: entry.cue.trim() }
    if (entry.fppEnabled) {
      if (entry.fppPosition.trim() === '') return { error: `Entry ${index + 1}'s FPP position is required when its FPP binding is enabled.` }
      const position = Number(entry.fppPosition)
      if (!Number.isInteger(position) || position < 0) {
        return { error: `Entry ${index + 1}'s FPP position must be a whole number, zero or greater.` }
      }
      const fppKey = `${entry.fppSection}\0${position}`
      if (seenFppKeys.has(fppKey)) {
        return {
          error: `Entry ${index + 1} shares its FPP section/position with another entry; two entries deriving the same FPP entry key are refused.`,
        }
      }
      seenFppKeys.add(fppKey)
      built.fpp = {
        section: entry.fppSection,
        position,
        ...(entry.fppExpectedSequenceFilename.trim() !== '' ? { expectedSequenceFilename: entry.fppExpectedSequenceFilename.trim() } : {}),
        ...(entry.fppExpectedMediaFilename.trim() !== '' ? { expectedMediaFilename: entry.fppExpectedMediaFilename.trim() } : {}),
      }
    }
    entries.push(built)
  }

  const payload: ConfigShowPlaylist = {
    show: form.show.trim(),
    name: form.name.trim(),
    runner: form.runner,
    entries,
  }

  if (form.runner === 'fpp') {
    if (form.fppInstanceUuid.trim() === '') return { error: 'FPP instance UUID is required when the runner is fpp.' }
    if (form.fppPlaylistName.trim() === '') return { error: 'FPP playlist name is required when the runner is fpp.' }
    if (form.fppPlaylistHash.trim() === '') return { error: 'FPP playlist hash is required when the runner is fpp.' }
    if (!/^[0-9a-f]{64}$/.test(form.fppPlaylistHash.trim())) {
      return { error: 'FPP playlist hash must be 64 lowercase hex characters, the imported canonical hash.' }
    }
    payload.fpp = {
      instanceUuid: form.fppInstanceUuid.trim(),
      playlistName: form.fppPlaylistName.trim(),
      playlistHash: form.fppPlaylistHash.trim(),
    }
    if (form.mismatchPolicy !== '') {
      payload.mismatchPolicy = form.mismatchPolicy
      if (form.mismatchPolicy === 'safeCue') {
        if (form.safeCueRef.trim() === '') {
          return { error: 'A safe cue is required when the mismatch policy is "safeCue".' }
        }
        payload.safeCueRef = form.safeCueRef.trim()
      } else if (form.safeCueRef.trim() !== '') {
        return { error: 'Safe cue only applies when the mismatch policy is "safeCue". Clear it or pick "safeCue".' }
      }
    } else if (form.safeCueRef.trim() !== '') {
      return { error: 'Safe cue only applies when a mismatch policy is set.' }
    }
  } else {
    if (form.mismatchPolicy !== '') {
      return { error: 'A mismatch policy only applies when the runner is fpp.' }
    }
    if (form.showmeshAudioEnabled) {
      if (form.showmeshAudioRepeat === '') {
        return { error: 'Repeat is required when showmesh-audio settings are enabled and has no default.' }
      }
      payload.showmeshAudio = { repeat: form.showmeshAudioRepeat }
    }
  }

  return { payload }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowPlaylistConfigResponse; revisions: ConfigRevisionMeta[] }

export interface ShowPlaylistDetailProps {
  isNew?: boolean
}

export function ShowPlaylistDetail({ isNew = false }: ShowPlaylistDetailProps) {
  const params = useParams<{ id: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm())
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  // Cue id -> label, so an entry's `cue` reference can be shown by name as
  // well as id. Resolved from `GET /config/show.cue?show=<id>` (the same
  // list endpoint ShowCues' own list view uses), never a lookup the API
  // does not support: this never fetches a single Cue's full payload,
  // only the same summary list a Cues list view renders.
  const [cueLabels, setCueLabels] = useState<Record<string, string>>({})

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getShowPlaylist(existingId), getShowPlaylistRevisions(existingId)])
      .then(([config, revisionsResp]) => {
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setForm(formFromPayload(config.payload))
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [existingId, readGate.allowed, isNew])

  useEffect(() => {
    const show = form.show.trim()
    if (show === '' || !readGate.allowed) {
      setCueLabels({})
      return
    }
    let cancelled = false
    listConfigObjects('show.cue', show)
      .then((resp) => {
        if (cancelled) return
        const next: Record<string, string> = {}
        for (const obj of resp.objects) next[obj.id] = obj.label
        setCueLabels(next)
      })
      .catch(() => {
        // Best-effort display resolution only: a failure to list Cues
        // leaves entries showing their raw cue id, never blocks the form.
        if (!cancelled) setCueLabels({})
      })
    return () => {
      cancelled = true
    }
  }, [form.show, readGate.allowed])

  function updateEntry(index: number, patch: Partial<EntryForm>): void {
    setForm((f) => ({ ...f, entries: f.entries.map((e, i) => (i === index ? { ...e, ...patch } : e)) }))
  }

  function addEntry(): void {
    setForm((f) => (f.entries.length >= MAX_ENTRIES ? f : { ...f, entries: [...f.entries, newEntryForm()] }))
  }

  function removeEntry(index: number): void {
    setForm((f) => ({ ...f, entries: f.entries.filter((_, i) => i !== index) }))
  }

  function moveEntry(index: number, direction: -1 | 1): void {
    setForm((f) => {
      const target = index + direction
      if (target < 0 || target >= f.entries.length) return f
      const entries = f.entries.slice()
      const [moved] = entries.splice(index, 1)
      if (moved === undefined) return f
      entries.splice(target, 0, moved)
      return { ...f, entries }
    })
  }

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A playlist id is required.')
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
      const resp = await putShowPlaylist(id, built.payload)
      if (isNew) {
        navigate(`/config/show.playlist/${encodeURIComponent(id)}`)
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      const revisionsResp = await getShowPlaylistRevisions(id)
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, revisions: revisionsResp.revisions } : prev))
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
      <div>
        <h2 className="panel__title">{isNew ? 'New playlist' : 'Playlist'}</h2>
        <p className="panel panel--error" role="status">
          {pageGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading playlist…</p>
  }
  if (!isNew && state.kind === 'error') {
    return (
      <p className="panel panel--error" role="alert">
        {state.message}
      </p>
    )
  }

  const editable = writeGate.allowed

  return (
    <div className="operator-page authoring-page">
      {!isNew && form.show !== '' && (
        <p className="settings-breadcrumb"><Link to={showWorkspacePath(form.show)}>Back to {form.show}</Link> / Playlist details</p>
      )}
      <h2 className="panel__title">{isNew ? 'New playlist' : form.name || existingId}</h2>

      {!editable && (
        <p className="text-muted" role="status">
          Viewing only: editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="form-field">
          Playlist id
          <input type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable}>
        <div className="form-field">
          <ShowSelect label="Show" value={form.show} onChange={(show) => setForm({ ...form, show })} />
        </div>
        <label className="form-field">
          Name
          <input type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        </label>

        <h3 className="panel__title">Runner and authority</h3>
        <p className="text-muted">
          The runner is which system actually plays this Playlist: FPP, or ShowMesh&rsquo;s own
          audio player. A mismatch policy only applies to an FPP-run Playlist; showmesh-audio
          settings only apply to a showmesh-audio-run one.
        </p>
        <label className="form-field">
          Runner
          <select
            value={form.runner}
            onChange={(e) =>
              setForm({
                ...form,
                runner: e.target.value as PlaylistRunner,
                // Switching runner clears the fields the new runner does
                // not permit, mirroring the server's own runner-scoped
                // rules rather than leaving a stale, now-invalid value
                // sitting in the form.
                mismatchPolicy: '',
                safeCueRef: '',
                showmeshAudioEnabled: false,
                showmeshAudioRepeat: '',
              })
            }
          >
            <option value="" disabled>
              Choose one, never defaulted
            </option>
            {RUNNERS.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        </label>

        {form.runner === 'fpp' && (
          <>
            <label className="form-field">
              FPP instance UUID
              <input
                type="text"
                value={form.fppInstanceUuid}
                onChange={(e) => setForm({ ...form, fppInstanceUuid: e.target.value })}
              />
            </label>
            <label className="form-field">
              FPP playlist name
              <input
                type="text"
                value={form.fppPlaylistName}
                onChange={(e) => setForm({ ...form, fppPlaylistName: e.target.value })}
              />
            </label>
            <label className="form-field">
              FPP playlist hash (the imported canonical hash, 64 lowercase hex characters)
              <input
                type="text"
                value={form.fppPlaylistHash}
                onChange={(e) => setForm({ ...form, fppPlaylistHash: e.target.value })}
              />
            </label>

            <label className="form-field">
              Mismatch policy (optional)
              <select
                value={form.mismatchPolicy}
                onChange={(e) =>
                  setForm({
                    ...form,
                    mismatchPolicy: e.target.value as PlaylistMismatchPolicy,
                    safeCueRef: e.target.value === 'safeCue' ? form.safeCueRef : '',
                  })
                }
              >
                <option value="">Not set</option>
                {MISMATCH_POLICIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
            {form.mismatchPolicy === 'safeCue' && (
              <label className="form-field">
                Safe cue (must name a same-show Cue)
                <input
                  type="text"
                  value={form.safeCueRef}
                  onChange={(e) => setForm({ ...form, safeCueRef: e.target.value })}
                />
              </label>
            )}
          </>
        )}

        {form.runner === 'showmesh-audio' && (
          <>
            <label className="form-field form-field--checkbox">
              <input
                type="checkbox"
                checked={form.showmeshAudioEnabled}
                onChange={(e) => setForm({ ...form, showmeshAudioEnabled: e.target.checked })}
              />
              Configure showmesh-audio settings
            </label>
            {form.showmeshAudioEnabled && (
              <label className="form-field">
                Repeat
                <select
                  value={form.showmeshAudioRepeat}
                  onChange={(e) => setForm({ ...form, showmeshAudioRepeat: e.target.value as PlaylistShowmeshAudioRepeat })}
                >
                  <option value="" disabled>
                    Choose one, never defaulted
                  </option>
                  {REPEAT_MODES.map((r) => (
                    <option key={r} value={r}>
                      {r}
                    </option>
                  ))}
                </select>
              </label>
            )}
          </>
        )}

        <h3 className="panel__title">Entries</h3>
        <p className="text-muted">
          The ordered run of Cues this Playlist plays. Order is meaningful: use Move up/Move down
          to change it. Each entry names one Cue, defined on the Cues screen; a Playlist never
          declares an output directly itself.
        </p>
        <div className="table-scroll">
          <table className="config-table" aria-label="Entries">
            <thead>
              <tr>
                <th scope="col">Order</th>
                <th scope="col">Entry id</th>
                <th scope="col">Cue</th>
                <th scope="col">FPP section</th>
                <th scope="col">FPP position</th>
                <th scope="col">Actions</th>
              </tr>
            </thead>
            <tbody>
              {form.entries.map((entry, index) => (
                <tr key={index}>
                  <th scope="row">{index + 1}</th>
                  <td>
                    <input
                      aria-label={`Entry ${index + 1} id`}
                      type="text"
                      value={entry.id}
                      onChange={(e) => updateEntry(index, { id: e.target.value })}
                    />
                  </td>
                  <td>
                    <input
                      aria-label={`Entry ${index + 1} cue`}
                      type="text"
                      value={entry.cue}
                      onChange={(e) => updateEntry(index, { cue: e.target.value })}
                    />
                    {entry.cue.trim() !== '' && cueLabels[entry.cue.trim()] !== undefined && (
                      <div className="text-muted">{cueLabels[entry.cue.trim()]}</div>
                    )}
                  </td>
                  <td>
                    <label className="form-field form-field--checkbox">
                      <input
                        type="checkbox"
                        aria-label={`Entry ${index + 1} FPP binding enabled`}
                        checked={entry.fppEnabled}
                        onChange={(e) => updateEntry(index, { fppEnabled: e.target.checked })}
                      />
                      Bound
                    </label>
                    {entry.fppEnabled && (
                      <input
                        aria-label={`Entry ${index + 1} FPP section`}
                        type="text"
                        value={entry.fppSection}
                        onChange={(e) => updateEntry(index, { fppSection: e.target.value })}
                      />
                    )}
                  </td>
                  <td>
                    {entry.fppEnabled && (
                      <input
                        aria-label={`Entry ${index + 1} FPP position`}
                        type="number"
                        min={0}
                        value={entry.fppPosition}
                        onChange={(e) => updateEntry(index, { fppPosition: e.target.value })}
                      />
                    )}
                  </td>
                  <td>
                    {editable && (
                      <>
                        <button type="button" onClick={() => moveEntry(index, -1)} disabled={index === 0}>
                          Move up
                        </button>
                        <button
                          type="button"
                          onClick={() => moveEntry(index, 1)}
                          disabled={index === form.entries.length - 1}
                        >
                          Move down
                        </button>
                        <button type="button" onClick={() => removeEntry(index)} disabled={form.entries.length <= 1}>
                          Remove entry {index + 1}
                        </button>
                      </>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {editable && (
          <button type="button" onClick={addEntry} disabled={form.entries.length >= MAX_ENTRIES}>
            Add entry
          </button>
        )}
      </fieldset>

      {editable && (
        <div style={{ marginTop: '1rem' }}>
          {saveError !== null && (
            <p role="alert" className="session-form__error">
              {saveError}
            </p>
          )}
          <ScopedButton
            requiredScope={CONFIG_WRITE_SCOPE}
            onClick={() => void handleSave()}
            busy={saving}
            busyReason="Saving this playlist revision…"
          >
            {saving ? 'Saving…' : isNew ? 'Create playlist' : 'Save playlist'}
          </ScopedButton>
        </div>
      )}

      {!isNew && state.kind === 'loaded' && (
        <>
          <p className="panel" role="status">
            Active revision {state.config.revision}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
          </p>
          {state.revisions.length > 0 && (
            <>
              <h3 className="panel__title">Revision history</h3>
              <div className="table-scroll">
                <table className="config-table" aria-label="Revision history">
                  <thead>
                    <tr>
                      <th scope="col">Revision</th>
                      <th scope="col">Active</th>
                      <th scope="col">Created at</th>
                      <th scope="col">Created by</th>
                    </tr>
                  </thead>
                  <tbody>
                    {state.revisions.map((rev) => (
                      <tr key={rev.revision}>
                        <th scope="row">{rev.revision}</th>
                        <td>{rev.active ? 'active' : ''}</td>
                        <td>{formatAbsolute(rev.createdAt)}</td>
                        <td>{rev.createdByPrincipalName ?? '-'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </>
          )}
        </>
      )}
    </div>
  )
}
