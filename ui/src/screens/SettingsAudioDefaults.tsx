import { useEffect, useState } from 'react'
import {
  getAudioSettingsConfig,
  getAudioSettingsConfigRevisions,
  putAudioSettingsConfig,
  type AudioSettingsConfigResponse,
} from '../api'
import { Button, ButtonRow, Field, Input, RevisionHistory, RuledStrip, Section, Select } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: AudioSettingsConfigResponse }
  | { kind: 'failed'; reason: string }

const LTC_FRAME_RATES = ['30', '29.97', '25', '24'] as const

export function SettingsAudioDefaults() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  const [fadeDurationMs, setFadeDurationMs] = useState('')
  const [maxBackgroundGainDb, setMaxBackgroundGainDb] = useState('')
  const [duckTargetGainDb, setDuckTargetGainDb] = useState('')
  const [duckFadeDurationMs, setDuckFadeDurationMs] = useState('')
  const [duckRestoreFadeDurationMs, setDuckRestoreFadeDurationMs] = useState('')
  const [driftIgnoreThresholdMs, setDriftIgnoreThresholdMs] = useState('')
  const [scheduledStartDeliveryBoundMs, setScheduledStartDeliveryBoundMs] = useState('')
  const [scheduledStartMarginMs, setScheduledStartMarginMs] = useState('')
  const [multisyncFallbackWindowMs, setMultisyncFallbackWindowMs] = useState('')
  const [multisyncStartLeadMs, setMultisyncStartLeadMs] = useState('')
  const [ltcFrameRate, setLtcFrameRate] = useState<(typeof LTC_FRAME_RATES)[number]>('30')
  const [ltcDefaultStartOffset, setLtcDefaultStartOffset] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<AudioSettingsConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getAudioSettingsConfig()
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
        setFadeDurationMs(String(response.payload.defaultFadeDurationMs))
        setMaxBackgroundGainDb(String(response.payload.defaultMaxBackgroundGainDb))
        setDuckTargetGainDb(String(response.payload.duckTargetGainDb))
        setDuckFadeDurationMs(String(response.payload.duckFadeDurationMs))
        setDuckRestoreFadeDurationMs(String(response.payload.duckRestoreFadeDurationMs))
        setDriftIgnoreThresholdMs(String(response.payload.driftIgnoreThresholdMs))
        setScheduledStartDeliveryBoundMs(String(response.payload.scheduledStartDeliveryBoundMs))
        setScheduledStartMarginMs(String(response.payload.scheduledStartMarginMs))
        setMultisyncFallbackWindowMs(String(response.payload.multisyncFallbackWindowMs))
        setMultisyncStartLeadMs(String(response.payload.multisyncStartLeadMs))
        setLtcFrameRate(response.payload.ltcFrameRate)
        setLtcDefaultStartOffset(response.payload.ltcDefaultStartOffset)
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const discard = () => {
    if (state.kind !== 'loaded') return
    setFadeDurationMs(String(state.response.payload.defaultFadeDurationMs))
    setMaxBackgroundGainDb(String(state.response.payload.defaultMaxBackgroundGainDb))
    setDuckTargetGainDb(String(state.response.payload.duckTargetGainDb))
    setDuckFadeDurationMs(String(state.response.payload.duckFadeDurationMs))
    setDuckRestoreFadeDurationMs(String(state.response.payload.duckRestoreFadeDurationMs))
    setDriftIgnoreThresholdMs(String(state.response.payload.driftIgnoreThresholdMs))
    setScheduledStartDeliveryBoundMs(String(state.response.payload.scheduledStartDeliveryBoundMs))
    setScheduledStartMarginMs(String(state.response.payload.scheduledStartMarginMs))
    setMultisyncFallbackWindowMs(String(state.response.payload.multisyncFallbackWindowMs))
    setMultisyncStartLeadMs(String(state.response.payload.multisyncStartLeadMs))
    setLtcFrameRate(state.response.payload.ltcFrameRate)
    setLtcDefaultStartOffset(state.response.payload.ltcDefaultStartOffset)
    setDirty(false)
    setSaveError(null)
  }

  const save = () => {
    if (state.kind !== 'loaded') return
    const fadeMs = Number(fadeDurationMs)
    const maxGain = Number(maxBackgroundGainDb)
    const duckGain = Number(duckTargetGainDb)
    const duckFadeMs = Number(duckFadeDurationMs)
    const duckRestoreFadeMs = Number(duckRestoreFadeDurationMs)
    const driftMs = Number(driftIgnoreThresholdMs)
    const deliveryBoundMs = Number(scheduledStartDeliveryBoundMs)
    const marginMs = Number(scheduledStartMarginMs)
    const fallbackWindowMs = Number(multisyncFallbackWindowMs)
    const startLeadMs = Number(multisyncStartLeadMs)
    if (!Number.isFinite(fadeMs) || fadeMs < 0) {
      setSaveError('Default fade duration must be a non-negative number of milliseconds.')
      return
    }
    if (!Number.isFinite(maxGain)) {
      setSaveError('Max background gain must be a number of decibels.')
      return
    }
    if (!Number.isFinite(duckGain) || duckGain >= 0) {
      setSaveError('Duck target gain must be negative.')
      return
    }
    if (!Number.isInteger(duckFadeMs) || duckFadeMs <= 0) {
      setSaveError('Duck fade duration must be a whole number of milliseconds, greater than zero.')
      return
    }
    if (!Number.isInteger(duckRestoreFadeMs) || duckRestoreFadeMs <= 0) {
      setSaveError('Duck restore fade duration must be a whole number of milliseconds, greater than zero.')
      return
    }
    if (!Number.isFinite(driftMs) || driftMs < 0) {
      setSaveError('Drift ignore threshold must be a non-negative number of milliseconds.')
      return
    }
    if (!/^\d{2}:\d{2}:\d{2}:\d{2}$/.test(ltcDefaultStartOffset)) {
      setSaveError('LTC default start offset must be HH:MM:SS:FF.')
      return
    }
    if (!Number.isInteger(deliveryBoundMs) || deliveryBoundMs < 0 || deliveryBoundMs > 60000) {
      setSaveError('Scheduled start delivery bound must be a whole number of milliseconds between 0 and 60000.')
      return
    }
    if (!Number.isInteger(marginMs) || marginMs < 0 || marginMs > 60000) {
      setSaveError('Scheduled start margin must be a whole number of milliseconds between 0 and 60000.')
      return
    }
    if (!Number.isInteger(fallbackWindowMs) || fallbackWindowMs < 0 || fallbackWindowMs > 10000) {
      setSaveError('MultiSync fallback window must be a whole number of milliseconds between 0 and 10000.')
      return
    }
    if (!Number.isInteger(startLeadMs) || startLeadMs < 0 || startLeadMs > 5000) {
      setSaveError('MultiSync start lead must be a whole number of milliseconds between 0 and 5000.')
      return
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: getAudioSettingsConfig,
      write: () =>
        putAudioSettingsConfig({
          defaultFadeCurve: 'linear',
          defaultFadeDurationMs: fadeMs,
          defaultMaxBackgroundGainDb: maxGain,
          duckTargetGainDb: duckGain,
          duckFadeDurationMs: duckFadeMs,
          duckRestoreFadeDurationMs: duckRestoreFadeMs,
          driftIgnoreThresholdMs: driftMs,
          ltcFrameRate,
          ltcDefaultStartOffset,
          scheduledStartDeliveryBoundMs: deliveryBoundMs,
          scheduledStartMarginMs: marginMs,
          multisyncFallbackWindowMs: fallbackWindowMs,
          multisyncStartLeadMs: startLeadMs,
        }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response })
          setDirty(false)
          return
        }
        if (outcome.kind === 'stale') {
          setStale(outcome)
          return
        }
        setSaveError(outcome.reason)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Audio <span className="sm-faint">/</span> Installation defaults</p>
      <h2 className="sm-section__title">Audio behaviour every node starts from</h2>

      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for audio defaults." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <>
          <Section id="st-fades" title="Fades and gain">
            <div className="sm-grid sm-grid--auto">
              <Field label="Default fade curve">
                {(props) => (
                  <p {...props} className="sm-input sm-data sm-muted">
                    linear
                  </p>
                )}
              </Field>
              <Field label="Default fade duration (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    value={fadeDurationMs}
                    onChange={(e) => {
                      setFadeDurationMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Max background gain (dB)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    value={maxBackgroundGainDb}
                    onChange={(e) => {
                      setMaxBackgroundGainDb(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Duck target gain (dB)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    value={duckTargetGainDb}
                    onChange={(e) => {
                      setDuckTargetGainDb(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Duck fade duration (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={1}
                    value={duckFadeDurationMs}
                    onChange={(e) => {
                      setDuckFadeDurationMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Duck restore fade duration (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={1}
                    value={duckRestoreFadeDurationMs}
                    onChange={(e) => {
                      setDuckRestoreFadeDurationMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
          </Section>

          <Section id="st-timing" title="Timing and LTC">
            <div className="sm-grid sm-grid--auto">
              <Field label="Drift ignore threshold (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    value={driftIgnoreThresholdMs}
                    onChange={(e) => {
                      setDriftIgnoreThresholdMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Scheduled start delivery bound (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    max={60000}
                    value={scheduledStartDeliveryBoundMs}
                    onChange={(e) => {
                      setScheduledStartDeliveryBoundMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Scheduled start margin (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    max={60000}
                    value={scheduledStartMarginMs}
                    onChange={(e) => {
                      setScheduledStartMarginMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="MultiSync fallback window (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    max={10000}
                    value={multisyncFallbackWindowMs}
                    onChange={(e) => {
                      setMultisyncFallbackWindowMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="MultiSync start lead (ms)">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    max={5000}
                    value={multisyncStartLeadMs}
                    onChange={(e) => {
                      setMultisyncStartLeadMs(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="LTC frame rate">
                {(props) => (
                  <Select
                    {...props}
                    value={ltcFrameRate}
                    onChange={(e) => {
                      setLtcFrameRate(e.target.value as (typeof LTC_FRAME_RATES)[number])
                      setDirty(true)
                    }}
                  >
                    {LTC_FRAME_RATES.map((rate) => (
                      <option key={rate} value={rate}>
                        {rate}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              <Field label="LTC default start offset">
                {(props) => (
                  <Input
                    {...props}
                    value={ltcDefaultStartOffset}
                    onChange={(e) => {
                      setLtcDefaultStartOffset(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
          </Section>
        </>
      )}

      <ButtonRow>
        <Button
          variant="primary"
          onClick={save}
          disabled={!dirty || saving || state.kind !== 'loaded' || !gate.allowed}
          title={gate.allowed ? undefined : gate.reason}
        >
          {saving ? 'Saving…' : 'Save defaults'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving}>
          Discard changes
        </Button>
        {state.kind === 'loaded' && (
          <div className="sm-push-end">
            <RevisionHistory fetch={getAudioSettingsConfigRevisions} reloadKey={attempt} />
          </div>
        )}
      </ButtonRow>
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            setAttempt((n) => n + 1)
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
    </>
  )
}
