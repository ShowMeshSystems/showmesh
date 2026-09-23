import { useCallback, useEffect, useState } from 'react'
import { dispatchNightCommand, getCurrentNightSession, type Model, type NightSessionState } from '../api'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'

export const STOP_HOLD_BANNER_TEXT = 'The show is stopped. Press Resume on Live Control to start the show playlist from its first song.'
export const RESUME_NO_HOLD_HINT = 'The show is not stopped, so there is nothing to resume.'
export const RESUME_HINT = 'Starts the show playlist from its first song.'
export const RESUME_CONFIRM_TITLE = 'Start the show playlist from its first song now?'

type StopHold = NonNullable<NightSessionState['stopHold']>

/**
 * The current night session: the live stream's copy once it has reported, a read until then.
 * A stream reset discards the earlier read and reads again, so a cleared hold never comes back.
 */
export function useCurrentNightSession(model: Model): NightSessionState | null {
  const streamed = model.nightSession
  const [seeded, setSeeded] = useState<NightSessionState | null>(null)
  const [reported, setReported] = useState(false)
  const [readGeneration, setReadGeneration] = useState(0)
  if (streamed !== null && !reported) setReported(true)
  if (streamed === null && reported) {
    setReported(false)
    setSeeded(null)
    setReadGeneration((g) => g + 1)
  }
  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((response) => {
        if (!cancelled) setSeeded(response.session)
      })
      .catch(() => {
        // An unread session shows no hold; the stream reports it once it arrives.
      })
    return () => {
      cancelled = true
    }
  }, [readGeneration])
  return streamed ?? seeded
}

export function stopHoldDetail(hold: StopHold): string {
  const at = formatClock(hold.at) ?? 'an unreported time'
  return hold.principal === undefined || hold.principal === '' ? `Stopped at ${at}` : `Stopped at ${at} by ${hold.principal}`
}

export type ResumeShowControl = {
  held: boolean
  disabled: boolean
  busy: boolean
  title: string
  /** Opens the confirm; nothing is sent until onConfirm. */
  onClick: () => void
  confirmOpen: boolean
  onConfirm: () => void
  onCancel: () => void
  error: string | null
}

/** Resume is enabled only while a hold stands and the device holds night:command. */
export function useResumeShow(model: Model, session: NightSessionState | null): ResumeShowControl {
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'night:command')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [confirmOpen, setConfirmOpen] = useState(false)
  const held = session?.stopHold !== undefined
  const onConfirm = useCallback(() => {
    setConfirmOpen(false)
    setBusy(true)
    setError(null)
    dispatchNightCommand('resume-show')
      .catch((err: unknown) => setError(describeApiError(err)))
      .finally(() => setBusy(false))
  }, [])
  return {
    held,
    disabled: !gate.allowed || !held || busy,
    busy,
    title: !gate.allowed ? gate.reason : held ? RESUME_HINT : RESUME_NO_HOLD_HINT,
    onClick: useCallback(() => setConfirmOpen(true), []),
    confirmOpen,
    onConfirm,
    onCancel: useCallback(() => setConfirmOpen(false), []),
    error,
  }
}
