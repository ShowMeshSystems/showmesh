import { useState } from 'react'
import { applyRenderSurface, clearRenderSurface, restartRenderPipeline } from '../api'
import type { ObservationEntry, RenderCommandResult } from '../api'
import { useModelContext } from '../app/ModelContext'
import { describeApiError } from '../app/session'
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

export function RenderSurfacePanel({ nodeId, entries }: RenderSurfacePanelProps) {
  const model = useModelContext()
  const connected = model.connection.kind === 'live'

  if (entries.length === 0) {
    // "No data yet" (this node has never published a render report) must
    // look visibly different from "still loading" (the snapshot itself
    // has not arrived) — model.connection distinguishes those two states
    // one level up (DataFreshnessNotice already renders the loading
    // case), so once we reach here with a real node and an empty render
    // array, it specifically means "reported nothing, ever."
    return (
      <p className="text-muted" role="status">
        This node has never published a render report — no surface is configured on it, or
        its agent has not reported in yet.
      </p>
    )
  }

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

  return (
    <>
      {order.map((surfaceId) => (
        <div key={surfaceId} className="render-surface">
          <h4 className="panel__subtitle">Surface: {surfaceId}</h4>
          {bySurface.get(surfaceId)!.map((entry) => (
            <EvidenceValue
              key={entry.signal}
              label={entry.signal}
              evidence={entry}
              serverTime={model.serverTime}
              serverTimeReceivedAt={model.serverTimeReceivedAt}
              connected={connected}
            />
          ))}
          <RenderSurfaceControls nodeId={nodeId} surfaceId={surfaceId} />
        </div>
      ))}
    </>
  )
}

type CallState =
  | { kind: 'idle' }
  | { kind: 'submitting'; verb: 'apply' | 'clear' | 'restart' }
  | { kind: 'result'; verb: 'apply' | 'clear' | 'restart'; result: RenderCommandResult }
  | { kind: 'error'; verb: 'apply' | 'clear' | 'restart'; message: string }

// RenderSurfaceControls holds the apply/clear/restart buttons for one
// surface. "apply" needs a sequenceId the operator supplies (this build
// has no sequence picker yet — G-8/show-authoring UI is a different
// seam's scope), read from a plain text input rather than blocking this
// control on that seam landing first.
function RenderSurfaceControls({ nodeId, surfaceId }: { nodeId: string; surfaceId: string }) {
  const [sequenceId, setSequenceId] = useState('')
  const [state, setState] = useState<CallState>({ kind: 'idle' })

  async function run(verb: 'apply' | 'clear' | 'restart'): Promise<void> {
    if (state.kind === 'submitting') return
    setState({ kind: 'submitting', verb })
    try {
      const result =
        verb === 'apply'
          ? await applyRenderSurface(nodeId, surfaceId, sequenceId)
          : verb === 'clear'
            ? await clearRenderSurface(nodeId, surfaceId)
            : await restartRenderPipeline(nodeId, surfaceId)
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
