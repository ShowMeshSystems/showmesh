import { Link, useParams } from 'react-router-dom'
import { AutomationWorkspace } from './automation/AutomationWorkspace'

/**
 * `Show Automation.dc.html` folds the standalone action list into the show
 * workspace's Automation tab: an action never appears on its own in a
 * running show (UI-DESIGN-GUIDE.md section 3), so there is no longer a
 * separate "Show actions" screen — see `Macros.tsx`'s identical note.
 */
export function ShowActions() {
  const { showId } = useParams<{ showId?: string }>()
  if (showId === undefined) {
    return (
      <div className="page-body">
        <p role="status">
          Actions live inside a show&rsquo;s Automation tab now. Open a show from <Link to="/shows">Shows</Link>,
          then its Automation tab.
        </p>
      </div>
    )
  }
  return <AutomationWorkspace showId={showId} />
}
