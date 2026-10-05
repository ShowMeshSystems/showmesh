import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { StatusPair } from './Status'
import type { Tone } from './tone'

type AttentionProps = {
  tone: Tone
  /** The state word, plus its qualifier: "Offline · 26 m". */
  state: string
  /** What happened, at value weight. */
  fact: ReactNode
  /** The consequence, then the action, at helper size. */
  detail?: ReactNode
  appearance?: 'chip' | 'word'
}

/** One thing asking for an operator. The state word carries the colour. */
export function AttentionRow({ tone, state, fact, detail, appearance = 'chip' }: AttentionProps) {
  return (
    <div className="sm-attn">
      <StatusPair tone={tone} label={state} appearance={appearance} />
      <div>
        <p className="sm-attn__fact">{fact}</p>
        {detail !== undefined && <p className="sm-attn__detail">{detail}</p>}
      </div>
    </div>
  )
}

type TileProps = {
  label: string
  /** Tabular numerals: "3 / 4". */
  value: ReactNode
  detail: ReactNode
  to?: string
}

/** A counted fact that links to the screen that explains it. */
export function StatTile({ label, value, detail, to }: TileProps) {
  const body = (
    <>
      <p className="sm-tile__label">{label}</p>
      <p className="sm-tile__value">{value}</p>
      <p className="sm-tile__detail">{detail}</p>
    </>
  )
  if (to === undefined) return <div className="sm-tile">{body}</div>
  return (
    <Link className="sm-tile" to={to}>
      {body}
    </Link>
  )
}

/** One count inside a tile's detail line. `bad` is the failure tone, for a failed count only. */
export function TileCount({ tone, children }: { tone?: 'bad' | undefined; children: ReactNode }) {
  return <span className={tone === 'bad' ? 'sm-tile__count--bad' : undefined}>{children}</span>
}

export function Tiles({ children }: { children: ReactNode }) {
  return <div className="sm-tiles">{children}</div>
}
