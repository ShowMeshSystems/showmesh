import { Link, useLocation } from 'react-router-dom'
import { BlankingPlate, PageTitle, Section, Table, TableWrap } from '../kit'

/** Old addresses are not redirected. This page maps them instead. */
const MOVED: readonly { from: string; to: string; label: string }[] = [
  { from: '/nodes', to: '/monitor/fleet', label: 'Monitor › Fleet' },
  { from: '/fpp', to: '/monitor/fleet', label: 'Monitor › Fleet' },
  { from: '/resolume', to: '/monitor/fleet/resolume', label: 'Monitor › Fleet › Resolume' },
  { from: '/observations', to: '/monitor/signals', label: 'Monitor › Signals' },
  { from: '/events', to: '/monitor/activity', label: 'Monitor › Activity' },
  { from: '/audit', to: '/monitor/activity', label: 'Monitor › Activity' },
  { from: '/capabilities', to: '/monitor/capabilities', label: 'Monitor › Capabilities' },
  { from: '/config', to: '/settings/connections', label: 'Settings › Connections' },
]

export function NotFound() {
  const { pathname } = useLocation()
  const moved = MOVED.find((entry) => pathname.startsWith(entry.from))

  return (
    <>
      <PageTitle title="No page at this address" lede={pathname} />
      <BlankingPlate
        absence="empty"
        stamp="404"
        eyebrow="This address · not routed"
        title={moved === undefined ? 'Nothing is routed here' : `That screen is now ${moved.label}`}
        detail="The overhaul moved every screen into seven destinations. Old addresses are not redirected, so a bookmark lands here instead of somewhere that looks right and is not."
        actions={moved === undefined ? <Link to="/">Go to Dashboard</Link> : <Link to={moved.to}>Go to {moved.label}</Link>}
      />
      <Section id="moved" title="Where it probably went">
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
                  </td>
                </tr>
              ))}
            </tbody>
          </Table>
        </TableWrap>
      </Section>
    </>
  )
}
