import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import {
  getShowAction,
  getShowMacro,
  getShowMacroRevisions,
  listConfigObjects,
  listMacroRuns,
  putShowMacro,
  type ConfigRevisionMeta,
} from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'
import { ShowSelect } from '../components/ShowSelect'
import { RunMacroButton } from '../components/RunMacroButton'
import { MacroRunOutcome } from '../components/MacroRunOutcome'
import type {
  ActionIntegration,
  ConfigObjectSummary,
  ConfigShowMacro,
  ConfigShowMacroStep,
  LocalFallbackClass,
  MacroRunSummary,
  MacroStepOnFailure,
  MacroStepOnUnconfirmed,
  ShowMacroConfigResponse,
} from '../app/types'

// Authoring surface #2 of 2 for this wave (STEP-9-SPEC.md section 5.4).
// Same "server validates, this only mirrors" posture as
// ShowActionDetail.tsx — see that file's own header comment.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'
const MAX_STEPS = 32

// STEP-9-SPEC.md section 5.4: the closed enum has three members, and
// "reduced" is deliberately NOT one of them — "reduced must be rejected
// at the point of authoring with a stated reason: no delivery path
// exists for it... Do not offer it as a value they can pick and then
// have the server reject." So this list is the FULL set of options ever
// rendered in the select below; "reduced" never appears as a choice.
const LOCAL_FALLBACK_CLASSES: LocalFallbackClass[] = ['none', 'coordinator-required', 'silence']

interface StepForm {
  id: string
  action: string
  onFailure: MacroStepOnFailure
  onUnconfirmed: MacroStepOnUnconfirmed
  localFallbackClass: LocalFallbackClass
  localFallbackReason: string
}

function stepToForm(step: ConfigShowMacroStep): StepForm {
  return {
    id: step.id,
    action: step.action,
    onFailure: step.onFailure,
    onUnconfirmed: step.onUnconfirmed,
    localFallbackClass: step.localFallback.class,
    localFallbackReason: step.localFallback.reason,
  }
}

function newStepForm(): StepForm {
  return {
    id: '',
    action: '',
    // 'continue' since 2026-08-14 (owner decision: a macro run always runs
    // every step). This mirrors the server's own resolved default, which is
    // the only correct value here: a create form that pre-selects something
    // else silently authors macros that behave differently from ones
    // written through showmeshctl or curl.
    onFailure: 'continue',
    onUnconfirmed: 'continue',
    localFallbackClass: 'coordinator-required',
    localFallbackReason: '',
  }
}

interface MacroFormState {
  show: string
  label: string
  description: string
  steps: StepForm[]
}

function emptyMacroForm(): MacroFormState {
  return { show: '', label: '', description: '', steps: [newStepForm()] }
}

function formFromPayload(payload: ConfigShowMacro): MacroFormState {
  return {
    show: payload.show,
    label: payload.label,
    description: payload.description,
    steps: payload.steps.map(stepToForm),
  }
}

function buildPayload(form: MacroFormState): { payload: ConfigShowMacro } | { error: string } {
  if (form.show.trim() === '') return { error: 'Show is required.' }
  if (form.label.trim() === '') return { error: 'Label is required.' }
  if (form.steps.length === 0) return { error: 'A macro needs at least one step.' }
  if (form.steps.length > MAX_STEPS) return { error: `A macro may have at most ${MAX_STEPS} steps.` }

  const seenIds = new Set<string>()
  const steps: ConfigShowMacroStep[] = []
  for (const [index, step] of form.steps.entries()) {
    const id = step.id.trim()
    if (id === '') return { error: `Step ${index + 1} needs an id.` }
    if (seenIds.has(id)) return { error: `Step id "${id}" is used more than once. Every step id must be unique.` }
    seenIds.add(id)
    if (step.action.trim() === '') return { error: `Step "${id}" needs an action.` }
    if (step.localFallbackReason.trim() === '') {
      return { error: `Step "${id}" needs a reason for its local fallback, even when the class is "none".` }
    }
    steps.push({
      id,
      action: step.action.trim(),
      onFailure: step.onFailure,
      onUnconfirmed: step.onUnconfirmed,
      localFallback: { class: step.localFallbackClass, reason: step.localFallbackReason.trim() },
    })
  }

  return {
    payload: { show: form.show.trim(), label: form.label.trim(), description: form.description, steps },
  }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowMacroConfigResponse; revisions: ConfigRevisionMeta[] }

export interface MacroDetailProps {
  isNew?: boolean
}

export function MacroDetail({ isNew = false }: MacroDetailProps) {
  const params = useParams<{ id: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const existingId = isNew ? undefined : params.id

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<MacroFormState>(emptyMacroForm())
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)

  const [actions, setActions] = useState<ConfigObjectSummary[] | null>(null)
  const [runs, setRuns] = useState<MacroRunSummary[] | null>(null)
  const [runsError, setRunsError] = useState<string | null>(null)
  // Track D seam D-4 (build contract §2.4): "the localFallback.class
  // constraint surfaced honestly (every Resolume action is
  // coordinator-required, and the server refuses anything else)." A show
  // action's OWN integration is not on ConfigObjectSummary (id/label/
  // revision only) — this fetches each referenced action's full payload,
  // once per distinct id, and caches the answer here.
  // `null` means the lookup was tried and failed (unknown id, 403, transport
  // error, or an in-progress free-text edit) — it is still recorded so the
  // id is never retried, which is what bounds the effect below.
  const [actionIntegrations, setActionIntegrations] = useState<Record<string, ActionIntegration | null>>({})

  // Filtered to this macro's own show: a macro step may only reference an
  // action in the same show namespace, and the server refuses a
  // cross-show reference at write time. Filtering here rather than
  // rendering every action from every show and letting the write refuse
  // it — the alternative this seam considered — because an operator
  // authoring a "halloween-2026" macro who picks a "christmas-2026"
  // action from the dropdown gets a 400 the dropdown itself steered them
  // into; an action outside the current show is simply not offered.
  useEffect(() => {
    if (!readGate.allowed) return
    const show = form.show.trim()
    let cancelled = false
    listConfigObjects('show.action', show === '' ? undefined : show)
      .then((resp) => {
        if (!cancelled) setActions(resp.objects)
      })
      .catch(() => {
        // Non-fatal: the action picker degrades to a free-text field
        // (see the render below) rather than blocking the whole page —
        // this list is a convenience, not the source of truth the server
        // still validates `step.action` against at write time.
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, form.show])

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getShowMacro(existingId), getShowMacroRevisions(existingId)])
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

  useEffect(() => {
    if (isNew) return
    if (existingId === undefined) return
    if (!readGate.allowed) return
    let cancelled = false
    listMacroRuns({ macroId: existingId, limit: 20 })
      .then((resp) => {
        if (!cancelled) setRuns(resp.runs)
      })
      .catch((err: unknown) => {
        if (!cancelled) setRunsError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [existingId, readGate.allowed, isNew])

  useEffect(() => {
    if (!readGate.allowed) return
    const unknownIds = [
      ...new Set(
        form.steps
          .map((s) => s.action.trim())
          .filter((id) => id !== '' && !(id in actionIntegrations)),
      ),
    ]
    if (unknownIds.length === 0) return
    let cancelled = false
    void Promise.all(
      unknownIds.map((id) =>
        getShowAction(id)
          .then((resp) => [id, resp.payload.target.integration] as const)
          .catch(() => [id, null] as const),
      ),
    ).then((results) => {
      if (cancelled) return
      setActionIntegrations((prev) => {
        let changed = false
        const next = { ...prev }
        for (const [id, integration] of results) {
          if (!(id in next)) {
            next[id] = integration
            changed = true
          }
        }
        return changed ? next : prev
      })
    })
    return () => {
      cancelled = true
    }
  }, [form.steps, readGate.allowed, actionIntegrations])

  function updateStep(index: number, patch: Partial<StepForm>): void {
    setForm((f) => ({ ...f, steps: f.steps.map((s, i) => (i === index ? { ...s, ...patch } : s)) }))
  }

  function addStep(): void {
    setForm((f) => (f.steps.length >= MAX_STEPS ? f : { ...f, steps: [...f.steps, newStepForm()] }))
  }

  function removeStep(index: number): void {
    setForm((f) => ({ ...f, steps: f.steps.filter((_, i) => i !== index) }))
  }

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : existingId
    if (id === undefined || id === '') {
      setSaveError('A macro id is required.')
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
      const resp = await putShowMacro(id, built.payload)
      if (isNew) {
        navigate(`/macros/${encodeURIComponent(id)}`)
        return
      }
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, config: resp } : prev))
      const revisionsResp = await getShowMacroRevisions(id)
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
        <h2 className="panel__title">{isNew ? 'New macro' : 'Macro'}</h2>
        <p className="panel panel--error" role="status">
          {pageGate.reason}
        </p>
      </div>
    )
  }

  if (!isNew && state.kind === 'loading') {
    return <p className="text-muted">Loading macro…</p>
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
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', flexWrap: 'wrap', gap: '0.75rem' }}>
        <h2 className="panel__title">{isNew ? 'New macro' : form.label || existingId}</h2>
        {!isNew && existingId !== undefined && <RunMacroButton macroId={existingId} label="Run this macro" />}
      </div>

      {!editable && (
        <p className="text-muted" role="status">
          Viewing only: editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="form-field">
          Macro id
          <input type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable}>
        <label className="form-field">
          Show
          <ShowSelect value={form.show} onChange={(show) => setForm({ ...form, show })} />
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
      </fieldset>

      <h3 className="panel__title">Steps</h3>
      <p className="text-muted">
        Each step invokes a show action, in order. A failed step stops the remaining steps unless
        it says otherwise; a step whose effect could not be confirmed does not, by default: see
        each step&rsquo;s own reason once this macro has run.
      </p>

      <ol className="list-plain">
        {form.steps.map((step, index) => (
          <li key={index} className="show-macro-form__step">
            <fieldset disabled={!editable}>
              <legend>Step {index + 1}</legend>
              <label className="form-field">
                Step id
                <input type="text" value={step.id} onChange={(e) => updateStep(index, { id: e.target.value })} />
              </label>
              <label className="form-field">
                Action
                {actions !== null && actions.length > 0 ? (
                  <select value={step.action} onChange={(e) => updateStep(index, { action: e.target.value })}>
                    <option value="" disabled>
                      Choose an action
                    </option>
                    {actions.map((a) => (
                      <option key={a.id} value={a.id}>
                        {a.label} ({a.id})
                      </option>
                    ))}
                  </select>
                ) : (
                  <input
                    type="text"
                    placeholder="show action id"
                    value={step.action}
                    onChange={(e) => updateStep(index, { action: e.target.value })}
                  />
                )}
              </label>
              <label className="form-field">
                If this step fails
                <select
                  value={step.onFailure}
                  onChange={(e) => updateStep(index, { onFailure: e.target.value as MacroStepOnFailure })}
                >
                  <option value="abort">Abort the remaining steps (default)</option>
                  <option value="continue">Continue with the remaining steps</option>
                </select>
              </label>
              <label className="form-field">
                If this step cannot be confirmed
                <select
                  value={step.onUnconfirmed}
                  onChange={(e) => updateStep(index, { onUnconfirmed: e.target.value as MacroStepOnUnconfirmed })}
                >
                  <option value="continue">Continue with the remaining steps (default)</option>
                  <option value="abort">Abort the remaining steps</option>
                </select>
              </label>
              <label className="form-field">
                If the coordinator refuses or is unreachable
                <select
                  value={step.localFallbackClass}
                  onChange={(e) =>
                    updateStep(index, { localFallbackClass: e.target.value as LocalFallbackClass })
                  }
                >
                  {LOCAL_FALLBACK_CLASSES.map((cls) => (
                    <option
                      key={cls}
                      value={cls}
                      disabled={actionIntegrations[step.action.trim()] === 'resolume' && cls !== 'coordinator-required'}
                    >
                      {cls}
                    </option>
                  ))}
                </select>
              </label>
              {/* Track D seam D-4 (build contract §2.4): mirroring, not
                  enforcing (ADR-030) — every option besides
                  "coordinator-required" is disabled above once this step's
                  own action is known to be a Resolume integration, but the
                  coordinator still refuses a saved mismatch on its own;
                  this note states the reason where an operator authoring
                  this exact field would look. */}
              {actionIntegrations[step.action.trim()] === 'resolume' && (
                <p className="text-muted" role="status">
                  This step invokes a Resolume action. Every Resolume action is
                  coordinator-required: Resolume holds no local fallback for a
                  coordinator-hosted adapter, so the coordinator refuses anything
                  other than &ldquo;coordinator-required&rdquo; here.
                </p>
              )}
              {/* STEP-9-SPEC.md section 5.4: stated where an operator
                  authoring this exact field would look, not buried in a
                  document this component never links to. */}
              <p className="text-muted">
                A &ldquo;reduced&rdquo; local fallback (a node running a scaled-down version of the
                show on its own) is not offered here: no delivery path exists for it in this
                deployment, so offering it would let this be configured and then silently never
                take effect.
              </p>
              <label className="form-field">
                Reason (required, in your own words; this is what an operator reads while the
                coordinator is unreachable)
                <input
                  type="text"
                  value={step.localFallbackReason}
                  onChange={(e) => updateStep(index, { localFallbackReason: e.target.value })}
                />
              </label>
              {editable && form.steps.length > 1 && (
                <button type="button" onClick={() => removeStep(index)}>
                  Remove step {index + 1}
                </button>
              )}
            </fieldset>
          </li>
        ))}
      </ol>
      {editable && (
        <button type="button" onClick={addStep} disabled={form.steps.length >= MAX_STEPS}>
          Add step
        </button>
      )}

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
            busyReason="Saving this macro revision…"
          >
            {saving ? 'Saving…' : isNew ? 'Create macro' : 'Save macro'}
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

          <h3 className="panel__title">Recent runs</h3>
          {runsError !== null && (
            <p className="panel panel--error" role="alert">
              {runsError}
            </p>
          )}
          {runs === null && runsError === null && <p className="text-muted">Loading runs…</p>}
          {runs !== null && runs.length === 0 && <p className="text-muted">This macro has not been run yet.</p>}
          {runs !== null && runs.length > 0 && (
            <ul className="list-plain">
              {runs.map((run) => (
                <li key={run.id}>
                  <Link className="entity-link" to={`/macros/${encodeURIComponent(run.macroObjectId)}/runs/${encodeURIComponent(run.id)}`}>
                    <div className="text-muted">
                      {formatAbsolute(run.createdAt)}: {run.trigger}, {run.issuerPrincipalName}
                    </div>
                    <MacroRunOutcome run={run} />
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </div>
  )
}
