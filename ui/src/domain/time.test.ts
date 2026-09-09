import { describe, expect, it } from 'vitest'
import { millisToTimecode, timecodeToMillis } from './time'

describe('millisToTimecode', () => {
  it('formats hh:mm:ss and drops .ff when the frame remainder is zero', () => {
    expect(millisToTimecode(0, 30)).toBe('00:00:00')
    expect(millisToTimecode(3_723_000, 30)).toBe('01:02:03')
  })

  it('appends .ff for a nonzero frame remainder', () => {
    expect(millisToTimecode(100, 30)).toBe('00:00:00.03')
    expect(millisToTimecode(500, 30)).toBe('00:00:00.15')
  })

  it('drops the frame field entirely when fps is null', () => {
    expect(millisToTimecode(100, null)).toBe('00:00:00')
  })

  it('treats 29.97 as a 30-frame wheel', () => {
    expect(millisToTimecode(966, 29.97)).toBe('00:00:00.29')
  })

  it('rounds to a whole millisecond before formatting', () => {
    expect(millisToTimecode(999.6, 30)).toBe('00:00:01')
  })
})

describe('timecodeToMillis', () => {
  it('accepts hh:mm:ss', () => {
    expect(timecodeToMillis('01:02:03', 30)).toBe(3_723_000)
  })

  it('accepts hh:mm:ss.ff', () => {
    expect(timecodeToMillis('00:00:00.03', 30)).toBe(100)
  })

  it('accepts mm:ss', () => {
    expect(timecodeToMillis('02:03', 30)).toBe(123_000)
  })

  it('accepts mm:ss.ff', () => {
    expect(timecodeToMillis('00:00.15', 30)).toBe(500)
  })

  it('accepts a bare integer of milliseconds', () => {
    expect(timecodeToMillis('1500', 30)).toBe(1500)
    expect(timecodeToMillis('1500', null)).toBe(1500)
  })

  it('rejects a frame count at or beyond fps', () => {
    expect(timecodeToMillis('00:00:00.30', 30)).toBeNull()
    expect(timecodeToMillis('00:00:00.29', 30)).not.toBeNull()
  })

  it('treats 29.97 as frames 0..29', () => {
    expect(timecodeToMillis('00:00:00.29', 29.97)).not.toBeNull()
    expect(timecodeToMillis('00:00:00.30', 29.97)).toBeNull()
  })

  it('rejects a .ff suffix when fps is unknown', () => {
    expect(timecodeToMillis('00:00:00.03', null)).toBeNull()
  })

  it('rejects an out-of-range minute or second field', () => {
    expect(timecodeToMillis('00:60:00', 30)).toBeNull()
    expect(timecodeToMillis('00:00:60', 30)).toBeNull()
  })

  it('rejects malformed text', () => {
    expect(timecodeToMillis('not a time', 30)).toBeNull()
    expect(timecodeToMillis('', 30)).toBeNull()
    expect(timecodeToMillis('1:2:3:4', 30)).toBeNull()
  })

  it('rounds to a whole millisecond', () => {
    // 1 frame at 29.97 fps is 1000/29.97 = 33.3667ms, which rounds to 33ms.
    expect(timecodeToMillis('00:00:00.01', 29.97)).toBe(33)
  })
})
