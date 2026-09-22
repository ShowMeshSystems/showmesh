import type { Node, ObservationEntry } from '../api'
import type { Absence } from '../kit'
import { EVIDENCE_ABSENCE, EVIDENCE_LABEL } from '../domain/evidence'
import { ageMs, formatDuration } from '../domain/time'
import type { SignalFact } from './nodeSyncModel'

/** A session's or the LTC generator's own report is too old to trust for a moving reading once it passes this age. */
const STALE_THRESHOLD_MS = 45_000

const SOURCE_ROLE_WORDS: Record<string, string> = {
  background: 'background bed',
  cue: 'cue',
  announcement: 'announcement',
}

/** Operator words for a source role, or the raw role text this build does not know. */
export function sourceRoleWord(role: string): string {
  return SOURCE_ROLE_WORDS[role] ?? role
}

const NOT_REPORTED_REASON = 'This node has not reported this signal.'

function findSignal(entries: readonly ObservationEntry[], kind: string, id: string, signal: string): ObservationEntry | undefined {
  return entries.find((entry) => entry.resource.kind === kind && entry.resource.id === id && entry.signal === signal)
}

function fact<T>(entry: ObservationEntry | undefined, toValue: (value: NonNullable<ObservationEntry['value']>) => T): SignalFact<T> {
  if (entry === undefined || entry.state !== 'current' || entry.value === null) {
    const absence: Absence = entry === undefined ? 'unobserved' : EVIDENCE_ABSENCE[entry.state]
    const label = entry === undefined ? EVIDENCE_LABEL.not_collected : EVIDENCE_LABEL[entry.state]
    return { kind: 'absent', absence, label, fact: entry?.reason ?? NOT_REPORTED_REASON }
  }
  return { kind: 'value', value: toValue(entry.value) }
}

function staleFact<T>(age: number): SignalFact<T> {
  return { kind: 'absent', absence: 'stale', label: 'Stale', fact: `Last reported ${formatDuration(age)} ago.` }
}

/** m:ss.t. Never a bare millisecond or second count. */
export function formatPositionTenths(ms: number): string {
  const totalTenths = Math.max(0, Math.round(ms / 100))
  const wholeSeconds = Math.floor(totalTenths / 10)
  const tenth = totalTenths % 10
  const minutes = Math.floor(wholeSeconds / 60)
  const seconds = wholeSeconds % 60
  return `${minutes}:${String(seconds).padStart(2, '0')}.${tenth}`
}

/** A reading that is still fresh: its display text and its own age, so the row can show both. */
export type FreshReading = { text: string; ageMs: number }

export type SessionPlaybackRow = {
  sessionId: string
  sourceRole: SignalFact<string>
  item: SignalFact<string>
  state: SignalFact<string>
  position: SignalFact<FreshReading>
}

export type NodePlaybackStatus = {
  sessions: SessionPlaybackRow[]
  ltcTimecode: SignalFact<FreshReading>
}

/** Every distinct audio_session id this node's own observations name, in first-seen order. */
function sessionIds(entries: readonly ObservationEntry[]): string[] {
  const seen: string[] = []
  for (const entry of entries) {
    if (entry.resource.kind !== 'audio_session') continue
    if (!seen.includes(entry.resource.id)) seen.push(entry.resource.id)
  }
  return seen
}

/**
 * One session's position, advanced locally by elapsed wall time while it is
 * reported playing and its report is fresh; snapped to whatever the latest
 * report says otherwise. Reads as stale, and stops advancing, the moment
 * its report passes 45 s old or the node itself reports the session stale.
 * `nowMs` is the caller's own clock (coordinator time where available), so
 * this stays testable without a real timer.
 */
function positionFact(entries: readonly ObservationEntry[], sessionId: string, stateValue: string | null, nowMs: number): SignalFact<FreshReading> {
  const positionEntry = findSignal(entries, 'audio_session', sessionId, 'audio_session.position_ms')
  if (positionEntry === undefined || positionEntry.state !== 'current' || positionEntry.value === null) {
    return fact(positionEntry, () => ({ text: '', ageMs: 0 }))
  }

  const age = ageMs(positionEntry.observedAt, new Date(nowMs).toISOString()) ?? 0
  const staleEntry = findSignal(entries, 'audio_session', sessionId, 'audio_session.stale')
  const reportedStale = staleEntry?.state === 'current' && staleEntry.value === true
  if (age > STALE_THRESHOLD_MS || reportedStale) return staleFact(Math.max(0, age))

  const reportedMs = Number(positionEntry.value)
  const currentMs = stateValue === 'playing' ? reportedMs + Math.max(0, age) : reportedMs
  return { kind: 'value', value: { text: formatPositionTenths(currentMs), ageMs: Math.max(0, age) } }
}

/**
 * The LTC timecode string's freshness fact: the same 45 s staleness rule as
 * a session position, but never advanced locally, because it is text, not
 * a number this model can add elapsed time to.
 */
function timecodeFact(entries: readonly ObservationEntry[], nowMs: number): SignalFact<FreshReading> {
  const entry = entries.find((candidate) => candidate.resource.kind === 'node' && candidate.signal === 'node.audio.ltc.timecode')
  if (entry === undefined || entry.state !== 'current' || entry.value === null) {
    return fact(entry, () => ({ text: '', ageMs: 0 }))
  }
  const age = ageMs(entry.observedAt, new Date(nowMs).toISOString()) ?? 0
  if (age > STALE_THRESHOLD_MS) return staleFact(Math.max(0, age))
  return { kind: 'value', value: { text: String(entry.value), ageMs: Math.max(0, age) } }
}

/**
 * The "Now playing" section's model: one row per audio session this node
 * has ever reported, plus the LTC timecode it reports at the node level.
 * `nowMs` drives the local advance; pass the render tick's own clock, not a
 * captured one, so the row keeps moving between reports.
 */
export function nodePlaybackStatus(node: Node, nowMs: number): NodePlaybackStatus {
  const entries = node.audio
  const sessions = sessionIds(entries).map((sessionId): SessionPlaybackRow => {
    const roleEntry = findSignal(entries, 'audio_session', sessionId, 'audio_session.source_role')
    const sourceRole = fact(roleEntry, (value) => sourceRoleWord(String(value)))
    const itemEntry = findSignal(entries, 'audio_session', sessionId, 'audio_session.playlist.item_id')
    const item = fact(itemEntry, (value) => String(value))
    const stateEntry = findSignal(entries, 'audio_session', sessionId, 'audio_session.state')
    const state = fact(stateEntry, (value) => String(value))
    const stateValue = state.kind === 'value' ? state.value : null

    return {
      sessionId,
      sourceRole,
      item,
      state,
      position: positionFact(entries, sessionId, stateValue, nowMs),
    }
  })

  return { sessions, ltcTimecode: timecodeFact(entries, nowMs) }
}
