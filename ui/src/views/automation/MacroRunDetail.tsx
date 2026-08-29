import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { getMacroRun } from '../../api'
import { describeApiError, evaluateAnyScope } from '../../app/session'
import { useModelContext } from '../../app/ModelContext'
import { formatAbsolute } from '../../app/time'
import { MacroRunOutcome } from '../../components/MacroRunOutcome'
import { MacroRunStepRow } from '../../components/MacroRunStepRow'
import { showAutomationPath } from '../../components/showWorkspacePaths'
import type { MacroRun } from '../../app/types'

const READ_SCOPES = ['show:macro:run', 'config:write']

/**
 * Per-step macro-run detail is deliberately FETCHED, not streamed
 * (UI-DESIGN-GUIDE.md §6: "a 32-step run must not put 32 events on every
 * client's stream"); this view is what goes and gets it, and stops polling
 * the moment `state === 'finished'`.
 */
const RUNNING_POLL_INTERVAL_MS = 3_000

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; run: MacroRun; loadedAt: number; pollError: string | null }

export function MacroRunDetail({ showId, macroId, runId }: { showId: string; macroId: string; runId: string }) {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const stateRef = useRef(state)
  stateRef.current = state

  async function load(): Promise<void> {
    try {
      const resp = await getMacroRun(runId)
      setState({ kind: 'loaded', run: resp.run, loadedAt: Date.now(), pollError: null })
    } catch (err) {
      const message = describeApiError(err)
      setState((prev) => (prev.kind === 'loaded' ? { ...prev, pollError: message } : { kind: 'error', message }))
    }
  }

  useEffect(() => {
    if (!readGate.allowed) return
    setState({ kind: 'loading' })
    void load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runId, readGate.allowed])

  useEffect(() => {
    if (!readGate.allowed) return
    const interval = setInterval(() => {
      if (stateRef.current.kind === 'loaded' && stateRef.current.run.state === 'finished') {
        clearInterval(interval)
        return
      }
      void load()
    }, RUNNING_POLL_INTERVAL_MS)
    return () => clearInterval(interval)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [runId, readGate.allowed])

  if (!readGate.allowed) {
    return (
      <div className="card automation-inspector__section" role="status">
        {readGate.reason}
      </div>
    )
  }
  if (state.kind === 'loading') {
    return <div className="card automation-inspector__section text-muted">Loading run…</div>
  }
  if (state.kind === 'error') {
    return (
      <div className="card automation-inspector__section" role="alert" style={{ color: 'var(--bad-fg)' }}>
        {state.message}
      </div>
    )
  }

  const { run, loadedAt, pollError } = state

  return (
    <div className="card">
      <div className="automation-inspector__section" style={{ background: 'var(--raised)' }}>
        <p className="t-meta automation-inspector__eyebrow">Run</p>
        <h2 id="macro-run-detail-heading" className="t-heading" style={{ margin: '5px 0 0' }}>
          <Link to={`${showAutomationPath(showId)}/macros/${encodeURIComponent(run.macroObjectId)}`}>{run.macroObjectId}</Link>{' '}
          <span className="t-small" style={{ color: 'var(--text-muted)' }}>revision {run.macroRevision}</span>
        </h2>
        <p className="t-small" style={{ margin: '6px 0 0', color: 'var(--text-muted)' }}>
          Started {formatAbsolute(run.createdAt)} by {run.issuerPrincipalName} ({run.trigger})
          {run.finishedAt !== null && <>, finished {formatAbsolute(run.finishedAt)}</>}
        </p>
      </div>

      {pollError !== null && (
        <p className="automation-inspector__section" role="alert" style={{ color: 'var(--bad-fg)' }}>
          Could not refresh this run just now: {pollError} What is shown below is the last known state, not
          necessarily current.
        </p>
      )}

      <div className="automation-inspector__section">
        <MacroRunOutcome run={run} />
        <p className="t-small" style={{ marginTop: 8, color: 'var(--text-faint)' }}>Last updated {formatAbsolute(new Date(loadedAt).toISOString())}.</p>
        {macroId !== run.macroObjectId && (
          <p className="t-small" role="status" style={{ color: 'var(--text-muted)' }}>
            This run belongs to macro {run.macroObjectId}, not {macroId}.
          </p>
        )}
      </div>

      <div className="automation-inspector__section">
        <h3 id="macro-run-step-list-heading" className="t-meta automation-inspector__eyebrow">
          Steps
        </h3>
        {run.steps.length === 0 ? (
          <p className="t-small" style={{ color: 'var(--text-muted)' }}>This run has no steps recorded.</p>
        ) : (
          <ol className="list-plain macro-run-step-list" aria-labelledby="macro-run-step-list-heading">
            {run.steps.map((step) => (
              <MacroRunStepRow key={step.stepIndex} step={step} />
            ))}
          </ol>
        )}
      </div>
    </div>
  )
}
