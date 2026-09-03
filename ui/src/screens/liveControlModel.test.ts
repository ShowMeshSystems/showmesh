import { describe, expect, it } from 'vitest'
import type { ObservationEntry } from '../api'
import {
  audioSessionSummaries,
  deriveAudioSessionRevision,
  exactAudioSessionRevisionValue,
  parseExactRevisionInput,
} from './liveControlModel'

function observation(overrides: Partial<ObservationEntry> = {}): ObservationEntry {
  return {
    resource: { kind: 'audio_session', id: 'cue-activation:show' },
    signal: 'audio_session.desired_revision',
    value: 85,
    unit: null,
    state: 'current',
    reason: null,
    observedAt: '2026-09-01T21:00:00Z',
    collectedAt: '2026-09-01T21:00:00Z',
    source: 'agent',
    quality: 'direct',
    validForSeconds: null,
    ...overrides,
  } as unknown as ObservationEntry
}

describe('exactAudioSessionRevisionValue', () => {
  it('carries a decimal string beyond Number.MAX_SAFE_INTEGER through as an exact bigint', () => {
    expect(exactAudioSessionRevisionValue('1788358834726046720')).toBe(1788358834726046720n)
  })

  it('carries a small JS number through as a bigint', () => {
    expect(exactAudioSessionRevisionValue(85)).toBe(85n)
  })

  it('rejects anything that is not a plain integer', () => {
    expect(exactAudioSessionRevisionValue('not-a-number')).toBeNull()
    expect(exactAudioSessionRevisionValue(1.5)).toBeNull()
    expect(exactAudioSessionRevisionValue(null)).toBeNull()
    expect(exactAudioSessionRevisionValue(true)).toBeNull()
  })
})

describe('deriveAudioSessionRevision', () => {
  it('sends 1 for a session never observed', () => {
    expect(deriveAudioSessionRevision([], 'bg-holiday-01')).toEqual({ next: 1n, observed: null })
  })

  it('derives observed + 1 for a small-counter session (rehearsal-background style)', () => {
    const observations = [observation({ resource: { kind: 'audio_session', id: 'rehearsal-background' }, value: 85 })]
    expect(deriveAudioSessionRevision(observations, 'rehearsal-background')).toEqual({ next: 86n, observed: 85n })
  })

  /**
   * The defect this guards: a cue-activation session's desired_revision is
   * UnixNano-scale (pkg/cueactivation.AudioSessionRevision) and arrives
   * here as an exact decimal string once bigint.ts's parser has done its
   * job, well beyond Number.MAX_SAFE_INTEGER. `observed + 1` computed in
   * JS `number` arithmetic on a value this size is a silent no-op
   * (`observed + 1 === observed`), which is exactly what let every
   * transport command against a cue-activation session get refused as
   * stale_revision. This asserts the dispatched revision is strictly
   * greater than the observed one, in exact BigInt arithmetic.
   */
  it('dispatches a strictly greater exact revision for a session whose observed desired revision exceeds Number.MAX_SAFE_INTEGER', () => {
    const observedDecimalString = '1788358834726046720'
    const observations = [
      observation({
        resource: { kind: 'audio_session', id: 'cue-activation:show' },
        value: observedDecimalString,
      }),
    ]

    const { next, observed } = deriveAudioSessionRevision(observations, 'cue-activation:show')

    expect(observed).toBe(BigInt(observedDecimalString))
    expect(next).toBe(BigInt(observedDecimalString) + 1n)
    expect(next > (observed as bigint)).toBe(true)
    // The bug this test would have caught: `Number(observedDecimalString) + 1`
    // rounds to the same double as the observed value itself.
    expect(Number(observedDecimalString) + 1).toBe(Number(observedDecimalString))
  })

  it('ignores an observation for a different session id', () => {
    const observations = [observation({ resource: { kind: 'audio_session', id: 'other-session' }, value: 5 })]
    expect(deriveAudioSessionRevision(observations, 'bg-holiday-01')).toEqual({ next: 1n, observed: null })
  })
})

describe('parseExactRevisionInput', () => {
  it('parses a full int64 as an exact bigint, past what a number spinner could hold precisely', () => {
    expect(parseExactRevisionInput('1788358834726046721')).toBe(1788358834726046721n)
  })

  it('rejects a non-digit string', () => {
    expect(parseExactRevisionInput('not-a-number')).toBeNull()
    expect(parseExactRevisionInput('')).toBeNull()
    expect(parseExactRevisionInput('12.5')).toBeNull()
  })

  it('rejects a negative integer: api/openapi.yaml declares revision minimum 0', () => {
    expect(parseExactRevisionInput('-3')).toBeNull()
  })
})

describe('audioSessionSummaries', () => {
  const actions: { id: string; label: string; audioSessionId: string }[] = []

  /**
   * The defect this guards: the block only ever showed a session's state
   * after the operator had already picked or typed its id. This asserts
   * the summary is derived from the known-session list alone, with no
   * session selected.
   */
  it('reports a known session state without that session having been selected', () => {
    const observations = [
      observation({
        resource: { kind: 'audio_session', id: 'bg-holiday-01' },
        signal: 'audio_session.state',
        value: 'playing',
        state: 'current',
      }),
    ]

    const summaries = audioSessionSummaries(observations, actions, '2026-09-01T21:00:05Z')

    expect(summaries).toHaveLength(1)
    expect(summaries[0]).toMatchObject({ sessionId: 'bg-holiday-01', tone: 'good', stateLabel: 'playing' })
  })

  it('reports a known session as unobserved, never as a fabricated "stopped", when only some other signal has reported', () => {
    const observations = [
      observation({
        resource: { kind: 'audio_session', id: 'bg-holiday-01' },
        signal: 'audio_session.gain.effective',
        value: -3,
        state: 'current',
      }),
    ]

    const summaries = audioSessionSummaries(observations, actions, '2026-09-01T21:00:05Z')

    expect(summaries).toHaveLength(1)
    expect(summaries[0]?.stateLabel).toBe('Unobserved')
    expect(summaries[0]?.tone).toBe('unknown')
  })

  it('qualifies a stale state observation as stale rather than presenting it as current fact', () => {
    const observations = [
      observation({
        resource: { kind: 'audio_session', id: 'night-bg' },
        signal: 'audio_session.state',
        value: 'playing',
        state: 'stale',
        observedAt: '2026-09-01T09:00:00Z',
      }),
    ]

    const summaries = audioSessionSummaries(observations, actions, '2026-09-01T21:00:00Z')

    expect(summaries[0]?.tone).toBe('warn')
    expect(summaries[0]?.stateLabel).toContain('playing')
    expect(summaries[0]?.stateLabel).toContain('stale')
  })

  it('reports position in mm:ss when a position observation is present', () => {
    const observations = [
      observation({ resource: { kind: 'audio_session', id: 'bg-holiday-01' }, signal: 'audio_session.state', value: 'playing' }),
      observation({
        resource: { kind: 'audio_session', id: 'bg-holiday-01' },
        signal: 'audio_session.position_ms',
        value: 90000,
      }),
    ]

    const summaries = audioSessionSummaries(observations, actions, '2026-09-01T21:00:00Z')

    expect(summaries[0]?.positionLabel).toBe('1:30')
  })

  it('leaves position null when no position observation has been reported', () => {
    const observations = [
      observation({ resource: { kind: 'audio_session', id: 'bg-holiday-01' }, signal: 'audio_session.state', value: 'playing' }),
    ]

    const summaries = audioSessionSummaries(observations, actions, '2026-09-01T21:00:00Z')

    expect(summaries[0]?.positionLabel).toBeNull()
  })
})
