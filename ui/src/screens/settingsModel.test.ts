import { describe, expect, it } from 'vitest'
import { audioNodeVerdict, hasAudioCapability, liveCycle } from './settingsModel'
import type { Node, NightSessionState } from '../api'

describe('audioNodeVerdict', () => {
  const base = { programRoute: 'hw:CARD=USB,DEV=0', programChannels: [1, 2], ltcRoute: '', ltcChannel: '' }

  it('accepts a program-only declaration with distinct channels', () => {
    expect(audioNodeVerdict(base)).toEqual({ ok: true })
  })

  it('accepts a matching LTC route on a discrete channel', () => {
    expect(audioNodeVerdict({ ...base, ltcRoute: base.programRoute, ltcChannel: '3' })).toEqual({ ok: true })
  })

  it('refuses a channel index reused within programChannels', () => {
    const verdict = audioNodeVerdict({ ...base, programChannels: [1, 1] })
    expect(verdict.ok).toBe(false)
  })

  it('refuses an LTC channel already claimed by programChannels', () => {
    const verdict = audioNodeVerdict({ ...base, ltcRoute: base.programRoute, ltcChannel: '2' })
    expect(verdict.ok).toBe(false)
  })

  it('refuses an LTC route that differs from the program route, matching the coordinator code rather than the mismatch problem type text', () => {
    const verdict = audioNodeVerdict({ ...base, ltcRoute: 'hw:CARD=PCH,DEV=0', ltcChannel: '3' })
    expect(verdict.ok).toBe(false)
  })

  it('refuses an LTC route given without a channel', () => {
    const verdict = audioNodeVerdict({ ...base, ltcRoute: base.programRoute, ltcChannel: '' })
    expect(verdict.ok).toBe(false)
  })
})

describe('hasAudioCapability', () => {
  function node(capabilities: Node['capabilities']): Node {
    return { capabilities } as unknown as Node
  }

  it('is true for audio.output.local', () => {
    expect(hasAudioCapability(node([{ id: 'audio.output.local', version: 1 }]))).toBe(true)
  })

  it('is true for audio.output.ltc', () => {
    expect(hasAudioCapability(node([{ id: 'audio.output.ltc', version: 1 }]))).toBe(true)
  })

  it('is false with neither', () => {
    expect(hasAudioCapability(node([{ id: 'transport.ndi.send', version: 1 }]))).toBe(false)
  })
})

describe('liveCycle', () => {
  it('is null when there is no session', () => {
    expect(liveCycle(null)).toBeNull()
  })

  it('is null when the session reports a non-live state', () => {
    expect(liveCycle({ state: 'resting-intershow', cycle: 2 } as unknown as NightSessionState)).toBeNull()
  })

  it('names the cycle when the session reports it live', () => {
    expect(liveCycle({ state: 'live', cycle: 3 } as unknown as NightSessionState)).toEqual({ cycle: 3 })
  })
})
