import { useEffect, useRef, useState } from 'react'
import {
  getShowModeConfig,
  getShowModeConfigRevisions,
  putShowModeConfig,
  type ConfigRevisionMeta,
  type ShowModeConfigResponse,
} from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from './ScopedButton'

// ADR-033: the WRITE surface for the installation-wide operating mode.
// Mirrors RenderSettingsPanel's shape exactly, including showing the
// revision and updated-at metadata alongside the control, which that panel's
// own header comment records as a review finding
// (ResolumeRecoveryToggle.tsx omitted it) and this panel does not repeat.
//
// The READ half of the mode does not live here. ADR-033 decision 3 says the
// mode appears "persistently, not on a settings page", and that is
// ShowModeIndicator in the app header. This panel is where an operator with
// config:write CHANGES it.
//
// Setting the mode gates nothing. ADR-033 decision 4 is non-negotiable: no
// mode may refuse, delay, or degrade blackout, stop, or power-off, so
// nothing on this page warns about, confirms, or blocks any other action.
const CONFIG_WRITE_SCOPE = 'config:write'

const SHOW_MODES = ['program', 'show'] as const
type ShowMode = (typeof SHOW_MODES)[number]

const MODE_LABELS: Record<ShowMode, string> = {
  program: 'Program mode (being set up or programmed)',
  show: 'Show mode (a show is running)',
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowModeConfigResponse; revisions: ConfigRevisionMeta[] }

export function ShowModePanel() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [mode, setMode] = useState<ShowMode>('program')
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  // Mirrors Configuration.tsx's own savingRef: a synchronous guard against
  // two fast clicks both submitting a write before React commits the first
  // setSaving(true).
  const savingRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    if (!scopeGate.allowed) {
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const [config, revisionsResp] = await Promise.all([
          getShowModeConfig(),
          getShowModeConfigRevisions(),
        ])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setMode(config.payload.mode as ShowMode)
      } catch (err) {
        if (cancelled) return
        // This kind never 404s (a well-defined default), so any error here
        // is a genuine failure, never a "nothing configured yet" signal.
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [scopeGate.allowed, reloadGeneration])

  async function handleSave(): Promise<void> {
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      // A full replacement: "mode" is sent on every save, never merged
      // server-side against the previous revision.
      await putShowModeConfig({ mode })
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <div>
      <h2 className="panel__title">Show mode</h2>
      <p className="text-muted">
        One operating mode for the whole installation: <code>program</code> while it is being set up
        or programmed, <code>show</code> while a show is running. Not per-node, not per-device, and
        never a per-subsystem flag. Changing it requires the <code>config:write</code> scope; every
        signed-in role can SEE the current mode in the header on every page.
      </p>
      <p className="text-muted">
        The mode changes what the system does, never who may do it, and it gates no command: no mode
        refuses, delays, or degrades blackout, stop, or power-off. It applies without a coordinator
        restart, in both directions.
      </p>

      {!scopeGate.allowed && (
        <p className="panel panel--error" role="status">
          {scopeGate.reason}
        </p>
      )}

      {scopeGate.allowed && (
        <>
          {state.kind === 'loading' && <p className="text-muted">Loading show mode…</p>}
          {state.kind === 'error' && (
            <p className="panel panel--error" role="alert">
              {state.message}
            </p>
          )}

          {state.kind === 'loaded' && (
            <>
              <p className="config-status" role="status">
                {state.config.revision === 0 ? (
                  <>
                    Never set, showing the built-in default{' '}
                    <code>{state.config.payload.mode}</code> (source{' '}
                    <code>{state.config.source}</code>). A fresh install is by definition being set
                    up.
                  </>
                ) : (
                  <>
                    Active revision {state.config.revision} (source {state.config.source}
                    {state.config.createdByPrincipalName !== null &&
                      `, by ${state.config.createdByPrincipalName}`}
                    ), updated {formatAbsolute(state.config.updatedAt)}.
                  </>
                )}
              </p>

              <p className="section-notice" role="status">
                {state.config.resolumeWebSocketEffect}
              </p>

              <div className="config-form">
                <label htmlFor="show-mode">Operating mode</label>
                <select
                  id="show-mode"
                  value={mode}
                  onChange={(e) => setMode(e.target.value as ShowMode)}
                >
                  {SHOW_MODES.map((v) => (
                    <option key={v} value={v}>
                      {MODE_LABELS[v]}
                    </option>
                  ))}
                </select>
              </div>

              <div className="config-save-row">
                {saveError !== null && (
                  <p role="alert" className="session-form__error">
                    {saveError}
                  </p>
                )}
                <ScopedButton
                  requiredScope={CONFIG_WRITE_SCOPE}
                  onClick={() => void handleSave()}
                  busy={saving}
                  busyReason="Saving show mode…"
                >
                  {saving ? 'Saving…' : 'Save show mode'}
                </ScopedButton>
              </div>

              {state.revisions.length > 0 && (
                <>
                  <h3 className="panel__title">Revision history</h3>
                  <div className="table-scroll">
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
                          <td>{rev.createdByPrincipalName ?? '(unknown)'}</td>
                          <td>{rev.source}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                  </div>
                </>
              )}
            </>
          )}
        </>
      )}
    </div>
  )
}
