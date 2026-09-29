import type { Evidence, Node } from '../api'
import type { Absence } from '../kit'
import { EVIDENCE_ABSENCE, EVIDENCE_LABEL } from '../domain/evidence'

/** A signal's value when current, or the kit's absence presentation when it is not. */
export type SignalFact<T> = { kind: 'value'; value: T } | { kind: 'absent'; absence: Absence; label: string; fact: string }

export type LocalClockValue = { name: string; setByOperator: boolean }

export type NodeSyncStatus = {
  localClock: SignalFact<LocalClockValue>
  syncLine: SignalFact<string>
  steer: SignalFact<string>
}

function findSignal(entries: readonly Evidence[], signal: string): Evidence | undefined {
  return entries.find((entry) => entry.signal === signal)
}

const NOT_REPORTED_REASON = 'This node has not reported this signal.'

function fact<T>(entry: Evidence | undefined, toValue: (value: NonNullable<Evidence['value']>) => T): SignalFact<T> {
  if (entry === undefined || entry.state !== 'current' || entry.value === null) {
    const absence: Absence = entry === undefined ? 'unobserved' : EVIDENCE_ABSENCE[entry.state]
    const label = entry === undefined ? EVIDENCE_LABEL.not_collected : EVIDENCE_LABEL[entry.state]
    return { kind: 'absent', absence, label, fact: entry?.reason ?? NOT_REPORTED_REASON }
  }
  return { kind: 'value', value: toValue(entry.value) }
}

/** δ in µs to one decimal under 1 ms, otherwise ms to two decimals. Sign is whatever the offset carries; never forced positive. */
function formatOffsetNs(offsetNs: number): string {
  if (Math.abs(offsetNs) < 1_000_000) return `${(offsetNs / 1000).toFixed(1)} µs`
  return `${(offsetNs / 1_000_000).toFixed(2)} ms`
}

/** Always signed, two decimals: the steer and (once measured) the rate are adjustments, where +0.00 and -0.00 are different facts than "absent". */
function formatSignedPpm(ppm: number): string {
  const sign = ppm < 0 ? '-' : '+'
  return `${sign}${Math.abs(ppm).toFixed(2)}`
}

/** A state value this build does not know must never borrow a known state's name or presentation. */
function syncLineText(state: string, follows: SignalFact<string>, offsetNs: SignalFact<number>): SignalFact<string> {
  if (state === 'free_running') return { kind: 'value', value: 'Sync: Free-running on the local clock' }
  if (state !== 'locked' && state !== 'acquiring') {
    return {
      kind: 'absent',
      absence: 'unavailable',
      label: 'Unavailable',
      fact: `This node reported a sync state this version does not know: ${state}.`,
    }
  }

  const stateWord = state === 'locked' ? 'Locked' : 'Acquiring'
  const clauses: string[] = []
  if (follows.kind === 'value' && follows.value !== '') clauses.push(`Follows ${follows.value}`)
  if (offsetNs.kind === 'value') clauses.push(`δ ${formatOffsetNs(offsetNs.value)}`)

  return { kind: 'value', value: clauses.length === 0 ? `Sync: ${stateWord}.` : `Sync: ${stateWord}. ${clauses.join(', ')}` }
}

/**
 * ADR-052 decision 6's sync status line, plus the local clock fact and the
 * PTP frequency steer, for a node that has already been confirmed to carry
 * the audio capability. Every value not currently reported is omitted, never
 * guessed or shown as zero.
 */
export function nodeSyncStatus(node: Node): NodeSyncStatus {
  const localClockEntry = findSignal(node.audio, 'node.audio.clock.local')
  const localClockSourceEntry = findSignal(node.audio, 'node.audio.clock.local.source')
  const localClock = fact(localClockEntry, (value) => ({
    name: String(value),
    setByOperator: localClockSourceEntry?.state === 'current' && localClockSourceEntry.value === 'override',
  }))

  const stateEntry = findSignal(node.audio, 'node.audio.sync.state')
  const followsFact = fact(findSignal(node.audio, 'node.audio.sync.follows'), (value) => String(value))
  const offsetFact = fact(findSignal(node.audio, 'node.audio.sync.offset_ns'), (value) => Number(value))

  const syncLine: SignalFact<string> =
    stateEntry !== undefined && stateEntry.state === 'current' && stateEntry.value !== null
      ? syncLineText(String(stateEntry.value), followsFact, offsetFact)
      : fact(stateEntry, () => '')

  const steer = fact(findSignal(node.clock, 'node.clock.ptp.frequency_ppm'), (value) => `Clock steered ${formatSignedPpm(Number(value))} ppm`)

  return { localClock, syncLine, steer }
}
