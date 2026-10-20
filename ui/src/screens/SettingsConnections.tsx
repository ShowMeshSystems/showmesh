import { useEffect, useState } from 'react'
import {
  ApiError,
  getFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  getFPPMQTTConfig,
  getFPPMQTTConfigRevisions,
  getFPPConnectSettingsConfig,
  getFPPConnectSettingsConfigRevisions,
  getFPPPairing,
  getResolumeInstancesConfig,
  getResolumeInstancesConfigRevisions,
  postFPPPairing,
  putFPPBrightnessExcludedRanges,
  putFPPEndpointsConfig,
  putFPPMQTTConfig,
  putFPPConnectSettingsConfig,
  putResolumeInstancesConfig,
  type ConfigFPPEndpoint,
  type ConfigResolumeInstance,
  type Evidence,
  type FPPBrightnessExcludedRangesResponse,
  type FPPPairingStateResponse,
} from '../api'
import { AbsenceLabel, Button, ButtonRow, Choice, DefinitionStrip, Drawer, Field, Input, NotWired, NotWiredBanner, RevisionHistory, RuledStrip, Section, Select, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { guardedCreate, guardedSave } from '../domain/save'
import { StaleWriteStrip, type StaleWrite } from './StaleWrite'
import { DisplayBrightnessPlayerReadout } from './DisplayBrightness'
import { fppHealthFor, resolumeHealthFor, hostRowsToMap, hostsMapToRows, HEALTH_TONE, type MQTTHostRow } from './settingsModel'
import { SettingsReleaseCheck } from './SystemVersions'
import { brightnessReadout, CHANNEL_RANGES_FORM_ERROR, formatChannelRanges, normalizePairingCode, pairingSentence, parseChannelRanges, type SignalFact } from './fppPairingModel'

type Load<R> = { kind: 'loading' } | { kind: 'loaded'; response: R } | { kind: 'notConfigured'; detail: string } | { kind: 'failed'; reason: string }

function useConfigLoad<R>(fetcher: () => Promise<R>, attempt: number): Load<R> {
  const [state, setState] = useState<Load<R>>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    fetcher()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'notConfigured', detail: describeApiError(err) })
          return
        }
        setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [attempt])
  return state
}

type RowOutcome = { savedAt: number } | { stale: StaleWrite } | { error: string } | null

function patchRow<T>(rows: readonly T[], index: number, patch: Partial<T>): T[] {
  return rows.map((row, i) => (i === index ? { ...row, ...patch } : row))
}

export function SettingsConnections() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const reloadAll = () => setAttempt((n) => n + 1)
  const [detailInstanceId, setDetailInstanceId] = useState<string | null>(null)

  const fppLoad = useConfigLoad(getFPPEndpointsConfig, attempt)
  const resolumeLoad = useConfigLoad(getResolumeInstancesConfig, attempt)
  const mqttLoad = useConfigLoad(getFPPMQTTConfig, attempt)

  const [endpoints, setEndpoints] = useState<ConfigFPPEndpoint[]>([])
  const [endpointsDirty, setEndpointsDirty] = useState(false)
  const [instances, setInstances] = useState<ConfigResolumeInstance[]>([])
  const [instancesDirty, setInstancesDirty] = useState(false)
  const [brokerURL, setBrokerURL] = useState('')
  const [username, setUsername] = useState('')
  const [topicPrefix, setTopicPrefix] = useState('')
  const [newPassword, setNewPassword] = useState('')
  const [hosts, setHosts] = useState<MQTTHostRow[]>([])
  const [mqttDirty, setMqttDirty] = useState(false)

  const [fppOutcome, setFppOutcome] = useState<RowOutcome>(null)
  const [resolumeOutcome, setResolumeOutcome] = useState<RowOutcome>(null)
  const [mqttOutcome, setMqttOutcome] = useState<RowOutcome>(null)
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    if (fppLoad.kind === 'loaded') {
      setEndpoints(fppLoad.response.payload.endpoints)
      setEndpointsDirty(false)
    } else if (fppLoad.kind === 'notConfigured') {
      setEndpoints([])
      setEndpointsDirty(false)
    }
  }, [fppLoad])

  useEffect(() => {
    if (resolumeLoad.kind === 'loaded') {
      setInstances(resolumeLoad.response.payload.instances)
      setInstancesDirty(false)
    } else if (resolumeLoad.kind === 'notConfigured') {
      setInstances([])
      setInstancesDirty(false)
    }
  }, [resolumeLoad])

  useEffect(() => {
    if (mqttLoad.kind === 'loaded') {
      setBrokerURL(mqttLoad.response.payload.brokerURL)
      setUsername(mqttLoad.response.payload.username)
      setTopicPrefix(mqttLoad.response.payload.topicPrefix)
      setHosts(hostsMapToRows(mqttLoad.response.payload.hosts))
      setNewPassword('')
      setMqttDirty(false)
    } else if (mqttLoad.kind === 'notConfigured') {
      setBrokerURL('')
      setUsername('')
      setTopicPrefix('')
      setHosts([])
      setNewPassword('')
      setMqttDirty(false)
    }
  }, [mqttLoad])

  const anyDirty = endpointsDirty || instancesDirty || mqttDirty

  const saveConnections = async () => {
    setSaving(true)
    setFppOutcome(null)
    setResolumeOutcome(null)
    setMqttOutcome(null)
    try {
      if (endpointsDirty) {
        const payload = { endpoints }
        try {
          if (fppLoad.kind === 'loaded') {
            const outcome = await guardedSave({
              loaded: fppLoad.response,
              read: getFPPEndpointsConfig,
              write: () => putFPPEndpointsConfig(payload),
            })
            if (outcome.kind === 'saved') {
              setEndpointsDirty(false)
              setFppOutcome({ savedAt: Date.now() })
            } else if (outcome.kind === 'stale') {
              setFppOutcome({ stale: outcome })
            } else {
              setFppOutcome({ error: outcome.reason })
            }
          } else {
            const outcome = await guardedCreate({ read: getFPPEndpointsConfig, write: () => putFPPEndpointsConfig(payload) })
            if (outcome.kind === 'created') {
              setEndpointsDirty(false)
              setFppOutcome({ savedAt: Date.now() })
            } else if (outcome.kind === 'taken') {
              setFppOutcome({ error: 'Someone else configured FPP players while this was open. Reload and start again.' })
            } else {
              setFppOutcome({ error: outcome.reason })
            }
          }
        } catch (err: unknown) {
          setFppOutcome({ error: describeApiError(err) })
        }
      }

      if (instancesDirty) {
        const payload = { instances }
        try {
          if (resolumeLoad.kind === 'loaded') {
            const outcome = await guardedSave({
              loaded: resolumeLoad.response,
              read: getResolumeInstancesConfig,
              write: () => putResolumeInstancesConfig(payload),
            })
            if (outcome.kind === 'saved') {
              setInstancesDirty(false)
              setResolumeOutcome({ savedAt: Date.now() })
            } else if (outcome.kind === 'stale') {
              setResolumeOutcome({ stale: outcome })
            } else {
              setResolumeOutcome({ error: outcome.reason })
            }
          } else {
            const outcome = await guardedCreate({ read: getResolumeInstancesConfig, write: () => putResolumeInstancesConfig(payload) })
            if (outcome.kind === 'created') {
              setInstancesDirty(false)
              setResolumeOutcome({ savedAt: Date.now() })
            } else if (outcome.kind === 'taken') {
              setResolumeOutcome({ error: 'Someone else configured Resolume while this was open. Reload and start again.' })
            } else {
              setResolumeOutcome({ error: outcome.reason })
            }
          }
        } catch (err: unknown) {
          setResolumeOutcome({ error: describeApiError(err) })
        }
      }

      if (mqttDirty) {
        const request = {
          brokerURL,
          username,
          topicPrefix,
          hosts: hostRowsToMap(hosts),
          ...(newPassword !== '' ? { password: newPassword } : {}),
        }
        try {
          if (mqttLoad.kind === 'loaded') {
            const outcome = await guardedSave({
              loaded: mqttLoad.response,
              read: getFPPMQTTConfig,
              write: () => putFPPMQTTConfig(request),
            })
            if (outcome.kind === 'saved') {
              setMqttDirty(false)
              setNewPassword('')
              setMqttOutcome({ savedAt: Date.now() })
            } else if (outcome.kind === 'stale') {
              setMqttOutcome({ stale: outcome })
            } else {
              setMqttOutcome({ error: outcome.reason })
            }
          } else {
            const outcome = await guardedCreate({ read: getFPPMQTTConfig, write: () => putFPPMQTTConfig(request) })
            if (outcome.kind === 'created') {
              setMqttDirty(false)
              setNewPassword('')
              setMqttOutcome({ savedAt: Date.now() })
            } else if (outcome.kind === 'taken') {
              setMqttOutcome({ error: 'Someone else changed the event feed. Reload and start again.' })
            } else {
              setMqttOutcome({ error: outcome.reason })
            }
          }
        } catch (err: unknown) {
          setMqttOutcome({ error: describeApiError(err) })
        }
      }
    } finally {
      setSaving(false)
      reloadAll()
    }
  }

  const discard = () => {
    if (fppLoad.kind === 'loaded') setEndpoints(fppLoad.response.payload.endpoints)
    else setEndpoints([])
    if (resolumeLoad.kind === 'loaded') setInstances(resolumeLoad.response.payload.instances)
    else setInstances([])
    if (mqttLoad.kind === 'loaded') {
      setBrokerURL(mqttLoad.response.payload.brokerURL)
      setUsername(mqttLoad.response.payload.username)
      setTopicPrefix(mqttLoad.response.payload.topicPrefix)
      setHosts(hostsMapToRows(mqttLoad.response.payload.hosts))
    } else {
      setBrokerURL('')
      setUsername('')
      setTopicPrefix('')
      setHosts([])
    }
    setNewPassword('')
    setEndpointsDirty(false)
    setInstancesDirty(false)
    setMqttDirty(false)
    setFppOutcome(null)
    setResolumeOutcome(null)
    setMqttOutcome(null)
  }

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Connections</p>
      <h2 className="sm-section__title">Reaching the systems ShowMesh talks to</h2>

      <NotWiredBanner
        what="Test"
        missing="test endpoint for a configured connection"
        detail="Health comes from the coordinator's polling."
      />

      <Section id="st-fpp" title="FPP players">
        {fppLoad.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the configured FPP players." />
        ) : fppLoad.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={fppLoad.reason} />
        ) : (
          <>
            {fppLoad.kind === 'notConfigured' && (
              <RuledStrip absence="unavailable" label="Not configured" fact={fppLoad.detail} />
            )}
            <div className="sm-panel sm-stack-4">
              {endpoints.map((endpoint, index) => {
                const health = fppHealthFor(model.fpp, endpoint.id)
                const loadedRow = fppLoad.kind === 'loaded' ? fppLoad.response.payload.endpoints[index] : undefined
                const rowDirty = loadedRow === undefined || loadedRow.id !== endpoint.id || loadedRow.url !== endpoint.url
                return (
                  <div key={index} className="sm-panel">
                    <div className="sm-inline-row sm-inline-row--end">
                      <Field label="Player id">
                        {(props) => (
                          <Input {...props}
                            value={endpoint.id}
                            onChange={(e) => {
                              setEndpoints(patchRow(endpoints, index, { id: e.target.value }))
                              setEndpointsDirty(true)
                            }}
                            className="sm-input--id"
                          />
                        )}
                      </Field>
                      <Field label="Player URL">
                        {(props) => (
                          <Input {...props}
                            value={endpoint.url}
                            onChange={(e) => {
                              setEndpoints(patchRow(endpoints, index, { url: e.target.value }))
                              setEndpointsDirty(true)
                            }}
                            className="sm-input--address"
                          />
                        )}
                      </Field>
                      <NotWired>
                        <Button>Test</Button>
                      </NotWired>
                      <Button
                        variant="quiet"
                        onClick={() => setDetailInstanceId(endpoint.id)}
                        disabled={endpoint.id === ''}
                      >
                        Pairing and brightness
                      </Button>
                      <Button
                        variant="quiet"
                        onClick={() => {
                          setEndpoints(endpoints.filter((_, i) => i !== index))
                          setEndpointsDirty(true)
                        }}
                      >
                        Remove
                      </Button>
                      {rowDirty && <StatusPair tone="warn" label="Unsaved" />}
                    </div>
                    {health !== null ? (
                      <p className="sm-small sm-muted">
                        <StatusPair tone={HEALTH_TONE[health.health] ?? 'unknown'} label={health.health} />{' '}
                        {health.lastPollAt !== null ? `Last polled ${formatClock(health.lastPollAt)}` : 'Never polled'}
                        {health.lastPollError !== null && health.lastPollError !== '' && ` · ${health.lastPollError}`}
                      </p>
                    ) : (
                      <p className="sm-small sm-faint">Not yet reported by the coordinator's own polling.</p>
                    )}
                  </div>
                )
              })}
            </div>
            <Button
              onClick={() => {
                setEndpoints([...endpoints, { id: '', url: '' }])
                setEndpointsDirty(true)
              }}
            >
              Add a player
            </Button>
          </>
        )}
        {fppOutcome !== null && 'stale' in fppOutcome && (
          <StaleWriteStrip stale={fppOutcome.stale} onReload={() => { setFppOutcome(null); reloadAll() }} />
        )}
        {fppOutcome !== null && 'error' in fppOutcome && <RuledStrip absence="failed" label="Save failed" fact={fppOutcome.error} />}
      </Section>

      <Section id="st-res" title="Resolume">
        {resolumeLoad.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the configured Resolume instances." />
        ) : resolumeLoad.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={resolumeLoad.reason} />
        ) : (
          <>
            {resolumeLoad.kind === 'notConfigured' && (
              <RuledStrip absence="unavailable" label="Not configured" fact={resolumeLoad.detail} />
            )}
            <div className="sm-panel sm-stack-4">
              {instances.map((instance, index) => {
                const health = resolumeHealthFor(model.resolume, instance.id)
                const loadedRow = resolumeLoad.kind === 'loaded' ? resolumeLoad.response.payload.instances[index] : undefined
                const rowDirty = loadedRow === undefined || loadedRow.id !== instance.id || loadedRow.url !== instance.url || loadedRow.oscPort !== instance.oscPort || (loadedRow.control ?? 'rest') !== (instance.control ?? 'rest')
                return (
                  <div key={index} className="sm-panel">
                    <div className="sm-inline-row sm-inline-row--end">
                      <Field label="Instance id">
                        {(props) => (
                          <Input {...props}
                            value={instance.id}
                            onChange={(e) => {
                              setInstances(patchRow(instances, index, { id: e.target.value }))
                              setInstancesDirty(true)
                            }}
                            className="sm-input--id"
                          />
                        )}
                      </Field>
                      <Field label="Instance URL">
                        {(props) => (
                          <Input {...props}
                            value={instance.url}
                            onChange={(e) => {
                              setInstances(patchRow(instances, index, { url: e.target.value }))
                              setInstancesDirty(true)
                            }}
                            className="sm-input--address"
                          />
                        )}
                      </Field>
                      <Field label="OSC port" help="Arena's OSC input, used for layer fades. Empty means 7000.">
                        {(props) => (
                          <Input {...props}
                            type="number"
                            min={1}
                            max={65535}
                            placeholder="7000"
                            value={instance.oscPort ?? ''}
                            onChange={(e) => {
                              const next: ConfigResolumeInstance = { ...instance }
                              delete next.oscPort
                              if (e.target.value !== '') next.oscPort = Number(e.target.value)
                              setInstances(instances.map((row, i) => (i === index ? next : row)))
                              setInstancesDirty(true)
                            }}
                            className="sm-input--narrow"
                          />
                        )}
                      </Field>
                      <Field label="Control" help="OSC only sends Arena commands and never reads it, so nothing is confirmed.">
                        {(props) => (
                          <Select {...props}
                            value={instance.control ?? 'rest'}
                            onChange={(e) => {
                              setInstances(patchRow(instances, index, { control: e.target.value === 'osc' ? 'osc' : 'rest' }))
                              setInstancesDirty(true)
                            }}
                          >
                            <option value="rest">REST and OSC</option>
                            <option value="osc">OSC only</option>
                          </Select>
                        )}
                      </Field>
                      <NotWired>
                        <Button>Test</Button>
                      </NotWired>
                      <Button
                        variant="quiet"
                        onClick={() => {
                          setInstances(instances.filter((_, i) => i !== index))
                          setInstancesDirty(true)
                        }}
                      >
                        Remove
                      </Button>
                      {rowDirty && <StatusPair tone="warn" label="Unsaved" />}
                    </div>
                    {rowDirty && loadedRow !== undefined && (
                      <p className="sm-small sm-muted">
                        Still polling the saved value <span className="sm-data">{loadedRow.url}</span> until you save.
                      </p>
                    )}
                    {health !== null && health.control === 'osc' ? (
                      <p className="sm-small sm-muted">
                        {health.observations.find((o) => o.signal === 'resolume.reachable')?.reason ?? health.health}
                      </p>
                    ) : health !== null ? (
                      <p className="sm-small sm-muted">
                        <StatusPair tone={HEALTH_TONE[health.health] ?? 'unknown'} label={health.health} />
                      </p>
                    ) : (
                      <p className="sm-small sm-faint">Not yet reported by the coordinator's own polling.</p>
                    )}
                  </div>
                )
              })}
            </div>
            <Button
              onClick={() => {
                setInstances([...instances, { id: '', url: '' }])
                setInstancesDirty(true)
              }}
            >
              Add an instance
            </Button>
          </>
        )}
        {resolumeOutcome !== null && 'stale' in resolumeOutcome && (
          <StaleWriteStrip stale={resolumeOutcome.stale} onReload={() => { setResolumeOutcome(null); reloadAll() }} />
        )}
        {resolumeOutcome !== null && 'error' in resolumeOutcome && (
          <RuledStrip absence="failed" label="Save failed" fact={resolumeOutcome.error} />
        )}
      </Section>

      <Section id="st-mqtt" title="Event feed">
        {mqttLoad.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the event feed configuration." />
        ) : mqttLoad.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={mqttLoad.reason} />
        ) : (
          <>
            {mqttLoad.kind === 'notConfigured' && (
              <RuledStrip absence="unavailable" label="Not configured" fact={mqttLoad.detail} />
            )}
            <div className="sm-grid sm-form-column">
              <Field label="Broker">
                {(props) => (
                  <Input {...props}
                    value={brokerURL}
                    onChange={(e) => {
                      setBrokerURL(e.target.value)
                      setMqttDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Username">
                {(props) => (
                  <Input {...props}
                    value={username}
                    onChange={(e) => {
                      setUsername(e.target.value)
                      setMqttDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Topic prefix">
                {(props) => (
                  <Input {...props}
                    value={topicPrefix}
                    onChange={(e) => {
                      setTopicPrefix(e.target.value)
                      setMqttDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label={mqttLoad.kind === 'loaded' && mqttLoad.response.payload.passwordSet ? 'A password is set. Set a new one' : 'No password is set. Set one'}>
                {(props) => (
                  <Input {...props}
                    type="password"
                    value={newPassword}
                    autoComplete="new-password"
                    onChange={(e) => {
                      setNewPassword(e.target.value)
                      setMqttDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
            <div className="sm-stack-3" data-testid="mqtt-hosts">
              <p className="sm-field__label">Players on this feed</p>
              <p className="sm-small sm-muted">
                One row per player: its id and the host name FPP publishes under.
              </p>
              <div className="sm-panel sm-stack-4">
                {hosts.map((row, index) => (
                  <div key={index} className="sm-inline-row sm-inline-row--end">
                    <Field label="Host id">
                      {(props) => (
                        <Input {...props}
                          value={row.id}
                          placeholder="barn-player"
                          onChange={(e) => {
                            setHosts(patchRow(hosts, index, { id: e.target.value }))
                            setMqttDirty(true)
                          }}
                          className="sm-input--id"
                        />
                      )}
                    </Field>
                    <Field label="Host name">
                      {(props) => (
                        <Input {...props}
                          value={row.hostName}
                          placeholder={row.id !== '' ? row.id : 'Same as the id'}
                          onChange={(e) => {
                            setHosts(patchRow(hosts, index, { hostName: e.target.value }))
                            setMqttDirty(true)
                          }}
                          className="sm-input--id"
                        />
                      )}
                    </Field>
                    <Button
                      variant="quiet"
                      onClick={() => {
                        setHosts(hosts.filter((_, i) => i !== index))
                        setMqttDirty(true)
                      }}
                    >
                      Remove
                    </Button>
                  </div>
                ))}
              </div>
              <Button
                onClick={() => {
                  setHosts([...hosts, { id: '', hostName: '' }])
                  setMqttDirty(true)
                }}
              >
                Add a player to the feed
              </Button>
            </div>
          </>
        )}
        {mqttOutcome !== null && 'stale' in mqttOutcome && (
          <StaleWriteStrip stale={mqttOutcome.stale} onReload={() => { setMqttOutcome(null); reloadAll() }} />
        )}
        {mqttOutcome !== null && 'error' in mqttOutcome && <RuledStrip absence="failed" label="Save failed" fact={mqttOutcome.error} />}
      </Section>

      <FPPConnectSettings />

      <ButtonRow>
        <Button
          variant="primary"
          onClick={saveConnections}
          disabled={!anyDirty || saving || !gate.allowed}
          title={gate.allowed ? undefined : gate.reason}
        >
          {saving ? 'Saving…' : 'Save connections'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!anyDirty || saving}>
          Discard changes
        </Button>
        <div className="sm-push-end">
          <p className="sm-small sm-faint">FPP players</p>
          <RevisionHistory id="st-fpp-rev" fetch={getFPPEndpointsConfigRevisions} reloadKey={attempt} />
          <p className="sm-small sm-faint">Resolume</p>
          <RevisionHistory id="st-res-rev" fetch={getResolumeInstancesConfigRevisions} reloadKey={attempt} />
          <p className="sm-small sm-faint">Event feed</p>
          <RevisionHistory id="st-mqtt-rev" fetch={getFPPMQTTConfigRevisions} reloadKey={attempt} />
        </div>
      </ButtonRow>

      <SettingsReleaseCheck />

      <Drawer open={detailInstanceId !== null} onClose={() => setDetailInstanceId(null)} labelledBy="fpp-instance-detail-title" width="wide">
        {detailInstanceId !== null && <FPPInstanceDetail instanceId={detailInstanceId} />}
      </Drawer>
    </>
  )
}

/** The FPP endpoint detail (Settings > Connections > the FPP instance): pairing, and this player's brightness readout and excluded channels. */
function FPPInstanceDetail({ instanceId }: { instanceId: string }) {
  const model = useModelContext()
  const instance = fppHealthFor(model.fpp, instanceId)
  return (
    <div className="sm-stack-5">
      <h2 id="fpp-instance-detail-title" className="sm-heading">{instanceId}</h2>
      <PairingSection instanceId={instanceId} />
      <BrightnessSection instanceId={instanceId} observations={instance?.observations ?? []} />
    </div>
  )
}

function PairingSection({ instanceId }: { instanceId: string }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'principal:write')
  const [state, setState] = useState<FPPPairingStateResponse | null>(null)
  const [readError, setReadError] = useState<string | null>(null)
  const [code, setCode] = useState('')
  const [pairing, setPairing] = useState(false)
  const [pairError, setPairError] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    const read = () => {
      getFPPPairing(instanceId)
        .then((response) => {
          if (!cancelled) setState(response)
        })
        .catch((err: unknown) => {
          if (!cancelled) setReadError(describeApiError(err))
        })
    }
    read()
    const interval = setInterval(() => {
      if (state?.state === 'waiting') read()
    }, 3000)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [instanceId, state?.state])

  const startPairing = () => {
    setPairing(true)
    setPairError(null)
    postFPPPairing(instanceId, normalizePairingCode(code))
      .then((response) => {
        setState({ serverTime: response.serverTime, state: response.state, code: response.code, expiresAt: response.expiresAt, pairedAt: null, principalId: response.principalId })
        setCode('')
      })
      .catch((err: unknown) => setPairError(describeApiError(err)))
      .finally(() => setPairing(false))
  }

  return (
    <Section id="fpp-instance-pairing-title" title="Pairing">
      {readError !== null ? (
        <RuledStrip absence="failed" label="Read failed" fact={readError} />
      ) : state === null ? (
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this instance's pairing state." />
      ) : (
        <p className="sm-body">{pairingSentence(state)}</p>
      )}
      <div className="sm-inline-row">
        <Input
          aria-label="Pairing code"
          placeholder="XXXX-XXXX"
          value={code}
          onChange={(e) => setCode(e.target.value)}
          className="sm-input--narrow"
        />
        <Button
          variant="primary"
          onClick={startPairing}
          disabled={pairing || code.trim() === '' || !gate.allowed}
          title={gate.allowed ? undefined : gate.reason}
        >
          {pairing ? 'Pairing…' : 'Pair'}
        </Button>
      </div>
      {pairError !== null && <RuledStrip absence="failed" label="Pairing failed" fact={pairError} />}
    </Section>
  )
}

function FactCell<T>({ fact, render }: { fact: SignalFact<T>; render: (value: T) => string }) {
  if (fact.kind === 'absent') return <AbsenceLabel absence={fact.absence} label={fact.label} />
  return <span className="sm-data">{render(fact.value)}</span>
}

/** One entry per distinct reason among the absent facts, so a shared reason is stated once. */
function absentFacts(facts: readonly SignalFact<unknown>[]) {
  const seen = new Map<string, Extract<SignalFact<unknown>, { kind: 'absent' }>>()
  for (const f of facts) if (f.kind === 'absent' && !seen.has(f.fact)) seen.set(f.fact, f)
  return [...seen.values()]
}

function BrightnessSection({ instanceId, observations }: { instanceId: string; observations: readonly Evidence[] }) {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'fpp:command')
  const readout = brightnessReadout(observations)
  const absentReasons = absentFacts([readout.ceiling, readout.transitionGain, readout.effectiveOutput, readout.fadeActive, readout.excludedRanges])

  return (
    <Section id="fpp-instance-brightness-title" title="Brightness">
      <DisplayBrightnessPlayerReadout instanceId={instanceId} />
      <DefinitionStrip
        items={[
          { term: 'Ceiling', value: <FactCell fact={readout.ceiling} render={(v) => `${v}%`} /> },
          { term: 'Transition gain', value: <FactCell fact={readout.transitionGain} render={(v) => `${v}%`} /> },
          { term: 'Effective output', value: <FactCell fact={readout.effectiveOutput} render={(v) => `${v}%`} /> },
          { term: 'Fade', value: <FactCell fact={readout.fadeActive} render={(v) => (v ? 'Fading' : 'Not fading')} /> },
          { term: 'Excluded channels', value: <FactCell fact={readout.excludedRanges} render={(v) => v} /> },
        ]}
      />
      <ExcludedRangesField instanceId={instanceId} reported={readout.excludedRanges} allowed={gate.allowed} deniedReason={gate.allowed ? undefined : gate.reason} />
      {absentReasons.map((reason) => (
        <RuledStrip key={reason.fact} absence={reason.absence} label={reason.label} fact={reason.fact} />
      ))}
    </Section>
  )
}

type ExcludedRangesOutcome = { kind: 'ok'; sentence: string } | { kind: 'unconfirmed' | 'failed'; reason: string }

function excludedRangesOutcome(response: FPPBrightnessExcludedRangesResponse): ExcludedRangesOutcome {
  if (response.excludedRanges === undefined) return { kind: 'unconfirmed', reason: response.command.outcomeReason }
  if (response.excludedRanges.length === 0) return { kind: 'ok', sentence: 'Saved. The player now excludes no channels.' }
  return { kind: 'ok', sentence: `Saved. The player now excludes ${formatChannelRanges(response.excludedRanges)}.` }
}

/** The channel ranges the ceiling leaves alone: a text field in the form the CLI takes, saved whole. */
function ExcludedRangesField({ instanceId, reported, allowed, deniedReason }: { instanceId: string; reported: SignalFact<string>; allowed: boolean; deniedReason: string | undefined }) {
  const reportedText = reported.kind === 'value' ? reported.value : ''
  const [draft, setDraft] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [formError, setFormError] = useState<string | undefined>(undefined)
  const [outcome, setOutcome] = useState<ExcludedRangesOutcome | null>(null)

  const save = () => {
    const ranges = parseChannelRanges(draft ?? reportedText)
    setOutcome(null)
    if (ranges === null) {
      setFormError(CHANNEL_RANGES_FORM_ERROR)
      return
    }
    setFormError(undefined)
    setSaving(true)
    putFPPBrightnessExcludedRanges(instanceId, ranges)
      .then((response) => {
        const next = excludedRangesOutcome(response)
        setOutcome(next)
        if (next.kind === 'ok') setDraft(null)
      })
      .catch((err: unknown) => setOutcome({ kind: 'failed', reason: describeApiError(err) }))
      .finally(() => setSaving(false))
  }

  return (
    <>
      <Field label="Channels the ceiling never changes" help="First channel and count, like 1-512, 1025-512." error={formError}>
        {(props) => (
          <Input
            {...props}
            type="text"
            value={draft ?? reportedText}
            onChange={(e) => { setDraft(e.target.value); setFormError(undefined) }}
            disabled={!allowed}
            title={deniedReason}
          />
        )}
      </Field>
      <ButtonRow>
        <Button variant="primary" disabled={!allowed || saving || draft === null} title={deniedReason} onClick={save}>{saving ? 'Saving…' : 'Save excluded channels'}</Button>
      </ButtonRow>
      {outcome !== null && (outcome.kind === 'ok' ? (
        <p className="sm-small sm-muted">{outcome.sentence}</p>
      ) : (
        <RuledStrip absence="failed" label={outcome.kind === 'unconfirmed' ? 'Not confirmed' : 'Not saved'} fact={outcome.reason} />
      ))}
    </>
  )
}

function FPPConnectSettings() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const [attempt, setAttempt] = useState(0)
  const load = useConfigLoad(getFPPConnectSettingsConfig, attempt)
  const [enabled, setEnabled] = useState(true)
  const [maxFileBytes, setMaxFileBytes] = useState('')
  const [maxAssetDirBytes, setMaxAssetDirBytes] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (load.kind !== 'loaded') return
    setEnabled(load.response.payload.enabled)
    setMaxFileBytes(String(load.response.payload.maxFileBytes))
    setMaxAssetDirBytes(String(load.response.payload.maxAssetDirBytes))
    setDirty(false)
  }, [load])

  const save = () => {
    const file = Number(maxFileBytes)
    const total = Number(maxAssetDirBytes)
    if (!Number.isSafeInteger(file) || file < 1 || !Number.isSafeInteger(total) || total < file) {
      setError('Use whole bytes. The total limit must be at least the per-file limit.')
      return
    }
    setSaving(true)
    setError(null)
    putFPPConnectSettingsConfig({ enabled, maxFileBytes: file, maxAssetDirBytes: total })
      .then(() => { setDirty(false); setAttempt((n) => n + 1) })
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <Section id="st-fppconnect" title="FPP Connect">
      {load.kind === 'loading' ? <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for FPP Connect settings." /> : load.kind === 'failed' ? <RuledStrip absence="failed" label="Read failed" fact={load.reason} /> : (
        <>
          <Choice type="checkbox" checked={enabled} onChange={(e) => { setEnabled(e.target.checked); setDirty(true) }} label="Enable the xLights ingestion listener" />
          <div className="sm-grid sm-form-column sm-stack-3">
            <Field label="Maximum file bytes">
              {(props) => <Input {...props} type="number" min="1" value={maxFileBytes} onChange={(e) => { setMaxFileBytes(e.target.value); setDirty(true) }} />}
            </Field>
            <Field label="Maximum asset-directory bytes">
              {(props) => <Input {...props} type="number" min="1" value={maxAssetDirBytes} onChange={(e) => { setMaxAssetDirBytes(e.target.value); setDirty(true) }} />}
            </Field>
          </div>
          <ButtonRow>
            <Button variant="primary" disabled={!dirty || saving || !gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={save}>{saving ? 'Saving…' : 'Save FPP Connect settings'}</Button>
            <div className="sm-push-end">
              <RevisionHistory id="st-fppconnect-rev" fetch={getFPPConnectSettingsConfigRevisions} reloadKey={attempt} />
            </div>
          </ButtonRow>
        </>
      )}
      {error !== null && <RuledStrip absence="failed" label="Save failed" fact={error} />}
    </Section>
  )
}
