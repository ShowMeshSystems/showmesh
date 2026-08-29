import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
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
import { PlannedFeature } from '../components/SharedLayouts'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showNightSessionPath, showNightSessionsPath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import '../styles/night.css'
import type {
  ConfigNightSession,
  ConfigNightSessionBackgroundAudioItem,
  ConfigNightSessionCueWrite,
  ConfigNightSessionWrite,
  NightSessionConfigResponse,
} from '../app/types'

// Show Night Session.dc.html's `view: 'edit'` branch: the night-session
// definition editor. On ShowDetail.tsx/MacroDetail.tsx's shared
// precedent ("server validates, this only mirrors" — never substitutes
// its own judgement for a PUT rejection). `siteControl` and
// `interlocks` are specified in the schema (both a read shape and, per
// the generated write type, a write shape) but this seam has never
// implemented the endpoint accepting them: PUT /config/night.session
// rejects either key outright if present. The mock draws authoring for
// both, so both render as PlannedFeature below rather than as working
// controls — the owner's ruling is to show the idea, loudly stamped,
// not to silently omit what the mock drew.
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

export function newBackgroundAudioItemForm(show = ''): BackgroundAudioItemForm {
  return { itemId: '', show, sequence: '', target: '' }
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

export function emptyForm(show = ''): FormState {
  return {
    show,
    label: '',
    showPlaylistFppInstanceId: '',
    showPlaylistPlaylist: '',
    restingFppInstanceId: '',
    restingPlaylist: '',
    restingEndOfNightPlaylist: '',
    restingEndOfNightRepeat: false,
    restingTimelineShow: show,
    restingTimelineSequence: '',
    restingTimelineTarget: '',
    backgroundAudioEnabled: false,
    backgroundAudioItems: [newBackgroundAudioItemForm(show)],
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
        : [newBackgroundAudioItemForm(payload.show)],
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
    if (cue.name.trim() === '') return { error: `${label} Transition Step ${index + 1} needs a name.` }
    if (cue.action.trim() === '') return { error: `${label} Transition Step "${cue.name.trim()}" needs an action.` }
    const offsetTrimmed = cue.offsetMs.trim()
    if (offsetTrimmed === '') return { error: `${label} Transition Step "${cue.name.trim()}" needs an offset (may be negative).` }
    const offsetMs = Number(offsetTrimmed)
    if (!Number.isInteger(offsetMs)) {
      return { error: `${label} Transition Step "${cue.name.trim()}"'s offset must be a whole number of milliseconds.` }
    }
    let fadeDurationMs: number | undefined
    if (cue.fadeDurationMs.trim() !== '') {
      const parsed = parseNonNegativeInt(cue.fadeDurationMs, `${label} Transition Step "${cue.name.trim()}"'s fade duration`)
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

  const enterShowCues = buildCues(form.enterShowCues, 'Enter-show')
  if ('error' in enterShowCues) return enterShowCues
  const enterRestingCues = buildCues(form.enterRestingCues, 'Enter-resting')
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
    // `Number('-Infinity')` is `-Infinity`, which is neither NaN nor
    // greater than 0, so a NaN-only check let it through, and
    // `JSON.stringify(-Infinity)` serializes to `null`, which the wire
    // schema's non-nullable `maxGainDb: number` would reject as a type
    // error rather than the range error an operator typing garbage here
    // actually needs to see. `Number.isFinite` excludes NaN AND both
    // infinities.
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

function slugify(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
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
  const params = useParams<{ showId: string; id: string }>()
  const showId = params.showId ?? ''
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id
  const workspaceData = useShowWorkspaceData(showId)

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [idManuallyEdited, setIdManuallyEdited] = useState(false)
  const [form, setForm] = useState<FormState>(emptyForm(showId))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)

  const [revisionView, setRevisionView] = useState<
    | { kind: 'idle' }
    | { kind: 'loading'; revision: number }
    | { kind: 'error'; revision: number; message: string }
    | { kind: 'loaded'; revision: number; config: NightSessionConfigResponse }
  >({ kind: 'idle' })

  useEffect(() => {
    if (isNew) setForm((f) => ({ ...f, show: showId, restingTimelineShow: showId }))
  }, [isNew, showId])

  useEffect(() => {
    if (isNew && !idManuallyEdited) setNewId(slugify(form.label))
  }, [isNew, idManuallyEdited, form.label])

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
    setForm((f) => ({ ...f, backgroundAudioItems: [...f.backgroundAudioItems, newBackgroundAudioItemForm(showId)] }))
  }

  function removeBackgroundAudioItem(index: number): void {
    setForm((f) => ({ ...f, backgroundAudioItems: f.backgroundAudioItems.filter((_, i) => i !== index) }))
  }

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A definition id is required.')
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
        navigate(showNightSessionPath(showId, id))
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      // The server resolves defaults on write (endOfNightPlaylist,
      // endOfNightRepeat, every cue's barrier/onFailure) that this
      // form's own optimistic `built.payload` never set explicitly;
      // re-seeding from the response makes the resolved values visible
      // immediately rather than only after a full page reload re-fetches
      // this same object.
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
  const editable = writeGate.allowed

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
          <span className="ruled-strip__explanation">Reading this night-session definition.</span>
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
      <>
        <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap' }}>
          <div style={{ minWidth: 0 }}>
            <p className="t-meta night-eyebrow">{isNew ? 'New definition' : 'Editing definition'}</p>
            <h2 className="t-heading">{isNew ? 'New night session' : form.label || existingId}</h2>
            {!isNew && state.kind === 'loaded' && (
              <p className="t-small night-muted">
                <span className="t-data">{existingId}</span> · revision <span className="t-data">{state.config.revision}</span>
                {' · saving takes effect on the next cycle, not this one'}
              </p>
            )}
          </div>
          <button type="button" className="btn btn--secondary" onClick={() => navigate(showNightSessionsPath(showId))}>
            All definitions
          </button>
        </div>

        {!editable && (
          <p className="t-small night-muted" role="status">
            Viewing only: editing requires the <code>config:write</code> scope.
          </p>
        )}

        <fieldset disabled={!editable} style={{ border: 0, padding: 0, margin: 0 }}>
          <section aria-labelledby="ns-id" className="night-section">
            <h3 id="ns-id" className="t-meta night-eyebrow">
              Identity &amp; show playlist
            </h3>
            <div className="night-field-grid">
              {isNew && (
                <label className="field">
                  <span className="field__label">Definition id</span>
                  <input
                    className="field__input field__input--data"
                    type="text"
                    value={newId}
                    onChange={(e) => {
                      setIdManuallyEdited(true)
                      setNewId(e.target.value)
                    }}
                  />
                </label>
              )}
              <label className="field">
                <span className="field__label">Label</span>
                <input className="field__input" type="text" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} />
              </label>
              <label className="field">
                <span className="field__label">Show playlist FPP instance</span>
                <input
                  className="field__input field__input--data"
                  type="text"
                  value={form.showPlaylistFppInstanceId}
                  onChange={(e) => setForm({ ...form, showPlaylistFppInstanceId: e.target.value })}
                />
              </label>
              <div className="field">
                <label className="field">
                  <span className="field__label">Show playlist</span>
                  <input
                    className="field__input field__input--data"
                    type="text"
                    value={form.showPlaylistPlaylist}
                    onChange={(e) => setForm({ ...form, showPlaylistPlaylist: e.target.value })}
                  />
                </label>
                <span className="field__help">Referenced, never created: FPP owns this playlist.</span>
              </div>
            </div>
          </section>

          <section aria-labelledby="ns-rest" className="night-section">
            <h3 id="ns-rest" className="t-meta night-eyebrow">
              Resting
            </h3>
            <p className="t-small night-muted" style={{ maxWidth: '74ch' }}>
              The loop that runs between cycles. Its own sequence decides how long a rest lasts; this definition
              holds no duration for it.
            </p>
            <div className="night-field-grid">
              <label className="field">
                <span className="field__label">FPP instance</span>
                <input
                  className="field__input field__input--data"
                  type="text"
                  value={form.restingFppInstanceId}
                  onChange={(e) => setForm({ ...form, restingFppInstanceId: e.target.value })}
                />
              </label>
              <label className="field">
                <span className="field__label">Resting playlist</span>
                <input
                  className="field__input field__input--data"
                  type="text"
                  value={form.restingPlaylist}
                  onChange={(e) => setForm({ ...form, restingPlaylist: e.target.value })}
                />
              </label>
              <div className="field">
                <label className="field">
                  <span className="field__label">End-of-night playlist</span>
                  <input
                    className="field__input field__input--data"
                    type="text"
                    value={form.restingEndOfNightPlaylist}
                    onChange={(e) => setForm({ ...form, restingEndOfNightPlaylist: e.target.value })}
                  />
                </label>
                <span className="field__help">Left unset it is the resting playlist above.</span>
              </div>
              <div className="field">
                <span className="field__label">Repeat after the last show</span>
                <div className="segmented" style={{ width: 'max-content' }}>
                  <button
                    type="button"
                    className="segmented__option"
                    aria-pressed={!form.restingEndOfNightRepeat}
                    disabled={!editable}
                    onClick={() => setForm({ ...form, restingEndOfNightRepeat: false })}
                  >
                    Play once
                  </button>
                  <button
                    type="button"
                    className="segmented__option"
                    aria-pressed={form.restingEndOfNightRepeat}
                    disabled={!editable}
                    onClick={() => setForm({ ...form, restingEndOfNightRepeat: true })}
                  >
                    Repeat
                  </button>
                </div>
              </div>
            </div>

            <div className="card night-card">
              <p className="t-body" style={{ fontWeight: 500, margin: 0 }}>
                Resting timeline asset
              </p>
              <p className="t-small night-muted">
                One asset, named the way every asset is named: show, sequence, and the node that renders it. In{' '}
                <span className="t-data">{showId}</span>.
              </p>
              <div className="night-field-grid">
                <label className="field">
                  <span className="field__label">Sequence</span>
                  <input
                    className="field__input field__input--data"
                    type="text"
                    value={form.restingTimelineSequence}
                    onChange={(e) => setForm({ ...form, restingTimelineSequence: e.target.value })}
                  />
                </label>
                <label className="field">
                  <span className="field__label">Target node</span>
                  <input
                    className="field__input field__input--data"
                    type="text"
                    value={form.restingTimelineTarget}
                    onChange={(e) => setForm({ ...form, restingTimelineTarget: e.target.value })}
                  />
                </label>
              </div>
            </div>

            <div className="card night-card">
              <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 14, flexWrap: 'wrap' }}>
                <div>
                  <p className="t-body" style={{ fontWeight: 500, margin: 0 }}>
                    Background audio
                  </p>
                  <p className="t-small night-muted">
                    A ShowMesh background session, played over the resting loop. Configured here; a definition
                    without it is complete, not degraded.
                  </p>
                </div>
                <label className="field__check">
                  <input
                    type="checkbox"
                    checked={form.backgroundAudioEnabled}
                    onChange={(e) => setForm({ ...form, backgroundAudioEnabled: e.target.checked })}
                  />
                  Configure background audio
                </label>
              </div>
              {form.backgroundAudioEnabled && (
                <>
                  <ol className="night-bg-list">
                    {form.backgroundAudioItems.map((item, i) => (
                      <li key={i} className="night-bg-list__row">
                        <span className="t-data night-faint" aria-hidden="true">
                          &#10303; {i + 1}
                        </span>
                        <input
                          className="field__input field__input--data"
                          type="text"
                          placeholder="item id (stable, survives a reorder)"
                          value={item.itemId}
                          onChange={(e) => updateBackgroundAudioItem(i, { itemId: e.target.value })}
                        />
                        <input
                          className="field__input field__input--data"
                          type="text"
                          placeholder="sequence"
                          value={item.sequence}
                          onChange={(e) => updateBackgroundAudioItem(i, { sequence: e.target.value })}
                        />
                        <input
                          className="field__input field__input--data"
                          type="text"
                          placeholder="target node"
                          value={item.target}
                          onChange={(e) => updateBackgroundAudioItem(i, { target: e.target.value })}
                        />
                        <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} className="btn btn--quiet" onClick={() => removeBackgroundAudioItem(i)}>
                          Remove
                        </ScopedButton>
                      </li>
                    ))}
                  </ol>
                  <p className="t-small night-faint">
                    Drag order is not yet editable here; add and remove items above. Item ids are stable and survive
                    a reorder: a run refers to an item id, never to &ldquo;the second item&rdquo;.
                  </p>
                  <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} className="btn btn--secondary" onClick={addBackgroundAudioItem}>
                    Add background audio item
                  </ScopedButton>

                  <div className="night-field-grid" style={{ marginTop: 12 }}>
                    <label className="field">
                      <span className="field__label">Repeat</span>
                      <select
                        className="field__input"
                        value={form.backgroundAudioRepeat}
                        onChange={(e) => setForm({ ...form, backgroundAudioRepeat: e.target.value as FormState['backgroundAudioRepeat'] })}
                      >
                        <option value="playlist">playlist</option>
                        <option value="item">item</option>
                        <option value="none">none</option>
                      </select>
                    </label>
                    <label className="field">
                      <span className="field__label">After an interruption</span>
                      <select
                        className="field__input"
                        value={form.backgroundAudioResume}
                        onChange={(e) => setForm({ ...form, backgroundAudioResume: e.target.value as FormState['backgroundAudioResume'] })}
                      >
                        <option value="resume">resume</option>
                        <option value="restart">restart</option>
                      </select>
                    </label>
                    <label className="field">
                      <span className="field__label">Item transition</span>
                      <select
                        className="field__input"
                        value={form.backgroundAudioItemTransition}
                        onChange={(e) =>
                          setForm({ ...form, backgroundAudioItemTransition: e.target.value as FormState['backgroundAudioItemTransition'] })
                        }
                      >
                        <option value="crossfade">crossfade</option>
                        <option value="gapless">gapless</option>
                        <option value="sequential">sequential</option>
                      </select>
                    </label>
                    {form.backgroundAudioItemTransition === 'crossfade' && (
                      <div className="field">
                        <label className="field">
                          <span className="field__label">Crossfade (ms)</span>
                          <input
                            className="field__input field__input--data"
                            type="number"
                            value={form.backgroundAudioCrossfadeMs}
                            onChange={(e) => setForm({ ...form, backgroundAudioCrossfadeMs: e.target.value })}
                          />
                        </label>
                        <span className="field__help">Required for crossfade, refused for the other two.</span>
                      </div>
                    )}
                    <div className="field">
                      <label className="field">
                        <span className="field__label">Ceiling (dB)</span>
                        <input
                          className="field__input field__input--data"
                          type="number"
                          value={form.backgroundAudioMaxGainDb}
                          onChange={(e) => setForm({ ...form, backgroundAudioMaxGainDb: e.target.value })}
                        />
                      </label>
                      <span className="field__help">0 dB or quieter.</span>
                    </div>
                  </div>
                </>
              )}
            </div>
          </section>

          <CueListEditor
            id="ns-enter"
            heading="Enter show"
            offsetHelp="Offsets are relative to the show start. Negative runs before it."
            cues={form.enterShowCues}
            onUpdate={(i, patch) => updateCue('enterShowCues', i, patch)}
            onAdd={() => addCue('enterShowCues')}
            onRemove={(i) => removeCue('enterShowCues', i)}
            blackoutLabel="Blackout hold (ms)"
            blackoutHelp="Dark between the resting loop stopping and the show starting."
            blackoutValue={form.enterShowBlackoutHoldMs}
            onBlackoutChange={(v) => setForm({ ...form, enterShowBlackoutHoldMs: v })}
          />

          <CueListEditor
            id="ns-return"
            heading="Enter resting"
            offsetHelp="Offsets are relative to the show ending."
            cues={form.enterRestingCues}
            onUpdate={(i, patch) => updateCue('enterRestingCues', i, patch)}
            onAdd={() => addCue('enterRestingCues')}
            onRemove={(i) => removeCue('enterRestingCues', i)}
            blackoutLabel="Blackout after the show (ms)"
            blackoutHelp="Dark before the resting loop picks up."
            blackoutValue={form.enterRestingBlackoutAfterShowMs}
            onBlackoutChange={(v) => setForm({ ...form, enterRestingBlackoutAfterShowMs: v })}
          />

          <section aria-labelledby="ns-site" className="night-section">
            <h3 id="ns-site" className="t-meta night-eyebrow">
              Site control &amp; interlocks
            </h3>
            <p className="t-small night-muted" style={{ maxWidth: '74ch' }}>
              Both are optional in full. A definition that omits them runs the whole night loop unchanged.
            </p>
            <PlannedFeature
              title="Site control"
              why="night.session.siteControl (the presentation power-off binding and its prerequisites) is specified in the schema, but the endpoint that saves a night-session definition rejects the key if it is present. There is nowhere to send this yet, so it cannot be authored from here."
            />
            <PlannedFeature
              title="Interlock authoring"
              why="night.session.interlocks can be read back from a saved definition, but the save endpoint refuses the key outright, so there is no way to author, change, or remove an interlock rule from this form. A rule already stored by another means would still show up wherever the coordinator reports it; it just cannot be created or edited here."
            />
          </section>
        </fieldset>

        {editable && (
          <div className="night-save-row">
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
              busyReason="Saving this definition…"
            >
              {saving ? 'Saving…' : isNew ? 'Create definition' : 'Save definition'}
            </ScopedButton>
            <button type="button" className="btn btn--quiet" onClick={() => navigate(showNightSessionsPath(showId))} disabled={saving}>
              Discard changes
            </button>
            {!isNew && state.kind === 'loaded' && (
              <span className="t-small night-muted night-save-row__revision">
                Saving creates revision <span className="t-data">{state.config.revision + 1}</span>
              </span>
            )}
          </div>
        )}

        {!isNew && state.kind === 'loaded' && (
          <section aria-label="Status" className="night-section">
            <p className="ruled-strip" role="status">
              <span className="ruled-strip__state t-meta">Active revision</span>
              <span className="ruled-strip__explanation">
                {state.config.revision}
                {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
              </span>
            </p>
            {state.revisions.length > 0 && (
              <details className="details-section">
                <summary className="details-section__summary">Revision history</summary>
                <div className="table-wrap">
                  <table className="table">
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
                          <td className="t-data">{rev.revision}</td>
                          <td>{rev.active ? 'active' : ''}</td>
                          <td className="t-data">{formatAbsolute(rev.createdAt)}</td>
                          <td>{rev.createdByPrincipalName ?? '-'}</td>
                          <td>
                            <button type="button" className="btn btn--quiet" onClick={() => void viewRevision(rev.revision)}>
                              View payload
                            </button>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
                {revisionView.kind === 'loading' && (
                  <p className="t-small night-muted">Loading revision {revisionView.revision}…</p>
                )}
                {revisionView.kind === 'error' && (
                  <p className="ruled-strip ruled-strip--failed" role="alert">
                    <span className="ruled-strip__state t-meta">Failed</span>
                    <span className="ruled-strip__explanation">{revisionView.message}</span>
                  </p>
                )}
                {revisionView.kind === 'loaded' && (
                  <div className="card night-card">
                    <p className="t-subhead" style={{ margin: 0 }}>
                      Revision {revisionView.revision} (immutable, may not be the active one)
                    </p>
                    <pre className="table-wrap">{JSON.stringify(revisionView.config.payload, null, 2)}</pre>
                  </div>
                )}
              </details>
            )}
          </section>
        )}

        <p className="t-small night-muted">
          Some phases refuse a new block interlock while the night is in them. If a save is withheld, the
          coordinator names the phase and the rule.
        </p>
      </>
    )
  })()

  return (
    <ShowWorkspaceFrame showId={showId} active="night-sessions" data={workspaceData}>
      {content}
    </ShowWorkspaceFrame>
  )
}

function CueListEditor({
  id,
  heading,
  offsetHelp,
  cues,
  onUpdate,
  onAdd,
  onRemove,
  blackoutLabel,
  blackoutHelp,
  blackoutValue,
  onBlackoutChange,
}: {
  id: string
  heading: string
  offsetHelp: string
  cues: CueForm[]
  onUpdate: (index: number, patch: Partial<CueForm>) => void
  onAdd: () => void
  onRemove: (index: number) => void
  blackoutLabel: string
  blackoutHelp: string
  blackoutValue: string
  onBlackoutChange: (v: string) => void
}) {
  const offsets = cues.map((c) => Number(c.offsetMs)).filter((n) => Number.isFinite(n))
  const min = offsets.length > 0 ? Math.min(0, ...offsets) : 0
  const max = offsets.length > 0 ? Math.max(0, ...offsets) : 0
  const span = max - min || 1

  return (
    <section aria-labelledby={id} className="night-section">
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 14, flexWrap: 'wrap' }}>
        <h3 id={id} className="t-meta night-eyebrow">
          {heading} <span className="night-muted">· {cues.length} {cues.length === 1 ? 'cue' : 'cues'}</span>
        </h3>
        <span className="t-small night-faint">{offsetHelp}</span>
      </div>

      {cues.length > 0 && (
        <div className="night-timeline" role="img" aria-label={`${heading} timing, ${cues.length} cues from ${min} to ${max} milliseconds`}>
          <div className="night-timeline__rule" aria-hidden="true" />
          {cues.map((cue, i) => {
            const offset = Number(cue.offsetMs)
            const pct = Number.isFinite(offset) ? ((offset - min) / span) * 100 : 0
            return (
              <div
                key={i}
                className={`night-timeline__mark${cue.barrier ? ' night-timeline__mark--barrier' : ''}`}
                style={{ left: `${pct}%` }}
                aria-hidden="true"
              >
                <span className="night-timeline__mark-line" />
                <span className="night-timeline__mark-label">{cue.name || `Cue ${i + 1}`}</span>
              </div>
            )
          })}
        </div>
      )}

      {cues.length === 0 && (
        <p className="ruled-strip ruled-strip--empty" role="status">
          <span className="ruled-strip__state t-meta">Empty</span>
          <span className="ruled-strip__explanation">No Transition Steps yet.</span>
        </p>
      )}

      {cues.length > 0 && (
        <div className="table-wrap">
          <table className="table table--full">
            <thead>
              <tr>
                <th>Cue</th>
                <th>Role</th>
                <th>Offset</th>
                <th>Fade</th>
                <th>Barrier</th>
                <th>On failure</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {cues.map((cue, i) => (
                <tr key={i} className={cue.barrier ? 'night-row--barrier' : undefined}>
                  <td>
                    <input
                      className="field__input field__input--data"
                      type="text"
                      placeholder="name"
                      value={cue.name}
                      onChange={(e) => onUpdate(i, { name: e.target.value })}
                    />
                    <input
                      className="field__input field__input--data"
                      style={{ marginTop: 4 }}
                      type="text"
                      placeholder="action id"
                      value={cue.action}
                      onChange={(e) => onUpdate(i, { action: e.target.value })}
                    />
                  </td>
                  <td>
                    <select className="field__input" value={cue.role} onChange={(e) => onUpdate(i, { role: e.target.value as CueRole })}>
                      {CUE_ROLES.map((r) => (
                        <option key={r} value={r}>
                          {r}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>
                    <input
                      className="field__input field__input--data"
                      type="number"
                      aria-label="Offset ms (signed)"
                      placeholder="offset ms (signed)"
                      value={cue.offsetMs}
                      onChange={(e) => onUpdate(i, { offsetMs: e.target.value })}
                    />
                  </td>
                  <td>
                    <input
                      className="field__input field__input--data"
                      type="number"
                      aria-label="Fade duration ms (optional)"
                      placeholder="fade duration ms (optional)"
                      value={cue.fadeDurationMs}
                      onChange={(e) => onUpdate(i, { fadeDurationMs: e.target.value })}
                    />
                  </td>
                  <td>
                    <label className="field__check">
                      <input type="checkbox" checked={cue.barrier} onChange={(e) => onUpdate(i, { barrier: e.target.checked })} />
                      Barrier
                    </label>
                  </td>
                  <td>
                    <select className="field__input" value={cue.onFailure} onChange={(e) => onUpdate(i, { onFailure: e.target.value as CueOnFailure })}>
                      {CUE_ON_FAILURE.map((f) => (
                        <option key={f} value={f}>
                          {f}
                        </option>
                      ))}
                    </select>
                  </td>
                  <td>
                    <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} className="btn btn--quiet" onClick={() => onRemove(i)}>
                      Remove
                    </ScopedButton>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="night-blackout-row">
        <div className="field" style={{ maxWidth: '16rem' }}>
          <label className="field">
            <span className="field__label">{blackoutLabel}</span>
            <input className="field__input field__input--data" type="number" value={blackoutValue} onChange={(e) => onBlackoutChange(e.target.value)} />
          </label>
          <span className="field__help">{blackoutHelp}</span>
        </div>
        <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} className="btn btn--secondary" onClick={onAdd}>
          Add cue
        </ScopedButton>
      </div>
    </section>
  )
}
