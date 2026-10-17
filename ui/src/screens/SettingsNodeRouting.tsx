import { useEffect, useState } from 'react'
import {
  ApiError,
  deleteAudioNode,
  getAudioNode,
  getAudioNodeConfigRevisions,
  getAudioRoutingChoices,
  getNodeClock,
  getNodeClockConfigRevisions,
  listConfigObjects,
  putAudioNode,
  putNodeClock,
  type AudioNodeConfigResponse,
  type AudioNodeSummary,
  type AudioRoutingChoicesResponse,
  type ConfigObjectSummary,
  type NodeClockConfigResponse,
} from '../api'
import { Button, ButtonRow, Choice, DeletePanel, Field, Input, RadioCardList, RevisionHistory, RuledStrip, Section, Segmented, Select, StatusPair, type Absence } from '../kit'
import type { ConfigAudioNode, ConfigAudioOutputLatency, ConfigNodeClock } from '../api'
import { useModelContext } from '../app/ModelContext'
import { routeName, routeShortName } from './audioRouteModel'
import { describeApiError, evaluateScope, type ScopeGateResult } from '../domain/session'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import {
  advertisedRoutes,
  audioNodeVerdict,
  channelCountLabel,
  channelListLabel,
  defaultRoutingEntryMode,
  hasAudioCapability,
  nodeClockVerdict,
  type NodeClockProvider,
  type RoutingEntryMode,
} from './settingsModel'

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
type ChoicesState = { kind: 'loading' } | { kind: 'loaded'; choices: AudioRoutingChoicesResponse } | { kind: 'failed'; reason: string; forbidden: boolean }

const ENTRY_MODE_OPTIONS: readonly { value: RoutingEntryMode; label: string }[] = [
  { value: 'choices', label: 'From the node' },
  { value: 'manual', label: 'Enter by hand' },
]

const DISCOVERY_ABSENCE: Record<AudioRoutingChoicesResponse['discovery'], Absence> = {
  available: 'unavailable',
  stale: 'stale',
  failed: 'failed',
  partial: 'unavailable',
  not_reported: 'unobserved',
  absent: 'unavailable',
}

const DISCOVERY_LABEL: Record<AudioRoutingChoicesResponse['discovery'], string> = {
  available: 'No outputs',
  stale: 'Offline',
  failed: 'Unreadable',
  partial: 'Partial',
  not_reported: 'Not listed',
  absent: 'No outputs',
}

type NodeState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: AudioNodeConfigResponse }
  | { kind: 'notfound' }
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
  const [nodesAttempt, setNodesAttempt] = useState(0)
  const [selectedId, setSelectedId] = useState<string | null>(null)
  // node.clock is a separate config kind from audio.node (RES-019, ADR-039):
  // a node can carry a PTP clock configuration without ever advertising audio
  // routing, so its own node id is chosen independently of the audio.node
  // picker above rather than reused from `selectedId`.
  const [nodeClockObjectsState, setNodeClockObjectsState] = useState<NodeClockObjectsState>({ kind: 'loading' })
  const [clockNodeId, setClockNodeId] = useState('')

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
  },[nodesAttempt])

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

  // A node that reports audio outputs but has nothing saved is offered too, so
  // the first routing for a node can be created from its own report.
  const unconfiguredAudioNodes =
    nodesState.kind === 'loaded' ? model.nodes.filter((n) => hasAudioCapability(n) && !nodesState.nodes.some((s) => s.id === n.nodeId)) : []
  const shownId = selectedId ?? unconfiguredAudioNodes[0]?.nodeId ?? null

  // Only nodes the coordinator knows and that have no clock settings yet, so a
  // mistyped or invented node id cannot be set up.
  const clocklessNodes =
    nodeClockObjectsState.kind === 'loaded' ? model.nodes.filter((n) => !nodeClockObjectsState.objects.some((o) => o.id === n.nodeId)) : []

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Audio <span className="sm-faint">/</span> Node routing</p>
      <h2 className="sm-section__title">Where this node's audio leaves the building</h2>

      <Section id="st-node" title="Node">
        {nodesState.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for configured audio nodes." />
        ) : nodesState.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={nodesState.reason} />
        ) : nodesState.nodes.length === 0 && unconfiguredAudioNodes.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="No node reports audio outputs, and none has audio routing saved." />
        ) : (
          <Field label="Audio node">
            {(props) => (
              <Select
                {...props}
                value={shownId ?? ''}
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
                {unconfiguredAudioNodes.map((node) => (
                  <option key={node.nodeId} value={node.nodeId}>
                    {node.nodeId}, not set up
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
      </Section>

      {shownId !== null && (
        <NodeRoutingForm
          key={`audio:${shownId}`}
          nodeId={shownId}
          saveGate={gate}
          onCreated={(id) => {
            setSelectedId(id)
            setNodesAttempt((n) => n + 1)
          }}
          onDeleted={(id) => {
            setNodesState((prev) => (prev.kind !== 'loaded' ? prev : { kind: 'loaded', nodes: prev.nodes.filter((n) => n.id !== id) }))
            setSelectedId((prev) => (prev === id ? null : prev))
          }}
        />
      )}

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
        <Field label="Set up a node">
          {(props) => (
            <Select
              {...props}
              value={clocklessNodes.some((n) => n.nodeId === clockNodeId) ? clockNodeId : ''}
              disabled={clocklessNodes.length === 0}
              onChange={(e) => setClockNodeId(e.target.value)}
            >
              <option value="">{clocklessNodes.length === 0 ? 'Every node already has clock settings' : 'Select a node…'}</option>
              {clocklessNodes.map((n) => (
                <option key={n.nodeId} value={n.nodeId}>
                  {n.nodeId}
                </option>
              ))}
            </Select>
          )}
        </Field>
      </Section>

      {clockNodeId.trim() !== '' && <NodeClockSection key={`clock:${clockNodeId.trim()}`} nodeId={clockNodeId.trim()} saveGate={gate} />}
    </>
  )
}

function NodeRoutingForm({ nodeId, saveGate, onDeleted, onCreated }: { nodeId: string; saveGate: ScopeGateResult; onDeleted: (id: string) => void; onCreated: (id: string) => void }) {
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
  const [deleting, setDeleting] = useState(false)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [choicesAttempt, setChoicesAttempt] = useState(0)
  const [choicesState, setChoicesState] = useState<ChoicesState>({ kind: 'loading' })
  const [modeOverride, setModeOverride] = useState<RoutingEntryMode | null>(null)

  useEffect(() => {
    let cancelled = false
    setChoicesState({ kind: 'loading' })
    getAudioRoutingChoices(nodeId)
      .then((choices) => {
        if (!cancelled) setChoicesState({ kind: 'loaded', choices })
      })
      .catch((err: unknown) => {
        if (!cancelled) setChoicesState({ kind: 'failed', reason: describeApiError(err), forbidden: err instanceof ApiError && err.status === 403 })
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, attempt, choicesAttempt])

  const choices = choicesState.kind === 'loaded' ? choicesState.choices : null
  const choicesUsable = choices !== null && choices.discovery === 'available' && choices.routes.length > 0
  const manualRefusal = choices !== null && !choices.manualEntry.allowed ? (choices.manualEntry.reason ?? 'This node does not accept channels entered by hand.') : null
  const entryMode: RoutingEntryMode = !choicesUsable
    ? 'manual'
    : manualRefusal !== null
      ? 'choices'
      : (modeOverride ?? defaultRoutingEntryMode(choices))
  const pipewireOutputs = choices?.routes.filter((r) => r.source === 'pipewire') ?? []
  const routeChoice = entryMode === 'choices' ? (choices?.routes.find((r) => r.route === programRoute) ?? null) : null

  const removeNode = () => {
    setDeleting(true)
    setDeleteError(null)
    deleteAudioNode(nodeId)
      .then(() => onDeleted(nodeId))
      .catch((err: unknown) => setDeleteError(describeApiError(err)))
      .finally(() => setDeleting(false))
  }

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
        setModeOverride(null)
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'notfound' })
          return
        }
        setState({ kind: 'failed', reason: describeApiError(err) })
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
  const chosenGroup = routeChoice?.programGroups.find((g) => g.channels.join(',') === programChannels.join(',')) ?? null
  const noLtcChannelReason =
    chosenGroup === null || chosenGroup.ltcChannels.length > 0
      ? null
      : choices?.ltc.available === false && choices.ltc.reason !== undefined
        ? choices.ltc.reason
        : routeChoice?.programGroups.some((g) => g.ltcChannels.length > 0)
          ? 'No channel is left for timecode. Turn LTC off or pick other program channels.'
          : 'This output has no spare channel for timecode. Turn LTC off or pick another.'
  const choiceReason =
    entryMode !== 'choices'
      ? (manualRefusal ?? undefined)
      : routeChoice === null
        ? 'Choose an output this node reports.'
        : chosenGroup === null
          ? 'Choose the program channels.'
          : ltcOn && noLtcChannelReason !== null
            ? noLtcChannelReason
            : ltcOn && !chosenGroup.ltcChannels.includes(Number(ltcChannelText))
              ? 'Choose a timecode channel from the ones offered.'
              : undefined
  const canSave =
    choiceReason === undefined &&
    verdict.ok &&
    channelsValid &&
    ltcChannelValid &&
    zoneValid &&
    outputLatencyValueUsValid &&
    outputLatencyProvenanceValid

  const setEntryMode = (mode: RoutingEntryMode) => {
    setModeOverride(mode)
    if (mode === 'choices' && choices !== null && !choices.routes.some((r) => r.route === programRoute)) {
      setProgramRoute(choices.routes[0]?.route ?? '')
      setProgramChannelsText('')
      setLtcChannelText('')
    }
    setDirty(true)
  }

  const chooseGroup = (value: string) => {
    const group = routeChoice?.programGroups.find((g) => g.channels.join(',') === value)
    if (group === undefined) return
    setProgramChannelsText(group.channels.join(', '))
    if (!group.ltcChannels.includes(Number(ltcChannelText))) setLtcChannelText('')
    setDirty(true)
  }

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
    setModeOverride(null)
    setDirty(false)
    setSaveError(null)
  }

  const buildPayload = (base: ConfigAudioNode): ConfigAudioNode => {
    // Starts from the payload this edit was loaded from and overrides only
    // the fields this form manages, so a field it does not manage survives.
    const payload: ConfigAudioNode = {
      ...base,
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
    return payload
  }

  const create = () => {
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedCreate({
      read: () => getAudioNode(nodeId),
      write: () => putAudioNode(nodeId, buildPayload({} as ConfigAudioNode)),
    })
      .then((outcome) => {
        if (outcome.kind === 'created') {
          setState({ kind: 'loaded', response: outcome.response })
          setDirty(false)
          setAttempt((n) => n + 1)
          onCreated(nodeId)
          return
        }
        if (outcome.kind === 'taken') {
          setSaveError(`Routing for ${nodeId} was saved by someone else while you were editing. Reload the page to see it.`)
          return
        }
        setSaveError(outcome.reason)
      })
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  const save = () => {
    if (!canSave) return
    if (state.kind === 'notfound') {
      create()
      return
    }
    if (state.kind !== 'loaded') return
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: state.response,
      read: () => getAudioNode(nodeId),
      write: () => putAudioNode(nodeId, buildPayload(state.response.payload)),
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
  const isNew = state.kind === 'notfound'

  return (
    <>
      {isNew && <RuledStrip absence="empty" label="Not set up" fact={`${nodeId} has no audio routing yet. Choose its output below and save to set it up.`} />}
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
          <RoutingChoicesStatus
            state={choicesState}
            usable={choicesUsable}
            entryMode={entryMode}
            suggestManual={saveGate.allowed && manualRefusal === null}
            onRefresh={() => setChoicesAttempt((n) => n + 1)}
          />
          {manualRefusal !== null && !choicesUsable && <p className="sm-small sm-muted">{manualRefusal}</p>}
          {choicesUsable && manualRefusal === null && (
            <Segmented label="Channels" value={entryMode} options={ENTRY_MODE_OPTIONS} onChange={setEntryMode} />
          )}
          {entryMode === 'choices' && choices !== null ? (
            <>
              <Field label="Output">
                {(props) => (
                  <Select
                    {...props}
                    value={programRoute}
                    onChange={(e) => {
                      const picked = choices.routes.find((r) => r.route === e.target.value)
                      setProgramRoute(e.target.value)
                      setProgramChannelsText('')
                      setLtcChannelText('')
                      if (picked !== undefined) {
                        // A PipeWire output plays only through PipeWire, aimed at that output.
                        setSinkBackend(picked.source === 'pipewire' ? 'pipewiresink' : 'alsasink')
                        setPipewireTargetNode(picked.source === 'pipewire' ? picked.route : '')
                      }
                      setDirty(true)
                    }}
                  >
                    {routeChoice === null && <option value="">Choose an output</option>}
                    {choices.routes.map((route) => (
                      <option key={route.route} value={route.route} title={route.route}>
                        {routeName(route.route, route.shortName)}, {channelCountLabel(route)}
                      </option>
                    ))}
                  </Select>
                )}
              </Field>
              {routeChoice !== null && (
                <RadioCardList
                  label="Program channels"
                  options={routeChoice.programGroups.map((g) => ({
                    value: g.channels.join(','),
                    title: channelListLabel(g.channels),
                    desc:
                      g.ltcChannels.length > 0
                        ? `Timecode can use ${g.ltcChannels.join(', ')}.`
                        : (routeChoice.ltcReason ?? 'No channel is left for timecode.'),
                  }))}
                  value={chosenGroup?.channels.join(',') ?? ''}
                  onChange={chooseGroup}
                />
              )}
            </>
          ) : (
            <>
              {programRoutes !== null && programRoutes.length > 0 ? (
                <Field label="Route">
                  {(props) => (
                    <Select
                      {...props}
                      value={programRoute}
                      disabled={manualRefusal !== null}
                      onChange={(e) => {
                        setProgramRoute(e.target.value)
                        setDirty(true)
                      }}
                    >
                      {programRoute !== '' && !programRoutes.includes(programRoute) && (
                        <option value={programRoute}>{programRoute}, no longer reported by this node</option>
                      )}
                      {programRoutes.map((route) => (
                        <option key={route} value={route} title={route}>
                          {routeName(route, routeShortName(node ?? undefined, route))}
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
                        disabled={manualRefusal !== null}
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
                    disabled={manualRefusal !== null}
                    onChange={(e) => {
                      setProgramChannelsText(e.target.value)
                      setDirty(true)
                    }}
                  />
                )}
              </Field>
            </>
          )}
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
              {(props) =>
                pipewireOutputs.length > 0 ? (
                  <Select
                    {...props}
                    value={pipewireTargetNode}
                    onChange={(e) => {
                      setPipewireTargetNode(e.target.value)
                      setDirty(true)
                    }}
                  >
                    <option value="">PipeWire's default output</option>
                    {pipewireTargetNode !== '' && !pipewireOutputs.some((r) => r.route === pipewireTargetNode) && (
                      <option value={pipewireTargetNode}>{pipewireTargetNode}, no longer reported by this node</option>
                    )}
                    {pipewireOutputs.map((r) => (
                      <option key={r.route} value={r.route} title={r.route}>
                        {routeName(r.route, r.shortName)}
                      </option>
                    ))}
                  </Select>
                ) : (
                  <Input
                    {...props}
                    value={pipewireTargetNode}
                    onChange={(e) => {
                      setPipewireTargetNode(e.target.value)
                      setDirty(true)
                    }}
                  />
                )
              }
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
            <button
              type="button"
              className="sm-segmented__item"
              aria-pressed={ltcOn}
              disabled={entryMode === 'choices' && !ltcOn && choices?.ltc.available === false}
              title={entryMode === 'choices' && choices?.ltc.available === false ? choices.ltc.reason : undefined}
              onClick={() => { setLtcOn(true); setDirty(true) }}
            >
              On
            </button>
          </span>
        </div>
        {entryMode === 'choices' && !ltcOn && choices?.ltc.available === false && choices.ltc.reason !== undefined && (
          <p className="sm-small sm-muted sm-stack-4">{choices.ltc.reason}</p>
        )}
        {ltcOn && (
          <div className="sm-grid sm-form-column sm-stack-4">
            <div className="sm-field">
              <span className="sm-field__label">Route</span>
              <p className="sm-input sm-data sm-muted" title={programRoute}>
                {programRoute === '' ? 'Set a program route first' : routeName(programRoute, routeShortName(node ?? undefined, programRoute))}
              </p>
            </div>
            {entryMode === 'choices' ? (
              chosenGroup === null ? (
                <p className="sm-small sm-muted">Choose the program channels first. Timecode uses a channel they leave free.</p>
              ) : noLtcChannelReason !== null ? (
                <p className="sm-small sm-muted">{noLtcChannelReason}</p>
              ) : (
                <Field label="Channel">
                  {(props) => (
                    <Select
                      {...props}
                      value={chosenGroup.ltcChannels.includes(Number(ltcChannelText)) ? ltcChannelText : ''}
                      onChange={(e) => {
                        setLtcChannelText(e.target.value)
                        setDirty(true)
                      }}
                    >
                      {!chosenGroup.ltcChannels.includes(Number(ltcChannelText)) && <option value="">Select a channel</option>}
                      {chosenGroup.ltcChannels.map((ch) => (
                        <option key={ch} value={String(ch)}>
                          {ch}
                        </option>
                      ))}
                    </Select>
                  )}
                </Field>
              )
            ) : (
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
            )}
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
            ? `One route, program on channel${programChannels.length === 1 ? '' : 's'} ${programChannels.join(' and ')}${ltcOn ? `, LTC on ${ltcChannelText}` : ', no LTC'}.`
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
                ? (choiceReason !== undefined
                    ? choiceReason
                    : !verdict.ok
                    ? verdict.reason
                    : !zoneValid
                      ? 'A zone name is required when the role is zone.'
                      : !outputLatencyValueUsValid
                        ? 'Output latency value must be a whole number of microseconds.'
                        : !outputLatencyProvenanceValid
                          ? 'A measured latency needs measured at, reference, confidence and configuration.'
                          : undefined)
                : undefined
          }
        >
          {saving ? 'Saving…' : 'Save routing'}
        </Button>
        {!isNew && (
          <>
            <Button variant="quiet" onClick={discard} disabled={!dirty || saving}>
              Discard changes
            </Button>
            <div className="sm-push-end">
              <RevisionHistory fetch={() => getAudioNodeConfigRevisions(nodeId)} reloadKey={`${nodeId}:${attempt}`} />
            </div>
          </>
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

      {!isNew && <DeletePanel
        title="Delete this node"
        confirmNoun="the node's own id"
        confirmValue={nodeId}
        actionLabel="Delete node"
        deleting={deleting}
        error={deleteError}
        allowed={saveGate.allowed}
        disallowedReason={saveGate.allowed ? undefined : saveGate.reason}
        onDelete={removeNode}
      >
        <p className="sm-small sm-muted">Show actions and cues that target this node will report a missing target.</p>
      </DeletePanel>}
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
          fact={`${nodeId} has no clock settings and reports unsynchronized.`}
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
                Someone else saved clock settings for <span className="sm-data">{nodeId}</span>. Your values are kept.{' '}
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
        <div className="sm-push-end">
          <RevisionHistory fetch={() => getNodeClockConfigRevisions(nodeId)} reloadKey={`${nodeId}:${attempt}`} id="st-node-clock-rev" />
        </div>
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
  )
}

// Why choices are or are not offered, and the saved placement's standing against them.
function RoutingChoicesStatus({
  state,
  usable,
  entryMode,
  suggestManual,
  onRefresh,
}: {
  state: ChoicesState
  usable: boolean
  entryMode: RoutingEntryMode
  suggestManual: boolean
  onRefresh: () => void
}) {
  if (state.kind === 'loading') {
    return <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator which outputs this node reports." />
  }
  const refresh = (
    <Button variant="quiet" onClick={onRefresh}>
      Check again
    </Button>
  )
  if (state.kind === 'failed') {
    return (
      <div className="sm-grid sm-stack-4">
        <RuledStrip absence="failed" label="Read failed" fact={suggestManual && !state.forbidden ? `${state.reason} Enter channels by hand below.` : state.reason} />
        <ButtonRow>{refresh}</ButtonRow>
      </div>
    )
  }
  const choices = state.choices
  const current = choices.current
  const keptNote = entryMode === 'manual' && current !== undefined && !current.offered && current.reason !== undefined ? current.reason : null
  if (usable) {
    return keptNote === null ? null : <p className="sm-small sm-muted">{keptNote}</p>
  }
  const fact =
    choices.discovery === 'available'
      ? `This node reports no outputs with channels to choose from.${suggestManual ? ' Enter channels by hand below.' : ''}`
      : (choices.reason ?? `This node reports no outputs.${suggestManual ? ' Enter channels by hand below.' : ''}`)
  return (
    <div className="sm-grid sm-stack-4">
      <RuledStrip absence={DISCOVERY_ABSENCE[choices.discovery]} label={DISCOVERY_LABEL[choices.discovery]} fact={fact} />
      {keptNote !== null && <p className="sm-small sm-muted">{keptNote}</p>}
      <ButtonRow>{refresh}</ButtonRow>
    </div>
  )
}
