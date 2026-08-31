import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import {
  getMacroRun,
  getShowAction,
  getShowActionRevisions,
  getShowMacro,
  getShowMacroRevisions,
  invokeAction,
  listConfigObjects,
  listResolumeActions,
  putShowAction,
  putShowMacro,
  submitMacroRun,
  type ActionBinding,
  type ActionInvocationResult,
  type ConfigObjectSummary,
  type ConfigShowAction,
  type ConfigShowActionMQTTExpect,
  type ConfigShowActionTarget,
  type ConfigShowMacro,
  type ConfigShowMacroLocalFallback,
  type MacroRun,
  type ResolumeAction,
  type ShowActionConfigResponse,
  type ShowMacroConfigResponse,
} from '../api'
import { AttentionRow, BlankingPlate, Button, Choice, Field, Input, Panes, RevisionHistory, RuledStrip, Section, Segmented, Select, StatusPair, Table, TableWrap, Textarea } from '../kit'
import type { Tone } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../domain/session'
import { guardedCreate, guardedSave, type SaveOutcome } from '../domain/save'
import { formatClock } from '../domain/time'
import { StaleWriteStrip } from './StaleWrite'
import { fetchActionBindings, fetchShowActions, fetchShowContents, fetchShowMacros } from './showsData'
import {
  actionIntegrationLabel,
  actionTargetSummary,
  audioDerivedSafetyClass,
  bindingLabel,
  bindingsByAction,
  bindingTone,
  fppDerivedSafetyClass,
  lastRunForMacro,
  macroBindingSummary,
  macroUsagesForAction,
  macrosUsingAction,
  resolumeDerivedSafetyClass,
  slugify,
  type SafetyClass,
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
    setState((prev) => {
      if (prev.kind !== 'loaded') return prev
      const known = prev.macros.some((m) => m.id === response.id)
      return { ...prev, macros: known ? prev.macros.map((m) => (m.id === response.id ? response : m)) : [...prev.macros, response] }
    })
  }

  const recheckBindings = () => {
    fetchActionBindings(showId).then((bindings) => {
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, bindings, bindingsCheckedAt: new Date().toISOString() } : prev))
    })
  }

  return { state, reload: () => setAttempt((n) => n + 1), upsertMacro, recheckBindings }
}

type Aside =
  | { kind: 'none' }
  | { kind: 'step'; macroId: string; stepIndex: number }
  | { kind: 'run'; runId: string }
  | { kind: 'action-draft' }
  | { kind: 'action-edit'; actionId: string }
  | { kind: 'macro-draft' }

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
              <Button disabled={!canAuthor.allowed} title={canAuthor.allowed ? undefined : canAuthor.reason} onClick={() => setAside({ kind: 'action-draft' })}>
                New action
              </Button>
              <Button
                variant="primary"
                disabled={!canAuthor.allowed}
                title={canAuthor.allowed ? undefined : canAuthor.reason}
                onClick={() => setAside({ kind: 'macro-draft' })}
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
          <p className="sm-small sm-muted">An action's row edits the action itself, its target, its safety class, and what counts as confirmation.</p>
          <h3 className="sm-subsection__title">
            In a macro <span className="sm-small sm-muted">· {inMacro.length}</span>
          </h3>
          <ActionTable actions={inMacro} macros={macros} bindings={bindingMap} canInvoke={canInvoke} usedByColumn onOpenAction={(id) => setAside({ kind: 'action-edit', actionId: id })} />
          <h3 className="sm-subsection__title sm-stack-4">
            Not in a macro <span className="sm-small sm-muted">· {notInMacro.length}</span>
          </h3>
          <p className="sm-small sm-muted">Reachable only by direct invoke, or by a cue that names it and pins a revision. No macro will ever fire it.</p>
          <ActionTable actions={notInMacro} macros={macros} bindings={bindingMap} canInvoke={canInvoke} usedByColumn={false} onOpenAction={(id) => setAside({ kind: 'action-edit', actionId: id })} />
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
        {aside.kind === 'action-draft' && (
          <ActionDraft
            showId={showId}
            model={model}
            canAuthor={canAuthor}
            onCreated={() => {
              reload()
              setAside({ kind: 'none' })
            }}
            onCancel={() => setAside({ kind: 'none' })}
          />
        )}
        {aside.kind === 'action-edit' &&
          (() => {
            const editing = actions.find((a) => a.id === aside.actionId)
            if (editing === undefined) return null
            return (
              <ActionEditor
                key={`${editing.id}:${editing.revision}`}
                action={editing}
                model={model}
                canAuthor={canAuthor}
                binding={bindingMap.get(editing.id)}
                macroUsages={macroUsagesForAction(macros, editing.id)}
                onSaved={() => {
                  reload()
                  setAside({ kind: 'none' })
                }}
                onCancel={() => setAside({ kind: 'none' })}
                onReloadAfterStale={() => {
                  reload()
                  setAside({ kind: 'none' })
                }}
              />
            )
          })()}
        {aside.kind === 'macro-draft' && (
          <MacroDraft
            showId={showId}
            actions={actions}
            model={model}
            canAuthor={canAuthor}
            onCreated={(response) => {
              upsertMacro(response)
              setAside({ kind: 'step', macroId: response.id, stepIndex: 0 })
            }}
            onCancel={() => setAside({ kind: 'none' })}
          />
        )}
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
  onOpenAction,
}: {
  actions: readonly ShowActionConfigResponse[]
  macros: readonly ShowMacroConfigResponse[]
  bindings: ReadonlyMap<string, ActionBinding>
  canInvoke: { allowed: boolean; reason?: string }
  usedByColumn: boolean
  onOpenAction: (actionId: string) => void
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
                    <button type="button" className="sm-linkbutton" onClick={() => onOpenAction(action.id)}>
                      <strong>{action.payload.label}</strong>
                    </button>
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
  const [stale, setStale] = useState<Extract<SaveOutcome<ShowMacroConfigResponse>, { kind: 'stale' }> | null>(null)

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
    setStale(null)
    guardedSave({
      loaded: macro,
      read: () => getShowMacro(macro.id),
      write: () => putShowMacro(macro.id, payload),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          onSaved(outcome.response)
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
      {stale !== null && (
        <StaleWriteStrip
          stale={stale}
          onReload={() => {
            setStale(null)
            getShowMacro(macro.id).then(onSaved).catch((err: unknown) => setSaveError(describeApiError(err)))
          }}
        />
      )}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
      <RevisionHistory fetch={() => getShowMacroRevisions(macro.id)} reloadKey={`${macro.id}:${macro.revision}`} />
    </div>
  )
}

// ---------------------------------------------------------------------
// The creation pattern (D-011 B / D-017 B): new action, editing an
// action, and new macro. Object Creation.dc.html sections 3, 4 and 5.
// ---------------------------------------------------------------------

const ACTION_INTEGRATION_OPTIONS: readonly { value: ConfigShowActionTarget['integration']; label: string }[] = [
  { value: 'fpp', label: 'FPP' },
  { value: 'mqtt', label: 'MQTT' },
  { value: 'resolume', label: 'Resolume' },
  { value: 'audio', label: 'Audio' },
]

const SAFETY_CLASS_OPTIONS: readonly { value: SafetyClass; label: string }[] = [
  { value: 'none', label: 'None' },
  { value: 'blackout', label: 'Blackout' },
  { value: 'stop', label: 'Stop' },
  { value: 'powerOff', label: 'Power off' },
]

/** The eight wire actions fppcommand_primitives.go's registry accepts, in that file's own order. */
const FPP_PRIMITIVE_OPTIONS = [
  'stopPlaylist',
  'startPlaylist',
  'stopPlaylistGracefully',
  'pausePlaylist',
  'resumePlaylist',
  'nextPlaylistItem',
  'prevPlaylistItem',
  'setVolume',
] as const

type FppParamDef = {
  name: string
  kind: 'string' | 'bool' | 'int' | 'enum'
  required: boolean
  label: string
  help?: string
  min?: number
  max?: number
  options?: readonly string[]
  default: string
}

/** Only the three primitives fppcommand_primitives.go declares Params for. The other five take none. */
const FPP_PRIMITIVE_PARAMS: Record<string, FppParamDef[]> = {
  startPlaylist: [
    { name: 'playlist', kind: 'string', required: true, label: 'Playlist name', default: '' },
    { name: 'repeat', kind: 'bool', required: false, label: 'Repeat', default: 'false' },
    { name: 'ifBusy', kind: 'enum', required: false, label: 'If already busy', options: ['refuse', 'replace'], default: 'refuse' },
  ],
  stopPlaylistGracefully: [{ name: 'afterLoop', kind: 'bool', required: false, label: 'Wait for the current loop to finish', default: 'false' }],
  setVolume: [{ name: 'volume', kind: 'int', required: true, label: 'Volume', min: 0, max: 100, default: '' }],
}

function defaultFppParamValues(primitive: string): Record<string, string> {
  return Object.fromEntries((FPP_PRIMITIVE_PARAMS[primitive] ?? []).map((def) => [def.name, def.default]))
}

/** The thirteen operation names showActionAudioActions declares (internal/coordinator/config/showaction.go). */
const AUDIO_ACTION_OPTIONS = [
  'audio.session.apply',
  'audio.session.prepare',
  'audio.session.start',
  'audio.session.pause',
  'audio.session.resume',
  'audio.session.seek',
  'audio.session.advance',
  'audio.session.stop',
  'audio.session.clear',
  'audio.gain.set',
  'audio.gain.fade',
  'audio.output.mute',
  'audio.output.unmute',
] as const

type FppTargetValue = { instanceId: string; primitive: string; params: Record<string, string> }
type MqttTargetValue = {
  broker: string
  topic: string
  payload: string
  qos: '0' | '1' | '2'
  retain: boolean
  expectKind: ConfigShowActionMQTTExpect['kind']
  expectTopic: string
  expectDeadline: string
  expectValue: string
}
type ResolumeTargetValue = { action: string; ref: Record<string, string> }
type AudioTargetValue = { audioNodeId: string; audioSessionId: string; audioAction: string; gainDb: string }

function emptyFppValue(): FppTargetValue {
  return { instanceId: '', primitive: '', params: {} }
}
function emptyMqttValue(): MqttTargetValue {
  return { broker: '', topic: '', payload: '', qos: '0', retain: false, expectKind: 'none', expectTopic: '', expectDeadline: '', expectValue: '' }
}
function emptyResolumeValue(): ResolumeTargetValue {
  return { action: '', ref: {} }
}
function emptyAudioValue(): AudioTargetValue {
  return { audioNodeId: '', audioSessionId: '', audioAction: '', gainDb: '' }
}

function fppValueFromTarget(target: ConfigShowActionTarget): FppTargetValue {
  const primitive = target.primitive ?? ''
  const params: Record<string, string> = {}
  for (const def of FPP_PRIMITIVE_PARAMS[primitive] ?? []) {
    const raw = target.params?.[def.name]
    params[def.name] = raw === undefined ? def.default : def.kind === 'bool' ? String(Boolean(raw)) : String(raw)
  }
  return { instanceId: target.instanceId ?? '', primitive, params }
}

function mqttValueFromTarget(target: ConfigShowActionTarget): MqttTargetValue {
  const expect = target.expect
  return {
    broker: target.broker ?? '',
    topic: target.publish?.topic ?? '',
    payload: target.publish?.payload ?? '',
    qos: String(target.publish?.qos ?? 0) as MqttTargetValue['qos'],
    retain: target.publish?.retain ?? false,
    expectKind: expect?.kind ?? 'none',
    expectTopic: expect?.topic ?? '',
    expectDeadline: expect?.deadlineSeconds !== undefined ? String(expect.deadlineSeconds) : '',
    expectValue: expect?.value ?? '',
  }
}

function resolumeValueFromTarget(target: ConfigShowActionTarget): ResolumeTargetValue {
  const ref: Record<string, string> = {}
  for (const [key, value] of Object.entries(target.ref ?? {})) ref[key] = String(value)
  return { action: target.action ?? '', ref }
}

function audioValueFromTarget(target: ConfigShowActionTarget): AudioTargetValue {
  const audioAction = target.audioAction ?? ''
  const gainKey = audioAction === 'audio.gain.set' ? 'gainDb' : audioAction === 'audio.gain.fade' ? 'targetGainDb' : null
  const gain = gainKey === null ? undefined : target.params?.[gainKey]
  return { audioNodeId: target.audioNodeId ?? '', audioSessionId: target.audioSessionId ?? '', audioAction, gainDb: gain === undefined ? '' : String(gain) }
}

type TargetBuild = { target: ConfigShowActionTarget; blockReason: string | null }

function buildFppTarget(value: FppTargetValue): TargetBuild {
  if (value.instanceId === '') return { target: { integration: 'fpp', instanceId: '', primitive: value.primitive }, blockReason: 'An FPP instance is required.' }
  if (value.primitive === '') return { target: { integration: 'fpp', instanceId: value.instanceId, primitive: '' }, blockReason: 'An FPP primitive is required.' }
  const defs = FPP_PRIMITIVE_PARAMS[value.primitive] ?? []
  const params: Record<string, unknown> = {}
  let blockReason: string | null = null
  for (const def of defs) {
    const raw = (value.params[def.name] ?? '').trim()
    if (def.kind === 'bool') {
      params[def.name] = raw === 'true'
      continue
    }
    if (raw === '') {
      if (def.required) blockReason ??= `${def.label} is required.`
      continue
    }
    if (def.kind === 'int') {
      const n = Number(raw)
      if (!Number.isInteger(n)) blockReason ??= `${def.label} must be a whole number.`
      else if (def.min !== undefined && (n < def.min || (def.max !== undefined && n > def.max))) blockReason ??= `${def.label} must be ${def.min} to ${def.max}.`
      else params[def.name] = n
      continue
    }
    params[def.name] = raw
  }
  const target: ConfigShowActionTarget = { integration: 'fpp', instanceId: value.instanceId, primitive: value.primitive, ...(Object.keys(params).length > 0 ? { params } : {}) }
  return { target, blockReason }
}

function buildMqttTarget(value: MqttTargetValue): TargetBuild {
  let blockReason: string | null = null
  if (value.broker.trim() === '') blockReason ??= 'A broker is required.'
  if (value.topic.trim() === '') blockReason ??= 'A publish topic is required.'
  let expect: ConfigShowActionMQTTExpect
  if (value.expectKind === 'none') {
    expect = { kind: 'none' }
  } else {
    if (value.expectTopic.trim() === '') blockReason ??= 'Confirmation needs a topic.'
    const deadline = Number(value.expectDeadline)
    const deadlineOk = value.expectDeadline.trim() !== '' && Number.isInteger(deadline) && deadline >= 1 && deadline <= 120
    if (!deadlineOk) blockReason ??= 'Deadline must be a whole number of seconds, 1 to 120.'
    if (value.expectKind === 'match') {
      expect = { kind: 'match', topic: value.expectTopic.trim(), deadlineSeconds: deadlineOk ? deadline : 0, value: value.expectValue }
    } else if (value.expectKind === 'number') {
      expect = {
        kind: 'number',
        topic: value.expectTopic.trim(),
        deadlineSeconds: deadlineOk ? deadline : 0,
        ...(value.expectValue.trim() !== '' ? { value: value.expectValue.trim() } : {}),
      }
    } else {
      expect = { kind: value.expectKind, topic: value.expectTopic.trim(), deadlineSeconds: deadlineOk ? deadline : 0 }
    }
  }
  const target: ConfigShowActionTarget = {
    integration: 'mqtt',
    broker: value.broker.trim(),
    publish: { topic: value.topic.trim(), payload: value.payload, qos: Number(value.qos) as 0 | 1 | 2, retain: value.retain },
    expect,
  }
  return { target, blockReason }
}

type ResolumeActionsState = { kind: 'loading' } | { kind: 'loaded'; actions: ResolumeAction[] } | { kind: 'failed'; reason: string }
type AudioNodesState = { kind: 'loading' } | { kind: 'loaded'; nodes: ConfigObjectSummary[] } | { kind: 'failed'; reason: string }

function buildResolumeTarget(value: ResolumeTargetValue, actionsState: ResolumeActionsState): TargetBuild {
  const base: ConfigShowActionTarget = { integration: 'resolume', action: value.action, ref: {} }
  if (actionsState.kind === 'loading') return { target: base, blockReason: 'Reading the Resolume action vocabulary.' }
  if (actionsState.kind === 'failed') return { target: base, blockReason: `Could not read the Resolume action vocabulary: ${actionsState.reason}` }
  if (value.action === '') return { target: base, blockReason: 'A Resolume action is required.' }
  const def = actionsState.actions.find((a) => a.name === value.action)
  if (def === undefined) return { target: base, blockReason: 'Unrecognized Resolume action.' }
  const ref: Record<string, unknown> = {}
  let blockReason: string | null = null
  for (const param of def.params) {
    const raw = (value.ref[param.name] ?? '').trim()
    if (param.kind === 'bool') {
      ref[param.name] = raw === 'true'
      continue
    }
    if (raw === '') {
      if (param.required) blockReason ??= `${param.name} is required.`
      continue
    }
    if (param.kind === 'number') {
      const n = Number(raw)
      if (!Number.isFinite(n)) blockReason ??= `${param.name} must be a number.`
      else ref[param.name] = n
      continue
    }
    ref[param.name] = raw
  }
  return { target: { integration: 'resolume', action: value.action, ref }, blockReason }
}

function buildAudioTarget(value: AudioTargetValue): TargetBuild {
  let blockReason: string | null = null
  if (value.audioNodeId === '') blockReason ??= 'An audio node is required.'
  if (value.audioSessionId.trim() === '') blockReason ??= 'A session id is required.'
  if (value.audioAction === '') blockReason ??= 'An audio operation is required.'
  let params: Record<string, unknown> | undefined
  if (value.audioAction === 'audio.gain.set' || value.audioAction === 'audio.gain.fade') {
    const key = value.audioAction === 'audio.gain.set' ? 'gainDb' : 'targetGainDb'
    const raw = value.gainDb.trim()
    if (raw === '') {
      blockReason ??= 'A decibel value is required.'
    } else {
      const n = Number(raw)
      if (!Number.isFinite(n)) blockReason ??= 'Gain must be a number, in decibels.'
      else if (n > 12) blockReason ??= 'Gain must not exceed 12 dB, the accepted ceiling.'
      else params = { [key]: n }
    }
  }
  const target: ConfigShowActionTarget = {
    integration: 'audio',
    audioNodeId: value.audioNodeId,
    audioSessionId: value.audioSessionId.trim(),
    audioAction: value.audioAction,
    ...(params !== undefined ? { params } : {}),
  }
  return { target, blockReason }
}

function FppParamField({ def, value, onChange }: { def: FppParamDef; value: string; onChange: (value: string) => void }) {
  if (def.kind === 'bool') {
    return <Choice type="checkbox" label={def.label} checked={value === 'true'} onChange={(e) => onChange(e.target.checked ? 'true' : 'false')} />
  }
  if (def.kind === 'enum') {
    return (
      <Field label={def.label}>
        {(props) => (
          <Select {...props} value={value} onChange={(e) => onChange(e.target.value)}>
            {(def.options ?? []).map((o) => (
              <option key={o} value={o}>
                {o}
              </option>
            ))}
          </Select>
        )}
      </Field>
    )
  }
  return (
    <Field label={def.label} help={def.help}>
      {(props) => (
        <Input {...props} className="sm-data" type={def.kind === 'int' ? 'number' : 'text'} min={def.min} max={def.max} value={value} onChange={(e) => onChange(e.target.value)} />
      )}
    </Field>
  )
}

function FppTarget({ value, onChange, fppInstances }: { value: FppTargetValue; onChange: (value: FppTargetValue) => void; fppInstances: readonly { instanceId: string }[] }) {
  const defs = FPP_PRIMITIVE_PARAMS[value.primitive] ?? []
  return (
    <div className="sm-inspector__group">
      <p className="sm-eyebrow sm-flat">fpp target</p>
      {fppInstances.length === 0 ? (
        <RuledStrip absence="empty" label="None" fact="No FPP instance is configured." />
      ) : (
        <Field label="Instance">
          {(props) => (
            <Select {...props} value={value.instanceId} onChange={(e) => onChange({ ...value, instanceId: e.target.value })}>
              <option value="">Select an instance</option>
              {fppInstances.map((instance) => (
                <option key={instance.instanceId} value={instance.instanceId}>
                  {instance.instanceId}
                </option>
              ))}
            </Select>
          )}
        </Field>
      )}
      <Field label="Primitive">
        {(props) => (
          <Select
            {...props}
            value={value.primitive}
            onChange={(e) => onChange({ instanceId: value.instanceId, primitive: e.target.value, params: defaultFppParamValues(e.target.value) })}
          >
            <option value="">Select a primitive</option>
            {FPP_PRIMITIVE_OPTIONS.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        )}
      </Field>
      {defs.map((def) => (
        <FppParamField key={def.name} def={def} value={value.params[def.name] ?? def.default} onChange={(v) => onChange({ ...value, params: { ...value.params, [def.name]: v } })} />
      ))}
      {defs.length === 0 && value.primitive !== '' && <p className="sm-small sm-faint">This primitive takes no parameters.</p>}
    </div>
  )
}

function MqttTarget({ value, onChange }: { value: MqttTargetValue; onChange: (value: MqttTargetValue) => void }) {
  return (
    <div className="sm-inspector__group">
      <p className="sm-eyebrow sm-flat">mqtt target</p>
      <Field label="Broker" help="Must name a broker this deployment declares.">
        {(props) => <Input {...props} className="sm-data" value={value.broker} onChange={(e) => onChange({ ...value, broker: e.target.value })} />}
      </Field>
      <Field label="Topic">
        {(props) => <Input {...props} className="sm-data" value={value.topic} onChange={(e) => onChange({ ...value, topic: e.target.value })} />}
      </Field>
      <Field label="Payload" help="Passed through undecoded.">
        {(props) => <Textarea {...props} className="sm-data" rows={2} value={value.payload} onChange={(e) => onChange({ ...value, payload: e.target.value })} />}
      </Field>
      <div className="sm-inspector__group">
        <Field label="QoS">
          {(props) => (
            <Select {...props} value={value.qos} onChange={(e) => onChange({ ...value, qos: e.target.value as MqttTargetValue['qos'] })}>
              <option value="0">0</option>
              <option value="1">1</option>
              <option value="2">2</option>
            </Select>
          )}
        </Field>
        <Choice type="checkbox" label="Retain" checked={value.retain} onChange={(e) => onChange({ ...value, retain: e.target.checked })} />
      </div>
      <Field label="Confirmation">
        {(props) => (
          <Select {...props} value={value.expectKind} onChange={(e) => onChange({ ...value, expectKind: e.target.value as ConfigShowActionMQTTExpect['kind'] })}>
            <option value="none">none</option>
            <option value="boolean">boolean</option>
            <option value="number">number</option>
            <option value="text">text</option>
            <option value="match">match</option>
          </Select>
        )}
      </Field>
      {value.expectKind !== 'none' && (
        <>
          <Field label="Confirmation topic">
            {(props) => <Input {...props} className="sm-data" value={value.expectTopic} onChange={(e) => onChange({ ...value, expectTopic: e.target.value })} />}
          </Field>
          <Field label="Deadline" help="Seconds. 1 to 120.">
            {(props) => <Input {...props} type="number" min={1} max={120} value={value.expectDeadline} onChange={(e) => onChange({ ...value, expectDeadline: e.target.value })} />}
          </Field>
        </>
      )}
      {(value.expectKind === 'number' || value.expectKind === 'match') && (
        <Field
          label="Match value"
          help={
            value.expectKind === 'match'
              ? 'Must be present. The empty string is a legal value: an empty box here is answered, not blank.'
              : 'Optional. An absent value accepts receipt with no equality check.'
          }
        >
          {(props) => <Input {...props} className="sm-data" value={value.expectValue} onChange={(e) => onChange({ ...value, expectValue: e.target.value })} />}
        </Field>
      )}
    </div>
  )
}

function ResolumeTarget({
  value,
  onChange,
  actionsState,
}: {
  value: ResolumeTargetValue
  onChange: (value: ResolumeTargetValue) => void
  actionsState: ResolumeActionsState
}) {
  if (actionsState.kind === 'loading') {
    return (
      <div className="sm-inspector__group">
        <p className="sm-eyebrow sm-flat">resolume target</p>
        <RuledStrip absence="loading" label="Reading" fact="Fetching this deployment's Resolume action vocabulary." />
      </div>
    )
  }
  if (actionsState.kind === 'failed') {
    return (
      <div className="sm-inspector__group">
        <p className="sm-eyebrow sm-flat">resolume target</p>
        <RuledStrip absence="failed" label="Read failed" fact={actionsState.reason} detail="The save stays withheld; there is no fallback to a guessed action list." />
      </div>
    )
  }
  const selected = actionsState.actions.find((a) => a.name === value.action) ?? null
  return (
    <div className="sm-inspector__group">
      <p className="sm-eyebrow sm-flat">resolume target</p>
      <Field label="Action">
        {(props) => (
          <Select {...props} value={value.action} onChange={(e) => onChange({ action: e.target.value, ref: {} })}>
            <option value="">Select an action</option>
            {actionsState.actions.map((a) => (
              <option key={a.name} value={a.name}>
                {a.name}
              </option>
            ))}
          </Select>
        )}
      </Field>
      {selected !== null &&
        (selected.params.length === 0 ? (
          <p className="sm-small sm-faint">This action names no reference: nothing else to fill in.</p>
        ) : (
          selected.params.map((param) =>
            param.kind === 'bool' ? (
              <Choice
                key={param.name}
                type="checkbox"
                label={param.name}
                checked={(value.ref[param.name] ?? '') === 'true'}
                onChange={(e) => onChange({ ...value, ref: { ...value.ref, [param.name]: e.target.checked ? 'true' : 'false' } })}
              />
            ) : (
              <Field key={param.name} label={param.name} help={param.required ? undefined : 'Optional.'}>
                {(props) => (
                  <Input
                    {...props}
                    className="sm-data"
                    type={param.kind === 'number' ? 'number' : 'text'}
                    value={value.ref[param.name] ?? ''}
                    onChange={(e) => onChange({ ...value, ref: { ...value.ref, [param.name]: e.target.value } })}
                  />
                )}
              </Field>
            ),
          )
        ))}
    </div>
  )
}

function AudioTarget({ value, onChange, nodesState }: { value: AudioTargetValue; onChange: (value: AudioTargetValue) => void; nodesState: AudioNodesState }) {
  return (
    <div className="sm-inspector__group">
      <p className="sm-eyebrow sm-flat">audio target</p>
      {nodesState.kind === 'loading' && <RuledStrip absence="loading" label="Reading" fact="Fetching this deployment's declared audio nodes." />}
      {nodesState.kind === 'failed' && <RuledStrip absence="failed" label="Read failed" fact={nodesState.reason} />}
      {nodesState.kind === 'loaded' &&
        (nodesState.nodes.length === 0 ? (
          <RuledStrip absence="empty" label="None" fact="No audio node is declared." />
        ) : (
          <Field label="Audio node" help="Declared nodes only. Undeclared ones are listed in Settings › Node routing.">
            {(props) => (
              <Select {...props} value={value.audioNodeId} onChange={(e) => onChange({ ...value, audioNodeId: e.target.value })}>
                <option value="">Select an audio node</option>
                {nodesState.nodes.map((node) => (
                  <option key={node.id} value={node.id}>
                    {node.label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        ))}
      <Field label="Session" help="Minted by the caller, not looked up: the pkg/audio session id this operation targets.">
        {(props) => <Input {...props} className="sm-data" value={value.audioSessionId} onChange={(e) => onChange({ ...value, audioSessionId: e.target.value })} />}
      </Field>
      <Field label="Operation">
        {(props) => (
          <Select {...props} value={value.audioAction} onChange={(e) => onChange({ ...value, audioAction: e.target.value })}>
            <option value="">Select an operation</option>
            {AUDIO_ACTION_OPTIONS.map((a) => (
              <option key={a} value={a}>
                {a}
              </option>
            ))}
          </Select>
        )}
      </Field>
      {(value.audioAction === 'audio.gain.set' || value.audioAction === 'audio.gain.fade') && (
        <Field
          label={value.audioAction === 'audio.gain.set' ? 'Gain' : 'Target gain'}
          help="Decibels only. 0 dB unity, -60 dB silence, +12 dB the ceiling. The pre-decibel linear gain is refused at authoring time."
        >
          {(props) => (
            <div className="sm-inline-row">
              <Input {...props} className="sm-data" type="number" max={12} value={value.gainDb} onChange={(e) => onChange({ ...value, gainDb: e.target.value })} />
              <span className="sm-data sm-small sm-muted">dB</span>
            </div>
          )}
        </Field>
      )}
      <p className="sm-small sm-faint">No expect block: confirmation for audio is the node's own reply, not an authored match.</p>
    </div>
  )
}

/** The chosen operation that decided a derived safety class, so the note beside the disabled Segmented names it. */
function decidingOperation(integration: ConfigShowActionTarget['integration'] | '', fpp: FppTargetValue, resolume: ResolumeTargetValue, audio: AudioTargetValue): string {
  if (integration === 'fpp') return fpp.primitive
  if (integration === 'resolume') return resolume.action
  if (integration === 'audio') return audio.audioAction
  return ''
}

function SafetyClassBlock({
  integration,
  derivedClass,
  decidedBy,
  mqttValue,
  onMqttChange,
}: {
  integration: ConfigShowActionTarget['integration'] | ''
  derivedClass: SafetyClass | null
  decidedBy: string
  mqttValue: SafetyClass | ''
  onMqttChange: (value: SafetyClass | '') => void
}) {
  return (
    <div className="sm-inspector__group">
      <h3 className="sm-subsection__title">Safety class</h3>
      {integration === 'mqtt' ? (
        <>
          <Segmented<SafetyClass | ''> label="Safety class" value={mqttValue} options={SAFETY_CLASS_OPTIONS} onChange={onMqttChange} />
          <p className="sm-small sm-muted">
            No default in the schema, so no default here. It decides which permission invoking this action needs and whether an interlock can withhold it. Stored as{' '}
            <span className="sm-data">none</span>, <span className="sm-data">blackout</span>, <span className="sm-data">stop</span>, or <span className="sm-data">powerOff</span>.
          </p>
        </>
      ) : derivedClass !== null ? (
        <>
          <Segmented<SafetyClass> label="Safety class" value={derivedClass} options={SAFETY_CLASS_OPTIONS} onChange={() => {}} disabled />
          <p className="sm-small sm-muted">
            Derived from <span className="sm-data">{decidedBy}</span>. Not settable directly: the coordinator refuses a safetyClass that disagrees with the target's own
            registered class.
          </p>
        </>
      ) : (
        <p className="sm-small sm-faint">Choose the target above first; the safety class follows from it.</p>
      )}
    </div>
  )
}

function ActionDraft({
  showId,
  model,
  canAuthor,
  onCreated,
  onCancel,
}: {
  showId: string
  model: ReturnType<typeof useModelContext>
  canAuthor: { allowed: boolean; reason?: string }
  onCreated: (response: ShowActionConfigResponse) => void
  onCancel: () => void
}) {
  const [label, setLabel] = useState('')
  const [id, setId] = useState('')
  const [idTouched, setIdTouched] = useState(false)
  const [integration, setIntegration] = useState<ConfigShowActionTarget['integration'] | ''>('')
  const [fppValue, setFppValue] = useState<FppTargetValue>(emptyFppValue())
  const [mqttValue, setMqttValue] = useState<MqttTargetValue>(emptyMqttValue())
  const [resolumeValue, setResolumeValue] = useState<ResolumeTargetValue>(emptyResolumeValue())
  const [audioValue, setAudioValue] = useState<AudioTargetValue>(emptyAudioValue())
  const [mqttSafetyClass, setMqttSafetyClass] = useState<SafetyClass | ''>('')
  const [resolumeActions, setResolumeActions] = useState<ResolumeActionsState>({ kind: 'loading' })
  const [audioNodes, setAudioNodes] = useState<AudioNodesState>({ kind: 'loading' })
  const [creating, setCreating] = useState(false)
  const [taken, setTaken] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  useEffect(() => {
    if (integration !== 'resolume') return
    let cancelled = false
    setResolumeActions({ kind: 'loading' })
    listResolumeActions()
      .then((response) => {
        if (!cancelled) setResolumeActions({ kind: 'loaded', actions: response.actions })
      })
      .catch((err: unknown) => {
        if (!cancelled) setResolumeActions({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [integration])

  useEffect(() => {
    if (integration !== 'audio') return
    let cancelled = false
    setAudioNodes({ kind: 'loading' })
    listConfigObjects('audio.node')
      .then((response) => {
        if (!cancelled) setAudioNodes({ kind: 'loaded', nodes: response.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setAudioNodes({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [integration])

  const onLabelChange = (value: string) => {
    setLabel(value)
    if (!idTouched) setId(slugify(value))
  }
  const onIdChange = (value: string) => {
    setId(value)
    setIdTouched(true)
  }

  let target: ConfigShowActionTarget | null = null
  let targetBlockReason: string | null = null
  let derivedClass: SafetyClass | null = null

  if (integration === 'fpp') {
    const built = buildFppTarget(fppValue)
    target = built.target
    targetBlockReason = built.blockReason
    if (fppValue.primitive !== '') derivedClass = fppDerivedSafetyClass(fppValue.primitive)
  } else if (integration === 'mqtt') {
    const built = buildMqttTarget(mqttValue)
    target = built.target
    targetBlockReason = built.blockReason
  } else if (integration === 'resolume') {
    const built = buildResolumeTarget(resolumeValue, resolumeActions)
    target = built.target
    targetBlockReason = built.blockReason
    if (resolumeValue.action !== '') derivedClass = resolumeDerivedSafetyClass(resolumeValue.action)
  } else if (integration === 'audio') {
    const built = buildAudioTarget(audioValue)
    target = built.target
    targetBlockReason = built.blockReason
    if (audioValue.audioAction !== '') derivedClass = audioDerivedSafetyClass(audioValue.audioAction)
  }

  const safetyClass: SafetyClass | null = integration === 'mqtt' ? (mqttSafetyClass === '' ? null : mqttSafetyClass) : derivedClass

  let blockReason: string | null = null
  if (label.trim() === '') blockReason = 'A label is required.'
  else if (id.trim() === '') blockReason = 'An id is required.'
  else if (integration === '') blockReason = 'An integration is required.'
  else if (targetBlockReason !== null) blockReason = targetBlockReason
  else if (safetyClass === null) blockReason = 'Safety class required'

  const create = () => {
    if (blockReason !== null || target === null || integration === '') return
    const payload: ConfigShowAction = { show: showId, label: label.trim(), description: '', safetyClass: safetyClass as SafetyClass, target }
    setCreating(true)
    setTaken(false)
    setCreateError(null)
    guardedCreate({
      read: () => getShowAction(id),
      write: () => putShowAction(id, payload),
    })
      .then((outcome) => {
        if (outcome.kind === 'taken') {
          setTaken(true)
          return
        }
        if (outcome.kind === 'unreadable') {
          setCreateError(outcome.reason)
          return
        }
        onCreated(outcome.response)
      })
      .catch((err: unknown) => setCreateError(describeApiError(err)))
      .finally(() => setCreating(false))
  }

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow sm-eyebrow--accent">Draft · new action</p>

      <div className="sm-inspector__group">
        <Field
          label="Label"
          help={
            <>
              What a step row and a run row read. {id === '' ? 'The id comes from the label.' : <>Id <span className="sm-data">{id}</span>, from the label, editable until created.</>}
            </>
          }
        >
          {(props) => <Input {...props} value={label} onChange={(e) => onLabelChange(e.target.value)} />}
        </Field>
        <Field label="Id">{(props) => <Input {...props} className="sm-data" value={id} onChange={(e) => onIdChange(e.target.value)} />}</Field>
      </div>

      <div className="sm-inspector__group">
        <h3 className="sm-subsection__title">Integration</h3>
        <Segmented<ConfigShowActionTarget['integration'] | ''>
          label="Integration"
          value={integration}
          options={ACTION_INTEGRATION_OPTIONS}
          onChange={(value) => {
            setIntegration(value)
            setMqttSafetyClass('')
          }}
        />
        <p className="sm-small sm-faint">
          Stored as <span className="sm-data">fpp</span>, <span className="sm-data">mqtt</span>, <span className="sm-data">resolume</span>, or{' '}
          <span className="sm-data">audio</span>. Immutable once created. Changing where an action points is a new action, not an edit.
        </p>
      </div>

      {integration === 'fpp' && <FppTarget value={fppValue} onChange={setFppValue} fppInstances={model.fpp} />}
      {integration === 'mqtt' && <MqttTarget value={mqttValue} onChange={setMqttValue} />}
      {integration === 'resolume' && <ResolumeTarget value={resolumeValue} onChange={setResolumeValue} actionsState={resolumeActions} />}
      {integration === 'audio' && <AudioTarget value={audioValue} onChange={setAudioValue} nodesState={audioNodes} />}

      {integration !== '' && (
        <SafetyClassBlock
          integration={integration}
          derivedClass={derivedClass}
          decidedBy={decidingOperation(integration, fppValue, resolumeValue, audioValue)}
          mqttValue={mqttSafetyClass}
          onMqttChange={setMqttSafetyClass}
        />
      )}

      {taken && (
        <RuledStrip
          absence="failed"
          label="Id taken"
          fact={<span className="sm-data">{id}</span>}
          detail="Already names an action in this show."
        />
      )}
      {createError !== null && <RuledStrip absence="failed" label="Save failed" fact={createError} />}

      <div className="sm-inspector__actions">
        <span className="sm-small sm-muted">
          {blockReason ?? (
            <>
              Creates <span className="sm-data">{id}</span>
            </>
          )}
        </span>
        <div className="sm-btn-row">
          <Button variant="quiet" onClick={onCancel} disabled={creating}>
            Discard
          </Button>
          <Button variant="primary" onClick={create} disabled={creating || blockReason !== null || !canAuthor.allowed} title={!canAuthor.allowed ? canAuthor.reason : (blockReason ?? undefined)}>
            {creating ? 'Creating…' : 'Create action'}
          </Button>
        </div>
      </div>
    </div>
  )
}

function ActionEditor({
  action,
  model,
  canAuthor,
  binding,
  macroUsages,
  onSaved,
  onCancel,
  onReloadAfterStale,
}: {
  action: ShowActionConfigResponse
  model: ReturnType<typeof useModelContext>
  canAuthor: { allowed: boolean; reason?: string }
  binding: ActionBinding | undefined
  macroUsages: readonly { label: string; stepNumbers: number[] }[]
  onSaved: (response: ShowActionConfigResponse) => void
  onCancel: () => void
  onReloadAfterStale: () => void
}) {
  const integration = action.payload.target.integration
  const [label, setLabel] = useState(action.payload.label)
  const [fppValue, setFppValue] = useState<FppTargetValue>(() => (integration === 'fpp' ? fppValueFromTarget(action.payload.target) : emptyFppValue()))
  const [mqttValue, setMqttValue] = useState<MqttTargetValue>(() => (integration === 'mqtt' ? mqttValueFromTarget(action.payload.target) : emptyMqttValue()))
  const [resolumeValue, setResolumeValue] = useState<ResolumeTargetValue>(() => (integration === 'resolume' ? resolumeValueFromTarget(action.payload.target) : emptyResolumeValue()))
  const [audioValue, setAudioValue] = useState<AudioTargetValue>(() => (integration === 'audio' ? audioValueFromTarget(action.payload.target) : emptyAudioValue()))
  const [mqttSafetyClass, setMqttSafetyClass] = useState<SafetyClass | ''>(integration === 'mqtt' ? action.payload.safetyClass : '')
  const [resolumeActions, setResolumeActions] = useState<ResolumeActionsState>({ kind: 'loading' })
  const [audioNodes, setAudioNodes] = useState<AudioNodesState>({ kind: 'loading' })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [stale, setStale] = useState<Extract<SaveOutcome<ShowActionConfigResponse>, { kind: 'stale' }> | null>(null)

  useEffect(() => {
    if (integration !== 'resolume') return
    let cancelled = false
    setResolumeActions({ kind: 'loading' })
    listResolumeActions()
      .then((response) => {
        if (!cancelled) setResolumeActions({ kind: 'loaded', actions: response.actions })
      })
      .catch((err: unknown) => {
        if (!cancelled) setResolumeActions({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [integration])

  useEffect(() => {
    if (integration !== 'audio') return
    let cancelled = false
    setAudioNodes({ kind: 'loading' })
    listConfigObjects('audio.node')
      .then((response) => {
        if (!cancelled) setAudioNodes({ kind: 'loaded', nodes: response.objects })
      })
      .catch((err: unknown) => {
        if (!cancelled) setAudioNodes({ kind: 'failed', reason: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [integration])

  let target: ConfigShowActionTarget | null = null
  let targetBlockReason: string | null = null
  let derivedClass: SafetyClass | null = null

  if (integration === 'fpp') {
    const built = buildFppTarget(fppValue)
    target = built.target
    targetBlockReason = built.blockReason
    if (fppValue.primitive !== '') derivedClass = fppDerivedSafetyClass(fppValue.primitive)
  } else if (integration === 'mqtt') {
    const built = buildMqttTarget(mqttValue)
    target = built.target
    targetBlockReason = built.blockReason
  } else if (integration === 'resolume') {
    const built = buildResolumeTarget(resolumeValue, resolumeActions)
    target = built.target
    targetBlockReason = built.blockReason
    if (resolumeValue.action !== '') derivedClass = resolumeDerivedSafetyClass(resolumeValue.action)
  } else {
    const built = buildAudioTarget(audioValue)
    target = built.target
    targetBlockReason = built.blockReason
    if (audioValue.audioAction !== '') derivedClass = audioDerivedSafetyClass(audioValue.audioAction)
  }

  const safetyClass: SafetyClass | null = integration === 'mqtt' ? (mqttSafetyClass === '' ? null : mqttSafetyClass) : derivedClass

  let blockReason: string | null = null
  if (label.trim() === '') blockReason = 'A label is required.'
  else if (targetBlockReason !== null) blockReason = targetBlockReason
  else if (safetyClass === null) blockReason = 'Safety class required'

  const save = () => {
    if (blockReason !== null || target === null) return
    const payload: ConfigShowAction = { show: action.payload.show, label: label.trim(), description: action.payload.description, safetyClass: safetyClass as SafetyClass, target }
    setSaving(true)
    setSaveError(null)
    setStale(null)
    guardedSave({
      loaded: action,
      read: () => getShowAction(action.id),
      write: () => putShowAction(action.id, payload),
    })
      .then((outcome) => {
        if (outcome.kind === 'saved') {
          onSaved(outcome.response)
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

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow sm-eyebrow--accent">Editing · {action.payload.label}</p>

      {binding !== undefined && binding.state !== 'ok' && (
        <div className="sm-attn">
          <StatusPair tone={bindingTone(binding.state)} label={binding.state === 'broken' ? 'Binding broken' : 'Binding unknown'} />
          <div>
            <p className="sm-attn__fact">{binding.reason}</p>
            <p className="sm-attn__detail">Editing is not blocked; invoke stays closed until it resolves.</p>
          </div>
        </div>
      )}

      <div className="sm-inspector__group">
        <Field label="Label">{(props) => <Input {...props} value={label} onChange={(e) => setLabel(e.target.value)} />}</Field>
        <div className="sm-grid sm-grid--auto">
          <div className="sm-field">
            <span className="sm-field__label">Id</span>
            <p className="sm-data">{action.id}</p>
          </div>
          <div className="sm-field">
            <span className="sm-field__label">Integration</span>
            <p className="sm-data">{integration}</p>
          </div>
        </div>
        <p className="sm-small sm-faint">Both immutable. Pointing this somewhere else means a new action and a step change.</p>
      </div>

      {integration === 'fpp' && <FppTarget value={fppValue} onChange={setFppValue} fppInstances={model.fpp} />}
      {integration === 'mqtt' && <MqttTarget value={mqttValue} onChange={setMqttValue} />}
      {integration === 'resolume' && <ResolumeTarget value={resolumeValue} onChange={setResolumeValue} actionsState={resolumeActions} />}
      {integration === 'audio' && <AudioTarget value={audioValue} onChange={setAudioValue} nodesState={audioNodes} />}

      <SafetyClassBlock
        integration={integration}
        derivedClass={derivedClass}
        decidedBy={decidingOperation(integration, fppValue, resolumeValue, audioValue)}
        mqttValue={mqttSafetyClass}
        onMqttChange={setMqttSafetyClass}
      />

      {macroUsages.length === 0 ? (
        <p className="sm-small sm-muted">Not used by any macro in this show.</p>
      ) : (
        <div className="sm-attn">
          <div>
            <p className="sm-attn__fact">
              Used by{' '}
              {macroUsages.map((usage, index) => (
                <span key={usage.label}>
                  {index > 0 && '; '}
                  <strong>{usage.label}</strong> step{usage.stepNumbers.length > 1 ? 's' : ''} <span className="sm-data">{usage.stepNumbers.join(', ')}</span>
                </span>
              ))}
              .
            </p>
            <p className="sm-attn__detail">Saving changes what that macro does tonight; it does not change the macro's own revision.</p>
          </div>
        </div>
      )}

      <div className="sm-inspector__actions">
        <span className="sm-small sm-muted">
          {blockReason ?? (
            <>
              Creates revision <span className="sm-data">{action.revision + 1}</span>
            </>
          )}
        </span>
        <div className="sm-btn-row">
          <Button variant="quiet" onClick={onCancel} disabled={saving}>
            Discard
          </Button>
          <Button variant="primary" onClick={save} disabled={saving || blockReason !== null || !canAuthor.allowed} title={!canAuthor.allowed ? canAuthor.reason : (blockReason ?? undefined)}>
            {saving ? 'Saving…' : 'Save action'}
          </Button>
        </div>
      </div>
      {stale !== null && <StaleWriteStrip stale={stale} onReload={onReloadAfterStale} />}
      {saveError !== null && <RuledStrip absence="failed" label="Save failed" fact={saveError} />}
      <RevisionHistory fetch={() => getShowActionRevisions(action.id)} reloadKey={`${action.id}:${action.revision}`} />
    </div>
  )
}

function MacroDraft({
  showId,
  actions,
  model,
  canAuthor,
  onCreated,
  onCancel,
}: {
  showId: string
  actions: readonly ShowActionConfigResponse[]
  model: ReturnType<typeof useModelContext>
  canAuthor: { allowed: boolean; reason?: string }
  onCreated: (response: ShowMacroConfigResponse) => void
  onCancel: () => void
}) {
  const [label, setLabel] = useState('')
  const [id, setId] = useState('')
  const [idTouched, setIdTouched] = useState(false)
  const [description, setDescription] = useState('')
  const [localActions, setLocalActions] = useState<ShowActionConfigResponse[]>([])
  const [creatingAction, setCreatingAction] = useState(false)
  const [actionId, setActionId] = useState('')
  const [stepId, setStepId] = useState('')
  const [fallbackClass, setFallbackClass] = useState<ConfigShowMacroLocalFallback['class']>('none')
  const [fallbackReason, setFallbackReason] = useState('')
  const [creating, setCreating] = useState(false)
  const [taken, setTaken] = useState(false)
  const [createError, setCreateError] = useState<string | null>(null)

  if (creatingAction) {
    return (
      <ActionDraft
        showId={showId}
        model={model}
        canAuthor={canAuthor}
        onCreated={(response) => {
          setLocalActions((prev) => [...prev, response])
          setActionId(response.id)
          setCreatingAction(false)
        }}
        onCancel={() => setCreatingAction(false)}
      />
    )
  }

  const availableActions = (() => {
    const byId = new Map(actions.map((a) => [a.id, a]))
    for (const a of localActions) byId.set(a.id, a)
    return Array.from(byId.values())
  })()

  const onLabelChange = (value: string) => {
    setLabel(value)
    if (!idTouched) setId(slugify(value))
  }
  const onIdChange = (value: string) => {
    setId(value)
    setIdTouched(true)
  }

  let blockReason: string | null = null
  if (label.trim() === '') blockReason = 'A label is required.'
  else if (id.trim() === '') blockReason = 'An id is required.'
  else if (actionId === '') blockReason = 'A first step needs an action.'
  else if (stepId.trim() === '') blockReason = 'A first step needs an id.'
  else if (fallbackReason.trim() === '') blockReason = 'Fallback reason required'

  const create = () => {
    if (blockReason !== null) return
    const payload: ConfigShowMacro = {
      show: showId,
      label: label.trim(),
      description,
      steps: [{ id: stepId.trim(), action: actionId, onFailure: 'continue', onUnconfirmed: 'continue', localFallback: { class: fallbackClass, reason: fallbackReason.trim() } }],
    }
    setCreating(true)
    setTaken(false)
    setCreateError(null)
    guardedCreate({
      read: () => getShowMacro(id),
      write: () => putShowMacro(id, payload),
    })
      .then((outcome) => {
        if (outcome.kind === 'taken') {
          setTaken(true)
          return
        }
        if (outcome.kind === 'unreadable') {
          setCreateError(outcome.reason)
          return
        }
        onCreated(outcome.response)
      })
      .catch((err: unknown) => setCreateError(describeApiError(err)))
      .finally(() => setCreating(false))
  }

  return (
    <div className="sm-inspector">
      <p className="sm-eyebrow sm-eyebrow--accent">Draft · new macro</p>

      <div className="sm-inspector__group">
        <Field label="Label">{(props) => <Input {...props} value={label} onChange={(e) => onLabelChange(e.target.value)} />}</Field>
        <Field label="Id" help="Immutable once created. Interlocks and night session cues name macros by id.">
          {(props) => <Input {...props} className="sm-data" value={id} onChange={(e) => onIdChange(e.target.value)} />}
        </Field>
        <Field label="Description · optional" help="One line, read above the step list.">
          {(props) => <Input {...props} value={description} onChange={(e) => setDescription(e.target.value)} />}
        </Field>
      </div>

      <div className="sm-inspector__group">
        <p className="sm-eyebrow sm-flat">Step 1</p>
        <Field
          label="Action"
          help={
            <>
              This show only.{' '}
              <button type="button" className="sm-linkbutton" onClick={() => setCreatingAction(true)}>
                New action
              </button>{' '}
              if the one you need does not exist yet, the draft is kept.
            </>
          }
        >
          {(props) => (
            <Select {...props} value={actionId} onChange={(e) => setActionId(e.target.value)}>
              <option value="">Select an action</option>
              {availableActions.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.payload.label} · {a.payload.target.integration}
                </option>
              ))}
            </Select>
          )}
        </Field>
        <Field label="Step id" help="Unique within this macro. Run rows are keyed by it.">
          {(props) => <Input {...props} className="sm-data" value={stepId} onChange={(e) => setStepId(e.target.value)} />}
        </Field>
        <Field label="Fallback class">
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
        <Field label="Fallback reason" help="Required and non-empty even when the class is none. It is what an operator reads while the coordinator is unreachable.">
          {(props) => <Input {...props} value={fallbackReason} onChange={(e) => setFallbackReason(e.target.value)} />}
        </Field>
        <p className="sm-small sm-muted">
          On-failure and on-unconfirmed both start at <span className="sm-data">continue</span> and are not asked here: a macro run always runs every step. Change them in
          the step editor when a step needs to deviate.
        </p>
      </div>

      <p className="sm-small sm-muted">
        Creates the macro with 1 step and opens it in the step editor. Up to <span className="sm-data">32</span> steps.
      </p>

      {taken && <RuledStrip absence="failed" label="Id taken" fact={<span className="sm-data">{id}</span>} detail="Already names a macro in this show." />}
      {createError !== null && <RuledStrip absence="failed" label="Save failed" fact={createError} />}

      <div className="sm-inspector__actions">
        <span className="sm-small sm-muted">
          {blockReason ?? (
            <>
              Creates <span className="sm-data">{id}</span>
            </>
          )}
        </span>
        <div className="sm-btn-row">
          <Button variant="quiet" onClick={onCancel} disabled={creating}>
            Discard
          </Button>
          <Button variant="primary" onClick={create} disabled={creating || blockReason !== null || !canAuthor.allowed} title={!canAuthor.allowed ? canAuthor.reason : (blockReason ?? undefined)}>
            {creating ? 'Creating…' : 'Create macro'}
          </Button>
        </div>
      </div>
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
