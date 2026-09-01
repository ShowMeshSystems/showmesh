import { Link, useLocation } from 'react-router-dom'
import { ButtonRow, Section, Table, TableWrap } from '../kit'

/** The mock's six folded destinations. `prefixes` matches the address hit. */
const MOVED: readonly { prefixes: readonly string[]; from: string; to: string; label: string; note?: string }[] = [
  {
    prefixes: ['/observations'],
    from: '/observations',
    to: '/monitor/signals',
    label: 'Monitor › Signals',
    note: 'by observation, across every resource',
  },
  {
    prefixes: ['/nodes', '/fpp', '/resolume'],
    from: '/nodes · /fpp · /resolume',
    to: '/monitor/fleet',
    label: 'Monitor › Fleet',
    note: 'one table, Kind as a column',
  },
  {
    prefixes: ['/events', '/audit'],
    from: '/events · /audit',
    to: '/monitor/activity',
    label: 'Monitor › Activity',
    note: 'one stream; audit rows need an audit-read scope',
  },
  {
    prefixes: ['/capabilities'],
    from: '/capabilities',
    to: '/monitor/capabilities',
    label: 'Monitor › Capabilities',
  },
  {
    prefixes: ['/actions', '/macros'],
    from: '/actions · /macros',
    to: '/shows/automation',
    label: 'Shows › Automation',
    note: 'inside the show that owns them',
  },
  {
    prefixes: ['/config/show'],
    from: '/config/show/…/cues',
    to: '/shows/cues',
    label: 'Shows › Cues',
    note: 'a tab now, so it no longer leaves the show',
  },
]

/** Not a Monitor fold, so the mock's table does not list it, but a bookmark still lands here. */
const ALSO_MOVED = [
  { prefixes: ['/config'], to: '/settings/connections', label: 'Settings › Connections' },
  { prefixes: ['/monitor/readiness'], to: '/shows/playlists', label: 'Shows › Playlists' },
  { prefixes: ['/monitor/fleet/playlist-definitions'], to: '/shows/playlists', label: 'Shows › Playlists' },
  { prefixes: [], contains: ['/night-sessions'], to: '/night', label: 'Show Night' },
  { prefixes: ['/assets/manifest'], to: '/monitor/manifest', label: 'Monitor › Manifest' },
]

export function NotFound() {
  const { pathname } = useLocation()
  // `contains` is for the one folded address with a variable show id in the
  // middle of it. Everything else matches on its own prefix, as before.
  const matches = (entry: { prefixes: readonly string[]; contains?: readonly string[] }) =>
    entry.prefixes.some((prefix) => pathname.startsWith(prefix)) ||
    (entry.contains ?? []).some((part) => pathname.includes(part))
  const moved = MOVED.find(matches) ?? ALSO_MOVED.find(matches)

  return (
    <>
      <section aria-labelledby="nf-h" className="sm-notfound">
        <p className="sm-plate__eyebrow">Not found</p>
        <h1 className="sm-page__title" id="nf-h">No page at this address</h1>
        <p className="sm-notfound__path sm-data">{pathname}</p>
        <p className="sm-plate__detail">
          The show is running normally, the bar above is live. Nothing is wrong with the installation; this address
          just does not exist.
        </p>
        <p className="sm-plate__detail">
          {moved === undefined ? 'Nothing is routed here' : `That screen is now ${moved.label}`}
        </p>
        <ButtonRow>
          {moved === undefined ? (
            <Link className="sm-btn sm-btn--primary" to="/">
              Go to Dashboard
            </Link>
          ) : (
            <Link className="sm-btn sm-btn--primary" to={moved.to}>
              Go to {moved.label}
            </Link>
          )}
          <Link className="sm-btn" to="/monitor/signals">
            Open Monitor › Signals
          </Link>
        </ButtonRow>
      </section>
      <Section
        id="moved"
        title="Where it probably went"
        detail="Six destinations that each answered part of one question were folded into Monitor. A bookmark from before the change lands here."
      >
        <TableWrap label="Old addresses and where they went">
          <Table minWidth={520}>
            <thead>
              <tr>
                <th scope="col">Old address</th>
                <th scope="col">Now</th>
              </tr>
            </thead>
            <tbody>
              {MOVED.map((entry) => (
                <tr key={entry.from}>
                  <td className="sm-data">{entry.from}</td>
                  <td>
                    <Link to={entry.to}>{entry.label}</Link>
                    {entry.note !== undefined && <span className="sm-small sm-muted"> ({entry.note})</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </TableWrap>
        <p className="sm-section__detail">Old addresses are not redirected. Update the bookmark rather than relying on this page.</p>
      </Section>
    </>
  )
}
