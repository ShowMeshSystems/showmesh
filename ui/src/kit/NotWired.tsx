import { cloneElement, type ReactElement, type ReactNode } from 'react'

/**
 * A mock control with no endpoint behind it. Ruled 2026-08-29: build it as
 * drawn, make it inert, and say so loudly enough that nobody discovers it on
 * a show night. Never use this for a control that works.
 */
type BannerProps = {
  /** What the section would do once it is wired: "Fire an announcement cue". */
  what: string
  /** The missing fact, e.g. `<code className="sm-data">POST /cues/{'{id}'}/fire</code>` for a known path, or plain prose for a described absence. Callers wrap a path in `<code>` themselves; this component never assumes one. */
  missing: ReactNode
  detail?: ReactNode
}

export function NotWiredBanner({ what, missing, detail }: BannerProps) {
  return (
    <div className="sm-nowire" role="note">
      <div className="sm-nowire__gutter">
        <span className="sm-nowire__stamp">Not wired</span>
      </div>
      <div className="sm-nowire__body">
        <p className="sm-nowire__title">{what} does nothing yet.</p>
        <p className="sm-nowire__detail">The control below is drawn to its final shape and is deliberately inert. The coordinator has no {missing}, so nothing is sent and nothing happens.</p>
        {detail !== undefined && <p className="sm-nowire__detail">{detail}</p>}
      </div>
    </div>
  )
}

/**
 * Wraps one inert control and tags it in place, so the warning travels with
 * the button rather than living only at the top of the section. Forces
 * `disabled` on the child: the tag may never appear on a live control.
 */
export function NotWired({ children, label = 'Not wired' }: { children: ReactElement; label?: string }) {
  return (
    <span className="sm-nowire-tag">
      {cloneElement(children, { disabled: true } as { disabled: boolean })}
      <span className="sm-nowire-tag__chip" aria-hidden="true">
        {label}
      </span>
      <span className="sm-sr-only">{label}. This control does nothing.</span>
    </span>
  )
}
