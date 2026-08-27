import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects, listResolumeActions } from '../api'
import type { ConfigObjectSummary, ResolumeAction } from '../app/types'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useResolumeComposition, resolumeCompositionOrNull } from '../app/useResolumeComposition'
import { AudioSessionPanel } from '../components/AudioSessionPanel'
import {
  FPPNextPlaylistItemControl,
  FPPPausePlaylistControl,
  FPPPrevPlaylistItemControl,
  FPPResumePlaylistControl,
} from '../components/FPPPlaylistTransportControls'
import { FPPStartPlaylistControl } from '../components/FPPStartPlaylistControl'
import { FPPStopPlaylistControl } from '../components/FPPStopPlaylistControl'
import { FPPStopPlaylistGracefullyControl } from '../components/FPPStopPlaylistGracefullyControl'
import { FPPSetVolumeControl } from '../components/FPPSetVolumeControl'
import { NightCommandButton } from '../components/NightCommandButton'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { RenderSurfacePanel } from '../components/RenderSurfacePanel'
import { ResolumeActionController } from '../components/ResolumeActionController'
import { RunMacroButton } from '../components/RunMacroButton'
import '../styles/operator-pages.css'

type LoadState<T> =
  | { kind: 'loading' }
  | { kind: 'loaded'; value: T }
  | { kind: 'error'; message: string }

function useMacroObjects(allowed: boolean): LoadState<ConfigObjectSummary[]> {
  const [state, setState] = useState<LoadState<ConfigObjectSummary[]>>({ kind: 'loading' })
  useEffect(() => {
    if (!allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.macro')
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', value: response.objects })
      })
      .catch((error: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(error) })
      })
    return () => {
      cancelled = true
    }
  }, [allowed])
  return state
}

function useResolumeActions(available: boolean): LoadState<ResolumeAction[]> {
  const [state, setState] = useState<LoadState<ResolumeAction[]>>({ kind: 'loading' })
  useEffect(() => {
    if (!available) return
    let cancelled = false
    setState({ kind: 'loading' })
    listResolumeActions()
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', value: response.actions })
      })
      .catch((error: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(error) })
      })
    return () => {
      cancelled = true
    }
  }, [available])
  return state
}

export function LiveControl() {
  const model = useModelContext()
  const macroRead = evaluateAnyScope(model.session, model.sessionFetchFailed, ['show:macro:run', 'config:write'])
  const macros = useMacroObjects(macroRead.allowed)
  const resolume = model.resolume[0]
  const resolumeActions = useResolumeActions(resolume !== undefined)
  const compositionState = useResolumeComposition(resolume?.composition?.name ?? null, resolume !== undefined)
  const composition = resolumeCompositionOrNull(compositionState)

  return (
    <div className="operator-page">
      <header className="operator-page__header">
        <div>
          <h1 className="operator-page__title">Live Control</h1>
          <p className="operator-page__lede text-muted">
            Dispatch the controls that are available on this coordinator. Every command reports its own confirmation or refusal.
          </p>
        </div>
        <Link className="button" to="/">
          Back to dashboard
        </Link>
      </header>

      <section aria-labelledby="live-control-status">
        <h2 id="live-control-status" className="panel__title">Control status</h2>
        <div className="operator-status-strip">
          <StatusCard label="Coordinator" value={model.connection.kind === 'live' ? 'Connected' : model.connection.kind} detail="Connection state" />
          <StatusCard label="FPP" value={model.fpp.length === 0 ? 'Unavailable' : `${model.fpp.length} configured`} detail="FPP transport controls" />
          <StatusCard label="Audio" value={model.nodes.some((node) => node.audio.length > 0) ? 'Available' : 'Unobserved'} detail="Session evidence required" />
          <StatusCard label="Resolume" value={resolume === undefined ? 'Unavailable' : 'Available'} detail="Capability-driven" />
        </div>
      </section>

      <section aria-labelledby="fpp-controls">
        <h2 id="fpp-controls" className="panel__title">FPP transport</h2>
        {model.fpp.length === 0 ? (
          <Unavailable title="FPP transport" reason="No FPP instance is configured on this coordinator." />
        ) : (
          <div className="operator-control-grid">
            {model.fpp.map((instance) => (
              <PanelErrorBoundary key={instance.instanceId} panelLabel={`FPP controls for ${instance.instanceId}`}>
                <article className="operator-control-card">
                  <h3>{instance.instanceId}</h3>
                  <p className="text-muted">Commands remain scope-gated and report confirmation below each control.</p>
                  <div className="operator-control-card__actions">
                    <FPPStartPlaylistControl instanceId={instance.instanceId} />
                    <FPPStopPlaylistControl instanceId={instance.instanceId} />
                    <FPPStopPlaylistGracefullyControl instanceId={instance.instanceId} />
                    <FPPPausePlaylistControl instanceId={instance.instanceId} />
                    <FPPResumePlaylistControl instanceId={instance.instanceId} />
                    <FPPPrevPlaylistItemControl instanceId={instance.instanceId} />
                    <FPPNextPlaylistItemControl instanceId={instance.instanceId} observations={instance.observations} />
                    <FPPSetVolumeControl instanceId={instance.instanceId} />
                  </div>
                </article>
              </PanelErrorBoundary>
            ))}
          </div>
        )}
      </section>

      <section aria-labelledby="night-controls">
        <h2 id="night-controls" className="panel__title">Show Night controls</h2>
        <div className="operator-control-card operator-control-card--wide">
          <p className="text-muted">Lifecycle controls are always visible. They are disabled with a reason when permission or current evidence is insufficient.</p>
          <div className="operator-control-card__actions">
            <NightCommandButton command="prepare-site" label="Prepare site" onApplied={() => undefined} />
            <NightCommandButton command="run-readiness" label="Run readiness" onApplied={() => undefined} />
            <NightCommandButton command="start-preshow" label="Start preshow" onApplied={() => undefined} />
            <NightCommandButton command="start-night" label="Start night" onApplied={() => undefined} />
            <NightCommandButton command="request-final-show" label="Request final show" onApplied={() => undefined} />
            <NightCommandButton command="fade-out-night" label="Fade out night" onApplied={() => undefined} />
            <NightCommandButton command="power-down-presentation" label="Power down presentation" onApplied={() => undefined} />
          </div>
        </div>
      </section>

      <div className="operator-page__columns">
        <section aria-labelledby="node-controls">
          <h2 id="node-controls" className="panel__title">Node controls</h2>
          {model.nodes.length === 0 ? (
            <Unavailable title="Node controls" reason="No nodes are currently observed." />
          ) : (
            <div className="operator-control-grid">
              {model.nodes.map((node) => (
                <PanelErrorBoundary key={node.nodeId} panelLabel={`Controls for ${node.label ?? node.nodeId}`}>
                  <article className="operator-control-card">
                    <h3>{node.label ?? node.nodeId}</h3>
                    <AudioSessionPanel nodeId={node.nodeId} entries={node.audio} />
                    <RenderSurfacePanel nodeId={node.nodeId} entries={node.render} />
                  </article>
                </PanelErrorBoundary>
              ))}
            </div>
          )}
        </section>

        <section aria-labelledby="resolume-controls">
          <h2 id="resolume-controls" className="panel__title">Resolume controls</h2>
          {resolume === undefined ? (
            <Unavailable title="Resolume controls" reason="Resolume is not configured on this coordinator." />
          ) : resolumeActions.kind === 'error' ? (
            <p className="panel panel--error" role="alert">{resolumeActions.message}</p>
          ) : resolumeActions.kind !== 'loaded' ? (
            <p className="text-muted" role="status">Loading Resolume actions…</p>
          ) : compositionState.kind !== 'loaded' ? (
            <Unavailable title="Resolume controls" reason={compositionState.kind === 'loading' ? 'Loading the stored composition…' : 'No usable composition is available.'} />
          ) : (
            <div className="operator-control-card">
              <p className="text-muted">Available actions are populated from the stored composition.</p>
              <ResolumeActionController actions={resolumeActions.value} composition={composition} />
            </div>
          )}
        </section>
      </div>

      <section aria-labelledby="macro-controls">
        <h2 id="macro-controls" className="panel__title">Show actions</h2>
        {!macroRead.allowed ? (
          <Unavailable title="Show actions" reason={macroRead.reason} />
        ) : macros.kind === 'loading' ? (
          <p className="text-muted" role="status">Loading available show actions…</p>
        ) : macros.kind === 'error' ? (
          <p className="panel panel--error" role="alert">{macros.message}</p>
        ) : macros.value.length === 0 ? (
          <Unavailable title="Show actions" reason="No show macros are configured yet." />
        ) : (
          <div className="operator-control-grid">
            {macros.value.map((macro) => (
              <article key={macro.id} className="operator-control-card">
                <h3>{macro.label || macro.id}</h3>
                <span className="operator-list__meta">Revision {macro.currentRevision}</span>
                <RunMacroButton macroId={macro.id} />
              </article>
            ))}
          </div>
        )}
      </section>

      <section aria-labelledby="unavailable-controls">
        <h2 id="unavailable-controls" className="panel__title">Not available on this coordinator</h2>
        <div className="operator-summary-grid">
          <Unavailable title="Brightness ceiling" reason="No brightness control capability is advertised." />
          <Unavailable title="Site control" reason="No site-control capability is advertised." />
          <Unavailable title="Interlocks" reason="No interlock capability is advertised." />
          <Unavailable title="Global emergency stop" reason="No coordinator-wide stop capability is advertised." />
        </div>
      </section>
    </div>
  )
}

function StatusCard({ label, value, detail }: { label: string; value: string; detail: string }) {
  return (
    <div className="operator-status-card">
      <span className="operator-status-card__label">{label}</span>
      <span className="operator-status-card__value">{value}</span>
      <span className="operator-status-card__detail">{detail}</span>
    </div>
  )
}

function Unavailable({ title, reason }: { title: string; reason: string }) {
  return (
    <article className="operator-control-card">
      <h3>{title}</h3>
      <p className="text-muted" role="status">Unavailable: {reason}</p>
    </article>
  )
}
