import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowCue, getShowCueRevisions, putShowCue, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import type { ConfigShowCue, CueAnnouncementPolicy, ShowCueConfigResponse } from '../app/types'

// Track H seam H6 (TRACK-H-cues-and-playlists.md "H6"): show.cue authoring.
// Same "mirror, not authority" posture as ShowActionDetail.tsx and
// ShowSurfaceDetail.tsx (ADR-030): every check this form makes also
// exists server-side, and a refused `PUT` renders through describeApiError
// exactly like those two, never a client-invented message. This is a FULL
// REPLACEMENT form: `outputs` is sent as whatever the four toggles below
// resolve to, not merged with what the server already holds.
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

function emptyForm(): FormState {
  return {
    show: '',
    name: '',
    renderEnabled: false,
    renderSequence: '',
    audioEnabled: false,
    audioAsset: '',
    audioStartOffsetMillis: '',
    ltcEnabled: false,
    ltcStartOffsetMillis: '',
    announcementEnabled: false,
    announcementPolicy: '',
    announcementDuckGainDb: '',
    announcementFadeMillis: '',
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
    audioStartOffsetMillis: outputs.audio === undefined ? '' : String(outputs.audio.startOffsetMillis),
    ltcEnabled: outputs.ltc !== undefined,
    ltcStartOffsetMillis: outputs.ltc === undefined ? '' : String(outputs.ltc.startOffsetMillis),
    announcementEnabled: outputs.announcement !== undefined,
    announcementPolicy: outputs.announcement?.policy ?? '',
    announcementDuckGainDb: outputs.announcement?.duckGainDb === undefined ? '' : String(outputs.announcement.duckGainDb),
    announcementFadeMillis: outputs.announcement === undefined ? '' : String(outputs.announcement.fadeMillis),
  }
}

/**
 * Mirrors, not enforces (ADR-030): every check here also exists
 * server-side (api/openapi.yaml's own description of PUT
 * /config/show.cue/{id}): an outputs object with nothing enabled, an
 * `ltc` or `announcement` enabled without `audio`, and a "duck" policy
 * with no gain are all refusals the coordinator makes independently; this
 * only saves a round trip for the common omission.
 */
function buildPayload(form: FormState): { payload: ConfigShowCue } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.name.trim() === '') return { error: 'Name is required.' }
  if (form.name.trim().length > 200) return { error: 'Name must be 200 characters or fewer.' }

  if (!form.renderEnabled && !form.audioEnabled && !form.ltcEnabled && !form.announcementEnabled) {
    return {
      error:
        'At least one output (render, audio, LTC, or announcement) is required. A Cue declaring nothing is an authoring mistake, not an empty-but-valid Cue.',
    }
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
    if (!Number.isInteger(audioStartOffsetMillis) || audioStartOffsetMillis < 0) {
      return { error: 'Audio start offset must be a whole number of milliseconds, zero or greater.' }
    }
    outputs.audio = { asset: form.audioAsset.trim(), startOffsetMillis: audioStartOffsetMillis }
  }

  if (form.ltcEnabled) {
    const ltcStartOffsetMillis = Number(form.ltcStartOffsetMillis)
    if (!Number.isInteger(ltcStartOffsetMillis) || ltcStartOffsetMillis < 0 || ltcStartOffsetMillis > 86400000) {
      return { error: 'LTC start offset must be a whole number of milliseconds, from 0 up to 24 hours (86400000).' }
    }
    outputs.ltc = { startOffsetMillis: ltcStartOffsetMillis }
  }

  if (form.announcementEnabled) {
    if (form.announcementPolicy === '') {
      return { error: 'Announcement policy is required and has no default; pick duck, mix, or interrupt.' }
    }
    const fadeMillis = Number(form.announcementFadeMillis)
    if (!Number.isInteger(fadeMillis) || fadeMillis < 0 || fadeMillis > 60000) {
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

  return {
    payload: {
      show: form.show.trim(),
      name: form.name.trim(),
      outputs,
    },
  }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowCueConfigResponse; revisions: ConfigRevisionMeta[] }

export interface ShowCueDetailProps {
  isNew?: boolean
}

export function ShowCueDetail({ isNew = false }: ShowCueDetailProps) {
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
        navigate(`/config/show.cue/${encodeURIComponent(id)}`)
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
  if (!pageGate.allowed) {
    return (
      <div>
        <h2 className="panel__title">{isNew ? 'New cue' : 'Cue'}</h2>
        <p className="panel panel--error" role="status">
          {pageGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading cue…</p>
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
    <div>
      <h2 className="panel__title">{isNew ? 'New cue' : form.name || existingId}</h2>

      {!editable && (
        <p className="text-muted" role="status">
          Viewing only: editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="form-field">
          Cue id
          <input type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable}>
        <label className="form-field">
          Show
          <input type="text" value={form.show} onChange={(e) => setForm({ ...form, show: e.target.value })} />
        </label>
        <label className="form-field">
          Name
          <input type="text" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
        </label>

        <h3 className="panel__title">Outputs</h3>
        <p className="text-muted">
          At least one output is required. LTC and announcement each require audio to also be
          enabled: an activation with no audio has no clock domain to emit against and no
          subject for the announcement policy.
        </p>

        <label className="form-field form-field--checkbox">
          <input
            type="checkbox"
            checked={form.renderEnabled}
            onChange={(e) => setForm({ ...form, renderEnabled: e.target.checked })}
          />
          Render
        </label>
        {form.renderEnabled && (
          <label className="form-field">
            Sequence (the logical sequence name, never an FSEQ filename or asset id)
            <input
              type="text"
              value={form.renderSequence}
              onChange={(e) => setForm({ ...form, renderSequence: e.target.value })}
            />
          </label>
        )}

        <label className="form-field form-field--checkbox">
          <input
            type="checkbox"
            checked={form.audioEnabled}
            onChange={(e) =>
              setForm({
                ...form,
                audioEnabled: e.target.checked,
                // Disabling audio disables what depends on it, mirroring
                // the server's own audio-dependency rule rather than
                // leaving a stale enabled-but-invalid combination sitting
                // in the form.
                ltcEnabled: e.target.checked ? form.ltcEnabled : false,
                announcementEnabled: e.target.checked ? form.announcementEnabled : false,
              })
            }
          />
          Audio
        </label>
        {form.audioEnabled && (
          <>
            <label className="form-field">
              Asset
              <input
                type="text"
                value={form.audioAsset}
                onChange={(e) => setForm({ ...form, audioAsset: e.target.value })}
              />
            </label>
            <label className="form-field">
              Start offset (milliseconds)
              <input
                type="number"
                min={0}
                value={form.audioStartOffsetMillis}
                onChange={(e) => setForm({ ...form, audioStartOffsetMillis: e.target.value })}
              />
            </label>
          </>
        )}

        <label className="form-field form-field--checkbox">
          <input
            type="checkbox"
            checked={form.ltcEnabled}
            disabled={!form.audioEnabled}
            onChange={(e) => setForm({ ...form, ltcEnabled: e.target.checked })}
          />
          LTC{!form.audioEnabled && ' (requires audio)'}
        </label>
        {form.ltcEnabled && (
          <label className="form-field">
            Start offset (milliseconds, up to 24 hours / 86400000)
            <input
              type="number"
              min={0}
              max={86400000}
              value={form.ltcStartOffsetMillis}
              onChange={(e) => setForm({ ...form, ltcStartOffsetMillis: e.target.value })}
            />
          </label>
        )}

        <label className="form-field form-field--checkbox">
          <input
            type="checkbox"
            checked={form.announcementEnabled}
            disabled={!form.audioEnabled}
            onChange={(e) => setForm({ ...form, announcementEnabled: e.target.checked })}
          />
          Announcement{!form.audioEnabled && ' (requires audio)'}
        </label>
        {form.announcementEnabled && (
          <>
            <label className="form-field">
              Policy
              <select
                value={form.announcementPolicy}
                onChange={(e) =>
                  setForm({
                    ...form,
                    announcementPolicy: e.target.value as CueAnnouncementPolicy,
                    // Duck gain is refused server-side on "mix"/"interrupt"
                    // (api/openapi.yaml's own description of this PUT):
                    // clearing it here when the policy changes away from
                    // "duck" keeps the form from resubmitting a stale value
                    // the server would refuse.
                    announcementDuckGainDb: e.target.value === 'duck' ? form.announcementDuckGainDb : '',
                  })
                }
              >
                <option value="" disabled>
                  Choose one, never defaulted
                </option>
                {ANNOUNCEMENT_POLICIES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
            {form.announcementPolicy === 'duck' && (
              <label className="form-field">
                Duck gain (dB, below 0 and no lower than -60)
                <input
                  type="number"
                  min={-60}
                  max={-0.1}
                  step="any"
                  value={form.announcementDuckGainDb}
                  onChange={(e) => setForm({ ...form, announcementDuckGainDb: e.target.value })}
                />
              </label>
            )}
            <label className="form-field">
              Fade (milliseconds, 0&ndash;60000)
              <input
                type="number"
                min={0}
                max={60000}
                value={form.announcementFadeMillis}
                onChange={(e) => setForm({ ...form, announcementFadeMillis: e.target.value })}
              />
            </label>
          </>
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
            busyReason="Saving this cue revision…"
          >
            {saving ? 'Saving…' : isNew ? 'Create cue' : 'Save cue'}
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
