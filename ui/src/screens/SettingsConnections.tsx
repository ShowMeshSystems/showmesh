import { useEffect, useState } from 'react'
import {
  ApiError,
  getFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  getFPPMQTTConfig,
  getFPPMQTTConfigRevisions,
  getFPPConnectSettingsConfig,
  getFPPConnectSettingsConfigRevisions,
  getResolumeInstancesConfig,
  getResolumeInstancesConfigRevisions,
  putFPPEndpointsConfig,
  putFPPMQTTConfig,
  putFPPConnectSettingsConfig,
  putResolumeInstancesConfig,
  type ConfigFPPEndpoint,
  type ConfigResolumeInstance,
} from '../api'
import { Button, ButtonRow, Choice, Input, NotWired, NotWiredBanner, RevisionHistory, RuledStrip, Section, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { guardedCreate, guardedSave } from '../domain/save'
import { StaleWriteStrip, type StaleWrite } from './StaleWrite'
import { fppHealthFor, resolumeHealthFor, hostRowsToMap, hostsMapToRows, HEALTH_TONE, type MQTTHostRow } from './settingsModel'

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
              setMqttOutcome({ error: 'Someone else configured the event feed while this was open. Reload and start again.' })
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
      <p className="sm-page__lede">
        Addresses must be reachable from the coordinator, not from this browser. Saving does not disturb a running
        show. The coordinator re-polls on the next cycle.
      </p>

      <NotWiredBanner
        what="Test"
        missing="test endpoint for a configured connection"
        detail="Health below comes from the coordinator's own polling, which is what the fleet already reports. A test would be a live check from the coordinator, not saved, and it would not prove the address still answers at showtime."
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
                    <div className="sm-inline-row">
                      <Input
                        aria-label="Player id"
                        value={endpoint.id}
                        onChange={(e) => {
                          setEndpoints(patchRow(endpoints, index, { id: e.target.value }))
                          setEndpointsDirty(true)
                        }}
                        style={{ maxWidth: 200 }}
                      />
                      <Input
                        aria-label="Player URL"
                        value={endpoint.url}
                        onChange={(e) => {
                          setEndpoints(patchRow(endpoints, index, { url: e.target.value }))
                          setEndpointsDirty(true)
                        }}
                        style={{ maxWidth: 260 }}
                      />
                      <NotWired>
                        <Button>Test</Button>
                      </NotWired>
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
        <RevisionHistory id="st-fpp-rev" fetch={getFPPEndpointsConfigRevisions} reloadKey={attempt} />
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
                const rowDirty = loadedRow === undefined || loadedRow.id !== instance.id || loadedRow.url !== instance.url
                return (
                  <div key={index} className="sm-panel">
                    <div className="sm-inline-row">
                      <Input
                        aria-label="Instance id"
                        value={instance.id}
                        onChange={(e) => {
                          setInstances(patchRow(instances, index, { id: e.target.value }))
                          setInstancesDirty(true)
                        }}
                        style={{ maxWidth: 200 }}
                      />
                      <Input
                        aria-label="Instance URL"
                        value={instance.url}
                        onChange={(e) => {
                          setInstances(patchRow(instances, index, { url: e.target.value }))
                          setInstancesDirty(true)
                        }}
                        style={{ maxWidth: 260 }}
                      />
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
                        The saved value is <span className="sm-data">{loadedRow.url}</span> and the coordinator is
                        still polling it. Nothing changes until you save.
                      </p>
                    )}
                    {health !== null ? (
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
        <RevisionHistory id="st-res-rev" fetch={getResolumeInstancesConfigRevisions} reloadKey={attempt} />
      </Section>

      <Section id="st-mqtt" title="Event feed" detail="How FPP's plugin reaches the coordinator. Playlist-entry identity arrives here.">
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
              <label className="sm-field">
                <span className="sm-field__label">Broker</span>
                <Input
                  value={brokerURL}
                  onChange={(e) => {
                    setBrokerURL(e.target.value)
                    setMqttDirty(true)
                  }}
                />
              </label>
              <label className="sm-field">
                <span className="sm-field__label">Username</span>
                <Input
                  value={username}
                  onChange={(e) => {
                    setUsername(e.target.value)
                    setMqttDirty(true)
                  }}
                />
              </label>
              <label className="sm-field">
                <span className="sm-field__label">Topic prefix</span>
                <Input
                  value={topicPrefix}
                  onChange={(e) => {
                    setTopicPrefix(e.target.value)
                    setMqttDirty(true)
                  }}
                />
              </label>
              <label className="sm-field">
                <span className="sm-field__label">
                  {mqttLoad.kind === 'loaded' && mqttLoad.response.payload.passwordSet ? 'A password is set. Set a new one' : 'No password is set. Set one'}
                </span>
                <Input
                  type="password"
                  value={newPassword}
                  autoComplete="new-password"
                  onChange={(e) => {
                    setNewPassword(e.target.value)
                    setMqttDirty(true)
                  }}
                />
                <span className="sm-field__help">The stored password is never sent back to this browser; leave this blank to keep it unchanged.</span>
              </label>
            </div>
            <div className="sm-stack-3" data-testid="mqtt-hosts">
              <p className="sm-field__label">Host name overrides</p>
              <p className="sm-small sm-muted">
                Maps a player id to the HostName it publishes in MQTT topics; unlisted ids are ignored.
              </p>
              <div className="sm-panel sm-stack-4">
                {hosts.map((row, index) => (
                  <div key={index} className="sm-inline-row">
                    <Input
                      aria-label="Host id"
                      value={row.id}
                      onChange={(e) => {
                        setHosts(patchRow(hosts, index, { id: e.target.value }))
                        setMqttDirty(true)
                      }}
                      style={{ maxWidth: 200 }}
                    />
                    <Input
                      aria-label="HostName"
                      value={row.hostName}
                      onChange={(e) => {
                        setHosts(patchRow(hosts, index, { hostName: e.target.value }))
                        setMqttDirty(true)
                      }}
                      style={{ maxWidth: 200 }}
                    />
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
                Add a host override
              </Button>
            </div>
          </>
        )}
        {mqttOutcome !== null && 'stale' in mqttOutcome && (
          <StaleWriteStrip stale={mqttOutcome.stale} onReload={() => { setMqttOutcome(null); reloadAll() }} />
        )}
        {mqttOutcome !== null && 'error' in mqttOutcome && <RuledStrip absence="failed" label="Save failed" fact={mqttOutcome.error} />}
        <RevisionHistory id="st-mqtt-rev" fetch={getFPPMQTTConfigRevisions} reloadKey={attempt} />
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
      </ButtonRow>
      <p className="sm-small sm-faint">
        A test is a live check from the coordinator. It is not saved, and it does not prove the address will still
        answer at showtime.
      </p>
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
      setError('Both limits must be whole bytes, and the total asset-directory limit must be at least the per-file limit.')
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
    <Section id="st-fppconnect" title="FPP Connect" detail="Controls xLights ingestion on the coordinator; byte limits are enforced before files enter a node asset directory.">
      {load.kind === 'loading' ? <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for FPP Connect settings." /> : load.kind === 'failed' ? <RuledStrip absence="failed" label="Read failed" fact={load.reason} /> : (
        <>
          <Choice type="checkbox" checked={enabled} onChange={(e) => { setEnabled(e.target.checked); setDirty(true) }} label="Enable the xLights ingestion listener" />
          <div className="sm-grid sm-form-column sm-stack-3">
            <label className="sm-field"><span className="sm-field__label">Maximum file bytes</span><Input type="number" min="1" value={maxFileBytes} onChange={(e) => { setMaxFileBytes(e.target.value); setDirty(true) }} /></label>
            <label className="sm-field"><span className="sm-field__label">Maximum asset-directory bytes</span><Input type="number" min="1" value={maxAssetDirBytes} onChange={(e) => { setMaxAssetDirBytes(e.target.value); setDirty(true) }} /></label>
          </div>
          <ButtonRow><Button variant="primary" disabled={!dirty || saving || !gate.allowed} title={gate.allowed ? undefined : gate.reason} onClick={save}>{saving ? 'Saving…' : 'Save FPP Connect settings'}</Button></ButtonRow>
        </>
      )}
      {error !== null && <RuledStrip absence="failed" label="Save failed" fact={error} />}
      <RevisionHistory id="st-fppconnect-rev" fetch={getFPPConnectSettingsConfigRevisions} reloadKey={attempt} />
    </Section>
  )
}
