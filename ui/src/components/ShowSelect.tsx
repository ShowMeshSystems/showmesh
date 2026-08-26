import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { listConfigObjects } from '../api'
import { describeApiError } from '../app/session'
import type { ConfigObjectSummary } from '../app/types'

type ShowListState =
  | { kind: 'loading' }
  | { kind: 'error'; message: string }
  | { kind: 'loaded'; shows: ConfigObjectSummary[] }

export interface ShowSelectProps {
  value: string
  onChange: (value: string) => void
  /**
   * Only for a control that is not itself wrapped in a `<label>` (a
   * compact table row rather than a `<label className="form-field">`
   * field): applies an accessible name directly so the control is still
   * reachable by `getByLabelText` and by assistive tech.
   */
  ariaLabel?: string
}

/**
 * The `show` field shared by every authoring form that carries one
 * (ShowActionDetail, ShowCueDetail, ShowPlaylistDetail, ShowSurfaceDetail,
 * MacroDetail, NightSessionDetail): a Show is a namespace, and every
 * cross-object reference has to stay inside it, so a hand-typed value
 * that does not match one produces an object that is either refused or
 * silently belongs to a namespace that does not exist.
 *
 * Renders a `<select>` populated from `GET /config/show` whenever that
 * list is readable and non-empty. Three failure shapes must never be
 * confused with one another on screen:
 *  - the list failed to load (network/permission/server error): falls
 *    back to the plain text input, with the reason stated inline, rather
 *    than a select that would otherwise look like "there are no shows";
 *  - the list loaded and is genuinely empty: for a brand-new object there
 *    is nothing to choose, so this says so and points at where a show is
 *    created rather than rendering an empty select;
 *  - the list loaded (empty or not) but the object being edited already
 *    carries a `show` that is not in it (a show can be removed from the
 *    list while an object still references it: the local dev
 *    coordinator has exactly this today): the current value is kept
 *    selectable and visibly marked as not in the list, never silently
 *    dropped or swapped for something else.
 */
export function ShowSelect({ value, onChange, ariaLabel }: ShowSelectProps) {
  const [state, setState] = useState<ShowListState>({ kind: 'loading' })

  useEffect(() => {
    let cancelled = false
    listConfigObjects('show')
      .then((resp) => {
        if (cancelled) return
        setState({ kind: 'loaded', shows: resp.objects })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setState({ kind: 'error', message: describeApiError(err) })
      })
    return () => {
      cancelled = true
    }
  }, [])

  if (state.kind === 'loading') {
    return (
      <>
        <input type="text" aria-label={ariaLabel} value={value} disabled aria-busy="true" onChange={() => {}} />
        <p className="text-muted">Loading shows…</p>
      </>
    )
  }

  if (state.kind === 'error') {
    return (
      <>
        <input type="text" aria-label={ariaLabel} value={value} onChange={(e) => onChange(e.target.value)} />
        <p className="text-muted" role="status">
          Could not load the show list ({state.message}); type the show id by hand.
        </p>
      </>
    )
  }

  const { shows } = state
  const knownIds = new Set(shows.map((s) => s.id))
  const valueIsKnown = value === '' || knownIds.has(value)

  if (shows.length === 0) {
    if (value === '') {
      return (
        <p className="text-muted" role="status">
          No shows are configured yet. <Link to="/config/show/new">Create a show</Link> before assigning one here.
        </p>
      )
    }
    // A show can be removed from the list while an object still
    // references it: the current value must stay visible and editable,
    // never vanish just because nothing else is available to pick.
    return (
      <>
        <input type="text" aria-label={ariaLabel} value={value} onChange={(e) => onChange(e.target.value)} />
        <p className="text-muted" role="status">
          No shows are currently configured; showing the value already on this object.
        </p>
      </>
    )
  }

  return (
    <>
      <select aria-label={ariaLabel} value={value} onChange={(e) => onChange(e.target.value)}>
        <option value="" disabled={value !== ''}>
          Select a show…
        </option>
        {!valueIsKnown && <option value={value}>{value} (not in the show list)</option>}
        {shows.map((s) => (
          <option key={s.id} value={s.id}>
            {s.label}
          </option>
        ))}
      </select>
      {!valueIsKnown && (
        <p className="text-muted" role="status">
          This value is not in the current show list. It is kept as-is; pick another to change it.
        </p>
      )}
    </>
  )
}
