import type { ReactNode } from 'react'
import { NavLink } from 'react-router-dom'

export type Connection = 'live' | 'degraded' | 'lost' | 'unknown'

type ChromeProps = {
  showPicker: ReactNode
  mode: ReactNode
  /** Title, position, cycle, time to next transition. Truncates. */
  nowPlaying: ReactNode
  connection: ReactNode
  principal: ReactNode
}

/**
 * The bar must not wrap. If its height changes, the rail's sticky offset
 * puts the first nav group behind it: fix the wrap, never the offset.
 */
export function ChromeBar({ showPicker, mode, nowPlaying, connection, principal }: ChromeProps) {
  return (
    <header className="sm-chrome" data-chrome>
      <div className="sm-chrome__left">
        <span className="sm-brand" aria-hidden="true">SM</span>
        <span className="sm-chrome__divider" aria-hidden="true" />
        {showPicker}
        {mode}
      </div>
      <div className="sm-chrome__now">{nowPlaying}</div>
      <div className="sm-chrome__right">
        {connection}
        <span className="sm-chrome__divider" aria-hidden="true" />
        {principal}
      </div>
    </header>
  )
}

/** Position of the current item, as a fraction between 0 and 1. */
export function ChromeProgress({ value, label }: { value: number | null; label: string }) {
  if (value === null) return <div className="sm-progress" />
  const pct = Math.max(0, Math.min(1, value)) * 100
  return (
    <div className="sm-progress" role="progressbar" aria-label={label} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(pct)}>
      <div className="sm-progress__fill" style={{ width: `${pct}%` }} />
    </div>
  )
}

export function ConnectionPill({ state, label }: { state: Connection; label: string }) {
  return (
    <span className={`sm-conn sm-conn--${state}`}>
      <span className="sm-conn__dot" aria-hidden="true" />
      {label}
    </span>
  )
}

/** The rail and the page beside it. */
export function ShellBody({ children }: { children: ReactNode }) {
  return <div className="sm-shell-body">{children}</div>
}

export function Rail({ children }: { children: ReactNode }) {
  return <nav className="sm-rail" data-rail aria-label="Primary">{children}</nav>
}

export function RailGroup({ children }: { children: string }) {
  return <p className="sm-rail__group">{children}</p>
}

/**
 * A badge here is an attention count, never an inventory count: it means an
 * operator has something to do. Without a read there is no badge at all.
 */
export function RailLink({ to, children, badge }: { to: string; children: string; badge?: ReactNode }) {
  return (
    <NavLink to={to} className="sm-rail__link" end={to === '/'}>
      {children}
      {badge}
    </NavLink>
  )
}

export function RailBadge({ tone, count }: { tone: 'bad' | 'warn' | 'live'; count: ReactNode }) {
  return <span className={`sm-rail__badge sm-rail__badge--${tone}`}>{count}</span>
}

export function Panes({ children }: { children: ReactNode }) {
  return <div className="sm-panes" data-panes>{children}</div>
}
