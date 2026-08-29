import { useEffect, useMemo, useState } from 'react'
import { Link, useLocation, useParams } from 'react-router-dom'
import { getShowMacro, listActionBindings, listConfigObjects } from '../../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../../app/session'
import { useModelContext } from '../../app/ModelContext'
import { formatAbsolute } from '../../app/time'
import { RunMacroButton } from '../../components/RunMacroButton'
import { ActionBindingCheck } from '../../components/ActionBindingCheck'
import { ActionInvokeButton } from '../../components/ActionInvokeButton'
import { showAutomationPath } from '../../components/showWorkspacePaths'
import type { ActionBinding, ConfigObjectSummary, ConfigShowMacro } from '../../app/types'
import { deriveMacroConsequence, deriveMacroReadiness, describeMacroReadiness } from './automationDerive'
import { useActionFacts } from './useActionFacts'
import { INTEGRATION_LABEL } from './describeAction'
import { MacroStepEditor } from './MacroStepEditor'
import { MacroRunDetail } from './MacroRunDetail'
import { ShowActionDetail } from '../ShowActionDetail'
import '../../styles/automation.css'

const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'
const RUN_SCOPE = 'show:macro:run'

interface MacroEntry {
  summary: ConfigObjectSummary
  payload: ConfigShowMacro | null
}

/**
 * The Automation workspace tab (Show Automation.dc.html), reached at
 * `/shows/:showId/automation` and its six sub-routes (ROUTE-MAP.md): the
 * macro/action list on the left never changes shape across those routes,
 * only the inspector on the right does, driven by whichever of
 * `:macroId`/`:actionId`/`:runId` the mounting route supplies.
 */
export function AutomationWorkspace({ showId }: { showId: string }) {
  const params = useParams<{ macroId?: string; actionId?: string; runId?: string }>()
  const location = useLocation()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const runGate = evaluateScope(model.session, model.sessionFetchFailed, RUN_SCOPE)

  const [macros, setMacros] = useState<MacroEntry[] | null>(null)
  const [actions, setActions] = useState<ConfigObjectSummary[] | null>(null)
  const [bindings, setBindings] = useState<Map<string, ActionBinding>>(new Map())
  const [loadError, setLoadError] = useState<string | null>(null)
  const [filterText, setFilterText] = useState('')

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    listConfigObjects('show.macro', showId)
      .then(async (resp) => {
        if (cancelled) return
        setMacros(resp.objects.map((summary) => ({ summary, payload: null })))
        const payloads = await Promise.all(
          resp.objects.map((o) =>
            getShowMacro(o.id)
              .then((r): readonly [string, ConfigShowMacro] => [o.id, r.payload])
              .catch((): readonly [string, null] => [o.id, null]),
          ),
        )
        if (cancelled) return
        const byId = new Map(payloads)
        setMacros((prev) => (prev ?? resp.objects.map((summary) => ({ summary, payload: null }))).map((m) => ({ ...m, payload: byId.get(m.summary.id) ?? null })))
      })
      .catch((err: unknown) => {
        if (!cancelled) setLoadError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, showId])

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    listConfigObjects('show.action', showId)
      .then((resp) => {
        if (!cancelled) setActions(resp.objects)
      })
      .catch((err: unknown) => {
        if (!cancelled) setLoadError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, showId])

  useEffect(() => {
    // No credential required (ADR-024 constraint 23) — never gated on
    // readGate, and a failed fetch just leaves badges off the list.
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

  const allActionIds = useMemo(() => {
    const ids = new Set<string>()
    for (const m of macros ?? []) {
      for (const s of m.payload?.steps ?? []) ids.add(s.action)
    }
    return [...ids]
  }, [macros])
  const facts = useActionFacts(allActionIds, readGate.allowed)

  const inMacroActionIds = new Set(allActionIds)
  const actionsInMacro = (actions ?? []).filter((a) => inMacroActionIds.has(a.id))
  const actionsNotInMacro = (actions ?? []).filter((a) => !inMacroActionIds.has(a.id))

  const usedByLabels = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const m of macros ?? []) {
      for (const s of m.payload?.steps ?? []) {
        const list = map.get(s.action) ?? []
        if (!list.includes(m.summary.label)) list.push(m.summary.label)
        map.set(s.action, list)
      }
    }
    return map
  }, [macros])

  const macroCards = (macros ?? []).map((entry) => {
    const steps = entry.payload?.steps ?? []
    const readiness = deriveMacroReadiness(steps, bindings)
    const consequence = deriveMacroConsequence(steps.map((s) => facts[s.action]?.safetyClass).filter((c): c is NonNullable<typeof c> => c !== undefined && c !== null))
    const running = model.macroRuns.filter((r) => r.macroObjectId === entry.summary.id && r.state === 'running').sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1))[0]
    return { entry, readiness, consequence, running }
  })

  const needsYou = macroCards.filter((c) => c.readiness.kind === 'broken')

  const filter = filterText.trim().toLowerCase()
  const visibleMacroCards = filter === '' ? macroCards : macroCards.filter((c) => c.entry.summary.label.toLowerCase().includes(filter) || c.entry.summary.id.toLowerCase().includes(filter))

  const isNewMacro = location.pathname.endsWith('/macros/new')
  const isNewAction = location.pathname.endsWith('/actions/new')
  const selectedMacroId = params.macroId
  const selectedActionId = params.actionId
  const selectedRunId = params.runId

  if (!readGate.allowed) {
    return (
      <div className="page-body">
        <p role="status">{readGate.reason}</p>
      </div>
    )
  }

  return (
    <div className="page-body">
      <div className="panes">
        <section aria-labelledby="automation-macros-heading" style={{ minWidth: 0 }}>
          <div className="automation-toolbar">
            <div className="automation-toolbar__controls">
              <input
                className="automation-toolbar__search"
                type="text"
                placeholder="Filter macros…"
                value={filterText}
                onChange={(e) => setFilterText(e.target.value)}
                aria-label="Filter macros"
              />
            </div>
            <div style={{ display: 'flex', gap: 10 }}>
              {writeGate.allowed && (
                <Link className="btn btn--secondary" to={`${showAutomationPath(showId)}/actions/new`}>
                  New action
                </Link>
              )}
              {writeGate.allowed && (
                <Link className="btn btn--primary" to={`${showAutomationPath(showId)}/macros/new`}>
                  New macro
                </Link>
              )}
            </div>
          </div>

          <p className="t-small" style={{ marginTop: 10, color: 'var(--text-muted)' }}>
            A macro composes actions and runs them in order. An action never appears on its own in a running show.
          </p>

          {loadError !== null && (
            <p role="alert" className="t-small" style={{ color: 'var(--bad-fg)' }}>
              {loadError}
            </p>
          )}

          {needsYou.length > 0 && (
            <section aria-labelledby="automation-needs-you-heading" style={{ marginTop: 14 }}>
              <h2 id="automation-needs-you-heading" className="t-meta" style={{ color: 'var(--bad-fg)', letterSpacing: '0.09em', textTransform: 'uppercase' }}>
                Needs you
              </h2>
              {needsYou.map(({ entry, readiness }) => (
                <div key={entry.summary.id} className="ruled-strip ruled-strip--failed">
                  <span className="ruled-strip__state t-meta">Broken</span>
                  <div>
                    <p className="ruled-strip__fact">
                      <Link to={`${showAutomationPath(showId)}/macros/${encodeURIComponent(entry.summary.id)}`}>{entry.summary.label}</Link>
                    </p>
                    <p className="ruled-strip__explanation">
                      {readiness.kind === 'broken' ? readiness.reason : ''}
                    </p>
                  </div>
                </div>
              ))}
            </section>
          )}

          <section aria-labelledby="automation-macros-heading" style={{ marginTop: 18 }}>
            <h2 id="automation-macros-heading" className="t-heading">
              Macros
            </h2>
            {visibleMacroCards.length === 0 && <p className="t-small" style={{ color: 'var(--text-faint)' }}>No macros match this filter.</p>}
            <div style={{ display: 'grid', gap: 12, marginTop: 10 }}>
              {visibleMacroCards.map(({ entry, readiness, consequence, running }) => {
                const badge = describeMacroReadiness(readiness)
                const selected = selectedMacroId === entry.summary.id && selectedRunId === undefined
                return (
                  <div
                    key={entry.summary.id}
                    className={`macro-card${readiness.kind === 'broken' ? ' macro-card--broken' : ''}`}
                    aria-current={selected ? 'true' : undefined}
                  >
                    <Link to={`${showAutomationPath(showId)}/macros/${encodeURIComponent(entry.summary.id)}`} className="macro-card__link" style={{ display: 'block', color: 'inherit', textDecoration: 'none' }}>
                      <div className="macro-card__head">
                        <div style={{ minWidth: 0 }}>
                          <strong className="t-subhead">{entry.summary.label}</strong>
                          <p className="t-data" style={{ margin: '2px 0 0', fontSize: 11, color: 'var(--text-faint)' }}>
                            {entry.summary.id} · revision {entry.summary.currentRevision} · {entry.payload?.steps.length ?? '…'} steps
                          </p>
                        </div>
                        <span className={`status-pair status-pair--${badge.tone === 'unknown' && readiness.kind === 'unchecked' ? 'unobserved' : badge.tone}`}>{badge.label}</span>
                      </div>
                      {consequence !== null && (
                        <p className="t-small" style={{ margin: '8px 0 0', color: 'var(--warn-fg)' }}>{consequence}</p>
                      )}
                      {selected && <span className="table__editing-chip t-small" style={{ marginTop: 8 }}>▸ Editing</span>}
                    </Link>
                    <div className="macro-card__footer">
                      <span className="t-small" style={{ color: 'var(--text-muted)' }}>
                        {running !== undefined ? (
                          <Link to={`${showAutomationPath(showId)}/macros/${encodeURIComponent(entry.summary.id)}/runs/${encodeURIComponent(running.id)}`}>Running now</Link>
                        ) : (
                          'Never run, or see the inspector for its last run.'
                        )}
                      </span>
                      {runGate.allowed ? (
                        <RunMacroButton macroId={entry.summary.id} />
                      ) : (
                        <button type="button" className="btn btn--secondary btn--gloved" disabled aria-disabled="true" title={runGate.reason}>
                          Run
                        </button>
                      )}
                    </div>
                  </div>
                )
              })}
            </div>
          </section>

          <section aria-labelledby="automation-actions-heading" style={{ marginTop: 24 }}>
            <h2 id="automation-actions-heading" className="t-heading">
              Actions
            </h2>
            <h3 className="t-meta automation-inspector__eyebrow" style={{ marginTop: 10 }}>
              In a macro <span className="automation-section__count">· {actionsInMacro.length}</span>
            </h3>
            <ActionList
              showId={showId}
              actions={actionsInMacro}
              bindings={bindings}
              facts={facts}
              selectedId={selectedActionId}
              variant="in-macro"
              usedByLabels={usedByLabels}
            />
            <h3 className="t-meta automation-inspector__eyebrow" style={{ marginTop: 14 }}>
              Not in a macro <span className="automation-section__count">· {actionsNotInMacro.length}</span>
            </h3>
            <p className="t-small" style={{ color: 'var(--text-muted)', margin: '4px 0 0' }}>
              Reachable only by direct invoke, or by a cue that names it and pins a revision. No macro will ever fire it.
            </p>
            <ActionList showId={showId} actions={actionsNotInMacro} bindings={bindings} facts={facts} selectedId={selectedActionId} variant="not-in-macro" />
          </section>
        </section>

        <aside>
          {selectedRunId !== undefined && selectedMacroId !== undefined ? (
            <MacroRunDetail showId={showId} macroId={selectedMacroId} runId={selectedRunId} />
          ) : isNewMacro ? (
            <MacroStepEditor showId={showId} isNew />
          ) : selectedMacroId !== undefined ? (
            <MacroStepEditor showId={showId} macroId={selectedMacroId} />
          ) : isNewAction ? (
            <ShowActionDetail isNew />
          ) : selectedActionId !== undefined ? (
            <ShowActionDetail />
          ) : (
            <div className="card automation-inspector__section">
              <p className="t-small" style={{ color: 'var(--text-faint)' }}>
                Select a macro or an action on the left to see it here.
              </p>
            </div>
          )}
        </aside>
      </div>
    </div>
  )
}

function ActionList({
  showId,
  actions,
  bindings,
  facts,
  selectedId,
  variant,
  usedByLabels,
}: {
  showId: string
  actions: ConfigObjectSummary[]
  bindings: Map<string, ActionBinding>
  facts: Record<string, ReturnType<typeof useActionFacts>[string]>
  selectedId: string | undefined
  variant: 'in-macro' | 'not-in-macro'
  usedByLabels?: Map<string, string[]>
}) {
  if (actions.length === 0) {
    return <p className="t-small" style={{ color: 'var(--text-faint)' }}>None.</p>
  }
  return (
    <div className="card table-wrap" style={{ marginTop: 6 }}>
      <table className="table table--inspector">
        <thead>
          <tr>
            <th>Action</th>
            <th>Kind</th>
            <th>{variant === 'in-macro' ? 'Used by' : 'Invoke'}</th>
            <th>Binding</th>
          </tr>
        </thead>
        <tbody>
          {actions.map((a) => {
            const binding = bindings.get(a.id)
            const fact = facts[a.id]
            return (
              <tr key={a.id} aria-current={selectedId === a.id ? 'true' : undefined} className={binding?.state === 'broken' ? 'automation-action-row--broken' : ''}>
                <td>
                  <Link to={`${showAutomationPath(showId)}/actions/${encodeURIComponent(a.id)}`} className="automation-action-cell__label">
                    {a.label}
                  </Link>
                </td>
                <td>{fact != null && <span className="macro-step-row__kind">{INTEGRATION_LABEL[fact.integration]}</span>}</td>
                <td>
                  {variant === 'in-macro' ? (
                    <span className="t-small text-muted">{usedByLabels?.get(a.id)?.join(', ') ?? ''}</span>
                  ) : (
                    <ActionInvokeButton actionId={a.id} label="Invoke" />
                  )}
                </td>
                <td>
                  {binding !== undefined ? (
                    <>
                      <ActionBindingCheck actionId={a.id} />
                      {fact?.unconfirmableByDesign === true && <span className="automation-action-binding-note">Never confirms</span>}
                    </>
                  ) : (
                    <span className="t-small" style={{ color: 'var(--text-faint)' }}>updated {formatAbsolute(a.updatedAt)}</span>
                  )}
                </td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}
