import type { ReactNode } from 'react'

/** A page is an h1 and one muted line. Nothing goes above or beside it. */
export function PageTitle({ title, lede }: { title: string; lede?: ReactNode }) {
  return (
    <>
      <h1 className="sm-page__title">{title}</h1>
      {lede !== undefined && <p className="sm-page__lede">{lede}</p>}
    </>
  )
}

type SectionProps = {
  id: string
  title: string
  /** Section eyebrow. Legal above an h2, never above the page h1. */
  eyebrow?: string
  detail?: ReactNode
  /** Sits on the heading row, right-aligned. */
  aside?: ReactNode
  children: ReactNode
}

export function Section({ id, title, eyebrow, detail, aside, children }: SectionProps) {
  return (
    <section className="sm-section" aria-labelledby={id}>
      {eyebrow !== undefined && <p className="sm-eyebrow">{eyebrow}</p>}
      <div className="sm-section__head">
        <h2 className="sm-section__title" id={id}>{title}</h2>
        {aside}
      </div>
      {detail !== undefined && <p className="sm-section__detail">{detail}</p>}
      {children}
    </section>
  )
}

export function Callout({ children }: { children: ReactNode }) {
  return <p className="sm-callout">{children}</p>
}

export type Definition = { term: string; value: ReactNode; detail?: ReactNode }

/** Hairline-separated facts in one row. */
export function DefinitionStrip({ items }: { items: readonly Definition[] }) {
  return (
    <dl className="sm-defs">
      {items.map((item) => (
        <div key={item.term}>
          <dt>{item.term}</dt>
          <dd>{item.value}</dd>
          {item.detail !== undefined && <dd>{item.detail}</dd>}
        </div>
      ))}
    </dl>
  )
}
