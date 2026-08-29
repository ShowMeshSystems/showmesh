import type { ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { StatusBadge, type StatusTone } from '../../components/StatusBadge'

// Readiness split in two (UI-DESIGN-GUIDE.md section 8 / Dashboard.dc.html):
// "Running" and "Next start gated" are different questions. Mid-show, one
// combined verdict has to imply something is wrong with a show that is
// playing fine — splitting removes that false implication.
export interface ReadinessCardProps {
  heading: string
  tone: StatusTone
  icon: string
  label: string
  detail: ReactNode
  actions?: ReactNode
}

function ReadinessCard({ heading, tone, icon, label, detail, actions }: ReadinessCardProps) {
  return (
    <article className={`readiness-card readiness-card--${tone}`} aria-labelledby={`readiness-card-${heading}`}>
      <h3 id={`readiness-card-${heading}`} className="t-subhead">
        {heading}
      </h3>
      <p className="readiness-card__verdict">
        <StatusBadge tone={tone} icon={icon} label={label} />
      </p>
      <p className="readiness-card__detail t-small text-muted">{detail}</p>
      {actions && <div className="readiness-card__actions">{actions}</div>}
    </article>
  )
}

export function ReadinessSplit({ running, nextStart }: { running: ReadinessCardProps; nextStart: ReadinessCardProps }) {
  return (
    <section className="readiness-split" aria-labelledby="dashboard-readiness-split">
      <h2 id="dashboard-readiness-split" className="t-heading">
        Readiness
      </h2>
      <div className="readiness-split__cards">
        <ReadinessCard {...running} />
        <ReadinessCard {...nextStart} />
      </div>
    </section>
  )
}

export function ReadinessDetailLink() {
  return (
    <Link className="btn btn--quiet btn--compact" to="/monitor/readiness">
      See readiness detail →
    </Link>
  )
}
