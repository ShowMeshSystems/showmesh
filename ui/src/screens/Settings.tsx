import { Link, NavLink, Outlet } from 'react-router-dom'
import { PageTitle } from '../kit'
import { useModelContext } from '../app/ModelContext'

const TABS: readonly { path: string; label: string }[] = [
  { path: 'connections', label: 'Connections' },
  { path: 'delivery', label: 'Content delivery' },
  { path: 'recovery', label: 'Render recovery' },
  { path: 'appearance', label: 'Appearance' },
  { path: 'audio-defaults', label: 'Audio defaults' },
  { path: 'node-routing', label: 'Node routing' },
  { path: 'mode', label: 'Mode' },
]

/** Settings owns installation configuration; Access remains a leaving tab at `/access`. */
export function Settings() {
  const model = useModelContext()
  const resolume = model.resolume[0]
  return (
    <>
      <PageTitle
        title="Settings"
        lede="Installation-wide configuration. Every save creates a coordinator revision, is attributed to you, and can conflict with someone else's."
      />

      <nav className="sm-facets" aria-label="Settings tabs">
        {TABS.map((tab) => (
          <NavLink key={tab.path} to={`/settings/${tab.path}`} className="sm-facets__tab">
            {tab.label}
          </NavLink>
        ))}
        <NavLink to={resolume === undefined ? '/settings/resolume' : `/settings/resolume/${encodeURIComponent(resolume.instanceId)}`} className="sm-facets__tab">
          Resolume
        </NavLink>
        <Link to="/access" className="sm-facets__tab">
          Access<span aria-hidden="true"> ↗</span>
        </Link>
      </nav>

      <Outlet />
    </>
  )
}
