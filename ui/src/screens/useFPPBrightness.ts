import { useEffect, useState } from 'react'
import { getFPPBrightness, type FPPBrightnessResponse } from '../api'
import { describeApiError } from '../domain/session'

const BRIGHTNESS_POLL_MS = 5000

export type BrightnessRead = { doc: FPPBrightnessResponse | null; readAt: string | null; error: string | null }

/** Polls GET /fpp/brightness. A failed read keeps the last document and says when it was read. */
export function useFPPBrightness(): [BrightnessRead, (doc: FPPBrightnessResponse) => void] {
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
