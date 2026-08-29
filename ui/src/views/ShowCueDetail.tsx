import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowCue, getShowCueRevisions, putShowCue, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showCuePath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type { ConfigShowCue, CueAnnouncementPolicy, ShowCueConfigResponse } from '../app/types'

// Show Cues.dc.html's composer: derives the id from the name (editable),
// offers the four outputs (render, audio, ltc, announcement) as
// checkboxes with at least one required, and closes with a plain-
// language summary of what activation will do. Cues are SHARED across
// playlists, so the inspector warns that an edit changes every playlist
// that uses it. ADR-030 posture unchanged: every check here also exists
// server-side, and a refused PUT renders through describeApiError
// verbatim.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

const ANNOUNCEMENT_POLICIES: CueAnnouncementPolicy[] = ['duck', 'mix', 'interrupt']

interface FormState {
  show: string
  name: string
  renderEnabled: boolean
  renderSequence: string
  audioEnabled: boolean
  audioAsset: string
  audioStartOffsetMillis: string
  ltcEnabled: boolean
  ltcStartOffsetMillis: string
  announcementEnabled: boolean
  announcementPolicy: CueAnnouncementPolicy | ''
  announcementDuckGainDb: string
  announcementFadeMillis: string
}

function emptyForm(show: string): FormState {
  return {
    show,
    name: '',
    renderEnabled: false,
    renderSequence: '',
    audioEnabled: false,
    audioAsset: '',
    audioStartOffsetMillis: '0',
    ltcEnabled: false,
    ltcStartOffsetMillis: '0',
    announcementEnabled: false,
    announcementPolicy: '',
    announcementDuckGainDb: '',
    announcementFadeMillis: '400',
  }
}

function formFromPayload(payload: ConfigShowCue): FormState {
  const outputs = payload.outputs
  return {
    show: payload.show,
    name: payload.name,
    renderEnabled: outputs.render !== undefined,
    renderSequence: outputs.render?.sequence ?? '',
    audioEnabled: outputs.audio !== undefined,
    audioAsset: outputs.audio?.asset ?? '',
    audioStartOffsetMillis: outputs.audio === undefined ? '0' : String(outputs.audio.startOffsetMillis),
    ltcEnabled: outputs.ltc !== undefined,
    ltcStartOffsetMillis: outputs.ltc === undefined ? '0' : String(outputs.ltc.startOffsetMillis),
    announcementEnabled: outputs.announcement !== undefined,
    announcementPolicy: outputs.announcement?.policy ?? '',
    announcementDuckGainDb: outputs.announcement?.duckGainDb === undefined ? '' : String(outputs.announcement.duckGainDb),
    announcementFadeMillis: outputs.announcement === undefined ? '400' : String(outputs.announcement.fadeMillis),
  }
}

function slugify(name: string): string {
  return name
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '')
}

/**
 * Mirrors, not enforces (ADR-030): every check here also exists
 * server-side. An outputs object with nothing enabled, an `ltc` or
 * `announcement` enabled without `audio`, and a "duck" policy with no
 * gain are all refusals the coordinator makes independently.
 */
function buildPayload(form: FormState): { payload: ConfigShowCue } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.name.trim() === '') return { error: 'Name is required.' }
  if (form.name.trim().length > 200) return { error: 'Name must be 200 characters or fewer.' }

  if (!form.renderEnabled && !form.audioEnabled && !form.ltcEnabled && !form.announcementEnabled) {
    return { error: 'At least one output is required. Pick at least one.' }
  }
  if (form.ltcEnabled && !form.audioEnabled) {
    return { error: 'LTC requires audio to also be enabled: it emits from the program-audio clock domain.' }
  }
  if (form.announcementEnabled && !form.audioEnabled) {
    return { error: 'Announcement requires audio to also be enabled: an announcement needs something to play.' }
  }

  const outputs: ConfigShowCue['outputs'] = {}

  if (form.renderEnabled) {
    if (form.renderSequence.trim() === '') return { error: 'Render sequence is required when render is enabled.' }
    outputs.render = { sequence: form.renderSequence.trim() }
  }

  if (form.audioEnabled) {
    if (form.audioAsset.trim() === '') return { error: 'Audio asset is required when audio is enabled.' }
    const audioStartOffsetMillis = Number(form.audioStartOffsetMillis)
    if (form.audioStartOffsetMillis.trim() === '' || !Number.isInteger(audioStartOffsetMillis) || audioStartOffsetMillis < 0) {
      return { error: 'Audio start offset must be a whole number of milliseconds, zero or greater.' }
    }
    outputs.audio = { asset: form.audioAsset.trim(), startOffsetMillis: audioStartOffsetMillis }
  }

  if (form.ltcEnabled) {
    const ltcStartOffsetMillis = Number(form.ltcStartOffsetMillis)
    if (form.ltcStartOffsetMillis.trim() === '' || !Number.isInteger(ltcStartOffsetMillis) || ltcStartOffsetMillis < 0 || ltcStartOffsetMillis > 86400000) {
      return { error: 'LTC start offset must be a whole number of milliseconds, from 0 up to 24 hours (86400000).' }
    }
    outputs.ltc = { startOffsetMillis: ltcStartOffsetMillis }
  }

  if (form.announcementEnabled) {
    if (form.announcementPolicy === '') {
      return { error: 'Announcement policy is required and has no default; pick duck, mix, or interrupt.' }
    }
    const fadeMillis = Number(form.announcementFadeMillis)
    if (form.announcementFadeMillis.trim() === '' || !Number.isInteger(fadeMillis) || fadeMillis < 0 || fadeMillis > 60000) {
      return { error: 'Announcement fade must be a whole number of milliseconds, from 0 to 60000.' }
    }
    if (form.announcementPolicy === 'duck') {
      if (form.announcementDuckGainDb.trim() === '') {
        return { error: 'Duck gain is required when the announcement policy is "duck".' }
      }
      const duckGainDb = Number(form.announcementDuckGainDb)
      if (!Number.isFinite(duckGainDb) || duckGainDb >= 0 || duckGainDb < -60) {
        return { error: 'Duck gain must be a number below 0 dB, no lower than -60 dB.' }
      }
      outputs.announcement = { policy: 'duck', duckGainDb, fadeMillis }
    } else {
      if (form.announcementDuckGainDb.trim() !== '') {
        return { error: 'Duck gain only applies when the announcement policy is "duck". Clear it or pick "duck".' }
      }
      outputs.announcement = { policy: form.announcementPolicy, fadeMillis }
    }
  }

  return { payload: { show: form.show.trim(), name: form.name.trim(), outputs } }
}

/** The plain-language summary of what activation will do, closing the composer. */
function summarize(form: FormState): string {
  const parts: string[] = []
  if (form.audioEnabled && form.audioAsset.trim() !== '') parts.push(`play ${form.audioAsset.trim()}`)
  if (form.renderEnabled && form.renderSequence.trim() !== '') parts.push(`render ${form.renderSequence.trim()}`)
  if (form.announcementEnabled && form.announcementPolicy === 'duck' && form.announcementDuckGainDb.trim() !== '') {
    parts.push(`duck the background bed to ${form.announcementDuckGainDb.trim()} dB over ${form.announcementFadeMillis || '0'} ms`)
  } else if (form.announcementEnabled && form.announcementPolicy === 'mix') {
    parts.push('mix with the background bed')
  } else if (form.announcementEnabled && form.announcementPolicy === 'interrupt') {
    parts.push('interrupt the background bed')
  }
  if (form.ltcEnabled) parts.push('emit LTC')
  if (parts.length === 0) return 'On activation this cue will do nothing until at least one output is configured.'
  return `On activation this cue will ${parts.join(', ')}, and leave FPP untouched unless render is enabled.`
}

export interface ShowCueDetailProps {
  isNew?: boolean
}

export function ShowCueDetail({ isNew = false }: ShowCueDetailProps) {
  // App.tsx's real route is `shows/:showId/cues/:id`; `cueId` is kept as
  // a fallback so an older link/test using that name still resolves.
  const params = useParams<{ showId: string; cueId?: string; id?: string }>()
  const showId = params.showId ?? ''
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : (params.cueId ?? params.id)
  const workspaceData = useShowWorkspaceData(showId)

  type LoadState =
    | { kind: 'new' }
    | { kind: 'loading' }
    | { kind: 'error'; message: string }
    | { kind: 'loaded'; config: ShowCueConfigResponse; revisions: ConfigRevisionMeta[] }

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [idManuallyEdited, setIdManuallyEdited] = useState(false)
  const [form, setForm] = useState<FormState>(emptyForm(showId))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)

  useEffect(() => {
    if (isNew) setForm((f) => ({ ...f, show: showId }))
  }, [isNew, showId])

  useEffect(() => {
    if (isNew && !idManuallyEdited) setNewId(slugify(form.name))
  }, [isNew, idManuallyEdited, form.name])

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getShowCue(existingId), getShowCueRevisions(existingId)])
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

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A cue id is required.')
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
      const resp = await putShowCue(id, built.payload)
      if (isNew) {
        navigate(showCuePath(showId, id))
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      const revisionsResp = await getShowCueRevisions(id)
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
          <span className="ruled-strip__explanation">Reading this cue.</span>
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
      <div className="card" style={{ maxWidth: 480 }}>
        <div style={{ padding: 14, borderBottom: '1px solid var(--border)', background: 'var(--raised)' }}>
          <h2 className="t-meta shows-faint">{isNew ? 'New cue' : 'Cue'}</h2>
          <p className="t-small shows-muted">
            {isNew
              ? `In ${showId}. Cues can only reference this show's assets.`
              : `Used by playlists in this show · edits here apply to all of them.`}
          </p>
        </div>

        {!editable && (
          <p className="t-small shows-muted" role="status" style={{ padding: '0 14px' }}>
            Viewing only: editing requires the <code>config:write</code> scope.
          </p>
        )}

        <fieldset disabled={!editable} style={{ border: 0, padding: 14, margin: 0 }}>
          <label className="field">
            <span className="field__label">Name</span>
            <input className="field__input" type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </label>
          {isNew && (
            <label className="field" style={{ marginTop: 10 }}>
              <span className="field__label">Id (from the name, editable)</span>
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

          <h3 className="t-subhead" style={{ marginTop: 18 }}>
            What does this cue do?
          </h3>
          <p className="t-small shows-muted">Pick at least one.</p>

          <div className="composer-outputs">
            <label className={`composer-output-option${form.renderEnabled ? ' composer-output-option--checked' : ''}`}>
              <input type="checkbox" checked={form.renderEnabled} onChange={(e) => setForm({ ...form, renderEnabled: e.target.checked })} />
              <span>
                <span className="t-body" style={{ fontWeight: 500 }}>
                  Render
                </span>
                <br />
                <span className="t-small shows-muted">Drive lighting and video from a sequence</span>
              </span>
            </label>
            <label className={`composer-output-option${form.audioEnabled ? ' composer-output-option--checked' : ''}`}>
              <input
                type="checkbox"
                checked={form.audioEnabled}
                onChange={(e) =>
                  setForm({
                    ...form,
                    audioEnabled: e.target.checked,
                    ltcEnabled: e.target.checked ? form.ltcEnabled : false,
                    announcementEnabled: e.target.checked ? form.announcementEnabled : false,
                  })
                }
              />
              <span>
                <span className="t-body" style={{ fontWeight: 500 }}>
                  Audience audio
                </span>
                <br />
                <span className="t-small shows-muted">Play an audio asset on the program bus</span>
              </span>
            </label>
            <label className={`composer-output-option${form.ltcEnabled ? ' composer-output-option--checked' : ''}`}>
              <input type="checkbox" checked={form.ltcEnabled} disabled={!editable || !form.audioEnabled} onChange={(e) => setForm({ ...form, ltcEnabled: e.target.checked })} />
              <span>
                <span className="t-body" style={{ fontWeight: 500 }}>
                  LTC{!form.audioEnabled && ' (requires audio)'}
                </span>
                <br />
                <span className="t-small shows-muted">Emit timecode for Resolume</span>
              </span>
            </label>
            <label className={`composer-output-option${form.announcementEnabled ? ' composer-output-option--checked' : ''}`}>
              <input
                type="checkbox"
                checked={form.announcementEnabled}
                disabled={!editable || !form.audioEnabled}
                onChange={(e) => setForm({ ...form, announcementEnabled: e.target.checked })}
              />
              <span>
                <span className="t-body" style={{ fontWeight: 500 }}>
                  Announcement{!form.audioEnabled && ' (requires audio)'}
                </span>
                <br />
                <span className="t-small shows-muted">Speak over the background bed</span>
              </span>
            </label>
          </div>

          {form.renderEnabled && (
            <label className="field" style={{ marginTop: 14 }}>
              <span className="field__label">Logical sequence (never an FSEQ filename or asset id)</span>
              <input className="field__input" type="text" value={form.renderSequence} onChange={(e) => setForm({ ...form, renderSequence: e.target.value })} />
            </label>
          )}

          {form.audioEnabled && (
            <div style={{ display: 'grid', gap: 12, marginTop: 14 }}>
              <label className="field">
                <span className="field__label">Audio asset</span>
                <input className="field__input" type="text" value={form.audioAsset} onChange={(e) => setForm({ ...form, audioAsset: e.target.value })} />
                <span className="field__help">This show&rsquo;s assets only.</span>
              </label>
              <label className="field" style={{ maxWidth: 160 }}>
                <span className="field__label">Start offset (ms)</span>
                <input
                  className="field__input field__input--data"
                  type="number"
                  min={0}
                  value={form.audioStartOffsetMillis}
                  onChange={(e) => setForm({ ...form, audioStartOffsetMillis: e.target.value })}
                />
              </label>
            </div>
          )}

          {form.ltcEnabled && (
            <label className="field" style={{ marginTop: 14, maxWidth: 220 }}>
              <span className="field__label">LTC start offset (ms, up to 24 h / 86400000)</span>
              <input
                className="field__input field__input--data"
                type="number"
                min={0}
                max={86400000}
                value={form.ltcStartOffsetMillis}
                onChange={(e) => setForm({ ...form, ltcStartOffsetMillis: e.target.value })}
              />
              <span className="field__help">Lives on the cue, not the entry, the same timecode wherever this cue runs.</span>
            </label>
          )}

          {form.announcementEnabled && (
            <div style={{ marginTop: 14 }}>
              <span className="t-small shows-muted">While it plays</span>
              <div className="segmented" role="group" aria-label="Announcement policy" style={{ marginTop: 6 }}>
                {ANNOUNCEMENT_POLICIES.map((p) => (
                  <button
                    key={p}
                    type="button"
                    className="segmented__option"
                    aria-pressed={form.announcementPolicy === p}
                    disabled={!editable}
                    onClick={() =>
                      setForm({ ...form, announcementPolicy: p, announcementDuckGainDb: p === 'duck' ? form.announcementDuckGainDb : '' })
                    }
                  >
                    {p === 'duck' ? 'Duck' : p === 'mix' ? 'Mix' : 'Interrupt'}
                  </button>
                ))}
              </div>
              <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 12, marginTop: 12 }}>
                {form.announcementPolicy === 'duck' && (
                  <label className="field">
                    <span className="field__label">Duck to (dB, below 0, no lower than -60)</span>
                    <input
                      className="field__input field__input--data"
                      type="number"
                      min={-60}
                      max={-0.1}
                      step="any"
                      value={form.announcementDuckGainDb}
                      onChange={(e) => setForm({ ...form, announcementDuckGainDb: e.target.value })}
                    />
                  </label>
                )}
                <label className="field">
                  <span className="field__label">Fade (ms, 0&ndash;60000)</span>
                  <input
                    className="field__input field__input--data"
                    type="number"
                    min={0}
                    max={60000}
                    value={form.announcementFadeMillis}
                    onChange={(e) => setForm({ ...form, announcementFadeMillis: e.target.value })}
                  />
                </label>
              </div>
              <p className="field__help">Duck level applies only to the Duck policy.</p>
            </div>
          )}
        </fieldset>

        <p className="composer-summary">{summarize(form)}</p>

        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, padding: '12px 14px', background: 'var(--raised)' }}>
          <span className="t-small shows-muted">
            {isNew ? 'Creates revision 1' : !isNew && state.kind === 'loaded' ? `Cue rev ${state.config.revision}` : ''}
          </span>
          {editable && (
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              className="btn btn--primary"
              onClick={() => void handleSave()}
              busy={saving}
              busyReason="Saving this cue revision…"
            >
              {saving ? 'Saving…' : isNew ? 'Create cue' : 'Save cue'}
            </ScopedButton>
          )}
        </div>
        {saveError !== null && (
          <p role="alert" className="field__error" style={{ padding: '0 14px 12px' }}>
            {saveError}
          </p>
        )}

        {!isNew && state.kind === 'loaded' && state.revisions.length > 0 && (
          <div style={{ padding: 14, borderTop: '1px solid var(--border)' }}>
            <h3 className="t-subhead">Revision history</h3>
            <div className="table-wrap">
              <table className="table" aria-label="Revision history">
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
          </div>
        )}
      </div>
    )
  })()

  return (
    <ShowWorkspaceFrame showId={showId} active="cues" data={workspaceData}>
      {content}
    </ShowWorkspaceFrame>
  )
}
