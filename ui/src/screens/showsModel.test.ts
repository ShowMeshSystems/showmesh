import { describe, expect, it } from 'vitest'
import { audioDerivedSafetyClass, formatBytes, fppDerivedSafetyClass, hashLabel, macroUsagesForAction, resolumeDerivedSafetyClass } from './showsModel'

describe('the safety-class derivation tables', () => {
  it('derives fpp: only stopPlaylist and stopPlaylistGracefully are stop', () => {
    expect(fppDerivedSafetyClass('stopPlaylist')).toBe('stop')
    expect(fppDerivedSafetyClass('stopPlaylistGracefully')).toBe('stop')
    expect(fppDerivedSafetyClass('startPlaylist')).toBe('none')
    expect(fppDerivedSafetyClass('pausePlaylist')).toBe('none')
    expect(fppDerivedSafetyClass('resumePlaylist')).toBe('none')
    expect(fppDerivedSafetyClass('nextPlaylistItem')).toBe('none')
    expect(fppDerivedSafetyClass('prevPlaylistItem')).toBe('none')
    expect(fppDerivedSafetyClass('setVolume')).toBe('none')
  })

  it('derives resolume: only blackout and clearLayer are blackout', () => {
    expect(resolumeDerivedSafetyClass('blackout')).toBe('blackout')
    expect(resolumeDerivedSafetyClass('clearLayer')).toBe('blackout')
    expect(resolumeDerivedSafetyClass('launchClip')).toBe('none')
    expect(resolumeDerivedSafetyClass('launchColumn')).toBe('none')
    expect(resolumeDerivedSafetyClass('selectDeck')).toBe('none')
    expect(resolumeDerivedSafetyClass('setLayerBypass')).toBe('none')
    expect(resolumeDerivedSafetyClass('setLayerMaster')).toBe('none')
  })

  it('derives audio: session.stop and session.clear are stop, output.mute is blackout, everything else is none', () => {
    expect(audioDerivedSafetyClass('audio.session.stop')).toBe('stop')
    expect(audioDerivedSafetyClass('audio.session.clear')).toBe('stop')
    expect(audioDerivedSafetyClass('audio.output.mute')).toBe('blackout')
    expect(audioDerivedSafetyClass('audio.gain.set')).toBe('none')
    expect(audioDerivedSafetyClass('audio.gain.fade')).toBe('none')
    expect(audioDerivedSafetyClass('audio.session.apply')).toBe('none')
    expect(audioDerivedSafetyClass('audio.session.prepare')).toBe('none')
    expect(audioDerivedSafetyClass('audio.session.start')).toBe('none')
    expect(audioDerivedSafetyClass('audio.session.pause')).toBe('none')
    expect(audioDerivedSafetyClass('audio.session.resume')).toBe('none')
    expect(audioDerivedSafetyClass('audio.session.seek')).toBe('none')
    expect(audioDerivedSafetyClass('audio.session.advance')).toBe('none')
    expect(audioDerivedSafetyClass('audio.output.unmute')).toBe('none')
  })
})

describe('macroUsagesForAction', () => {
  it('reports the macros and 1-based step numbers that reference an action', () => {
    const macros = [
      {
        payload: {
          label: 'Weather Hold',
          steps: [
            { id: 's1', action: 'other-action' } as never,
            { id: 's2', action: 'garage-speakers-mute' } as never,
            { id: 's3', action: 'other-action' } as never,
            { id: 's4', action: 'garage-speakers-mute' } as never,
          ],
        },
      },
      { payload: { label: 'Not Involved', steps: [{ id: 's1', action: 'other-action' } as never] } },
    ]
    expect(macroUsagesForAction(macros, 'garage-speakers-mute')).toEqual([{ label: 'Weather Hold', stepNumbers: [2, 4] }])
  })

  it('returns nothing for an action no macro references', () => {
    const macros = [{ payload: { label: 'Weather Hold', steps: [{ id: 's1', action: 'other-action' } as never] } }]
    expect(macroUsagesForAction(macros, 'unused-action')).toEqual([])
  })
})

describe('formatBytes', () => {
  it('reports a small count in bytes and larger ones in decimal units', () => {
    expect(formatBytes(0)).toBe('0 B')
    expect(formatBytes(999)).toBe('999 B')
    expect(formatBytes(1000)).toBe('1.0 kB')
    expect(formatBytes(30_100_000)).toBe('30.1 MB')
    expect(formatBytes(1_800_000_000)).toBe('1.8 GB')
  })
})

describe('hashLabel', () => {
  it('shortens a hash and strips a sha256 prefix', () => {
    expect(hashLabel('sha256:b1e7c0a7b6d5e4f3a2b1c0d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3c2d1e035')).toBe('b1e7…35')
    expect(hashLabel('abcd')).toBe('abcd')
  })
})
