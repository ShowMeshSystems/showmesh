import { useEffect, useRef, useState } from 'react'
import {
  getRenderSettingsConfig,
  getRenderSettingsConfigRevisions,
  putRenderSettingsConfig,
  type ConfigRenderRestartPolicy,
  type ConfigRevisionMeta,
  type RenderSettingsConfigResponse,
} from '../api'
import { describeApiError, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import { ScopedButton } from './ScopedButton'

// Track B seam B2c (ADR-039): the render.settings configuration singleton
// — what a surface draws while the MultiSync timeline is stopped, opened,
// or unknown (idleOutput, TRACK-B-BUILD-CONTRACT.md ruling 3), and the
// render pipeline supervisor's bounded restart backoff. Mirrors
// Configuration.tsx's own fpp.endpoints editor shape: gated by
// config:write for reads and writes alike, an explicit rendering for every
// state, and revision/updated-at metadata shown alongside the control — a
// gap a recent review found on the resolume.recovery toggle
// (ResolumeRecoveryToggle.tsx shows no revision at all) and this panel
// exists not to repeat.
const CONFIG_WRITE_SCOPE = 'config:write'

const IDLE_OUTPUTS = ['black', 'hold', 'diagnostic'] as const
type IdleOutput = (typeof IDLE_OUTPUTS)[number]

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: RenderSettingsConfigResponse; revisions: ConfigRevisionMeta[] }

export function RenderSettingsPanel() {
  const model = useModelContext()
  const scopeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)

  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [idleOutput, setIdleOutput] = useState<IdleOutput>('black')
  const [restartPolicy, setRestartPolicy] = useState<ConfigRenderRestartPolicy>({
    initialDelaySeconds: 1,
    maxDelaySeconds: 30,
    maxConsecutiveFastFailures: 5,
  })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  // Mirrors Configuration.tsx's own savingRef: a synchronous guard against
  // two fast clicks both submitting a write before React commits the first
  // setSaving(true) — see that file's own comment for the full reasoning.
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
          getRenderSettingsConfig(),
          getRenderSettingsConfigRevisions(),
        ])
        if (cancelled) return
        setState({ kind: 'loaded', config, revisions: revisionsResp.revisions })
        setIdleOutput(config.payload.idleOutput)
        setRestartPolicy(config.payload.restartPolicy)
      } catch (err) {
        if (cancelled) return
        // Unlike fpp.endpoints, this kind never 404s (a well-defined
        // default), so any error here is a genuine failure, never a
        // "nothing configured yet" signal to render specially.
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
      // A full replacement (ADR-039 decision 5's house style): every field
      // is sent on every save, never merged server-side against the
      // previous revision — see config.DecodeRenderSettingsPayload's own
      // doc comment for why an absent field is refused rather than
      // defaulted.
      await putRenderSettingsConfig({ idleOutput, restartPolicy })
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
      <h2 className="panel__title">Render settings</h2>
      <p className="text-muted">
        What a render surface draws while its MultiSync timeline is stopped, opened, or unknown
        (<code>idleOutput</code>), and the render pipeline supervisor&rsquo;s bounded restart backoff
        (<code>restartPolicy</code>). Requires the <code>config:write</code> scope (admin only); there is no
        read-only scope for this page.
      </p>

      {!scopeGate.allowed && (
        <p className="panel panel--error" role="status">
          {scopeGate.reason}
        </p>
      )}

      {scopeGate.allowed && (
        <>
          {state.kind === 'loading' && <p className="text-muted">Loading render settings…</p>}
          {state.kind === 'error' && (
            <p className="panel panel--error" role="alert">
              {state.message}
            </p>
          )}

          {state.kind === 'loaded' && (
            <>
              <p className="panel" role="status">
                {state.config.revision === 0 ? (
                  <>
                    Never configured, showing the built-in default (source <code>{state.config.source}</code>).
                  </>
                ) : (
                  <>
                    Active revision {state.config.revision} (source {state.config.source}
                    {state.config.createdByPrincipalName !== null && `, by ${state.config.createdByPrincipalName}`}
                    ), updated {formatAbsolute(state.config.updatedAt)}.
                  </>
                )}
              </p>

              <div className="config-form">
                <label htmlFor="render-idle-output">Idle output</label>
                <select
                  id="render-idle-output"
                  value={idleOutput}
                  onChange={(e) => setIdleOutput(e.target.value as IdleOutput)}
                >
                  {IDLE_OUTPUTS.map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </select>

                <label htmlFor="render-initial-delay">Restart: initial delay (seconds)</label>
                <input
                  id="render-initial-delay"
                  type="number"
                  min={1}
                  max={60}
                  value={restartPolicy.initialDelaySeconds}
                  onChange={(e) =>
                    setRestartPolicy((p) => ({ ...p, initialDelaySeconds: Number(e.target.value) }))
                  }
                />

                <label htmlFor="render-max-delay">Restart: max delay (seconds)</label>
                <input
                  id="render-max-delay"
                  type="number"
                  min={1}
                  max={300}
                  value={restartPolicy.maxDelaySeconds}
                  onChange={(e) => setRestartPolicy((p) => ({ ...p, maxDelaySeconds: Number(e.target.value) }))}
                />

                <label htmlFor="render-max-fast-failures">Restart: consecutive fast failures before giving up</label>
                <input
                  id="render-max-fast-failures"
                  type="number"
                  min={1}
                  max={20}
                  value={restartPolicy.maxConsecutiveFastFailures}
                  onChange={(e) =>
                    setRestartPolicy((p) => ({ ...p, maxConsecutiveFastFailures: Number(e.target.value) }))
                  }
                />
              </div>

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
                  busyReason="Saving render settings…"
                >
                  {saving ? 'Saving…' : 'Save render settings'}
                </ScopedButton>
              </div>

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
                </>
              )}
            </>
          )}
        </>
      )}
    </div>
  )
}
