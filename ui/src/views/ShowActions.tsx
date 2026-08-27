import { useEffect, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { listActionBindings, listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ActionBinding, ConfigObjectSummary } from '../app/types'
import { ActionBindingBadge } from '../components/DomainBadges'
import { ActionInvokeButton } from '../components/ActionInvokeButton'
import { showWorkspacePath } from '../components/showWorkspacePaths'

// Same read posture as Macros.tsx (STEP-9-SPEC.md section 5.5: "Same
// read posture as GET /config/show.action"): show:macro:run OR
// config:write, because a macro's own detail view (MacroDetail.tsx)
// needs to resolve the actions it composes for a principal who can run
// macros but is not an admin.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; objects: ConfigObjectSummary[] }

export function ShowActions() {
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  // ?show=<id>, mirroring ShowSurfaces.tsx's own filter.
  const [searchParams, setSearchParams] = useSearchParams()
  const showFilter = searchParams.get('show') ?? ''

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  // Binding-check results, keyed by action id. A SEPARATE fetch from the
  // list above: the binding sweep requires no credential at all (ADR-024
  // constraint 23), so it must not be gated on readGate, and a failure to
  // fetch it must not blank the list itself —
  // an action row with no binding entry yet renders with no badge, never
  // an error for the whole table.
  const [bindings, setBindings] = useState<Map<string, ActionBinding>>(new Map())

  useEffect(() => {
    if (!readGate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.action', showFilter === '' ? undefined : showFilter)
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', objects: resp.objects })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [readGate.allowed, showFilter])

  useEffect(() => {
    let cancelled = false
    listActionBindings(showFilter === '' ? undefined : showFilter)
      .then((list) => {
        if (cancelled) return
        setBindings(new Map(list.map((b) => [b.actionId, b])))
      })
      .catch(() => {
        // Best-effort: the list itself still renders with no badges.
      })
    return () => {
      cancelled = true
    }
  }, [showFilter])

  return (
    <div className="operator-page authoring-page">
      <div className="operator-page__header">
        <h2 className="panel__title">Show actions</h2>
        {/* This task's finding 9, applied identically to Macros.tsx's
            "New macro" link: hidden outright for a principal without
            config:write, the one deviation in this app from the standing
            rule (OPERATOR-UI section 14 / ADR-024 decision 12) that a
            control the principal may not use is rendered disabled with a
            stated reason, never hidden. Mirrors ScopedButton's own
            disabled shape exactly. */}
        {writeGate.allowed ? (
          <Link className="entity-link" to="/actions/new">
            New action
          </Link>
        ) : (
          <span className="scoped-button">
            <button type="button" disabled aria-disabled="true" title={writeGate.reason}>
              New action
            </button>
            <span className="scoped-button__reason">{writeGate.reason}</span>
          </span>
        )}
      </div>
      <p className="operator-page__lede text-muted">
        A show action is one logical step a macro can invoke: an FPP primitive or an external
        MQTT command. Macros compose actions; actions never appear on their own in a running
        show.
      </p>

      <label className="form-field" style={{ maxWidth: '20rem' }}>
        Narrow by show
        <input
          type="text"
          placeholder="show id, or leave blank for every show"
          value={showFilter}
          onChange={(e) => {
            const value = e.target.value
            setSearchParams(value === '' ? {} : { show: value })
          }}
        />
      </label>

      {!readGate.allowed && (
        <p className="panel panel--error" role="status">
          {readGate.reason}
        </p>
      )}

      {readGate.allowed && state.kind === 'loading' && <p className="text-muted">Loading actions…</p>}
      {readGate.allowed && state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {readGate.allowed && state.kind === 'loaded' && (
        <>
          {state.objects.length === 0 ? (
            <p className="text-muted">No show actions are configured yet.</p>
          ) : (
            // Same overflow gap this task's finding 11 found and fixed on
            // Macros.tsx's own .config-table — this table shares the same
            // markup shape (just without a Run column), so it gets the
            // same fix.
            <div className="table-scroll">
              <table className="config-table">
                <thead>
                  <tr>
                    <th>Label</th>
                    <th>Show</th>
                    <th>Revision</th>
                    <th>Updated</th>
                    <th>Binding</th>
                    <th>Invoke</th>
                  </tr>
                </thead>
                <tbody>
                  {state.objects.map((obj) => {
                    const binding = bindings.get(obj.id)
                    return (
                      <tr key={obj.id}>
                        <td>
                          <Link className="entity-link" to={`/actions/${encodeURIComponent(obj.id)}`}>
                            {obj.label}
                          </Link>
                        </td>
                        <td><Link className="entity-link" to={showWorkspacePath(obj.show)}>{obj.show}</Link></td>
                        <td>{obj.currentRevision}</td>
                        <td>{formatAbsolute(obj.updatedAt)}</td>
                        <td>
                          {binding ? (
                            <ActionBindingBadge state={binding.state} reason={binding.reason} />
                          ) : (
                            <span className="text-muted">-</span>
                          )}
                        </td>
                        <td>
                          <ActionInvokeButton actionId={obj.id} label="Go" />
                        </td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </>
      )}
    </div>
  )
}
