import { useEffect, useState } from 'react'
import { getAudioSettingsConfig, putAudioSettingsConfig, type AudioSettingsConfigResponse } from '../api'
import { Button, ButtonRow, Field, Input, RuledStrip, Section, Select } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
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
  const [driftIgnoreThresholdMs, setDriftIgnoreThresholdMs] = useState('')
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
        setDriftIgnoreThresholdMs(String(response.payload.driftIgnoreThresholdMs))
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
    setDriftIgnoreThresholdMs(String(state.response.payload.driftIgnoreThresholdMs))
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
    const driftMs = Number(driftIgnoreThresholdMs)
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
    if (!Number.isFinite(driftMs) || driftMs < 0) {
      setSaveError('Drift ignore threshold must be a non-negative number of milliseconds.')
      return
    }
    if (!/^\d{2}:\d{2}:\d{2}:\d{2}$/.test(ltcDefaultStartOffset)) {
      setSaveError('LTC default start offset must be HH:MM:SS:FF.')
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
          driftIgnoreThresholdMs: driftMs,
          ltcFrameRate,
          ltcDefaultStartOffset,
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
      <p className="sm-page__lede">
        Installation-wide. A node's own routing decides where sound goes; these decide how it behaves once it gets
        there.
      </p>

      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for audio defaults." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <>
          <Section id="st-fades" title="Fades and gain">
            <div className="sm-grid sm-grid--auto">
              <div className="sm-field">
                <span className="sm-field__label">Default fade curve</span>
                <p className="sm-input sm-data sm-muted">linear</p>
                <span className="sm-field__help">Linear is the only curve implemented. More would be an audio-engine change, not a setting.</span>
              </div>
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
              <Field label="Max background gain (dB)" help="Ceiling for the bed. A night session can lower it, never raise it.">
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
              <Field label="Duck target gain (dB)" help="Below 0. A cue may override it. Provisional, not measured.">
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
            </div>
          </Section>

          <Section id="st-timing" title="Timing and LTC">
            <div className="sm-grid sm-grid--auto">
              <Field label="Drift ignore threshold (ms)" help="Below this, drift is not corrected at all.">
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
              <Field label="LTC frame rate" help="Must match what Resolume expects, or timecode reads as drift.">
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
              <Field label="LTC default start offset" help="HH:MM:SS:FF, non-drop-frame. A cue's own offset wins over this.">
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
            <p className="sm-section__footnote">Drift is corrected at track boundaries, never by continuously changing playback rate.</p>
          </Section>

          <Section id="st-loss" title="If the audio device disappears">
            <div className="sm-panel">
              <p className="sm-body" style={{ fontWeight: 500, margin: 0 }}>Fails silent, not configurable</p>
              <p className="sm-small sm-muted sm-stack-2">
                A lost audio device produces silence rather than falling back to another route. Uncontrolled gain into
                an FM transmitter is worse than nothing, so this is a recorded exception to the local-fallback rule and
                has no toggle.
              </p>
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
          <span className="sm-small sm-muted sm-push-end">
            Active revision <span className="sm-data">{state.response.revision}</span> ·{' '}
            {state.response.createdByPrincipalName ?? 'unknown principal'} {formatClock(state.response.updatedAt) ?? 'at an unrecorded time'}
          </span>
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
