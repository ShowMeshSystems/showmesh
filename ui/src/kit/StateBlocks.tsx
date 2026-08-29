import type { ReactNode } from 'react'

/**
 * The four absences, plus the session and permission states that share their
 * shape. Only `unobserved` means never collected, and only it takes the
 * dashed edge; empty, stale and unavailable are settled facts.
 */
export type Absence =
  | 'loading'
  | 'empty'
  | 'stale'
  | 'failed'
  | 'unavailable'
  | 'unobserved'
  | 'signedOut'
  | 'noPermission'

const LABEL_TONE: Record<Absence, string> = {
  loading: '',
  empty: '',
  stale: 'sm-strip__label--warn',
  failed: 'sm-strip__label--bad',
  unavailable: '',
  unobserved: 'sm-strip__label--unobserved',
  signedOut: '',
  noPermission: 'sm-strip__label--warn',
}

type StripProps = {
  absence: Absence
  /** The state word, plus any qualifier: "Stale · 4 m 12 s". */
  label: string
  /** The fact, at value weight. */
  fact: ReactNode
  /** The caveat and the action, at helper size. */
  detail?: ReactNode
}

/**
 * The default absence treatment. Sits in the row or field where the content
 * would have been: no fill, no radius, no card.
 */
export function RuledStrip({ absence, label, fact, detail }: StripProps) {
  return (
    <div className="sm-strip">
      <span className={['sm-strip__label', LABEL_TONE[absence]].filter(Boolean).join(' ')}>{label}</span>
      <div>
        <p className="sm-strip__fact">{fact}</p>
        {detail !== undefined && <p className="sm-strip__detail">{detail}</p>}
      </div>
    </div>
  )
}

type PlateProps = {
  absence: Absence
  /** Short stamp for the hatched gutter: "No sig", "Perm". */
  stamp: string
  /** Resource and state: "Audio routing · unobserved". */
  eyebrow: string
  title: ReactNode
  detail?: ReactNode
  actions?: ReactNode
}

const PLATE_TONE: Partial<Record<Absence, string>> = {
  stale: 'sm-plate--warn',
  noPermission: 'sm-plate--warn',
  failed: 'sm-plate--bad',
  unobserved: 'sm-plate--unobserved',
}

/**
 * For a whole region that cannot render. The hatch runs in the gutter only;
 * copy always sits on clean surface.
 */
export function BlankingPlate({ absence, stamp, eyebrow, title, detail, actions }: PlateProps) {
  const classes = ['sm-plate', PLATE_TONE[absence]].filter(Boolean).join(' ')
  return (
    <div className={classes}>
      <div className="sm-plate__gutter">
        <span className="sm-plate__stamp">{stamp}</span>
      </div>
      <div className="sm-plate__body">
        <p className="sm-plate__eyebrow">{eyebrow}</p>
        <p className="sm-plate__title">{title}</p>
        {detail !== undefined && <p className="sm-plate__detail">{detail}</p>}
        {actions !== undefined && <div className="sm-plate__actions">{actions}</div>}
      </div>
    </div>
  )
}
