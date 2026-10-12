import { useCallback, useEffect, useState } from 'react'
import {
  ApiError,
  armEmergencyStopHardStop,
  emergencyStop,
  emergencyStopPowerDown,
  fireEmergencyStopHardStop,
  type EmergencyStopResult,
  type Model,
} from '../api'
import type { Tone } from '../kit'
import { describeApiError, evaluateScope } from '../domain/session'
import { effectiveServerTimeIso, parseIsoMs } from '../domain/time'

export type EmergencyLevel = 'stop' | 'stop-power-down' | 'hard-stop'

export const EMERGENCY_LEVEL_LABEL: Record<EmergencyLevel, string> = {
  stop: 'Stop',
  'stop-power-down': 'Stop and power down',
  'hard-stop': 'Hard stop',
}

/** The confirm wording for the two levels that ask first, shared by Live Control and the top bar. */
export const STOP_CONFIRM_TITLE = 'Stop every configured FPP instance now?'
export const STOP_CONFIRM_DETAIL = 'Your stop actions also run, such as stopping audio or blacking out Resolume.'
export const STOP_POWER_DOWN_CONFIRM_TITLE = 'Stop and power down now?'
export const STOP_POWER_DOWN_CONFIRM_DETAIL =
  'Every FPP instance stops, your stop actions run and the night powers down.'

/** [emergencyStop]/[emergencyStopPowerDown]/[fireEmergencyStopHardStop] share one result shape and one refusal path. */
export type EmergencyOutcomeState =
  | { kind: 'result'; level: EmergencyLevel; result: EmergencyStopResult }
  | { kind: 'error'; level: EmergencyLevel; message: string; hadHttpStatus: boolean }

export function instanceOutcomeTone(outcome: string): Tone {
  if (outcome === 'confirmed') return 'good'
  if (outcome === 'unconfirmed') return 'warn'
  if (outcome === 'unconfirmable') return 'unknown'
  return 'bad' // refused | failed
}

/** The three emergency stop levels and the hard-stop arm, for Live Control and the top bar alike. Each caller holds its own arm. */
export function useEmergencyStop(model: Model) {
  const gate = evaluateScope(model.session, model.sessionFetchFailed, 'show:emergencystop:invoke')
  const [outcome, setOutcome] = useState<EmergencyOutcomeState | null>(null)
  const [busy, setBusy] = useState<EmergencyLevel | false>(false)
  const [hardStopArm, setHardStopArm] = useState<{ armToken: string; expiresAt: string } | null>(null)
  const [hardStopArmBusy, setHardStopArmBusy] = useState(false)
  const [hardStopArmError, setHardStopArmError] = useState<string | null>(null)
  const [armTick, setArmTick] = useState(0)

  const run = useCallback((level: EmergencyLevel, call: () => Promise<EmergencyStopResult>) => {
    setBusy(level)
    call()
      .then((result) => setOutcome({ kind: 'result', level, result }))
      .catch((err: unknown) =>
        setOutcome({
          kind: 'error',
          level,
          message: describeApiError(err),
          hadHttpStatus: err instanceof ApiError && err.status !== undefined,
        }),
      )
      .finally(() => setBusy(false))
  }, [])

  const stop = useCallback(() => run('stop', emergencyStop), [run])
  const stopPowerDown = useCallback(() => run('stop-power-down', emergencyStopPowerDown), [run])

  const armHardStop = useCallback(() => {
    setHardStopArmBusy(true)
    setHardStopArmError(null)
    armEmergencyStopHardStop()
      .then((response) => setHardStopArm({ armToken: response.armToken, expiresAt: response.expiresAt }))
      .catch((err: unknown) => setHardStopArmError(describeApiError(err)))
      .finally(() => setHardStopArmBusy(false))
  }, [])

  /** Drops the token this browser holds. The coordinator has no disarm call; an unused token lapses at its own expiry. */
  const cancelArm = useCallback(() => {
    setHardStopArm(null)
    setHardStopArmError(null)
  }, [])

  // Ticks once a second only while a token is live, so the countdown and its
  // own expiry (armExpired below) stay current without polling the server.
  useEffect(() => {
    if (hardStopArm === null) return
    const id = setInterval(() => setArmTick((tick) => tick + 1), 1000)
    return () => clearInterval(id)
  }, [hardStopArm])
  // Falls back to the browser clock when no server time has been heard yet
  // (before the first snapshot): a missing countdown is worse than one
  // measured against an uncorrected clock for a ~10s window.
  const armNowMs = parseIsoMs(effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())) ?? Date.now()
  const armExpiresMs = hardStopArm === null ? null : parseIsoMs(hardStopArm.expiresAt)
  const armRemainingMs = armExpiresMs === null ? null : armExpiresMs - armNowMs
  const armExpired = armRemainingMs !== null && armRemainingMs <= 0
  void armTick // triggers the recompute above; the value itself is unused

  const fireHardStop = useCallback(() => {
    if (hardStopArm === null) return
    const armToken = hardStopArm.armToken
    setHardStopArm(null)
    run('hard-stop', () => fireEmergencyStopHardStop(armToken))
  }, [hardStopArm, run])

  return {
    gate,
    outcome,
    busy,
    hardStopArm,
    hardStopArmBusy,
    hardStopArmError,
    armRemainingMs,
    armExpired,
    stop,
    stopPowerDown,
    armHardStop,
    cancelArm,
    fireHardStop,
  }
}
