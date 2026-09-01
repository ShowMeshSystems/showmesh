import { PROBLEM_TYPE } from '../api'
import type { CurrentRun, Evidence, FPPCommandResult, FPPInstance, Model, Node } from '../api'
import type { Tone } from '../kit'
import { EVIDENCE_TONE } from '../domain/evidence'
import { ageMs, formatClock, formatDuration } from '../domain/time'

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
