// Time and freshness helpers, shared by every view/component that renders
// an age. Every age in this seam is computed against the coordinator's
// `serverTime`, never the browser clock (spec section 5.3 / ADR-020
// decision 6) -- that is the whole point of shipping `serverTime` on every
// response: it makes operator clock skew visible instead of silently
// wrong. `formatAge` below takes the reference time as an explicit
// parameter for exactly this reason, so a caller cannot reach for
// `Date.now()` by accident.

/**
 * A ShowMesh hypothesis, not a measured value: the clock-skew magnitude
 * above which this UI bothers surfacing a warning to the operator. Chosen
 * to be well above ordinary NTP drift and well below "something is
 * actually wrong", with no bench evidence behind either bound.
 */
export const CLOCK_SKEW_WARNING_THRESHOLD_MS = 5_000

export function parseIsoMs(iso: string | null): number | null {
  if (iso === null) return null
  const ms = Date.parse(iso)
  return Number.isNaN(ms) ? null : ms
}

/**
 * Age of `atIso` relative to `referenceIso` (normally `model.serverTime`),
 * in milliseconds. Returns null when either timestamp is unknown -- this
 * must never be filled in with the browser clock; a null age is rendered
 * as "age unknown", never as zero or "just now".
 */
export function ageMs(atIso: string | null, referenceIso: string | null): number | null {
  const at = parseIsoMs(atIso)
  const reference = parseIsoMs(referenceIso)
  if (at === null || reference === null) return null
  return reference - at
}

/** Human-readable relative age, e.g. "3s ago", "12m ago", "4h ago", "6d ago". */
export function formatAge(ms: number): string {
  if (ms < 0) {
    // Clock skew or a reference time older than the observation; state it
    // plainly rather than showing a nonsensical negative duration.
    return 'in the future (check clock skew)'
  }
  const seconds = Math.floor(ms / 1000)
  if (seconds < 5) return 'just now'
  if (seconds < 60) return `${seconds}s ago`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.floor(hours / 24)
  return `${days}d ago`
}

/**
 * The coordinator's clock, advanced by real elapsed browser time since it
 * was last reported — the "effective now" evidence ages must be computed
 * against so they keep advancing between responses instead of freezing at
 * the last response's value (the defect this function exists to fix: an
 * evidence panel reading "observed just now" for over a minute after the
 * coordinator went away). The anchor point is still `serverTime`, never
 * the raw browser clock — see this file's header comment — so operator
 * clock skew stays visible in whatever `ageMs` computes from the result;
 * only the elapsed-time component is browser-clock-derived, exactly the
 * way `DataFreshnessNotice` already derives its own advancing age from
 * `snapshotReceivedAt`.
 *
 * `serverTimeReceivedAt` is the browser-clock instant (`Date.now()`) at
 * which `serverTime` was captured (domain.ts's
 * `Model.serverTimeReceivedAt`). When it is unknown — a caller that has
 * not been wired to it, or a model from before the first response — this
 * falls back to returning `serverTime` unchanged, i.e. today's behavior:
 * a fixed reference with no advancement.
 */
export function effectiveServerTimeIso(
  serverTime: string | null,
  serverTimeReceivedAt: number | null,
  nowMs: number,
): string | null {
  if (serverTime === null || serverTimeReceivedAt === null) return serverTime
  const serverMs = parseIsoMs(serverTime)
  if (serverMs === null) return serverTime
  return new Date(serverMs + (nowMs - serverTimeReceivedAt)).toISOString()
}

/** Human-readable absolute timestamp for detail views and tooltips. */
export function formatAbsolute(iso: string | null): string {
  const ms = parseIsoMs(iso)
  if (ms === null) return 'unknown'
  return new Date(ms).toLocaleString()
}
