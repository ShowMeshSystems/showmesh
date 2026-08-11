import { CLOCK_SKEW_WARNING_THRESHOLD_MS } from '../app/time'

// spec section 5.3: "Track and expose clockSkewMs... Seam C surfaces it
// when it exceeds a threshold. Label the threshold in code as an
// unmeasured ShowMesh hypothesis, not a measured value." The threshold
// itself lives in app/time.ts next to the other time hypotheses; this
// component only decides how to render it once that decision has been made.
export interface ClockSkewWarningProps {
  clockSkewMs: number | null
}

export function ClockSkewWarning({ clockSkewMs }: ClockSkewWarningProps) {
  if (clockSkewMs === null) return null
  if (Math.abs(clockSkewMs) < CLOCK_SKEW_WARNING_THRESHOLD_MS) return null

  const direction = clockSkewMs > 0 ? 'ahead of' : 'behind'
  const seconds = Math.round(Math.abs(clockSkewMs) / 1000)

  return (
    <p className="evidence__reason" role="status">
      ⚠ This device's clock is roughly {seconds}s {direction} the coordinator's. Displayed
      ages are computed against the coordinator's clock and remain correct, but if this
      device's own clock is being trusted elsewhere, treat it with that skew in mind.
    </p>
  )
}
