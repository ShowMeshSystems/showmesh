import { NavLink, Outlet } from 'react-router-dom'
import { PageTitle } from '../kit'
import { useModelContext } from '../app/ModelContext'

const TABS: readonly { path: string; label: string }[] = [
  { path: 'connections', label: 'Connections' },
  { path: 'delivery', label: 'Content delivery' },
  { path: 'recovery', label: 'Render recovery' },
  { path: 'weather-delay', label: 'Weather delay' },
  { path: 'dashboard', label: 'Dashboard' },
  { path: 'appearance', label: 'Appearance' },
  { path: 'audio-defaults', label: 'Audio defaults' },
  { path: 'node-routing', label: 'Node routing' },
  { path: 'node-enrollment', label: 'Node enrollment' },
  { path: 'mode', label: 'Mode' },
]

/** Settings owns installation configuration; Access is its own rail destination. */
export function Settings() {
  const model = useModelContext()
  const resolume = model.resolume[0]
  return (
    <>
      <PageTitle title="Settings" />

      <nav className="sm-facets" aria-label="Settings tabs">
        {TABS.map((tab) => (
          <NavLink key={tab.path} to={`/settings/${tab.path}`} className="sm-facets__tab">
            {tab.label}
          </NavLink>
        ))}
        <NavLink to={resolume === undefined ? '/settings/resolume' : `/settings/resolume/${encodeURIComponent(resolume.instanceId)}`} className="sm-facets__tab">
          Resolume
        </NavLink>
      </nav>

      <Outlet />
    </>
  )
}
