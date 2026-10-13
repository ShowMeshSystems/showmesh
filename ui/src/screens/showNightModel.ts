import type { Model, NightBackgroundAudio, NightCue, NightCycleOutcome, NightReadinessCheck, NightSessionState } from '../api'
import type { Tone } from '../kit'
import { ageMs, formatClock, formatDuration } from '../domain/time'
import { NIGHT_LIFECYCLE_GROUPS, findSignal, transportState } from './liveControlModel'

/** A night command in its button's own words, such as Start night. */
export function nightCommandLabel(command: string): string {
  for (const group of NIGHT_LIFECYCLE_GROUPS) {
    const found = group.commands.find(([id]) => id === command)
    if (found !== undefined) return found[1]
  }
  if (command === 'resume-show') return 'Resume'
  return inWords(command)
}

/** A raw id such as end-of-night as words: End of night. */
export function inWords(id: string): string {
  const words = id
    .replace(/([a-z])([A-Z])/g, '$1 $2')
    .replace(/[-_:]+/g, ' ')
    .trim()
    .toLowerCase()
  return words === '' ? '' : words.charAt(0).toUpperCase() + words.slice(1)
}

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
  /** Hover text when the strip has room for one word only. */
  title?: string
  status: 'done' | 'now' | 'ahead' | 'unknown' | 'notWired' | 'error' | 'stopped'
}

/** The one word the cycle strip has room for; the reason lives in the coordinator log. */
const CYCLE_OUTCOME_LABEL: Record<NightCycleOutcome['outcome'], string> = {
  completed: 'Complete',
  stopped: 'Stopped',
  interrupted: 'Interrupted',
  unknown: 'Unconfirmed',
}

const CYCLE_OUTCOME_STATUS: Record<NightCycleOutcome['outcome'], RailStep['status']> = {
  completed: 'done',
  stopped: 'stopped',
  interrupted: 'error',
  unknown: 'unknown',
}

export function cycleRail(session: NightSessionState, nowIso: string | null): RailStep[] {
  const currentIndex = CYCLE_STEPS.findIndex((step) => step.state === session.state)
  const inState = ageMs(session.stateEnteredAt, nowIso)
  const phases = CYCLE_STEPS.map((step, index) => {
    if (currentIndex === -1) {
      return { key: step.state, label: step.label, detail: 'not started', status: 'ahead' as const }
    }
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
  return [
    ...phases,
    {
      key: 'back-to-resting',
      label: 'Back to resting',
      detail: session.cycle > 0 ? `starts cycle ${session.cycle + 1}` : 'next cycle not started',
      status: 'ahead',
    },
  ]
}

/**
 * The whole-night timeline the mock draws. Each cycle before the current one
 * is rendered from `session.finishedCycles`, verbatim; a cycle that ran
 * before that field existed has no record and reads as not recorded, never
 * as invented design.
 */
export function nightRail(session: NightSessionState): RailStep[] {
  const cycleActive = CYCLE_STEPS.some((step) => step.state === session.state)
  const preshowComplete = session.cycle > 0 || cycleActive || ['end-of-night-resting', 'fading-out', 'stopped'].includes(session.state)
  const preshow: RailStep = session.state === 'preshow'
    ? { key: 'preshow', label: 'Preshow', detail: 'now', status: 'now' }
    : preshowComplete
      ? { key: 'preshow', label: 'Preshow', detail: 'time not reported', status: 'done' }
      : { key: 'preshow', label: 'Preshow', detail: 'not started', status: 'ahead' }

  const cycleSteps: RailStep[] = []
  for (let cycle = 1; cycle <= Math.max(3, session.cycle); cycle += 1) {
    if (cycle < session.cycle) {
      const finished = session.finishedCycles?.find((entry) => entry.cycle === cycle)
      if (finished === undefined) {
        cycleSteps.push({
          key: `cycle-${cycle}`,
          label: `Cycle ${cycle}`,
          detail: 'Not recorded',
          status: 'unknown',
        })
      } else {
        cycleSteps.push({
          key: `cycle-${cycle}`,
          label: `Cycle ${cycle}`,
          detail: CYCLE_OUTCOME_LABEL[finished.outcome],
          title: `Check the coordinator log for cycle ${cycle}.`,
          status: CYCLE_OUTCOME_STATUS[finished.outcome],
        })
      }
    } else if (cycle === session.cycle && session.cycle > 0) {
      cycleSteps.push({
        key: `cycle-${cycle}`,
        label: `Cycle ${cycle}`,
        detail: session.stateLabel,
        status: 'now',
      })
    } else {
      cycleSteps.push({
        key: `cycle-${cycle}`,
        label: `Cycle ${cycle}`,
        detail: 'not started',
        status: 'ahead',
      })
    }
  }

  const rail: RailStep[] = [
    preshow,
    ...cycleSteps,
    {
      key: 'more',
      label: 'More cycles',
      detail: session.admissionClosed ? 'admission closed' : 'while admission is open',
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
    rail.push({ key: 'shutdown', label: 'Shutdown', detail: inWords(session.shutdownIntent), status: 'now' })
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
  if (instance === undefined) return { known: false, reason: 'No FPP is reporting a position.' }
  // fpp.position.seconds is FPP's seconds_played, not a duration, so
  // remaining is read directly rather than derived from it.
  const remaining = findSignal(instance.observations, 'fpp.position.remaining.seconds')
  if (remaining === undefined || typeof remaining.value !== 'number') {
    return { known: false, reason: 'FPP has not reported a position yet.' }
  }
  if (remaining.state !== 'current') {
    return { known: false, reason: 'The position FPP reported is out of date, so the next transition time is unknown.' }
  }
  return { known: true, remainingSeconds: Math.max(0, remaining.value), source: instance.instanceId }
}

/** Each fact is one message and renders on its own line. */
export type EvidenceReadout = { key: string; label: string; tone: Tone; facts: string[] }

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

export type ReadinessCheckReadout = { key: string; label: string; tone: Tone; facts: readonly string[] }

/**
 * Every check `run-readiness` recorded, including its own `interlock:<phase>:<name>`
 * entries - the pre-emptive interlock visibility, not just the aggregate
 * outcome the readiness readout above already states.
 */
export function readinessChecks(checks: readonly NightReadinessCheck[]): ReadinessCheckReadout[] {
  return checks.map((check, index) => ({
    key: `${check.name}:${index}`,
    label: `${check.label} · ${check.stateLabel}`,
    tone: READINESS_CHECK_TONE[check.state] ?? 'unknown',
    facts: check.reasons,
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
    return 'The volume ceiling for this night is not reported yet.'
  }
  if (audio.pinnedMaxGainDb === null) {
    return audio.reason !== '' ? audio.reason : 'This night has no volume ceiling for background audio.'
  }
  return `Volume ceiling: ${audio.pinnedMaxGainDb} dB, fixed when the night started.`
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

const PHASE_STATE_WORDS: Record<string, string> = {
  recorded: '',
  unknown: ' unknown',
  not_configured: ' not set up',
  not_available: ' unavailable',
}

function phaseLabel(name: string, state: string): string {
  return `${name}${PHASE_STATE_WORDS[state] ?? ' unknown'}`
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
      : `${readiness.outcomeLabel ?? 'Could not be determined'} at ${formatClock(readiness.completedAt ?? null) ?? 'an unrecorded time'}` +
        `${readinessAge === null ? '' : `, ${formatDuration(readinessAge)} ago`}` +
        `${readiness.sameEpoch ? ', since the last prepare' : ', before the last prepare'}` +
        `${readiness.fresh ? '' : ', no longer fresh'}.`

  const hold = session.stopHold
  return [
    ...(hold === undefined
      ? []
      : [
          {
            key: 'stopHold',
            label: hold.principal === undefined || hold.principal === '' ? 'Stopped' : `Stopped by ${hold.principal}`,
            tone: 'bad' as const,
            facts: [hold.reason, 'Resume starts the show from its first song.'],
          },
        ]),
    {
      key: 'transition',
      label: phaseLabel('Transition', session.transition.state),
      tone: PHASE_TONE[session.transition.state] ?? 'unknown',
      facts: [session.transition.reason !== '' ? session.transition.reason : 'Nothing is recorded for this transition.'],
    },
    {
      key: 'power',
      label: phaseLabel('Power phase', session.powerPhase.state),
      tone: PHASE_TONE[session.powerPhase.state] ?? 'unknown',
      facts: [session.powerPhase.reason !== '' ? session.powerPhase.reason : 'Nothing is recorded for the power phase.'],
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
      facts: [readinessFact],
    },
    {
      key: 'audio',
      label: phaseLabel('Background audio', session.backgroundAudio.state),
      tone: PHASE_TONE[session.backgroundAudio.state] ?? 'unknown',
      facts: [
        session.backgroundAudio.state !== 'recorded'
          ? session.backgroundAudio.reason !== ''
            ? session.backgroundAudio.reason
            : 'Nothing is recorded for background audio.'
          : `${session.backgroundAudio.steps.length} ${session.backgroundAudio.steps.length === 1 ? 'step' : 'steps'} this cycle.`,
      ],
    },
    {
      key: 'attribution',
      label: session.attributionDegraded ? 'User not recorded' : 'Users recorded',
      tone: session.attributionDegraded ? 'bad' : 'good',
      facts: [
        session.attributionDegraded
          ? 'A step ran with no user recorded. Check the coordinator log.'
          : 'Every command this night has a user recorded.',
      ],
    },
    {
      key: 'authorization',
      label: 'Last command',
      tone: session.authorization.state === 'recorded' ? 'pending' : 'unknown',
      facts: [
        session.authorization.state === 'recorded'
          ? `${session.authorization.command === undefined ? 'A command' : nightCommandLabel(session.authorization.command)} by ${session.authorization.principalName ?? 'an unknown user'}, ${formatClock(session.authorization.recordedAt) ?? 'at an unrecorded time'}.`
          : session.authorization.reason !== undefined && session.authorization.reason !== ''
            ? session.authorization.reason
            : 'No command is on record for this night.',
      ],
    },
    {
      key: 'definition',
      label: 'Definition',
      tone: 'pending',
      facts: [`${session.configObjectId}, version ${session.configRevision}.`],
    },
    {
      key: 'armed-show',
      label: session.armedShowId === '' ? 'No show ready' : session.showCommitted ? 'Show started' : 'Show ready',
      tone: session.armedShowId === '' ? 'unknown' : session.showCommitted ? 'good' : 'pending',
      facts: [
        session.armedShowId === ''
          ? 'No show is ready to start.'
          : session.showCommitted
            ? 'The show for this cycle has started.'
            : 'The show for this cycle is ready and has not started yet.',
      ],
    },
    {
      key: 'admission',
      label: session.admissionClosed ? 'Admission closed' : 'Admission open',
      tone: session.admissionClosed ? 'pending' : 'good',
      facts: [
        session.admissionClosed
          ? `Admission closed ${formatClock(session.admissionClosedAt) ?? 'at an unrecorded time'}.`
          : 'Admission is open.',
      ],
    },
    {
      key: 'updated',
      label: 'Session last updated',
      tone: 'pending',
      facts: [`${formatClock(session.updatedAt) ?? 'An unrecorded time'}.`],
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

/** A step's outcome or state in words. An unlisted value still reads as words. */
const STEP_STATE_WORDS: Record<string, string> = {
  confirmed: 'Confirmed',
  unconfirmed: 'Not confirmed',
  unconfirmable: 'Cannot confirm',
  failed: 'Failed',
  refused: 'Refused',
  ambiguous: 'Unclear',
  not_dispatched: 'Waiting',
  pending: 'Waiting',
  dispatched: 'Sent',
  resolved: 'Done',
}

function stepStateWords(value: string): string {
  return STEP_STATE_WORDS[value] ?? inWords(value)
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
      when: cue.dispatchedAt === null ? 'Waiting' : (formatClock(cue.dispatchedAt) ?? 'Sent'),
      name: cue.name,
      detail: cue.actionRevision === null ? inWords(cue.role) : `${inWords(cue.role)} · version ${cue.actionRevision}`,
      phase: CUE_PHASE[cue.phase],
      target: inWords(cue.role),
      action: cue.action,
      tone: outcome === undefined ? (cue.state === 'not_dispatched' ? 'pending' : 'warn') : (OUTCOME_TONE[outcome] ?? 'unknown'),
      state: stepStateWords(outcome ?? cue.state),
      resolved: cue.resolvedAt === null ? (cue.reason ?? null) : formatClock(cue.resolvedAt),
    }
  })
}

export type BackgroundAudioStepRow = {
  key: string
  when: string
  sequence: string
  cueName: string
  nodeId: string
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
      key: `${step.cueName}:${step.kind}:${step.nodeId}:${index}`,
      when: step.dispatchedAt === null ? 'Waiting' : (formatClock(step.dispatchedAt) ?? 'Sent'),
      sequence: inWords(step.sequence),
      cueName: step.cueName,
      nodeId: step.nodeId,
      kind: inWords(step.kind),
      detail: `version ${step.actionRevision}`,
      tone: outcome === undefined ? (step.state === 'pending' ? 'pending' : 'warn') : (OUTCOME_TONE[outcome] ?? 'unknown'),
      state: stepStateWords(outcome ?? step.state),
      resolved: step.resolvedAt === null ? (step.reason ?? null) : formatClock(step.resolvedAt),
    }
  })
}

const REPEAT_LABEL: Record<string, string> = { none: 'Plays once', item: 'Repeats the current item', playlist: 'Repeats the whole list' }
const RESUME_LABEL: Record<string, string> = { resume: 'Resumes where it left off', restart: 'Restarts from the first item' }

export type PlannedBackgroundAudio =
  | { kind: 'not_configured'; fact: string }
  | { kind: 'unavailable'; fact: string }
  | {
      kind: 'configured'
      source: string
      settings: { label: string; value: string }[]
      nodes: string
      items: { key: string; position: number; sequence: string; target: string; itemId: string }[]
    }

/** What the session's own pinned revision will play, so it reads the same before and after the bed starts. */
export function plannedBackgroundAudio(audio: NightBackgroundAudio): PlannedBackgroundAudio {
  const plan = audio.plan
  if (plan.state !== 'recorded') {
    return { kind: 'unavailable', fact: plan.reason !== '' ? plan.reason : 'The planned background audio could not be read. Reload the page.' }
  }
  if (!plan.configured) {
    return { kind: 'not_configured', fact: 'No background audio. Add it in the night definition.' }
  }
  const transition =
    plan.itemTransition === 'crossfade' && plan.crossfadeMs !== null
      ? `Crossfade over ${plan.crossfadeMs} ms`
      : plan.itemTransition === 'gapless'
        ? 'Gapless'
        : 'One after another'
  return {
    kind: 'configured',
    source: plan.mediaPlaylist !== '' ? `Media playlist ${plan.mediaPlaylist}` : 'Items listed on the night definition',
    settings: [
      { label: 'Repeat', value: REPEAT_LABEL[plan.repeat] ?? plan.repeat },
      { label: 'Resume', value: RESUME_LABEL[plan.resume] ?? plan.resume },
      { label: 'Transition', value: transition },
    ],
    nodes: plan.nodes.join(', '),
    items: plan.items.map((item) => ({ key: `${item.position}:${item.itemId}`, position: item.position, sequence: item.sequence, target: item.target, itemId: item.itemId })),
  }
}

export function nowPlaying(model: Model) {
  const instance = model.fpp[0]
  const run = model.currentRuns?.runs.find((entry) => entry.runner === 'fpp')
  const state = instance === undefined ? null : transportState(instance)
  return { instance, run, state }
}

/** The run-of-show strip's label when the cue record is not a list of cues. */
export const CUES_STATE_LABEL: Record<string, string> = {
  unknown: 'Unknown',
  not_configured: 'Not set up',
  not_available: 'Unavailable',
}

/** The one wording for a degraded night that reported no reason. */
export const DEGRADED_WITHOUT_REASON = 'The night stopped advancing. Run End session, then Prepare site.'

/** What a night command did, as separate messages. The UI never joins them. */
export type NightCommandOutcome = {
  tone: Tone
  label: string
  lines: string[]
  blocked: readonly { name: string; reason: string }[]
}
