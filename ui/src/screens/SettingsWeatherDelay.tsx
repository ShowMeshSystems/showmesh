import { useEffect, useState } from 'react'
import {
  getWeatherDelayConfig,
  getWeatherDelayConfigRevisions,
  listAssets,
  putWeatherDelayConfig,
  type ConfigWeatherDelayNWSTriggerPayload,
  type ConfigWeatherDelayPowerGroupPayload,
  type WeatherDelayConfigResponse,
} from '../api'
import { Button, ButtonRow, Choice, ChoiceGroup, Field, Input, RevisionHistory, RuledStrip, Section, Select } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { useWeatherDelay } from '../app/WeatherDelayContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'

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
          {value !== '' && !assets.some((asset) => asset.id === value) && <option value={value}>{value}</option>}
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
 * default. One load and save cycle for this config kind alone.
 */
export function SettingsWeatherDelay() {
  const model = useModelContext()
  const weatherDelay = useWeatherDelay()
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
  const [dismissQuietMinutes, setDismissQuietMinutes] = useState('')
  const [nwsEnabled, setNwsEnabled] = useState(false)
  const [nwsLatitude, setNwsLatitude] = useState('')
  const [nwsLongitude, setNwsLongitude] = useState('')
  const [nwsContact, setNwsContact] = useState('')
  const [nwsPollSeconds, setNwsPollSeconds] = useState('')
  const [nwsEventTypes, setNwsEventTypes] = useState('')

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
    setDismissQuietMinutes(
      response.payload.triggers?.dismissQuietMinutes === undefined ? '' : String(response.payload.triggers.dismissQuietMinutes),
    )
    const nws = response.payload.triggers?.nws
    setNwsEnabled(nws?.enabled ?? false)
    setNwsLatitude(nws?.latitude === undefined ? '' : String(nws.latitude))
    setNwsLongitude(nws?.longitude === undefined ? '' : String(nws.longitude))
    setNwsContact(nws?.contact ?? '')
    setNwsPollSeconds(nws?.pollSeconds === undefined ? '' : String(nws.pollSeconds))
    setNwsEventTypes((nws?.eventTypes ?? []).join(', '))
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
        // The picker still shows a saved asset by its id rather than blocking the rest of the form.
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
    if (nwsEnabled && nwsContact.trim() === '') {
      setSaveError('A contact is required to enable the weather service poller.')
      return
    }
    const nwsPayloadPresent =
      nwsEnabled || nwsLatitude.trim() !== '' || nwsLongitude.trim() !== '' || nwsContact.trim() !== '' || nwsPollSeconds.trim() !== '' || nwsEventTypes.trim() !== ''
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
            ...(dismissQuietMinutes.trim() === '' ? {} : { dismissQuietMinutes: Number(dismissQuietMinutes) }),
            ...(nwsPayloadPresent
              ? {
                  nws: {
                    enabled: nwsEnabled,
                    ...(nwsLatitude.trim() === '' ? {} : { latitude: Number(nwsLatitude) }),
                    ...(nwsLongitude.trim() === '' ? {} : { longitude: Number(nwsLongitude) }),
                    ...(nwsContact.trim() === '' ? {} : { contact: nwsContact.trim() }),
                    ...(nwsPollSeconds.trim() === '' ? {} : { pollSeconds: Number(nwsPollSeconds) }),
                    ...(nwsEventTypes.trim() === ''
                      ? {}
                      : { eventTypes: nwsEventTypes.split(',').map((t) => t.trim()).filter((t) => t !== '') }),
                  } satisfies ConfigWeatherDelayNWSTriggerPayload,
                }
              : {}),
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
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Weather delay</p>
      <h2 className="sm-section__title">What happens when weather stops the show</h2>
      {state.kind === 'loading' ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for weather delay settings." />
      ) : state.kind === 'failed' ? (
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      ) : (
        <>
          {state.response.revision === 0 && state.response.source === 'default' && (
            <RuledStrip absence="unobserved" label="Default" fact="Nothing saved yet. These are the defaults." />
          )}

          <Section id="st-weatherdelay-alert" title="Alert" detail="The alert, power groups and triggers are each optional.">
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
            detail="ShowMesh never switches power. Your power controller watches each group."
            aside={
              <Button variant="quiet" size="compact" onClick={addPowerGroup}>
                Add power group
              </Button>
            }
          >
            {powerGroups.length === 0 ? (
              <RuledStrip absence="empty" label="No power groups" fact="No power groups." />
            ) : (
              powerGroups.map((group) => (
                <div key={group.key} className="sm-panel sm-grid sm-stack-3">
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
                  <Choice
                    type="checkbox"
                    label="Publish this group's dark heartbeat"
                    checked={group.heartbeatEnabled}
                    onChange={(e) => updatePowerGroup(group.key, { heartbeatEnabled: e.target.checked })}
                  />
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
              <Field label="Answer window (seconds)" help="How long a trigger waits for an answer before it delays. Default 30.">
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
              <Field label="Cancel answer window (seconds)" help="The wait when the night is nearly over. Default 180.">
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
              <Field label="Restart minutes" help="With less night left than this, the question offers cancel. Default 15.">
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
              <Field label="Dismiss quiet minutes" help="How long Dismiss silences the same warning. 0 is no silence. Default 30.">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={0}
                    value={dismissQuietMinutes}
                    onChange={(e) => {
                      setDismissQuietMinutes(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
          </Section>

          <Section
            id="st-weatherdelay-nws"
            title="Weather service poller"
            detail="Reads US National Weather Service alerts. Off by default."
          >
            <Choice
              type="checkbox"
              label="Enable the weather service poller"
              checked={nwsEnabled}
              onChange={(e) => {
                setNwsEnabled(e.target.checked)
                setDirty(true)
              }}
            />
            <div className="sm-grid sm-grid--auto">
              <Field label="Latitude">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    value={nwsLatitude}
                    onChange={(e) => {
                      setNwsLatitude(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Longitude">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    value={nwsLongitude}
                    onChange={(e) => {
                      setNwsLongitude(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Contact" help="Required to enable: that API's own policy requires a contact.">
                {(props) => (
                  <Input
                    {...props}
                    value={nwsContact}
                    onChange={(e) => {
                      setNwsContact(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Poll seconds" help="Defaults to 60.">
                {(props) => (
                  <Input
                    {...props}
                    type="number"
                    min={1}
                    value={nwsPollSeconds}
                    onChange={(e) => {
                      setNwsPollSeconds(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
            <Field label="Event types" help="Comma-separated. Defaults to Tornado Warning, Severe Thunderstorm Warning.">
              {(props) => (
                <Input
                  {...props}
                  value={nwsEventTypes}
                  onChange={(e) => {
                    setNwsEventTypes(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
            {weatherDelay.state.kind === 'loaded' && (weatherDelay.state.response.sources?.length ?? 0) > 0 && (
              <div className="sm-stack-2">
                <p className="sm-small sm-faint">Trigger source health</p>
                {weatherDelay.state.response.sources?.map((source) => (
                  <p key={source.source} className="sm-small">
                    <span className="sm-data">{source.source}</span>{' '}
                    {source.enabled ? 'Enabled.' : 'Disabled.'}
                    {source.lastError !== undefined && ` ${source.lastError}`}
                  </p>
                ))}
                {weatherDelay.state.response.sourcesMessage !== undefined && (
                  <p className="sm-small sm-faint">{weatherDelay.state.response.sourcesMessage}</p>
                )}
              </div>
            )}
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
    </>
  )
}
