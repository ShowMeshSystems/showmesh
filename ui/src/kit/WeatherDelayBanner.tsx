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
 * The shell-level, non-dismissible banner shown on every screen while a
 * weather delay or a weather cancel-night is active, or while that state
 * cannot be read. The caller owns every word; this component owns the shape.
 */
export function WeatherDelayBanner({
  kindLabel,
  elapsedLabel,
  startedByLabel,
  savedLabel,
  resume,
  cancelNight,
  error,
  unknown = false,
}: {
  kindLabel: string
  elapsedLabel?: string
  startedByLabel?: string
  savedLabel?: ReactNode
  resume?: WeatherDelayBannerAction
  cancelNight?: WeatherDelayBannerAction
  error?: ReactNode
  unknown?: boolean
}) {
  return (
    <div className={unknown ? 'sm-wdbanner sm-wdbanner--unknown' : 'sm-wdbanner'} role="region" aria-label={kindLabel}>
      <div className="sm-wdbanner__fact">
        <span className="sm-wdbanner__kind" role="alert">
          {kindLabel}
        </span>
        {elapsedLabel !== undefined && <span>{elapsedLabel}</span>}
        {startedByLabel !== undefined && <span>{startedByLabel}</span>}
        {savedLabel !== undefined && <span>{savedLabel}</span>}
      </div>
      {(cancelNight !== undefined || resume !== undefined) && (
        <div className="sm-wdbanner__actions">
          {cancelNight !== undefined && (
            <Button
              variant="danger"
              size="gloved"
              onClick={cancelNight.onClick}
              disabled={cancelNight.disabled}
              title={cancelNight.title}
            >
              {cancelNight.busy ? 'Cancelling night…' : cancelNight.label}
            </Button>
          )}
          {resume !== undefined && (
            <Button variant="primary" size="gloved" onClick={resume.onClick} disabled={resume.disabled} title={resume.title}>
              {resume.busy ? 'Resuming…' : resume.label}
            </Button>
          )}
        </div>
      )}
      {error !== undefined && (
        <p className="sm-wdbanner__error" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
