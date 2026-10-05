/**
 * Pure derivations for the FPP instance detail's Pairing and Brightness
 * sections (CONTRACT.md sections 1 and 3): the pairing state's plain
 * sentence, pairing-code normalization, and the four brightness signals as
 * kit-ready facts. No React, no fetching, so each is testable without a
 * coordinator.
 */
import type { Evidence, FPPPairingStateResponse } from '../api'
import type { Absence } from '../kit'
import { EVIDENCE_ABSENCE, EVIDENCE_LABEL } from '../domain/evidence'
import { formatClock } from '../domain/time'

export type SignalFact<T> = { kind: 'value'; value: T } | { kind: 'absent'; absence: Absence; label: string; fact: string }

function findSignal(entries: readonly Evidence[], signal: string): Evidence | undefined {
  return entries.find((entry) => entry.signal === signal)
}

const NOT_REPORTED_REASON = 'This FPP instance has not reported this signal.'

function fact<T>(entry: Evidence | undefined, toValue: (value: NonNullable<Evidence['value']>) => T): SignalFact<T> {
  if (entry === undefined || entry.state !== 'current' || entry.value === null) {
    const absence: Absence = entry === undefined ? 'unobserved' : EVIDENCE_ABSENCE[entry.state]
    const label = entry === undefined ? EVIDENCE_LABEL.not_collected : EVIDENCE_LABEL[entry.state]
    return { kind: 'absent', absence, label, fact: entry?.reason ?? NOT_REPORTED_REASON }
  }
  return { kind: 'value', value: toValue(entry.value) }
}

export type BrightnessReadout = {
  ceiling: SignalFact<number>
  transitionGain: SignalFact<number>
  effectiveOutput: SignalFact<number>
  fadeActive: SignalFact<boolean>
}

/** The four `fpp.brightness.*` signals (contract section 3), each a kit-ready fact. */
export function brightnessReadout(observations: readonly Evidence[]): BrightnessReadout {
  return {
    ceiling: fact<number>(findSignal(observations, 'fpp.brightness.ceiling'), (v) => Number(v)),
    transitionGain: fact<number>(findSignal(observations, 'fpp.brightness.transition_gain'), (v) => Number(v)),
    effectiveOutput: fact<number>(findSignal(observations, 'fpp.brightness.effective_output'), (v) => Number(v)),
    fadeActive: fact<boolean>(findSignal(observations, 'fpp.brightness.fade_active'), (v) => Boolean(v)),
  }
}

/**
 * A pairing code as the plugin's own screen displays it, in any case, with
 * or without the dash (contract section 1). Anything the operator types
 * past eight alphanumeric characters is dropped rather than accepted
 * silently past the shape the coordinator expects; the coordinator itself
 * is the final judge of whether the result is a valid code.
 */
export function normalizePairingCode(input: string): string {
  const cleaned = input
    .replace(/[^0-9A-Za-z]/g, '')
    .toUpperCase()
    .slice(0, 8)
  if (cleaned.length <= 4) return cleaned
  return `${cleaned.slice(0, 4)}-${cleaned.slice(4)}`
}

/** The plain-English pairing state line the contract requires (section 1): not paired, waiting with the code and expiry, or paired at a time. */
export function pairingSentence(state: FPPPairingStateResponse): string {
  if (state.state === 'waiting') {
    const expires = formatClock(state.expiresAt)
    return expires !== null ? `Waiting for the plugin: code ${state.code}, expires ${expires}.` : `Waiting for the plugin: code ${state.code}.`
  }
  if (state.state === 'paired') {
    const pairedAt = formatClock(state.pairedAt)
    return pairedAt !== null ? `Paired at ${pairedAt}.` : 'Paired with the plugin.'
  }
  return 'Not paired with the plugin.'
}
