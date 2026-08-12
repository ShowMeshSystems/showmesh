import { useEffect, useState } from 'react'
import {
  ApiError,
  getFPPEndpointsConfig,
  getFPPEndpointsConfigRevisions,
  putFPPEndpointsConfig,
  type ConfigFPPEndpoint,
  type ConfigRevisionMeta,
  type FPPEndpointsConfigResponse,
} from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from '../components/ScopedButton'

// Step 7 seam A (RES-008 D1): the configuration write surface's own view —
// the active `fpp.endpoints` configuration, its revision history, and an
// editor. Admin-only: `config:write` is the ONLY scope this surface is
// gated by, for reads and writes alike (there is no `config:read` scope —
// internal/coordinator/api/config.go's own doc comment records why), so
// this view treats a missing/stale/unavailable `config:write` grant as a
// reason not to even ATTEMPT the fetch, not merely a reason to disable the
// save button — the same "unknown/stale renders as not permitted, never
// permissive" rule (ADR-024 decision 12) applied to the whole page rather
// than one control, because every request this page could make (including
// both GETs) would be refused identically anyway.
const CONFIG_WRITE_SCOPE = 'config:write'

// LoadState deliberately carries NO "not permitted" variant. An earlier
// version stored the not-permitted REASON string in this state, set by the
// fetch effect below, keyed only on `scopeGate.allowed`. That was a real
// bug, found only by testing a live transition in a real browser (CLAUDE.md's
// standing lesson: the suite renders one fixed session per test and never
// catches this): signed-out (`allowed: false`, reason "sign in") to signed
// in as a principal without config:write (`allowed: false`, reason "role
// does not include config:write") never changes `scopeGate.allowed`'s VALUE
// (false -> false), so the effect never re-ran and the page kept showing
// the stale "sign in" message to an operator who was, in fact, already
// signed in. The fix is structural: the not-permitted reason is read
// straight from `scopeGate` on every render, never captured into state.
type LoadState =
  | { kind: 'loading' }
  | { kind: 'not_configured' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: FPPEndpointsConfigResponse; revisions: ConfigRevisionMeta[] }

export function Configuration() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [rows, setRows] = useState<ConfigFPPEndpoint[]>([])
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  // A generation counter, not a boolean: "load again" needs to fire once
  // per Save success, and a plain `useEffect` dependency on `scopeGate.allowed`
  // alone would never re-run after the first successful fetch (the
  // dependency's VALUE does not change between an initial load and a later
  // reload the Save handler wants to trigger).
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    if (!scopeGate.allowed) {
      // Nothing to fetch, and nothing to clear here either: the render
      // below reads `scopeGate` directly whenever `!scopeGate.allowed`, so
      // whatever `state` currently holds simply is not looked at. Leaving
      // it alone (rather than resetting it) also means a grant that comes
      // back later resumes from 'loading' via the branch below, not from
      // a state this effect would otherwise have to reconstruct.
      return
    }

    let cancelled = false
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const [config, revisionsResp] = await Promise.all([
          getFPPEndpointsConfig(),
          getFPPEndpointsConfigRevisions(),
        ])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setRows(config.payload.endpoints)
      } catch (err) {
        if (cancelled) return
        // 404 ("no fpp.endpoints configuration has been created yet") is
        // not a failure this page shows as an error — it is the normal
        // starting state for a coordinator nothing has configured yet,
        // and this view's job is to let an admin create the first
        // revision, not to report its absence as broken.
        if (err instanceof ApiError && err.status === 404) {
          setState({ kind: 'not_configured' })
          setRows([])
          return
        }
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [scopeGate.allowed, reloadGeneration])

  function addRow(): void {
    setRows((r) => [...r, { id: '', url: '' }])
  }

  function removeRow(index: number): void {
    setRows((r) => r.filter((_, i) => i !== index))
  }

  function updateRow(index: number, field: 'id' | 'url', value: string): void {
    setRows((r) => r.map((row, i) => (i === index ? { ...row, [field]: value } : row)))
  }

  async function handleSave(): Promise<void> {
    if (saving) return
    setSaving(true)
    setSaveError(null)
    try {
      // Trimmed here, client-side, purely for operator convenience
      // (trailing whitespace pasted from elsewhere); the coordinator's own
      // validation (ADR-009: rejected before activation) is the actual
      // authority on what is acceptable, and this view renders whatever
      // it says via describeApiError below — it never second-guesses a
      // coordinator rejection with a different, client-invented message.
      const payload = { endpoints: rows.map((r) => ({ id: r.id.trim(), url: r.url.trim() })) }
      await putFPPEndpointsConfig(payload)
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div>
      <h2 className="panel__title">Configuration</h2>
      <p className="text-muted">
        The list of FPP instances this coordinator polls (RES-008 D1), moved out of{' '}
        <code>SHOWMESH_FPP_ENDPOINTS</code> into this coordinator&rsquo;s own store. Requires the{' '}
        <code>config:write</code> scope — admin only; there is no read-only scope for this page.
      </p>

      {!scopeGate.allowed && (
        <p className="panel panel--error" role="status">
          {scopeGate.reason}
        </p>
      )}

      {scopeGate.allowed && (
        <>
          {state.kind === 'loading' && <p className="text-muted">Loading configuration…</p>}
          {state.kind === 'error' && (
            <p className="panel panel--error" role="alert">
              {state.message}
            </p>
          )}

          {(state.kind === 'loaded' || state.kind === 'not_configured') && (
            <>
              {state.kind === 'not_configured' && (
                <p className="text-muted">
                  No <code>fpp.endpoints</code> configuration exists yet. Add at least one instance below and save
                  to create the first revision.
                </p>
              )}
              {state.kind === 'loaded' && (
                <p className="panel" role="status">
                  Active revision {state.config.revision} (source {state.config.source}
                  {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}).{' '}
                  <strong>{state.config.restartRequiredReason}</strong>
                </p>
              )}

              <table className="config-table">
                <thead>
                  <tr>
                    <th>Instance ID</th>
                    <th>URL</th>
                    <th aria-label="Remove" />
                  </tr>
                </thead>
                <tbody>
                  {rows.map((row, index) => (
                    <tr key={index}>
                      <td>
                        <input
                          type="text"
                          aria-label={`Instance ${index + 1} id`}
                          value={row.id}
                          onChange={(e) => updateRow(index, 'id', e.target.value)}
                        />
                      </td>
                      <td>
                        <input
                          type="text"
                          aria-label={`Instance ${index + 1} url`}
                          placeholder="http://10.0.1.20"
                          value={row.url}
                          onChange={(e) => updateRow(index, 'url', e.target.value)}
                        />
                      </td>
                      <td>
                        <button
                          type="button"
                          onClick={() => removeRow(index)}
                          aria-label={`Remove instance ${index + 1}`}
                        >
                          Remove
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
              <button type="button" onClick={addRow}>
                Add instance
              </button>

              <div style={{ marginTop: '1rem' }}>
                {saveError !== null && (
                  <p role="alert" className="session-form__error">
                    {saveError}
                  </p>
                )}
                <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} onClick={() => void handleSave()}>
                  {saving ? 'Saving…' : 'Save configuration'}
                </ScopedButton>
              </div>

              {state.kind === 'loaded' && state.revisions.length > 0 && (
                <>
                  <h3 className="panel__title">Revision history</h3>
                  <table className="config-table">
                    <thead>
                      <tr>
                        <th>Revision</th>
                        <th>Active</th>
                        <th>Created at</th>
                        <th>Created by</th>
                        <th>Source</th>
                      </tr>
                    </thead>
                    <tbody>
                      {state.revisions.map((rev) => (
                        <tr key={rev.revision}>
                          <td>{rev.revision}</td>
                          <td>{rev.active ? 'active' : ''}</td>
                          <td>{formatAbsolute(rev.createdAt)}</td>
                          <td>{rev.createdByPrincipalName ?? '(coordinator startup migration)'}</td>
                          <td>{rev.source}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </>
              )}
            </>
          )}
        </>
      )}
    </div>
  )
}
