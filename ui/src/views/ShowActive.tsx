import { Navigate } from 'react-router-dom'

// ROUTE-MAP.md owner ruling, 2026-08-29: show activation now lives on
// the Shows destination itself (src/views/Shows.tsx), positioned above
// "All shows" - a dropdown plus a Select button, with activation history
// on the same page. That is the ruled home; this file is no longer a
// parked standalone screen.
//
// `/config/show.active` is still wired in App.tsx (this group does not
// edit App.tsx) and now maps to "Shows" per ROUTE-MAP.md's old-addresses
// table, so this component redirects there rather than duplicating the
// activation UI a second time or leaving a dead screen mounted at the
// old address.
export function ShowActive() {
  return <Navigate replace to="/shows" />
}
