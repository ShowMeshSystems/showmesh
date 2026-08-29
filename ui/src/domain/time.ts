/**
 * Ages are measured against the coordinator's clock, never the browser's,
 * and keep advancing between responses so a disconnected page does not
 * freeze its ages at the last thing it heard.
 */
export const CLOCK_SKEW_WARNING_THRESHOLD_MS = 5_000

export function parseIsoMs(iso: string | null): number | null {
  if (iso === null) return null
  const ms = Date.parse(iso)
  return Number.isNaN(ms) ? null : ms
}

export function ageMs(atIso: string | null, referenceIso: string | null): number | null {
  const at = parseIsoMs(atIso)
  const reference = parseIsoMs(referenceIso)
  if (at === null || reference === null) return null
  return reference - at
}

/** "26 m", "4 h 39 m", "0.4 s": the mocks' own duration voice. */
export function formatDuration(ms: number): string {
  if (ms < 0) return 'in the future'
  const seconds = ms / 1000
  if (seconds < 10) return `${seconds.toFixed(1)} s`
  if (seconds < 60) return `${Math.floor(seconds)} s`
  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes} m`
  const hours = Math.floor(minutes / 60)
  const rest = minutes % 60
  if (hours < 24) return rest === 0 ? `${hours} h` : `${hours} h ${rest} m`
  return `${Math.floor(hours / 24)} d`
}

export function formatAge(ms: number): string {
  if (ms < 0) return 'in the future (check clock skew)'
  return `${formatDuration(ms)} ago`
}

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

/** Clock time only: "20:41". Absence is stated by the caller, never here. */
export function formatClock(iso: string | null): string | null {
  const ms = parseIsoMs(iso)
  if (ms === null) return null
  // 24-hour: the operator reads this at night, and 20:41 is unambiguous.
  return new Date(ms).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
}
