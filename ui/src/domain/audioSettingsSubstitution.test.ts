import { describe, expect, it } from 'vitest'
import type { Node } from '../api'
import { audioSettingFieldLabel, audioSettingName, audioSettingSubstitution } from './audioSettingsSubstitution'

const ev = (signal: string, value: string, state = 'current') => ({ signal, value, state }) as unknown as Node['audio'][number]

describe('audioSettingSubstitution', () => {
  it('maps every node field name to the form label', () => {
    expect(audioSettingName('DefaultFadeDurationMs')).toBe('Default fade duration')
    expect(audioSettingFieldLabel('fadeDuration')).toBe('Default fade duration (ms)')
    expect(audioSettingName('SomethingNew')).toBe('an audio setting')
  })

  it('is null for accepted and for a state that is no longer current', () => {
    expect(audioSettingSubstitution({ audio: [ev('node.audio.settings.state', 'accepted')] })).toBeNull()
    expect(audioSettingSubstitution({ audio: [ev('node.audio.settings.state', 'substituted', 'stale')] })).toBeNull()
  })

  it('names each refused field once with the node reason', () => {
    const result = audioSettingSubstitution({
      audio: [
        ev('node.audio.settings.state', 'substituted'),
        ev('node.audio.settings.substituted_fields', 'DuckTargetGain; DuckTargetGain'),
        ev('node.audio.settings.reason', 'too loud'),
      ],
    })
    expect(result).toEqual({ fields: ['Duck target gain'], reason: 'too loud' })
  })
})
