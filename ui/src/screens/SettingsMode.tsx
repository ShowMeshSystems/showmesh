import { useEffect, useState } from 'react'
import {
  getCurrentNightSession,
  getShowModeConfig,
  getShowModeConfigRevisions,
  putShowModeConfig,
  type ConfigShowModePayload,
  type NightSessionState,
  type ShowModeConfigResponse,
} from '../api'
import { Button, ButtonRow, RevisionHistory, RuledStrip, Section, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatDateClock } from '../domain/time'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { liveCycle } from './settingsModel'

/**
 * `model.nightSession` is only ever set by a `nightSession.changed` frame, so
 * a tab that only read the model would never show the in-progress warning.
 * Seeded the way Dashboard seeds it; a streamed session still wins.
 */
function useNightSession(): NightSessionState | null {
  const model = useModelContext()
  const [seeded, setSeeded] = useState<NightSessionState | null>(null)

  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((response) => {
        if (!cancelled) setSeeded(response.session)
      })
      .catch(() => {
        // A failed seed leaves the warning off, which is what an unread
        // session is. It never claims a show is or is not in progress.
      })
    return () => {
      cancelled = true
    }
  }, [])

  return model.nightSession ?? seeded
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: ShowModeConfigResponse }
  | { kind: 'failed'; reason: string }

const MODE_OPTIONS: readonly { value: ConfigShowModePayload['mode']; title: string; consequence: string }[] = [
  {
    value: 'show',
    title: 'Show mode',
    consequence: 'There is an audience. When something cannot be resolved, output is held rather than guessed, and destructive changes ask twice.',
  },
  {
    value: 'program',
    title: 'Program mode',
    consequence: 'Setup and authoring. Nothing assumes an audience is watching, so reconfiguration is expected rather than guarded.',
  },
]

export function SettingsMode() {
  const model = useModelContext()
  const nightSession = useNightSession()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [requestedMode, setRequestedMode] = useState<ConfigShowModePayload['mode']>('show')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<ShowModeConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getShowModeConfig()
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
        setRequestedMode(response.payload.mode)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  const cycle = liveCycle(nightSession)

  const apply = () => {
    if (state.kind !== 'loaded') return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: getShowModeConfig,
      write: () => putShowModeConfig({ mode: requestedMode }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response })
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

  const alreadyActive = state.kind === 'loaded' && state.response.payload.mode === requestedMode

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Mode</p>
      <h2 className="sm-section__title">What this installation is for right now</h2>
      <p className="sm-page__lede">Installation-wide, and every screen reads it. Changing it creates a coordinator revision attributed to you.</p>

      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the current mode." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <>
          <Section id="st-mode" title="Mode">
            <fieldset className="sm-grid" style={{ border: 0, padding: 0, margin: 0 }}>
              <legend className="sm-sr-only">Mode</legend>
              {MODE_OPTIONS.map((option) => {
                const isActive = state.response.payload.mode === option.value
                return (
                  <label key={option.value} className={`sm-panel sm-option${requestedMode === option.value ? ' sm-option--selected' : ''}`}>
                    <div className="sm-inline-row">
                      <input
                        type="radio"
                        name="show-mode"
                        value={option.value}
                        checked={requestedMode === option.value}
                        onChange={() => setRequestedMode(option.value)}
                      />
                      <span className="sm-subhead">{option.title}</span>
                      {isActive && <StatusPair tone="good" label="Active" />}
                    </div>
                    <p className="sm-small sm-muted">{option.consequence}</p>
                  </label>
                )
              })}
            </fieldset>
          </Section>

          {cycle !== null && (
            <RuledStrip
              absence="stale"
              label="Show in progress"
              fact={`Cycle ${cycle.cycle} is live.`}
              detail="Switching to Program mode now is allowed, but it stops treating the audience as present."
            />
          )}

          <ButtonRow>
            <Button
              variant="primary"
              onClick={apply}
              disabled={alreadyActive || saving || !gate.allowed}
              title={!gate.allowed ? gate.reason : alreadyActive ? `${requestedMode === 'show' ? 'Show' : 'Program'} mode is already active, so there is nothing to apply.` : undefined}
            >
              {saving ? 'Applying…' : 'Apply mode'}
            </Button>
            {alreadyActive && (
              <span className="sm-small sm-faint">
                {requestedMode === 'show' ? 'Show' : 'Program'} mode is already active, so there is nothing to apply.
              </span>
            )}
          </ButtonRow>
          <p className="sm-small sm-muted sm-stack-3">{state.response.resolumeWebSocketEffect}</p>
          <p className="sm-small sm-muted sm-stack-3">
            {state.response.cueActivationPin.effect}
            {state.response.cueActivationPin.pinned && (
              <>
                {' '}Pinned to show <span className="sm-data">{state.response.cueActivationPin.show}</span>, generation{' '}
                <span className="sm-data">{state.response.cueActivationPin.generation}</span>
                {state.response.cueActivationPin.pinnedAt !== undefined &&
                  `, since ${formatDateClock(state.response.cueActivationPin.pinnedAt) ?? 'an unrecorded time'}`}
                .
              </>
            )}
          </p>
          <p className="sm-small sm-muted sm-stack-3">
            Playlist mismatch handling is expected to follow this setting rather than being configured per playlist.
            That wiring does not exist yet. Today the per-playlist control on Shows is what takes effect.
          </p>
        </>
      )}

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

      <RevisionHistory fetch={getShowModeConfigRevisions} reloadKey={attempt} />
    </>
  )
}
