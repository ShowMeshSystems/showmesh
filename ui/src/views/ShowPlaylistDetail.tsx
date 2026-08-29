import { useEffect, useRef, useState, type DragEvent, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
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
import { PlannedFeature } from '../components/SharedLayouts'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showPlaylistPath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type {
  ConfigShowPlaylist,
  PlaylistMismatchPolicy,
  PlaylistRunner,
  PlaylistShowmeshAudioRepeat,
  ShowPlaylistConfigResponse,
} from '../app/types'

// Show Authoring.dc.html: a Playlist's runner decides which authority
// model applies. fpp mirrors an imported order it does not own (entry
// position IS row order - no typed number field, no reordering here);
// showmesh-audio is authored and reorderable, with its own repeat and
// on-mismatch controls. ADR-030 posture unchanged from the pre-overhaul
// form: every check here also exists server-side; a refused PUT renders
// through describeApiError verbatim, never a client-invented message.
// This is still a FULL REPLACEMENT form: `entries` is sent as exactly
// what the table holds.
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

function emptyForm(show: string, runner: PlaylistRunner | ''): FormState {
  return {
    show,
    name: '',
    runner,
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
 * /config/show.playlist/{id}).
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
  // App.tsx's real route is `shows/:showId/playlists/:id`; `playlistId`
  // is kept as a fallback so an older link/test using that name still
  // resolves.
  const params = useParams<{ showId: string; playlistId?: string; id?: string }>()
  const showId = params.showId ?? ''
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : (params.playlistId ?? params.id)
  const workspaceData = useShowWorkspaceData(showId)

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm(showId, ''))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  // Cue id -> label, resolved from GET /config/show.cue?show=<id>, the
  // same list summary the Cues tab renders. Used to build the bound-cue
  // picker: a real, API-backed list, never a fake empty <select>.
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
    if (isNew) setForm((f) => ({ ...f, show: showId }))
  }, [isNew, showId])

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

  // Native drag-and-drop reorder for the showmesh-audio entries table,
  // alongside the Move up/down buttons kept for keyboard/no-pointer
  // access (UI-DESIGN-GUIDE.md section 6: reorderable lists get a ⠿
  // handle with cursor:grab and "Drag to reorder" once in the footer).
  const dragIndex = useRef<number | null>(null)
  function handleDragStart(index: number): void {
    dragIndex.current = index
  }
  function handleDragOver(e: DragEvent): void {
    e.preventDefault()
  }
  function handleDrop(index: number): void {
    const from = dragIndex.current
    dragIndex.current = null
    if (from === null || from === index) return
    setForm((f) => {
      const entries = f.entries.slice()
      const [moved] = entries.splice(from, 1)
      if (moved === undefined) return f
      entries.splice(index, 0, moved)
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
        navigate(showPlaylistPath(showId, id))
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
  const editable = writeGate.allowed
  const hasCueOptions = Object.keys(cueLabels).length > 0

  function renderCueField(entry: EntryForm, index: number): ReactNode {
    if (hasCueOptions) {
      return (
        <select
          aria-label={`Entry ${index + 1} cue`}
          value={entry.cue}
          disabled={!editable}
          onChange={(e) => updateEntry(index, { cue: e.target.value })}
        >
          <option value="">No cue bound</option>
          {entry.cue.trim() !== '' && cueLabels[entry.cue.trim()] === undefined && (
            <option value={entry.cue}>{entry.cue} (not in this show&rsquo;s cue list)</option>
          )}
          {Object.entries(cueLabels).map(([id, label]) => (
            <option key={id} value={id}>
              {label}
            </option>
          ))}
        </select>
      )
    }
    return (
      <input
        aria-label={`Entry ${index + 1} cue`}
        type="text"
        value={entry.cue}
        disabled={!editable}
        placeholder="cue id"
        onChange={(e) => updateEntry(index, { cue: e.target.value })}
      />
    )
  }

  function verdictFor(entry: EntryForm): ReactNode {
    return entry.cue.trim() !== '' ? (
      <span className="t-meta verdict-bound">✓ Bound</span>
    ) : (
      <span className="t-meta verdict-unbound">Unbound</span>
    )
  }

  const content = (() => {
    if (!pageGate.allowed) {
      return (
        <p className="ruled-strip ruled-strip--no-permission" role="status">
          <span className="ruled-strip__state t-meta">No permission</span>
          <span className="ruled-strip__explanation">{pageGate.reason}</span>
        </p>
      )
    }
    if (!isNew && state.kind === 'loading') {
      return (
        <p className="ruled-strip ruled-strip--loading" role="status">
          <span className="ruled-strip__state t-meta">Loading</span>
          <span className="ruled-strip__explanation">Reading this playlist.</span>
        </p>
      )
    }
    if (!isNew && state.kind === 'error') {
      return (
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">{state.message}</span>
        </p>
      )
    }

    return (
      <div>
        {!editable && (
          <p className="t-small shows-muted" role="status">
            Viewing only: editing requires the <code>config:write</code> scope.
          </p>
        )}

        {isNew && (
          <label className="field">
            <span className="field__label">Playlist id</span>
            <input className="field__input" type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
          </label>
        )}

        <fieldset disabled={!editable} style={{ border: 0, padding: 0, margin: 0 }}>
          <label className="field">
            <span className="field__label">Name</span>
            <input className="field__input" type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </label>

          <div className="playlist-editing-header">
            <span className="t-meta shows-faint">Runner</span>
            <label className="field" style={{ maxWidth: '20rem' }}>
              <select
                aria-label="Runner"
                value={form.runner}
                onChange={(e) =>
                  setForm({
                    ...form,
                    runner: e.target.value as PlaylistRunner,
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
          </div>

          {form.runner === 'fpp' && (
            <>
              <div className="ruled-strip">
                <span className="ruled-strip__state t-meta">Authority</span>
                <span className="ruled-strip__explanation">
                  <strong className="t-subhead" style={{ display: 'block' }}>
                    FPP decides what plays next
                  </strong>
                  You cannot reorder this list here: it mirrors FPP&rsquo;s playlist. Entry position
                  comes from row order, not a typed number field. Your job is to bind each imported
                  entry to a cue.
                </span>
              </div>

              <div className="grid-two" style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 14, marginTop: 14 }}>
                <label className="field">
                  <span className="field__label">FPP instance UUID</span>
                  <input
                    className="field__input"
                    type="text"
                    value={form.fppInstanceUuid}
                    onChange={(e) => setForm({ ...form, fppInstanceUuid: e.target.value })}
                  />
                </label>
                <label className="field">
                  <span className="field__label">FPP playlist name</span>
                  <input
                    className="field__input"
                    type="text"
                    value={form.fppPlaylistName}
                    onChange={(e) => setForm({ ...form, fppPlaylistName: e.target.value })}
                  />
                </label>
              </div>
              <label className="field" style={{ marginTop: 14 }}>
                <span className="field__label">FPP playlist hash (the imported canonical hash, 64 lowercase hex characters)</span>
                <input
                  className="field__input field__input--data"
                  type="text"
                  value={form.fppPlaylistHash}
                  onChange={(e) => setForm({ ...form, fppPlaylistHash: e.target.value })}
                />
                <span className="field__help">
                  Derived evidence, not a field to guess at: this is the hash reported at import time.
                  This build has no separate re-import evidence field to diff it against, so a
                  hash-changed verdict is not shown here.
                </span>
              </label>
              <PlannedFeature
                title="Re-import from FPP"
                why="No re-import endpoint exists in this API for show.playlist: there is nothing to POST to that would re-read FPP's current definition and recompute the canonical hash. Edit the FPP fields and entries above directly instead."
                preview={<button type="button" className="btn btn--secondary">Re-import</button>}
              />

              <h3 className="t-subhead" style={{ marginTop: 20 }}>
                Entries
              </h3>
              <div className="table-wrap card" style={{ marginTop: 10 }}>
                <table className="table table--full" aria-label="Entries">
                  <thead>
                    <tr>
                      <th scope="col">FPP entry</th>
                      <th scope="col">Bound cue</th>
                      <th scope="col">Verdict</th>
                      {editable && <th scope="col">Actions</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {form.entries.map((entry, index) => (
                      <tr key={index}>
                        <td>
                          <input
                            aria-label={`Entry ${index + 1} id`}
                            type="text"
                            value={entry.id}
                            disabled={!editable}
                            onChange={(e) => updateEntry(index, { id: e.target.value })}
                          />
                          <label className="field__check" style={{ marginTop: 6 }}>
                            <input
                              type="checkbox"
                              aria-label={`Entry ${index + 1} FPP binding enabled`}
                              checked={entry.fppEnabled}
                              disabled={!editable}
                              onChange={(e) => updateEntry(index, { fppEnabled: e.target.checked })}
                            />
                            FPP bound
                          </label>
                          {entry.fppEnabled && (
                            <div style={{ display: 'grid', gap: 6, marginTop: 6 }}>
                              <input
                                aria-label={`Entry ${index + 1} FPP section`}
                                type="text"
                                placeholder="section"
                                value={entry.fppSection}
                                disabled={!editable}
                                onChange={(e) => updateEntry(index, { fppSection: e.target.value })}
                              />
                              <input
                                aria-label={`Entry ${index + 1} FPP position`}
                                type="number"
                                min={0}
                                placeholder="position"
                                value={entry.fppPosition}
                                disabled={!editable}
                                onChange={(e) => updateEntry(index, { fppPosition: e.target.value })}
                              />
                            </div>
                          )}
                        </td>
                        <td>{renderCueField(entry, index)}</td>
                        <td>{verdictFor(entry)}</td>
                        {editable && (
                          <td>
                            <button type="button" onClick={() => removeEntry(index)} disabled={form.entries.length <= 1}>
                              Remove
                            </button>
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
                {editable && (
                  <div className="table__footer-note">
                    <button type="button" onClick={addEntry} disabled={form.entries.length >= MAX_ENTRIES}>
                      Add entry
                    </button>
                  </div>
                )}
              </div>

              <div className="ruled-strip" style={{ marginTop: 20 }}>
                <span className="ruled-strip__state t-meta">On mismatch</span>
                <span className="ruled-strip__explanation">
                  <div className="segmented" role="group" aria-label="On mismatch">
                    {MISMATCH_POLICIES.map((p) => (
                      <button
                        key={p}
                        type="button"
                        className="segmented__option"
                        aria-pressed={form.mismatchPolicy === p}
                        disabled={!editable}
                        onClick={() =>
                          setForm({ ...form, mismatchPolicy: p, safeCueRef: p === 'safeCue' ? form.safeCueRef : '' })
                        }
                      >
                        {p === 'hold' ? 'Hold' : p === 'blackAndSilence' ? 'Black & silence' : 'Safe cue'}
                      </button>
                    ))}
                  </div>
                  {form.mismatchPolicy === 'safeCue' && (
                    <label className="field" style={{ marginTop: 10, maxWidth: '20rem' }}>
                      <span className="field__label">Safe cue (must name a same-show cue)</span>
                      <input
                        className="field__input"
                        type="text"
                        value={form.safeCueRef}
                        onChange={(e) => setForm({ ...form, safeCueRef: e.target.value })}
                      />
                    </label>
                  )}
                  <p className="playlist-not-wired">This control is what takes effect today.</p>
                </span>
              </div>

              <PlannedFeature
                title="Mismatch policy follows Show vs Program mode"
                why="Settings › Mode carries no field wiring it to a playlist's mismatchPolicy; no endpoint here links the two, so the per-playlist control above stays the only thing that takes effect."
                preview={
                  <div className="ruled-strip">
                    <span className="ruled-strip__state t-meta">On mismatch</span>
                    <span className="ruled-strip__explanation">Follows Show / Program mode automatically.</span>
                  </div>
                }
              />
            </>
          )}

          {form.runner === 'showmesh-audio' && (
            <>
              <div className="ruled-strip">
                <span className="ruled-strip__state t-meta">Authority</span>
                <span className="ruled-strip__explanation">
                  <strong className="t-subhead" style={{ display: 'block' }}>
                    ShowMesh decides what plays next
                  </strong>
                  You author this order. ShowMesh owns progression, repeat, and the audio playhead. No
                  LTC is emitted unless a cue declares it.
                </span>
              </div>

              {/* The settings object is optional on the payload, so this has to be
                  clearable. Without it a playlist that once carried showmeshAudio
                  could never have it removed again: every other control only ever
                  sets it, and the sole workaround was switching runner away and
                  back, which silently discards mismatch policy and safe cue too. */}
              <label className="field__check" style={{ marginTop: 16 }}>
                <input
                  type="checkbox"
                  checked={form.showmeshAudioEnabled}
                  disabled={!editable}
                  onChange={(event) =>
                    setForm({ ...form, showmeshAudioEnabled: event.target.checked })
                  }
                />
                <span>Configure showmesh-audio settings</span>
              </label>
              <p className="field__help" style={{ marginTop: 4 }}>
                Cleared, this playlist is saved without a showmesh-audio settings object and the
                coordinator's own defaults apply.
              </p>

              <div style={{ display: 'flex', alignItems: 'center', gap: 14, flexWrap: 'wrap', marginTop: 16 }}>
                <span className="t-meta shows-faint">Repeat</span>
                <div className="segmented" role="group" aria-label="Repeat">
                  {REPEAT_MODES.map((r) => (
                    <button
                      key={r}
                      type="button"
                      className="segmented__option"
                      aria-pressed={form.showmeshAudioRepeat === r}
                      disabled={!editable}
                      onClick={() => setForm({ ...form, showmeshAudioEnabled: true, showmeshAudioRepeat: r })}
                    >
                      {r === 'none' ? 'None' : 'All'}
                    </button>
                  ))}
                </div>
              </div>

              <h3 className="t-subhead" style={{ marginTop: 20 }}>
                Entries
              </h3>
              <div className="table-wrap card" style={{ marginTop: 10 }}>
                <table className="table table--full" aria-label="Entries">
                  <thead>
                    <tr>
                      <th scope="col">Ord</th>
                      <th scope="col">Entry id</th>
                      <th scope="col">Cue</th>
                      {editable && <th scope="col">Actions</th>}
                    </tr>
                  </thead>
                  <tbody>
                    {form.entries.map((entry, index) => (
                      <tr
                        key={index}
                        draggable={editable}
                        onDragStart={() => handleDragStart(index)}
                        onDragOver={handleDragOver}
                        onDrop={() => handleDrop(index)}
                      >
                        <td className="t-data table__drag-handle" aria-hidden="true">
                          ⠿ {index + 1}
                        </td>
                        <td>
                          <input
                            aria-label={`Entry ${index + 1} id`}
                            type="text"
                            value={entry.id}
                            disabled={!editable}
                            onChange={(e) => updateEntry(index, { id: e.target.value })}
                          />
                        </td>
                        <td>{renderCueField(entry, index)}</td>
                        {editable && (
                          <td>
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
                              Remove
                            </button>
                          </td>
                        )}
                      </tr>
                    ))}
                  </tbody>
                </table>
                <div className="table__footer-note">
                  Drag to reorder.
                  {editable && (
                    <button type="button" onClick={addEntry} disabled={form.entries.length >= MAX_ENTRIES} style={{ marginLeft: 12 }}>
                      Add cue
                    </button>
                  )}
                </div>
              </div>
            </>
          )}
        </fieldset>

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
              busyReason="Saving this playlist revision…"
            >
              {saving ? 'Saving…' : isNew ? 'Create playlist' : 'Save playlist'}
            </ScopedButton>
          </div>
        )}

        {!isNew && state.kind === 'loaded' && (
          <>
            <p className="t-small shows-muted" role="status" style={{ marginTop: 12 }}>
              Active revision {state.config.revision}
              {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
            </p>
            {state.revisions.length > 0 && (
              <>
                <h3 className="t-subhead">Revision history</h3>
                <div className="table-wrap card">
                  <table className="table table--full" aria-label="Revision history">
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
                          <td className="t-data">{rev.revision}</td>
                          <td>{rev.active ? 'active' : ''}</td>
                          <td className="t-small">{formatAbsolute(rev.createdAt)}</td>
                          <td className="t-small">{rev.createdByPrincipalName ?? '-'}</td>
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
  })()

  return (
    <ShowWorkspaceFrame showId={showId} active="playlists" data={workspaceData}>
      {content}
    </ShowWorkspaceFrame>
  )
}
