import type { FPPBrightnessPlayer, FPPBrightnessResponse } from '../api'
import type { Tone } from '../kit'
import { formatClock } from '../domain/time'

/** Each FPP's state against the display ceiling, as a tone and a state word. */
export function playerStateStatus(state: string): { tone: Tone; word: string } {
  switch (state) {
    case 'following':
      return { tone: 'good', word: 'Following' }
    case 'catchingUp':
      return { tone: 'pending', word: 'Catching up' }
    case 'notReporting':
      return { tone: 'warn', word: 'Not reporting' }
    case 'pluginTooOld':
      return { tone: 'warn', word: 'Plugin too old' }
    default:
      return { tone: 'unknown', word: 'Unknown' }
  }
}

/** The ceiling an FPP reported, with where a fade on it is going: "40% → 60%". Null when it reported none. */
export function reportedCeiling(player: FPPBrightnessPlayer): string | null {
  if (player.ceiling === undefined) return null
  if (player.ceilingTarget !== undefined && player.ceilingTarget !== player.ceiling) return `${player.ceiling}% → ${player.ceilingTarget}%`
  return `${player.ceiling}%`
}

/** Who set the display ceiling and when, as one sentence. Null before a ceiling is set. */
export function ceilingSentence(doc: FPPBrightnessResponse): string | null {
  const c = doc.ceiling
  if (c === null || c === undefined) return null
  const at = formatClock(c.changedAt) ?? c.changedAt
  const by = c.changedBy.kind === 'fpp' ? `taken from a change made on ${c.changedBy.id}` : `set by ${c.changedBy.name ?? c.changedBy.id}`
  const fading = c.fadeEndsAt !== null && c.fadeEndsAt !== undefined && Date.parse(c.fadeEndsAt) > Date.parse(doc.serverTime)
  const fadeEnd = fading ? formatClock(c.fadeEndsAt ?? null) : null
  const fade = fadeEnd !== null ? `, fading until ${fadeEnd}` : ''
  return `${c.targetPercent}%, ${by} at ${at}${fade}.`
}

export const MAX_FADE_SECONDS = 86400

/** The fade field's value as whole seconds, or null when it is not a whole number in range. */
export function parseFadeSeconds(text: string): number | null {
  const trimmed = text.trim()
  if (trimmed === '') return 0
  if (!/^\d+$/.test(trimmed)) return null
  const n = Number(trimmed)
  return n <= MAX_FADE_SECONDS ? n : null
}
