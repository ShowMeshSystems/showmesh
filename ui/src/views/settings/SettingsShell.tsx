import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import '../settings-pages.css'

// UI-DESIGN-GUIDE.md section 3 / Settings.dc.html: Settings has seven tabs
// in the same horizontal tab language used everywhere else in the
// overhaul (the `.tabs`/`.tabs__item` primitives, primitives.css), plus
// Access as an eighth entry that LEAVES the screen rather than switching a
// tab -- marked with the same "leaves the screen" arrow ROUTE-MAP.md
// specifies. `active` is left undefined by nothing rendering it (Access
// has no tab id): a tab strip item is either the current tab or a link
// elsewhere, never both.
export type SettingsTabId = 'connections' | 'delivery' | 'recovery' | 'appearance' | 'defaults' | 'audio' | 'mode'

interface TabDescriptor {
  id: SettingsTabId | 'access'
  label: string
  to: string
  external?: boolean
}

// Order matches Settings.dc.html's tab strip exactly (line 144-151): the
// mock is the pixel source of truth for tab order, not alphabetical or
// route-declaration order.
const TABS: TabDescriptor[] = [
  { id: 'connections', label: 'Connections', to: '/settings/connections' },
  { id: 'delivery', label: 'Content delivery', to: '/settings/content-delivery' },
  { id: 'recovery', label: 'Render recovery', to: '/settings/render-recovery' },
  { id: 'access', label: 'Access', to: '/access', external: true },
  { id: 'appearance', label: 'Appearance', to: '/settings/appearance' },
  { id: 'defaults', label: 'Audio defaults', to: '/settings/audio-defaults' },
  { id: 'audio', label: 'Node routing', to: '/settings/node-routing' },
  { id: 'mode', label: 'Mode', to: '/settings/mode' },
]

export function SettingsShell({ active, children }: { active: SettingsTabId; children: ReactNode }) {
  return (
    <div className="operator-page settings-shell">
      <h1 className="t-display settings-shell__title">Settings</h1>
      <p className="t-small text-muted settings-shell__lede">
        Installation-wide configuration. Every save creates a coordinator revision, is attributed to
        you, and can conflict with someone else&rsquo;s.
      </p>
      <nav className="tabs settings-shell__tabs" aria-label="Settings">
        {TABS.map((tab) =>
          tab.external ? (
            <Link key={tab.id} className="tabs__item" to={tab.to}>
              {tab.label} <span aria-hidden="true">&#8599;</span>
            </Link>
          ) : (
            <Link
              key={tab.id}
              className="tabs__item"
              to={tab.to}
              aria-current={tab.id === active ? 'page' : undefined}
            >
              {tab.label}
            </Link>
          ),
        )}
      </nav>
      <div className="settings-shell__content">{children}</div>
    </div>
  )
}
