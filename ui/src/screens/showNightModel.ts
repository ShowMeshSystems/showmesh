import type { Model, NightCue, NightSessionState } from '../api'
import type { Tone } from '../kit'
import { ageMs, formatClock, formatDuration } from '../domain/time'
import { findSignal, transportState } from './liveControlModel'

/** The lifecycle states the controller reports, in the order it moves through them. */
const CYCLE_STEPS: readonly { state: NightSessionState['state']; label: string }[] = [
  { state: 'resting-intershow', label: 'Resting' },
  { state: 'transition-to-show', label: 'To show' },
  { state: 'live', label: 'Live' },
  { state: 'transition-to-resting', label: 'To resting' },
]

export type RailStep = {
  key: string
  label: string
  detail: string
  status: 'done' | 'now' | 'ahead' | 'unknown' | 'notWired'
}

/** The states outside the repeating cycle, and what to call each on the rail. */
const OFF_CYCLE_LABEL: Record<string, string> = {
  inactive: 'Inactive',
  preparing: 'Preparing',
  preshow: 'Preshow',
  'end-of-night-resting': 'End-of-night resting',
  'fading-out': 'Fading out',
  stopped: 'Stopped',
}

export function cycleRail(session: NightSessionState, nowIso: string | null): RailStep[] {
  const currentIndex = CYCLE_STEPS.findIndex((step) => step.state === session.state)
  const inState = ageMs(session.stateEnteredAt, nowIso)
  if (currentIndex === -1) {
    const label = OFF_CYCLE_LABEL[session.state] ?? session.state
    return [
      {
        key: session.state,
        label,
        detail: `Not in the repeating cycle · ${inState === null ? 'now' : `${formatDuration(inState)} in state`}`,
        status: 'now' as const,
      },
    ]
  }
  return CYCLE_STEPS.map((step, index) => {
    if (index < currentIndex) return { key: step.state, label: step.label, detail: 'done this cycle', status: 'done' as const }
    if (index === currentIndex) {
      return {
        key: step.state,
        label: step.label,
        detail: inState === null ? 'now' : `${formatDuration(inState)} in state`,
        status: 'now' as const,
      }
    }
    return { key: step.state, label: step.label, detail: 'ahead', status: 'ahead' as const }
  })
}

/**
 * The whole-night timeline the mock draws. `NightSessionState` reports only
 * the cycle the session is in, so cycles before it are placeholders: the
 * step exists because the cycle happened, but nothing about it is reported.
 */
export function nightRail(session: NightSessionState): RailStep[] {
  const cycleSteps: RailStep[] = []
  for (let cycle = 1; cycle <= session.cycle; cycle += 1) {
    if (cycle < session.cycle) {
      cycleSteps.push({
        key: `cycle-${cycle}`,
        label: `Cycle ${cycle}`,
        detail: 'not reported',
        status: 'notWired',
      })
    } else {
      cycleSteps.push({
        key: `cycle-${cycle}`,
        label: `Cycle ${cycle}`,
        detail: session.state,
        status: 'now',
      })
    }
  }

  const rail: RailStep[] = [
    ...cycleSteps,
    {
      key: 'more',
      label: 'More cycles',
      detail: session.admissionClosed ? 'admission closed' : 'while admission open',
      status: session.admissionClosed ? 'ahead' : 'ahead',
    },
    {
      key: 'end',
      label: 'End of night',
      detail: session.finalShowRequested
        ? `final show requested${session.finalShowRequestedAt === null ? '' : ` ${formatClock(session.finalShowRequestedAt) ?? ''}`}`
        : 'not requested',
      status: session.finalShowRequested ? 'now' : 'ahead',
    },
  ]
  if (session.shutdownIntent !== '') {
    rail.push({ key: 'shutdown', label: 'Shutdown', detail: session.shutdownIntent, status: 'now' })
  }
  return rail
}

export type NextTransition =
  | { known: true; remainingSeconds: number; source: string }
  | { known: false; reason: string }

/**
 * Derived from observed playback, not a clock. A stale or missing position
 * makes the boundary unknown rather than assumed.
 */
export function nextTransition(model: Model): NextTransition {
  const instance = model.fpp[0]
  if (instance === undefined) return { known: false, reason: 'No FPP instance is reporting a position.' }
  const elapsed = findSignal(instance.observations, 'fpp.position.elapsed.seconds')
  const total = findSignal(instance.observations, 'fpp.position.seconds')
  if (elapsed === undefined || total === undefined || typeof elapsed.value !== 'number' || typeof total.value !== 'number') {
    return { known: false, reason: 'The playhead position has not been observed.' }
  }
  if (elapsed.state !== 'current' || total.state !== 'current') {
    return { known: false, reason: `The playhead position is ${elapsed.state.replace('_', ' ')}, so the boundary is unknown rather than assumed.` }
  }
  return { known: true, remainingSeconds: Math.max(0, total.value - elapsed.value), source: instance.instanceId }
}

/** The outcome enum reads as a verdict in a sentence, not as a bare word. */
const READINESS_WORD: Record<string, string> = {
  ready: 'Passed',
  not_ready: 'Did not pass',
  unknown: 'Could not be determined',
}

export type EvidenceReadout = { key: string; label: string; tone: Tone; fact: string }

/**
 * `recorded` says evidence exists, never that the phase went well: the
 * reason carries what happened. So the label states the evidence state and
 * the tone never reads as a health verdict.
 */
const PHASE_TONE: Record<string, Tone> = {
  recorded: 'pending',
  unknown: 'unknown',
  not_configured: 'pending',
  not_available: 'unknown',
}

function phaseLabel(name: string, state: string): string {
  return `${name} ${state.replace('_', ' ')}`
}

/** Six readouts. Anything not observed says so, and none of them is inferred. */
export function evidenceReadouts(session: NightSessionState, nowIso: string | null): EvidenceReadout[] {
  const readiness = session.readiness
  const readinessAge = ageMs(readiness.completedAt ?? null, nowIso)
  const readinessFact =
    readiness.state !== 'recorded'
      ? readiness.reason !== ''
        ? readiness.reason
        : 'No readiness result has been recorded.'
      : `${READINESS_WORD[readiness.outcome ?? 'unknown']} at ${formatClock(readiness.completedAt ?? null) ?? 'an unrecorded time'}` +
        `${readinessAge === null ? '' : `, ${formatDuration(readinessAge)} ago`}` +
        `${readiness.sameEpoch ? ', this epoch' : ', from an earlier epoch'}` +
        `${readiness.fresh ? '' : ', no longer fresh'}.`

  return [
    {
      key: 'transition',
      label: phaseLabel('Transition', session.transition.state),
      tone: PHASE_TONE[session.transition.state] ?? 'unknown',
      fact: session.transition.reason !== '' ? session.transition.reason : 'Nothing recorded for this transition.',
    },
    {
      key: 'power',
      label: phaseLabel('Power phase', session.powerPhase.state),
      tone: PHASE_TONE[session.powerPhase.state] ?? 'unknown',
      fact: session.powerPhase.reason !== '' ? session.powerPhase.reason : 'Nothing recorded for the power phase.',
    },
    {
      key: 'readiness',
      label: 'Readiness',
      tone:
        readiness.state !== 'recorded'
          ? 'unknown'
          : readiness.outcome === 'ready' && readiness.fresh && readiness.sameEpoch
            ? 'good'
            : 'warn',
      fact: readinessFact,
    },
    {
      key: 'audio',
      label: phaseLabel('Background audio', session.backgroundAudio.state),
      tone: PHASE_TONE[session.backgroundAudio.state] ?? 'unknown',
      fact:
        session.backgroundAudio.reason !== ''
          ? `${session.backgroundAudio.reason} ${session.backgroundAudio.steps.length} steps this cycle.`
          : `${session.backgroundAudio.steps.length} steps this cycle.`,
    },
    {
      key: 'attribution',
      label: session.attributionDegraded ? 'Attribution degraded' : 'Attribution complete',
      tone: session.attributionDegraded ? 'bad' : 'good',
      fact: session.attributionDegraded
        ? 'A step was dispatched with no authorizing principal recorded. This never clears for the rest of the session.'
        : 'Every dispatch this session has an authorizing principal recorded.',
    },
    {
      key: 'authorization',
      label: 'Last command',
      tone: session.authorization.state === 'recorded' ? 'pending' : 'unknown',
      fact:
        session.authorization.state === 'recorded'
          ? `${session.authorization.command ?? 'a command'} by ${session.authorization.principalName ?? 'an unnamed principal'}, recorded ${formatClock(session.authorization.recordedAt) ?? 'at an unrecorded time'}.`
          : session.authorization.reason !== undefined && session.authorization.reason !== ''
            ? session.authorization.reason
            : 'No authorizing command has been recorded for this session.',
    },
  ]
}

export type RunStep = {
  key: string
  when: string
  name: string
  detail: string
  phase: string
  target: string
  action: string
  tone: Tone
  state: string
  resolved: string | null
}

const CUE_PHASE: Record<NightCue['phase'], string> = {
  enterShow: 'Enter show',
  enterResting: 'Enter resting',
  fadeOut: 'Fade out',
}

const OUTCOME_TONE: Record<string, Tone> = {
  confirmed: 'good',
  unconfirmed: 'warn',
  // Unavailable, not a failure: an action expecting no response reports this
  // on every run, forever, by design.
  unconfirmable: 'pending',
  failed: 'bad',
  refused: 'bad',
  ambiguous: 'warn',
}

/** `unconfirmable` is unavailable, not a failure: it reports that on every run, by design. */
export function runOfShow(session: NightSessionState): RunStep[] {
  return session.cues.cues.map((cue, index) => {
    const outcome = cue.outcome
    return {
      key: `${cue.name}:${index}`,
      when: cue.dispatchedAt === null ? 'Armed' : (formatClock(cue.dispatchedAt) ?? 'dispatched'),
      name: cue.name,
      detail: cue.actionRevision === null ? cue.role : `${cue.role} · rev ${cue.actionRevision}`,
      phase: CUE_PHASE[cue.phase],
      target: cue.role,
      action: cue.action,
      tone: outcome === undefined ? (cue.state === 'not_dispatched' ? 'pending' : 'warn') : (OUTCOME_TONE[outcome] ?? 'unknown'),
      state: outcome ?? (cue.state === 'not_dispatched' ? 'Armed' : cue.state.replace('_', ' ')),
      resolved: cue.resolvedAt === null ? (cue.reason ?? null) : formatClock(cue.resolvedAt),
    }
  })
}

export function nowPlaying(model: Model) {
  const instance = model.fpp[0]
  const run = model.currentRuns?.runs.find((entry) => entry.runner === 'fpp')
  const state = instance === undefined ? null : transportState(instance)
  return { instance, run, state }
}
