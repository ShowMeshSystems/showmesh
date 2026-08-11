import type { ConnectionState } from '../app/types'
import { formatAge } from '../app/time'

// OPERATOR-UI section 7 / spec section 6.7: when connectivity is lost the
// UI retains last known state and shows when it was last updated, and
// never lets a disconnected view read as current. This component is that
// wording, used once at the top of every data-bearing view rather than
// re-decided per view.
//
// `snapshotReceivedAt` is a browser-clock timestamp by definition (spec
// section 5.5's comment on Model.snapshotReceivedAt: "browser clock, for
// 'last updated'"), so comparing it against the browser's own Date.now()
// here is correct and is the one place in this codebase that is
// appropriate -- unlike evidence ages, which are always computed against
// serverTime (see app/time.ts).
export interface DataFreshnessNoticeProps {
  connection: ConnectionState
  snapshotReceivedAt: number | null
  now?: number // injectable for tests; defaults to Date.now()
}

export function DataFreshnessNotice({ connection, snapshotReceivedAt, now }: DataFreshnessNoticeProps) {
  if (snapshotReceivedAt === null) {
    return <p className="text-muted">No data received from the coordinator yet.</p>
  }

  const reference = now ?? Date.now()
  const age = formatAge(reference - snapshotReceivedAt)

  if (connection.kind === 'live') {
    return <p className="text-muted">Last updated {age}.</p>
  }

  return (
    <p className="text-muted" role="status">
      Showing last known data, received {age}. The browser is not connected to the
      coordinator right now, so nothing below has updated since then.
    </p>
  )
}
