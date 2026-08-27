/** The canonical route shape for a Show authoring workspace. */
export type ShowWorkspaceSection =
  | 'overview'
  | 'run-of-show'
  | 'cues'
  | 'assets'
  | 'automation'
  | 'presentation'
  | 'show-night'
  | 'readiness'

export function showWorkspacePath(showId: string, section: ShowWorkspaceSection = 'overview'): string {
  const encoded = encodeURIComponent(showId)
  return section === 'overview' ? `/config/show/${encoded}/workspace` : `/config/show/${encoded}/workspace/${section}`
}

