import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects, listResolumeActions } from '../api'
import type { ConfigObjectSummary, ResolumeAction } from '../app/types'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useResolumeComposition, resolumeCompositionOrNull } from '../app/useResolumeComposition'
import { AudioSessionPanel } from '../components/AudioSessionPanel'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
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
import { CommandGroup, OperatorPageHeader, OperatorSection, StatusStrip, StatusStripItem, UnavailableBlock } from '../components/SharedLayouts'
import '../styles/operator-pages.css'

type LoadState<T> = { kind: 'loading' } | { kind: 'loaded'; value: T } | { kind: 'error'; message: string }

function useMacroObjects(allowed: boolean): LoadState<ConfigObjectSummary[]> {
  const [state, setState] = useState<LoadState<ConfigObjectSummary[]>>({
    kind: 'loading',
  })
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
  const [state, setState] = useState<LoadState<ResolumeAction[]>>({
    kind: 'loading',
  })
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
  const fppStatus = fppControlStatus(model)
  const audioStatus = audioControlStatus(model)
  const resolumeStatus = resolumeControlStatus(model)

  return (
    <div className="operator-page live-control-page">
      <OperatorPageHeader
        title="Live Control"
        lede="Dispatch the controls that are available on this coordinator. Every command reports its own confirmation or refusal."
        actions={<Link className="button button--secondary" to="/">
          Back to dashboard
        </Link>}
      />

      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />

      <OperatorSection title="Control status" detail="What this coordinator can currently confirm" aria-labelledby="live-control-status">
        <div className="live-control-status-region">
          <StatusStrip label="Control status">
            <StatusStripItem
              label="Coordinator"
              detail="Connection state"
              tone={model.connection.kind === 'live' ? 'good' : 'unknown'}
            >{model.connection.kind === 'live' ? 'Connected' : model.connection.kind}</StatusStripItem>
            <StatusStripItem
              label="FPP"
              detail={fppStatus.detail}
              tone={fppStatus.tone === 'good' ? 'good' : fppStatus.tone === 'bad' ? 'bad' : 'unknown'}
            >{fppStatus.value}</StatusStripItem>
            <StatusStripItem
              label="Audio"
              detail={audioStatus.detail}
              tone={audioStatus.tone === 'good' ? 'good' : audioStatus.tone === 'bad' ? 'bad' : 'unknown'}
            >{audioStatus.value}</StatusStripItem>
            <StatusStripItem
              label="Resolume"
              detail={resolumeStatus.detail}
              tone={resolumeStatus.tone === 'good' ? 'good' : resolumeStatus.tone === 'bad' ? 'bad' : 'unknown'}
            >{resolumeStatus.value}</StatusStripItem>
          </StatusStrip>
        </div>
      </OperatorSection>

      <section className="live-control-section" aria-labelledby="fpp-controls">
        <div className="live-control-section__heading">
          <h2 id="fpp-controls">FPP transport</h2>
          <p>Playlist and output controls</p>
        </div>
        {model.fpp.length === 0 ? (
          <Unavailable title="FPP transport" reason="No FPP instance is configured on this coordinator." />
        ) : (
          <div className="live-control-groups">
            {model.fpp.map((instance) => (
              <PanelErrorBoundary key={instance.instanceId} panelLabel={`FPP controls for ${instance.instanceId}`}>
                <CommandGroup title={instance.instanceId} detail="Commands remain scope-gated and report confirmation below each control.">
                  <div className="live-control-command-rack" aria-label={`FPP commands for ${instance.instanceId}`}>
                    <FPPStartPlaylistControl instanceId={instance.instanceId} />
                    <FPPStopPlaylistControl instanceId={instance.instanceId} />
                    <FPPStopPlaylistGracefullyControl instanceId={instance.instanceId} />
                    <FPPPausePlaylistControl instanceId={instance.instanceId} />
                    <FPPResumePlaylistControl instanceId={instance.instanceId} />
                    <FPPPrevPlaylistItemControl instanceId={instance.instanceId} />
                    <FPPNextPlaylistItemControl instanceId={instance.instanceId} observations={instance.observations} />
                    <FPPSetVolumeControl instanceId={instance.instanceId} />
                  </div>
                </CommandGroup>
              </PanelErrorBoundary>
            ))}
          </div>
        )}
      </section>

      <section className="live-control-section" aria-labelledby="night-controls">
        <div className="live-control-section__heading">
          <h2 id="night-controls">Show Night controls</h2>
          <p>Lifecycle controls</p>
        </div>
        <p className="section-notice">
          Lifecycle controls are always visible. They are disabled with a reason when permission or current evidence is insufficient.
        </p>
        <div className="live-control-command-rack" aria-label="Show Night lifecycle actions">
          <NightCommandButton command="prepare-site" label="Prepare site" />
          <NightCommandButton command="run-readiness" label="Run readiness" />
          <NightCommandButton command="start-preshow" label="Start preshow" />
          <NightCommandButton command="start-night" label="Start night" />
          <NightCommandButton command="request-final-show" label="Request final show" />
          <NightCommandButton command="fade-out-night" label="Fade out night" />
          <NightCommandButton command="power-down-presentation" label="Power down presentation" />
        </div>
      </section>

      <div className="live-control-columns">
        <section className="live-control-section" aria-labelledby="node-controls">
          <div className="live-control-section__heading">
            <h2 id="node-controls">Node controls</h2>
            <p>Audio and rendering sessions</p>
          </div>
          {model.nodes.length === 0 ? (
            <Unavailable title="Node controls" reason="No nodes are currently observed." />
          ) : (
            <div className="live-control-groups">
              {model.nodes.map((node) => (
                <PanelErrorBoundary key={node.nodeId} panelLabel={`Controls for ${node.label ?? node.nodeId}`}>
                  <article className="live-control-group">
                    <h3>{node.label ?? node.nodeId}</h3>
                    <AudioSessionPanel nodeId={node.nodeId} entries={node.audio} />
                    <RenderSurfacePanel nodeId={node.nodeId} entries={node.render} />
                  </article>
                </PanelErrorBoundary>
              ))}
            </div>
          )}
        </section>

        <section className="live-control-section" aria-labelledby="resolume-controls">
          <div className="live-control-section__heading">
            <h2 id="resolume-controls">Resolume controls</h2>
            <p>Composition actions</p>
          </div>
          {resolume === undefined ? (
            <Unavailable title="Resolume controls" reason="Resolume is not configured on this coordinator." />
          ) : resolumeActions.kind === 'error' ? (
            <p className="section-notice notice--error" role="alert">
              {resolumeActions.message}
            </p>
          ) : resolumeActions.kind !== 'loaded' ? (
            <p className="text-muted" role="status">
              Loading Resolume actions…
            </p>
          ) : compositionState.kind !== 'loaded' ? (
            <Unavailable
              title="Resolume controls"
              reason={compositionState.kind === 'loading' ? 'Loading the stored composition…' : 'No usable composition is available.'}
            />
          ) : (
            <div className="live-control-group">
              <p className="text-muted">Available actions are populated from the stored composition.</p>
              <ResolumeActionController actions={resolumeActions.value} composition={composition} />
            </div>
          )}
        </section>
      </div>

      <section className="live-control-section" aria-labelledby="macro-controls">
        <div className="live-control-section__heading">
          <h2 id="macro-controls">Show actions</h2>
          <p>Configured macros</p>
        </div>
        {!macroRead.allowed ? (
          <Unavailable title="Show actions" reason={macroRead.reason} />
        ) : macros.kind === 'loading' ? (
          <p className="text-muted" role="status">
            Loading available show actions…
          </p>
        ) : macros.kind === 'error' ? (
          <p className="section-notice notice--error" role="alert">
            {macros.message}
          </p>
        ) : macros.value.length === 0 ? (
          <Unavailable title="Show actions" reason="No show macros are configured yet." />
        ) : (
          <div className="live-control-groups">
            {macros.value.map((macro) => (
              <article key={macro.id} className="live-control-group">
                <h3>{macro.label || macro.id}</h3>
                <span className="operator-list__meta">Revision {macro.currentRevision}</span>
                <RunMacroButton macroId={macro.id} />
              </article>
            ))}
          </div>
        )}
      </section>

      <section className="live-control-section" aria-labelledby="unavailable-controls">
        <div className="live-control-section__heading">
          <h2 id="unavailable-controls">Not available on this coordinator</h2>
          <p>Capabilities not advertised here</p>
        </div>
        <div className="live-control-unavailable">
          <Unavailable title="Brightness ceiling" reason="No brightness control capability is advertised." />
          <Unavailable title="Site control" reason="No site-control capability is advertised." />
          <Unavailable title="Interlocks" reason="No interlock capability is advertised." />
          <Unavailable title="Global emergency stop" reason="No coordinator-wide stop capability is advertised." />
        </div>
      </section>
    </div>
  )
}

type StatusTone = 'good' | 'unknown' | 'bad'

type ControlStatus = { value: string; detail: string; tone: StatusTone }

function snapshotStatus(model: ReturnType<typeof useModelContext>): 'unobserved' | 'stale' | 'live' {
  if (model.snapshotReceivedAt === null) return 'unobserved'
  return model.connection.kind === 'live' ? 'live' : 'stale'
}

function fppControlStatus(model: ReturnType<typeof useModelContext>): ControlStatus {
  if (model.fpp.length === 0) return { value: 'Unavailable', detail: 'No FPP instance is configured', tone: 'unknown' }
  if (snapshotStatus(model) === 'unobserved') return { value: 'Unobserved', detail: 'No coordinator snapshot received', tone: 'unknown' }
  if (snapshotStatus(model) === 'stale') return { value: 'Stale', detail: 'Last known FPP evidence', tone: 'unknown' }
  if (model.fpp.some((instance) => instance.health === 'failed')) return { value: 'Failed', detail: 'An FPP instance has failed', tone: 'bad' }
  if (model.fpp.some((instance) => instance.health !== 'healthy' && instance.health !== 'suppressed')) {
    return { value: 'Unknown', detail: 'FPP health is not healthy', tone: 'unknown' }
  }
  return { value: 'Live', detail: `${model.fpp.length} configured`, tone: 'good' }
}

function audioControlStatus(model: ReturnType<typeof useModelContext>): ControlStatus {
  const hasAudioEvidence = model.nodes.some((node) => node.audio.length > 0)
  if (!hasAudioEvidence) return { value: 'Unobserved', detail: 'Session evidence required', tone: 'unknown' }
  if (snapshotStatus(model) === 'unobserved') return { value: 'Unobserved', detail: 'No coordinator snapshot received', tone: 'unknown' }
  if (snapshotStatus(model) === 'stale') return { value: 'Stale', detail: 'Last known audio evidence', tone: 'unknown' }
  return { value: 'Live', detail: 'Current session evidence', tone: 'good' }
}

function resolumeControlStatus(model: ReturnType<typeof useModelContext>): ControlStatus {
  const resolume = model.resolume[0]
  if (resolume === undefined) return { value: 'Unavailable', detail: 'Not configured on this coordinator', tone: 'unknown' }
  if (snapshotStatus(model) === 'unobserved') return { value: 'Unobserved', detail: 'No coordinator snapshot received', tone: 'unknown' }
  if (snapshotStatus(model) === 'stale') return { value: 'Stale', detail: 'Last known Resolume evidence', tone: 'unknown' }
  if (resolume.health === 'failed') return { value: 'Failed', detail: 'The Resolume instance has failed', tone: 'bad' }
  if (resolume.health !== 'healthy' && resolume.health !== 'suppressed') {
    return { value: 'Unknown', detail: 'Resolume health is not healthy', tone: 'unknown' }
  }
  return { value: 'Live', detail: 'Current coordinator evidence', tone: 'good' }
}

function Unavailable({ title, reason }: { title: string; reason: string }) {
  return <UnavailableBlock title={title} reason={<>Unavailable: {reason}</>} />
}
