import type { Model, NightBackgroundAudio, NightCue, NightReadinessCheck, NightSessionState } from '../api'
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
 * `not_verifiable` is structurally incapable of ever reporting anything
 * else, and `not_configured` means the check's own optional configuration
 * is absent - neither is a failure, so both read as `pending` rather than
 * `unknown` or `bad`.
 */
const READINESS_CHECK_TONE: Record<NightReadinessCheck['state'], Tone> = {
  healthy: 'good',
  degraded: 'warn',
  failed: 'bad',
  unknown: 'unknown',
  not_verifiable: 'pending',
  not_configured: 'pending',
}

export type ReadinessCheckReadout = { key: string; label: string; tone: Tone; fact: string }

/**
 * Every check `run-readiness` recorded, including its own `interlock:<phase>:<name>`
 * entries - the pre-emptive interlock visibility, not just the aggregate
 * outcome the readiness readout above already states.
 */
export function readinessChecks(checks: readonly NightReadinessCheck[]): ReadinessCheckReadout[] {
  return checks.map((check, index) => ({
    key: `${check.name}:${index}`,
    label: `${check.name} · ${check.state.replace('_', ' ')}`,
    tone: READINESS_CHECK_TONE[check.state] ?? 'unknown',
    fact: check.reason !== '' ? check.reason : 'No reason was recorded for this check.',
  }))
}

/**
 * The ceiling this running session pinned when it started
 * (`resting.backgroundAudio.maxGainDb` on its own pinned `configRevision`),
 * stated so it is never mistaken for whatever `night.session`'s config
 * currently holds - that can differ across a later revision (owner ruling
 * 2026-08-28).
 */
export function pinnedCeilingFact(audio: NightBackgroundAudio): string {
  if (audio.pinnedMaxGainDb === undefined) {
    return "This build has not reported a pinned ceiling for this session, distinct from night.session's own config."
  }
  if (audio.pinnedMaxGainDb === null) {
    return audio.reason !== ''
      ? `Pinned ceiling: none. ${audio.reason}`
      : "This session pinned no background-audio ceiling: its pinned revision configures no background audio."
  }
  return `Pinned ceiling for this running session: ${audio.pinnedMaxGainDb} dB - the value this session locked in when it started, not whatever night.session's config currently holds.`
}

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

/** Anything not observed says so, and none of these readouts is inferred. */
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
        session.backgroundAudio.state !== 'recorded'
          ? session.backgroundAudio.reason !== ''
            ? session.backgroundAudio.reason
            : 'Nothing recorded for background audio.'
          : `${session.backgroundAudio.steps.length} ${session.backgroundAudio.steps.length === 1 ? 'step' : 'steps'} this cycle.`,
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
    {
      key: 'definition',
      label: 'Definition',
      tone: 'pending',
      fact: `${session.configObjectId} at revision ${session.configRevision}.`,
    },
    {
      key: 'armed-show',
      label: session.armedShowId === '' ? 'No show armed' : session.showCommitted ? 'Show committed' : 'Show armed',
      tone: session.armedShowId === '' ? 'unknown' : session.showCommitted ? 'good' : 'pending',
      fact:
        session.armedShowId === ''
          ? 'No show is armed for this session.'
          : session.showCommitted
            ? `${session.armedShowId} is armed and committed for this session.`
            : `${session.armedShowId} is armed but not yet committed.`,
    },
    {
      key: 'admission',
      label: session.admissionClosed ? 'Admission closed' : 'Admission open',
      tone: session.admissionClosed ? 'pending' : 'good',
      fact: session.admissionClosed
        ? `Admission closed ${formatClock(session.admissionClosedAt) ?? 'at an unrecorded time'}.`
        : 'Admission is open.',
    },
    {
      key: 'updated',
      label: 'Session last updated',
      tone: 'pending',
      fact: `${formatClock(session.updatedAt) ?? 'an unrecorded time'}.`,
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

export type BackgroundAudioStepRow = {
  key: string
  when: string
  sequence: string
  cueName: string
  kind: string
  detail: string
  tone: Tone
  state: string
  resolved: string | null
}

/** Every durable audio step this cycle recorded, `not_dispatched` never among them: unlike a cue, a step is only ever logged once dispatched. */
export function backgroundAudioSteps(audio: NightBackgroundAudio): BackgroundAudioStepRow[] {
  return audio.steps.map((step, index) => {
    const outcome = step.outcome
    return {
      key: `${step.cueName}:${step.kind}:${index}`,
      when: step.dispatchedAt === null ? 'Armed' : (formatClock(step.dispatchedAt) ?? 'dispatched'),
      sequence: step.sequence,
      cueName: step.cueName,
      kind: step.kind,
      detail: `rev ${step.actionRevision}`,
      tone: outcome === undefined ? (step.state === 'pending' ? 'pending' : 'warn') : (OUTCOME_TONE[outcome] ?? 'unknown'),
      state: outcome ?? step.state.replace('_', ' '),
      resolved: step.resolvedAt === null ? (step.reason ?? null) : formatClock(step.resolvedAt),
    }
  })
}

export function nowPlaying(model: Model) {
  const instance = model.fpp[0]
  const run = model.currentRuns?.runs.find((entry) => entry.runner === 'fpp')
  const state = instance === undefined ? null : transportState(instance)
  return { instance, run, state }
}
