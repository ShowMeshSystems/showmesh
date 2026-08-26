import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getShowModeConfig, type ShowModeConfigResponse } from '../api'
import { describeApiError } from '../app/session'

// ADR-033 decision 3: "the mode appears on the Operator UI persistently,
// not on a settings page." This component is that indicator, and it lives
// in Layout's app-header, which renders on every route and is deliberately
// NOT gated on Layout's `blockContent` state, following SessionPanel's own
// precedent one line below it. A mode that is not visible is a trap,
// because every surface behaves differently and nothing says why, and a
// mode that disappears the moment the coordinator connection is degraded is
// exactly the moment an operator most needs to know which one they are in.
//
// It reads GET /config/show.mode, the one configuration read that does not
// require config:write (see internal/coordinator/api/showmode.go's header
// comment). That is the whole reason this indicator can exist: gated on
// config:write it would render nothing for the operator standing at the
// console, which is not persistent visibility.
//
// The three states are rendered distinctly and none of them is silently
// collapsed into another. "Loading" is not "program". A failed read is not
// "program" either: it says the mode could not be read, because inventing
// a mode here would be the UI asserting a system state from no evidence.
//
// Operator-reported: this reads as a clickable affordance but was a plain
// <span>, so clicking it did nothing. It is now a link to /config, the
// route holding ShowModePanel -- the config:write-gated control that
// actually changes the mode. It stays a link to that page, never the
// switch itself: changing the installation-wide operating mode from a
// persistent header element, with no context and no confirmation, is the
// wrong place for the control, only for the way to reach it.

// SHOWMESH CHOICE, NOT MEASURED. The mode is not carried on the change
// stream (it is configuration, not a resource ADR-020 models), so this
// indicator polls. Fifteen seconds is well inside "an operator notices
// within a moment" and is three coordinator publish intervals.
const SHOW_MODE_POLL_MS = 15_000

type IndicatorState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; config: ShowModeConfigResponse }

export function ShowModeIndicator() {
  const [state, setState] = useState<IndicatorState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false

    async function load(): Promise<void> {
      try {
        const config = await getShowModeConfig()
        if (cancelled) return
        setState({ kind: 'loaded', config })
      } catch (err) {
        if (cancelled) return
        // This kind never 404s, so any error here is a genuine failure and
        // is rendered as one rather than as a default value.
        setState({ kind: 'error', message: describeApiError(err) })
      }
    }

    void load()
    const timer = setInterval(() => void load(), SHOW_MODE_POLL_MS)
    return () => {
      cancelled = true
      clearInterval(timer)
    }
  }, [])

  if (state.kind === 'loading') {
    return (
      <Link to="/config" className="show-mode show-mode--unknown" role="status" aria-label="Show mode">
        Mode: loading…
      </Link>
    )
  }

  if (state.kind === 'error') {
    return (
      <Link
        to="/config"
        className="show-mode show-mode--unknown"
        role="status"
        aria-label="Show mode"
        title={state.message}
      >
        Mode: cannot be read
      </Link>
    )
  }

  const mode = state.config.payload.mode
  const never = state.config.revision === 0
  return (
    <Link
      to="/config"
      className={`show-mode show-mode--${mode}`}
      role="status"
      aria-label="Show mode"
      // ADR-033 decision 3: a behaviour caused by the mode states the mode
      // as its reason, and this is where an operator hovering the badge
      // reads it.
      title={state.config.resolumeWebSocketEffect}
    >
      Mode: {mode === 'show' ? 'Show' : 'Program'}
      {never && <span className="show-mode__note"> (default)</span>}
    </Link>
  )
}
