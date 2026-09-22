import { describe, expect, it } from 'vitest'
import type { Node, ObservationEntry } from '../api'
import { makeEvidence } from '../api/test-support/fixtures'
import { formatPositionTenths, nodePlaybackStatus, sourceRoleWord } from './nodePlaybackModel'

const NOW = '2026-08-11T12:00:00.000Z'
const NOW_MS = Date.parse(NOW)

function nodeWith(audio: ObservationEntry[]): Node {
  return { audio } as unknown as Node
}

function sessionEv(sessionId: string, signal: string, overrides: Partial<ObservationEntry> = {}): ObservationEntry {
  return { ...makeEvidence({ signal, ...overrides }), resource: { kind: 'audio_session', id: sessionId } }
}

function nodeEv(signal: string, overrides: Partial<ObservationEntry> = {}): ObservationEntry {
  return { ...makeEvidence({ signal, ...overrides }), resource: { kind: 'node', id: 'n1' } }
}

/** A minimal playing session, all fields fresh at NOW unless overridden. */
function playingSession(sessionId: string, positionMs: number, overrides: Partial<ObservationEntry> = {}): ObservationEntry[] {
  return [
    sessionEv(sessionId, 'audio_session.source_role', { value: 'background' }),
    sessionEv(sessionId, 'audio_session.playlist.item_id', { value: 'item-42' }),
    sessionEv(sessionId, 'audio_session.state', { value: 'playing' }),
    sessionEv(sessionId, 'audio_session.position_ms', { value: positionMs, observedAt: NOW, ...overrides }),
  ]
}

describe('formatPositionTenths', () => {
  it('renders m:ss.t', () => {
    expect(formatPositionTenths(75_400)).toBe('1:15.4')
  })
  it('pads seconds under 10', () => {
    expect(formatPositionTenths(5_000)).toBe('0:05.0')
  })
})

describe('sourceRoleWord', () => {
  it('maps known roles to operator words', () => {
    expect(sourceRoleWord('background')).toBe('background bed')
    expect(sourceRoleWord('cue')).toBe('cue')
    expect(sourceRoleWord('announcement')).toBe('announcement')
  })
  it('falls back to the raw role text when this build does not know it', () => {
    expect(sourceRoleWord('sting')).toBe('sting')
  })
})

describe('nodePlaybackStatus', () => {
  it('advances a playing session locally by elapsed wall time from its own observedAt', () => {
    const status = nodePlaybackStatus(nodeWith(playingSession('bg', 10_000)), NOW_MS + 5_000)
    expect(status.sessions).toHaveLength(1)
    expect(status.sessions[0]?.position).toEqual({ kind: 'value', value: { text: '0:15.0', ageMs: 5_000 } })
  })

  it('does not advance a paused session; it reads the reported position exactly', () => {
    const audio = [
      sessionEv('bg', 'audio_session.source_role', { value: 'background' }),
      sessionEv('bg', 'audio_session.playlist.item_id', { value: 'item-42' }),
      sessionEv('bg', 'audio_session.state', { value: 'paused' }),
      sessionEv('bg', 'audio_session.position_ms', { value: 10_000, observedAt: NOW }),
    ]
    const status = nodePlaybackStatus(nodeWith(audio), NOW_MS + 5_000)
    expect(status.sessions[0]?.position).toEqual({ kind: 'value', value: { text: '0:10.0', ageMs: 5_000 } })
  })

  it('a new report snaps to its own value rather than continuing the previous advance', () => {
    const first = nodePlaybackStatus(nodeWith(playingSession('bg', 10_000)), NOW_MS + 5_000)
    expect(first.sessions[0]?.position).toEqual({ kind: 'value', value: { text: '0:15.0', ageMs: 5_000 } })

    const laterObservedAt = new Date(NOW_MS + 5_000).toISOString()
    const secondAudio = playingSession('bg', 20_000, { observedAt: laterObservedAt })
    const second = nodePlaybackStatus(nodeWith(secondAudio), NOW_MS + 6_000)
    expect(second.sessions[0]?.position).toEqual({ kind: 'value', value: { text: '0:21.0', ageMs: 1_000 } })
  })

  it('stops advancing and reads stale once the observation passes 45 s old', () => {
    const oldObservedAt = new Date(NOW_MS - 46_000).toISOString()
    const audio = playingSession('bg', 10_000, { observedAt: oldObservedAt })
    const status = nodePlaybackStatus(nodeWith(audio), NOW_MS)
    expect(status.sessions[0]?.position).toEqual({ kind: 'absent', absence: 'stale', label: 'Stale', fact: 'Last reported 46 s ago.' })
  })

  it('reads stale the moment the node reports the session stale, even if the observation is fresh', () => {
    const audio = [...playingSession('bg', 10_000), sessionEv('bg', 'audio_session.stale', { value: true })]
    const status = nodePlaybackStatus(nodeWith(audio), NOW_MS + 1_000)
    expect(status.sessions[0]?.position.kind).toBe('absent')
    expect((status.sessions[0]?.position as { absence: string }).absence).toBe('stale')
  })

  it('a position that was never collected reads as the kit absence, never 0:00', () => {
    const audio = [
      sessionEv('bg', 'audio_session.source_role', { value: 'background' }),
      sessionEv('bg', 'audio_session.playlist.item_id', { value: 'item-42' }),
      sessionEv('bg', 'audio_session.state', { value: 'playing' }),
      sessionEv('bg', 'audio_session.position_ms', { state: 'not_collected', value: null, reason: 'no fresh position is available' }),
    ]
    const status = nodePlaybackStatus(nodeWith(audio), NOW_MS)
    expect(status.sessions[0]?.position).toEqual({ kind: 'absent', absence: 'unobserved', label: 'Unobserved', fact: 'no fresh position is available' })
  })

  it('reports operator words for the source role', () => {
    const status = nodePlaybackStatus(nodeWith(playingSession('bg', 0)), NOW_MS)
    expect(status.sessions[0]?.sourceRole).toEqual({ kind: 'value', value: 'background bed' })
  })

  it('reports no session rows for a node that has never reported an audio session', () => {
    const status = nodePlaybackStatus(nodeWith([nodeEv('node.audio.ltc.timecode', { value: '01:00:00:00' })]), NOW_MS)
    expect(status.sessions).toEqual([])
  })

  it('discovers every distinct session id the node has reported, in first-seen order', () => {
    const audio = [...playingSession('bg', 0), ...playingSession('cue', 0)]
    const status = nodePlaybackStatus(nodeWith(audio), NOW_MS)
    expect(status.sessions.map((s) => s.sessionId)).toEqual(['bg', 'cue'])
  })

  it('the LTC timecode reads fresh and does not advance locally', () => {
    const status = nodePlaybackStatus(nodeWith([nodeEv('node.audio.ltc.timecode', { value: '01:00:00:00', observedAt: NOW })]), NOW_MS + 10_000)
    expect(status.ltcTimecode).toEqual({ kind: 'value', value: { text: '01:00:00:00', ageMs: 10_000 } })
  })

  it('the LTC timecode reads stale once its own report passes 45 s old', () => {
    const oldObservedAt = new Date(NOW_MS - 50_000).toISOString()
    const status = nodePlaybackStatus(nodeWith([nodeEv('node.audio.ltc.timecode', { value: '01:00:00:00', observedAt: oldObservedAt })]), NOW_MS)
    expect(status.ltcTimecode).toEqual({ kind: 'absent', absence: 'stale', label: 'Stale', fact: 'Last reported 50 s ago.' })
  })

  it('the LTC timecode reads absent when this node has never reported it', () => {
    const status = nodePlaybackStatus(nodeWith([]), NOW_MS)
    expect(status.ltcTimecode).toEqual({ kind: 'absent', absence: 'unobserved', label: 'Unobserved', fact: 'This node has not reported this signal.' })
  })
})
