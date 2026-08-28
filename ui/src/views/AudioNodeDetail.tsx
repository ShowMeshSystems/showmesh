import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getAudioNode, getAudioNodeConfigRevisions, putAudioNode, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { useUnsavedChanges } from '../app/UnsavedChanges'
import type { AudioNodeConfigResponse, ConfigAudioNode, Node } from '../app/types'

// ADR-018/ADR-039: one audio.node object's editor, the second of the two
// audio configuration kinds this build closes the UI gap for. Mirrors
// ShowCueDetail.tsx's per-object shape (full replacement PUT, revision
// history, config:write gates both read and write alike -- audio.node
// has no separate read scope, unlike show.cue's show:macro:run/
// config:write pair).
const CONFIG_WRITE_SCOPE = 'config:write'

interface FormState {
  programRoute: string
  ltcRoute: string
  programChannels: string
  ltcChannel: string
  clockDomain: string
  clockDomainProvenance: string
}

function emptyForm(): FormState {
  return {
    programRoute: '',
    ltcRoute: '',
    programChannels: '',
    ltcChannel: '',
    clockDomain: '',
    clockDomainProvenance: '',
  }
}

function formFromPayload(payload: ConfigAudioNode): FormState {
  return {
    programRoute: payload.programRoute,
    // A program-only node stores neither LTC field; both come back as
    // blank, which is the same shape buildPayload treats as "no LTC".
    ltcRoute: payload.ltcRoute ?? '',
    programChannels: payload.programChannels.join(', '),
    ltcChannel: payload.ltcChannel === undefined ? '' : String(payload.ltcChannel),
    clockDomain: payload.clockDomain,
    clockDomainProvenance: payload.clockDomainProvenance,
  }
}

/**
 * A capability's `attributes` is untyped on the wire ({[key: string]:
 * unknown}, generated/schema.d.ts's own Capability shape) -- this reads
 * the one field internal/agent/audiocapabilities.go's routeAttributes
 * actually puts there (`routes: string[]`) without trusting its shape.
 */
function attributeRoutes(attributes: Record<string, unknown> | undefined): string[] {
  if (attributes === undefined) return []
  const routes = attributes.routes
  if (!Array.isArray(routes)) return []
  return routes.filter((r): r is string => typeof r === 'string')
}

/**
 * Program and LTC route names come from their distinct capability
 * advertisements. The coordinator requires both names to match when LTC is
 * enabled, so the LTC picker is narrowed to the selected program route below.
 * A program-only node can still use a program-capable route that is not LTC
 * capable.
 */
function capabilityAttributes(node: Node | undefined, id: string): Record<string, unknown> | undefined {
  const capability = node?.capabilities.find((candidate) => candidate.id === id)
  return capability?.attributes as Record<string, unknown> | undefined
}

function advertisedRoutes(node: Node | undefined, capabilityId: string): string[] {
  return attributeRoutes(capabilityAttributes(node, capabilityId))
}

interface OutputGroup {
  id: string
  label: string
  channels: number[]
}

interface ChannelEvidence {
  channels: number[]
  groups: OutputGroup[]
}

/**
 * Read channel inventories only when the node capability actually carries
 * them. The current agent advertises routes, not channel inventories, so the
 * manual editor below is intentionally retained as a visibly separate
 * fallback. In particular, outputCount is a route count and must never be
 * turned into a made-up list of channels.
 */
function channelEvidence(node: Node | undefined, route: string, capabilityId = 'audio.output.local'): ChannelEvidence {
  const attributes = capabilityAttributes(node, capabilityId)
  if (attributes === undefined) return { channels: [], groups: [] }

  const channels = Array.isArray(attributes.channels)
    ? attributes.channels.filter(
        (channel): channel is number =>
          typeof channel === 'number' && Number.isInteger(channel) && channel > 0,
      )
    : []
  const groups = Array.isArray(attributes.outputGroups)
    ? attributes.outputGroups.flatMap((group, index): OutputGroup[] => {
        if (typeof group !== 'object' || group === null) return []
        const candidate = group as Record<string, unknown>
        const groupChannels = Array.isArray(candidate.channels)
          ? candidate.channels.filter(
              (channel): channel is number =>
                typeof channel === 'number' && Number.isInteger(channel) && channel > 0,
            )
          : []
        if (groupChannels.length === 0) return []
        const id = typeof candidate.id === 'string' ? candidate.id : `${route}-group-${index + 1}`
        const label = typeof candidate.label === 'string' ? candidate.label : id
        return [{ id, label, channels: groupChannels }]
      })
    : []
  return { channels: [...new Set(channels)].sort((a, b) => a - b), groups }
}

/**
 * Mirrors, not enforces (ADR-030): every check here also exists
 * server-side. programRoute/ltcRoute's live cross-check against this
 * node's own capability advertisement is NOT mirrored here beyond
 * restricting each picker to its own advertised capability when that evidence
 * exists. When no evidence is available the manual fallback remains visible
 * and the coordinator's own refusal is what the operator sees.
 */
function buildPayload(form: FormState): { payload: ConfigAudioNode } | { error: string } {
  if (form.programRoute.trim() === '') return { error: 'Program route is required.' }

  // LTC route and LTC channel are optional TOGETHER: both blank declares
  // a program-only node that emits no LTC, which is the only shape a
  // two-output interface can be declared in. One without the other is
  // refused here for the same reason the coordinator refuses it, rather
  // than sending half a declaration and letting the server explain.
  const wantLTC = form.ltcRoute.trim() !== '' || form.ltcChannel.trim() !== ''
  if (wantLTC && form.ltcRoute.trim() === '') {
    return { error: 'LTC route is required when an LTC channel is given. Clear both to declare a program-only node.' }
  }
  if (wantLTC && form.ltcChannel.trim() === '') {
    return { error: 'LTC channel is required when an LTC route is given. Clear both to declare a program-only node.' }
  }
  if (wantLTC && form.programRoute.trim() !== form.ltcRoute.trim()) {
    return {
      error:
        'Program route and LTC route must name the same route: program and LTC leave through one interface in one clock domain.',
    }
  }

  const programChannels = form.programChannels
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s !== '')
    .map((s) => Number(s))
  if (
    programChannels.length === 0 ||
    programChannels.some((n) => !Number.isInteger(n) || n < 1) ||
    new Set(programChannels).size !== programChannels.length
  ) {
    return {
      error:
        'Program channels must be a comma-separated list of distinct, positive, 1-based channel indices (e.g. "1, 2").',
    }
  }

  const ltcChannel = Number(form.ltcChannel)
  if (wantLTC && (!Number.isInteger(ltcChannel) || ltcChannel < 1)) {
    return { error: 'LTC channel must be a positive, 1-based channel index.' }
  }
  if (wantLTC && programChannels.includes(ltcChannel)) {
    return { error: 'LTC channel must not also appear in program channels.' }
  }

  if (form.clockDomain.trim() === '') return { error: 'Clock domain is required.' }
  if (form.clockDomainProvenance.trim() === '') return { error: 'Clock domain provenance is required.' }

  const payload: ConfigAudioNode = {
    programRoute: form.programRoute.trim(),
    programChannels,
    clockDomain: form.clockDomain.trim(),
    clockDomainProvenance: form.clockDomainProvenance.trim(),
  }
  if (wantLTC) {
    payload.ltcRoute = form.ltcRoute.trim()
    payload.ltcChannel = ltcChannel
  }
  return { payload }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: AudioNodeConfigResponse; revisions: ConfigRevisionMeta[] }

export interface AudioNodeDetailProps {
  isNew?: boolean
}

export function AudioNodeDetail({ isNew = false }: AudioNodeDetailProps) {
  const { clearUnsavedChanges } = useUnsavedChanges()
  const params = useParams<{ id: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm())
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)

  useEffect(() => {
    if (!scopeGate.allowed) clearUnsavedChanges()
  }, [clearUnsavedChanges, scopeGate.allowed])

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!scopeGate.allowed) return
    clearUnsavedChanges()
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getAudioNode(existingId), getAudioNodeConfigRevisions(existingId)])
      .then(([config, revisionsResp]) => {
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setForm(formFromPayload(config.payload))
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [clearUnsavedChanges, existingId, scopeGate.allowed, isNew])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A node id is required.')
      return
    }
    const built = buildPayload(form)
    if ('error' in built) {
      setSaveError(built.error)
      return
    }
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      const resp = await putAudioNode(id, built.payload)
      clearUnsavedChanges()
      if (isNew) {
        navigate(`/config/audio.node/${encodeURIComponent(id)}`)
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      const revisionsResp = await getAudioNodeConfigRevisions(id)
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, revisions: revisionsResp.revisions } : prev))
    } catch (err) {
      // A refused write never reads as saved: state above is untouched on
      // throw, and the coordinator's own reason renders below -- the
      // identical shape as ShowCueDetail.tsx's handleSave.
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  if (!scopeGate.allowed) {
    return (
      <div>
        <h2 className="panel__title">{isNew ? 'New audio node' : 'Audio node'}</h2>
        <p className="panel panel--error" role="status">
          {scopeGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading audio node…</p>
  }
  if (!isNew && state.kind === 'error') {
    return (
      <p className="panel panel--error" role="alert">
        {state.message}
      </p>
    )
  }

  const targetNodeId = isNew ? newId.trim() : (existingId ?? '')
  const targetNode = model.nodes.find((n) => n.nodeId === targetNodeId)
  const programRoutes = advertisedRoutes(targetNode, 'audio.output.local')
  const ltcRoutes = advertisedRoutes(targetNode, 'audio.output.ltc')
  const channelInventory = channelEvidence(targetNode, form.programRoute)
  const ltcChannelInventory = channelEvidence(targetNode, form.programRoute, 'audio.output.ltc')
  const ltcChannels = ltcChannelInventory.channels.filter((channel) => {
    const programChannels = form.programChannels
      .split(',')
      .map((value) => Number(value.trim()))
      .filter((value) => Number.isInteger(value) && value > 0)
    return !programChannels.includes(channel)
  })
  const selectedNodeIsOffline = targetNode !== undefined && targetNode.controlPlane.state !== 'online'
  const observedAudioNodes = model.nodes.filter(
    (node) =>
      node.capabilities.some((capability) => capability.id === 'audio.output.local') ||
      node.capabilities.some((capability) => capability.id === 'audio.output.ltc'),
  )

  return (
    <div className="operator-page audio-node-detail-page" data-unsaved-form>
      <p className="settings-breadcrumb">
        <a href="/config">Settings</a> / <a href="/config/audio.node">Audio routing</a>
      </p>
      <h2 className="panel__title">{isNew ? 'New audio node' : existingId}</h2>
      <p className="text-muted">
        This node&rsquo;s program and LTC output routes, channel assignment, and declared clock
        domain. <code>programRoute</code>/<code>ltcRoute</code> are cross-checked, live,
        against this node&rsquo;s own most recent capability advertisement: a route it has not
        advertised is refused.
      </p>

      {isNew && (
        <>
          <label className="form-field">
            Select an observed audio node
            <select
              aria-label="Select an observed audio node"
              value={observedAudioNodes.some((node) => node.nodeId === newId) ? newId : ''}
              onChange={(e) => setNewId(e.target.value)}
            >
              <option value="">Choose a node or enter an id manually</option>
              {observedAudioNodes.map((node) => (
                <option key={node.nodeId} value={node.nodeId}>
                  {node.nodeId}{node.label === null ? '' : ` (${node.label})`}
                </option>
              ))}
            </select>
          </label>
          <label className="form-field">
            Node id (manual fallback)
            <input
              type="text"
              list="audio-node-known-ids"
              aria-label="Node id"
              value={newId}
              onChange={(e) => setNewId(e.target.value)}
            />
            <datalist id="audio-node-known-ids">
              {model.nodes.map((n) => (
                <option key={n.nodeId} value={n.nodeId} />
              ))}
            </datalist>
          </label>
        </>
      )}

      {targetNodeId !== '' && selectedNodeIsOffline && (
        <p className="panel panel--warning" role="status">
          This node is currently {targetNode.controlPlane.state}. Its last advertised capabilities
          remain visible, but a save is checked against the coordinator&rsquo;s latest evidence.
        </p>
      )}
      {targetNodeId !== '' && targetNode === undefined && (
        <p className="panel panel--warning" role="status">
          No live API evidence is available for &ldquo;{targetNodeId}&rdquo;. Route choices and
          channel inventories cannot be offered from browser state. The manual route fallback is
          retained below and an incorrect route is refused by the coordinator on save.
        </p>
      )}
      {targetNodeId !== '' && targetNode !== undefined && programRoutes.length === 0 && (
        <p className="panel panel--warning" role="status">
          This node has not advertised a program output route. The API cannot provide a route
          choice, so use the clearly marked manual fallback below only if the coordinator has
          separately confirmed the route.
        </p>
      )}

      <label className="form-field">
        Program route
        {programRoutes.length > 0 ? (
          <select
            aria-label="Program route"
            value={form.programRoute}
            onChange={(e) => {
              const route = e.target.value
              setForm({
                ...form,
                programRoute: route,
                ltcRoute: form.ltcRoute === '' ? '' : ltcRoutes.includes(route) ? route : '',
              })
            }}
          >
            <option value="" disabled>
              Choose an advertised route
            </option>
            {form.programRoute !== '' && !programRoutes.includes(form.programRoute) && (
              <option value={form.programRoute} disabled>
                {form.programRoute} (no longer advertised)
              </option>
            )}
            {programRoutes.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        ) : (
          <input
            type="text"
            aria-label="Program route"
            placeholder="API route evidence unavailable"
            value={form.programRoute}
            onChange={(e) => setForm({ ...form, programRoute: e.target.value })}
          />
        )}
      </label>
      {programRoutes.length === 0 && (
        <p className="audio-manual-fallback" role="note">
          Manual route fallback. The coordinator remains authoritative; this field is not an
          inventory of detected interfaces.
        </p>
      )}

      <label className="form-field">
        LTC output
        {ltcRoutes.length > 0 ? (
          <select
            aria-label="LTC route"
            value={form.ltcRoute}
            onChange={(e) =>
              setForm({
                ...form,
                ltcRoute: e.target.value,
                ltcChannel: e.target.value === '' ? '' : form.ltcChannel,
              })
            }
          >
            <option value="">Off</option>
            {ltcRoutes
              .filter((route) => form.programRoute === '' || route === form.programRoute)
              .map((r) => (
                <option key={r} value={r}>
                  {r}
                </option>
              ))}
            {form.ltcRoute !== '' && !ltcRoutes.includes(form.ltcRoute) && (
              <option value={form.ltcRoute} disabled>
                {form.ltcRoute} (no longer advertised)
              </option>
            )}
          </select>
        ) : (
          <input
            type="text"
            aria-label="LTC route"
            placeholder="API LTC route evidence unavailable"
            value={form.ltcRoute}
            onChange={(e) => setForm({ ...form, ltcRoute: e.target.value })}
          />
        )}
      </label>
      {ltcRoutes.length === 0 && (
        <p className="audio-manual-fallback" role="note">
          Manual LTC route fallback. No LTC-capable route is advertised by the API, so an LTC
          route cannot be safely suggested.
        </p>
      )}
      <p className="text-muted">
        Off disables LTC. When enabled, the coordinator requires program and LTC to use the same
        advertised interface and one clock domain.
      </p>

      {channelInventory.groups.length > 0 ? (
        <fieldset className="audio-output-groups">
          <legend>Program output groups</legend>
          <p className="text-muted">
            Choose groups from the node&rsquo;s advertised channel inventory.
          </p>
          {channelInventory.groups.map((group) => {
            const selected = group.channels.every((channel) =>
              form.programChannels.split(',').map((value) => Number(value.trim())).includes(channel),
            )
            return (
              <label key={group.id} className="audio-output-group">
                <input
                  type="checkbox"
                  checked={selected}
                  onChange={(e) => {
                    const current = new Set(
                      form.programChannels
                        .split(',')
                        .map((value) => Number(value.trim()))
                        .filter((value) => Number.isInteger(value) && value > 0),
                    )
                    group.channels.forEach((channel) =>
                      e.target.checked ? current.add(channel) : current.delete(channel),
                    )
                    setForm({ ...form, programChannels: [...current].sort((a, b) => a - b).join(', ') })
                  }}
                />
                {group.label} ({group.channels.join(', ')})
              </label>
            )
          })}
        </fieldset>
      ) : channelInventory.channels.length > 0 ? (
        <fieldset className="audio-output-groups">
          <legend>Program output channels</legend>
          <p className="text-muted">Choose channels from the node&rsquo;s advertised inventory.</p>
          {channelInventory.channels.map((channel) => (
            <label key={channel} className="audio-output-group">
              <input
                type="checkbox"
                checked={form.programChannels
                  .split(',')
                  .map((value) => Number(value.trim()))
                  .includes(channel)}
                onChange={(e) => {
                  const current = new Set(
                    form.programChannels
                      .split(',')
                      .map((value) => Number(value.trim()))
                      .filter((value) => Number.isInteger(value) && value > 0),
                  )
                  if (e.target.checked) current.add(channel)
                  else current.delete(channel)
                  setForm({ ...form, programChannels: [...current].sort((a, b) => a - b).join(', ') })
                }}
              />
              Channel {channel}
            </label>
          ))}
        </fieldset>
      ) : (
        <div className="audio-manual-fallback">
          <label className="form-field">
            Program output groups/channels (manual fallback; comma-separated, distinct, 1-based)
            <input
              type="text"
              aria-label="Program channels"
              placeholder="API channel inventory unavailable"
              value={form.programChannels}
              onChange={(e) => setForm({ ...form, programChannels: e.target.value })}
            />
          </label>
          <p role="note">
            The API advertises routes only, not channel inventory. No channel list is inferred.
          </p>
        </div>
      )}

      {ltcChannels.length > 0 ? (
        <label className="form-field">
          LTC output channel
          <select
            aria-label="LTC channel"
            value={form.ltcChannel}
            onChange={(e) =>
              setForm({
                ...form,
                ltcChannel: e.target.value,
                ltcRoute: e.target.value === '' ? '' : form.ltcRoute,
              })
            }
          >
            <option value="">Off</option>
            {ltcChannels.map((channel) => (
              <option key={channel} value={channel}>
                Channel {channel}
              </option>
            ))}
          </select>
        </label>
      ) : (
        <div className="audio-manual-fallback">
          <label className="form-field">
            LTC output channel (manual fallback; 1-based and excluded from program channels)
            <input
              type="number"
              min={1}
              aria-label="LTC channel"
              placeholder="API channel inventory unavailable"
              value={form.ltcChannel}
              onChange={(e) => setForm({ ...form, ltcChannel: e.target.value })}
            />
          </label>
          <p role="note">
            Off means clear this field and LTC output. No channel inventory is inferred.
          </p>
        </div>
      )}

      <section className="audio-clock-fallback" aria-labelledby="audio-clock-heading">
        <h3 id="audio-clock-heading">Clock verification</h3>
        <p role="status">
          API clock verification is unavailable. The coordinator does not advertise authoritative
          clock choices, so no dropdown is shown and no browser clock is used.
        </p>
        <label className="form-field">
          Manual clock domain declaration
          <input
            type="text"
            aria-label="Clock domain"
            value={form.clockDomain}
            onChange={(e) => setForm({ ...form, clockDomain: e.target.value })}
          />
        </label>
        <label className="form-field">
          Manual clock domain provenance
          <input
            type="text"
            aria-label="Clock domain provenance"
            value={form.clockDomainProvenance}
            onChange={(e) => setForm({ ...form, clockDomainProvenance: e.target.value })}
          />
        </label>
      </section>

      <div style={{ marginTop: '1rem' }}>
        {saveError !== null && (
          <p role="alert" className="session-form__error">
            {saveError}
          </p>
        )}
        <ScopedButton
          requiredScope={CONFIG_WRITE_SCOPE}
          onClick={() => void handleSave()}
          busy={saving}
          busyReason="Saving this configuration revision…"
        >
          {saving ? 'Saving…' : isNew ? 'Create audio node' : 'Save audio node'}
        </ScopedButton>
      </div>

      {!isNew && state.kind === 'loaded' && (
        <>
          <p className="panel" role="status">
            Active revision {state.config.revision}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
          </p>
          {state.revisions.length > 0 && (
            <details className="details-section">
              <summary className="details-section__summary">Revision history</summary>
              <div className="table-scroll">
                <table className="config-table" aria-label="Revision history">
                  <thead>
                    <tr>
                      <th scope="col">Revision</th>
                      <th scope="col">Active</th>
                      <th scope="col">Created at</th>
                      <th scope="col">Created by</th>
                    </tr>
                  </thead>
                  <tbody>
                    {state.revisions.map((rev) => (
                      <tr key={rev.revision}>
                        <th scope="row">{rev.revision}</th>
                        <td>{rev.active ? 'active' : ''}</td>
                        <td>{formatAbsolute(rev.createdAt)}</td>
                        <td>{rev.createdByPrincipalName ?? '-'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </details>
          )}
        </>
      )}
    </div>
  )
}
