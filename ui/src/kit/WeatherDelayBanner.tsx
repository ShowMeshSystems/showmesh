import type { ReactNode } from 'react'
import { Button } from './Button'
import { StatusPair } from './Status'

export type WeatherDelayBannerAction = {
  label: string
  onClick: () => void
  disabled: boolean
  busy: boolean
  title?: string
}

/** One power group's own dark confirmation, ready to render (ADR-053 decision 10). The caller composes every sentence; this component only lays them out. */
export type WeatherDelayGroupView = {
  id: string
  label: string
  /** Set when confirmedDark is true: e.g. "Confirmed dark for 4 m". */
  confirmedLabel?: string
  /** Set when the group's state cannot be trusted, e.g. after a failed read. Never rendered as dark. */
  unknownLabel?: string
  /** Set when confirmedDark is false: the not-dark members, each with the API's own reason sentence verbatim. */
  notDarkMembers?: Array<{ label: string; reason: string }>
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
  groups,
  resume,
  cancelNight,
  error,
  unknown = false,
}: {
  kindLabel: string
  elapsedLabel?: string
  startedByLabel?: string
  savedLabel?: ReactNode
  groups?: WeatherDelayGroupView[]
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
      {groups !== undefined && groups.length > 0 && (
        <div className="sm-wdbanner__groups">
          {groups.map((group) => (
            <div className="sm-wdbanner__group" key={group.id}>
              <span className="sm-wdbanner__group-label">{group.label}</span>
              {group.unknownLabel !== undefined ? (
                <StatusPair tone="unknown" label={group.unknownLabel} />
              ) : group.confirmedLabel !== undefined ? (
                <StatusPair tone="good" label={group.confirmedLabel} />
              ) : (
                <>
                  <StatusPair tone="bad" label="Not dark" />
                  {group.notDarkMembers?.map((member, index) => (
                    <p className="sm-wdbanner__group-reason" key={`${group.id}-${index}`}>
                      <span className="sm-wdbanner__group-member">{member.label}:</span> {member.reason}
                    </p>
                  ))}
                </>
              )}
            </div>
          ))}
        </div>
      )}
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

/**
 * The shell-level banner shown on every screen while players are held dark with
 * no delay active. It never claims a delay is active; Resume opens the gates.
 */
export function WeatherDelayHeldBanner({
  messages,
  resume,
  error,
}: {
  messages: string[]
  resume: WeatherDelayBannerAction
  error?: ReactNode
}) {
  return (
    <div className="sm-wdbanner sm-wdbanner--held" role="region" aria-label="Players held dark">
      <div className="sm-wdbanner__fact">
        <span className="sm-wdbanner__kind" role="alert">
          Players held dark
        </span>
      </div>
      <div className="sm-wdbanner__players">
        {messages.map((message, index) => (
          <p className="sm-wdbanner__player" key={index}>
            {message}
          </p>
        ))}
      </div>
      <div className="sm-wdbanner__actions">
        <Button variant="primary" size="gloved" onClick={resume.onClick} disabled={resume.disabled} title={resume.title}>
          {resume.busy ? 'Resuming…' : resume.label}
        </Button>
      </div>
      {error !== undefined && (
        <p className="sm-wdbanner__error" role="alert">
          {error}
        </p>
      )}
    </div>
  )
}
