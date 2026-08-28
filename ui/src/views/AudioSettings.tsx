import { useEffect, useRef, useState } from 'react'
import {
  getAudioSettingsConfig,
  getAudioSettingsConfigRevisions,
  putAudioSettingsConfig,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, describeSignInState, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { useUnsavedChanges } from '../app/UnsavedChanges'
import type { AudioSettingsConfigResponse, ConfigAudioSettingsPayload } from '../app/types'
import { ConfigurationSection, FailedBlock, LoadingBlock, OperatorPageHeader, StaleBlock, UnavailableBlock } from '../components/SharedLayouts'

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

// The decibel bounds openapi.yaml gives the two gain fields. 0 dB is
// unity, -60 dB is the silence floor show.cue's duckGainDb already uses,
// and +12 dB is a typo guard rather than a tuned headroom figure.
const SILENCE_FLOOR_DB = -60
const MAX_BACKGROUND_GAIN_DB = 12

interface FormState {
  driftIgnoreThresholdMs: string
  defaultFadeCurve: ConfigAudioSettingsPayload['defaultFadeCurve'] | ''
  defaultFadeDurationMs: string
  defaultMaxBackgroundGainDb: string
  duckTargetGainDb: string
  ltcFrameRate: ConfigAudioSettingsPayload['ltcFrameRate'] | ''
  ltcDefaultStartOffset: string
}

function formFromPayload(payload: ConfigAudioSettingsPayload): FormState {
  return {
    driftIgnoreThresholdMs: String(payload.driftIgnoreThresholdMs),
    defaultFadeCurve: payload.defaultFadeCurve,
    defaultFadeDurationMs: String(payload.defaultFadeDurationMs),
    defaultMaxBackgroundGainDb: String(payload.defaultMaxBackgroundGainDb),
    duckTargetGainDb: String(payload.duckTargetGainDb),
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

  const defaultMaxBackgroundGainDb = Number(form.defaultMaxBackgroundGainDb)
  if (
    form.defaultMaxBackgroundGainDb.trim() === '' ||
    !Number.isFinite(defaultMaxBackgroundGainDb) ||
    defaultMaxBackgroundGainDb > MAX_BACKGROUND_GAIN_DB ||
    defaultMaxBackgroundGainDb < SILENCE_FLOOR_DB
  ) {
    return {
      error: `Default maximum background gain is in dB (0 dB is unity) and must be between ${SILENCE_FLOOR_DB} and ${MAX_BACKGROUND_GAIN_DB} dB.`,
    }
  }

  const duckTargetGainDb = Number(form.duckTargetGainDb)
  if (
    form.duckTargetGainDb.trim() === '' ||
    !Number.isFinite(duckTargetGainDb) ||
    duckTargetGainDb >= 0 ||
    duckTargetGainDb < SILENCE_FLOOR_DB
  ) {
    return {
      error: `Duck target gain is in dB and must be negative and at least ${SILENCE_FLOOR_DB} dB: 0 dB or louder would not duck anything, and ${SILENCE_FLOOR_DB} dB is already silence.`,
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
      defaultMaxBackgroundGainDb,
      duckTargetGainDb,
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
  const { clearUnsavedChanges } = useUnsavedChanges('audio-defaults')
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [form, setForm] = useState<FormState>({
    driftIgnoreThresholdMs: '',
    defaultFadeCurve: '',
    defaultFadeDurationMs: '',
    defaultMaxBackgroundGainDb: '',
    duckTargetGainDb: '',
    ltcFrameRate: '',
    ltcDefaultStartOffset: '',
  })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)
  const signInState = describeSignInState(model.session)

  const permissionState = !scopeGate.allowed && (
    signInState.kind === 'loading' ? <LoadingBlock title="Loading permissions" reason="Waiting for the coordinator to report what this device may do." />
      : signInState.kind === 'bootstrap_required' ? <UnavailableBlock title="Setup required" reason="No administrator exists on this coordinator. Claim the bootstrap code from its data volume to create one before editing audio settings." />
        : signInState.kind === 'signed_out' ? <UnavailableBlock title="Signed out" reason="This device is not signed in, so it cannot edit audio settings." />
          : model.sessionFetchFailed || signInState.session.scopesState !== 'current' ? <StaleBlock title="Stale permission evidence" reason="Audio settings remain unavailable until the coordinator can confirm this device’s current permissions." />
            : <UnavailableBlock title="Insufficient permission" reason={scopeGate.reason} />
  )

  useEffect(() => {
    if (!scopeGate.allowed) clearUnsavedChanges()
  }, [clearUnsavedChanges, scopeGate.allowed])

  useEffect(() => {
    if (!scopeGate.allowed) return
    clearUnsavedChanges()
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
  }, [clearUnsavedChanges, scopeGate.allowed, reloadGeneration])

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
      clearUnsavedChanges()
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
    <div className="operator-page audio-settings-page" data-unsaved-form="audio-defaults">
      <OperatorPageHeader
        eyebrow="Settings / Audio defaults"
        title="Audio settings"
        lede={<>The audio engine&rsquo;s installation-wide defaults: drift tolerance, fade behaviour, background gain ceiling, ducking, and the LTC timecode Resolume receives. Requires the <code>config:write</code> scope (admin only); there is no read-only scope for this page.</>}
      />

      {permissionState}

      {scopeGate.allowed && state.kind === 'loading' && <LoadingBlock title="Loading audio settings" reason="Loading configuration…" />}
      {scopeGate.allowed && state.kind === 'error' && (
        <FailedBlock title="Audio settings could not be loaded" reason={<>{state.message} <button type="button" onClick={() => setReloadGeneration((g) => g + 1)}>Retry</button></>} />
      )}

      {scopeGate.allowed && state.kind === 'loaded' && (
        <ConfigurationSection title="Audio defaults" detail="Current coordinator configuration and its revision history.">
          <p className="panel config-status" role="status">
            Active revision {state.config.revision} (source {state.config.source}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}), last
            updated {formatAbsolute(state.config.updatedAt)}.
          </p>
          {state.config.source !== 'api' && (
            <p className="panel panel--warning" role="status">
              These are coordinator defaults. They have not been saved as an operator revision yet.
            </p>
          )}

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
            Default maximum background gain (dB - 0 dB is unity gain, -60 to +12 dB)
            <input
              type="number"
              min={SILENCE_FLOOR_DB}
              max={MAX_BACKGROUND_GAIN_DB}
              step="any"
              aria-label="Default maximum background gain in dB"
              value={form.defaultMaxBackgroundGainDb}
              onChange={(e) => setForm({ ...form, defaultMaxBackgroundGainDb: e.target.value })}
            />
          </label>

          <label className="form-field">
            Duck target gain (dB - must be negative, and -60 dB is full silence)
            <input
              type="number"
              min={SILENCE_FLOOR_DB}
              max={-0.000001}
              step="any"
              aria-label="Duck target gain in dB"
              value={form.duckTargetGainDb}
              onChange={(e) => setForm({ ...form, duckTargetGainDb: e.target.value })}
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
        </ConfigurationSection>
      )}
    </div>
  )
}
