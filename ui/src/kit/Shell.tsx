import { Children, cloneElement, isValidElement, useEffect, useState, type MouseEvent, type ReactElement, type ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { Drawer } from './Drawer'

export type Connection = 'live' | 'degraded' | 'lost' | 'unknown'

/** True once the viewport is at or below `breakpoint`. False during SSR and the first paint. */
function useNarrow(breakpoint: number): boolean {
  const [narrow, setNarrow] = useState(false)
  useEffect(() => {
    const query = window.matchMedia(`(max-width: ${breakpoint}px)`)
    setNarrow(query.matches)
    const onChange = () => setNarrow(query.matches)
    query.addEventListener('change', onChange)
    return () => query.removeEventListener('change', onChange)
  }, [breakpoint])
  return narrow
}

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
  const compact = useNarrow(720)
  const [overflowOpen, setOverflowOpen] = useState(false)
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
        {compact ? (
          <>
            <button
              type="button"
              className="sm-chrome-toggle"
              aria-haspopup="true"
              aria-expanded={overflowOpen}
              aria-label="More"
              onClick={() => setOverflowOpen((v) => !v)}
            >
              <span aria-hidden="true">⋯</span>
            </button>
            {overflowOpen && (
              <>
                <div className="sm-chrome-overflow-scrim" onClick={() => setOverflowOpen(false)} aria-hidden="true" />
                <div className="sm-chrome-overflow" role="menu">
                  {connection}
                  <span className="sm-chrome__divider" aria-hidden="true" />
                  {principal}
                </div>
              </>
            )}
          </>
        ) : (
          <>
            {connection}
            <span className="sm-chrome__divider" aria-hidden="true" />
            {principal}
          </>
        )}
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

/**
 * The rail and the page beside it. Below the rail breakpoint the rail no
 * longer takes a grid column; this owns the opener and scrim that turn it
 * into a drawer instead, so no screen has to wire that state itself.
 */
export function ShellBody({ children }: { children: ReactNode }) {
  const [railOpen, setRailOpen] = useState(false)
  const items = Children.toArray(children)
  const railIndex = items.findIndex((child) => isValidElement(child) && child.type === Rail)
  const rail =
    railIndex === -1
      ? null
      : cloneElement(items[railIndex] as ReactElement<RailProps>, { open: railOpen, onClose: () => setRailOpen(false) })
  const rest = railIndex === -1 ? items : items.filter((_, index) => index !== railIndex)
  return (
    <div className="sm-shell-body">
      <button
        type="button"
        className="sm-rail-toggle"
        aria-haspopup="true"
        aria-expanded={railOpen}
        aria-label={railOpen ? 'Close navigation' : 'Open navigation'}
        onClick={() => setRailOpen((v) => !v)}
      >
        <span aria-hidden="true">☰</span>
      </button>
      <div className="sm-rail-scrim" data-open={railOpen} onClick={() => setRailOpen(false)} aria-hidden="true" />
      {rail}
      {rest}
    </div>
  )
}

type RailProps = { children: ReactNode; open?: boolean; onClose?: () => void }

/** `open`/`onClose` are supplied by `ShellBody` when `Rail` is its direct child; both default for standalone use (the specimen page). */
export function Rail({ children, open = false, onClose }: RailProps) {
  const closeOnLinkClick = (event: MouseEvent<HTMLElement>) => {
    if (onClose && event.target instanceof HTMLElement && event.target.closest('a')) onClose()
  }
  return (
    <nav className="sm-rail" data-rail data-open={open} aria-label="Primary" onClick={closeOnLinkClick}>
      {children}
    </nav>
  )
}

export function RailGroup({ children }: { children: string }) {
  return <p className="sm-rail__group">{children}</p>
}

/**
 * A badge here is an attention count, never an inventory count: it means an
 * operator has something to do. Without a read there is no badge at all.
 */
export function RailLink({ to, children, badge, sub }: { to: string; children: string; badge?: ReactNode; sub?: boolean }) {
  return (
    <NavLink to={to} className={`sm-rail__link${sub ? ' sm-rail__link--sub' : ''}`} end={to === '/'}>
      {children}
      {badge}
    </NavLink>
  )
}

export function RailBadge({ tone, count }: { tone: 'bad' | 'warn' | 'live'; count: ReactNode }) {
  return <span className={`sm-rail__badge sm-rail__badge--${tone}`}>{count}</span>
}

/** Sits below the nav links, pinned to the rail's bottom edge. */
export function RailFooter({ children }: { children: ReactNode }) {
  return <div className="sm-rail__footer">{children}</div>
}

type PanesProps = {
  children: ReactNode
  /** Whether the current selection opens the inspector drawer. */
  inspectorOpen: boolean
  /** Clears the selection; the row that opened the drawer gets focus back. */
  onInspectorClose: () => void
  /** Id of the heading rendered inside the `aside` content. */
  inspectorLabelledBy: string
  /** 'content' (default) or 'wide' for a form-heavy editor. */
  inspectorWidth?: 'content' | 'wide' | number
}

/**
 * D-021/D-022: the list is the whole page body. Its `aside` child never
 * renders in place; it floats in a `Drawer` when `inspectorOpen` is true.
 */
export function Panes({ children, inspectorOpen, onInspectorClose, inspectorLabelledBy, inspectorWidth = 'content' }: PanesProps) {
  const items = Children.toArray(children)
  const asideIndex = items.findIndex((child) => isValidElement(child) && child.type === 'aside')
  const aside = asideIndex === -1 ? null : (items[asideIndex] as ReactElement<{ children?: ReactNode }>)
  const body = asideIndex === -1 ? items : items.filter((_, index) => index !== asideIndex)
  return (
    <div className="sm-panes" data-panes>
      {body}
      <Drawer open={inspectorOpen} onClose={onInspectorClose} labelledBy={inspectorLabelledBy} width={inspectorWidth}>
        {aside?.props.children}
      </Drawer>
    </div>
  )
}
