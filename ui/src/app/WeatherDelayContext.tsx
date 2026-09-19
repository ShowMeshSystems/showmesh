import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import {
  ApiError,
  cancelNightForWeather,
  getWeatherDelayState,
  resumeFromWeatherDelay,
  startWeatherDelay,
  type WeatherDelayActionResult,
  type WeatherDelayStateResponse,
} from '../api'
import { describeApiError } from '../domain/session'
import { useModelContext } from './ModelContext'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: WeatherDelayStateResponse }
  | { kind: 'failed'; reason: string }

type WeatherDelayActionName = 'start' | 'cancelNight' | 'resume'

type ActionOutcome =
  | { kind: 'idle' }
  | { kind: 'result'; action: WeatherDelayActionName; result: WeatherDelayActionResult }
  | { kind: 'error'; action: WeatherDelayActionName; message: string; status: number | undefined }

interface WeatherDelayContextValue {
  state: LoadState
  busy: WeatherDelayActionName | false
  outcome: ActionOutcome
  start: () => void
  cancelNight: () => void
  resume: () => void
  dismissOutcome: () => void
}

const WeatherDelayContext = createContext<WeatherDelayContextValue | null>(null)

// Poll fallback while the coordinator's own `weatherDelay.changed` stream
// kind is not yet documented (see this branch's ask/answer record): a
// short interval while a delay is active gets an operator on another
// device the state within a few seconds even with no stream frame at all.
const POLL_ACTIVE_MS = 5_000
const POLL_IDLE_MS = 15_000

/**
 * ADR-053: the one place `GET /weather-delay` is read and polled, and
 * `/start`, `/cancel-night` and `/resume` are dispatched from, so the
 * shell banner (every screen) and Live Control's own controls share one
 * fetch and one result rather than racing two independent pollers.
 *
 * Reacts to `event.recorded` frames whose `category` is
 * `weatherDelay.changed` (this coordinator records the change as an
 * event even though `/stream` does not document a first-class
 * `weatherDelay.changed` SSE kind yet) for a near-immediate refresh, and
 * falls back to polling and to a refetch on reconnect.
 */
export function WeatherDelayProvider({ children }: { children: ReactNode }) {
  const model = useModelContext()
  const authenticated = model.session?.authenticated === true
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [busy, setBusy] = useState<WeatherDelayActionName | false>(false)
  const [outcome, setOutcome] = useState<ActionOutcome>({ kind: 'idle' })

  const refresh = useCallback(() => {
    getWeatherDelayState()
      .then((response) => setState({ kind: 'loaded', response }))
      .catch((err: unknown) => setState({ kind: 'failed', reason: describeApiError(err) }))
  }, [])

  useEffect(() => {
    if (!authenticated) return
    refresh()
  }, [authenticated, refresh])

  const lastEventSeq = useRef<number | null>(null)
  useEffect(() => {
    if (!authenticated) return
    const latest = model.events[0]
    if (latest === undefined || lastEventSeq.current === latest.seq) return
    lastEventSeq.current = latest.seq
    if (latest.category === 'weatherDelay.changed') refresh()
  }, [authenticated, model.events, refresh])

  // A `stream.reset` or a fresh reconnect carries no guarantee of its own
  // event.recorded frame for state that changed while disconnected; the
  // one signal every reconnect does carry is the connection itself going
  // live again.
  const wasLive = useRef(model.connection.kind === 'live')
  useEffect(() => {
    const isLive = model.connection.kind === 'live'
    if (authenticated && isLive && !wasLive.current) refresh()
    wasLive.current = isLive
  }, [authenticated, model.connection.kind, refresh])

  useEffect(() => {
    if (!authenticated) return
    const active = state.kind === 'loaded' && state.response.active
    const id = setInterval(refresh, active ? POLL_ACTIVE_MS : POLL_IDLE_MS)
    return () => clearInterval(id)
  }, [authenticated, state, refresh])

  const run = useCallback(
    (action: WeatherDelayActionName, call: () => Promise<WeatherDelayActionResult>) => {
      setBusy(action)
      setOutcome({ kind: 'idle' })
      call()
        .then((result) => {
          setOutcome({ kind: 'result', action, result })
          refresh()
        })
        .catch((err: unknown) => {
          setOutcome({
            kind: 'error',
            action,
            message: describeApiError(err),
            status: err instanceof ApiError ? err.status : undefined,
          })
        })
        .finally(() => setBusy(false))
    },
    [refresh],
  )

  const value: WeatherDelayContextValue = {
    state,
    busy,
    outcome,
    start: () => run('start', startWeatherDelay),
    cancelNight: () => run('cancelNight', cancelNightForWeather),
    resume: () => run('resume', resumeFromWeatherDelay),
    dismissOutcome: () => setOutcome({ kind: 'idle' }),
  }

  return <WeatherDelayContext.Provider value={value}>{children}</WeatherDelayContext.Provider>
}

export function useWeatherDelay(): WeatherDelayContextValue {
  const ctx = useContext(WeatherDelayContext)
  if (ctx === null) throw new Error('useWeatherDelay() called outside <WeatherDelayProvider>.')
  return ctx
}
