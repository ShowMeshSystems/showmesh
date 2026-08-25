import { useEffect, useState } from 'react'
import {
  applyRenderSurface,
  clearRenderSurface,
  listShowSurfacesForNode,
  probeRenderTransport,
  restartRenderPipeline,
} from '../api'
import type { ObservationEntry, RenderCommandResult } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { EvidenceValue } from './EvidenceValue'
import { ScopedButton } from './ScopedButton'

// Track B seam B2b-front: the render panel on the node detail view.
// node.render (api/domain.ts's ObservationEntry[]) is a flat per-signal
// list spanning every surface this node has ever reported (ADR-026: a
// surface, not the node, is what is observed) — this component groups it
// by surface and renders each signal through the ONE shared evidence
// renderer (EvidenceValue), never a second copy, with
// serverTime/serverTimeReceivedAt/connected wired through so ages update
// live rather than freezing (Step 4's own shipped-once bug).
export interface RenderSurfacePanelProps {
  nodeId: string
  entries: ObservationEntry[]
}

// Finding 16 (Track B surface fixes): matches api.go's showConfigReadScopes
// gate on GET /config/show.surface — the same scope pair views/Macros.tsx
// gates its own show.macro read on, for the identical reason: an operator
// role holds show:macro:run and NOT config:write, and this list must
// render for that role too.
const CONFIGURED_SURFACE_READ_SCOPES = ['show:macro:run', 'config:write']

type ConfiguredSurfacesState =
  | { kind: 'idle' }
  | { kind: 'loading' }
  | { kind: 'loaded'; surfaceIds: string[] }
  | { kind: 'error'; message: string }

// useConfiguredSurfaceIds resolves which show.surface objects are
// assigned to nodeId (payload.node) — the set `showmeshctl render apply`
// already reaches without any prior render report, and the render report
// alone cannot see (a supervisor entry, and so a report row, exists only
// AFTER an apply). GET /config/show.surface?node=<id> filters server-side,
// so this is one HTTP call regardless of how many surfaces are
// configured — an external review of PR #14 found the earlier
// listConfigObjects + per-row getShowSurface fan-out costing one HTTP
// call per configured surface just to read its node field.
function useConfiguredSurfaceIds(nodeId: string, allowed: boolean): ConfiguredSurfacesState {
  const [state, setState] = useState<ConfiguredSurfacesState>({ kind: 'idle' })

  useEffect(() => {
    if (!allowed) {
      setState({ kind: 'idle' })
      return
    }
    let cancelled = false
    setState({ kind: 'loading' })
    listShowSurfacesForNode(nodeId)
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', surfaceIds: resp.objects.map((obj) => obj.id) })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [nodeId, allowed])

  return state
}

export function RenderSurfacePanel({ nodeId, entries }: RenderSurfacePanelProps) {
  const model = useModelContext()
  const connected = model.connection.kind === 'live'
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, CONFIGURED_SURFACE_READ_SCOPES)
  const configured = useConfiguredSurfaceIds(nodeId, readGate.allowed)

  const bySurface = new Map<string, ObservationEntry[]>()
  const order: string[] = []
  for (const e of entries) {
    const id = e.resource.id
    if (!bySurface.has(id)) {
      bySurface.set(id, [])
      order.push(id)
    }
    bySurface.get(id)!.push(e)
  }

  // Finding 16: a surface configured on this node but never applied
  // reports zero evidence — union in every configured show.surface id,
  // even with an empty entry list, so its apply/clear/restart controls
  // render below regardless of whether the render report has ever
  // mentioned it.
  if (configured.kind === 'loaded') {
    for (const id of configured.surfaceIds) {
      if (!bySurface.has(id)) {
        bySurface.set(id, [])
        order.push(id)
      }
    }
  }

  if (order.length === 0) {
    if (configured.kind === 'loading') {
      return (
        <p className="text-muted" role="status">
          Loading configured surfaces for this node…
        </p>
      )
    }
    // "No data yet" (this node has never published a render report AND
    // has no configured surface reachable from here) must look visibly
    // different from "still loading" (the snapshot itself has not
    // arrived) — model.connection distinguishes those two states one
    // level up (DataFreshnessNotice already renders the loading case).
    return (
      <p className="text-muted" role="status">
        This node has never published a render report — no surface is configured on it, or
        its agent has not reported in yet.
      </p>
    )
  }

  return (
    <>
      {configured.kind === 'error' && (
        <p role="alert" className="render-surface__error">
          Could not check for configured surfaces with no render report yet: {configured.message}.
          Surfaces already reporting evidence still show below.
        </p>
      )}
      {order.map((surfaceId) => {
        const surfaceEntries = bySurface.get(surfaceId)!
        return (
          <div key={surfaceId} className="render-surface">
            <h4 className="panel__subtitle">Surface: {surfaceId}</h4>
            {surfaceEntries.length === 0 ? (
              <p className="text-muted" role="status">
                Configured for this node — never applied, so there is no render report yet.
              </p>
            ) : (
              surfaceEntries.map((entry) => (
                <EvidenceValue
                  key={entry.signal}
                  label={entry.signal}
                  evidence={entry}
                  serverTime={model.serverTime}
                  serverTimeReceivedAt={model.serverTimeReceivedAt}
                  connected={connected}
                />
              ))
            )}
            <RenderSurfaceControls nodeId={nodeId} surfaceId={surfaceId} />
          </div>
        )
      })}
    </>
  )
}

type Verb = 'apply' | 'clear' | 'restart' | 'probe'

type CallState =
  | { kind: 'idle' }
  | { kind: 'submitting'; verb: Verb }
  | { kind: 'result'; verb: Verb; result: RenderCommandResult }
  | { kind: 'error'; verb: Verb; message: string }

// RenderSurfaceControls holds the apply/clear/restart buttons for one
// surface. "apply" needs a sequenceId the operator supplies (this build
// has no sequence picker yet — G-8/show-authoring UI is a different
// seam's scope), read from a plain text input rather than blocking this
// control on that seam landing first.
function RenderSurfaceControls({ nodeId, surfaceId }: { nodeId: string; surfaceId: string }) {
  const [sequenceId, setSequenceId] = useState('')
  const [state, setState] = useState<CallState>({ kind: 'idle' })

  async function run(verb: Verb): Promise<void> {
    if (state.kind === 'submitting') return
    setState({ kind: 'submitting', verb })
    try {
      const result =
        verb === 'apply'
          ? await applyRenderSurface(nodeId, surfaceId, sequenceId)
          : verb === 'clear'
            ? await clearRenderSurface(nodeId, surfaceId)
            : verb === 'restart'
              ? await restartRenderPipeline(nodeId, surfaceId)
              : await probeRenderTransport(nodeId, surfaceId)
      setState({ kind: 'result', verb, result })
    } catch (err) {
      setState({ kind: 'error', verb, message: describeApiError(err) })
    }
  }

  const submitting = state.kind === 'submitting'

  return (
    <div className="render-surface__controls">
      <label className="text-muted">
        Sequence ID for apply:{' '}
        <input
          type="text"
          value={sequenceId}
          onChange={(e) => setSequenceId(e.target.value)}
          placeholder="e.g. opener"
        />
      </label>
      <div className="render-surface__buttons">
        <ScopedButton
          requiredScope="render:command"
          onClick={() => void run('apply')}
          busy={submitting && state.verb === 'apply'}
          busyReason="Applying…"
        >
          Apply
        </ScopedButton>
        <ScopedButton
          requiredScope="render:command"
          onClick={() => void run('clear')}
          busy={submitting && state.verb === 'clear'}
          busyReason="Clearing…"
        >
          Clear
        </ScopedButton>
        <ScopedButton
          requiredScope="render:command"
          onClick={() => void run('restart')}
          busy={submitting && state.verb === 'restart'}
          busyReason="Restarting…"
        >
          Restart
        </ScopedButton>
        <ScopedButton
          requiredScope="render:command"
          onClick={() => void run('probe')}
          busy={submitting && state.verb === 'probe'}
          busyReason="Probing…"
        >
          Probe transport
        </ScopedButton>
      </div>
      {state.kind === 'result' && <RenderCommandOutcome result={state.result} />}
      {state.kind === 'error' && (
        <p role="alert" className="render-surface__error">
          {state.message}
        </p>
      )}
    </div>
  )
}

// RenderCommandOutcome renders result.outcome literally, never inferring
// success from a bare 200 (ADR-003) — the same rule
// FPPStopPlaylistControl's own FPPCommandOutcome enforces for FPP.
function RenderCommandOutcome({ result }: { result: RenderCommandResult }) {
  return (
    <div role="status">
      {result.replay && (
        <p className="text-muted">
          This was already requested (idempotency key already used) — nothing new was
          dispatched; showing the original result.
        </p>
      )}
      {result.outcome === 'confirmed' && (
        <p className="render-surface__confirmed">Confirmed: {result.outcomeReason}</p>
      )}
      {result.outcome === 'unconfirmed' && (
        <p role="alert" className="render-surface__unconfirmed">
          Unconfirmed: {result.outcomeReason}
        </p>
      )}
      {result.outcome !== 'confirmed' && result.outcome !== 'unconfirmed' && (
        <p className="text-muted">Pending: this command has not yet resolved.</p>
      )}
    </div>
  )
}
