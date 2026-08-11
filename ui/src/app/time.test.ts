import { describe, expect, it } from 'vitest'
import { effectiveServerTimeIso } from './time'

// effectiveServerTimeIso is the fix for the reported defect: an evidence
// panel that kept reading "observed just now" for over a minute after the
// coordinator went away, because ages were computed against a `serverTime`
// that had stopped updating. These tests pin the two properties the fix
// depends on: the result keeps advancing with real elapsed browser time
// (so an evidence age is never frozen), and the advancement is layered on
// top of `serverTime` rather than replacing it with the raw browser clock
// (so operator clock skew -- the entire reason `serverTime` exists on
// every response, per api/openapi.yaml -- stays visible).
describe('effectiveServerTimeIso', () => {
  const SERVER_TIME = '2026-08-11T12:00:00.000Z'
  const SERVER_TIME_MS = Date.parse(SERVER_TIME)

  it('advances by exactly the browser-clock time elapsed since serverTime was captured', () => {
    const receivedAt = 5_000_000 // arbitrary browser-clock epoch ms
    const ninetySecondsLater = receivedAt + 90_000

    const result = effectiveServerTimeIso(SERVER_TIME, receivedAt, ninetySecondsLater)

    expect(result).toBe(new Date(SERVER_TIME_MS + 90_000).toISOString())
  })

  it('returns serverTime unchanged when queried at the instant it was received (zero elapsed time)', () => {
    const receivedAt = 5_000_000
    expect(effectiveServerTimeIso(SERVER_TIME, receivedAt, receivedAt)).toBe(SERVER_TIME)
  })

  it('preserves clock skew rather than collapsing to the raw browser clock', () => {
    // The coordinator's clock reads 10s ahead of the browser's at the
    // moment serverTime was captured -- i.e. serverTime is 10s later than
    // the browser-clock reading that received it. This is exactly the
    // condition Model.clockSkewMs (store.ts's computeClockSkewMs) records.
    const receivedAt = 5_000_000
    const skewedServerTime = new Date(receivedAt + 10_000).toISOString()

    const nowMs = receivedAt + 20_000 // 20s of real elapsed browser time
    const result = effectiveServerTimeIso(skewedServerTime, receivedAt, nowMs)

    // Correct: the original 10s-ahead skew is carried forward, plus the
    // 20s that actually elapsed -- 30s ahead of receivedAt, not merely
    // "now" (which would silently discard the skew this function exists
    // to preserve).
    expect(result).toBe(new Date(receivedAt + 30_000).toISOString())
    expect(result).not.toBe(new Date(nowMs).toISOString())
  })

  it('falls back to serverTime unchanged when the browser-clock anchor is unknown', () => {
    // No serverTimeReceivedAt (e.g. a caller not wired to it, or a model
    // from before the first response) -- degrades to today's fixed
    // reference rather than fabricating an anchor.
    expect(effectiveServerTimeIso(SERVER_TIME, null, Date.now())).toBe(SERVER_TIME)
  })

  it('returns null when serverTime itself is null, regardless of the anchor', () => {
    expect(effectiveServerTimeIso(null, 5_000_000, 5_090_000)).toBeNull()
  })
})
