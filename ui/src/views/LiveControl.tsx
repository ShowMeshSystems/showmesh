import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects, listResolumeActions } from '../api'
import type { ConfigObjectSummary, ResolumeAction } from '../app/types'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useResolumeComposition, resolumeCompositionOrNull } from '../app/useResolumeComposition'
import { AudioSessionPanel } from '../components/AudioSessionPanel'
import { ActionInvokeButton } from '../components/ActionInvokeButton'
import { DataFreshnessNotice } from '../components/DataFreshnessNotice'
import {
  FPPNextPlaylistItemControl,
  FPPPausePlaylistControl,
  FPPPrevPlaylistItemControl,
  FPPResumePlaylistControl,
} from '../components/FPPPlaylistTransportControls'
import { FPPStopPlaylistControl } from '../components/FPPStopPlaylistControl'
import { FPPStopPlaylistGracefullyControl } from '../components/FPPStopPlaylistGracefullyControl'
import { FPPSetVolumeControl } from '../components/FPPSetVolumeControl'
import { PanelErrorBoundary } from '../components/PanelErrorBoundary'
import { RenderSurfacePanel } from '../components/RenderSurfacePanel'
import { ResolumeActionController } from '../components/ResolumeActionController'
import { RunMacroButton } from '../components/RunMacroButton'
import { ShowModePanel } from '../components/ShowModePanel'
import { OperatorPageHeader, OperatorSection, PlannedFeature, UnavailableBlock, UnobservedBlock } from '../components/SharedLayouts'
import { AnnouncementsPanel } from './operate/AnnouncementsPanel'
import { NightLifecycleCommands } from './operate/NightLifecycleCommands'
import { OutputsTable } from './operate/OutputsTable'
import '../styles/operator-pages.css'
import '../styles/operate.css'

type LoadState<T> = { kind: 'loading' } | { kind: 'loaded'; value: T } | { kind: 'error'; message: string }

function useMacroObjects(allowed: boolean): LoadState<ConfigObjectSummary[]> {
  const [state, setState] = useState<LoadState<ConfigObjectSummary[]>>({ kind: 'loading' })
  useEffect(() => {
    if (!allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.macro')
      .then((response) => { if (!cancelled) setState({ kind: 'loaded', value: response.objects }) })
      .catch((error: unknown) => { if (!cancelled) setState({ kind: 'error', message: describeApiError(error) }) })
    return () => { cancelled = true }
  }, [allowed])
  return state
}

function useShowActionObjects(allowed: boolean): LoadState<ConfigObjectSummary[]> {
  const [state, setState] = useState<LoadState<ConfigObjectSummary[]>>({ kind: 'loading' })
  useEffect(() => {
    if (!allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    listConfigObjects('show.action')
      .then((response) => { if (!cancelled) setState({ kind: 'loaded', value: response.objects }) })
      .catch((error: unknown) => { if (!cancelled) setState({ kind: 'error', message: describeApiError(error) }) })
    return () => { cancelled = true }
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
      .then((response) => { if (!cancelled) setState({ kind: 'loaded', value: response.actions }) })
      .catch((error: unknown) => { if (!cancelled) setState({ kind: 'error', message: describeApiError(error) }) })
    return () => { cancelled = true }
  }, [available])
  return state
}

function TransportSection({ model }: { model: ReturnType<typeof useModelContext> }) {
  const [selected, setSelected] = useState<string | null>(null)
  const instanceId = selected ?? model.fpp[0]?.instanceId ?? null

  if (model.snapshotReceivedAt === null) {
    return <UnobservedBlock title="Transport" reason="No coordinator snapshot has been received, so FPP playback capability is not yet observed." headingLevel={3} />
  }
  if (model.fpp.length === 0) {
    return <UnavailableBlock title="Transport" reason="No FPP instance is configured on this coordinator." headingLevel={3} />
  }
  return (
    <div>
      {model.fpp.length > 1 && (
        <div className="segmented transport-instance-tabs" role="tablist" aria-label="FPP instance">
          {model.fpp.map((instance) => (
            <button
              key={instance.instanceId}
              type="button"
              className="segmented__option"
              aria-pressed={instance.instanceId === instanceId}
              onClick={() => setSelected(instance.instanceId)}
            >
              {instance.instanceId}
            </button>
          ))}
        </div>
      )}
      {instanceId !== null && (
        <PanelErrorBoundary panelLabel={`Transport for ${instanceId}`}>
          <div className="transport-rack" aria-label={`Transport controls for ${instanceId}`}>
            <FPPPrevPlaylistItemControl instanceId={instanceId} />
            <FPPPausePlaylistControl instanceId={instanceId} />
            <FPPResumePlaylistControl instanceId={instanceId} />
            <FPPNextPlaylistItemControl instanceId={instanceId} observations={model.fpp.find((i) => i.instanceId === instanceId)?.observations ?? []} />
            <FPPStopPlaylistGracefullyControl instanceId={instanceId} />
            <FPPStopPlaylistControl instanceId={instanceId} />
            <FPPSetVolumeControl instanceId={instanceId} />
          </div>
        </PanelErrorBoundary>
      )}
      <p className="absence-notice t-small">
        This coordinator advertises no installation-wide emergency stop. Stop now above halts this player only;
        projection and audio hold their last state until their own cues run.
      </p>
    </div>
  )
}

export function LiveControl() {
  const model = useModelContext()
  const macroRead = evaluateAnyScope(model.session, model.sessionFetchFailed, ['show:macro:run', 'config:write'])
  const macros = useMacroObjects(macroRead.allowed)
  const actions = useShowActionObjects(macroRead.allowed)
  const resolume = model.resolume[0]
  const resolumeActions = useResolumeActions(resolume !== undefined)
  const compositionState = useResolumeComposition(resolume?.composition?.name ?? null, resolume !== undefined)
  const composition = resolumeCompositionOrNull(compositionState)
  const macroActionLoadError = macros.kind === 'error' ? macros.message : actions.kind === 'error' ? actions.message : null
  const macroObjects = macros.kind === 'loaded' ? macros.value : []
  const actionObjects = actions.kind === 'loaded' ? actions.value : []
  const activeShowId = model.currentRuns?.activeShow.configured === true ? (model.currentRuns.activeShow.show ?? null) : null

  return (
    <div className="operator-page live-control-page">
      <OperatorPageHeader
        title="Live Control"
        lede="Acting on the show that is running now. A command is not successful because it was sent — each one reports the evidence that it took effect, or why it did not."
        actions={<Link className="button button--secondary" to="/">Back to dashboard</Link>}
      />

      <DataFreshnessNotice connection={model.connection} snapshotReceivedAt={model.snapshotReceivedAt} />

      <OperatorSection title="Transport" aria-labelledby="transport-controls">
        <TransportSection model={model} />
      </OperatorSection>

      <OperatorSection title="What each output is doing" detail="For the cue that is playing now" aria-labelledby="outputs-status">
        <OutputsTable nodes={model.nodes} serverTime={model.serverTime} serverTimeReceivedAt={model.serverTimeReceivedAt} snapshotReceivedAt={model.snapshotReceivedAt} />
      </OperatorSection>

      <OperatorSection title="Night lifecycle" detail={<Link to="/night">Show Night →</Link>} aria-labelledby="night-controls">
        <NightLifecycleCommands />
      </OperatorSection>

      <OperatorSection title="Macros" detail={`Configured show.macro objects · ${macroObjects.length}`} aria-labelledby="macro-controls">
        {!macroRead.allowed ? (
          <UnavailableBlock title="Macros" reason={macroRead.reason} headingLevel={3} />
        ) : macros.kind === 'loading' ? (
          <p className="text-muted" role="status">Loading configured macros…</p>
        ) : macros.kind === 'error' ? (
          <p className="section-notice notice--error" role="alert">{macros.message}</p>
        ) : macroObjects.length === 0 ? (
          <UnavailableBlock title="Macros" reason="No configured macro is available here." headingLevel={3} />
        ) : (
          <div className="live-control-groups">
            {macroObjects.map((macro) => (
              <article key={macro.id} className="live-control-group">
                <h3>{macro.label || macro.id}</h3>
                <span className="operator-list__meta">Revision {macro.currentRevision}</span>
                <RunMacroButton macroId={macro.id} />
              </article>
            ))}
          </div>
        )}
      </OperatorSection>

      <OperatorSection
        title="Announcements"
        detail="Directly activatable. They duck or interrupt the background bed and leave FPP alone. Firing one does not advance the run."
        aria-labelledby="announcement-controls"
      >
        <AnnouncementsPanel showId={activeShowId} />
      </OperatorSection>

      <OperatorSection title="Actions" detail={`Exposed show.action objects · ${actionObjects.length}. One integration command each; macros are built from these.`} aria-labelledby="action-controls">
        {!macroRead.allowed ? (
          <UnavailableBlock title="Actions" reason={macroRead.reason} headingLevel={3} />
        ) : actions.kind === 'loading' ? (
          <p className="text-muted" role="status">Loading exposed Actions…</p>
        ) : actions.kind === 'error' ? (
          <p className="section-notice notice--error" role="alert">{macroActionLoadError}</p>
        ) : actionObjects.length === 0 ? (
          <UnavailableBlock title="Actions" reason="No coordinator-exposed Action is available here." headingLevel={3} />
        ) : (
          <div className="live-control-groups">
            {actionObjects.map((action) => (
              <article key={action.id} className="live-control-group">
                <h3>{action.label || action.id}</h3>
                <span className="operator-list__meta">Revision {action.currentRevision}</span>
                <ActionInvokeButton actionId={action.id} label="Invoke" />
              </article>
            ))}
          </div>
        )}
      </OperatorSection>

      <OperatorSection title="Resolume" aria-labelledby="resolume-controls">
        {model.snapshotReceivedAt === null ? (
          <UnobservedBlock title="Resolume" reason="No coordinator snapshot has been received, so Resolume capability is not yet observed." headingLevel={3} />
        ) : resolume === undefined ? (
          <UnavailableBlock title="Resolume" reason="Resolume is not configured on this coordinator." headingLevel={3} />
        ) : resolumeActions.kind === 'error' ? (
          <p className="section-notice notice--error" role="alert">{resolumeActions.message}</p>
        ) : resolumeActions.kind !== 'loaded' ? (
          <p className="text-muted" role="status">Loading Resolume actions…</p>
        ) : compositionState.kind !== 'loaded' ? (
          <UnavailableBlock title="Resolume" reason={compositionState.kind === 'loading' ? 'Loading the stored composition…' : 'No usable composition is available.'} headingLevel={3} />
        ) : (
          <ResolumeActionController actions={resolumeActions.value} composition={composition} />
        )}
      </OperatorSection>

      <OperatorSection title="Audio and render-node controls" aria-labelledby="node-controls">
        {model.snapshotReceivedAt === null ? (
          <UnobservedBlock title="Audio and render-node controls" reason="No coordinator snapshot has been received, so node control capability is not yet observed." headingLevel={3} />
        ) : model.nodes.length === 0 ? (
          <UnavailableBlock title="Audio and render-node controls" reason="No nodes are currently observed." headingLevel={3} />
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
      </OperatorSection>

      <OperatorSection title="Active show and mode" aria-labelledby="show-selection-controls">
        <div className="live-control-groups">
          <article className="live-control-group">
            <h3>Active show</h3>
            <p className="text-muted">{activeShowDetail(model)}</p>
            <span className="scoped-button">
              <button type="button" className="btn btn--secondary" disabled={true} aria-disabled="true" title="Active-show selection has no route yet; the owner is placing the night-session definition editor that will host it.">
                Select active show
              </button>
              <span className="scoped-button__reason">
                Active-show selection has no route yet; the owner is placing the night-session definition editor that will host it.
              </span>
            </span>
          </article>
          <article className="live-control-group">
            <ShowModePanel headingLevel={3} />
          </article>
        </div>
      </OperatorSection>

      <OperatorSection title="Not available on this coordinator" aria-labelledby="unavailable-controls">
        <div className="live-control-unavailable">
          <PlannedFeature
            title="Installation-wide emergency stop"
            why="This coordinator advertises no installation-wide stop capability, and there is no command to send. Planned shape: in the top bar, instant, no confirmation dialog, and a double press to arm it."
            preview={
              <button type="button" className="btn btn--destructive btn--gloved" tabIndex={-1}>
                EMERGENCY STOP
              </button>
            }
          />
          <PlannedFeature
            title="Brightness ceiling"
            why="The schema carries no brightness field at all today; no capability or config field exists to hold a ceiling or multiplier."
          />
          <PlannedFeature
            title="Site control"
            why="Site control is specified but not implemented. There is nothing to read its state from and no command to change it."
          />
          <PlannedFeature
            title="Interlock authoring"
            why="Interlocks can be read on a night session, but nothing accepts a change to them, so there is no way to author one from here."
          />
        </div>
      </OperatorSection>
    </div>
  )
}

function activeShowDetail(model: ReturnType<typeof useModelContext>): string {
  if (model.currentRuns === null) {
    return model.currentRunsFetchFailed
      ? 'Unavailable: the coordinator current-runs projection could not be read.'
      : 'Unobserved: waiting for the coordinator current-runs projection.'
  }
  if (!model.currentRuns.activeShow.configured || model.currentRuns.activeShow.show === null) {
    return 'No active show is configured in the current coordinator projection.'
  }
  return `Active: ${model.currentRuns.activeShow.show} (generation ${model.currentRuns.activeShow.generation ?? 'not reported'}). Evidence: ${currentRunsEvidenceDetail(model)}.`
}

function currentRunsEvidenceDetail(model: ReturnType<typeof useModelContext>): string {
  if (model.connection.kind !== 'live') return `last known current-runs projection while the browser is ${model.connection.kind}`
  if (model.currentRunsReceivedAt === null) return 'current-runs projection with no browser receipt time recorded'
  return 'current-runs projection received by this browser'
}
