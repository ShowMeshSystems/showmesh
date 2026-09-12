import { PROBLEM_TYPE } from '../api'
import type {
  AlignedAudioStartResult,
  AudioSessionCommandResult,
  CurrentRun,
  Evidence,
  FPPCommandResult,
  FPPInstance,
  FPPPlaylistDefinitionMetadata,
  Model,
  NightCommandName,
  Node,
  ObservationEntry,
} from '../api'
import type { LifecycleCommandGroup, LifecycleCommandSpec, Tone } from '../kit'
import { EVIDENCE_LABEL, EVIDENCE_TONE } from '../domain/evidence'
import { ageMs, formatClock, formatDuration } from '../domain/time'

/** Gate shape shared by every night-lifecycle command surface. */
export type NightGate = { allowed: true } | { allowed: false; reason: string }

type NightCommandTuple = readonly [NightCommandName, string, string]

/**
 * The night lifecycle's one canonical group layout: Prepare, Start, End the
 * night. Show Night and Live Control both build their `LifecycleCommands`
 * groups from this so the two screens render one identical element.
 */
export const NIGHT_LIFECYCLE_GROUPS: readonly { id: string; title: string; commands: readonly NightCommandTuple[] }[] = [
  {
    id: 'lc-prep',
    title: 'Prepare',
    commands: [
      ['prepare-site', 'Prepare site', 'Opens a preparation epoch. Readiness and start-preshow both need one.'],
      ['run-readiness', 'Run readiness', 'Re-runs every readiness check against this epoch.'],
    ],
  },
  {
    id: 'lc-start',
    title: 'Start',
    commands: [
      ['start-preshow', 'Start preshow', 'Enters preshow from a prepared, ready session.'],
      ['start-night', 'Start night', 'Commits the armed show and starts the first cycle.'],
    ],
  },
  {
    id: 'lc-end',
    title: 'End the night',
    commands: [
      ['request-final-show', 'Request final show', 'Closes admission. The next normally timed show becomes the last.'],
      ['fade-out-night', 'Fade out night', 'Arriving mid-show makes this show final and the fade waits for it to finish.'],
      ['power-down-presentation', 'Power down presentation', 'The terminal intent. An interlock can withhold it.'],
      ['end-session', 'End session', 'Abandons the session. Never withheld by an interlock; prepare-site then starts a fresh one.'],
    ],
  },
]

/** Turns a `[command, label, detail]` tuple into a `LifecycleCommands` spec, gated the same way every night command is. */
export function nightCommandSpec(gate: NightGate, onRun: (command: NightCommandName) => void) {
  return ([command, label, detail]: NightCommandTuple): LifecycleCommandSpec => ({
    command,
    label,
    detail,
    disabled: !gate.allowed,
    disabledReason: gate.allowed ? undefined : gate.reason,
    onRun: () => onRun(command),
  })
}

/** Builds the shared `LifecycleCommands` groups; `startNightOptions` renders
 *  only in the start-night cell (the late-start checkbox). */
export function nightLifecycleGroups(
  gate: NightGate,
  onRun: (command: NightCommandName) => void,
  startNightOptions?: LifecycleCommandSpec['options'],
): LifecycleCommandGroup[] {
  const spec = nightCommandSpec(gate, onRun)
  return NIGHT_LIFECYCLE_GROUPS.map((group) => ({
    id: group.id,
    title: group.title,
    commands: group.commands.map((tuple) => {
      const built = spec(tuple)
      return tuple[0] === 'start-night' && startNightOptions !== undefined ? { ...built, options: startNightOptions } : built
    }),
  }))
}

export function findSignal(observations: readonly Evidence[], signal: string): Evidence | undefined {
  return observations.find((entry) => entry.signal === signal)
}

function stringValue(observations: readonly Evidence[], signal: string): string | null {
  const found = findSignal(observations, signal)
  if (found === undefined || found.value === null || typeof found.value === 'boolean') return null
  return String(found.value)
}

function numberValue(observations: readonly Evidence[], signal: string): number | null {
  const found = findSignal(observations, signal)
  return typeof found?.value === 'number' ? found.value : null
}

/** mm:ss, the transport's own voice. Never a bare second count. */
export function formatPosition(seconds: number | null): string | null {
  if (seconds === null || seconds < 0) return null
  const whole = Math.floor(seconds)
  return `${Math.floor(whole / 60)}:${String(whole % 60).padStart(2, '0')}`
}

export type TransportState = {
  playlist: string | null
  itemIndex: number | null
  itemCount: number | null
  media: string | null
  playerState: string | null
  elapsedSeconds: number | null
  totalSeconds: number | null
  volume: number | null
}

/** The playlist name FPP reports playing, or null when none is reported or it reported empty. */
export function reportedPlaylistName(state: TransportState): string | null {
  return state.playlist === null || state.playlist === '' ? null : state.playlist
}

/** The names FPP has ever imported for one instance, deduplicated and sorted. Empty when the coordinator has stored none. */
export function fppPlaylistNames(definitions: readonly FPPPlaylistDefinitionMetadata[], instanceUuid: string): string[] {
  const names = new Set<string>()
  for (const definition of definitions) {
    if (definition.instanceUuid === instanceUuid) names.add(definition.playlistName)
  }
  return Array.from(names).sort((a, b) => a.localeCompare(b))
}

export type FPPPosition = { elapsedSeconds: number; totalSeconds: number }

/**
 * `fpp.position.seconds` is FPP's own `seconds_played`, not a duration -
 * on the very first playing poll it equals elapsed, which is why total
 * must be built as elapsed + `fpp.position.remaining.seconds`
 * (`seconds_remaining`) instead. Null whenever either reading is absent
 * or not current, so a stale pair never renders a frozen position.
 */
export function fppPosition(observations: readonly Evidence[]): FPPPosition | null {
  const elapsed = findSignal(observations, 'fpp.position.elapsed.seconds')
  const remaining = findSignal(observations, 'fpp.position.remaining.seconds')
  if (elapsed === undefined || remaining === undefined || typeof elapsed.value !== 'number' || typeof remaining.value !== 'number') {
    return null
  }
  if (elapsed.state !== 'current' || remaining.state !== 'current') return null
  return { elapsedSeconds: elapsed.value, totalSeconds: elapsed.value + remaining.value }
}

export function transportState(instance: FPPInstance): TransportState {
  const obs = instance.observations
  return {
    playlist: stringValue(obs, 'fpp.playlist.name'),
    itemIndex: numberValue(obs, 'fpp.playlist.index'),
    itemCount: numberValue(obs, 'fpp.playlist.count'),
    media: stringValue(obs, 'fpp.media.filename') ?? stringValue(obs, 'fpp.sequence.name'),
    playerState: stringValue(obs, 'fpp.status.player_state'),
    elapsedSeconds: numberValue(obs, 'fpp.position.elapsed.seconds'),
    totalSeconds: fppPosition(obs)?.totalSeconds ?? null,
    volume: numberValue(obs, 'fpp.volume'),
  }
}

/**
 * The fraction of the current FPP item elapsed, or null when the
 * position (see [fppPosition]) is absent or not current.
 */
export function fppElapsedFraction(instance: FPPInstance | undefined): number | null {
  if (instance === undefined) return null
  const position = fppPosition(instance.observations)
  if (position === null || position.totalSeconds <= 0) return null
  return Math.min(1, Math.max(0, position.elapsedSeconds / position.totalSeconds))
}

/**
 * A command is not successful because it was sent. `outcome` is the
 * coordinator's own confirmation from observed evidence; `unconfirmed`
 * means it was dispatched and nothing has yet proved it took effect.
 */
export type CommandOutcome = {
  tone: Tone
  label: string
  detail: string
}

/**
 * `startPlaylist`'s `ifBusy: "refuse"` guard (the default) produces two
 * distinct 409 `type`s (api/openapi.yaml's own StartPlaylistCommandRequest):
 * a different playlist confirmed playing, or the evidence needed to tell
 * that not being current. Branch on the wire `type`, never on `detail`
 * prose, so an unrecognized 409 never gets mislabeled as either case.
 */
export type StartPlaylistConflictReason = 'differentPlaylistPlaying' | 'evidenceNotCurrent' | 'unknown'

export function classifyStartPlaylistConflict(problemType: string | undefined): StartPlaylistConflictReason {
  if (problemType === PROBLEM_TYPE.fppStartPlaylistEvidenceNotCurrent) return 'evidenceNotCurrent'
  if (problemType === PROBLEM_TYPE.fppStartPlaylistBusy) return 'differentPlaylistPlaying'
  return 'unknown'
}

export function describeFPPOutcome(result: FPPCommandResult, action: string): CommandOutcome {
  const dispatched = formatClock(result.dispatchedAt)
  const resolved = formatClock(result.resolvedAt)
  const sent = dispatched === null ? 'sent' : `accepted ${dispatched}`
  if (result.outcome === 'confirmed') {
    return {
      tone: 'good',
      label: 'Last command',
      detail: `${action} ${sent}${resolved === null ? '' : `, confirmed by observed evidence ${resolved}`}. ${result.outcomeReason}`.trim(),
    }
  }
  if (result.outcome === 'unconfirmed') {
    return {
      tone: 'warn',
      label: 'Not confirmed',
      detail: `${action} ${sent}. Nothing has yet reported that it took effect. ${result.outcomeReason}`.trim(),
    }
  }
  return {
    tone: 'unknown',
    label: 'Outcome unknown',
    detail: `${action} ${sent}. ${result.outcomeReason || 'The coordinator recorded no outcome for this command.'}`,
  }
}

export type OutputRow = {
  key: string
  name: string
  where: string
  doing: string
  content: string | null
  tone: Tone
  evidence: string
  confirmed: boolean
}

const OUTPUT_CAPABILITY: Record<string, string> = {
  'transport.ndi.send': 'NDI',
  'display.hdmi': 'HDMI',
  'matrix.render': 'matrix',
  'audio.output.local': 'local audio',
  'audio.output.ltc': 'LTC',
}

function outputKind(node: Node): string {
  const named = node.capabilities.map((capability) => OUTPUT_CAPABILITY[capability.id]).filter((name): name is string => name !== undefined)
  return named.length === 0 ? 'output not advertised' : named.join(' · ')
}

/**
 * One row per surface this fleet is actually reporting on, from the
 * render observations themselves. A surface nothing has reported is not
 * invented here: it simply has no row, and the count below the table says so.
 */
export function outputRows(model: Model, nowIso: string | null): OutputRow[] {
  const rows: OutputRow[] = []
  for (const node of model.nodes) {
    const bySurface = new Map<string, Evidence[]>()
    for (const entry of node.render) {
      // A render observation names the surface it is about. Anything else
      // in this array is about the node, and has no row in this table.
      if (entry.resource.kind !== 'surface') continue
      const id = entry.resource.id
      const list = bySurface.get(id) ?? []
      list.push(entry as unknown as Evidence)
      bySurface.set(id, list)
    }
    for (const [surfaceId, observations] of bySurface) {
      const state = findSignal(observations, 'surface.pipeline.state')
      const rate = numberValue(observations, 'surface.frames.rate')
      const content = stringValue(observations, 'surface.content.fseq_filename') ?? stringValue(observations, 'surface.content.cue_id')
      const freshest = observations.reduce<Evidence | undefined>(
        (best, entry) => (best === undefined || (entry.observedAt ?? '') > (best.observedAt ?? '') ? entry : best),
        undefined,
      )
      const age = ageMs(freshest?.observedAt ?? null, nowIso)
      const evidenceState = freshest?.state ?? 'not_collected'
      const stale = evidenceState !== 'current'
      rows.push({
        key: `${node.nodeId}:${surfaceId}`,
        name: surfaceId,
        where: `${node.nodeId} · ${outputKind(node)}`,
        doing:
          state === undefined || state.value === null
            ? 'Unknown'
            : `${String(state.value)}${rate === null ? '' : ` at ${rate} fps`}`,
        content,
        tone: EVIDENCE_TONE[evidenceState],
        evidence:
          age === null
            ? evidenceState.replace('_', ' ')
            : stale
              ? `${evidenceState.replace('_', ' ')} ${formatDuration(age)}`
              : `${formatDuration(age)} ago`,
        confirmed: evidenceState === 'current',
      })
    }
  }
  return rows
}

/** Audio rows come from the runner's own current run, not from a guess. */
export function audioRows(model: Model, nowIso: string | null): OutputRow[] {
  const runs = model.currentRuns?.runs ?? []
  return runs
    .filter((run: CurrentRun) => run.runner === 'showmesh-audio')
    .map((run) => {
      const age = ageMs(run.freshness.observedAt, nowIso)
      const confirmed = run.freshness.state === 'current'
      return {
        key: `audio:${run.id}`,
        name: 'Program audio',
        where: `${run.playlistId} · ${run.runner}`,
        doing: `${run.playback.state}${run.playback.positionMs === null ? '' : ` at ${formatPosition(run.playback.positionMs / 1000) ?? ''}`}`,
        content: run.playback.media !== '' ? run.playback.media : null,
        tone: EVIDENCE_TONE[run.freshness.state as keyof typeof EVIDENCE_TONE] ?? 'unknown',
        evidence: age === null ? run.freshness.state : confirmed ? `${formatDuration(age)} ago` : `${run.freshness.state} ${formatDuration(age)}`,
        confirmed,
      }
    })
}

/**
 * `audioRows` falls back to an empty array whenever `currentRuns` is null,
 * which reads identically whether the coordinator has never answered yet or
 * cannot answer at all (an older coordinator serving no `GET /current-runs`).
 * Those are different facts; this tells them apart so the caller can say
 * which one it is instead of silently dropping the audio evidence.
 */
export function currentRunsAbsence(model: Model): 'loading' | 'unavailable' | null {
  if (model.currentRuns !== null) return null
  return model.currentRunsFetchFailed ? 'unavailable' : 'loading'
}

/**
 * A real source of a session id, either an observed `audio_session`
 * resource this coordinator has seen, or an authored show.action's own
 * `audioSessionId` target. Never a fake picker.
 */
export type AudioSessionOption = { sessionId: string; origin: string }

export function audioSessionOptions(
  observations: readonly ObservationEntry[],
  actions: readonly { id: string; label: string; audioSessionId: string }[],
): AudioSessionOption[] {
  const origins = new Map<string, string>()
  for (const entry of observations) {
    if (entry.resource.kind !== 'audio_session') continue
    if (!origins.has(entry.resource.id)) origins.set(entry.resource.id, 'observed')
  }
  for (const action of actions) {
    if (!origins.has(action.audioSessionId)) {
      origins.set(action.audioSessionId, `from action ${action.label !== '' ? action.label : action.id}`)
    }
  }
  return [...origins.entries()].map(([sessionId, origin]) => ({ sessionId, origin }))
}

const AUDIO_SESSION_DESIRED_REVISION_SIGNAL = 'audio_session.desired_revision'

/**
 * A cue-activation session's `desired_revision` is UnixNano-scale (see
 * pkg/cueactivation.AudioSessionRevision) and routinely exceeds
 * Number.MAX_SAFE_INTEGER, so the client's own JSON parser (bigint.ts)
 * carries it as an exact decimal string rather than a rounded `number`.
 * This turns either shape into an exact `bigint`, never losing a digit.
 */
export function exactAudioSessionRevisionValue(value: unknown): bigint | null {
  if (typeof value === 'number' && Number.isInteger(value)) return BigInt(value)
  if (typeof value === 'string' && /^-?\d+$/.test(value)) return BigInt(value)
  return null
}

/** Derive, don't ask (guide §7): observed desired revision plus one, or 1 for a session never observed. */
export function deriveAudioSessionRevision(
  observations: readonly ObservationEntry[],
  sessionId: string,
): { next: bigint; observed: bigint | null } {
  const entry = observations.find(
    (candidate) =>
      candidate.resource.kind === 'audio_session' &&
      candidate.resource.id === sessionId &&
      candidate.signal === AUDIO_SESSION_DESIRED_REVISION_SIGNAL,
  )
  const observed = entry === undefined ? null : exactAudioSessionRevisionValue(entry.value)
  return { next: observed === null ? 1n : observed + 1n, observed }
}

/**
 * A typed revision override, or `null` for anything that is not a plain
 * non-negative decimal integer. api/openapi.yaml declares `revision`
 * minimum 0 for every audio session request body, so a leading minus is
 * rejected here rather than dispatched.
 */
export function parseExactRevisionInput(text: string): bigint | null {
  const trimmed = text.trim()
  if (!/^\d+$/.test(trimmed)) return null
  try {
    return BigInt(trimmed)
  } catch {
    return null
  }
}

/**
 * Every known session's observed `audio_session.state` (and position,
 * where reported) so Live Control can say what is playing before the
 * operator has picked or typed a session id. A session with no observed
 * state reads as unobserved, never as a fabricated "stopped".
 */
export type AudioSessionSummary = {
  sessionId: string
  origin: string
  tone: Tone
  stateLabel: string
  positionLabel: string | null
}

export function audioSessionSummaries(
  observations: readonly ObservationEntry[],
  actions: readonly { id: string; label: string; audioSessionId: string }[],
  nowIso: string | null,
): AudioSessionSummary[] {
  return audioSessionOptions(observations, actions).map((option) => {
    const stateEntry = findAudioSessionSignal(observations, option.sessionId, 'audio_session.state')
    const positionEntry = findAudioSessionSignal(observations, option.sessionId, 'audio_session.position_ms')
    const evidenceState = stateEntry?.state ?? 'not_collected'
    const stateValue =
      stateEntry !== undefined && stateEntry.value !== null && typeof stateEntry.value !== 'boolean'
        ? String(stateEntry.value)
        : null
    const age = ageMs(stateEntry?.observedAt ?? null, nowIso)
    const stateLabel =
      stateValue === null
        ? EVIDENCE_LABEL.not_collected
        : evidenceState === 'current'
          ? stateValue
          : `${stateValue} (${EVIDENCE_LABEL[evidenceState].toLowerCase()}${age === null ? '' : `, ${formatDuration(age)} ago`})`
    const positionMs = typeof positionEntry?.value === 'number' ? positionEntry.value : null
    return {
      sessionId: option.sessionId,
      origin: option.origin,
      tone: stateValue === null ? EVIDENCE_TONE.not_collected : EVIDENCE_TONE[evidenceState],
      stateLabel,
      positionLabel: positionMs === null || positionEntry?.state !== 'current' ? null : formatPosition(positionMs / 1000),
    }
  })
}

function findAudioSessionSignal(
  observations: readonly ObservationEntry[],
  sessionId: string,
  signal: string,
): ObservationEntry | undefined {
  return observations.find(
    (candidate) => candidate.resource.kind === 'audio_session' && candidate.resource.id === sessionId && candidate.signal === signal,
  )
}

const AUDIO_SESSION_GOOD_OUTCOMES = new Set(['started', 'position', 'stopped', 'completed'])

/**
 * `outcome: "unconfirmable"` is a real, expected outcome whenever the
 * node's own evidence does not corroborate a dispatched command, warn,
 * never bad, per the API's own AudioSessionCommandResult description.
 */
export function describeAudioSessionOutcome(result: AudioSessionCommandResult, action: string): CommandOutcome {
  const replaySuffix = result.replay ? ' This response reuses the original dispatch; nothing was re-sent.' : ''
  const attributionSuffix = result.attributionDegraded
    ? ' Attribution is degraded because the audit record could not be written.'
    : ''
  if (result.outcome === '') {
    return {
      tone: 'pending',
      label: 'Pending',
      detail: `${action}: replayed before the original request resolved.${attributionSuffix}`,
    }
  }
  if (AUDIO_SESSION_GOOD_OUTCOMES.has(result.outcome)) {
    return {
      tone: 'good',
      label: result.outcome.charAt(0).toUpperCase() + result.outcome.slice(1),
      detail: `${action} dispatched.${replaySuffix}${attributionSuffix}`,
    }
  }
  if (result.outcome === 'unconfirmable') {
    return {
      tone: 'warn',
      label: 'Dispatched',
      detail: `${action}: dispatched. The node's session engine cannot corroborate it. That is expected on this build, not a transport failure. ${result.reason}`.trim() + replaySuffix + attributionSuffix,
    }
  }
  return {
    tone: 'bad',
    label: 'Refused',
    detail: `${action}: ${result.reason}`.trim() + replaySuffix + attributionSuffix,
  }
}

/**
 * A typed scheduled-start instant, or `null` for anything that is not a
 * plain non-negative decimal integer.
 *
 * A `bigint`, and never a `number`: this is `T0` in NANOSECONDS on the
 * target node's own media clock (api/openapi.yaml,
 * AudioSessionStartParams). Nanoseconds since an epoch are around 1.79e18
 * and `Number.MAX_SAFE_INTEGER` is 9.007e15, so a `number` would round
 * the operator's instant before it reached the wire. Milliseconds are not
 * an alternative unit here: 1 ms is 48 samples at 48 kHz.
 */
export function parseScheduledAtNsInput(text: string): bigint | null {
  return parseExactRevisionInput(text)
}

/** One node.audio.timeline.* signal and the label Live Control shows it under. */
export type AudioTimelineRow = {
  signal: string
  label: string
  /** The reported value, already rendered; `null` when the signal carries none. */
  value: string | null
  /** Why there is no value, straight from the coordinator; never invented here. */
  reason: string | null
  state: string
}

/**
 * RES-019 section 10's six timeline signals, in the order they read as a
 * story: what was asked for, where the node should be, where it is, and
 * how far apart those are, then the resync history.
 */
const AUDIO_TIMELINE_SIGNALS: readonly { signal: string; label: string }[] = [
  { signal: 'node.audio.timeline.scheduled_at', label: 'Scheduled at (ns)' },
  { signal: 'node.audio.timeline.expected_ms', label: 'Expected (ms)' },
  { signal: 'node.audio.timeline.actual_ms', label: 'Actual (ms)' },
  { signal: 'node.audio.timeline.error_ms', label: 'Error (ms)' },
  { signal: 'node.audio.timeline.resyncs', label: 'Resyncs' },
  { signal: 'node.audio.timeline.last_resync_reason', label: 'Last resync' },
]

/**
 * The six timeline rows for `nodeId`, in AUDIO_TIMELINE_SIGNALS' order.
 *
 * A signal this coordinator holds no observation for at all is reported
 * as absent with that stated, not omitted from the list and not shown as
 * a zero: an operator asking why a node is not aligned needs to see the
 * difference between "no timeline" and "a timeline reading zero".
 *
 * `scheduled_at` arrives as a decimal STRING rather than a number when it
 * exceeds `Number.MAX_SAFE_INTEGER`, because the API client parses
 * responses through `parseJsonPreservingBigInts`. Both spellings render
 * the same way here, which is the point: the digits are never rounded on
 * the way to the screen.
 */
export function audioTimelineRows(observations: ObservationEntry[], nodeId: string): AudioTimelineRow[] {
  return AUDIO_TIMELINE_SIGNALS.map(({ signal, label }) => {
    const entry = observations.find(
      (candidate) =>
        candidate.resource.kind === 'node' && candidate.resource.id === nodeId && candidate.signal === signal,
    )
    if (entry === undefined) {
      return { signal, label, value: null, reason: 'This coordinator holds no observation for this signal.', state: 'unknown' }
    }
    return {
      signal,
      label,
      value: entry.value === null ? null : String(entry.value),
      reason: entry.reason,
      state: entry.state,
    }
  })
}

/**
 * The outcome line for an aligned multi-node start.
 *
 * `aligned` false is deliberately NOT reported as good and NOT as a
 * failure. No usable media-clock reading existed, so every node started on
 * arrival exactly as it always has, which is the documented behaviour for
 * a node without a locked clock. An operator who asked for an aligned
 * start and was shown a plain success would have been told something
 * false, so it reads as a warning carrying the coordinator's own reason.
 */
export function describeAlignedStart(result: AlignedAudioStartResult): CommandOutcome {
  const worst = [...result.prepares, ...result.starts].find(
    (r) => !AUDIO_SESSION_GOOD_OUTCOMES.has(r.outcome) && r.outcome !== 'unconfirmable',
  )
  if (worst !== undefined) {
    return {
      tone: 'bad',
      label: 'Refused',
      detail: `Aligned start: ${worst.nodeId} reported ${worst.outcome}${worst.reason === '' ? '' : ` (${worst.reason})`}.`,
    }
  }
  if (!result.aligned || result.selection === null) {
    return {
      tone: 'warn',
      label: 'Not aligned',
      detail: `Aligned start: ${result.unalignedReason}`,
    }
  }
  const nodes = result.starts.length
  return {
    tone: 'good',
    label: 'Aligned',
    detail:
      `Aligned start: ${nodes} node${nodes === 1 ? '' : 's'} scheduled at ${String(result.selection.scheduledAtNs)} ns ` +
      `on ${result.selection.clockNodeId}'s media clock` +
      (result.selection.clockErrorBoundKnown
        ? '.'
        : '. The clock stated no error bound, so the lead carries no allowance for clock uncertainty.'),
  }
}
