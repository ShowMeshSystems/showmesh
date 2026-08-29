import { useEffect, useRef, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import {
  getShowMacro,
  getShowMacroRevisions,
  listActionBindings,
  listConfigObjects,
  listMacroRuns,
  putShowMacro,
  type ConfigRevisionMeta,
} from '../../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../../app/session'
import { useModelContext } from '../../app/ModelContext'
import { formatAbsolute } from '../../app/time'
import { ScopedButton } from '../../components/ScopedButton'
import { ShowSelect } from '../../components/ShowSelect'
import { RunMacroButton } from '../../components/RunMacroButton'
import { MacroRunOutcome } from '../../components/MacroRunOutcome'
import type {
  ActionBinding,
  ConfigObjectSummary,
  ConfigShowMacro,
  ConfigShowMacroStep,
  LocalFallbackClass,
  MacroRunSummary,
  MacroStepOnFailure,
  MacroStepOnUnconfirmed,
  ShowMacroConfigResponse,
} from '../../app/types'
import { showAutomationPath } from '../../components/showWorkspacePaths'
import { deriveMacroConsequence, deriveMacroReadiness, describeMacroReadiness } from './automationDerive'
import { useActionFacts } from './useActionFacts'

const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'
const MAX_STEPS = 32

// STEP-9-SPEC.md section 5.4 / DESIGN-DECISIONS-AND-API-FACTS.md §6: the
// closed enum has three members, and "reduced" is deliberately NOT one of
// them — no delivery path exists for it in this deployment, so it must
// never appear as a pickable option here.
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
    // every step) — the server's own resolved default.
    onFailure: 'continue',
    onUnconfirmed: 'continue',
    localFallbackClass: 'coordinator-required',
    localFallbackReason: '',
  }
}

interface MacroFormState {
  label: string
  description: string
  steps: StepForm[]
}

function emptyMacroForm(): MacroFormState {
  return { label: '', description: '', steps: [newStepForm()] }
}

function formFromPayload(payload: ConfigShowMacro): MacroFormState {
  return { label: payload.label, description: payload.description, steps: payload.steps.map(stepToForm) }
}

function buildPayload(show: string, form: MacroFormState): { payload: ConfigShowMacro } | { error: string } {
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

  return { payload: { show, label: form.label.trim(), description: form.description, steps } }
}

type LoadState =
  | { kind: 'new' }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowMacroConfigResponse; revisions: ConfigRevisionMeta[] }

export interface MacroStepEditorProps {
  showId: string
  macroId?: string
  isNew?: boolean
}

/**
 * The inspector's macro step editor variant (Show Automation.dc.html,
 * `pane="step"`), reached at `/shows/:showId/automation/macros/:macroId`
 * (or `/macros/new`). The readiness and consequence roll-ups at the top are
 * the point of this screen (UI-DESIGN-GUIDE.md section 7 "derive, don't
 * ask", design decisions 8 and 10): both are computed from evidence already
 * on screen — the binding sweep and each referenced action's own safety
 * class — never asked of the operator.
 */
export function MacroStepEditor({ showId, macroId, isNew = false }: MacroStepEditorProps) {
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>(isNew ? { kind: 'new' } : { kind: 'loading' })
  const [newId, setNewId] = useState('')
  const [form, setForm] = useState<MacroFormState>(emptyMacroForm())
  // The macro's own show, editable only when editing an EXISTING macro
  // (`show.macro`'s `show` field is not immutable server-side, unlike
  // `show.cue`/`show.playlist` — see PUT /config/show.macro's own
  // description). Starts at the route's showId, the same value a new
  // macro is always created under.
  const [moveToShow, setMoveToShow] = useState(showId)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const savingRef = useRef(false)

  const [actions, setActions] = useState<ConfigObjectSummary[] | null>(null)
  const [runs, setRuns] = useState<MacroRunSummary[] | null>(null)
  const [runsError, setRunsError] = useState<string | null>(null)
  const [bindings, setBindings] = useState<Map<string, ActionBinding>>(new Map())

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    listConfigObjects('show.action', showId)
      .then((resp) => {
        if (!cancelled) setActions(resp.objects)
      })
      .catch(() => {
        // Non-fatal: the action picker degrades to a free-text field.
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, showId])

  useEffect(() => {
    // The binding sweep requires no credential (ADR-024 constraint 23), so
    // it is never gated on readGate, and a failed fetch leaves the map
    // empty rather than blanking the editor.
    let cancelled = false
    listActionBindings(showId)
      .then((list) => {
        if (!cancelled) setBindings(new Map(list.map((b) => [b.actionId, b])))
      })
      .catch(() => undefined)
    return () => {
      cancelled = true
    }
  }, [showId])

  useEffect(() => {
    if (isNew) setMoveToShow(showId)
  }, [isNew, showId])

  useEffect(() => {
    if (isNew || macroId === undefined || !readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([getShowMacro(macroId), getShowMacroRevisions(macroId)])
      .then(([config, revisionsResp]) => {
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setForm(formFromPayload(config.payload))
        setMoveToShow(config.payload.show)
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [macroId, readGate.allowed, isNew])

  useEffect(() => {
    if (isNew || macroId === undefined || !readGate.allowed) return
    let cancelled = false
    listMacroRuns({ macroId, limit: 20 })
      .then((resp) => {
        if (!cancelled) setRuns(resp.runs)
      })
      .catch((err: unknown) => {
        if (!cancelled) setRunsError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [macroId, readGate.allowed, isNew])

  const actionIds = form.steps.map((s) => s.action.trim()).filter((id) => id !== '')
  const facts = useActionFacts(actionIds, readGate.allowed)

  const readiness = deriveMacroReadiness(form.steps, bindings)
  const readinessBadge = describeMacroReadiness(readiness)
  const consequence = deriveMacroConsequence(actionIds.map((id) => facts[id]?.safetyClass).filter((c): c is NonNullable<typeof c> => c !== undefined && c !== null))

  function updateStep(index: number, patch: Partial<StepForm>): void {
    setForm((f) => ({ ...f, steps: f.steps.map((s, i) => (i === index ? { ...s, ...patch } : s)) }))
  }

  function addStep(): void {
    setForm((f) => (f.steps.length >= MAX_STEPS ? f : { ...f, steps: [...f.steps, newStepForm()] }))
  }

  function moveStep(index: number, direction: -1 | 1): void {
    setForm((f) => {
      const target = index + direction
      if (target < 0 || target >= f.steps.length) return f
      const steps = [...f.steps]
      const a = steps[index]
      const b = steps[target]
      if (a === undefined || b === undefined) return f
      steps[index] = b
      steps[target] = a
      return { ...f, steps }
    })
  }

  function removeStep(index: number): void {
    setForm((f) => ({ ...f, steps: f.steps.filter((_, i) => i !== index) }))
  }

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    const id = isNew ? newId.trim() : macroId
    if (id === undefined || id === '') {
      setSaveError('A macro id is required.')
      return
    }
    const built = buildPayload(moveToShow.trim(), form)
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
        navigate(`${showAutomationPath(showId)}/macros/${encodeURIComponent(id)}`)
        return
      }
      if (built.payload.show !== showId) {
        // Saved into a different show than the route names: this URL's
        // :showId no longer matches the macro's own show, so land on the
        // object at its new show-scoped address rather than leaving the
        // operator on a stale one.
        navigate(`${showAutomationPath(built.payload.show)}/macros/${encodeURIComponent(id)}`)
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
      <div className="card automation-inspector__section" role="status">
        {pageGate.reason}
      </div>
    )
  }
  if (!isNew && state.kind === 'loading') {
    return <div className="card automation-inspector__section text-muted">Loading macro…</div>
  }
  if (!isNew && state.kind === 'error') {
    return (
      <div className="card automation-inspector__section" role="alert" style={{ color: 'var(--bad-fg)' }}>
        {state.message}
      </div>
    )
  }

  const editable = writeGate.allowed
  const lastRun = (runs !== null && runs.length > 0 ? runs[0] : null) ?? null

  return (
    <div className="card">
      <div className="automation-inspector__section" style={{ background: 'var(--raised)' }}>
        <p className="t-meta" style={{ margin: 0, color: 'var(--text-faint)' }}>
          {isNew ? 'New macro' : 'Macro'} · Editing
        </p>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', gap: 12, marginTop: 6 }}>
          <h2 id="macro-step-editor-heading" className="t-heading" style={{ margin: 0 }}>
            {isNew ? 'New macro' : form.label || macroId}
          </h2>
          {!isNew && macroId !== undefined && <RunMacroButton macroId={macroId} label="Run" />}
        </div>
        <p className={`status-pair status-pair--${readinessBadge.tone === 'unknown' && readiness.kind === 'unchecked' ? 'unobserved' : readinessBadge.tone}`} style={{ marginTop: 8 }}>
          {readinessBadge.label}
        </p>
        {consequence !== null && (
          <p className="t-small" style={{ marginTop: 8, color: 'var(--warn-fg)' }}>
            {consequence}
          </p>
        )}
      </div>

      {!editable && (
        <p className="t-small automation-inspector__section" role="status" style={{ color: 'var(--text-muted)' }}>
          Viewing only: editing requires the <code>config:write</code> scope.
        </p>
      )}

      {isNew && (
        <label className="field automation-inspector__section" style={{ borderBottom: 0 }}>
          <span className="field__label">Macro id</span>
          <input className="field__input" type="text" value={newId} disabled={!editable} onChange={(e) => setNewId(e.target.value)} />
        </label>
      )}

      <fieldset disabled={!editable} className="automation-inspector__section" style={{ border: 0, display: 'grid', gap: 12 }}>
        <label className="field">
          <span className="field__label">Label</span>
          <input className="field__input" type="text" value={form.label} onChange={(e) => setForm({ ...form, label: e.target.value })} />
        </label>
        <label className="field">
          <span className="field__label">Description</span>
          <input className="field__input" type="text" value={form.description} onChange={(e) => setForm({ ...form, description: e.target.value })} />
        </label>
      </fieldset>

      <div className="automation-inspector__section">
        <h3 id="macro-step-list-heading" className="t-meta automation-inspector__eyebrow">
          Steps
        </h3>
        <p className="t-small" style={{ color: 'var(--text-muted)' }}>
          Each step invokes a show action, in order. A failed step stops the remaining steps unless it says
          otherwise; a step whose effect could not be confirmed does not, by default.
        </p>

        <ol className="list-plain" aria-labelledby="macro-step-list-heading" style={{ margin: 0, padding: 0, display: 'grid', gap: 12 }}>
          {form.steps.map((step, index) => {
            const stepBinding = bindings.get(step.action.trim())
            const stepFacts = facts[step.action.trim()]
            const deviatesFromDefault = step.onFailure !== 'continue' || step.onUnconfirmed !== 'continue'
            return (
              <li key={index} className="card">
                <fieldset disabled={!editable} className="automation-inspector__section" style={{ border: 0, display: 'grid', gap: 10 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, flexWrap: 'wrap' }}>
                    <span className="macro-step-row__index" aria-hidden="true">
                      ⠿
                    </span>
                    <strong className="t-subhead">Step {index + 1}</strong>
                    {editable && (
                      <span style={{ display: 'inline-flex', gap: 4 }}>
                        <button type="button" className="btn btn--quiet btn--compact" disabled={index === 0} onClick={() => moveStep(index, -1)} aria-label={`Move step ${index + 1} up`}>
                          ↑
                        </button>
                        <button
                          type="button"
                          className="btn btn--quiet btn--compact"
                          disabled={index === form.steps.length - 1}
                          onClick={() => moveStep(index, 1)}
                          aria-label={`Move step ${index + 1} down`}
                        >
                          ↓
                        </button>
                      </span>
                    )}
                    {stepBinding !== undefined && (
                      <span className={`status-pair status-pair--${stepBinding.state === 'ok' ? 'good' : stepBinding.state === 'broken' ? 'bad' : 'unobserved'}`}>
                        {stepBinding.state}
                      </span>
                    )}
                    {stepFacts?.unconfirmableByDesign === true && (
                      <span className="status-pair status-pair--unobserved">unconfirmable, by design</span>
                    )}
                  </div>
                  <label className="field">
                    <span className="field__label">Step id</span>
                    <input className="field__input" type="text" value={step.id} onChange={(e) => updateStep(index, { id: e.target.value })} />
                  </label>
                  <label className="field">
                    <span className="field__label">Action</span>
                    {actions !== null && actions.length > 0 ? (
                      <select className="field__input" value={step.action} onChange={(e) => updateStep(index, { action: e.target.value })}>
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
                        className="field__input"
                        type="text"
                        placeholder="show action id"
                        value={step.action}
                        onChange={(e) => updateStep(index, { action: e.target.value })}
                      />
                    )}
                  </label>
                  {stepBinding !== undefined && stepBinding.reason !== '' && (
                    <p className="t-small" style={{ color: 'var(--text-muted)' }}>{stepBinding.reason}</p>
                  )}

                  {/* A step row states its policy ONLY when it deviates from
                      the default (continue/continue) — DESIGN-DECISIONS-
                      AND-API-FACTS.md §6's own owner decision. */}
                  {deviatesFromDefault ? (
                    <>
                      <label className="field">
                        <span className="field__label">If this step fails</span>
                        <select className="field__input" value={step.onFailure} onChange={(e) => updateStep(index, { onFailure: e.target.value as MacroStepOnFailure })}>
                          <option value="continue">Continue with the remaining steps (default)</option>
                          <option value="abort">Abort the remaining steps</option>
                        </select>
                      </label>
                      <label className="field">
                        <span className="field__label">If this step cannot be confirmed</span>
                        <select className="field__input" value={step.onUnconfirmed} onChange={(e) => updateStep(index, { onUnconfirmed: e.target.value as MacroStepOnUnconfirmed })}>
                          <option value="continue">Continue with the remaining steps (default)</option>
                          <option value="abort">Abort the remaining steps</option>
                        </select>
                      </label>
                    </>
                  ) : (
                    editable && (
                      <button
                        type="button"
                        className="btn btn--quiet btn--compact"
                        onClick={() => updateStep(index, { onFailure: 'abort' })}
                      >
                        Change failure/confirmation policy…
                      </button>
                    )
                  )}

                  <label className="field">
                    <span className="field__label">If the coordinator refuses or is unreachable</span>
                    <select
                      className="field__input"
                      value={step.localFallbackClass}
                      onChange={(e) => updateStep(index, { localFallbackClass: e.target.value as LocalFallbackClass })}
                    >
                      {LOCAL_FALLBACK_CLASSES.map((cls) => (
                        <option key={cls} value={cls} disabled={stepFacts?.integration === 'resolume' && cls !== 'coordinator-required'}>
                          {cls}
                        </option>
                      ))}
                    </select>
                  </label>
                  {stepFacts?.integration === 'resolume' && (
                    <p className="t-small" role="status" style={{ color: 'var(--text-muted)' }}>
                      Every Resolume action is coordinator-required: Resolume holds no local fallback for a
                      coordinator-hosted adapter.
                    </p>
                  )}
                  <label className="field">
                    <span className="field__label">Local fallback reason (required, even when the class is &ldquo;none&rdquo;)</span>
                    <input
                      className="field__input"
                      type="text"
                      value={step.localFallbackReason}
                      onChange={(e) => updateStep(index, { localFallbackReason: e.target.value })}
                    />
                  </label>
                  {editable && form.steps.length > 1 && (
                    <button type="button" className="btn btn--quiet btn--compact" onClick={() => removeStep(index)}>
                      Remove step {index + 1}
                    </button>
                  )}
                </fieldset>
              </li>
            )
          })}
        </ol>
        {editable && (
          <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 12, flexWrap: 'wrap' }}>
            <button type="button" className="btn btn--secondary" onClick={addStep} disabled={form.steps.length >= MAX_STEPS}>
              Add step
            </button>
            {form.steps.length > 1 && <span className="t-small text-muted">Use each step&rsquo;s ↑/↓ buttons to reorder.</span>}
          </div>
        )}
      </div>

      {!isNew && (
        <fieldset disabled={!editable} className="automation-inspector__section" style={{ border: 0, borderTop: '1px solid var(--border)' }}>
          <h3 id="macro-move-heading" className="t-meta automation-inspector__eyebrow">
            Move to another show
          </h3>
          <ShowSelect ariaLabel="Move to another show" value={moveToShow} onChange={setMoveToShow} />
          <p className="t-small" style={{ color: 'var(--text-muted)', marginTop: 6 }}>
            Moving this macro out of {showId} removes it from {showId}&rsquo;s Automation list, and
            requires every step&rsquo;s action to also exist in the destination show: a step naming
            an action that only exists in {showId} is refused on save, naming the step.
          </p>
        </fieldset>
      )}

      {editable && (
        <div className="automation-inspector__footer">
          {saveError !== null && (
            <p role="alert" className="field__error">
              {saveError}
            </p>
          )}
          <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} onClick={() => void handleSave()} busy={saving} busyReason="Saving this macro revision…" className="btn btn--primary">
            {saving ? 'Saving…' : isNew ? 'Create macro' : 'Save macro'}
          </ScopedButton>
        </div>
      )}

      {!isNew && state.kind === 'loaded' && (
        <div className="automation-inspector__section">
          <h3 className="t-meta automation-inspector__eyebrow">Revision</h3>
          <p className="t-small" style={{ color: 'var(--text-muted)' }}>
            Active revision {state.config.revision}
            {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}.
          </p>

          <h3 className="t-meta automation-inspector__eyebrow" style={{ marginTop: 14 }}>
            Last run
          </h3>
          {runsError !== null && (
            <p role="alert" className="t-small" style={{ color: 'var(--bad-fg)' }}>
              {runsError}
            </p>
          )}
          {runs === null && runsError === null && <p className="t-small" style={{ color: 'var(--text-muted)' }}>Loading runs…</p>}
          {lastRun === null && runs !== null && <p className="t-small" style={{ color: 'var(--text-faint)' }}>Never run.</p>}
          {lastRun !== null && (
            <Link to={`${showAutomationPath(showId)}/macros/${encodeURIComponent(lastRun.macroObjectId)}/runs/${encodeURIComponent(lastRun.id)}`}>
              <p className="t-small" style={{ color: 'var(--text-muted)', margin: '4px 0' }}>
                {formatAbsolute(lastRun.createdAt)}: {lastRun.trigger}, {lastRun.issuerPrincipalName}
              </p>
              <MacroRunOutcome run={lastRun} />
            </Link>
          )}
        </div>
      )}
    </div>
  )
}
