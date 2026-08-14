import { useState } from 'react'
import { setFPPVolume } from '../api'
import { useFPPCommandCall } from '../app/useFPPCommand'
import { FPPCommandOutcome } from './FPPCommandOutcome'
import { ScopedButton } from './ScopedButton'

export interface FPPSetVolumeControlProps {
  instanceId: string
}

// Capture section 1.5: FPP itself does not validate "Volume Set" — it
// CLAMPS an out-of-range value (999 -> 100) and COERCES a non-numeric
// one to 0, both silently, both with a 200. This project's own standing
// rule (fppcommand_primitives.go's ValidateVolume, and this document's
// own instruction) is that ShowMesh validates instead of trusting FPP
// to reject a bad value — this control validates client-side for the
// same reason, one layer further out: the operator sees WHY a value is
// rejected, before any request leaves the browser, rather than a value
// being silently rewritten into something nobody asked for. It never
// clamps the input itself; a value outside 0-100, or a non-integer, is
// refused with a stated reason and nothing is sent.
export function FPPSetVolumeControl({ instanceId }: FPPSetVolumeControlProps) {
  const [raw, setRaw] = useState('')
  const [validationError, setValidationError] = useState<string | null>(null)
  const { state, run } = useFPPCommandCall()
  const submitting = state.kind === 'submitting'

  function parseVolume(): number | null {
    const trimmed = raw.trim()
    if (trimmed === '') {
      setValidationError('Enter a volume from 0 to 100.')
      return null
    }
    const n = Number(trimmed)
    if (!Number.isFinite(n) || !Number.isInteger(n)) {
      // FPP itself would silently coerce a non-numeric value to 0 rather
      // than reject it (capture section 1.5) — this control refuses
      // instead of sending it, so the operator sees why before anything
      // leaves the browser.
      setValidationError(`"${raw}" is not a whole number. Volume must be a whole number from 0 to 100.`)
      return null
    }
    if (n < 0 || n > 100) {
      // FPP itself would silently clamp an out-of-range value to the
      // nearest end of 0-100 rather than reject it (capture section 1.5)
      // — same reasoning as the non-integer case above.
      setValidationError(`${n} is outside the allowed range. Volume must be a whole number from 0 to 100.`)
      return null
    }
    setValidationError(null)
    return n
  }

  function handleClick(): void {
    if (submitting) return
    const volume = parseVolume()
    if (volume === null) return
    run(() => setFPPVolume(instanceId, volume))
  }

  return (
    <div className="fpp-command-control">
      <label>
        Volume (0-100){' '}
        <input
          type="number"
          min={0}
          max={100}
          step={1}
          value={raw}
          disabled={submitting}
          onChange={(e) => setRaw(e.target.value)}
        />
      </label>
      {validationError !== null && (
        <p role="alert" className="fpp-command-control__error">
          {validationError}
        </p>
      )}
      <ScopedButton requiredScope="fpp:command" busy={submitting} onClick={handleClick}>
        {submitting ? 'Setting…' : 'Set Volume'}
      </ScopedButton>
      {submitting && (
        <p className="text-muted" role="status">
          Waiting for the coordinator to confirm the volume actually changed…
        </p>
      )}
      {state.kind === 'result' && (
        <FPPCommandOutcome result={state.result} confirmedSummary="volume set" />
      )}
      {state.kind === 'error' && (
        <p role="alert" className="fpp-command-control__error">
          {state.message}
        </p>
      )}
    </div>
  )
}
