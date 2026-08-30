import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  getMacroRun,
  invokeAction,
  putShowMacro,
  submitMacroRun,
  type ActionBinding,
  type ActionInvocationResult,
  type ConfigShowActionTarget,
  type ConfigShowMacro,
  type ConfigShowMacroLocalFallback,
  type MacroRun,
  type ShowActionConfigResponse,
  type ShowMacroConfigResponse,
} from '../api'
import { AttentionRow, BlankingPlate, Button, Field, Input, Panes, RuledStrip, Section, Segmented, Select, StatusPair, Table, TableWrap } from '../kit'
import type { Tone } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { fetchActionBindings, fetchShowActions, fetchShowContents, fetchShowMacros } from './showsData'
import {
  actionIntegrationLabel,
  actionTargetSummary,
  bindingLabel,
  bindingsByAction,
  bindingTone,
  lastRunForMacro,
  macroBindingSummary,
  macrosUsingAction,
} from './showsModel'

type ListState =
  | { kind: 'loading' }
  | { kind: 'loaded'; macros: ShowMacroConfigResponse[]; actions: ShowActionConfigResponse[]; bindings: ActionBinding[]; bindingsCheckedAt: string }
  | { kind: 'failed'; reason: string }

function useAutomation(showId: string): { state: ListState; reload: () => void; upsertMacro: (r: ShowMacroConfigResponse) => void; recheckBindings: () => void } {
  const [attempt, setAttempt] = useState(0)
  const [state, setState] = useState<ListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    fetchShowContents(showId)
      .then(async (contents) => {
        const [macros, actions, bindings] = await Promise.all([
          fetchShowMacros(contents.macros),
          fetchShowActions(contents.actions),
          fetchActionBindings(showId),
        ])
        if (!cancelled) setState({ kind: 'loaded', macros, actions, bindings, bindingsCheckedAt: new Date().toISOString() })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [showId, attempt])

  const upsertMacro = (response: ShowMacroConfigResponse) => {
    setState((prev) => (prev.kind === 'loaded' ? { ...prev, macros: prev.macros.map((m) => (m.id === response.id ? response : m)) } : prev))
  }

  const recheckBindings = () => {
    fetchActionBindings(showId).then((bindings) => {
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, bindings, bindingsCheckedAt: new Date().toISOString() } : prev))
    })
  }

  return { state, reload: () => setAttempt((n) => n + 1), upsertMacro, recheckBindings }
}

type Aside = { kind: 'none' } | { kind: 'step'; macroId: string; stepIndex: number } | { kind: 'run'; runId: string }

const KIND_FILTERS: readonly { value: 'all' | ConfigShowActionTarget['integration']; label: string }[] = [
  { value: 'all', label: 'All' },
  { value: 'fpp', label: 'FPP' },
  { value: 'mqtt', label: 'MQTT' },
  { value: 'resolume', label: 'Resolume' },
  { value: 'audio', label: 'Audio' },
]

export function ShowsAutomation() {
  const { id: showId = '' } = useParams<{ id: string }>()
  const model = useModelContext()
  const { state, reload, upsertMacro, recheckBindings } = useAutomation(showId)
  const [aside, setAside] = useState<Aside>({ kind: 'none' })
  const [filterText, setFilterText] = useState('')
  const [filterKind, setFilterKind] = useState<'all' | ConfigShowActionTarget['integration']>('all')

  if (state.kind === 'loading') {
    return (
      <Section id="au-list" title="Macros and actions">
        <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for this show's macros and actions." />
      </Section>
    )
  }

  if (state.kind === 'failed') {
    return (
      <Section id="au-list" title="Macros and actions">
        <RuledStrip
          absence="failed"
          label="Read failed"
          fact={state.reason}
          detail={
            <button type="button" className="sm-linkbutton" onClick={reload}>
              Try again
            </button>
          }
        />
      </Section>
    )
  }

  const { macros, actions, bindings, bindingsCheckedAt } = state
  const bindingMap = bindingsByAction(bindings)
  const canAuthor = evaluateScope(model.session, model.sessionFetchFailed, 'config:write')
  const canRun = evaluateScope(model.session, model.sessionFetchFailed, 'show:macro:run')
  const canInvoke = evaluateScope(model.session, model.sessionFetchFailed, 'show:action:invoke')
  const viewOnly = !canAuthor.allowed && evaluateAnyScope(model.session, model.sessionFetchFailed, ['show:macro:run', 'config:write']).allowed

  const matchesText = (label: string, id: string) =>
    filterText === '' || label.toLowerCase().includes(filterText.toLowerCase()) || id.toLowerCase().includes(filterText.toLowerCase())

  const visibleMacros = macros.filter((macro) => {
    if (!matchesText(macro.payload.label, macro.id)) return false
    if (filterKind === 'all') return true
    return macro.payload.steps.some((step) => actions.find((a) => a.id === step.action)?.payload.target.integration === filterKind)
  })

  const visibleActions = actions.filter((action) => {
    if (!matchesText(action.payload.label, action.id)) return false
    if (filterKind === 'all') return true
    return action.payload.target.integration === filterKind
  })

  const inMacro = visibleActions.filter((a) => macrosUsingAction(macros, a.id).length > 0)
  const notInMacro = visibleActions.filter((a) => macrosUsingAction(macros, a.id).length === 0)

  const broken = actions
    .map((a) => ({ action: a, binding: bindingMap.get(a.id) }))
    .filter(({ binding }) => binding !== undefined && binding.state !== 'ok')

  const stepAside = aside.kind === 'step' ? macros.find((m) => m.id === aside.macroId) ?? null : null

  return (
    <Panes>
      <div>
        <Section
          id="au-list"
          title="Macros and actions"
          aside={
            <div className="sm-inline-row">
              <Button
                disabled
                title="A new action needs an integration-specific target form the mock does not draw; see docs/ui-rebuild/OPEN-DECISIONS.md D-017."
              >
                New action
              </Button>
              <Button
                variant="primary"
                disabled
                title="A new macro needs a first step chosen before it can be saved, and the mock does not draw that form; see docs/ui-rebuild/OPEN-DECISIONS.md D-017."
              >
                New macro
              </Button>
            </div>
          }
        >
          <p className="sm-small sm-muted">
            A macro is what an operator fires. Actions are its steps, in order, and never run on their own in a show, except by direct invoke or a cue
            that names one.
          </p>
          <div className="sm-inline-row sm-stack-3">
            <Input aria-label="Filter macros and actions" placeholder="Filter macros and actions…" value={filterText} onChange={(e) => setFilterText(e.target.value)} />
            <Segmented label="Filter by integration" value={filterKind} options={KIND_FILTERS} onChange={setFilterKind} />
            <Button onClick={recheckBindings}>Check bindings</Button>
          </div>
          <p className="sm-small sm-faint sm-stack-2">Bindings checked {formatClock(bindingsCheckedAt) ?? 'just now'}, by this browser.</p>

          {viewOnly && (
            <div className="sm-attn sm-stack-4">
              <span className="sm-strip__label">Run only</span>
              <div>
                <p className="sm-attn__fact">
                  You hold <span className="sm-data">show:macro:run</span>, not <span className="sm-data">config:write</span>.
                </p>
                <p className="sm-attn__detail">You can fire these and read every run; authoring is closed to you.</p>
              </div>
            </div>
          )}
        </Section>

        <Section id="au-attn" title="Needs you" aside={broken.length > 0 ? <span className="sm-small sm-muted">{broken.length} items</span> : undefined}>
          {broken.length === 0 ? (
            <BlankingPlate
              absence="empty"
              stamp="Clear"
              eyebrow="Bindings · empty"
              title="Nothing needs you"
              detail="Every action's binding checked ok, or none has been checked yet."
            />
          ) : (
            broken.map(({ action, binding }) => (
              <AttentionRow
                key={action.id}
                tone={bindingTone(binding?.state)}
                state={binding?.state === 'broken' ? 'Binding broken' : 'Binding unknown'}
                fact={
                  <>
                    <strong>{action.payload.label}</strong> {binding?.reason}
                  </>
                }
                detail={macrosUsingAction(macros, action.id).length > 0 ? `Used by ${macrosUsingAction(macros, action.id).join(', ')}.` : 'Not used by any macro in this show.'}
              />
            ))
          )}
        </Section>

        <Section id="au-macros" title="Macros" aside={<span className="sm-small sm-muted">{visibleMacros.length}</span>}>
          <p className="sm-small sm-muted">Select a step to edit how it runs. Editing a step's own action changes every macro that uses it.</p>
          {visibleMacros.length === 0 ? (
            <RuledStrip absence="empty" label="None" fact="No macro matches here." />
          ) : (
            visibleMacros.map((macro) => {
              const summary = macroBindingSummary(macro.payload.steps, bindingMap)
              const lastRun = lastRunForMacro(model.macroRuns, macro.id)
              const cardTone: Tone = summary.broken > 0 ? 'bad' : summary.unknown > 0 ? 'unknown' : 'good'
              return (
                <MacroCard
                  key={macro.id}
                  macro={macro}
                  actions={actions}
                  bindings={bindingMap}
                  summary={summary}
                  cardTone={cardTone}
                  lastRun={lastRun}
                  canRun={canRun}
                  selectedStep={aside.kind === 'step' && aside.macroId === macro.id ? aside.stepIndex : null}
                  onSelectStep={(stepIndex) => setAside({ kind: 'step', macroId: macro.id, stepIndex })}
                  onOpenRun={() => (lastRun !== null ? setAside({ kind: 'run', runId: lastRun.id }) : undefined)}
                />
              )
            })
          )}
        </Section>

        <Section id="au-actions" title="Actions" aside={<span className="sm-small sm-muted">{visibleActions.length}</span>}>
          <p className="sm-small sm-muted">An action's own target, safety class, and confirmation rule. Authoring an action is not yet built here; see D-017.</p>
          <h3 className="sm-subsection__title">
            In a macro <span className="sm-small sm-muted">· {inMacro.length}</span>
          </h3>
          <ActionTable actions={inMacro} macros={macros} bindings={bindingMap} canInvoke={canInvoke} usedByColumn />
          <h3 className="sm-subsection__title sm-stack-4">
            Not in a macro <span className="sm-small sm-muted">· {notInMacro.length}</span>
          </h3>
          <p className="sm-small sm-muted">Reachable only by direct invoke, or by a cue that names it and pins a revision. No macro will ever fire it.</p>
          <ActionTable actions={notInMacro} macros={macros} bindings={bindingMap} canInvoke={canInvoke} usedByColumn={false} />
        </Section>
      </div>

      <aside>
        {stepAside !== null && aside.kind === 'step' && (
          <StepEditor
            key={`${stepAside.id}:${aside.stepIndex}:${stepAside.revision}`}
            macro={stepAside}
            stepIndex={aside.stepIndex}
            actions={actions}
            bindings={bindingMap}
            canAuthor={canAuthor}
            onSaved={(response) => {
              upsertMacro(response)
              setAside({ kind: 'none' })
            }}
            onCancel={() => setAside({ kind: 'none' })}
          />
        )}
        {aside.kind === 'run' && <RunViewer runId={aside.runId} onClose={() => setAside({ kind: 'none' })} />}
      </aside>
    </Panes>
  )
}

function MacroCard({
  macro,
  actions,
  bindings,
  summary,
  cardTone,
  lastRun,
  canRun,
  selectedStep,
  onSelectStep,
  onOpenRun,
}: {
  macro: ShowMacroConfigResponse
  actions: readonly ShowActionConfigResponse[]
  bindings: ReadonlyMap<string, ActionBinding>
  summary: { ok: number; broken: number; unknown: number; total: number }
  cardTone: Tone
  lastRun: ReturnType<typeof lastRunForMacro>
  canRun: { allowed: boolean; reason?: string }
  selectedStep: number | null
  onSelectStep: (index: number) => void
  onOpenRun: () => void
}) {
  const [runOutcome, setRunOutcome] = useState<{ tone: Tone; detail: string } | null>(null)
  const [running, setRunning] = useState(false)

  const run = () => {
    setRunning(true)
    setRunOutcome(null)
    submitMacroRun(macro.id)
      .then((resp) => setRunOutcome({ tone: 'good', detail: resp.replay ? 'Replayed an in-flight submission with the same key.' : 'Accepted. Steps report their own outcomes.' }))
      .catch((err: unknown) => setRunOutcome({ tone: 'bad', detail: describeApiError(err) }))
      .finally(() => setRunning(false))
  }

  return (
    <div className="sm-panel sm-stack-4">
      <div className="sm-section__head">
        <div>
          <h3 className="sm-subsection__title">{macro.payload.label}</h3>
          <p className="sm-data sm-small sm-faint">
            {macro.id} · revision {macro.revision} · {macro.payload.steps.length} {macro.payload.steps.length === 1 ? 'step' : 'steps'}
          </p>
        </div>
        <div className="sm-inline-row">
          <StatusPair
            tone={cardTone}
            label={summary.broken > 0 ? `Step binding broken` : summary.unknown > 0 ? 'Bindings not fully checked' : `${summary.ok} of ${summary.total} bindings ok`}
          />
          <Button size="gloved" onClick={run} disabled={running || !canRun.allowed} title={canRun.allowed ? undefined : canRun.reason}>
            {running ? 'Running…' : 'Run'}
          </Button>
        </div>
      </div>
      {macro.payload.description !== '' && <p className="sm-small sm-muted">{macro.payload.description}</p>}

      <ol className="sm-plain-list sm-data">
        {macro.payload.steps.map((step, index) => {
          const action = actions.find((a) => a.id === step.action)
          const binding = bindings.get(step.action)
          return (
            <li key={step.id} aria-current={selectedStep === index ? 'true' : undefined} className={selectedStep === index ? 'sm-table__row--current' : undefined}>
              <button type="button" className="sm-linkbutton" onClick={() => onSelectStep(index)} aria-pressed={selectedStep === index}>
                {index + 1}. {action?.payload.label ?? step.action}
              </button>{' '}
              <span className="sm-small sm-faint">{step.id}</span>
              {selectedStep === index && <span className="sm-viewing">Editing</span>}
              {action !== undefined && <span className="sm-chip">{actionIntegrationLabel(action.payload.target.integration)}</span>}
              <StatusPair tone={bindingTone(binding?.state)} label={bindingLabel(binding?.state)} />
              {step.onFailure === 'abort' && <span className="sm-small sm-muted"> Aborts the rest if it fails.</span>}
            </li>
          )
        })}
      </ol>

      <p className="sm-section__footnote">
        {lastRun === null ? (
          'Never run. There is no run history to read, that is a settled fact, not missing evidence.'
        ) : (
          <>
            Last run <span className="sm-data">{formatClock(lastRun.createdAt) ?? 'at an unrecorded time'}</span> by {lastRun.issuerPrincipalName}{' '}
            <span className="sm-data sm-small">{lastRun.trigger}</span> ·{' '}
            {lastRun.state === 'running' ? (
              <StatusPair tone="pending" label="Running" />
            ) : (
              <>
                <StatusPair tone={lastRun.completed === true ? 'good' : 'unknown'} label={lastRun.completed === true ? 'Completed' : 'Not completed'} />{' '}
                <StatusPair tone={lastRun.confirmed === true ? 'good' : 'warn'} label={lastRun.confirmed === true ? 'Confirmed' : 'Not confirmed'} />
              </>
            )}{' '}
            <button type="button" className="sm-linkbutton" onClick={onOpenRun}>
              Run detail
            </button>
          </>
        )}
      </p>
      {runOutcome !== null && <RuledStrip absence={runOutcome.tone === 'good' ? 'empty' : 'failed'} label={runOutcome.tone === 'good' ? 'Accepted' : 'Refused'} fact={runOutcome.detail} />}
    </div>
  )
}

function ActionTable({
  actions,
  macros,
  bindings,
  canInvoke,
  usedByColumn,
}: {
  actions: readonly ShowActionConfigResponse[]
  macros: readonly ShowMacroConfigResponse[]
  bindings: ReadonlyMap<string, ActionBinding>
  canInvoke: { allowed: boolean; reason?: string }
  usedByColumn: boolean
}) {
  return (
    <TableWrap label={usedByColumn ? 'Actions used by a macro, scrollable' : 'Actions not used by any macro, scrollable'}>
      <Table>
        <thead>
          <tr>
            <th scope="col">Action</th>
            <th scope="col">Kind</th>
            {usedByColumn ? <th scope="col">Used by</th> : <th scope="col">Invoke</th>}
            <th scope="col">Binding</th>
          </tr>
        </thead>
        <tbody>
          {actions.length === 0 ? (
            <tr>
              <td colSpan={4}>
                <RuledStrip absence="empty" label="None" fact="No action matches here." />
              </td>
            </tr>
          ) : (
            actions.map((action) => {
              const binding = bindings.get(action.id)
              return (
                <tr key={action.id}>
                  <td>
                    <strong>{action.payload.label}</strong>
                    <br />
                    <span className="sm-data sm-small sm-faint">
                      {actionTargetSummary(action.payload.target)} · {action.id}
                    </span>
                  </td>
                  <td>
                    <span className="sm-chip">{actionIntegrationLabel(action.payload.target.integration)}</span>
                  </td>
                  {usedByColumn ? (
                    <td className="sm-small sm-muted">{macrosUsingAction(macros, action.id).join(', ')}</td>
                  ) : (
                    <td>
                      <InvokeButton actionId={action.id} binding={binding} canInvoke={canInvoke} />
                    </td>
                  )}
                  <td>
                    <StatusPair tone={bindingTone(binding?.state)} label={bindingLabel(binding?.state)} />
                    {binding !== undefined && binding.state !== 'ok' && <br />}
                    {binding !== undefined && binding.state !== 'ok' && <span className="sm-small sm-faint">{binding.reason}</span>}
                  </td>
                </tr>
              )
            })
          )}
        </tbody>
      </Table>
    </TableWrap>
  )
}

function InvokeButton({ actionId, binding, canInvoke }: { actionId: string; binding: ActionBinding | undefined; canInvoke: { allowed: boolean; reason?: string } }) {
  const [state, setState] = useState<{ tone: Tone; detail: string } | null>(null)
  const [invoking, setInvoking] = useState(false)

  let reason: string | undefined
  if (!canInvoke.allowed) reason = canInvoke.reason
  else if (binding?.state === 'broken') reason = `Binding broken: ${binding.reason}`
  else if (binding?.state === 'unknown') reason = binding.reason

  const invoke = () => {
    setInvoking(true)
    setState(null)
    invokeAction(actionId)
      .then((result: ActionInvocationResult) => setState({ tone: outcomeTone(result.outcome), detail: result.outcomeReason }))
      .catch((err: unknown) => setState({ tone: 'bad', detail: describeApiError(err) }))
      .finally(() => setInvoking(false))
  }

  return (
    <>
      <Button size="compact" onClick={invoke} disabled={invoking || reason !== undefined} title={reason}>
        {invoking ? 'Invoking…' : 'Invoke'}
      </Button>
      {state !== null && (
        <>
          <br />
          <StatusPair tone={state.tone} label={state.detail} />
        </>
      )}
    </>
  )
}

function outcomeTone(outcome: ActionInvocationResult['outcome']): Tone {
  if (outcome === 'confirmed') return 'good'
  if (outcome === 'unconfirmed' || outcome === 'unconfirmable') return 'warn'
  if (outcome === 'refused' || outcome === 'failed') return 'bad'
  return 'pending'
}

function stepOutcomeTone(outcome: MacroRun['steps'][number]['outcome'], state: MacroRun['steps'][number]['state']): Tone {
  if (outcome === 'confirmed') return 'good'
  if (outcome === 'unconfirmed' || outcome === 'unconfirmable') return 'warn'
  if (outcome === 'failed') return 'bad'
  if (state === 'skipped') return 'unknown'
  return 'pending'
}

const FALLBACK_OPTIONS: readonly { value: ConfigShowMacroLocalFallback['class']; label: string }[] = [
  { value: 'none', label: 'None' },
  { value: 'coordinator-required', label: 'Coordinator required' },
  { value: 'silence', label: 'Silence' },
]

function StepEditor({
  macro,
  stepIndex,
  actions,
  bindings,
  canAuthor,
  onSaved,
  onCancel,
}: {
  macro: ShowMacroConfigResponse
  stepIndex: number
  actions: readonly ShowActionConfigResponse[]
  bindings: ReadonlyMap<string, ActionBinding>
  canAuthor: { allowed: boolean; reason?: string }
  onSaved: (response: ShowMacroConfigResponse) => void
  onCancel: () => void
}) {
  const step = macro.payload.steps[stepIndex]
  const [actionId, setActionId] = useState(step?.action ?? '')
  const [stepId, setStepId] = useState(step?.id ?? '')
  const [onFailure, setOnFailure] = useState<'abort' | 'continue'>(step?.onFailure ?? 'continue')
  const [onUnconfirmed, setOnUnconfirmed] = useState<'abort' | 'continue'>(step?.onUnconfirmed ?? 'continue')
  const [fallbackClass, setFallbackClass] = useState<ConfigShowMacroLocalFallback['class']>(step?.localFallback.class ?? 'none')
  const [fallbackReason, setFallbackReason] = useState(step?.localFallback.reason ?? '')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  if (step === undefined) return null

  const binding = bindings.get(step.action)
  const action = actions.find((a) => a.id === actionId)

  let blockReason: string | null = null
  if (actionId.trim() === '') blockReason = 'A step needs an action.'
  else if (stepId.trim() === '') blockReason = 'A step needs an id.'
  else if (macro.payload.steps.some((s, i) => i !== stepIndex && s.id === stepId.trim())) blockReason = `The id "${stepId}" already names another step in this macro.`
  else if (fallbackReason.trim() === '') blockReason = 'State what happens locally while the coordinator is unreachable, in your own words.'

  const save = () => {
    if (blockReason !== null) return
    const steps = macro.payload.steps.map((s, i) =>
      i === stepIndex
        ? { id: stepId.trim(), action: actionId, onFailure, onUnconfirmed, localFallback: { class: fallbackClass, reason: fallbackReason.trim() } }
        : s,
    )
    const payload: ConfigShowMacro = { show: macro.payload.show, label: macro.payload.label, description: macro.payload.description, steps }
    setSaving(true)
    setSaveError(null)
    putShowMacro(macro.id, payload)
      .then((response) => onSaved(response))
      .catch((err: unknown) => setSaveError(describeApiError(err)))
      .finally(() => setSaving(false))
  }

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow sm-eyebrow--accent">
        Editing · {macro.payload.label} step {stepIndex + 1} of {macro.payload.steps.length}
      </p>

      {binding !== undefined && binding.state !== 'ok' && (
        <div className="sm-attn">
          <StatusPair tone={bindingTone(binding.state)} label={bindingLabel(binding.state)} />
          <div>
            <p className="sm-attn__fact">{binding.reason}</p>
          </div>
        </div>
      )}

      <div className="sm-inspector__group">
        <Field label="Action" help="This show's own actions only. A step cannot reference another show's action.">
          {(props) => (
            <Select {...props} value={actionId} onChange={(e) => setActionId(e.target.value)}>
              {actions.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.payload.label} · {a.payload.target.integration}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Step id" help="Unique within this macro. It is what a run's step rows are keyed by.">
          {(props) => <Input {...props} className="sm-data" value={stepId} onChange={(e) => setStepId(e.target.value)} />}
        </Field>
      </div>

      <div className="sm-inspector__group">
        <h3 className="sm-subsection__title">If this step fails</h3>
        <Segmented
          label="If this step fails"
          value={onFailure}
          onChange={setOnFailure}
          options={[
            { value: 'abort', label: 'Abort the rest' },
            { value: 'continue', label: 'Continue' },
          ]}
        />
      </div>

      <div className="sm-inspector__group">
        <h3 className="sm-subsection__title">If it cannot be confirmed</h3>
        <Segmented
          label="If it cannot be confirmed"
          value={onUnconfirmed}
          onChange={setOnUnconfirmed}
          options={[
            { value: 'abort', label: 'Abort the rest' },
            { value: 'continue', label: 'Continue' },
          ]}
        />
        <p className="sm-small sm-faint">Unconfirmed means no evidence arrived, not that the wrong thing happened.</p>
      </div>

      <div className="sm-inspector__group">
        <Field label="If the coordinator refuses or is unreachable">
          {(props) => (
            <Select {...props} value={fallbackClass} onChange={(e) => setFallbackClass(e.target.value as ConfigShowMacroLocalFallback['class'])}>
              {FALLBACK_OPTIONS.map((o) => (
                <option key={o.value} value={o.value}>
                  {o.label}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Reason" help="Required, in your own words. This is what an operator reads while the coordinator is unreachable.">
          {(props) => <Input {...props} value={fallbackReason} onChange={(e) => setFallbackReason(e.target.value)} />}
        </Field>
        <p className="sm-small sm-faint">
          A <span className="sm-data">reduced</span> fallback is not offered by this schema: only none, coordinator-required, and silence are accepted.
        </p>
      </div>

      <div className="sm-inspector__actions">
        <span className="sm-small sm-muted">{action !== undefined ? `${actionIntegrationLabel(action.payload.target.integration)} action` : 'Creates a new step revision'}</span>
        <div className="sm-btn-row">
          <Button variant="quiet" onClick={onCancel} disabled={saving}>
            Cancel
          </Button>
          <Button
            variant="primary"
            onClick={save}
            disabled={saving || !canAuthor.allowed || blockReason !== null}
            title={!canAuthor.allowed ? canAuthor.reason : (blockReason ?? undefined)}
          >
            {saving ? 'Saving…' : 'Save macro'}
          </Button>
        </div>
      </div>
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
    </div>
  )
}

function RunViewer({ runId, onClose }: { runId: string; onClose: () => void }) {
  const [state, setState] = useState<{ kind: 'loading' } | { kind: 'loaded'; run: MacroRun } | { kind: 'failed'; reason: string }>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })
    getMacroRun(runId)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', run: response.run })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [runId])

  if (state.kind === 'loading') {
    return (
      <div className="sm-inspector">
        <RuledStrip absence="loading" label="Reading" fact="Fetching this run's step detail." />
      </div>
    )
  }
  if (state.kind === 'failed') {
    return (
      <div className="sm-inspector">
        <RuledStrip absence="failed" label="Read failed" fact={state.reason} />
      </div>
    )
  }

  const { run } = state

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow sm-eyebrow--accent">Run detail</p>
      <p className="sm-small sm-muted">
        Started <span className="sm-data">{formatClock(run.createdAt) ?? 'an unrecorded time'}</span>
        {run.finishedAt !== null && (
          <>
            , finished <span className="sm-data">{formatClock(run.finishedAt)}</span>
          </>
        )}{' '}
        · revision <span className="sm-data">{run.macroRevision}</span> · by {run.issuerPrincipalName} <span className="sm-data">{run.trigger}</span>
      </p>

      <div className="sm-inspector__group">
        <p className="sm-small">
          <StatusPair tone={run.state === 'running' ? 'pending' : run.completed === true ? 'good' : 'unknown'} label={run.state === 'running' ? 'Running' : run.completed === true ? 'Completed' : 'Not completed'} />
        </p>
        <p className="sm-small">
          <StatusPair tone={run.confirmed === true ? 'good' : run.state === 'running' ? 'pending' : 'warn'} label={run.confirmed === true ? 'Confirmed' : run.state === 'running' ? 'Pending' : 'Not confirmed'} />
        </p>
        {run.reason !== '' && <p className="sm-small sm-muted">{run.reason}</p>}
      </div>

      <ol className="sm-plain-list">
        {run.steps.map((step) => (
          <li key={step.stepId}>
            <span className="sm-data">{step.stepIndex + 1}</span> <span className="sm-data">{step.stepId}</span>{' '}
            <StatusPair tone={stepOutcomeTone(step.outcome, step.state)} label={step.outcome ?? step.state} />
            <br />
            <span className="sm-small sm-muted">
              {step.dispatchedAt === null ? 'Not dispatched.' : `Dispatched ${formatClock(step.dispatchedAt) ?? ''}`}
              {step.resolvedAt !== null && ` · resolved ${formatClock(step.resolvedAt) ?? ''}`}
            </span>
            <br />
            <span className="sm-small sm-muted">{step.outcomeReason}</span>
          </li>
        ))}
      </ol>
      {run.attributionDegraded && (
        <p className="sm-small">
          <StatusPair tone="warn" label="Attribution degraded" /> This run's own audit write may not be durable. Each step's outcome above is still what was
          observed.
        </p>
      )}

      <div className="sm-inspector__actions">
        <Button variant="quiet" onClick={onClose}>
          Close
        </Button>
      </div>
    </div>
  )
}
