import { PROBLEM_TYPE } from '../api'
import type {
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
import { EVIDENCE_TONE } from '../domain/evidence'
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

export function transportState(instance: FPPInstance): TransportState {
  const obs = instance.observations
  return {
    playlist: stringValue(obs, 'fpp.playlist.name'),
    itemIndex: numberValue(obs, 'fpp.playlist.index'),
    itemCount: numberValue(obs, 'fpp.playlist.count'),
    media: stringValue(obs, 'fpp.media.filename') ?? stringValue(obs, 'fpp.sequence.name'),
    playerState: stringValue(obs, 'fpp.status.player_state'),
    elapsedSeconds: numberValue(obs, 'fpp.position.elapsed.seconds'),
    totalSeconds: numberValue(obs, 'fpp.position.seconds'),
    volume: numberValue(obs, 'fpp.volume'),
  }
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
 * `audioSessionId` target. Never a fake picker (SM-269 design section 3).
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

/** Derive, don't ask (guide §7): observed desired revision plus one, or 1 for a session never observed. */
export function deriveAudioSessionRevision(
  observations: readonly ObservationEntry[],
  sessionId: string,
): { next: number; observed: number | null } {
  const entry = observations.find(
    (candidate) =>
      candidate.resource.kind === 'audio_session' &&
      candidate.resource.id === sessionId &&
      candidate.signal === AUDIO_SESSION_DESIRED_REVISION_SIGNAL,
  )
  const observed = typeof entry?.value === 'number' ? entry.value : null
  return { next: observed === null ? 1 : observed + 1, observed }
}

const AUDIO_SESSION_GOOD_OUTCOMES = new Set(['started', 'position', 'stopped', 'completed'])

/**
 * `outcome: "unconfirmable"` is a real, expected outcome while the
 * shipped agent's session engine has no working pipeline backend — warn,
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
