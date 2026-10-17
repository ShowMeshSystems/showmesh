import { useEffect, useRef, useState } from 'react'
import { getFPPBrightness, putFPPBrightnessCeiling, type FPPBrightnessResponse } from '../api'
import { AbsenceLabel, Field, Input, RuledStrip, Section, Slider, StatusPair, Table, TableWrap } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { describeApiError, evaluateScope } from '../domain/session'
import { formatClock } from '../domain/time'
import { ceilingSentence, MAX_FADE_SECONDS, parseFadeSeconds, playerStateStatus, reportedCeiling } from './displayBrightnessModel'

const BRIGHTNESS_POLL_MS = 5000
const BRIGHTNESS_DEBOUNCE_MS = 250

type BrightnessRead = { doc: FPPBrightnessResponse | null; readAt: string | null; error: string | null }

/** Polls GET /fpp/brightness. A failed read keeps the last document and says when it was read. */
function useFPPBrightness(): [BrightnessRead, (doc: FPPBrightnessResponse) => void] {
  const [read, setRead] = useState<BrightnessRead>({ doc: null, readAt: null, error: null })
  useEffect(() => {
    let cancelled = false
    const load = () => {
      getFPPBrightness()
        .then((doc) => {
          if (!cancelled) setRead({ doc, readAt: doc.serverTime, error: null })
        })
        .catch((err: unknown) => {
          if (!cancelled) setRead((prev) => ({ ...prev, error: describeApiError(err) }))
        })
    }
    load()
    const interval = setInterval(load, BRIGHTNESS_POLL_MS)
    return () => {
      cancelled = true
      clearInterval(interval)
    }
  }, [])
  return [read, (doc) => setRead({ doc, readAt: doc.serverTime, error: null })]
}

/** The display's one brightness ceiling, every FPP follows it (Settings > Connections). */
export function DisplayBrightnessSection() {
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
    <Section id="st-display-brightness" title="Display brightness" detail="One brightness ceiling for the whole display. Every FPP player follows it.">
      {read.doc === null ? (
        read.error !== null ? (
          <RuledStrip absence="failed" label="Read failed" fact={read.error} />
        ) : (
          <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the display brightness." />
        )
      ) : (
        <>
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
          {read.error !== null && (
            <RuledStrip absence="stale" label="Stale" fact={read.error} detail={`Showing what was read at ${formatClock(read.readAt) ?? 'an earlier time'}.`} />
          )}
          {read.doc.players.length === 0 ? (
            <RuledStrip absence="empty" label="No players" fact="No FPP players are configured. Add one above." />
          ) : (
            <TableWrap label="Each FPP player's brightness, scrollable">
              <Table minWidth={520}>
                <thead>
                  <tr>
                    <th scope="col">Player</th>
                    <th scope="col">State</th>
                    <th scope="col">Ceiling</th>
                    <th scope="col">Detail</th>
                  </tr>
                </thead>
                <tbody>
                  {read.doc.players.map((player) => {
                    const status = playerStateStatus(player.state)
                    const reported = reportedCeiling(player)
                    return (
                      <tr key={player.instanceId}>
                        <td className="sm-data">{player.instanceId}</td>
                        <td><StatusPair tone={status.tone} label={status.word} /></td>
                        <td>{reported !== null ? <span className="sm-data">{reported}</span> : <AbsenceLabel absence="unavailable" label="Not reported" />}</td>
                        <td className="sm-small">{player.reason ?? ''}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </Table>
            </TableWrap>
          )}
        </>
      )}
    </Section>
  )
}

/** One FPP's place against the display ceiling, for the FPP drawer. The control lives in Settings > Connections. */
export function DisplayBrightnessPlayerReadout({ instanceId }: { instanceId: string }) {
  const [read] = useFPPBrightness()
  const player = read.doc?.players.find((p) => p.instanceId === instanceId)
  if (read.doc === null) {
    return read.error !== null ? <RuledStrip absence="failed" label="Read failed" fact={read.error} /> : <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the display brightness." />
  }
  if (player === undefined) {
    return <RuledStrip absence="unavailable" label="Not listed" fact="This player is not in the saved FPP players. Save the list to bring it into the display brightness." />
  }
  const status = playerStateStatus(player.state)
  const target = read.doc.ceiling?.targetPercent
  return (
    <>
      <p className="sm-body">
        <StatusPair tone={status.tone} label={status.word} />{' '}
        {target !== undefined ? `The display brightness is ${target}%. Change it in Settings, Connections, Display brightness.` : 'No display brightness has been set. Set it in Settings, Connections, Display brightness.'}
      </p>
      {player.reason !== undefined && <p className="sm-small sm-muted">{player.reason}</p>}
    </>
  )
}
