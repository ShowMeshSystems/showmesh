import type { ReactNode } from 'react'
import { TONE_GLYPH } from './tone'

/**
 * Chrome-level warning that every age and relative time on screen is off.
 * The caller owns the wording: the kit does not know how this app formats a
 * duration, and must not import the app's domain modules to find out.
 */
export function ClockSkewStrip({ children }: { children: ReactNode }) {
  return (
    <div className="sm-skewstrip" role="status">
      <span aria-hidden="true">{TONE_GLYPH.warn}</span>
      <span className="sm-skewstrip__word">Clock skew</span>
      <span>{children}</span>
    </div>
  )
}
