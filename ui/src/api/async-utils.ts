import { AbortError } from './errors'

/** Resolves after `ms`, or rejects with AbortError if `signal` fires first. */
export function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal.aborted) {
      reject(new AbortError())
      return
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', onAbort)
      resolve()
    }, ms)
    const onAbort = (): void => {
      clearTimeout(timer)
      reject(new AbortError())
    }
    signal.addEventListener('abort', onAbort, { once: true })
  })
}

/**
 * Never resolves on its own; rejects with AbortError when `signal`
 * fires. Used to pause the reconnect loop in the `unauthorized` state
 * (spec section 5.6: "do not retry with backoff") without a timer —
 * the only way out is `submitToken`/`clearToken` aborting the signal.
 */
export function waitUntilAborted(signal: AbortSignal): Promise<never> {
  return new Promise((_resolve, reject) => {
    if (signal.aborted) {
      reject(new AbortError())
      return
    }
    signal.addEventListener('abort', () => reject(new AbortError()), { once: true })
  })
}
