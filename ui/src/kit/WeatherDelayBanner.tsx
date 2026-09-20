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
 * The shell-level banner shown on every screen while an automatic trigger's
 * question is pending (ADR-053 decision 12): the reason sentence, a
 * countdown to the deadline, and one-press buttons with no confirmation
 * step. Visually distinct from the active-delay and held-dark banners
 * (`sm-wdbanner--question`, an accent tone rather than bad or warn) so it
 * reads as "answer this" rather than "something is already wrong".
 *
 * A question the coordinator no longer reports is rendered by omitting
 * countdownLabel, consequenceLabel and start: what happened stays on
 * screen, but nothing is offered that would act on a question that is gone.
 */
export function WeatherDelayQuestionBanner({
  reason,
  countdownLabel,
  consequenceLabel,
  askedLabel,
  expiresLabel,
  start,
  cancelNight,
  dismiss,
  error,
}: {
  reason: string
  countdownLabel?: string
  consequenceLabel?: string
  askedLabel?: string
  expiresLabel?: string
  start?: WeatherDelayBannerAction
  cancelNight?: WeatherDelayBannerAction
  dismiss: WeatherDelayBannerAction
  error?: ReactNode
}) {
  return (
    <div className="sm-wdbanner sm-wdbanner--question" role="region" aria-label="Weather trigger question">
      <div className="sm-wdbanner__fact">
        <span className="sm-wdbanner__kind" role="alert">
          {reason}
        </span>
        {consequenceLabel !== undefined && <span>{consequenceLabel}</span>}
        {countdownLabel !== undefined && <span>{countdownLabel}</span>}
        {askedLabel !== undefined && <span>{askedLabel}</span>}
        {expiresLabel !== undefined && <span>{expiresLabel}</span>}
      </div>
      <div className="sm-wdbanner__actions">
        {start !== undefined && (
          <Button variant="primary" size="gloved" onClick={start.onClick} disabled={start.disabled} title={start.title}>
            {start.busy ? 'Starting…' : start.label}
          </Button>
        )}
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
        <Button variant="quiet" size="gloved" onClick={dismiss.onClick} disabled={dismiss.disabled} title={dismiss.title}>
          {dismiss.busy ? 'Dismissing…' : dismiss.label}
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
