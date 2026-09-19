import { useEffect, useState } from 'react'
import {
  getRenderSettingsConfig,
  getRenderSettingsConfigRevisions,
  getWeatherDelayConfig,
  getWeatherDelayConfigRevisions,
  listAssets,
  putRenderSettingsConfig,
  putWeatherDelayConfig,
  type ConfigWeatherDelayPowerGroupPayload,
  type RenderSettingsConfigResponse,
  type WeatherDelayConfigResponse,
} from '../api'
import { Button, ButtonRow, ChoiceGroup, Field, Input, RevisionHistory, RuledStrip, Section, Select } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
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
            detail="On failure, output goes black rather than holding the last frame. Restarts are bounded, so a broken pipeline stops retrying instead of flickering at the audience."
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
          <div className="sm-push-end">
            <RevisionHistory fetch={getRenderSettingsConfigRevisions} reloadKey={attempt} />
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
      <p className="sm-small sm-faint">Saving does not restart anything now. It changes what happens the next time a pipeline fails.</p>

      <WeatherDelaySettingsSection />
    </>
  )
}

type WeatherDelayLoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: WeatherDelayConfigResponse }
  | { kind: 'failed'; reason: string }

type AssetOption = { id: string; label: string }

type PowerGroupDraft = {
  key: string
  id: string
  label: string
  fppInstanceIds: string[]
  resolumeInstanceIds: string[]
  renderNodeIds: string[]
  heartbeatEnabled: boolean
  heartbeatIntervalSeconds: string
}

function powerGroupDraftFrom(group: ConfigWeatherDelayPowerGroupPayload, key: string): PowerGroupDraft {
  return {
    key,
    id: group.id,
    label: group.label ?? '',
    fppInstanceIds: group.fppInstanceIds ?? [],
    resolumeInstanceIds: group.resolumeInstanceIds ?? [],
    renderNodeIds: group.renderNodeIds ?? [],
    heartbeatEnabled: group.heartbeat?.enabled ?? false,
    heartbeatIntervalSeconds: group.heartbeat?.intervalSeconds === undefined ? '' : String(group.heartbeat.intervalSeconds),
  }
}

function powerGroupPayloadFrom(draft: PowerGroupDraft): ConfigWeatherDelayPowerGroupPayload {
  const interval = Number(draft.heartbeatIntervalSeconds)
  return {
    id: draft.id.trim(),
    ...(draft.label.trim() === '' ? {} : { label: draft.label.trim() }),
    ...(draft.fppInstanceIds.length === 0 ? {} : { fppInstanceIds: draft.fppInstanceIds }),
    ...(draft.resolumeInstanceIds.length === 0 ? {} : { resolumeInstanceIds: draft.resolumeInstanceIds }),
    ...(draft.renderNodeIds.length === 0 ? {} : { renderNodeIds: draft.renderNodeIds }),
    ...(draft.heartbeatEnabled || draft.heartbeatIntervalSeconds.trim() !== ''
      ? {
          heartbeat: {
            enabled: draft.heartbeatEnabled,
            ...(draft.heartbeatIntervalSeconds.trim() !== '' && Number.isFinite(interval) ? { intervalSeconds: interval } : {}),
          },
        }
      : {}),
  }
}

/** A generic asset-id picker over this coordinator's own audio assets, shared by the delay alert and the cancel-night alert. */
function WeatherAlertAssetPicker({
  label,
  help,
  assets,
  value,
  onChange,
}: {
  label: string
  help: string
  assets: readonly AssetOption[]
  value: string
  onChange: (id: string) => void
}) {
  return (
    <Field label={label} help={help}>
      {(props) => (
        <Select {...props} value={value} onChange={(e) => onChange(e.target.value)}>
          <option value="">No alert asset</option>
          {assets.map((asset) => (
            <option key={asset.id} value={asset.id}>
              {asset.label}
            </option>
          ))}
        </Select>
      )}
    </Field>
  )
}

let powerGroupKeySeq = 0
function nextPowerGroupKey(): string {
  powerGroupKeySeq += 1
  return `wd-pg-${powerGroupKeySeq}`
}

/**
 * ADR-053 decision 13's `show.weatherdelay` config: the alert, its power
 * groups, and its automatic triggers, every field optional with a working
 * default. Kept as its own load/save cycle, independent of render
 * recovery's above, since the two config kinds share nothing.
 */
function WeatherDelaySettingsSection() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<WeatherDelayLoadState>({ kind: 'loading' })
  const [assets, setAssets] = useState<AssetOption[]>([])

  const [delayAssetId, setDelayAssetId] = useState('')
  const [cancelNightAssetId, setCancelNightAssetId] = useState('')
  const [repeatCount, setRepeatCount] = useState('')
  const [alertNodeIds, setAlertNodeIds] = useState<string[]>([])
  const [powerGroups, setPowerGroups] = useState<PowerGroupDraft[]>([])
  const [answerWindowSeconds, setAnswerWindowSeconds] = useState('')
  const [cancelAnswerWindowSeconds, setCancelAnswerWindowSeconds] = useState('')
  const [restartMinutes, setRestartMinutes] = useState('')

  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<WeatherDelayConfigResponse>, { kind: 'stale' }> | null>(null)

  const load = (response: WeatherDelayConfigResponse) => {
    setDelayAssetId(response.payload.alert?.delayAssetId ?? '')
    setCancelNightAssetId(response.payload.alert?.cancelNightAssetId ?? '')
    setRepeatCount(response.payload.alert?.repeatCount === undefined ? '' : String(response.payload.alert.repeatCount))
    setAlertNodeIds(response.payload.alert?.nodeIds ?? [])
    setPowerGroups((response.payload.powerGroups ?? []).map((group) => powerGroupDraftFrom(group, nextPowerGroupKey())))
    setAnswerWindowSeconds(response.payload.triggers?.answerWindowSeconds === undefined ? '' : String(response.payload.triggers.answerWindowSeconds))
    setCancelAnswerWindowSeconds(
      response.payload.triggers?.cancelAnswerWindowSeconds === undefined ? '' : String(response.payload.triggers.cancelAnswerWindowSeconds),
    )
    setRestartMinutes(response.payload.triggers?.restartMinutes === undefined ? '' : String(response.payload.triggers.restartMinutes))
    setDirty(false)
  }

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getWeatherDelayConfig()
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
        load(response)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [attempt])

  useEffect(() => {
    let cancelled = false
    listAssets()
      .then((response) => {
        if (!cancelled) {
          setAssets(
            response.assets
              .filter((asset) => asset.mediaType === 'audio')
              .map((asset) => ({ id: asset.id, label: asset.runtimeFilename !== '' ? asset.runtimeFilename : asset.id })),
          )
        }
      })
      .catch(() => {
        // The picker degrades to an id-only fallback rather than blocking the rest of the form.
      })
    return () => {
      cancelled = true
    }
  }, [])

  const fppOptions = model.fpp.map((entry) => ({ value: entry.instanceId, label: entry.instanceId }))
  const resolumeOptions = model.resolume.map((entry) => ({ value: entry.instanceId, label: entry.instanceId }))
  const nodeOptions = model.nodes.map((node) => ({ value: node.nodeId, label: node.label !== null && node.label !== '' ? `${node.nodeId} · ${node.label}` : node.nodeId }))

  const discard = () => {
    if (state.kind !== 'loaded') return
    load(state.response)
    setSaveError(null)
  }

  const addPowerGroup = () => {
    setPowerGroups((groups) => [
      ...groups,
      { key: nextPowerGroupKey(), id: '', label: '', fppInstanceIds: [], resolumeInstanceIds: [], renderNodeIds: [], heartbeatEnabled: false, heartbeatIntervalSeconds: '' },
    ])
    setDirty(true)
  }

  const removePowerGroup = (key: string) => {
    setPowerGroups((groups) => groups.filter((g) => g.key !== key))
    setDirty(true)
  }

  const updatePowerGroup = (key: string, patch: Partial<PowerGroupDraft>) => {
    setPowerGroups((groups) => groups.map((g) => (g.key === key ? { ...g, ...patch } : g)))
    setDirty(true)
  }

  const save = () => {
    if (state.kind !== 'loaded') return
    const ids = powerGroups.map((g) => g.id.trim())
    if (ids.some((id) => id === '')) {
      setSaveError('Every power group needs an id.')
      return
    }
    if (new Set(ids).size !== ids.length) {
      setSaveError('Power group ids must be unique.')
      return
    }
    const repeat = repeatCount.trim() === '' ? undefined : Number(repeatCount)
    if (repeat !== undefined && (!Number.isFinite(repeat) || repeat < 0)) {
      setSaveError('Repeat count must be a non-negative number.')
      return
    }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: getWeatherDelayConfig,
      write: () =>
        putWeatherDelayConfig({
          alert: {
            ...(delayAssetId === '' ? {} : { delayAssetId }),
            ...(cancelNightAssetId === '' ? {} : { cancelNightAssetId }),
            ...(repeat === undefined ? {} : { repeatCount: repeat }),
            ...(alertNodeIds.length === 0 ? {} : { nodeIds: alertNodeIds }),
          },
          powerGroups: powerGroups.map(powerGroupPayloadFrom),
          triggers: {
            ...(answerWindowSeconds.trim() === '' ? {} : { answerWindowSeconds: Number(answerWindowSeconds) }),
            ...(cancelAnswerWindowSeconds.trim() === '' ? {} : { cancelAnswerWindowSeconds: Number(cancelAnswerWindowSeconds) }),
            ...(restartMinutes.trim() === '' ? {} : { restartMinutes: Number(restartMinutes) }),
          },
        }),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response })
          load(outcome.response)
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
      <Section
        id="st-weatherdelay"
        title="Weather delay"
        detail="ADR-053's optional alert, power groups, and automatic triggers. Every field is optional; an installation that does not want this leaves it unconfigured."
      >
        {state.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for weather delay settings." />
        ) : state.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
        ) : (
          <>
            {state.response.revision === 0 && state.response.source === 'default' && (
              <RuledStrip absence="unobserved" label="Default" fact="Nothing has been written for weather delay yet. These are the coordinator's own defaults." />
            )}

            <Section id="st-weatherdelay-alert" title="Alert">
              <div className="sm-grid sm-grid--auto">
                <WeatherAlertAssetPicker
                  label="Delay alert"
                  help="Plays when Weather delay starts."
                  assets={assets}
                  value={delayAssetId}
                  onChange={(id) => {
                    setDelayAssetId(id)
                    setDirty(true)
                  }}
                />
                <WeatherAlertAssetPicker
                  label="Cancel-night alert"
                  help="Plays when Cancel night starts."
                  assets={assets}
                  value={cancelNightAssetId}
                  onChange={(id) => {
                    setCancelNightAssetId(id)
                    setDirty(true)
                  }}
                />
                <Field label="Repeat count" help="How many times the alert plays. Defaults to 10.">
                  {(props) => (
                    <Input
                      {...props}
                      type="number"
                      min={0}
                      value={repeatCount}
                      onChange={(e) => {
                        setRepeatCount(e.target.value)
                        setDirty(true)
                      }}
                    />
                  )}
                </Field>
              </div>
              <ChoiceGroup
                label="Target nodes"
                help="Empty means every declared audio node."
                options={nodeOptions}
                value={alertNodeIds}
                onChange={(value) => {
                  setAlertNodeIds(value)
                  setDirty(true)
                }}
              />
            </Section>

            <Section
              id="st-weatherdelay-power"
              title="Power groups"
              detail="ShowMesh never switches power itself. Each group's dark heartbeat is for an installation's own power controller to watch."
              aside={
                <Button variant="quiet" size="compact" onClick={addPowerGroup}>
                  Add power group
                </Button>
              }
            >
              {powerGroups.length === 0 ? (
                <RuledStrip absence="empty" label="No power groups" fact="No power group is configured." />
              ) : (
                powerGroups.map((group) => (
                  <div key={group.key} className="sm-panel" style={{ display: 'grid', gap: 'var(--s-3)', marginBottom: 'var(--s-3)' }}>
                    <div className="sm-grid sm-grid--auto">
                      <Field label="Id">
                        {(props) => (
                          <Input {...props} value={group.id} onChange={(e) => updatePowerGroup(group.key, { id: e.target.value })} />
                        )}
                      </Field>
                      <Field label="Label">
                        {(props) => (
                          <Input {...props} value={group.label} onChange={(e) => updatePowerGroup(group.key, { label: e.target.value })} />
                        )}
                      </Field>
                      <Field label="Heartbeat interval (seconds)">
                        {(props) => (
                          <Input
                            {...props}
                            type="number"
                            min={1}
                            value={group.heartbeatIntervalSeconds}
                            onChange={(e) => updatePowerGroup(group.key, { heartbeatIntervalSeconds: e.target.value })}
                          />
                        )}
                      </Field>
                    </div>
                    <label className="sm-choice">
                      <input
                        type="checkbox"
                        checked={group.heartbeatEnabled}
                        onChange={(e) => updatePowerGroup(group.key, { heartbeatEnabled: e.target.checked })}
                      />
                      <span>Publish this group's dark heartbeat</span>
                    </label>
                    <ChoiceGroup
                      label="FPP instances"
                      options={fppOptions}
                      value={group.fppInstanceIds}
                      onChange={(value) => updatePowerGroup(group.key, { fppInstanceIds: value })}
                    />
                    <ChoiceGroup
                      label="Resolume instances"
                      options={resolumeOptions}
                      value={group.resolumeInstanceIds}
                      onChange={(value) => updatePowerGroup(group.key, { resolumeInstanceIds: value })}
                    />
                    <ChoiceGroup
                      label="Render nodes"
                      options={nodeOptions}
                      value={group.renderNodeIds}
                      onChange={(value) => updatePowerGroup(group.key, { renderNodeIds: value })}
                    />
                    <ButtonRow>
                      <Button variant="quiet" size="compact" onClick={() => removePowerGroup(group.key)}>
                        Remove power group
                      </Button>
                    </ButtonRow>
                  </div>
                ))
              )}
            </Section>

            <Section id="st-weatherdelay-triggers" title="Automatic triggers" detail="A trigger may start a delay; it may never end one.">
              <div className="sm-grid sm-grid--auto">
                <Field label="Answer window (seconds)" help="How long a trigger waits for an operator before starting a delay on its own. Defaults to 30.">
                  {(props) => (
                    <Input
                      {...props}
                      type="number"
                      min={1}
                      value={answerWindowSeconds}
                      onChange={(e) => {
                        setAnswerWindowSeconds(e.target.value)
                        setDirty(true)
                      }}
                    />
                  )}
                </Field>
                <Field label="Cancel answer window (seconds)" help="The longer window used when too little of the night would remain. Defaults to 180.">
                  {(props) => (
                    <Input
                      {...props}
                      type="number"
                      min={1}
                      value={cancelAnswerWindowSeconds}
                      onChange={(e) => {
                        setCancelAnswerWindowSeconds(e.target.value)
                        setDirty(true)
                      }}
                    />
                  )}
                </Field>
                <Field label="Restart minutes" help="Below this much night remaining after a warning expires, the question becomes delay or cancel. Defaults to 15.">
                  {(props) => (
                    <Input
                      {...props}
                      type="number"
                      min={1}
                      value={restartMinutes}
                      onChange={(e) => {
                        setRestartMinutes(e.target.value)
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
            {saving ? 'Saving…' : 'Save weather delay'}
          </Button>
          <Button variant="quiet" onClick={discard} disabled={!dirty || saving}>
            Discard changes
          </Button>
          {state.kind === 'loaded' && (
            <div className="sm-push-end">
              <RevisionHistory fetch={getWeatherDelayConfigRevisions} reloadKey={attempt} />
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
      </Section>
    </>
  )
}
