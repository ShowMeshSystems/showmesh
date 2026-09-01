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

/** Date and clock time: "26 Aug 14:02". For history events spanning more than one day. */
export function formatDateClock(iso: string | null): string | null {
  const ms = parseIsoMs(iso)
  if (ms === null) return null
  const date = new Date(ms)
  const day = date.toLocaleDateString([], { day: 'numeric', month: 'short' })
  const clock = date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', hour12: false })
  return `${day} ${clock}`
}

function pad2(n: number): string {
  return String(n).padStart(2, '0')
}

/** How many distinct frame numbers an fps names, 0-based. 29.97 is counted (and rejected) as 30 non-drop frames, matching audio.settings' own ltcFrameRate vocabulary. */
function frameCeiling(fps: number): number {
  return fps === 29.97 ? 30 : Math.round(fps)
}

/**
 * `hh:mm:ss` at a known fps, `.ff` appended only when the frame remainder is
 * nonzero. `fps === null` drops the frame field entirely - the caller's own
 * fallback for an unread frame rate.
 */
export function millisToTimecode(ms: number, fps: number | null): string {
  const totalMs = Math.max(0, Math.round(ms))
  const totalSeconds = Math.floor(totalMs / 1000)
  const hh = Math.floor(totalSeconds / 3600)
  const mm = Math.floor((totalSeconds % 3600) / 60)
  const ss = totalSeconds % 60
  const base = `${pad2(hh)}:${pad2(mm)}:${pad2(ss)}`
  if (fps === null || fps <= 0) return base
  const remainderMs = totalMs - totalSeconds * 1000
  const frames = Math.min(Math.round((remainderMs / 1000) * fps), frameCeiling(fps) - 1)
  return frames === 0 ? base : `${base}.${pad2(frames)}`
}

/**
 * Parses `hh:mm:ss`, `hh:mm:ss.ff`, `mm:ss`, `mm:ss.ff`, or a bare integer of
 * milliseconds (paste-friendly). Returns null for anything malformed, an
 * out-of-range minute/second field, or a frame count at or beyond `fps`
 * (a `.ff` suffix is itself refused when `fps` is null - there is no rate to
 * interpret it against). Rounds to a whole millisecond.
 */
export function timecodeToMillis(text: string, fps: number | null): number | null {
  const trimmed = text.trim()
  if (trimmed === '') return null
  if (/^\d+$/.test(trimmed)) return Math.round(Number(trimmed))

  const [timePart, framePart, extra] = trimmed.split('.')
  if (extra !== undefined || timePart === undefined) return null

  const segments = timePart.split(':')
  if (segments.length !== 2 && segments.length !== 3) return null
  if (!segments.every((segment) => /^\d{1,2}$/.test(segment))) return null

  const [hh, mm, ss] =
    segments.length === 3 ? [Number(segments[0]), Number(segments[1]), Number(segments[2])] : [0, Number(segments[0]), Number(segments[1])]
  if (mm >= 60 || ss >= 60) return null

  let frames = 0
  if (framePart !== undefined) {
    if (!/^\d{1,2}$/.test(framePart)) return null
    if (fps === null || fps <= 0) return null
    frames = Number(framePart)
    if (frames >= frameCeiling(fps)) return null
  }

  const wholeMs = (hh * 3600 + mm * 60 + ss) * 1000
  const frameMs = fps === null || fps <= 0 ? 0 : (frames / fps) * 1000
  return Math.round(wholeMs + frameMs)
}
