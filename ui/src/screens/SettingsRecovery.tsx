import { useEffect, useState } from 'react'
import { getRenderSettingsConfig, putRenderSettingsConfig, type RenderSettingsConfigResponse } from '../api'
import { Button, ButtonRow, Field, Input, RuledStrip, Section } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: RenderSettingsConfigResponse }
  | { kind: 'failed'; reason: string }

const IDLE_OPTIONS: readonly { value: 'black' | 'hold' | 'diagnostic'; title: string; consequence: string }[] = [
  { value: 'black', title: 'Black', consequence: 'Nothing is being driven, and it looks like nothing.' },
  {
    value: 'hold',
    title: 'Hold the last frame',
    consequence: 'A frozen frame is indistinguishable from a running show. Only pick this if someone is watching the monitor.',
  },
  {
    value: 'diagnostic',
    title: 'Diagnostic pattern',
    consequence: 'Unmistakably not show content. Useful during setup, never with an audience present.',
  },
]

export function SettingsRecovery() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })

  const [idleOutput, setIdleOutput] = useState<'black' | 'hold' | 'diagnostic'>('black')
  const [initialDelaySeconds, setInitialDelaySeconds] = useState('')
  const [maxDelaySeconds, setMaxDelaySeconds] = useState('')
  const [maxConsecutiveFastFailures, setMaxConsecutiveFastFailures] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<RenderSettingsConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getRenderSettingsConfig()
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
        setIdleOutput(response.payload.idleOutput)
        setInitialDelaySeconds(String(response.payload.restartPolicy.initialDelaySeconds))
        setMaxDelaySeconds(String(response.payload.restartPolicy.maxDelaySeconds))
        setMaxConsecutiveFastFailures(String(response.payload.restartPolicy.maxConsecutiveFastFailures))
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
    setIdleOutput(state.response.payload.idleOutput)
    setInitialDelaySeconds(String(state.response.payload.restartPolicy.initialDelaySeconds))
    setMaxDelaySeconds(String(state.response.payload.restartPolicy.maxDelaySeconds))
    setMaxConsecutiveFastFailures(String(state.response.payload.restartPolicy.maxConsecutiveFastFailures))
    setDirty(false)
    setSaveError(null)
  }

  const save = () => {
    if (state.kind !== 'loaded') return
    const initial = Number(initialDelaySeconds)
    const max = Number(maxDelaySeconds)
    const fastFailures = Number(maxConsecutiveFastFailures)
    if (!Number.isInteger(initial) || initial < 1 || initial > 60) {
      setSaveError('Initial delay must be a whole number of seconds from 1 to 60.')
      return
    }
    if (!Number.isInteger(max) || max < 1 || max > 300) {
      setSaveError('Max delay must be a whole number of seconds from 1 to 300.')
      return
    }
    if (!Number.isInteger(fastFailures) || fastFailures < 1 || fastFailures > 20) {
      setSaveError('Fast failures before giving up must be a whole number from 1 to 20.')
      return
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: getRenderSettingsConfig,
      write: () =>
        putRenderSettingsConfig({
          idleOutput,
          restartPolicy: { initialDelaySeconds: initial, maxDelaySeconds: max, maxConsecutiveFastFailures: fastFailures },
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
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Render recovery</p>
      <h2 className="sm-section__title">What a render node shows when nothing is driving it</h2>

      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for render recovery settings." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <>
          <RuledStrip
            absence="stale"
            label="Read this first"
            fact="Idle output is what the audience sees when a pipeline has failed."
            detail="Black is honest; a held last frame can look like a working show for an hour. Restart bounds exist so a broken pipeline stops retrying forever rather than flickering at the audience."
          />
          {state.response.revision === 0 && state.response.source === 'default' && (
            <RuledStrip absence="unobserved" label="Default" fact="Nothing has been written for render recovery yet. These are the coordinator's own defaults." />
          )}

          <Section id="st-idle" title="Idle output" detail="What a render pipeline puts out when it has nothing to render.">
            <fieldset className="sm-grid" style={{ border: 0, padding: 0, margin: 0 }}>
              <legend className="sm-sr-only">Idle output</legend>
              {IDLE_OPTIONS.map((option) => (
                <label key={option.value} className={`sm-panel sm-option${idleOutput === option.value ? ' sm-option--selected' : ''}`}>
                  <div className="sm-inline-row">
                    <input
                      type="radio"
                      name="idle-output"
                      checked={idleOutput === option.value}
                      onChange={() => {
                        setIdleOutput(option.value)
                        setDirty(true)
                      }}
                    />
                    <span className="sm-subhead">{option.title}</span>
                    <span className="sm-data sm-small sm-faint">{option.value}</span>
                  </div>
                  <p className="sm-small sm-muted">{option.consequence}</p>
                </label>
              ))}
            </fieldset>
          </Section>

          <Section id="st-restart" title="Restart policy" detail="The pipeline supervisor backs off between restarts, and gives up rather than looping forever.">
            <div className="sm-grid sm-grid--auto">
              <Field label="Initial delay" help="1 to 60 seconds before the first retry.">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={1}
                    max={60}
                    value={initialDelaySeconds}
                    onChange={(e) => {
                      setInitialDelaySeconds(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Max delay" help="1 to 300 seconds the backoff caps at.">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={1}
                    max={300}
                    value={maxDelaySeconds}
                    onChange={(e) => {
                      setMaxDelaySeconds(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Fast failures before giving up" help="1 to 20 consecutive fast failures before the pipeline stays failed.">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={1}
                    max={20}
                    value={maxConsecutiveFastFailures}
                    onChange={(e) => {
                      setMaxConsecutiveFastFailures(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
            <p className="sm-section__footnote">{state.response.idleOutputEffectiveNote}</p>
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
          {saving ? 'Saving…' : 'Save recovery'}
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
      <p className="sm-small sm-faint">Saving does not restart anything now. It changes what happens the next time a pipeline fails.</p>
    </>
  )
}
