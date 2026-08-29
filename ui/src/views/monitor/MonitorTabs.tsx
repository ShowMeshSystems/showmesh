import { Link } from 'react-router-dom'

// The Monitor destination's own facet strip (UI-DESIGN-GUIDE.md section 3):
// Fleet / Signals / Activity / Capabilities / Readiness, organised by AXIS
// (by resource, by observation, by event, by capability, by check) rather
// than by resource type. Owner ruling 2026-08-29 added Readiness as a
// fifth facet inside this one destination -- the seven rail destinations
// are unchanged. Mirrors ShowWorkspaceTabs' shape (components/ShowWorkspace.tsx):
// a plain <nav className="tabs">, Link + aria-current, an optional inventory
// count (never an attention count -- that is the rail badge's job).
export type MonitorFacet = 'fleet' | 'signals' | 'activity' | 'capabilities' | 'readiness'

const FACETS: Array<{ id: MonitorFacet; label: string; path: string }> = [
  { id: 'fleet', label: 'Fleet', path: '/monitor/fleet' },
  { id: 'signals', label: 'Signals', path: '/monitor/signals' },
  { id: 'activity', label: 'Activity', path: '/monitor/activity' },
  { id: 'capabilities', label: 'Capabilities', path: '/monitor/capabilities' },
  { id: 'readiness', label: 'Readiness', path: '/monitor/readiness' },
]

export interface MonitorFacetCounts {
  fleet?: number
  signals?: number
  capabilities?: number
}

export function MonitorTabs({ active, counts }: { active: MonitorFacet; counts?: MonitorFacetCounts }) {
  return (
    <nav className="tabs" aria-label="Monitor facets">
      {FACETS.map((facet) => {
        const isActive = facet.id === active
        const count = counts?.[facet.id as keyof MonitorFacetCounts]
        return (
          <Link key={facet.id} to={facet.path} className="tabs__item" aria-current={isActive ? 'page' : undefined}>
            {facet.label}
            {count !== undefined && <span className="tabs__count">{count}</span>}
          </Link>
        )
      })}
    </nav>
  )
}
