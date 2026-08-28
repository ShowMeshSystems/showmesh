import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getAudioNode, getAudioNodeConfigRevisions, putAudioNode, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
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
 * The route names this node has actually advertised as BOTH
 * audio.output.local- and audio.output.ltc-capable -- the only names a
 * `PUT` naming them as programRoute/ltcRoute can succeed with, since the
 * coordinator refuses a route neither capability names AND refuses
 * programRoute/ltcRoute disagreeing with each other (api/openapi.yaml's
 * PUT /config/audio.node/{id} description). Driving the picker from this
 * intersection, rather than a free text field, is what keeps an operator
 * from ever typing a route this node cannot actually place audio on.
 */
function advertisedRoutes(node: Node | undefined): string[] {
  if (node === undefined) return []
  const local = node.capabilities.find((c) => c.id === 'audio.output.local')
  const ltc = node.capabilities.find((c) => c.id === 'audio.output.ltc')
  const localRoutes = attributeRoutes(local?.attributes as Record<string, unknown> | undefined)
  const ltcRoutes = attributeRoutes(ltc?.attributes as Record<string, unknown> | undefined)
  return localRoutes.filter((r) => ltcRoutes.includes(r))
}

/**
 * Mirrors, not enforces (ADR-030): every check here also exists
 * server-side. programRoute/ltcRoute's live cross-check against this
 * node's own capability advertisement is NOT mirrored here beyond
 * restricting the picker to [advertisedRoutes] when that evidence
 * exists -- a route this form could not restrict to (no evidence
 * available) is still sent as typed and the coordinator's own refusal
 * is what the operator sees.
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
    if (isNew) return
    if (existingId === undefined) return
    if (!scopeGate.allowed) return
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
  }, [existingId, scopeGate.allowed, isNew])

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
  const routes = advertisedRoutes(targetNode)

  return (
    <div>
      <h2 className="panel__title">{isNew ? 'New audio node' : existingId}</h2>
      <p className="text-muted">
        This node&rsquo;s program and LTC output routes, channel assignment, and declared clock
        domain. <code>programRoute</code>/<code>ltcRoute</code> are cross-checked, live,
        against this node&rsquo;s own most recent capability advertisement: a route it has not
        advertised is refused.
      </p>

      {isNew && (
        <label className="form-field">
          Node id
          <input
            type="text"
            list="audio-node-known-ids"
            value={newId}
            onChange={(e) => setNewId(e.target.value)}
          />
          <datalist id="audio-node-known-ids">
            {model.nodes.map((n) => (
              <option key={n.nodeId} value={n.nodeId} />
            ))}
          </datalist>
        </label>
      )}

      {targetNodeId !== '' && targetNode === undefined && (
        <p className="text-muted" role="status">
          This coordinator has no node evidence for &ldquo;{targetNodeId}&rdquo; yet, so no
          advertised route is known. Route/LTC route are plain text below; an incorrect route is
          refused by the coordinator on save.
        </p>
      )}
      {targetNodeId !== '' && targetNode !== undefined && routes.length === 0 && (
        <p className="text-muted" role="status">
          This node has not advertised any route usable for both program and LTC output. Route/LTC
          route are plain text below; an incorrect route is refused by the coordinator on save.
        </p>
      )}

      <label className="form-field">
        Program route
        {routes.length > 0 ? (
          <select
            aria-label="Program route"
            value={form.programRoute}
            onChange={(e) => setForm({ ...form, programRoute: e.target.value, ltcRoute: e.target.value })}
          >
            <option value="" disabled>
              Choose an advertised route
            </option>
            {routes.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        ) : (
          <input
            type="text"
            aria-label="Program route"
            value={form.programRoute}
            onChange={(e) => setForm({ ...form, programRoute: e.target.value })}
          />
        )}
      </label>

      <label className="form-field">
        LTC route
        {routes.length > 0 ? (
          <select
            aria-label="LTC route"
            value={form.ltcRoute}
            onChange={(e) => setForm({ ...form, ltcRoute: e.target.value, programRoute: e.target.value })}
          >
            <option value="" disabled>
              Choose an advertised route
            </option>
            {routes.map((r) => (
              <option key={r} value={r}>
                {r}
              </option>
            ))}
          </select>
        ) : (
          <input
            type="text"
            aria-label="LTC route"
            value={form.ltcRoute}
            onChange={(e) => setForm({ ...form, ltcRoute: e.target.value })}
          />
        )}
      </label>
      <p className="text-muted">
        Program route and LTC route must name the same route: program and LTC leave through one
        interface in one clock domain.
      </p>

      <label className="form-field">
        Program channels (comma-separated, distinct, 1-based, e.g. &ldquo;1, 2&rdquo;)
        <input
          type="text"
          aria-label="Program channels"
          value={form.programChannels}
          onChange={(e) => setForm({ ...form, programChannels: e.target.value })}
        />
      </label>

      <label className="form-field">
        LTC channel (1-based, must not also be a program channel)
        <input
          type="number"
          min={1}
          aria-label="LTC channel"
          value={form.ltcChannel}
          onChange={(e) => setForm({ ...form, ltcChannel: e.target.value })}
        />
      </label>

      <label className="form-field">
        Clock domain
        <input
          type="text"
          aria-label="Clock domain"
          value={form.clockDomain}
          onChange={(e) => setForm({ ...form, clockDomain: e.target.value })}
        />
      </label>
      <label className="form-field">
        Clock domain provenance
        <input
          type="text"
          aria-label="Clock domain provenance"
          value={form.clockDomainProvenance}
          onChange={(e) => setForm({ ...form, clockDomainProvenance: e.target.value })}
        />
      </label>
      <p className="text-muted">
        Operator-declared, never inferred: no software call on this platform proves two outputs
        share a hardware clock.
      </p>

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
