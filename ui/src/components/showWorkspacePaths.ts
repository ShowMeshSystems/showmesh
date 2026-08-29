/**
 * Canonical route shape for the Show authoring workspace
 * (ROUTE-MAP.md: every `/shows/:showId/*` route is a REAL nested route,
 * never a `?show=` query parameter and never a `<Navigate replace>` out
 * to a global list). A workspace tab never navigates out of the show: it
 * keeps the tab strip and the show in the breadcrumb, because the route
 * itself is nested under the show.
 *
 * Six tabs exist on the workspace (UI-DESIGN-GUIDE.md section 3, extended
 * by the Show Night Session mock): Playlists, Cues, Assets, Presentation,
 * Automation, and Night session. This group (screen builder C) owns
 * Playlists, Cues and Presentation; Assets and Automation are owned
 * elsewhere and are routed to here without their panels being built
 * here. Night session (the definitions list, editor, and activation) is
 * owned by this group too.
 */
export type ShowWorkspaceTab = 'playlists' | 'cues' | 'assets' | 'presentation' | 'automation' | 'night-sessions'

export function showPath(showId: string): string {
  return `/shows/${encodeURIComponent(showId)}`
}

/**
 * `tab` is optional so the many "back to this object's show" links owned
 * by other groups (Assets, Automation) that predate the five-tab
 * workspace keep compiling and keep working: with no tab they land on
 * the show's own identity page (`/shows/:showId`), the new generic "back
 * to show" destination now that a tab never leaves the show. A tab view
 * calls this with a `tab` to link a specific workspace destination.
 */
export function showWorkspacePath(showId: string, tab?: ShowWorkspaceTab): string {
  return tab === undefined ? showPath(showId) : `${showPath(showId)}/${tab}`
}

export function showPlaylistsPath(showId: string): string {
  return showWorkspacePath(showId, 'playlists')
}

export function showPlaylistNewPath(showId: string): string {
  return `${showPlaylistsPath(showId)}/new`
}

export function showPlaylistPath(showId: string, playlistId: string): string {
  return `${showPlaylistsPath(showId)}/${encodeURIComponent(playlistId)}`
}

export function showCuesPath(showId: string): string {
  return showWorkspacePath(showId, 'cues')
}

export function showCueNewPath(showId: string): string {
  return `${showCuesPath(showId)}/new`
}

export function showCuePath(showId: string, cueId: string): string {
  return `${showCuesPath(showId)}/${encodeURIComponent(cueId)}`
}

export function showPresentationPath(showId: string): string {
  return showWorkspacePath(showId, 'presentation')
}

export function showSurfaceNewPath(showId: string): string {
  return `${showPresentationPath(showId)}/new`
}

export function showSurfacePath(showId: string, surfaceId: string): string {
  return `${showPresentationPath(showId)}/${encodeURIComponent(surfaceId)}`
}

export function showAssetsPath(showId: string): string {
  return showWorkspacePath(showId, 'assets')
}

export function showAutomationPath(showId: string): string {
  return showWorkspacePath(showId, 'automation')
}

export function showNightSessionsPath(showId: string): string {
  return showWorkspacePath(showId, 'night-sessions')
}

export function showNightSessionNewPath(showId: string): string {
  return `${showNightSessionsPath(showId)}/new`
}

export function showNightSessionPath(showId: string, id: string): string {
  return `${showNightSessionsPath(showId)}/${encodeURIComponent(id)}`
}
