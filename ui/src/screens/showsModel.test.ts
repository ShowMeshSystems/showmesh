import { describe, expect, it } from 'vitest'
import {
  audioDerivedSafetyClass,
  cueActivationSummary,
  cueAssetMissing,
  cueRows,
  formatBytes,
  fppDerivedSafetyClass,
  hashLabel,
  macroUsagesForAction,
  resolumeDerivedSafetyClass,
  type CueActivationDraft,
} from './showsModel'

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

function asset(sequence: string, current = true) {
  return { sequence, current } as never
}

describe('cueAssetMissing', () => {
  it('is false for a cue with no audio output', () => {
    expect(cueAssetMissing({}, [asset('house-preshow-loop')])).toBe(false)
  })

  it('is false when the audio asset is among the current assets', () => {
    expect(cueAssetMissing({ audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } } as never, [asset('house-preshow-loop')])).toBe(false)
  })

  it('is true when the audio asset is not among the current assets', () => {
    expect(cueAssetMissing({ audio: { asset: 'weather-delay', startOffsetMillis: 0 } } as never, [asset('house-preshow-loop')])).toBe(true)
  })

  it('is true when the only matching asset is superseded, not current', () => {
    expect(cueAssetMissing({ audio: { asset: 'weather-delay', startOffsetMillis: 0 } } as never, [asset('weather-delay', false)])).toBe(true)
  })
})

describe('cueRows asset-missing signal', () => {
  it('flags an announcement cue whose audio asset is not in the show’s current assets', () => {
    const cues = [
      {
        id: 'cue-1',
        revision: 1,
        updatedAt: '2026-08-30T18:22:00Z',
        payload: {
          name: 'Weather Delay Notice',
          outputs: { audio: { asset: 'weather-delay', startOffsetMillis: 0 }, announcement: { policy: 'duck', duckGainDb: -12, fadeMillis: 400 } },
        },
      } as never,
    ]
    const rows = cueRows(cues, [], [asset('house-preshow-loop')])
    expect(rows[0]?.assetMissing).toBe(true)
  })

  it('does not flag a cue whose audio asset is present', () => {
    const cues = [
      {
        id: 'cue-1',
        revision: 1,
        updatedAt: '2026-08-30T18:22:00Z',
        payload: { name: 'House Preshow Loop', outputs: { audio: { asset: 'house-preshow-loop', startOffsetMillis: 0 } } },
      } as never,
    ]
    const rows = cueRows(cues, [], [asset('house-preshow-loop')])
    expect(rows[0]?.assetMissing).toBe(false)
  })
})

describe('cueActivationSummary', () => {
  const empty: CueActivationDraft = { render: null, audio: null, ltc: null, announcement: null }

  it('asks for an output when nothing is picked yet', () => {
    expect(cueActivationSummary(empty)).toBe('Pick at least one output to see what this cue will do.')
  })

  it('narrates a single audio-plus-announcement cue matching the mock', () => {
    const draft: CueActivationDraft = {
      render: null,
      audio: { asset: 'thank-you.wav', startOffsetMillis: 0, target: '' },
      ltc: null,
      announcement: { policy: 'duck', duckGainDb: -18, fadeMillis: 400, target: '' },
    }
    expect(cueActivationSummary(draft)).toBe(
      'On activation this cue will play thank-you.wav, duck the background bed to -18 dB over 400 ms, and leave FPP untouched.',
    )
  })

  it('states a positive start offset and a target node as facts', () => {
    const draft: CueActivationDraft = {
      render: null,
      audio: { asset: 'carol-bells.wav', startOffsetMillis: 250, target: 'node-a' },
      ltc: null,
      announcement: null,
    }
    expect(cueActivationSummary(draft)).toBe('On activation this cue will play carol-bells.wav at +250 ms on node-a and leave FPP untouched.')
  })

  it('narrates render, audio, and ltc together without the "leave FPP untouched" fact', () => {
    const draft: CueActivationDraft = {
      render: { sequence: 'wizards-winter' },
      audio: { asset: 'wizards-winter.wav', startOffsetMillis: 0, target: '' },
      ltc: { startOffsetMillis: 0, target: '' },
      announcement: null,
    }
    expect(cueActivationSummary(draft)).toBe('On activation this cue will render sequence wizards-winter, play wizards-winter.wav, and emit LTC from 0 ms.')
  })

  it('states an unnamed sequence and an unselected asset literally, never inventing a value', () => {
    const draft: CueActivationDraft = { render: { sequence: '' }, audio: { asset: '', startOffsetMillis: 0, target: '' }, ltc: null, announcement: null }
    expect(cueActivationSummary(draft)).toBe('On activation this cue will render an unnamed sequence and play an unselected asset.')
  })
})
