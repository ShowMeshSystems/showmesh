import { useEffect, useState } from 'react'
import {
  getAudioNode,
  getAudioNodeConfigRevisions,
  listConfigObjects,
  putAudioNode,
  type AudioNodeConfigResponse,
  type ConfigObjectSummary,
  type ConfigRevisionMeta,
} from '../api'
import { Button, ButtonRow, Field, Input, NotWiredBanner, RuledStrip, Section, Select, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope, type ScopeGateResult } from '../domain/session'
import { formatClock } from '../domain/time'
import { guardedSave, type SaveOutcome } from '../domain/save'
import { StaleWriteStrip } from './StaleWrite'
import { advertisedRoutes, audioNodeVerdict, hasAudioCapability } from './settingsModel'

type NodesState = { kind: 'loading' } | { kind: 'loaded'; nodes: ConfigObjectSummary[] } | { kind: 'failed'; reason: string }
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

export function SettingsNodeRouting() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')

  const [nodesState, setNodesState] = useState<NodesState>({ kind: 'loading' })
  const [selectedId, setSelectedId] = useState<string | null>(null)

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

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Audio <span className="sm-faint">/</span> Node routing</p>
      <h2 className="sm-section__title">Where this node's audio leaves the building</h2>
      <p className="sm-page__lede">
        Program and LTC leave through one interface in one clock domain. The coordinator refuses a split. A route
        this node has not advertised is refused on save.
      </p>

      <Section id="st-node" title="Node">
        {nodesState.kind === 'loading' ? (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for configured audio nodes." />
        ) : nodesState.kind === 'failed' ? (
          <RuledStrip absence="failed" label="Read failed" fact={nodesState.reason} />
        ) : nodesState.nodes.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="No audio.node object has ever been configured." />
        ) : (
          <Field label="Audio node" help={<>Nodes advertising <span className="sm-data">audio.output.local</span> or <span className="sm-data">audio.output.ltc</span>.</>}>
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

      {selectedId !== null && <NodeRoutingForm key={selectedId} nodeId={selectedId} saveGate={gate} />}
    </>
  )
}

function NodeRoutingForm({ nodeId, saveGate }: { nodeId: string; saveGate: ScopeGateResult }) {
  const model = useModelContext()
  const node = model.nodes.find((n) => n.nodeId === nodeId) ?? null
  const programRoutes = node !== null ? advertisedRoutes(node, 'audio.output.local') : null
  const ltcRoutesAdvertised = node !== null ? advertisedRoutes(node, 'audio.output.ltc') : null
  const heardAt = node?.evidence.hello.observedAt ?? null

  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<NodeState>({ kind: 'loading' })
  const [revisions, setRevisions] = useState<ConfigRevisionMeta[] | null>(null)

  const [programRoute, setProgramRoute] = useState('')
  const [programChannelsText, setProgramChannelsText] = useState('')
  const [ltcOn, setLtcOn] = useState(false)
  const [ltcChannelText, setLtcChannelText] = useState('')
  const [clockDomain, setClockDomain] = useState('')
  const [clockDomainProvenance, setClockDomainProvenance] = useState('')
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
        setClockDomain(response.payload.clockDomain)
        setClockDomainProvenance(response.payload.clockDomainProvenance)
        setDirty(false)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, attempt])

  useEffect(() => {
    let cancelled = false
    getAudioNodeConfigRevisions(nodeId)
      .then((response) => {
        if (!cancelled) setRevisions(response.revisions)
      })
      .catch(() => {
        if (!cancelled) setRevisions(null)
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
  const canSave = verdict.ok && channelsValid && ltcChannelValid && clockDomain.trim() !== '' && clockDomainProvenance.trim() !== ''

  const discard = () => {
    if (state.kind !== 'loaded') return
    setProgramRoute(state.response.payload.programRoute)
    setProgramChannelsText(state.response.payload.programChannels.join(', '))
    setLtcOn(state.response.payload.ltcRoute !== undefined && state.response.payload.ltcRoute !== '')
    setLtcChannelText(state.response.payload.ltcChannel !== undefined ? String(state.response.payload.ltcChannel) : '')
    setClockDomain(state.response.payload.clockDomain)
    setClockDomainProvenance(state.response.payload.clockDomainProvenance)
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
      write: () =>
        putAudioNode(nodeId, {
          programRoute,
          programChannels,
          clockDomain,
          clockDomainProvenance,
          ...(ltcOn ? { ltcRoute: programRoute, ltcChannel: Number(ltcChannelText) } : {}),
        }),
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
      <Section id="st-program" title="Program output">
        <div className="sm-grid sm-form-column">
          {programRoutes !== null && programRoutes.length > 0 ? (
            <Field label="Route" help={heardAt !== null ? `Advertised by ${nodeId} at ${formatClock(heardAt)}.` : `Advertised by ${nodeId}.`}>
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
              <p className="sm-small sm-faint" style={{ textTransform: 'uppercase', letterSpacing: '0.08em' }}>No channel inventory</p>
              <p className="sm-small sm-muted">
                {nodeId} has advertised no routes. The agent advertises routes, not which channels exist behind them,
                so there is nothing to pick from. Enter the route yourself. A route or channel the node rejects is
                refused on save.
              </p>
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
          <Field label="Program channels" help="Distinct, 1-based, comma separated.">
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
        </div>

        <div className="sm-panel sm-stack-4">
          <NotWiredBanner
            what="Output groups"
            missing={
              <>
                <code className="sm-data">outputGroups</code> attribute on the <code className="sm-data">audio.output.local</code> capability
              </>
            }
            detail="Once the agent advertises groups, this picker replaces the manual channel field above. Nothing sends outputGroups today; the picker is shown so the target is on record, not because it works."
          />
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
              <span className="sm-field__help">Follows the program route. It cannot differ.</span>
            </div>
            <Field
              label="Channel"
              help={
                ltcRoutesAdvertised === null
                  ? '1-based, and not one of the program channels above. No inventory is advertised, so this is entered too.'
                  : '1-based, and not one of the program channels above.'
              }
            >
              {(props) => (
                <Input
                  {...props}
                  value={ltcChannelText}
                  onChange={(e) => {
                    setLtcChannelText(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
          </div>
        )}
      </Section>

      <Section id="st-clock" title="Clock domain">
        <div className="sm-panel" style={{ borderStyle: 'dashed' }}>
          <p className="sm-small sm-faint" style={{ textTransform: 'uppercase', letterSpacing: '0.08em' }}>No API evidence</p>
          <p className="sm-small sm-muted">
            The coordinator does not advertise authoritative clock choices, so there is nothing to pick from and no
            browser clock is used. These two fields are your declaration, and they are recorded as such.
          </p>
          <div className="sm-grid sm-stack-4">
            <Field label="Domain">
              {(props) => (
                <Input
                  {...props}
                  value={clockDomain}
                  onChange={(e) => {
                    setClockDomain(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
            <Field label="How you know" help="Recorded with the revision so a later reader knows what this claim rests on.">
              {(props) => (
                <Input
                  {...props}
                  value={clockDomainProvenance}
                  onChange={(e) => {
                    setClockDomainProvenance(e.target.value)
                    setDirty(true)
                  }}
                />
              )}
            </Field>
          </div>
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
          title={!saveGate.allowed ? saveGate.reason : !canSave ? (verdict.ok ? undefined : verdict.reason) : undefined}
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

      <Section id="st-rev" title="Revisions">
        {revisions === null ? (
          <RuledStrip absence="unobserved" label="Unread" fact="Revision history could not be read just now." />
        ) : revisions.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="No prior revision recorded." />
        ) : (
          <div>
            {revisions.map((rev) => (
              <div key={rev.revision} className="sm-inline-row sm-stack-3">
                <StatusPair tone={rev.active ? 'good' : 'pending'} label={rev.active ? `Active · ${rev.revision}` : String(rev.revision)} />
                <p className="sm-small sm-muted">
                  {formatClock(rev.createdAt) ?? 'unrecorded time'} by {rev.createdByPrincipalName ?? 'unknown principal'}
                  {rev.note !== '' && `. ${rev.note}`}
                </p>
              </div>
            ))}
          </div>
        )}
      </Section>
    </>
  )
}
