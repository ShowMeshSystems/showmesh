import { Link, useLocation } from 'react-router-dom'
import { BlankingPlate, ButtonRow, PageTitle, Section, Table, TableWrap } from '../kit'

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
  { prefixes: ['/night-sessions'], to: '/night', label: 'Show Night' },
  { prefixes: ['/assets/manifest'], to: '/monitor/manifest', label: 'Monitor › Manifest' },
]

export function NotFound() {
  const { pathname } = useLocation()
  const matches = (entry: { prefixes: readonly string[] }) => entry.prefixes.some((prefix) => pathname.includes(prefix))
  const moved = MOVED.find(matches) ?? ALSO_MOVED.find(matches)

  return (
    <>
      <PageTitle title="No page at this address" lede={pathname} />
      <BlankingPlate
        absence="empty"
        stamp="404"
        eyebrow="This address · not routed"
        title={moved === undefined ? 'Nothing is routed here' : `That screen is now ${moved.label}`}
        detail="The overhaul moved every screen into seven destinations. Old addresses are not redirected, so a bookmark lands here instead of somewhere that looks right and is not."
        actions={
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
        }
      />
      <Section
        id="moved"
        title="Where it probably went"
        detail="Six destinations that each answered part of one question were folded into Monitor. A bookmark from before the change lands here."
      >
        <TableWrap label="Old addresses and where they went">
          <Table>
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
