import type { Model } from '../api'
import { StopHoldBanner } from '../kit'
import { STOP_HOLD_BANNER_TEXT, stopHoldDetail, useCurrentNightSession } from './stopHold'

/** Shown on every screen while an emergency Stop holds the night. */
export function StopHoldShellBanner({ model, authenticated }: { model: Model; authenticated: boolean }) {
  const session = useCurrentNightSession(model)
  const hold = session?.stopHold
  if (!authenticated || hold === undefined) return null
  return <StopHoldBanner message={STOP_HOLD_BANNER_TEXT} detail={stopHoldDetail(hold)} />
}
