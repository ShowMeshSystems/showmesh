import { useEffect, useRef, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowAction, getShowActionRevisions, putShowAction, type ConfigRevisionMeta } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import type { ActionIntegration, ConfigShowAction, SafetyClass, ShowActionConfigResponse } from '../app/types'

// Authoring surface #1 of 2 for this wave (STEP-9-SPEC.md section 5.3):
// "the owner declined the cut [of the show.action authoring surface] and
// it ships." Every rule this form enforces client-side is a MIRROR of a
// server-side rule the coordinator enforces independently (ADR-030: "the
// UI holds no authoring logic; validation is server-side and the browser
// may only mirror it") — this component never substitutes its own
// judgement for a `PUT` rejection; it renders whatever the server says
// via describeApiError, exactly like Configuration.tsx.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

const SAFETY_CLASSES: SafetyClass[] = ['none', 'blackout', 'stop', 'powerOff']
// The eight primitives Step 8 registered (docs/bench/fpp-command-vocabulary.md
// section 4's registry, as also enumerated in FPPCommandOutcome's own
// sibling controls) — this form does not invent a ninth, and the
// coordinator resolves whatever is picked through its own registry
// (STEP-9-SPEC.md section 5.3: "primitive must be one of the eight wire
// actions Step 8 registered, resolved through the existing registry
// rather than a second copy of the list"), so a stale copy here fails
// loudly at PUT time rather than silently drifting.
const FPP_PRIMITIVES = [
  'startPlaylist',
  'stopPlaylist',
  'stopPlaylistGracefully',
  'pausePlaylist',
  'resumePlaylist',
  'nextPlaylistItem',
  'prevPlaylistItem',
  'setVolume',
]
const MQTT_EXPECT_KINDS = ['none', 'boolean', 'number', 'text', 'match'] as const

interface FormState {
  show: string
  label: string
  description: string
  safetyClass: SafetyClass | ''
  integration: ActionIntegration
  // fpp fields
  instanceId: string
  primitive: string
  paramsJson: string
  // mqtt fields
  broker: string
  publishTopic: string
  publishPayload: string
  publishQos: '' | '0' | '1' | '2'
  publishRetain: boolean
  expectKind: (typeof MQTT_EXPECT_KINDS)[number]
  expectTopic: string
  expectValue: string
  expectDeadlineSeconds: string
}

function emptyForm(): FormState {
  return {
    show: '',
    label: '',
    description: '',
    safetyClass: '',
    integration: 'fpp',
    instanceId: '',
    primitive: '',
    paramsJson: '',
    broker: '',
    publishTopic: '',
    publishPayload: '',
    publishQos: '',
    publishRetain: false,
    expectKind: 'none',
    expectTopic: '',
    expectValue: '',
    expectDeadlineSeconds: '',
  }
}

function formFromPayload(payload: ConfigShowAction): FormState {
  const target = payload.target
  return {
    show: payload.show,
    label: payload.label,
    description: payload.description,
    safetyClass: payload.safetyClass,
    integration: target.integration,
    instanceId: target.instanceId ?? '',
    primitive: target.primitive ?? '',
    paramsJson: target.params === undefined ? '' : JSON.stringify(target.params, null, 2),
    broker: target.broker ?? '',
    publishTopic: target.publish?.topic ?? '',
    publishPayload: target.publish?.payload ?? '',
    publishQos: target.publish === undefined ? '' : (String(target.publish.qos) as '0' | '1' | '2'),
    publishRetain: target.publish?.retain ?? false,
    expectKind: target.expect?.kind ?? 'none',
    expectTopic: target.expect?.topic ?? '',
    expectValue: target.expect?.value ?? '',
    expectDeadlineSeconds: target.expect?.deadlineSeconds === undefined ? '' : String(target.expect.deadlineSeconds),
  }
}

/**
 * Builds the wire payload from form state, OR returns a client-side
 * validation message. This is a MIRROR, not the authority (see this
 * file's own header comment) — every check here also exists server-side;
 * this only saves a round trip for the common typo/omission case.
 */
function buildPayload(form: FormState): { payload: ConfigShowAction } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.label.trim() === '') return { error: 'Label is required.' }
  if (form.safetyClass === '') {
    return {
      error:
        'Safety class is required and is never defaulted — pick the one that matches what this action actually does.',
    }
  }

  if (form.integration === 'fpp') {
    if (form.instanceId.trim() === '') return { error: 'FPP instance id is required.' }
    if (form.primitive === '') return { error: 'Primitive is required.' }
    let params: Record<string, unknown> | undefined
    if (form.paramsJson.trim() !== '') {
      try {
        const parsed: unknown = JSON.parse(form.paramsJson)
        if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) {
          return { error: 'Params must be a JSON object, e.g. {"playlist": "Halloween Main"}.' }
        }
        params = parsed as Record<string, unknown>
      } catch {
        return { error: 'Params is not valid JSON.' }
      }
    }
    return {
      payload: {
        show: form.show.trim(),
        label: form.label.trim(),
        description: form.description,
        safetyClass: form.safetyClass,
        target: {
          integration: 'fpp',
          instanceId: form.instanceId.trim(),
          primitive: form.primitive,
          // `exactOptionalPropertyTypes`: an optional key must be OMITTED,
          // not set to `undefined`, to mean "absent" — this mirrors the
          // wire's own absent/null/present distinction (STEP-9-SPEC.md
          // section 5.3) at the TypeScript layer, so the spread is
          // conditional rather than always including a `params: undefined` key.
          ...(params !== undefined ? { params } : {}),
        },
      },
    }
  }

  // mqtt
  if (form.broker.trim() === '') {
    return { error: 'Broker is required for an MQTT action and has no default — say which one this publishes on.' }
  }
  if (form.publishTopic.trim() === '') return { error: 'Publish topic is required.' }
  if (form.publishQos === '') return { error: 'QoS is required and has no default — pick 0, 1, or 2.' }
  if (form.expectKind !== 'none') {
    if (form.expectTopic.trim() === '') return { error: 'Expected response topic is required unless the response kind is "none".' }
    if (form.expectDeadlineSeconds.trim() === '') {
      return { error: 'A deadline (in seconds, up to 120) is required unless the response kind is "none".' }
    }
    const deadline = Number(form.expectDeadlineSeconds)
    if (!Number.isInteger(deadline) || deadline <= 0 || deadline > 120) {
      return { error: 'Deadline must be a whole number of seconds from 1 to 120.' }
    }
    if (form.expectKind === 'match' && form.expectValue.trim() === '') {
      return { error: 'A "match" response requires the exact value to match.' }
    }
  }
  return {
    payload: {
      show: form.show.trim(),
      label: form.label.trim(),
      description: form.description,
      safetyClass: form.safetyClass,
      target: {
        integration: 'mqtt',
        broker: form.broker.trim(),
        publish: {
          topic: form.publishTopic.trim(),
          payload: form.publishPayload,
          qos: Number(form.publishQos) as 0 | 1 | 2,
          retain: form.publishRetain,
        },
        expect:
          form.expectKind === 'none'
            ? { kind: 'none' }
            : {
                kind: form.expectKind,
                topic: form.expectTopic.trim(),
                // Same `exactOptionalPropertyTypes` rule as `params`
                // above: omit `value` entirely rather than set it to
                // `undefined` when this response kind carries none.
                ...((form.expectKind === 'match' || form.expectKind === 'number') && form.expectValue !== ''
                  ? { value: form.expectValue }
                  : {}),
                deadlineSeconds: Number(form.expectDeadlineSeconds),
              },
      },
    },
  }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowActionConfigResponse; revisions: ConfigRevisionMeta[] }

export interface ShowActionDetailProps {
  isNew?: boolean
}

export function ShowActionDetail({ isNew = false }: ShowActionDetailProps) {
  const params = useParams<{ id: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<FormState>(emptyForm())
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false) // see Configuration.tsx's own savingRef for why this guards instead of `saving` state

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getShowAction(existingId), getShowActionRevisions(existingId)])
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
  }, [existingId, readGate.allowed, isNew])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('An action id is required.')
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
      const resp = await putShowAction(id, built.payload)
      if (isNew) {
        navigate(`/actions/${encodeURIComponent(id)}`)
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      const revisionsResp = await getShowActionRevisions(id)
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, revisions: revisionsResp.revisions } : prev))
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  const pageGate = isNew ? writeGate : readGate
  if (!pageGate.allowed) {
    return (
      <div>
        <h2 className="panel__title">{isNew ? 'New show action' : 'Show action'}</h2>
        <p className="panel panel--error" role="status">
          {pageGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading action…</p>
  }
  if (!isNew && state.kind === 'error') {
    return (
      <p className="panel panel--error" role="alert">
        {state.message}
      </p>
    )
  }

  const editable = writeGate.allowed

  return (
    <div>
      <h2 className="panel__title">{isNew ? 'New show action' : form.label || existingId}</h2>

      {!editable && (
        <p className="text-muted" role="status">
          Viewing only — editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="form-field">
          Action id
          <input type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable} className="show-action-form">
        <label className="form-field">
          Show
          <input type="text" value={form.show} onChange={(e) => setForm({ ...form, show: e.target.value })} />
        </label>
        <label className="form-field">
          Label
          <input type="text" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} />
        </label>
        <label className="form-field">
          Description
          <input
            type="text"
            value={form.description}
            onChange={(e) => setForm({ ...form, description: e.target.value })}
          />
        </label>
        <label className="form-field">
          Safety class
          <select
            value={form.safetyClass}
            onChange={(e) => setForm({ ...form, safetyClass: e.target.value as SafetyClass })}
          >
            <option value="" disabled>
              Choose one — never defaulted
            </option>
            {SAFETY_CLASSES.map((cls) => (
              <option key={cls} value={cls}>
                {cls}
              </option>
            ))}
          </select>
        </label>

        <label className="form-field">
          Integration
          <select
            value={form.integration}
            onChange={(e) => setForm({ ...form, integration: e.target.value as ActionIntegration })}
          >
            <option value="fpp">FPP primitive</option>
            <option value="mqtt">External MQTT command</option>
          </select>
        </label>

        {form.integration === 'fpp' ? (
          <>
            <label className="form-field">
              FPP instance id
              <input
                type="text"
                value={form.instanceId}
                onChange={(e) => setForm({ ...form, instanceId: e.target.value })}
              />
            </label>
            <label className="form-field">
              Primitive
              <select value={form.primitive} onChange={(e) => setForm({ ...form, primitive: e.target.value })}>
                <option value="" disabled>
                  Choose one
                </option>
                {FPP_PRIMITIVES.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </label>
            <label className="form-field">
              Params (JSON object, optional — shape depends on the primitive chosen above)
              <textarea
                rows={4}
                value={form.paramsJson}
                onChange={(e) => setForm({ ...form, paramsJson: e.target.value })}
              />
            </label>
          </>
        ) : (
          <>
            <label className="form-field">
              Broker (required — this deployment&rsquo;s declared broker identifier, never defaulted)
              <input type="text" value={form.broker} onChange={(e) => setForm({ ...form, broker: e.target.value })} />
            </label>
            <label className="form-field">
              Publish topic
              <input
                type="text"
                value={form.publishTopic}
                onChange={(e) => setForm({ ...form, publishTopic: e.target.value })}
              />
            </label>
            <label className="form-field">
              Publish payload
              <input
                type="text"
                value={form.publishPayload}
                onChange={(e) => setForm({ ...form, publishPayload: e.target.value })}
              />
            </label>
            <label className="form-field">
              QoS (required — no default)
              <select
                value={form.publishQos}
                onChange={(e) => setForm({ ...form, publishQos: e.target.value as FormState['publishQos'] })}
              >
                <option value="" disabled>
                  Choose one
                </option>
                <option value="0">0</option>
                <option value="1">1</option>
                <option value="2">2</option>
              </select>
            </label>
            <label className="form-field form-field--checkbox">
              <input
                type="checkbox"
                checked={form.publishRetain}
                onChange={(e) => setForm({ ...form, publishRetain: e.target.checked })}
              />
              Retain (defaults to off when left unchecked)
            </label>

            <label className="form-field">
              Expected response
              <select
                value={form.expectKind}
                onChange={(e) => setForm({ ...form, expectKind: e.target.value as FormState['expectKind'] })}
              >
                {MQTT_EXPECT_KINDS.map((k) => (
                  <option key={k} value={k}>
                    {k}
                  </option>
                ))}
              </select>
            </label>
            {form.expectKind === 'none' ? (
              <p className="text-muted">
                No response is expected. This step will report as unconfirmable, on every run, by
                design — that is honest, not a defect.
              </p>
            ) : (
              <>
                <label className="form-field">
                  Response topic
                  <input
                    type="text"
                    value={form.expectTopic}
                    onChange={(e) => setForm({ ...form, expectTopic: e.target.value })}
                  />
                </label>
                {(form.expectKind === 'match' || form.expectKind === 'number') && (
                  <label className="form-field">
                    {form.expectKind === 'match' ? 'Exact value to match' : 'Expected value (optional)'}
                    <input
                      type="text"
                      value={form.expectValue}
                      onChange={(e) => setForm({ ...form, expectValue: e.target.value })}
                    />
                  </label>
                )}
                <label className="form-field">
                  Deadline, in seconds (1&ndash;120)
                  <input
                    type="number"
                    min={1}
                    max={120}
                    value={form.expectDeadlineSeconds}
                    onChange={(e) => setForm({ ...form, expectDeadlineSeconds: e.target.value })}
                  />
                </label>
              </>
            )}
          </>
        )}
      </fieldset>

      {editable && (
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
            busyReason="Saving this action revision…"
          >
            {saving ? 'Saving…' : isNew ? 'Create action' : 'Save action'}
          </ScopedButton>
        </div>
      )}

      {!isNew && state.kind === 'loaded' && (
        <>
          <p className="panel" role="status">
            Active revision {state.config.revision}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
          </p>
          {state.revisions.length > 0 && (
            <>
              <h3 className="panel__title">Revision history</h3>
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Revision</th>
                    <th>Active</th>
                    <th>Created at</th>
                    <th>Created by</th>
                  </tr>
                </thead>
                <tbody>
                  {state.revisions.map((rev) => (
                    <tr key={rev.revision}>
                      <td>{rev.revision}</td>
                      <td>{rev.active ? 'active' : ''}</td>
                      <td>{formatAbsolute(rev.createdAt)}</td>
                      <td>{rev.createdByPrincipalName ?? '—'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </>
          )}
        </>
      )}
    </div>
  )
}

