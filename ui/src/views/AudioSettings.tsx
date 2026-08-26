import { useEffect, useRef, useState } from 'react'
import {
  getAudioSettingsConfig,
  getAudioSettingsConfigRevisions,
  putAudioSettingsConfig,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import type { AudioSettingsConfigResponse, ConfigAudioSettingsPayload } from '../app/types'

// ADR-039: the audio.settings engine-wide singleton, the last of the two
// audio configuration kinds that shipped with a full API path and
// showmeshctl coverage but no Operator UI control at all. A separate
// route from Configuration.tsx (which already exceeds 1000 lines): this
// form's seven fields, their cross-field validation, and their own
// revision history are exactly as large as any one section already on
// that page, and audio.settings is not part of the FPP/Resolume
// connection story that page's own label names.
const CONFIG_WRITE_SCOPE = 'config:write'

const FADE_CURVES: ConfigAudioSettingsPayload['defaultFadeCurve'][] = ['linear']
const LTC_FRAME_RATES: ConfigAudioSettingsPayload['ltcFrameRate'][] = ['24', '25', '29.97', '30']

// HH:MM:SS:FF, non-drop-frame - mirrors the coordinator's own
// ltcDefaultStartOffset format (api/openapi.yaml's ConfigAudioSettingsPayload
// description). A client-side mirror only (ADR-030): the coordinator is
// the actual authority on whether the frame count is valid for the chosen
// ltcFrameRate.
const LTC_OFFSET_PATTERN = /^\d{2}:\d{2}:\d{2}:\d{2}$/

interface FormState {
  driftIgnoreThresholdMs: string
  defaultFadeCurve: ConfigAudioSettingsPayload['defaultFadeCurve'] | ''
  defaultFadeDurationMs: string
  defaultMaxBackgroundGain: string
  duckTargetGain: string
  ltcFrameRate: ConfigAudioSettingsPayload['ltcFrameRate'] | ''
  ltcDefaultStartOffset: string
}

function formFromPayload(payload: ConfigAudioSettingsPayload): FormState {
  return {
    driftIgnoreThresholdMs: String(payload.driftIgnoreThresholdMs),
    defaultFadeCurve: payload.defaultFadeCurve,
    defaultFadeDurationMs: String(payload.defaultFadeDurationMs),
    defaultMaxBackgroundGain: String(payload.defaultMaxBackgroundGain),
    duckTargetGain: String(payload.duckTargetGain),
    ltcFrameRate: payload.ltcFrameRate,
    ltcDefaultStartOffset: payload.ltcDefaultStartOffset,
  }
}

/**
 * Mirrors, not enforces (ADR-030): every check here also exists
 * server-side. A full replacement PUT - every field is required and
 * non-null on every write, so this never omits a key the way
 * assets.settings/fpp.mqtt's partial-update forms do.
 */
function buildPayload(form: FormState): { payload: ConfigAudioSettingsPayload } | { error: string } {
  const driftIgnoreThresholdMs = Number(form.driftIgnoreThresholdMs)
  if (form.driftIgnoreThresholdMs.trim() === '' || !Number.isInteger(driftIgnoreThresholdMs) || driftIgnoreThresholdMs < 0) {
    return { error: 'Drift ignore threshold must be a whole number of milliseconds, zero or greater.' }
  }

  if (form.defaultFadeCurve === '') {
    return { error: 'Default fade curve is required.' }
  }

  const defaultFadeDurationMs = Number(form.defaultFadeDurationMs)
  if (form.defaultFadeDurationMs.trim() === '' || !Number.isInteger(defaultFadeDurationMs) || defaultFadeDurationMs < 0) {
    return { error: 'Default fade duration must be a whole number of milliseconds, zero or greater.' }
  }

  const defaultMaxBackgroundGain = Number(form.defaultMaxBackgroundGain)
  if (form.defaultMaxBackgroundGain.trim() === '' || !Number.isFinite(defaultMaxBackgroundGain) || defaultMaxBackgroundGain < 0) {
    return { error: 'Default maximum background gain must be a linear amplitude multiplier, zero or greater (not a dB value).' }
  }

  const duckTargetGain = Number(form.duckTargetGain)
  if (form.duckTargetGain.trim() === '' || !Number.isFinite(duckTargetGain) || duckTargetGain < 0 || duckTargetGain >= 1) {
    return {
      error:
        'Duck target gain must be a linear amplitude multiplier (not a dB value) from 0 up to but not including 1: a value of 1 or more would not duck anything.',
    }
  }

  if (form.ltcFrameRate === '') {
    return { error: 'LTC frame rate is required.' }
  }

  if (!LTC_OFFSET_PATTERN.test(form.ltcDefaultStartOffset.trim())) {
    return { error: 'LTC default start offset must be HH:MM:SS:FF (non-drop-frame).' }
  }

  return {
    payload: {
      driftIgnoreThresholdMs,
      defaultFadeCurve: form.defaultFadeCurve,
      defaultFadeDurationMs,
      defaultMaxBackgroundGain,
      duckTargetGain,
      ltcFrameRate: form.ltcFrameRate,
      ltcDefaultStartOffset: form.ltcDefaultStartOffset.trim(),
    },
  }
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: AudioSettingsConfigResponse; revisions: ConfigRevisionMeta[] }

export function AudioSettings() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [form, setForm] = useState<FormState>({
    driftIgnoreThresholdMs: '',
    defaultFadeCurve: '',
    defaultFadeDurationMs: '',
    defaultMaxBackgroundGain: '',
    duckTargetGain: '',
    ltcFrameRate: '',
    ltcDefaultStartOffset: '',
  })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    if (!scopeGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const [config, revisionsResp] = await Promise.all([
          getAudioSettingsConfig(),
          getAudioSettingsConfigRevisions(),
        ])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setForm(formFromPayload(config.payload))
      } catch (err) {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [scopeGate.allowed, reloadGeneration])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const built = buildPayload(form)
    if ('error' in built) {
      setSaveError(built.error)
      return
    }
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      await putAudioSettingsConfig(built.payload)
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      // A refused write never reads as saved: the form keeps whatever the
      // operator typed and the coordinator's own reason renders below,
      // exactly like ShowCueDetail.tsx's identical handleSave shape.
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <div>
      <h2 className="panel__title">Audio settings</h2>
      <p className="text-muted">
        The audio engine&rsquo;s installation-wide defaults: drift tolerance, fade behaviour,
        background gain ceiling, ducking, and the LTC timecode Resolume receives.
        Requires the <code>config:write</code> scope (admin only); there is no read-only scope
        for this page.
      </p>

      {!scopeGate.allowed && (
        <p className="panel panel--error" role="status">
          {scopeGate.reason}
        </p>
      )}

      {scopeGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading configuration…</p>}
      {scopeGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}

      {scopeGate.allowed && state.kind === 'loaded' && (
        <>
          <p className="panel" role="status">
            Active revision {state.config.revision} (source {state.config.source}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}), last
            updated {formatAbsolute(state.config.updatedAt)}.
          </p>

          <label className="form-field">
            Drift ignore threshold (milliseconds)
            <input
              type="number"
              min={0}
              aria-label="Drift ignore threshold milliseconds"
              value={form.driftIgnoreThresholdMs}
              onChange={(e) => setForm({ ...form, driftIgnoreThresholdMs: e.target.value })}
            />
          </label>
          <p className="text-muted">
            How far a session may drift from its clock before this coordinator corrects it. Never
            measured against real playback; a starting point, not a tuned value.
          </p>

          <label className="form-field">
            Default fade curve
            <select
              aria-label="Default fade curve"
              value={form.defaultFadeCurve}
              onChange={(e) =>
                setForm({ ...form, defaultFadeCurve: e.target.value as ConfigAudioSettingsPayload['defaultFadeCurve'] })
              }
            >
              <option value="" disabled>
                Choose one, never defaulted
              </option>
              {FADE_CURVES.map((curve) => (
                <option key={curve} value={curve}>
                  {curve}
                </option>
              ))}
            </select>
          </label>

          <label className="form-field">
            Default fade duration (milliseconds)
            <input
              type="number"
              min={0}
              aria-label="Default fade duration milliseconds"
              value={form.defaultFadeDurationMs}
              onChange={(e) => setForm({ ...form, defaultFadeDurationMs: e.target.value })}
            />
          </label>

          <label className="form-field">
            Default maximum background gain (linear amplitude multiplier, not dB - 1.0 is unity
            gain)
            <input
              type="number"
              min={0}
              step="any"
              aria-label="Default maximum background gain, linear"
              value={form.defaultMaxBackgroundGain}
              onChange={(e) => setForm({ ...form, defaultMaxBackgroundGain: e.target.value })}
            />
          </label>

          <label className="form-field">
            Duck target gain (linear amplitude multiplier, not dB - 0 is full silence, must be
            below 1)
            <input
              type="number"
              min={0}
              max={0.999999}
              step="any"
              aria-label="Duck target gain, linear"
              value={form.duckTargetGain}
              onChange={(e) => setForm({ ...form, duckTargetGain: e.target.value })}
            />
          </label>
          <p className="text-muted">
            Provisional: this value has never been heard on the installation&rsquo;s speakers. A
            muted session is unaffected; mute silences unconditionally.
          </p>

          <label className="form-field">
            LTC frame rate
            <select
              aria-label="LTC frame rate"
              value={form.ltcFrameRate}
              onChange={(e) =>
                setForm({ ...form, ltcFrameRate: e.target.value as ConfigAudioSettingsPayload['ltcFrameRate'] })
              }
            >
              <option value="" disabled>
                Choose one, never defaulted
              </option>
              {LTC_FRAME_RATES.map((rate) => (
                <option key={rate} value={rate}>
                  {rate}
                </option>
              ))}
            </select>
          </label>
          <p className="text-muted">
            Always non-drop-frame at every rate: 29.97 drop-frame behavior is unresearched, an
            explicit ruling, not a silent default.
          </p>

          <label className="form-field">
            LTC default start offset (HH:MM:SS:FF, non-drop-frame)
            <input
              type="text"
              placeholder="00:00:00:00"
              aria-label="LTC default start offset"
              value={form.ltcDefaultStartOffset}
              onChange={(e) => setForm({ ...form, ltcDefaultStartOffset: e.target.value })}
            />
          </label>

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
              busyReason="Saving this configuration revision…"
            >
              {saving ? 'Saving…' : 'Save audio settings'}
            </ScopedButton>
          </div>

          {state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <div className="table-scroll">
                <table className="config-table" aria-label="Revision history">
                  <thead>
                    <tr>
                      <th scope="col">Revision</th>
                      <th scope="col">Active</th>
                      <th scope="col">Created at</th>
                      <th scope="col">Created by</th>
                      <th scope="col">Source</th>
                    </tr>
                  </thead>
                  <tbody>
                    {state.revisions.map((rev) => (
                      <tr key={rev.revision}>
                        <th scope="row">{rev.revision}</th>
                        <td>{rev.active ? 'active' : ''}</td>
                        <td>{formatAbsolute(rev.createdAt)}</td>
                        <td>{rev.createdByPrincipalName ?? '(coordinator startup migration)'}</td>
                        <td>{rev.source}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </details>
          )}
        </>
      )}
    </div>
  )
}
