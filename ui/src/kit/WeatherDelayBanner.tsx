import type { ReactNode } from 'react'
import { Button } from './Button'

export type WeatherDelayBannerAction = {
  label: string
  onClick: () => void
  disabled: boolean
  busy: boolean
  title?: string
}

/**
 * ADR-053: the one shell-level, non-dismissible banner shown on every
 * screen while a weather delay or a weather cancel-night is active. The
 * caller owns every word (kind label, elapsed time, who started it,
 * whether it was saved) — this component only owns the shape, matching
 * `ClockSkewStrip`'s split.
 */
export function WeatherDelayBanner({
  kindLabel,
  elapsedLabel,
  startedByLabel,
  savedLabel,
  resume,
  cancelNight,
  error,
}: {
  kindLabel: string
  elapsedLabel: string
  startedByLabel: string
  savedLabel?: ReactNode
  resume: WeatherDelayBannerAction
  cancelNight?: WeatherDelayBannerAction
  error?: ReactNode
}) {
  return (
    <div className="sm-wdbanner" role="status">
      <div className="sm-wdbanner__fact">
        <span className="sm-wdbanner__kind">{kindLabel}</span>
        <span>{elapsedLabel}</span>
        <span>{startedByLabel}</span>
        {savedLabel !== undefined && <span>{savedLabel}</span>}
      </div>
      <div className="sm-wdbanner__actions">
        {cancelNight !== undefined && (
          <Button
            variant="danger"
            size="compact"
            onClick={cancelNight.onClick}
            disabled={cancelNight.disabled}
            title={cancelNight.title}
          >
            {cancelNight.busy ? 'Cancelling night…' : 'Cancel night'}
          </Button>
        )}
        <Button variant="primary" size="compact" onClick={resume.onClick} disabled={resume.disabled} title={resume.title}>
          {resume.busy ? 'Resuming…' : resume.label}
        </Button>
      </div>
      {error !== undefined && <p className="sm-wdbanner__error">{error}</p>}
    </div>
  )
}
