import type { CurrentRun, MacroRunSummary, NightSessionState, Node } from '../api'
import { formatPositionTenths, nodePlaybackStatus } from './nodePlaybackModel'
import { nodeSyncStatus } from './nodeSyncModel'

export type OverviewTile = { value: string; detail: string }

export function lifecycleTile(session: NightSessionState | null, macroRuns: readonly MacroRunSummary[]): OverviewTile {
  const running = macroRuns.filter((run) => run.state === 'running')
  const macro =
    running.length === 0 ? 'No macro is running.' : `Running macro: ${running.map((run) => run.macroObjectId).join(', ')}.`
  if (session === null) return { value: 'Not heard from', detail: macro }
  return { value: session.stateLabel, detail: macro }
}

/** One tile for a runner's current runs: the first run's state, item and position. */
export function runnerTile(runs: readonly CurrentRun[], runner: CurrentRun['runner'], idle: string): OverviewTile {
  const own = runs.filter((run) => run.runner === runner)
  if (own.length === 0) return { value: 'Nothing playing', detail: idle }
  const first = own[0]!
  const parts = [first.playlistId, first.playback.itemId]
  if (first.playback.positionMs !== null) parts.push(formatPositionTenths(first.playback.positionMs))
  if (own.length > 1) parts.push(`${own.length - 1} more`)
  return { value: first.playback.state, detail: parts.filter((part) => part !== '').join(' · ') }
}

function audioNodes(nodes: readonly Node[]): Node[] {
  return nodes.filter((node) => node.audio.length > 0)
}

/** How many audio nodes report their sync as locked. */
export function audioRouteTile(nodes: readonly Node[]): OverviewTile {
  const audio = audioNodes(nodes)
  if (audio.length === 0) return { value: 'No audio nodes', detail: 'No node has reported audio yet.' }
  const locked = audio.filter((node) => {
    const line = nodeSyncStatus(node).syncLine
    return line.kind === 'value' && line.value.startsWith('Sync: Locked')
  }).length
  return { value: `${locked} / ${audio.length}`, detail: locked === audio.length ? 'all locked' : `${audio.length - locked} not locked` }
}

/** The first fresh LTC timecode any node reports. */
export function smpteTiles(nodes: readonly Node[], nowMs: number): { source: OverviewTile } {
  for (const node of audioNodes(nodes)) {
    const timecode = nodePlaybackStatus(node, nowMs).ltcTimecode
    if (timecode.kind !== 'value') continue
    return { source: { value: timecode.value.text, detail: `from ${node.label ?? node.nodeId}` } }
  }
  return {
    source: { value: 'No timecode', detail: 'No node is reporting a timecode.' },
  }
}
