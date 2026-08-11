/**
 * Bounded exponential backoff with "equal jitter" (base/2 + random(0,
 * base/2), the AWS Architecture Blog's "Exponential Backoff and Jitter"
 * algorithm) for stream reconnection.
 *
 * UNMEASURED SHOWMESH HYPOTHESIS: baseMs, factor, and capMs below are
 * engineering guesses about a reasonable reconnect cadence against a
 * coordinator on a show LAN, not values derived from any observed
 * coordinator or network behavior. Nothing in this project has measured
 * how quickly a coordinator restart actually becomes reachable again, or
 * what reconnect pressure is acceptable. RES-009 (failure-mode testing)
 * is where that would happen. The choice of "equal jitter" as an
 * algorithm (as opposed to full or no jitter) is an ordinary engineering
 * choice to avoid a synchronized-thundering-herd pattern and a
 * near-zero first retry, not itself a numeric hypothesis.
 */
export interface BackoffConfig {
  readonly baseMs: number
  readonly factor: number
  readonly capMs: number
}

export const DEFAULT_BACKOFF: BackoffConfig = {
  baseMs: 500,
  factor: 2,
  capMs: 30_000,
}

/**
 * `attempt` is 1-based (the first retry after a failure is attempt 1).
 */
export function computeBackoffMs(attempt: number, config: BackoffConfig = DEFAULT_BACKOFF): number {
  const exponent = Math.max(0, attempt - 1)
  const capped = Math.min(config.capMs, config.baseMs * config.factor ** exponent)
  const half = capped / 2
  return Math.round(half + Math.random() * half)
}
