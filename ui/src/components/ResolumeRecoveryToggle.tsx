import { useEffect, useRef, useState } from 'react'
import { getResolumeRecovery, putResolumeRecoveryConfig } from '../api'
import { describeApiError } from '../app/session'
import { ScopedButton } from './ScopedButton'

// Track D seam D-3a §7.1/§2.6: the auto-restore toggle's own read and
// write control, and NOTHING else of Track D's UI (the clip/layer
// dropdowns and the Go button are D-4's). Placed on the dashboard rather
// than the config:write-gated Configuration view: the toggle's own state
// "must be visible without being hunted for" (§7.1), which a page whose
// entire content is hidden behind config:write cannot satisfy for a
// principal who only holds resolume:action or a read scope. GET
// /resolume/recovery is an open read (ADR-024) — this component renders
// with no session at all, matching the dashboard's own posture.
//
// Not part of `Model`/the SSE stream (build contract §1.7: no new
// observation signal is minted for this), so this component fetches for
// itself, the same pattern ResolumeCompositionUpload.tsx already uses for
// data outside the snapshot/delta stream.

const CONFIG_WRITE_SCOPE = 'config:write'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'unconfigured' }
  | { kind: 'loaded'; enabled: boolean; configured: boolean }

export function ResolumeRecoveryToggle() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  // Mirrors Configuration.tsx's own savingRef: a synchronous guard against
  // two fast clicks both flipping the toggle before React commits the
  // first setSaving(true).
  const savingRef = useRef(false)
  const [reloadGeneration, setReloadGeneration] = useState(0)

  useEffect(() => {
    let cancelled = false
    setState({ kind: 'loading' })

    async function load(): Promise<void> {
      try {
        const resp = await getResolumeRecovery()
        if (cancelled) return
        if (!resp.resolumeConfigured) {
          setState({ kind: 'unconfigured' })
          return
        }
        setState({ kind: 'loaded', enabled: resp.autoRestoreEnabled, configured: resp.autoRestoreConfigured })
      } catch (err) {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [reloadGeneration])

  async function handleToggle(next: boolean): Promise<void> {
    if (savingRef.current) return
    savingRef.current = true
    setSaving(true)
    setSaveError(null)
    try {
      await putResolumeRecoveryConfig({ autoRestoreEnabled: next })
      setReloadGeneration((g) => g + 1)
    } catch (err) {
      setSaveError(describeApiError(err))
    } finally {
      savingRef.current = false
      setSaving(false)
    }
  }

  return (
    <section className="panel" aria-label="Resolume crash recovery">
      <h2 className="panel__title">Resolume crash recovery</h2>
      {state.kind === 'loading' && <p className="text-muted">Loading…</p>}
      {state.kind === 'error' && (
        <p className="panel panel--error" role="alert">
          {state.message}
        </p>
      )}
      {state.kind === 'unconfigured' && (
        <p className="text-muted" role="status">
          Resolume is not configured on this coordinator — crash recovery is not available.
        </p>
      )}
      {state.kind === 'loaded' && (
        <>
          <p role="status">
            Automatic restore is <strong>{state.enabled ? 'ON' : 'OFF'}</strong>
            {state.configured ? '' : ' (default — no revision has ever been written)'}.
          </p>
          {saveError !== null && (
            <p role="alert" className="session-form__error">
              {saveError}
            </p>
          )}
          <ScopedButton
            requiredScope={CONFIG_WRITE_SCOPE}
            onClick={() => void handleToggle(!state.enabled)}
            busy={saving}
            busyReason="Writing this toggle…"
          >
            {saving ? 'Saving…' : state.enabled ? 'Turn automatic restore off' : 'Turn automatic restore on'}
          </ScopedButton>
        </>
      )}
    </section>
  )
}
