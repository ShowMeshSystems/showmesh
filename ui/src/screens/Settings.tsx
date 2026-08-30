import { Link, NavLink, Outlet } from 'react-router-dom'
import { PageTitle } from '../kit'

const TABS: readonly { path: string; label: string }[] = [
  { path: 'connections', label: 'Connections' },
  { path: 'delivery', label: 'Content delivery' },
  { path: 'recovery', label: 'Render recovery' },
  { path: 'appearance', label: 'Appearance' },
  { path: 'audio-defaults', label: 'Audio defaults' },
  { path: 'node-routing', label: 'Node routing' },
  { path: 'mode', label: 'Mode' },
]

/** The seven-tab shell (guide §3). Access is an eighth tab that leaves for `/access` rather than a child route. */
export function Settings() {
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
        <Link to="/access" className="sm-facets__tab">
          Access<span aria-hidden="true"> ↗</span>
        </Link>
      </nav>

      <Outlet />
    </>
  )
}
