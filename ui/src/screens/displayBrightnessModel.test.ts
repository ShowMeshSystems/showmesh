import { describe, expect, it } from 'vitest'
import { ceilingSentence, parseFadeSeconds, playerStateStatus, reportedCeiling } from './displayBrightnessModel'

describe('displayBrightnessModel', () => {
  it('reads the fade field as whole seconds in range', () => {
    expect(parseFadeSeconds('')).toBe(0)
    expect(parseFadeSeconds(' 30 ')).toBe(30)
    expect(parseFadeSeconds('86400')).toBe(86400)
    expect(parseFadeSeconds('86401')).toBeNull()
    expect(parseFadeSeconds('1.5')).toBeNull()
    expect(parseFadeSeconds('-1')).toBeNull()
  })

  it('shows where a fade on a player is going, and nothing invented for a player that reported nothing', () => {
    expect(reportedCeiling({ instanceId: 'a', state: 'catchingUp', ceiling: 40, ceilingTarget: 60 })).toBe('40% → 60%')
    expect(reportedCeiling({ instanceId: 'a', state: 'following', ceiling: 60, ceilingTarget: 60 })).toBe('60%')
    expect(reportedCeiling({ instanceId: 'a', state: 'pluginTooOld', ceiling: 60 })).toBe('60%')
    expect(reportedCeiling({ instanceId: 'a', state: 'notReporting' })).toBeNull()
  })

  it('gives every state a word, and an unknown state reads Unknown', () => {
    expect(playerStateStatus('following')).toEqual({ tone: 'good', word: 'Following' })
    expect(playerStateStatus('catchingUp').word).toBe('Catching up')
    expect(playerStateStatus('something-new')).toEqual({ tone: 'unknown', word: 'Unknown' })
  })

  it('names an FPP the ceiling was taken from', () => {
    const doc = { serverTime: 'x', players: [], ceiling: { targetPercent: 30, fadeEndsAt: null, changedAt: '2026-10-17T20:00:00Z', changedBy: { kind: 'fpp' as const, id: 'yard-remote' } } }
    expect(ceilingSentence(doc)).toMatch(/^30%, taken from a change made on yard-remote at /)
    expect(ceilingSentence({ ...doc, ceiling: null })).toBeNull()
  })

  it('mentions a fade only while it has not ended', () => {
    const ceiling = { targetPercent: 30, fadeEndsAt: '2026-10-17T20:00:30Z', changedAt: '2026-10-17T20:00:00Z', changedBy: { kind: 'operator' as const, id: 'p1' } }
    expect(ceilingSentence({ serverTime: '2026-10-17T20:00:10Z', players: [], ceiling })).toMatch(/fading until/)
    expect(ceilingSentence({ serverTime: '2026-10-17T20:01:00Z', players: [], ceiling })).not.toMatch(/fading/)
  })
})
