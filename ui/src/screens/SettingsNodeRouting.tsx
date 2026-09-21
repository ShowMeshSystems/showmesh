import { useEffect, useState } from 'react'
import {
  ApiError,
  getAudioNode,
  getAudioNodeConfigRevisions,
  getNodeClock,
  getNodeClockConfigRevisions,
  listConfigObjects,
  putAudioNode,
  putNodeClock,
  type AudioNodeConfigResponse,
  type AudioNodeSummary,
  type ConfigObjectSummary,
  type NodeClockConfigResponse,
} from '../api'
import { Button, ButtonRow, Choice, Field, Input, RevisionHistory, RuledStrip, Section, Segmented, Select, StatusPair } from '../kit'
import type { ConfigAudioNode, ConfigAudioOutputLatency, ConfigNodeClock } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope, type ScopeGateResult } from '../domain/session'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { advertisedRoutes, audioNodeVerdict, hasAudioCapability, nodeClockVerdict, type NodeClockProvider } from './settingsModel'

type AudioNodeRole = NonNullable<ConfigAudioNode['role']>
const DEFAULT_ROLE: AudioNodeRole = 'program+ltc'
const ROLE_OPTIONS: readonly { value: AudioNodeRole; label: string }[] = [
  { value: 'program', label: 'Program' },
  { value: 'program+ltc', label: 'Program + LTC' },
  { value: 'zone', label: 'Zone' },
]

type AudioSinkBackend = NonNullable<ConfigAudioNode['sinkBackend']>
const DEFAULT_SINK_BACKEND: AudioSinkBackend = 'alsasink'
const SINK_BACKEND_OPTIONS: readonly { value: AudioSinkBackend; label: string }[] = [
  { value: 'alsasink', label: 'ALSA' },
  { value: 'pipewiresink', label: 'PipeWire' },
]

type OutputLatencyMethod = NonNullable<ConfigAudioOutputLatency['method']>
const DEFAULT_OUTPUT_LATENCY_METHOD: OutputLatencyMethod = 'unmeasured'
const OUTPUT_LATENCY_METHOD_OPTIONS: readonly { value: OutputLatencyMethod; label: string }[] = [
  { value: 'unmeasured', label: 'Unmeasured' },
  { value: 'loopback', label: 'Loopback' },
  { value: 'acoustic', label: 'Acoustic' },
  { value: 'declared', label: 'Declared' },
]

const DEFAULT_PROVIDER: NodeClockProvider = 'managed'
const PROVIDER_OPTIONS: readonly { value: NodeClockProvider; label: string }[] = [
  { value: 'managed', label: 'Managed' },
  { value: 'external', label: 'External' },
  { value: 'fpp', label: 'FPP' },
]
const DEFAULT_HOLDOVER_LIMIT_SECONDS = 60

type NodesState = { kind: 'loading' } | { kind: 'loaded'; nodes: AudioNodeSummary[] } | { kind: 'failed'; reason: string }
type NodeClockObjectsState = { kind: 'loading' } | { kind: 'loaded'; objects: ConfigObjectSummary[] } | { kind: 'failed'; reason: string }
type NodeState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: AudioNodeConfigResponse }
  | { kind: 'failed'; reason: string }

function parseChannels(raw: string): number[] {
  return raw
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '')
    .map((s) => Number(s))
}

// isRfc3339 catches an obvious typo before submit; the server's own
// decode is the actual RFC 3339 authority.
function isRfc3339(s: string): boolean {
  return /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}/.test(s) && !Number.isNaN(Date.parse(s))
}

const LTC_CHANNEL_CHOICES = 8

// No channel inventory is advertised, so offer 1-8 minus program channels, keeping any stored value.
function ltcChannelOptions(programChannels: number[], current: string): number[] {
  const options = new Set<number>()
  for (let ch = 1; ch <= LTC_CHANNEL_CHOICES; ch++) {
    if (!programChannels.includes(ch)) options.add(ch)
  }
  const stored = Number(current)
  if (current.trim() !== '' && Number.isInteger(stored) && stored >= 1) options.add(stored)
  return [...options].sort((a, b) => a - b)
}

export function SettingsNodeRouting() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const [nodesState, setNodesState] = useState<NodesState>({ kind: 'loading' })
  const [selectedId, setSelectedId] = useState<string | null>(null)
  // node.clock is a separate config kind from audio.node (RES-019, ADR-039):
  // a node can carry a PTP clock configuration without ever advertising audio
  // routing, so its own node id is chosen independently of the audio.node
  // picker above rather than reused from `selectedId`.
  const [nodeClockObjectsState, setNodeClockObjectsState] = useState<NodeClockObjectsState>({ kind: 'loading' })
  const [clockNodeId, setClockNodeId] = useState('')
  // Only committed to `clockNodeId` on blur/Enter: a GET fired per keystroke
  // would fire once per character typed, and a partial id that fails the id
  // syntax check would flash a read-failed strip mid-typing.
  const [newClockNodeIdDraft, setNewClockNodeIdDraft] = useState('')

  useEffect(() => {
    let cancelled = false
    listConfigObjects('audio.node')
      .then((response) => {
        if (cancelled) return
        setNodesState({ kind: 'loaded', nodes: response.objects })
        setSelectedId((prev) => prev ?? response.objects[0]?.id ?? null)
      })
      .catch((err: unknown) => {
        if (!cancelled) setNodesState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    listConfigObjects('node.clock')
      .then((response) => {
        if (cancelled) return
        setNodeClockObjectsState({ kind: 'loaded', objects: response.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setNodeClockObjectsState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])

  const commitNewClockNodeId = () => {
    const trimmed = newClockNodeIdDraft.trim()
    if (trimmed === '') return
    setClockNodeId(trimmed)
  }

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Audio <span className="sm-faint">/</span> Node routing</p>
      <h2 className="sm-section__title">Where this node's audio leaves the building</h2>

      <Section id="st-node" title="Node">
        {nodesState.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for configured audio nodes." />
        ) : nodesState.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={nodesState.reason} />
        ) : nodesState.nodes.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="No audio.node object has ever been configured." />
        ) : (
          <Field label="Audio node">
            {(props) => (
              <Select
                {...props}
                value={selectedId ?? ''}
                onChange={(e) => setSelectedId(e.target.value)}
              >
                {nodesState.nodes.map((summary) => {
                  const node = model.nodes.find((n) => n.nodeId === summary.id) ?? null
                  const cap = node !== null && hasAudioCapability(node)
                  return (
                    <option key={summary.id} value={summary.id}>
                      {summary.id}, {cap ? node!.controlPlane.state : 'no audio capability'}
                    </option>
                  )
                })}
              </Select>
            )}
          </Field>
        )}
      </Section>

      {selectedId !== null && <NodeRoutingForm key={`audio:${selectedId}`} nodeId={selectedId} saveGate={gate} />}

      <Section id="st-node-clock-select" title="PTP clock node">
        {nodeClockObjectsState.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for configured node.clock objects." />
        ) : nodeClockObjectsState.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={nodeClockObjectsState.reason} />
        ) : (
          nodeClockObjectsState.objects.length > 0 && (
            <Field label="Existing node">
              {(props) => (
                <Select
                  {...props}
                  value={nodeClockObjectsState.objects.some((o) => o.id === clockNodeId) ? clockNodeId : ''}
                  onChange={(e) => setClockNodeId(e.target.value)}
                >
                  <option value="">Select a node…</option>
                  {nodeClockObjectsState.objects.map((summary) => (
                    <option key={summary.id} value={summary.id}>
                      {summary.id}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          )
        )}
        <Field label="New node id">
          {(props) => (
            <Input
              {...props}
              value={newClockNodeIdDraft}
              onChange={(e) => setNewClockNodeIdDraft(e.target.value)}
              onBlur={commitNewClockNodeId}
              onKeyDown={(e) => {
                if (e.key === 'Enter') {
                  e.preventDefault()
                  commitNewClockNodeId()
                }
              }}
            />
          )}
        </Field>
      </Section>

      {clockNodeId.trim() !== '' && <NodeClockSection key={`clock:${clockNodeId.trim()}`} nodeId={clockNodeId.trim()} saveGate={gate} />}
    </>
  )
}

function NodeRoutingForm({ nodeId, saveGate }: { nodeId: string; saveGate: ScopeGateResult }) {
  const model = useModelContext()
  const node = model.nodes.find((n) => n.nodeId === nodeId) ?? null
  const programRoutes = node !== null ? advertisedRoutes(node, 'audio.output.local') : null

  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<NodeState>({ kind: 'loading' })

  const [programRoute, setProgramRoute] = useState('')
  const [programChannelsText, setProgramChannelsText] = useState('')
  const [ltcOn, setLtcOn] = useState(false)
  const [ltcChannelText, setLtcChannelText] = useState('')
  const [role, setRole] = useState<AudioNodeRole>(DEFAULT_ROLE)
  const [zone, setZone] = useState('')
  const [sinkBackend, setSinkBackend] = useState<AudioSinkBackend>(DEFAULT_SINK_BACKEND)
  const [pipewireTargetNode, setPipewireTargetNode] = useState('')
  const [outputLatencyMethod, setOutputLatencyMethod] = useState<OutputLatencyMethod>(DEFAULT_OUTPUT_LATENCY_METHOD)
  const [outputLatencyValueUsText, setOutputLatencyValueUsText] = useState('')
  const [outputLatencyMeasuredAt, setOutputLatencyMeasuredAt] = useState('')
  const [outputLatencyReference, setOutputLatencyReference] = useState('')
  const [outputLatencyConfidence, setOutputLatencyConfidence] = useState('')
  const [outputLatencyConfiguration, setOutputLatencyConfiguration] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<AudioNodeConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getAudioNode(nodeId)
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
        setProgramRoute(response.payload.programRoute)
        setProgramChannelsText(response.payload.programChannels.join(', '))
        setLtcOn(response.payload.ltcRoute !== undefined && response.payload.ltcRoute !== '')
        setLtcChannelText(response.payload.ltcChannel !== undefined ? String(response.payload.ltcChannel) : '')
        setRole(response.payload.role ?? DEFAULT_ROLE)
        setZone(response.payload.zone ?? '')
        setSinkBackend(response.payload.sinkBackend ?? DEFAULT_SINK_BACKEND)
        setPipewireTargetNode(response.payload.pipewireTargetNode ?? '')
        const ol = response.payload.outputLatency
        // The coordinator returns method "" for a node saved before outputLatency existed.
        setOutputLatencyMethod(ol?.method || DEFAULT_OUTPUT_LATENCY_METHOD)
        setOutputLatencyValueUsText(ol?.valueUs !== undefined ? String(ol.valueUs) : '')
        setOutputLatencyMeasuredAt(ol?.measuredAt ?? '')
        setOutputLatencyReference(ol?.reference ?? '')
        setOutputLatencyConfidence(ol?.confidence ?? '')
        setOutputLatencyConfiguration(ol?.configuration ?? '')
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, attempt])

  const programChannels = parseChannels(programChannelsText)
  const verdict = audioNodeVerdict({
    programRoute,
    programChannels,
    ltcRoute: ltcOn ? programRoute : '',
    ltcChannel: ltcOn ? ltcChannelText : '',
  })
  const channelsValid = programChannels.every((n) => Number.isInteger(n) && n >= 1) && programChannels.length > 0
  const ltcChannelValid = !ltcOn || (Number.isInteger(Number(ltcChannelText)) && ltcChannelText.trim() !== '')
  const zoneValid = role !== 'zone' || zone.trim() !== ''
  const outputLatencyValueUsValid =
    outputLatencyMethod === 'unmeasured' ||
    (outputLatencyValueUsText.trim() !== '' && Number.isInteger(Number(outputLatencyValueUsText)))
  const outputLatencyProvenanceValid =
    outputLatencyMethod === 'unmeasured' ||
    (isRfc3339(outputLatencyMeasuredAt.trim()) &&
      outputLatencyReference.trim() !== '' &&
      outputLatencyConfidence.trim() !== '' &&
      outputLatencyConfiguration.trim() !== '')
  const canSave =
    verdict.ok &&
    channelsValid &&
    ltcChannelValid &&
    zoneValid &&
    outputLatencyValueUsValid &&
    outputLatencyProvenanceValid

  const discard = () => {
    if (state.kind !== 'loaded') return
    setProgramRoute(state.response.payload.programRoute)
    setProgramChannelsText(state.response.payload.programChannels.join(', '))
    setLtcOn(state.response.payload.ltcRoute !== undefined && state.response.payload.ltcRoute !== '')
    setLtcChannelText(state.response.payload.ltcChannel !== undefined ? String(state.response.payload.ltcChannel) : '')
    setRole(state.response.payload.role ?? DEFAULT_ROLE)
    setZone(state.response.payload.zone ?? '')
    setSinkBackend(state.response.payload.sinkBackend ?? DEFAULT_SINK_BACKEND)
    setPipewireTargetNode(state.response.payload.pipewireTargetNode ?? '')
    const ol = state.response.payload.outputLatency
    setOutputLatencyMethod(ol?.method || DEFAULT_OUTPUT_LATENCY_METHOD)
    setOutputLatencyValueUsText(ol?.valueUs !== undefined ? String(ol.valueUs) : '')
    setOutputLatencyMeasuredAt(ol?.measuredAt ?? '')
    setOutputLatencyReference(ol?.reference ?? '')
    setOutputLatencyConfidence(ol?.confidence ?? '')
    setOutputLatencyConfiguration(ol?.configuration ?? '')
    setDirty(false)
    setSaveError(null)
  }

  const save = () => {
    if (state.kind !== 'loaded' || !canSave) return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: () => getAudioNode(nodeId),
      // Starts from the payload this edit was loaded from and overrides only
      // the fields this form manages, rather than assembling a payload from
      // scratch: a field this screen does not manage (or does not manage
      // yet) survives the edit instead of being silently dropped, the same
      // pattern used elsewhere for a full-replacement PUT.
      write: () => {
        const payload: ConfigAudioNode = {
          ...state.response.payload,
          programRoute,
          programChannels,
          role,
          sinkBackend,
        }
        if (ltcOn) {
          payload.ltcRoute = programRoute
          payload.ltcChannel = Number(ltcChannelText)
        } else {
          delete payload.ltcRoute
          delete payload.ltcChannel
        }
        if (role === 'zone') payload.zone = zone
        else delete payload.zone
        if (sinkBackend === 'pipewiresink' && pipewireTargetNode.trim() !== '') payload.pipewireTargetNode = pipewireTargetNode
        else delete payload.pipewireTargetNode
        if (outputLatencyMethod === 'unmeasured') {
          delete payload.outputLatency
        } else {
          payload.outputLatency = {
            valueUs: Number(outputLatencyValueUsText),
            method: outputLatencyMethod,
            measuredAt: outputLatencyMeasuredAt,
            reference: outputLatencyReference,
            confidence: outputLatencyConfidence,
            configuration: outputLatencyConfiguration,
          }
        }
        return putAudioNode(nodeId, payload)
      },
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response })
          setDirty(false)
          setAttempt((n) => n + 1)
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

  if (state.kind === 'loading') {
    return <RuledStrip absence="loading" label="Reading" fact={`Asking the coordinator for ${nodeId}'s routing.`} />
  }
  if (state.kind === 'failed') {
    return <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
  }

  return (
    <>
      <Section id="st-role" title="Role">
        <div className="sm-grid sm-form-column">
          <Segmented
            label="Role"
            value={role}
            options={ROLE_OPTIONS}
            onChange={(v) => {
              setRole(v)
              setDirty(true)
            }}
          />
          {role === 'zone' && (
            <Field label="Zone">
              {(props) => (
                <Input
                  {...props}
                  value={zone}
                  onChange={(e) => {
                    setZone(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
          )}
        </div>
      </Section>

      <Section id="st-program" title="Program output">
        <div className="sm-grid sm-form-column">
          {programRoutes !== null && programRoutes.length > 0 ? (
            <Field label="Route">
              {(props) => (
                <Select
                  {...props}
                  value={programRoute}
                  onChange={(e) => {
                    setProgramRoute(e.target.value)
                    setDirty(true)
                  }}
                >
                  {programRoutes.map((route) => (
                    <option key={route} value={route}>
                      {route}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          ) : (
            <div className="sm-panel">
              <Field label="Route">
                {(props) => (
                  <Input
                    {...props}
                    value={programRoute}
                    onChange={(e) => {
                      setProgramRoute(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
          )}
          <Field label="Program channels">
            {(props) => (
              <Input
                {...props}
                value={programChannelsText}
                onChange={(e) => {
                  setProgramChannelsText(e.target.value)
                  setDirty(true)
                }}
              />
            )}
          </Field>
          <Segmented
            label="Backend"
            value={sinkBackend}
            options={SINK_BACKEND_OPTIONS}
            onChange={(v) => {
              setSinkBackend(v)
              setDirty(true)
            }}
          />
          {sinkBackend === 'pipewiresink' && (
            <Field label="PipeWire target node">
              {(props) => (
                <Input
                  {...props}
                  value={pipewireTargetNode}
                  onChange={(e) => {
                    setPipewireTargetNode(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
          )}
        </div>
      </Section>

      <Section id="st-ltc" title="LTC output">
        <div className="sm-inline-row">
          <span
            role="group"
            aria-label="LTC output"
            className="sm-segmented"
          >
            <button type="button" className="sm-segmented__item" aria-pressed={!ltcOn} onClick={() => { setLtcOn(false); setDirty(true) }}>
              Off
            </button>
            <button type="button" className="sm-segmented__item" aria-pressed={ltcOn} onClick={() => { setLtcOn(true); setDirty(true) }}>
              On
            </button>
          </span>
        </div>
        {ltcOn && (
          <div className="sm-grid sm-form-column sm-stack-4">
            <div className="sm-field">
              <span className="sm-field__label">Route</span>
              <p className="sm-input sm-data sm-muted">{programRoute === '' ? 'Set a program route first' : programRoute}</p>
            </div>
            <Field label="Channel">
              {(props) => (
                <Select
                  {...props}
                  value={ltcChannelText}
                  onChange={(e) => {
                    setLtcChannelText(e.target.value)
                    setDirty(true)
                  }}
                >
                  {ltcChannelText === '' && <option value="">Select a channel</option>}
                  {ltcChannelOptions(programChannels, ltcChannelText).map((ch) => (
                    <option key={ch} value={String(ch)}>
                      {ch}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          </div>
        )}
      </Section>

      <Section id="st-output-latency" title="Output latency">
        <div className="sm-grid sm-form-column">
          <Segmented
            label="Method"
            value={outputLatencyMethod}
            options={OUTPUT_LATENCY_METHOD_OPTIONS}
            onChange={(v) => {
              setOutputLatencyMethod(v)
              setDirty(true)
            }}
          />
          {outputLatencyMethod !== 'unmeasured' && (
            <div className="sm-grid sm-stack-4">
              <Field label="Value (microseconds)">
                {(props) => (
                  <Input
                    {...props}
                    value={outputLatencyValueUsText}
                    onChange={(e) => {
                      setOutputLatencyValueUsText(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Measured at">
                {(props) => (
                  <Input
                    {...props}
                    value={outputLatencyMeasuredAt}
                    onChange={(e) => {
                      setOutputLatencyMeasuredAt(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Reference">
                {(props) => (
                  <Input
                    {...props}
                    value={outputLatencyReference}
                    onChange={(e) => {
                      setOutputLatencyReference(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Confidence">
                {(props) => (
                  <Input
                    {...props}
                    value={outputLatencyConfidence}
                    onChange={(e) => {
                      setOutputLatencyConfidence(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
              <Field label="Configuration">
                {(props) => (
                  <Input
                    {...props}
                    value={outputLatencyConfiguration}
                    onChange={(e) => {
                      setOutputLatencyConfiguration(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </div>
          )}
        </div>
      </Section>

      <div className="sm-panel sm-stack-4">
        <div className="sm-inline-row">
          <StatusPair tone={verdict.ok ? 'good' : 'bad'} label={verdict.ok ? 'Will be accepted' : 'Will be refused'} />
        </div>
        <p className="sm-small sm-muted">
          {verdict.ok
            ? `One route, program on channel${programChannels.length === 1 ? '' : 's'} ${programChannels.join(' and ')}${ltcOn ? `, LTC on ${ltcChannelText}` : ', no LTC'}. Both routes match and no channel is claimed twice.`
            : verdict.reason}
        </p>
      </div>

      <ButtonRow>
        <Button
          variant="primary"
          onClick={save}
          disabled={!dirty || saving || !canSave || !saveGate.allowed}
          title={
            !saveGate.allowed
              ? saveGate.reason
              : !canSave
                ? (!verdict.ok
                    ? verdict.reason
                    : !zoneValid
                      ? 'A zone name is required when the role is zone.'
                      : !outputLatencyValueUsValid
                        ? 'Output latency value must be a whole number of microseconds.'
                        : !outputLatencyProvenanceValid
                          ? 'Measured at, reference, confidence, and configuration are all required for a measured output latency method.'
                          : undefined)
                : undefined
          }
        >
          {saving ? 'Saving…' : 'Save routing'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving}>
          Discard changes
        </Button>
        <span className="sm-small sm-muted sm-push-end">
          Active revision <span className="sm-data">{state.response.revision}</span>
        </span>
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

      <RevisionHistory fetch={() => getAudioNodeConfigRevisions(nodeId)} reloadKey={`${nodeId}:${attempt}`} mode="list" />
    </>
  )
}

type NodeClockState =
  | { kind: 'loading' }
  | { kind: 'notfound' }
  | { kind: 'loaded'; response: NodeClockConfigResponse }
  | { kind: 'failed'; reason: string }

function NodeClockSection({ nodeId, saveGate }: { nodeId: string; saveGate: ScopeGateResult }) {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<NodeClockState>({ kind: 'loading' })

  const [provider, setProvider] = useState<NodeClockProvider>(DEFAULT_PROVIDER)
  const [interfaceName, setInterfaceName] = useState('')
  const [domainText, setDomainText] = useState('')
  const [clientOnly, setClientOnly] = useState(false)
  const [holdoverLimitSecondsText, setHoldoverLimitSecondsText] = useState(String(DEFAULT_HOLDOVER_LIMIT_SECONDS))
  const [priority1Text, setPriority1Text] = useState('')
  const [hardwareTimestamping, setHardwareTimestamping] = useState(false)
  const [externalUdsAddress, setExternalUdsAddress] = useState('')
  const [fppBaseUrl, setFppBaseUrl] = useState('')
  const [phcDevice, setPhcDevice] = useState('')
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<NodeClockConfigResponse>, { kind: 'stale' }> | null>(null)
  const [taken, setTaken] = useState(false)

  const loadFrom = (payload: ConfigNodeClock) => {
    setProvider(payload.provider)
    setInterfaceName(payload.interface)
    setDomainText(String(payload.domain))
    setClientOnly(payload.clientOnly ?? false)
    setHoldoverLimitSecondsText(String(payload.holdoverLimitSeconds ?? DEFAULT_HOLDOVER_LIMIT_SECONDS))
    setPriority1Text(payload.priority1 !== undefined ? String(payload.priority1) : '')
    setHardwareTimestamping(payload.hardwareTimestamping ?? false)
    setExternalUdsAddress(payload.externalUdsAddress ?? '')
    setFppBaseUrl(payload.fppBaseUrl ?? '')
    setPhcDevice(payload.phcDevice ?? '')
    setDirty(false)
  }

  const resetToDefaults = () => {
    setProvider(DEFAULT_PROVIDER)
    setInterfaceName('')
    setDomainText('')
    setClientOnly(false)
    setHoldoverLimitSecondsText(String(DEFAULT_HOLDOVER_LIMIT_SECONDS))
    setPriority1Text('')
    setHardwareTimestamping(false)
    setExternalUdsAddress('')
    setFppBaseUrl('')
    setPhcDevice('')
    setDirty(false)
  }

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getNodeClock(nodeId)
      .then((response) => {
        if (cancelled) return
        setState({ kind: 'loaded', response })
        loadFrom(response.payload)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'notfound' })
          resetToDefaults()
          return
        }
        setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, attempt])

  const verdict = nodeClockVerdict({ provider, interfaceName, domainText, fppBaseUrl, holdoverLimitSecondsText, priority1Text, phcDeviceText: phcDevice })
  const canSave = verdict.ok

  const buildPayload = (base: ConfigNodeClock): ConfigNodeClock => {
    const payload: ConfigNodeClock = {
      ...base,
      provider,
      interface: interfaceName,
      domain: Number(domainText),
    }
    if (holdoverLimitSecondsText.trim() !== '') payload.holdoverLimitSeconds = Number(holdoverLimitSecondsText)
    else delete payload.holdoverLimitSeconds
    if (provider === 'managed') {
      payload.clientOnly = clientOnly
      payload.hardwareTimestamping = hardwareTimestamping
      if (priority1Text.trim() !== '') payload.priority1 = Number(priority1Text)
      else delete payload.priority1
      delete payload.externalUdsAddress
      delete payload.fppBaseUrl
      delete payload.phcDevice
    } else if (provider === 'external') {
      if (externalUdsAddress.trim() !== '') payload.externalUdsAddress = externalUdsAddress
      else delete payload.externalUdsAddress
      if (phcDevice.trim() !== '') payload.phcDevice = phcDevice.trim()
      else delete payload.phcDevice
      delete payload.clientOnly
      delete payload.priority1
      delete payload.hardwareTimestamping
      delete payload.fppBaseUrl
    } else {
      payload.fppBaseUrl = fppBaseUrl
      delete payload.clientOnly
      delete payload.priority1
      delete payload.hardwareTimestamping
      delete payload.externalUdsAddress
      delete payload.phcDevice
    }
    return payload
  }

  const discard = () => {
    if (state.kind !== 'loaded') return
    loadFrom(state.response.payload)
    setSaveError(null)
  }

  const create = () => {
    setSaving(true)
    setSaveError(null)
    setTaken(false)
    guardedCreate({
      read: () => getNodeClock(nodeId),
      write: () => putNodeClock(nodeId, buildPayload({} as ConfigNodeClock)),
    })
      .then((outcome) => {
        if (outcome.kind === 'created') {
          setState({ kind: 'loaded', response: outcome.response })
          loadFrom(outcome.response.payload)
          setAttempt((n) => n + 1)
          return
        }
        if (outcome.kind === 'taken') {
          setTaken(true)
          return
        }
        setSaveError(outcome.reason)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  const save = () => {
    if (state.kind !== 'loaded' || !canSave) return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: () => getNodeClock(nodeId),
      write: () => putNodeClock(nodeId, buildPayload(state.response.payload)),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          setState({ kind: 'loaded', response: outcome.response })
          setDirty(false)
          setAttempt((n) => n + 1)
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

  if (state.kind === 'loading') {
    return (
      <Section id="st-node-clock" title="PTP clock">
        <RuledStrip absence="loading" label="Reading" fact={`Asking the coordinator for ${nodeId}'s clock configuration.`} />
      </Section>
    )
  }
  if (state.kind === 'failed') {
    return (
      <Section id="st-node-clock" title="PTP clock">
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      </Section>
    )
  }

  const fields = (
    <>
      <div className="sm-grid sm-form-column">
        <Segmented
          label="Provider"
          value={provider}
          options={PROVIDER_OPTIONS}
          onChange={(v) => {
            setProvider(v)
            setDirty(true)
          }}
        />
        <Field label="Interface">
          {(props) => (
            <Input
              {...props}
              value={interfaceName}
              onChange={(e) => {
                setInterfaceName(e.target.value)
                setDirty(true)
              }}
            />
          )}
        </Field>
        <Field label="PTP domain number">
          {(props) => (
            <Input
              {...props}
              value={domainText}
              onChange={(e) => {
                setDomainText(e.target.value)
                setDirty(true)
              }}
            />
          )}
        </Field>
        <Field label="Holdover limit (seconds)">
          {(props) => (
            <Input
              {...props}
              value={holdoverLimitSecondsText}
              onChange={(e) => {
                setHoldoverLimitSecondsText(e.target.value)
                setDirty(true)
              }}
            />
          )}
        </Field>
        {provider === 'managed' && (
          <>
            <Choice
              type="checkbox"
              checked={clientOnly}
              onChange={(e) => {
                setClientOnly(e.target.checked)
                setDirty(true)
              }}
              label="Client only (this node never attempts to become the domain's grandmaster)"
            />
            <Choice
              type="checkbox"
              checked={hardwareTimestamping}
              onChange={(e) => {
                setHardwareTimestamping(e.target.checked)
                setDirty(true)
              }}
              label="Hardware timestamping"
            />
            <Field label="Priority1 · optional">
              {(props) => (
                <Input
                  {...props}
                  value={priority1Text}
                  onChange={(e) => {
                    setPriority1Text(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
          </>
        )}
        {provider === 'external' && (
          <>
            <Field label="External UDS address · optional">
              {(props) => (
                <Input
                  {...props}
                  value={externalUdsAddress}
                  onChange={(e) => {
                    setExternalUdsAddress(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
            <Field label="PHC device · optional">
              {(props) => (
                <Input
                  {...props}
                  value={phcDevice}
                  onChange={(e) => {
                    setPhcDevice(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
          </>
        )}
        {provider === 'fpp' && (
          <Field label="FPP base URL">
            {(props) => (
              <Input
                {...props}
                value={fppBaseUrl}
                onChange={(e) => {
                  setFppBaseUrl(e.target.value)
                  setDirty(true)
                }}
              />
            )}
          </Field>
        )}
      </div>
      <div className="sm-panel sm-stack-4">
        <StatusPair tone={verdict.ok ? 'good' : 'bad'} label={verdict.ok ? 'Will be accepted' : 'Will be refused'} />
        {!verdict.ok && <p className="sm-small sm-muted">{verdict.reason}</p>}
      </div>
    </>
  )

  if (state.kind === 'notfound') {
    return (
      <Section id="st-node-clock" title="PTP clock">
        <RuledStrip
          absence="empty"
          label="Not configured"
          fact={`No node.clock object exists for ${nodeId}. It reports unsynchronized and behaves as it did before this seam existed.`}
        />
        {fields}
        <ButtonRow>
          <Button variant="primary" onClick={create} disabled={saving || !canSave || !saveGate.allowed} title={!saveGate.allowed ? saveGate.reason : !canSave ? verdict.reason : undefined}>
            {saving ? 'Creating…' : 'Create clock config'}
          </Button>
        </ButtonRow>
        {taken && (
          <RuledStrip
            absence="failed"
            label="Already exists"
            fact={
              <>
                A node.clock object for <span className="sm-data">{nodeId}</span> was created by someone else while you were
                editing. Your typed values are kept here.{' '}
                <button
                  type="button"
                  className="sm-linkbutton"
                  onClick={() => {
                    setTaken(false)
                    setAttempt((n) => n + 1)
                  }}
                >
                  Reload it
                </button>
              </>
            }
          />
        )}
        {saveError !== null && <RuledStrip absence="failed" label="Create failed" fact={saveError} />}
      </Section>
    )
  }

  return (
    <Section id="st-node-clock" title="PTP clock">
      {fields}
      <ButtonRow>
        <Button variant="primary" onClick={save} disabled={!dirty || saving || !canSave || !saveGate.allowed} title={!saveGate.allowed ? saveGate.reason : !canSave ? verdict.reason : undefined}>
          {saving ? 'Saving…' : 'Save clock config'}
        </Button>
        <Button variant="quiet" onClick={discard} disabled={!dirty || saving}>
          Discard changes
        </Button>
        <span className="sm-small sm-muted sm-push-end">
          Active revision <span className="sm-data">{state.response.revision}</span>
        </span>
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

      <RevisionHistory fetch={() => getNodeClockConfigRevisions(nodeId)} reloadKey={`${nodeId}:${attempt}`} mode="list" id="st-node-clock-rev" />
    </Section>
  )
}
