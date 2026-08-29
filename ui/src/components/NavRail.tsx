import { NavLink } from 'react-router-dom'

/**
 * The seven-destination rail (UI-DESIGN-GUIDE.md section 3 / README.md
 * "Information architecture"): exactly Dashboard, Show Night, Live Control
 * (Operate); Shows, Assets (Author); Monitor, Settings (System). "Do not
 * add an eighth without deleting one."
 *
 * Route paths live in exactly one place -- this table -- so App.tsx's
 * route declarations (owned by a different seam) and this rail cannot
 * drift into disagreeing about where a destination lives. Import
 * `RAIL_DESTINATIONS` rather than hand-typing a path a second time.
 */
export const RAIL_DESTINATIONS = {
  dashboard: '/',
  showNight: '/night',
  liveControl: '/control',
  shows: '/shows',
  assets: '/assets',
  monitor: '/monitor',
  settings: '/settings',
} as const

export type RailDestinationKey = keyof typeof RAIL_DESTINATIONS

interface RailItem {
  key: RailDestinationKey
  label: string
  /** Mirrors react-router's own NavLink `end`: exact match only, vs. any sub-path. */
  end: boolean
}

interface RailGroup {
  heading: string
  items: RailItem[]
}

const RAIL_GROUPS: RailGroup[] = [
  {
    heading: 'Operate',
    items: [
      { key: 'dashboard', label: 'Dashboard', end: true },
      { key: 'showNight', label: 'Show Night', end: true },
      { key: 'liveControl', label: 'Live Control', end: true },
    ],
  },
  {
    heading: 'Author',
    items: [
      { key: 'shows', label: 'Shows', end: false },
      { key: 'assets', label: 'Assets', end: false },
    ],
  },
  {
    heading: 'System',
    items: [
      { key: 'monitor', label: 'Monitor', end: false },
      // Exact match only: /config's own settings tabs are not built yet,
      // but /config/show, /config/audio.node, etc. are already other
      // destinations' territory (Shows, and future Monitor/Settings
      // sub-routes) and must not also light up Settings as current.
      { key: 'settings', label: 'Settings', end: true },
    ],
  },
]

export interface NavRailProps {
  /**
   * Attention counts, never inventory counts (UI-DESIGN-GUIDE.md section
   * 3/4): a badge means the operator has something to do. Keyed by
   * destination so a caller can only ever badge a real rail item. Omit a
   * key (or the whole prop) rather than passing 0 or an invented number --
   * a signed-out or not-yet-read device must show no badges at all, and
   * this component does not fetch anything to make one up itself.
   */
  badges?: Partial<Record<RailDestinationKey, number>>
}

export function NavRail({ badges }: NavRailProps) {
  return (
    <nav className="rail" aria-label="Operator navigation">
      {RAIL_GROUPS.map((group) => (
        <div className="rail__group" key={group.heading}>
          <h3 className="rail__group-label t-meta">{group.heading}</h3>
          {group.items.map((item) => {
            const count = badges?.[item.key]
            return (
              <NavLink key={item.key} to={RAIL_DESTINATIONS[item.key]} end={item.end} className="rail__link">
                <span>{item.label}</span>
                {typeof count === 'number' && count > 0 && <span className="rail__badge">{count}</span>}
              </NavLink>
            )
          })}
        </div>
      ))}
    </nav>
  )
}
