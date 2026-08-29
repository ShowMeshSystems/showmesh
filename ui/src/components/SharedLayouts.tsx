import type { HTMLAttributes, ReactNode } from 'react'
import '../styles/operator-pages.css'

type Children = { children: ReactNode }
type StateBlockHeadingLevel = 2 | 3
type StateBlockOptions = { headingLevel?: StateBlockHeadingLevel | undefined }

export function OperatorPageHeader({ title, eyebrow, lede, actions }: { title: string; eyebrow?: string; lede?: ReactNode; actions?: ReactNode }) {
  return (
    <header className="shared-page-header">
      <div>
        {eyebrow && <p className="shared-page-header__eyebrow">{eyebrow}</p>}
        <h1 className="shared-page-header__title">{title}</h1>
        {lede && <p className="shared-page-header__lede">{lede}</p>}
      </div>
      {actions && <div className="shared-page-header__actions">{actions}</div>}
    </header>
  )
}

export function OperatorSection({ title, detail, actions, children, ...props }: { title: string; detail?: ReactNode; actions?: ReactNode } & Children & HTMLAttributes<HTMLElement>) {
  const headingId = props['aria-labelledby']
  return (
    <section className="shared-section" {...props}>
      <div className="shared-section__heading">
        <div>
          <h2 id={typeof headingId === 'string' ? headingId : undefined}>{title}</h2>
          {detail && <p>{detail}</p>}
        </div>
        {actions && <div className="shared-section__actions">{actions}</div>}
      </div>
      {children}
    </section>
  )
}

export function StatusStrip({ label, children }: { label: string } & Children) {
  return <dl className="shared-status-strip" role="group" aria-label={label}>{children}</dl>
}

export function StatusStripItem({ label, detail, children, tone = 'unknown' }: { label: string; detail?: ReactNode; tone?: 'good' | 'warn' | 'bad' | 'unknown' } & Children) {
  return (
    <div className={`shared-status-strip__item shared-status-strip__item--${tone}`}>
      <dt>{label}</dt>
      <dd>{children}</dd>
      {detail && <span>{detail}</span>}
    </div>
  )
}

export function AttentionList({ label = 'Needs attention', children }: { label?: string } & Children) {
  return <ul className="shared-attention-list" aria-label={label}>{children}</ul>
}

export function AttentionListItem({ children }: Children) {
  return <li className="shared-attention-list__item">{children}</li>
}

export function OverviewDetailWorkspace({ overview, detail }: { overview: ReactNode; detail: ReactNode }) {
  return <div className="shared-overview-detail"><div className="shared-overview-detail__overview">{overview}</div><div className="shared-overview-detail__detail">{detail}</div></div>
}

export function CommandGroup({ title, detail, children }: { title: string; detail?: ReactNode } & Children) {
  return <section className="shared-command-group" aria-label={title}><h3>{title}</h3>{detail && <p>{detail}</p>}<div className="shared-command-group__commands">{children}</div></section>
}

export function ConfigurationSection({ title, detail, children }: { title: string; detail?: ReactNode } & Children) {
  return <section className="shared-configuration-section" aria-label={title}><div><h2>{title}</h2>{detail && <p>{detail}</p>}</div>{children}</section>
}

export function EvidenceTable({ label, children }: { label: string } & Children) {
  return <div className="shared-evidence-table" role="region" aria-label={label} tabIndex={0}>{children}</div>
}

/* The eight inline absence and refusal states, rendered as the design's ruled
 * strip: a mono state word in a left column, the fact and its explanation on the
 * right, hairline top and bottom. It sits in the row or field where the content
 * would have been, so it carries no fill, no radius and no card.
 *
 * `state` drives the colour, and only `unobserved` gets a dashed edge. Stale, an
 * unsupported field and an empty list are settled facts, and must not borrow the
 * shape of evidence that was never collected. */
const STATE_WORD: Record<string, string> = {
  loading: 'Loading',
  empty: 'Empty',
  stale: 'Stale',
  failed: 'Failed',
  unavailable: 'Unavailable',
  unobserved: 'Unobserved',
  signed_out: 'Signed out',
  no_permission: 'No permission',
}

type StateBlockProps = {
  state: string
  title: string
  reason: ReactNode
  role?: 'status' | 'alert'
  /* Appended to the state word after a middot, for the one detail that belongs
   * beside it: a stale value's age, or a failed read's retry hint. */
  stateDetail?: string | undefined
  actions?: ReactNode | undefined
} & StateBlockOptions

function StateBlock({ state, title, reason, role = 'status', headingLevel = 2, stateDetail, actions }: StateBlockProps) {
  const word = STATE_WORD[state] ?? state
  return (
    <section className={`shared-state-block ruled-strip ruled-strip--${state.replace('_', '-')} shared-state-block--${state}`} role={role} aria-label={`${title}: ${state}`}>
      <span className="ruled-strip__state t-meta">{stateDetail ? `${word} · ${stateDetail}` : word}</span>
      <div>
        {headingLevel === 3 ? <h3 className="ruled-strip__fact">{title}</h3> : <h2 className="ruled-strip__fact">{title}</h2>}
        <p className="ruled-strip__explanation">{reason}</p>
        {actions && <div className="ruled-strip__actions">{actions}</div>}
      </div>
    </section>
  )
}

export function LoadingBlock({ title = 'Loading', reason = 'Waiting for coordinator data.', headingLevel }: { title?: string; reason?: ReactNode } & StateBlockOptions) { return <StateBlock state="loading" title={title} reason={reason} headingLevel={headingLevel} /> }
export function EmptyBlock({ title, reason, children, headingLevel, actions }: { title: string; reason: ReactNode; actions?: ReactNode | undefined } & Partial<Children> & StateBlockOptions) { return <StateBlock state="empty" title={title} reason={<>{reason}{children}</>} headingLevel={headingLevel} actions={actions} /> }
export function UnavailableBlock({ title, reason, headingLevel }: { title: string; reason: ReactNode } & StateBlockOptions) { return <StateBlock state="unavailable" title={title} reason={reason} headingLevel={headingLevel} /> }
export function FailedBlock({ title, reason, headingLevel, stateDetail, actions }: { title: string; reason: ReactNode; stateDetail?: string | undefined; actions?: ReactNode | undefined } & StateBlockOptions) { return <StateBlock state="failed" title={title} reason={reason} role="alert" headingLevel={headingLevel} stateDetail={stateDetail} actions={actions} /> }
export function StaleBlock({ title, reason, headingLevel, stateDetail, actions }: { title: string; reason: ReactNode; stateDetail?: string | undefined; actions?: ReactNode | undefined } & StateBlockOptions) { return <StateBlock state="stale" title={title} reason={reason} headingLevel={headingLevel} stateDetail={stateDetail} actions={actions} /> }
export function UnobservedBlock({ title, reason, headingLevel }: { title: string; reason: ReactNode } & StateBlockOptions) { return <StateBlock state="unobserved" title={title} reason={reason} headingLevel={headingLevel} /> }

/* Reads stay open when writes do not, so a signed-out region states what is still
 * readable rather than blanking. */
export function SignedOutBlock({ title, reason, headingLevel, actions }: { title: string; reason: ReactNode; actions?: ReactNode | undefined } & StateBlockOptions) { return <StateBlock state="signed_out" title={title} reason={reason} headingLevel={headingLevel} actions={actions} /> }

/* A refusal from a healthy coordinator, never a connection problem. */
export function NoPermissionBlock({ title, reason, headingLevel, actions }: { title: string; reason: ReactNode; actions?: ReactNode | undefined } & StateBlockOptions) { return <StateBlock state="no_permission" title={title} reason={reason} headingLevel={headingLevel} actions={actions} /> }

/* The second state treatment: a whole region that cannot render at all. The hatch
 * runs in the 76px gutter only and the copy sits on clean surface, so absence
 * never takes the shape of a card containing data. */
export function BlankingPlate({ variant, stamp, eyebrow, title, explanation, actions, headingLevel = 2 }: {
  variant: 'unobserved' | 'empty' | 'unavailable' | 'permission'
  stamp: string
  eyebrow: string
  title: ReactNode
  explanation: ReactNode
  actions?: ReactNode | undefined
} & StateBlockOptions) {
  return (
    <section className={`plate plate--${variant}`} role={variant === 'permission' ? 'alert' : 'status'}>
      <div className="plate__gutter">
        <span className="plate__stamp t-meta">{stamp}</span>
      </div>
      <div className="plate__body">
        <p className="plate__eyebrow t-meta">{eyebrow}</p>
        {headingLevel === 3 ? <h3 className="plate__heading t-heading">{title}</h3> : <h2 className="plate__heading t-heading">{title}</h2>}
        <p className="plate__explanation">{explanation}</p>
        {actions && <div className="plate__actions">{actions}</div>}
      </div>
    </section>
  )
}

/* A design idea that is drawn in the mocks but is not a working feature.
 *
 * Deliberately NOT one of the four absences, and it must never be mistaken for
 * one. The absences describe data the coordinator does or does not hold. This
 * describes a control that does not exist at all, so it takes its own visual
 * channel and carries a stamp an operator cannot read past.
 *
 * `why` states plainly what is missing, so a good idea stays visible and legible
 * as an idea without ever looking live. Anything passed as `preview` is a drawing
 * of a control, never a control: it is inert and hidden from assistive tech. */
export function PlannedFeature({ title, why, preview, headingLevel = 3 }: {
  title: string
  why: ReactNode
  preview?: ReactNode | undefined
} & StateBlockOptions) {
  return (
    <section className="planned" role="note" aria-label={`Not built: ${title}`}>
      <div className="planned__rule" aria-hidden="true" />
      <div className="planned__body">
        <span className="planned__stamp t-meta">Not built</span>
        {headingLevel === 2
          ? <h2 className="planned__title">{title}</h2>
          : <h3 className="planned__title">{title}</h3>}
        <p className="planned__why">{why}</p>
        {preview && <div className="planned__preview" aria-hidden="true" inert>{preview}</div>}
      </div>
    </section>
  )
}
