import { useEffect, useRef, useState } from 'react'
import { putFPPBrightnessCeiling } from '../api'
import { AbsenceLabel, Field, Input, RuledStrip, Section, Slider, StatusPair } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { ceilingSentence, MAX_FADE_SECONDS, parseFadeSeconds, playerStateStatus, reportedCeiling } from './displayBrightnessModel'
import { useFPPBrightness } from './useFPPBrightness'

const BRIGHTNESS_DEBOUNCE_MS = 250

/** Settings > Brightness: the display's one brightness ceiling, which every FPP player follows (ADR-062). */
export function SettingsBrightness() {
  const model = useModelContext()
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'fpp:command')
  const [read, accept] = useFPPBrightness()
  const [sliderValue, setSliderValue] = useState<number | null>(null)
  const [fadeText, setFadeText] = useState('0')
  const [writeError, setWriteError] = useState<string | null>(null)
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const inFlightRef = useRef(false)
  const pendingRef = useRef<number | null>(null)

  const desired = read.doc?.ceiling?.targetPercent ?? null
  useEffect(() => {
    const writing = debounceRef.current !== null || inFlightRef.current || pendingRef.current !== null
    if (!writing) setSliderValue(desired)
  }, [desired])
  useEffect(() => () => {
    if (debounceRef.current !== null) clearTimeout(debounceRef.current)
  }, [])

  const fadeSeconds = parseFadeSeconds(fadeText)

  const send = (value: number) => {
    if (inFlightRef.current) {
      pendingRef.current = value
      return
    }
    inFlightRef.current = true
    putFPPBrightnessCeiling(value, fadeSeconds ?? 0)
      .then((doc) => {
        accept(doc)
        setWriteError(null)
      })
      .catch((err: unknown) => setWriteError(describeApiError(err)))
      .finally(() => {
        inFlightRef.current = false
        if (pendingRef.current !== null) {
          const next = pendingRef.current
          pendingRef.current = null
          send(next)
        }
      })
  }

  const onChange = (value: number) => {
    setSliderValue(value)
    if (debounceRef.current !== null) clearTimeout(debounceRef.current)
    debounceRef.current = setTimeout(() => {
      debounceRef.current = null
      send(value)
    }, BRIGHTNESS_DEBOUNCE_MS)
  }

  const notSet = read.doc !== null && sliderValue === null
  const displayed = sliderValue ?? 100
  const sliderDisabledReason = !gate.allowed ? gate.reason : fadeSeconds === null ? 'Enter a fade from 0 to 86400 seconds first.' : undefined
  const sentence = read.doc !== null ? ceilingSentence(read.doc) : null

  return (
    <>
      <p className="sm-small sm-muted">Settings <span className="sm-faint">/</span> Brightness</p>
      <h2 className="sm-section__title">One brightness for the whole display</h2>
      {read.doc === null ? (
        read.error !== null ? (
          <RuledStrip absence="failed" label="Read failed" fact={read.error} />
        ) : (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the display brightness." />
        )
      ) : (
        <>
          {read.error !== null && (
            <RuledStrip absence="stale" label="Stale" fact={read.error} detail={`Showing what was read at ${formatClock(read.readAt) ?? 'an earlier time'}.`} />
          )}

          <Section id="st-brightness-ceiling" title="Ceiling" detail="Every FPP player follows this ceiling. A change made on any player becomes the new ceiling.">
            <Slider
              label="Display ceiling"
              value={displayed}
              min={0}
              max={100}
              valueLabel={notSet ? 'Not set' : `${displayed}%`}
              onChange={(e) => onChange(Number(e.target.value))}
              disabled={sliderDisabledReason !== undefined}
              title={sliderDisabledReason}
            />
            <Field label="Fade" help="Seconds, applied to the next change." error={fadeSeconds === null ? `Enter whole seconds from 0 to ${MAX_FADE_SECONDS}.` : undefined}>
              {(props) => (
                <Input {...props} inputMode="numeric" value={fadeText} onChange={(e) => setFadeText(e.target.value)} className="sm-input--narrow" disabled={!gate.allowed} />
              )}
            </Field>
            {sentence !== null ? (
              <p className="sm-small sm-muted">{sentence}</p>
            ) : (
              <RuledStrip absence="empty" label="Not set" fact="No display brightness has been set. Each player keeps its own ceiling until one is." />
            )}
            {writeError !== null && <RuledStrip absence="failed" label="Not saved" fact={writeError} />}
          </Section>

          <Section id="st-brightness-players" title="FPP players" detail="Each player's own report of its ceiling.">
            {read.doc.players.length === 0 ? (
              <RuledStrip absence="empty" label="No players" fact="No FPP players are configured. Add one on the Connections tab." />
            ) : (
              <div className="sm-panel sm-stack-4">
                {read.doc.players.map((player) => {
                  const status = playerStateStatus(player.state)
                  const reported = reportedCeiling(player)
                  return (
                    <div key={player.instanceId} className="sm-panel">
                      <div className="sm-inline-row">
                        <span className="sm-data">{player.instanceId}</span>
                        <StatusPair tone={status.tone} label={status.word} />
                        {reported !== null ? <span className="sm-data">{reported}</span> : <AbsenceLabel absence="unavailable" label="Not reported" />}
                      </div>
                      {player.reason !== undefined ? (
                        <p className="sm-small sm-muted">{player.reason}</p>
                      ) : (
                        <p className="sm-small sm-faint">{player.reportedAt !== undefined ? `Reported ${formatClock(player.reportedAt)}` : 'Not yet reported.'}</p>
                      )}
                    </div>
                  )
                })}
              </div>
            )}
          </Section>
        </>
      )}
    </>
  )
}
