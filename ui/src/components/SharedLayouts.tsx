import type { HTMLAttributes, ReactNode } from 'react'
import '../styles/operator-pages.css'

type Children = { children: ReactNode }

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

function StateBlock({ state, title, reason, role = 'status' }: { state: string; title: string; reason: ReactNode; role?: 'status' | 'alert' }) {
  return <section className={`shared-state-block shared-state-block--${state}`} role={role} aria-label={`${title}: ${state}`}><h2>{title}</h2><p>{reason}</p></section>
}

export function LoadingBlock({ title = 'Loading', reason = 'Waiting for coordinator data.' }: { title?: string; reason?: ReactNode }) { return <StateBlock state="loading" title={title} reason={reason} /> }
export function EmptyBlock({ title, reason, children }: { title: string; reason: ReactNode } & Partial<Children>) { return <StateBlock state="empty" title={title} reason={<>{reason}{children}</>} /> }
export function UnavailableBlock({ title, reason }: { title: string; reason: ReactNode }) { return <StateBlock state="unavailable" title={title} reason={reason} /> }
export function FailedBlock({ title, reason }: { title: string; reason: ReactNode }) { return <StateBlock state="failed" title={title} reason={reason} role="alert" /> }
export function StaleBlock({ title, reason }: { title: string; reason: ReactNode }) { return <StateBlock state="stale" title={title} reason={reason} /> }
export function UnobservedBlock({ title, reason }: { title: string; reason: ReactNode }) { return <StateBlock state="unobserved" title={title} reason={reason} /> }
