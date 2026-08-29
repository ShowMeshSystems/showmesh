import { useEffect, useState, type ReactNode } from 'react'
import { Link, Navigate } from 'react-router-dom'
import { getShow, listAssets, listConfigObjects } from '../api'
import { describeApiError, evaluateAnyScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { formatAbsolute } from '../app/time'
import type { ShowConfigResponse } from '../app/types'
import '../styles/shows.css'
import { showPath, showWorkspacePath, type ShowWorkspaceTab } from './showWorkspacePaths'

/**
 * The real nested route tree for the show authoring workspace
 * (ROUTE-MAP.md, UI-DESIGN-GUIDE.md section 3): six tabs, all under
 * `/shows/:showId/*`. No tab is a `<Navigate replace>` out to a global
 * route with a `?show=` query parameter — that scheme is gone. Screen
 * builder C (this group) owns Playlists, Cues, Presentation and Night
 * session; Assets and Automation are owned elsewhere and are only routed
 * to here, never built here.
 */
const TABS: Array<{ id: ShowWorkspaceTab; label: string }> = [
  { id: 'playlists', label: 'Playlists' },
  { id: 'cues', label: 'Cues' },
  { id: 'assets', label: 'Assets' },
  { id: 'presentation', label: 'Presentation' },
  { id: 'automation', label: 'Automation' },
  { id: 'night-sessions', label: 'Night session' },
]

const READ_SCOPES = ['show:macro:run', 'config:write']

export interface ShowWorkspaceCounts {
  playlists: number
  cues: number
  assets: number
  presentation: number
  automation: number
  'night-sessions': number
}

export type ShowWorkspaceDataState =
  | { kind: 'no_permission'; reason: string }
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; show: ShowConfigResponse; counts: ShowWorkspaceCounts }

/**
 * The one fetch every workspace tab shares: the show's identity plus the
 * inventory count of each tab's PRIMARY object (UI-DESIGN-GUIDE.md
 * section 3: "Tab counts are inventory... of the tab's primary object",
 * distinct from a rail badge's attention count). Fetched once here, not
 * once per tab, so every tab strip in the workspace renders the same
 * numbers regardless of which tab mounted it.
 */
export function useShowWorkspaceData(showId: string, enabled = true): ShowWorkspaceDataState {
  const model = useModelContext()
  const gate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const [state, setState] = useState<ShowWorkspaceDataState>({ kind: 'loading' })

  useEffect(() => {
    if (!gate.allowed || !enabled) return
    let cancelled = false
    setState({ kind: 'loading' })
    Promise.all([
      getShow(showId),
      listConfigObjects('show.playlist', showId),
      listConfigObjects('show.cue', showId),
      listConfigObjects('show.surface', showId),
      listConfigObjects('show.macro', showId),
      listAssets({ show: showId }),
      listConfigObjects('night.session', showId),
    ])
      .then(([show, playlists, cues, surfaces, macros, assets, nightSessions]) => {
        if (cancelled) return
        setState({
          kind: 'loaded',
          show,
          counts: {
            playlists: playlists.objects.length,
            cues: cues.objects.length,
            presentation: surfaces.objects.length,
            automation: macros.objects.length,
            assets: assets.assets.length,
            'night-sessions': nightSessions.objects.length,
          },
        })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [gate.allowed, showId, enabled])

  if (!gate.allowed) return { kind: 'no_permission', reason: gate.reason }
  return state
}

export function ShowWorkspaceTabs({
  showId,
  active,
  counts,
}: {
  showId: string
  active: ShowWorkspaceTab
  counts?: ShowWorkspaceCounts | undefined
}) {
  return (
    <nav className="tabs" aria-label="Show workspace">
      {TABS.map((tab) => {
        const isActive = tab.id === active
        const count = counts?.[tab.id]
        return (
          <Link
            key={tab.id}
            to={showWorkspacePath(showId, tab.id)}
            className="tabs__item"
            aria-current={isActive ? 'page' : undefined}
          >
            {tab.label}
            {count !== undefined && <span className="tabs__count">{count}</span>}
          </Link>
        )
      })}
    </nav>
  )
}

/**
 * The chrome every workspace tab shares: breadcrumb back to the Shows
 * list, the show's identity, and the tab strip. `active` decides which
 * tab reads current; `children` is the tab's own panel content. This
 * frame stays mounted the whole time an operator works inside one show,
 * which is the structural point of real nesting: the show and its tab
 * strip are never left behind by a tab's own navigation.
 */
export function ShowWorkspaceFrame({
  showId,
  active,
  data,
  children,
}: {
  showId: string
  active: ShowWorkspaceTab
  data: ShowWorkspaceDataState
  children: ReactNode
}) {
  const showName = data.kind === 'loaded' ? data.show.payload.name : showId

  return (
    <div className="operator-page">
      <header className="page-header">
        <p className="page-header__breadcrumb">
          <Link to="/shows">Shows</Link> <span aria-hidden="true">/</span> {showName}
        </p>
        <div className="page-header__row">
          <div style={{ minWidth: 0 }}>
            <h1 className="t-display page-header__title">{showName}</h1>
            {data.kind === 'loaded' && (
              <p className="page-header__meta">
                Revision <span className="t-data">{data.show.revision}</span>
                {data.show.createdByPrincipalName !== null && ` · saved by ${data.show.createdByPrincipalName}`}
                {' at '}
                {formatAbsolute(data.show.updatedAt)}
              </p>
            )}
          </div>
          <div className="page-header__actions">
            <Link className="btn btn--quiet" to={showPath(showId)}>
              Show details
            </Link>
            {/* Carried over control: the pre-overhaul workspace linked
                out to readiness evidence from every tab. ROUTE-MAP.md's
                2026-08-29 owner ruling makes Readiness a fifth Monitor
                facet at /monitor/readiness (owned by the Monitor group,
                not built here); this keeps the link reachable at its
                new, ruled home instead of the old /playlists/readiness
                address, which now 404s through NotFound's migration
                table. */}
            <Link className="btn btn--secondary" to={`/monitor/readiness?show=${encodeURIComponent(showId)}`}>
              Check readiness
            </Link>
          </div>
        </div>
      </header>

      <ShowWorkspaceTabs showId={showId} active={active} counts={data.kind === 'loaded' ? data.counts : undefined} />

      <div className="page-body">
        {data.kind === 'no_permission' && (
          <p className="ruled-strip ruled-strip--no-permission" role="status">
            <span className="ruled-strip__state t-meta">No permission</span>
            <span className="ruled-strip__explanation">{data.reason}</span>
          </p>
        )}
        {data.kind === 'error' && (
          <p className="ruled-strip ruled-strip--failed" role="alert">
            <span className="ruled-strip__state t-meta">Failed</span>
            <span className="ruled-strip__explanation">Could not load this show. {data.message}</span>
          </p>
        )}
        {(data.kind === 'loading' || data.kind === 'loaded') && children}
      </div>
    </div>
  )
}

/**
 * Compatibility shim for the pre-overhaul `/config/show/:id/workspace`
 * route (still declared in App.tsx, which this group does not edit -
 * BUILDER-BRIEF.md's "Do NOT wire routes in App.tsx"). That route
 * predates ROUTE-MAP.md and is not itself in the route map (neither the
 * live table nor the deliberately-not-redirected old-addresses table),
 * so it is orphaned rather than targeted for a stated destination; this
 * sends it to the show's own identity page, the nearest equivalent to
 * the old "overview" tab, rather than leaving a dead component mounted
 * there or breaking the still-wired route's compilation.
 */
export function ShowWorkspaceOverview({ showId }: { showId: string }) {
  return <Navigate replace to={showPath(showId)} />
}
