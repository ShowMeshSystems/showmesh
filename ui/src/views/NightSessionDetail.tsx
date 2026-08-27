import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  getNightSessionConfig,
  getNightSessionConfigRevision,
  getNightSessionConfigRevisions,
  putNightSessionConfig,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { ShowSelect } from '../components/ShowSelect'
import type {
  ConfigNightSession,
  ConfigNightSessionBackgroundAudioItem,
  ConfigNightSessionCueWrite,
  ConfigNightSessionWrite,
  NightSessionConfigResponse,
} from '../app/types'

// Track F seam F1 (RESTING-MODE.md, ADR-038, ADR-039): night.session
// authoring, on ShowDetail.tsx/MacroDetail.tsx's shared precedent
// ("server validates, this only mirrors" — never substitutes its own
// judgement for a PUT rejection). Three keys the schema names explicitly
// as rejected if present — siteControl, interlocks, and any
// calendar/duration-named key — are never offered here at all, matching
// this seam's own "reduced" lesson from MacroDetail.tsx: do not let an
// operator pick something the server will only refuse.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type CueRole = ConfigNightSessionCueWrite['role']
type CueOnFailure = NonNullable<ConfigNightSessionCueWrite['onFailure']>
const CUE_ROLES: CueRole[] = ['lighting', 'projection', 'audio', 'announcement', 'other']
const CUE_ON_FAILURE: CueOnFailure[] = ['continue', 'abort']

export interface CueForm {
  name: string
  role: CueRole
  action: string
  offsetMs: string
  fadeDurationMs: string
  barrier: boolean
  onFailure: CueOnFailure
}

export function newCueForm(): CueForm {
  return { name: '', role: 'lighting', action: '', offsetMs: '0', fadeDurationMs: '', barrier: false, onFailure: 'continue' }
}

function cueToForm(cue: ConfigNightSession['enterShow']['cues'][number]): CueForm {
  return {
    name: cue.name,
    role: cue.role,
    action: cue.action,
    offsetMs: String(cue.offsetMs),
    fadeDurationMs: cue.fadeDurationMs === undefined ? '' : String(cue.fadeDurationMs),
    barrier: cue.barrier,
    onFailure: cue.onFailure,
  }
}

export interface BackgroundAudioItemForm {
  itemId: string
  show: string
  sequence: string
  target: string
}

export function newBackgroundAudioItemForm(): BackgroundAudioItemForm {
  return { itemId: '', show: '', sequence: '', target: '' }
}

export interface FormState {
  show: string
  label: string
  showPlaylistFppInstanceId: string
  showPlaylistPlaylist: string
  restingFppInstanceId: string
  restingPlaylist: string
  restingEndOfNightPlaylist: string
  restingEndOfNightRepeat: boolean
  restingTimelineShow: string
  restingTimelineSequence: string
  restingTimelineTarget: string
  backgroundAudioEnabled: boolean
  backgroundAudioItems: BackgroundAudioItemForm[]
  backgroundAudioRepeat: 'none' | 'item' | 'playlist'
  backgroundAudioResume: 'resume' | 'restart'
  backgroundAudioItemTransition: 'sequential' | 'gapless' | 'crossfade'
  backgroundAudioCrossfadeMs: string
  backgroundAudioMaxGainDb: string
  enterShowCues: CueForm[]
  enterShowBlackoutHoldMs: string
  enterRestingCues: CueForm[]
  enterRestingBlackoutAfterShowMs: string
}

export function emptyForm(): FormState {
  return {
    show: '',
    label: '',
    showPlaylistFppInstanceId: '',
    showPlaylistPlaylist: '',
    restingFppInstanceId: '',
    restingPlaylist: '',
    restingEndOfNightPlaylist: '',
    restingEndOfNightRepeat: false,
    restingTimelineShow: '',
    restingTimelineSequence: '',
    restingTimelineTarget: '',
    backgroundAudioEnabled: false,
    backgroundAudioItems: [newBackgroundAudioItemForm()],
    backgroundAudioRepeat: 'none',
    backgroundAudioResume: 'resume',
    backgroundAudioItemTransition: 'sequential',
    backgroundAudioCrossfadeMs: '',
    backgroundAudioMaxGainDb: '0',
    enterShowCues: [],
    enterShowBlackoutHoldMs: '0',
    enterRestingCues: [],
    enterRestingBlackoutAfterShowMs: '0',
  }
}

function formFromPayload(payload: ConfigNightSession): FormState {
  return {
    show: payload.show,
    label: payload.label,
    showPlaylistFppInstanceId: payload.showPlaylist.fppInstanceId,
    showPlaylistPlaylist: payload.showPlaylist.playlist,
    restingFppInstanceId: payload.resting.fppInstanceId,
    restingPlaylist: payload.resting.playlist,
    restingEndOfNightPlaylist: payload.resting.endOfNightPlaylist,
    restingEndOfNightRepeat: payload.resting.endOfNightRepeat,
    restingTimelineShow: payload.resting.timelineAsset.show,
    restingTimelineSequence: payload.resting.timelineAsset.sequence,
    restingTimelineTarget: payload.resting.timelineAsset.target,
    backgroundAudioEnabled: payload.resting.backgroundAudio !== undefined,
    backgroundAudioItems:
      payload.resting.backgroundAudio !== undefined
        ? payload.resting.backgroundAudio.items.map((item: ConfigNightSessionBackgroundAudioItem) => ({ ...item }))
        : [newBackgroundAudioItemForm()],
    backgroundAudioRepeat: payload.resting.backgroundAudio?.repeat ?? 'none',
    backgroundAudioResume: payload.resting.backgroundAudio?.resume ?? 'resume',
    backgroundAudioItemTransition: payload.resting.backgroundAudio?.itemTransition ?? 'sequential',
    backgroundAudioCrossfadeMs:
      payload.resting.backgroundAudio?.crossfadeMs === undefined ? '' : String(payload.resting.backgroundAudio.crossfadeMs),
    backgroundAudioMaxGainDb:
      payload.resting.backgroundAudio === undefined ? '0' : String(payload.resting.backgroundAudio.maxGainDb),
    enterShowCues: payload.enterShow.cues.map(cueToForm),
    enterShowBlackoutHoldMs: String(payload.enterShow.blackoutHoldMs),
    enterRestingCues: payload.enterResting.cues.map(cueToForm),
    enterRestingBlackoutAfterShowMs: String(payload.enterResting.blackoutAfterShowMs),
  }
}

export function parseNonNegativeInt(value: string, fieldLabel: string): { value: number } | { error: string } {
  const trimmed = value.trim()
  if (trimmed === '') return { error: `${fieldLabel} is required.` }
  const n = Number(trimmed)
  if (!Number.isInteger(n) || n < 0) return { error: `${fieldLabel} must be a whole number, zero or greater.` }
  return { value: n }
}

export function buildCues(cues: CueForm[], label: string): { cues: ConfigNightSessionCueWrite[] } | { error: string } {
  const built: ConfigNightSessionCueWrite[] = []
  for (const [index, cue] of cues.entries()) {
    if (cue.name.trim() === '') return { error: `${label} cue ${index + 1} needs a name.` }
    if (cue.action.trim() === '') return { error: `${label} cue "${cue.name.trim()}" needs an action.` }
    const offsetTrimmed = cue.offsetMs.trim()
    if (offsetTrimmed === '') return { error: `${label} cue "${cue.name.trim()}" needs an offset (may be negative).` }
    const offsetMs = Number(offsetTrimmed)
    if (!Number.isInteger(offsetMs)) {
      return { error: `${label} cue "${cue.name.trim()}"'s offset must be a whole number of milliseconds.` }
    }
    let fadeDurationMs: number | undefined
    if (cue.fadeDurationMs.trim() !== '') {
      const parsed = parseNonNegativeInt(cue.fadeDurationMs, `${label} cue "${cue.name.trim()}"'s fade duration`)
      if ('error' in parsed) return parsed
      fadeDurationMs = parsed.value
    }
    built.push({
      name: cue.name.trim(),
      role: cue.role,
      action: cue.action.trim(),
      offsetMs,
      ...(fadeDurationMs === undefined ? {} : { fadeDurationMs }),
      barrier: cue.barrier,
      onFailure: cue.onFailure,
    })
  }
  return { cues: built }
}

export function buildPayload(form: FormState): { payload: ConfigNightSessionWrite } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.label.trim() === '') return { error: 'Label is required.' }
  if (form.showPlaylistFppInstanceId.trim() === '') return { error: 'The show playlist needs an FPP instance.' }
  if (form.showPlaylistPlaylist.trim() === '') return { error: 'The show playlist needs a playlist name.' }
  if (form.restingFppInstanceId.trim() === '') return { error: 'Resting needs an FPP instance.' }
  if (form.restingPlaylist.trim() === '') return { error: 'Resting needs a playlist name.' }
  if (form.restingTimelineShow.trim() === '' || form.restingTimelineSequence.trim() === '' || form.restingTimelineTarget.trim() === '') {
    return { error: 'The resting timeline asset needs show, sequence, and target all filled in.' }
  }

  const blackoutHold = parseNonNegativeInt(form.enterShowBlackoutHoldMs, 'enterShow blackout hold')
  if ('error' in blackoutHold) return blackoutHold
  const blackoutAfterShow = parseNonNegativeInt(form.enterRestingBlackoutAfterShowMs, 'enterResting blackout-after-show')
  if ('error' in blackoutAfterShow) return blackoutAfterShow

  const enterShowCues = buildCues(form.enterShowCues, 'enterShow')
  if ('error' in enterShowCues) return enterShowCues
  const enterRestingCues = buildCues(form.enterRestingCues, 'enterResting')
  if ('error' in enterRestingCues) return enterRestingCues

  let backgroundAudio: ConfigNightSessionWrite['resting']['backgroundAudio']
  if (form.backgroundAudioEnabled) {
    const items: ConfigNightSessionBackgroundAudioItem[] = []
    const seenIds = new Set<string>()
    for (const [index, item] of form.backgroundAudioItems.entries()) {
      if (item.itemId.trim() === '') return { error: `Background audio item ${index + 1} needs an item id.` }
      if (seenIds.has(item.itemId.trim())) return { error: `Background audio item id "${item.itemId.trim()}" is used more than once.` }
      seenIds.add(item.itemId.trim())
      if (item.show.trim() === '' || item.sequence.trim() === '' || item.target.trim() === '') {
        return { error: `Background audio item "${item.itemId.trim()}" needs show, sequence, and target all filled in.` }
      }
      items.push({ itemId: item.itemId.trim(), show: item.show.trim(), sequence: item.sequence.trim(), target: item.target.trim() })
    }
    if (items.length === 0) return { error: 'Background audio needs at least one item, or turn it off.' }
    const maxGainDbTrimmed = form.backgroundAudioMaxGainDb.trim()
    const maxGainDb = Number(maxGainDbTrimmed)
    // Suspicion resolved: `Number('-Infinity')` is `-Infinity`, which is
    // neither NaN nor greater than 0, so a NaN-only check let it through.
    // `JSON.stringify(-Infinity)` serializes to `null`, and the wire
    // schema's `maxGainDb` is `type: number` (not nullable), so this
    // would have come back from the coordinator as a type error rather
    // than the range error an operator typing garbage here actually
    // needs to see. `Number.isFinite` excludes NaN AND both infinities.
    if (maxGainDbTrimmed === '' || !Number.isFinite(maxGainDb) || maxGainDb > 0) {
      return { error: 'Background audio max gain must be a number, 0 dB or lower.' }
    }
    let crossfadeMs: number | undefined
    if (form.backgroundAudioItemTransition === 'crossfade') {
      const parsed = parseNonNegativeInt(form.backgroundAudioCrossfadeMs, 'Background audio crossfade duration')
      if ('error' in parsed) return parsed
      crossfadeMs = parsed.value
    } else if (form.backgroundAudioCrossfadeMs.trim() !== '') {
      return { error: 'Crossfade duration only applies when item transition is "crossfade". Clear it or pick that transition.' }
    }
    backgroundAudio = {
      items,
      repeat: form.backgroundAudioRepeat,
      resume: form.backgroundAudioResume,
      itemTransition: form.backgroundAudioItemTransition,
      ...(crossfadeMs === undefined ? {} : { crossfadeMs }),
      maxGainDb,
    }
  }

  const payload: ConfigNightSessionWrite = {
    show: form.show.trim(),
    label: form.label.trim(),
    showPlaylist: {
      fppInstanceId: form.showPlaylistFppInstanceId.trim(),
      playlist: form.showPlaylistPlaylist.trim(),
    },
    resting: {
      fppInstanceId: form.restingFppInstanceId.trim(),
      playlist: form.restingPlaylist.trim(),
      ...(form.restingEndOfNightPlaylist.trim() === '' ? {} : { endOfNightPlaylist: form.restingEndOfNightPlaylist.trim() }),
      endOfNightRepeat: form.restingEndOfNightRepeat,
      timelineAsset: {
        show: form.restingTimelineShow.trim(),
        sequence: form.restingTimelineSequence.trim(),
        target: form.restingTimelineTarget.trim(),
      },
      ...(backgroundAudio === undefined ? {} : { backgroundAudio }),
    },
    enterShow: { cues: enterShowCues.cues, blackoutHoldMs: blackoutHold.value },
    enterResting: { cues: enterRestingCues.cues, blackoutAfterShowMs: blackoutAfterShow.value },
  }
  return { payload }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: NightSessionConfigResponse; revisions: ConfigRevisionMeta[] }

export interface NightSessionDetailProps {
  isNew?: boolean
}

export function NightSessionDetail({ isNew = false }: NightSessionDetailProps) {
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

  const [revisionView, setRevisionView] = useState<
    { kind: 'idle' } | { kind: 'loading'; revision: number } | { kind: 'error'; revision: number; message: string } | { kind: 'loaded'; revision: number; config: NightSessionConfigResponse }
  >({ kind: 'idle' })

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getNightSessionConfig(existingId), getNightSessionConfigRevisions(existingId)])
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

  function updateCue(list: 'enterShowCues' | 'enterRestingCues', index: number, patch: Partial<CueForm>): void {
    setForm((f) => ({ ...f, [list]: f[list].map((c, i) => (i === index ? { ...c, ...patch } : c)) }))
  }

  function addCue(list: 'enterShowCues' | 'enterRestingCues'): void {
    setForm((f) => ({ ...f, [list]: [...f[list], newCueForm()] }))
  }

  function removeCue(list: 'enterShowCues' | 'enterRestingCues', index: number): void {
    setForm((f) => ({ ...f, [list]: f[list].filter((_, i) => i !== index) }))
  }

  function updateBackgroundAudioItem(index: number, patch: Partial<BackgroundAudioItemForm>): void {
    setForm((f) => ({
      ...f,
      backgroundAudioItems: f.backgroundAudioItems.map((item, i) => (i === index ? { ...item, ...patch } : item)),
    }))
  }

  function addBackgroundAudioItem(): void {
    setForm((f) => ({ ...f, backgroundAudioItems: [...f.backgroundAudioItems, newBackgroundAudioItemForm()] }))
  }

  function removeBackgroundAudioItem(index: number): void {
    setForm((f) => ({ ...f, backgroundAudioItems: f.backgroundAudioItems.filter((_, i) => i !== index) }))
  }

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A night session id is required.')
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
      const resp = await putNightSessionConfig(id, built.payload)
      if (isNew) {
        navigate(`/config/night.session/${encodeURIComponent(id)}`)
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      // Review finding 10: the server resolves defaults on write
      // (endOfNightPlaylist, endOfNightRepeat, every cue's barrier/
      // onFailure) that this form's own optimistic `built.payload` never
      // set explicitly — re-seeding from the response is what makes the
      // resolved values visible immediately rather than only after a
      // full page reload re-fetches this same object.
      setForm(formFromPayload(resp.payload))
      const revisionsResp = await getNightSessionConfigRevisions(id)
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, revisions: revisionsResp.revisions } : prev))
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  async function viewRevision(revision: number): Promise<void> {
    if (existingId === undefined) return
    setRevisionView({ kind: 'loading', revision })
    try {
      const config = await getNightSessionConfigRevision(existingId, revision)
      setRevisionView({ kind: 'loaded', revision, config })
    } catch (err) {
      setRevisionView({ kind: 'error', revision, message: describeApiError(err) })
    }
  }

  const pageGate = isNew ? writeGate : readGate
  if (!pageGate.allowed) {
    return (
      <div>
        <h2 className="panel__title">{isNew ? 'New night session' : 'Night session configuration'}</h2>
        <p className="panel panel--error" role="status">
          {pageGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading night session configuration…</p>
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
    <div className="operator-page">
      <header className="operator-page__header">
        <div>
          <h1 className="operator-page__title">{isNew ? 'New Show Night' : form.label || existingId}</h1>
          <p className="operator-page__lede text-muted">
            Edit the authored Show Night definition. Changes create a revision; the live controller pins one revision for its run.
          </p>
        </div>
        <Link className="button" to="/night">View live Show Night</Link>
      </header>
      <p className="text-muted">
        The authored definition a night session pins. FPP alone authorizes and
        schedules a night session; this form never accepts a calendar field or a rest-duration
        field, and the server rejects one outright if it ever appears. <code>siteControl</code>{' '}
        and <code>interlocks</code> are specified but not implemented in this seam, so this form
        never offers them either.
      </p>

      {!editable && (
        <p className="text-muted" role="status">
          Viewing only: editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="form-field">
          Night session id
          <input type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable}>
        <div className="form-field">
          <ShowSelect label="Show" value={form.show} onChange={(show) => setForm({ ...form, show })} />
        </div>
        <label className="form-field">
          Label
          <input type="text" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} />
        </label>

        <h3 className="panel__title">Show playlist</h3>
        <p className="text-muted">Names an FPP-owned playlist: referenced, never created.</p>
        <label className="form-field">
          FPP instance id
          <input
            type="text"
            value={form.showPlaylistFppInstanceId}
            onChange={(e) => setForm({ ...form, showPlaylistFppInstanceId: e.target.value })}
          />
        </label>
        <label className="form-field">
          Playlist
          <input
            type="text"
            value={form.showPlaylistPlaylist}
            onChange={(e) => setForm({ ...form, showPlaylistPlaylist: e.target.value })}
          />
        </label>

        <h3 className="panel__title">Resting</h3>
        <label className="form-field">
          FPP instance id
          <input
            type="text"
            value={form.restingFppInstanceId}
            onChange={(e) => setForm({ ...form, restingFppInstanceId: e.target.value })}
          />
        </label>
        <label className="form-field">
          Playlist
          <input type="text" value={form.restingPlaylist} onChange={(e) => setForm({ ...form, restingPlaylist: e.target.value })} />
        </label>
        <label className="form-field">
          End-of-night playlist (blank defaults to the resting playlist above)
          <input
            type="text"
            value={form.restingEndOfNightPlaylist}
            onChange={(e) => setForm({ ...form, restingEndOfNightPlaylist: e.target.value })}
          />
        </label>
        <label className="form-field form-field--checkbox">
          <input
            type="checkbox"
            checked={form.restingEndOfNightRepeat}
            onChange={(e) => setForm({ ...form, restingEndOfNightRepeat: e.target.checked })}
          />
          Repeat the end-of-night playlist
        </label>

        <h4 className="panel__title">Resting timeline asset</h4>
        <div className="form-field">
          <ShowSelect
            label="Show"
            value={form.restingTimelineShow}
            onChange={(restingTimelineShow) => setForm({ ...form, restingTimelineShow })}
          />
        </div>
        <label className="form-field">
          Sequence
          <input
            type="text"
            value={form.restingTimelineSequence}
            onChange={(e) => setForm({ ...form, restingTimelineSequence: e.target.value })}
          />
        </label>
        <label className="form-field">
          Target node
          <input
            type="text"
            value={form.restingTimelineTarget}
            onChange={(e) => setForm({ ...form, restingTimelineTarget: e.target.value })}
          />
        </label>

        <h4 className="panel__title">Background audio</h4>
        <label className="form-field form-field--checkbox">
          <input
            type="checkbox"
            checked={form.backgroundAudioEnabled}
            onChange={(e) => setForm({ ...form, backgroundAudioEnabled: e.target.checked })}
          />
          Configure background audio (its absence is valid and is not degraded)
        </label>
        {form.backgroundAudioEnabled && (
          <div className="panel">
            {form.backgroundAudioItems.map((item, i) => (
              <div key={i} style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
                <input
                  type="text"
                  placeholder="item id"
                  value={item.itemId}
                  onChange={(e) => updateBackgroundAudioItem(i, { itemId: e.target.value })}
                />
                <ShowSelect
                  ariaLabel="Show"
                  value={item.show}
                  onChange={(show) => updateBackgroundAudioItem(i, { show })}
                />
                <input
                  type="text"
                  placeholder="sequence"
                  value={item.sequence}
                  onChange={(e) => updateBackgroundAudioItem(i, { sequence: e.target.value })}
                />
                <input
                  type="text"
                  placeholder="target node"
                  value={item.target}
                  onChange={(e) => updateBackgroundAudioItem(i, { target: e.target.value })}
                />
                {/* Review finding 12: `editable` here encodes only whether this
                    principal holds config:write (`const editable =
                    writeGate.allowed`) — never a structural reason like
                    "this is a read-only historical revision". ADR-024
                    decision 12 requires that case to render disabled with
                    the reason, not be omitted, so this is a ScopedButton
                    (always rendered) rather than an `editable &&` guard. */}
                <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} onClick={() => removeBackgroundAudioItem(i)}>
                  Remove
                </ScopedButton>
              </div>
            ))}
            {/* Review finding 12: see the identical comment on the Remove
                button above. */}
            <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} onClick={addBackgroundAudioItem}>
              Add background audio item
            </ScopedButton>
            <label className="form-field">
              Repeat
              <select
                value={form.backgroundAudioRepeat}
                onChange={(e) => setForm({ ...form, backgroundAudioRepeat: e.target.value as FormState['backgroundAudioRepeat'] })}
              >
                <option value="none">none</option>
                <option value="item">item</option>
                <option value="playlist">playlist</option>
              </select>
            </label>
            <label className="form-field">
              Resume
              <select
                value={form.backgroundAudioResume}
                onChange={(e) => setForm({ ...form, backgroundAudioResume: e.target.value as FormState['backgroundAudioResume'] })}
              >
                <option value="resume">resume</option>
                <option value="restart">restart</option>
              </select>
            </label>
            <label className="form-field">
              Item transition
              <select
                value={form.backgroundAudioItemTransition}
                onChange={(e) =>
                  setForm({ ...form, backgroundAudioItemTransition: e.target.value as FormState['backgroundAudioItemTransition'] })
                }
              >
                <option value="sequential">sequential</option>
                <option value="gapless">gapless</option>
                <option value="crossfade">crossfade</option>
              </select>
            </label>
            {form.backgroundAudioItemTransition === 'crossfade' && (
              <label className="form-field">
                Crossfade duration (ms)
                <input
                  type="number"
                  value={form.backgroundAudioCrossfadeMs}
                  onChange={(e) => setForm({ ...form, backgroundAudioCrossfadeMs: e.target.value })}
                />
              </label>
            )}
            <label className="form-field">
              Max gain (dB, 0 or lower)
              <input
                type="number"
                value={form.backgroundAudioMaxGainDb}
                onChange={(e) => setForm({ ...form, backgroundAudioMaxGainDb: e.target.value })}
              />
            </label>
          </div>
        )}

        <CueListEditor
          heading="Enter-show Transition Steps"
          cues={form.enterShowCues}
          onUpdate={(i, patch) => updateCue('enterShowCues', i, patch)}
          onAdd={() => addCue('enterShowCues')}
          onRemove={(i) => removeCue('enterShowCues', i)}
        />
        <label className="form-field">
          Blackout hold (ms)
          <input
            type="number"
            value={form.enterShowBlackoutHoldMs}
            onChange={(e) => setForm({ ...form, enterShowBlackoutHoldMs: e.target.value })}
          />
        </label>

        <CueListEditor
          heading="Enter-resting Transition Steps"
          cues={form.enterRestingCues}
          onUpdate={(i, patch) => updateCue('enterRestingCues', i, patch)}
          onAdd={() => addCue('enterRestingCues')}
          onRemove={(i) => removeCue('enterRestingCues', i)}
        />
        <label className="form-field">
          Blackout-after-show (ms)
          <input
            type="number"
            value={form.enterRestingBlackoutAfterShowMs}
            onChange={(e) => setForm({ ...form, enterRestingBlackoutAfterShowMs: e.target.value })}
          />
        </label>
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
            busyReason="Saving this night session revision…"
          >
            {saving ? 'Saving…' : isNew ? 'Create night session' : 'Save night session'}
          </ScopedButton>
        </div>
      )}

      {/* Status: what the coordinator currently reports about this night
          session's stored configuration, kept apart from the authoring
          fieldset above. The active-revision line stays outside any
          <details>: it is short and always relevant, never a candidate
          for hiding. Revision history is a long, rarely-consulted list,
          a reasonable thing to start collapsed (per this seam's own
          rule, nothing here can be stale/failed evidence; it is a plain
          fetched list, not an EvidenceValue). */}
      {!isNew && state.kind === 'loaded' && (
        <section aria-label="Status">
          <p className="panel" role="status">
            Active revision {state.config.revision}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
          </p>
          {state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Revision</th>
                    <th>Active</th>
                    <th>Created at</th>
                    <th>Created by</th>
                    <th></th>
                  </tr>
                </thead>
                <tbody>
                  {state.revisions.map((rev) => (
                    <tr key={rev.revision}>
                      <td>{rev.revision}</td>
                      <td>{rev.active ? 'active' : ''}</td>
                      <td>{formatAbsolute(rev.createdAt)}</td>
                      <td>{rev.createdByPrincipalName ?? '-'}</td>
                      <td>
                        <button type="button" onClick={() => void viewRevision(rev.revision)}>
                          View payload
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              {revisionView.kind === 'loading' && <p className="text-muted">Loading revision {revisionView.revision}…</p>}
              {revisionView.kind === 'error' && (
                <p className="panel panel--error" role="alert">
                  {revisionView.message}
                </p>
              )}
              {revisionView.kind === 'loaded' && (
                <div className="panel">
                  <h4 className="panel__title">Revision {revisionView.revision} (immutable, may not be the active one)</h4>
                  <pre className="table-scroll">{JSON.stringify(revisionView.config.payload, null, 2)}</pre>
                </div>
              )}
            </details>
          )}
        </section>
      )}

      <p className="text-muted">
      See <Link to="/config/night.session.active">the active Show Night</Link> to change which
        session the coordinator is currently running.
      </p>
    </div>
  )
}

function CueListEditor({
  heading,
  cues,
  onUpdate,
  onAdd,
  onRemove,
}: {
  heading: string
  cues: CueForm[]
  onUpdate: (index: number, patch: Partial<CueForm>) => void
  onAdd: () => void
  onRemove: (index: number) => void
}) {
  return (
    <div>
      <h3 className="panel__title">{heading}</h3>
      {cues.length === 0 && <p className="text-muted">No Transition Steps yet.</p>}
      {cues.map((cue, i) => (
        <div key={i} className="panel" style={{ display: 'flex', gap: '0.5rem', flexWrap: 'wrap', marginBottom: '0.5rem' }}>
          <input type="text" placeholder="name" value={cue.name} onChange={(e) => onUpdate(i, { name: e.target.value })} />
          <select value={cue.role} onChange={(e) => onUpdate(i, { role: e.target.value as CueRole })}>
            {CUE_ROLES.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
          <input type="text" placeholder="action id" value={cue.action} onChange={(e) => onUpdate(i, { action: e.target.value })} />
          <input
            type="number"
            placeholder="offset ms (signed)"
            value={cue.offsetMs}
            onChange={(e) => onUpdate(i, { offsetMs: e.target.value })}
          />
          <input
            type="number"
            placeholder="fade duration ms (optional)"
            value={cue.fadeDurationMs}
            onChange={(e) => onUpdate(i, { fadeDurationMs: e.target.value })}
          />
          <label className="form-field--checkbox">
            <input type="checkbox" checked={cue.barrier} onChange={(e) => onUpdate(i, { barrier: e.target.checked })} />
            Barrier
          </label>
          <select value={cue.onFailure} onChange={(e) => onUpdate(i, { onFailure: e.target.value as CueOnFailure })}>
            {CUE_ON_FAILURE.map((f) => (
              <option key={f} value={f}>
                {f}
              </option>
            ))}
          </select>
          {/* Review finding 12: `editable` (the caller's writeGate.allowed)
              is a missing-scope reason, not a structural one — this is a
              ScopedButton, always rendered, rather than an omitted
              control (ADR-024 decision 12). */}
          <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} onClick={() => onRemove(i)}>
            Remove
          </ScopedButton>
        </div>
      ))}
      <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} onClick={onAdd}>
        Add Transition Step
      </ScopedButton>
    </div>
  )
}
