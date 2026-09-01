import { Children, isValidElement, type ReactElement, type ReactNode } from 'react'
import { NavLink } from 'react-router-dom'
import { Drawer } from './Drawer'

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
