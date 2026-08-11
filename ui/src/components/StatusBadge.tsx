// Generic status badge: icon glyph + text label + color, always together.
// OBSERVABILITY section 6.3 / spec section 6.6: health must never be
// encoded by color alone, verified in grayscale -- this component is the
// one place that pairing is implemented, so every caller inherits it
// rather than re-deciding it per panel.
export type StatusTone = 'good' | 'warn' | 'bad' | 'unknown'

export interface StatusBadgeProps {
  tone: StatusTone
  icon: string
  label: string
}

export function StatusBadge({ tone, icon, label }: StatusBadgeProps) {
  return (
    <span className={`status-badge status-badge--${tone}`}>
      <span className="status-badge__icon" aria-hidden="true">
        {icon}
      </span>
      <span>{label}</span>
    </span>
  )
}
