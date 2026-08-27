import { useEffect, useState, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { getShow, listAssets, listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import type { ConfigObjectSummary, Asset, ShowConfigResponse } from '../app/types'
import '../styles/operator-pages.css'
import { showWorkspacePath, type ShowWorkspaceSection } from './showWorkspacePaths'

/**
 * Canonical destinations for the show authoring workspace. App.tsx owns the
 * route declarations. Keeping them here means every object list and detail
 * page uses the same show-local vocabulary while the existing global deep
 * links remain available.
 */
const SECTIONS: Array<{ id: ShowWorkspaceSection; label: string; description: string }> = [
  { id: 'overview', label: 'Overview', description: 'Show identity and authoring status' },
  { id: 'run-of-show', label: 'Run of Show', description: 'Ordered presentation plan' },
  { id: 'cues', label: 'Cues', description: 'Render, audio, LTC, and announcements' },
  { id: 'assets', label: 'Assets', description: 'Show-scoped media and render files' },
  { id: 'automation', label: 'Automation', description: 'Actions and macros' },
  { id: 'presentation', label: 'Presentation', description: 'Surfaces and output assignments' },
  { id: 'show-night', label: 'Show Night', description: 'Transition Steps and live state' },
  { id: 'readiness', label: 'Readiness', description: 'Evidence before starting' },
]

export function ShowWorkspaceTabs({ showId, active }: { showId: string; active: ShowWorkspaceSection }) {
  return (
    <nav className="show-workspace__nav" aria-label="Show workspace">
      <ul>
        {SECTIONS.map((section) => (
          <li key={section.id}>
            <Link
              className="show-workspace__nav-link"
              to={showWorkspacePath(showId, section.id)}
              aria-current={section.id === active ? 'page' : undefined}
            >
              <span>
                <strong>{section.label}</strong>
                <span className="show-workspace__nav-detail">{section.description}</span>
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </nav>
  )
}

export function ShowWorkspaceFrame({
  showId,
  showName,
  active,
  children,
}: {
  showId: string
  showName?: string
  active: ShowWorkspaceSection
  children: ReactNode
}) {
  return (
    <div className="show-workspace operator-page">
      <header className="show-workspace__header">
        <div>
          <p className="settings-breadcrumb">
            <Link to="/config/show">Shows</Link> / {showName || showId}
          </p>
          <h1 className="operator-page__title">{showName || showId}</h1>
          <p className="operator-page__lede">Authoring workspace for this show. Changes create a new revision.</p>
        </div>
        <Link className="entity-link" to={`/config/show/${encodeURIComponent(showId)}`}>
          Edit show details
        </Link>
      </header>
      <ShowWorkspaceTabs showId={showId} active={active} />
      <main className="show-workspace__content">{children}</main>
    </div>
  )
}

type WorkspaceState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | {
      kind: 'loaded'
      show: ShowConfigResponse
      resources: Record<'cue' | 'playlist' | 'action' | 'surface', ConfigObjectSummary[]>
      assets: Asset[]
    }

const READ_SCOPES = ['show:macro:run', 'config:write']

const RESOURCE_CARDS: Array<{
  key: 'cue' | 'playlist' | 'action' | 'surface'
  label: string
  section: ShowWorkspaceSection
  empty: string
}> = [
  { key: 'cue', label: 'Cues', section: 'cues', empty: 'No Cues yet.' },
  { key: 'playlist', label: 'Playlists', section: 'run-of-show', empty: 'No Playlists yet.' },
  { key: 'action', label: 'Actions', section: 'automation', empty: 'No Actions yet.' },
  { key: 'surface', label: 'Surfaces', section: 'presentation', empty: 'No Surfaces yet.' },
]

export function ShowWorkspaceOverview({ showId }: { showId: string }) {
  const model = useModelContext()
  const gate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const [state, setState] = useState<WorkspaceState>({ kind: 'loading' })

  useEffect(() => {
    if (!gate.allowed) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([
      getShow(showId),
      listConfigObjects('show.cue', showId),
      listConfigObjects('show.playlist', showId),
      listConfigObjects('show.action', showId),
      listConfigObjects('show.surface', showId),
      listAssets({ show: showId }),
    ])
      .then(([show, cues, playlists, actions, surfaces, assets]) => {
        if (cancelled) return
        setState({
          kind: 'loaded',
          show,
          resources: {
            cue: cues.objects,
            playlist: playlists.objects,
            action: actions.objects,
            surface: surfaces.objects,
          },
          assets: assets.assets,
        })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [gate.allowed, showId])

  if (!gate.allowed) {
    return (
      <ShowWorkspaceFrame showId={showId} active="overview">
        <p className="panel panel--error" role="status">
          {gate.reason}
        </p>
      </ShowWorkspaceFrame>
    )
  }

  if (state.kind === 'loading') {
    return (
      <ShowWorkspaceFrame showId={showId} active="overview">
        <p className="text-muted" role="status" aria-busy="true">
          Loading show workspace…
        </p>
      </ShowWorkspaceFrame>
    )
  }

  if (state.kind === 'error') {
    return (
      <ShowWorkspaceFrame showId={showId} active="overview">
        <p className="panel panel--error" role="alert">
          Could not load this show workspace. {state.message}
        </p>
      </ShowWorkspaceFrame>
    )
  }

  return (
    <ShowWorkspaceFrame showId={showId} showName={state.show.payload.name} active="overview">
      <section className="show-workspace__intro" aria-labelledby="show-workspace-heading">
        <div>
          <h2 id="show-workspace-heading">Overview</h2>
          <p className="text-muted">
            Revision {state.show.revision}. {state.show.payload.notes || 'Add notes from Edit show details to describe this show.'}
          </p>
        </div>
        <p className="show-workspace__revision" role="status">
          {state.show.createdByPrincipalName ? `Last saved by ${state.show.createdByPrincipalName}.` : 'Last saved by the coordinator.'}
        </p>
      </section>

      <section aria-labelledby="show-workspace-resources">
        <h2 id="show-workspace-resources">Authoring resources</h2>
        <div className="show-workspace__cards">
          {RESOURCE_CARDS.map((card) => {
            const objects = state.resources[card.key]
            return (
              <Link className="show-workspace__card" key={card.key} to={showWorkspacePath(showId, card.section)}>
                <span className="show-workspace__card-label">{card.label}</span>
                <strong className="show-workspace__card-count">{objects.length}</strong>
                <span className="show-workspace__card-detail">{objects.length === 0 ? card.empty : `${objects.length} configured`}</span>
              </Link>
            )
          })}
          <Link className="show-workspace__card" to={showWorkspacePath(showId, 'assets')}>
            <span className="show-workspace__card-label">Assets</span>
            <strong className="show-workspace__card-count">{state.assets.length}</strong>
            <span className="show-workspace__card-detail">
              {state.assets.length === 0 ? 'No current assets yet.' : `${state.assets.length} show-scoped assets`}
            </span>
          </Link>
        </div>
      </section>

      <section className="show-workspace__future" aria-labelledby="show-workspace-future">
        <h2 id="show-workspace-future">Workspace modules</h2>
        <p className="text-muted">
          Run of Show, Show Night, and Readiness are visible here as distinct destinations. Their current live and readiness views remain available through the existing routes until the canonical show-local route wiring is added.
        </p>
        <div className="show-workspace__future-grid">
          <WorkspaceFutureLink showId={showId} section="run-of-show" label="Run of Show" detail="Playlist order is authored by each Playlist; a combined run editor is not available yet." />
          <WorkspaceFutureLink showId={showId} section="show-night" label="Show Night" detail="Live control is available at Show Night. Transition Step authoring is not available yet." />
          <WorkspaceFutureLink showId={showId} section="readiness" label="Readiness" detail="Readiness evidence is available for FPP Playlists. A show-wide readiness summary is not available yet." />
        </div>
      </section>
    </ShowWorkspaceFrame>
  )
}

function WorkspaceFutureLink({ showId, section, label, detail }: { showId: string; section: ShowWorkspaceSection; label: string; detail: string }) {
  return (
    <Link className="show-workspace__future-link" to={showWorkspacePath(showId, section)}>
      <strong>{label}</strong>
      <span>{detail}</span>
    </Link>
  )
}
