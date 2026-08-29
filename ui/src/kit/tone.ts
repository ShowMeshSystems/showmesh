/**
 * The four evidence tones. Every status surface renders a glyph and a state
 * word as well as a fill, so a tone is never the only signal.
 */
export type Tone = 'good' | 'warn' | 'bad' | 'unknown'

/** The glyph each tone carries when a caller has no more specific one. */
export const TONE_GLYPH: Record<Tone, string> = {
  good: '✓',
  warn: '⚠',
  bad: '✕',
  unknown: '?',
}
