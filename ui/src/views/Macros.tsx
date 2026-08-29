import { Link, useParams } from 'react-router-dom'
import { AutomationWorkspace } from './automation/AutomationWorkspace'

/**
 * `Show Automation.dc.html` (UI-DESIGN-GUIDE.md's screen map) replaces the
 * standalone macro list with the show workspace's Automation tab
 * (ROUTE-MAP.md: `/shows/:showId/automation`). This file now just resolves
 * `showId` off whichever route mounted it and hands off to
 * `AutomationWorkspace` — the real implementation lives in
 * `src/views/automation/`. Old, un-scoped mountings of this component (the
 * former global `/macros`) have no show to resolve; ROUTE-MAP.md states
 * that address is deliberately not redirected, so this renders the plain
 * "pick a show" notice rather than guessing one.
 */
export function Macros() {
  const { showId } = useParams<{ showId?: string }>()
  if (showId === undefined) {
    return (
      <div className="page-body">
        <p role="status">
          Automation is a show workspace tab now. Open a show from <Link to="/shows">Shows</Link>, then its
          Automation tab.
        </p>
      </div>
    )
  }
  return <AutomationWorkspace showId={showId} />
}
