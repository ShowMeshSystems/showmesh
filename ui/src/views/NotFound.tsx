import { Link, useLocation } from 'react-router-dom'
import '../styles/session.css'

// The `*` route (ROUTE-MAP.md): full live chrome renders around this
// view, because the show keeps running under a stale bookmark — the bar
// above is the proof. Old addresses are deliberately NOT redirected
// (UI-DESIGN-GUIDE.md §8): this page is the migration guide instead,
// mapping every pre-overhaul address this build's route table names to
// where it actually lives now. A route not on ROUTE-MAP.md is not
// invented here — see this build's own report for the one BUILDER-BRIEF
// named as blocked, awaiting the owner.
interface RouteMapping {
  /** Old address prefixes this row covers, exactly as ROUTE-MAP.md lists them. */
  old: string
  /** The live destination's route, or null when ROUTE-MAP.md marks it BLOCKED. */
  to: string | null
  label: string
  note: string
  /** Path prefixes this row matches, for guessing which row explains the
   * CURRENT stale address — never used to redirect, only to highlight. */
  matches: string[]
}

const ROUTE_MAP: RouteMapping[] = [
  {
    old: '/nodes · /nodes/:id',
    to: '/monitor/fleet',
    label: 'Monitor › Fleet',
    note: 'one table, Kind as a column',
    matches: ['/nodes'],
  },
  {
    old: '/fpp · /fpp/:id',
    to: '/monitor/fleet',
    label: 'Monitor › Fleet',
    note: 'one table, Kind as a column',
    matches: ['/fpp'],
  },
  {
    old: '/resolume',
    to: '/monitor/fleet',
    label: 'Monitor › Fleet',
    note: 'one table, Kind as a column',
    matches: ['/resolume'],
  },
  {
    old: '/observations',
    to: '/monitor/signals',
    label: 'Monitor › Signals',
    note: 'by observation, across every resource',
    matches: ['/observations'],
  },
  {
    old: '/events · /audit',
    to: '/monitor/activity',
    label: 'Monitor › Activity',
    note: 'one stream; audit rows need an audit-read scope',
    matches: ['/events', '/audit'],
  },
  {
    old: '/capabilities',
    to: '/monitor/capabilities',
    label: 'Monitor › Capabilities',
    note: 'by capability, across nodes',
    matches: ['/capabilities'],
  },
  {
    old: '/config and every /config/* settings page',
    to: '/settings',
    label: 'Settings',
    note: 'redirects to Settings › Connections',
    matches: ['/config'],
  },
  {
    old: '/config/show · /config/show/:id',
    to: '/shows',
    label: 'Shows',
    note: '',
    matches: ['/config/show'],
  },
  {
    old: '/config/show.playlist*',
    to: '/shows',
    label: 'Shows › Playlists',
    note: 'a tab now, so it no longer leaves the show',
    matches: ['/config/show.playlist'],
  },
  {
    old: '/config/show.cue*',
    to: '/shows',
    label: 'Shows › Cues',
    note: 'a tab now, so it no longer leaves the show',
    matches: ['/config/show.cue'],
  },
  {
    old: '/config/show.surface*',
    to: '/shows',
    label: 'Shows › Presentation',
    note: 'a tab now, so it no longer leaves the show',
    matches: ['/config/show.surface'],
  },
  {
    old: '/actions* · /macros*',
    to: '/shows',
    label: 'Shows › Automation',
    note: 'inside the show that owns them',
    matches: ['/actions', '/macros'],
  },
  {
    old: '/config/audio.settings',
    to: '/settings/audio-defaults',
    label: 'Settings › Audio defaults',
    note: '',
    matches: ['/config/audio.settings'],
  },
  {
    old: '/config/audio.node*',
    to: '/settings/node-routing',
    label: 'Settings › Node routing',
    note: '',
    matches: ['/config/audio.node'],
  },
  {
    old: '/playlists/readiness',
    to: null,
    label: 'BLOCKED, awaiting owner',
    note: '',
    matches: ['/playlists/readiness'],
  },
  {
    old: '/config/night.session*',
    to: null,
    label: 'BLOCKED, awaiting owner',
    note: '',
    matches: ['/config/night.session'],
  },
  {
    old: '/config/fpp-playlist-definitions*',
    to: null,
    label: 'BLOCKED, awaiting owner',
    note: '',
    matches: ['/config/fpp-playlist-definitions'],
  },
  {
    old: '/config/show.active',
    to: null,
    label: 'BLOCKED, awaiting owner',
    note: '',
    matches: ['/config/show.active'],
  },
]

export function NotFound() {
  const location = useLocation()
  const requestedPath = location.pathname

  // Never a redirect, only a guess at which row explains this address —
  // the longest matching prefix wins so a more specific row (e.g.
  // /config/show.playlist*) is preferred over a broader one (e.g. /config).
  const guess = ROUTE_MAP.filter((row) => row.matches.some((prefix) => requestedPath.startsWith(prefix))).sort(
    (a, b) => Math.max(...b.matches.map((m) => m.length)) - Math.max(...a.matches.map((m) => m.length)),
  )[0]
  const guessedLiveDestination = guess?.to ?? null

  return (
    <>
      <section aria-labelledby="nf-h" className="page-header">
        <p className="t-meta" style={{ margin: 0, color: 'var(--text-faint)' }}>
          Not found
        </p>
        <h1 id="nf-h" className="t-display" style={{ margin: '7px 0 0' }}>
          No page at this address
        </h1>

        <p className="not-found-path t-data">{requestedPath}</p>

        <p className="t-body" style={{ margin: '14px 0 0', color: 'var(--text-muted)' }}>
          The show is running normally, the bar above is live. Nothing is wrong with the
          installation; this address just does not exist. Old addresses are not redirected, so a
          404 here is usually an out of date bookmark rather than a typo.
        </p>

        <div className="not-found-actions">
          <Link to="/" className="btn btn--primary">
            Go to Dashboard
          </Link>
          {guessedLiveDestination !== null && (
            <Link to={guessedLiveDestination} className="btn btn--secondary">
              Open {guess!.label}
            </Link>
          )}
        </div>
      </section>

      <section aria-labelledby="nf-moved" className="page-body">
        <h2 id="nf-moved" className="t-meta" style={{ margin: 0, color: 'var(--text-faint)' }}>
          Where it probably went
        </h2>
        <p className="t-small route-map-table__note" style={{ margin: '8px 0 12px' }}>
          Every address the pre-overhaul UI used to serve, and where it lives now. Old addresses
          are not redirected; update the bookmark rather than relying on this table.
        </p>

        <div className="table-wrap card">
          <table className="table route-map-table">
            <thead>
              <tr>
                <th>Old address</th>
                <th>Now</th>
              </tr>
            </thead>
            <tbody>
              {ROUTE_MAP.map((row) => (
                <tr key={row.old} {...(row === guess ? { 'data-current-guess': 'true' } : {})}>
                  <td className="t-data" style={{ color: row.to === null ? 'var(--text-faint)' : 'var(--text-muted)' }}>
                    {row.old}
                  </td>
                  <td>
                    {row.to !== null ? (
                      <>
                        <Link to={row.to}>{row.label}</Link>{' '}
                        {row.note !== '' && (
                          <span className="t-small" style={{ color: 'var(--text-muted)' }}>
                            ({row.note})
                          </span>
                        )}
                      </>
                    ) : (
                      <span className="t-small" style={{ color: 'var(--text-faint)' }}>{row.label}</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>
    </>
  )
}
