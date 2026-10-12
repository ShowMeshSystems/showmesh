import { useContext } from 'react'
import type { AudioNodeSummary, Node } from '../api'
import { ModelContext } from '../app/ModelContext'

/** The node's short name for a route, from its advertised outputs. Undefined when it reports none. */
export function routeShortName(node: Node | undefined, route: string): string | undefined {
  const attributes = node?.capabilities.find((c) => c.id === 'audio.output.local')?.attributes as Record<string, unknown> | undefined
  const outputs = attributes?.['outputs']
  if (!Array.isArray(outputs)) return undefined
  for (const output of outputs as unknown[]) {
    if (typeof output !== 'object' || output === null) continue
    const entry = output as Record<string, unknown>
    if (entry['route'] === route && typeof entry['shortName'] === 'string' && entry['shortName'] !== '') return entry['shortName']
  }
  return undefined
}

/** "ch 1-2" for a run of adjacent channels, "ch 1, 3" otherwise. */
export function channelText(channels: readonly number[]): string {
  const first = channels[0]
  const last = channels[channels.length - 1]
  if (first === undefined || last === undefined) return ''
  if (channels.length === 1) return `ch ${first}`
  const adjacent = channels.every((channel, index) => channel === first + index)
  return adjacent ? `ch ${first}-${last}` : `ch ${channels.join(', ')}`
}

/** The short name, or the full route unchanged when the node reports none. */
export function routeName(route: string, shortName: string | undefined): string {
  return shortName === undefined || shortName === '' ? route : shortName
}

/** "M4 ch 1-2" when the node reports a short name, otherwise the full route unchanged. */
export function audioNodeRouteText(summary: AudioNodeSummary, fleet: readonly Node[]): string {
  const shortName = routeShortName(fleet.find((node) => node.nodeId === summary.id), summary.label)
  if (shortName === undefined) return summary.label
  return summary.programChannels.length === 0 ? shortName : `${shortName} ${channelText(summary.programChannels)}`
}

/** An audio node id with its route text, collapsed to the bare id when the route adds nothing. */
export function audioNodeIdentityText(summary: AudioNodeSummary, fleet: readonly Node[]): string {
  return hasRouteLabel(summary) ? `${summary.id} · ${audioNodeRouteText(summary, fleet)}` : summary.id
}

/** The fleet snapshot, or none outside a model provider. */
export function useFleetNodes(): readonly Node[] {
  return useContext(ModelContext)?.nodes ?? []
}

/** Whether the route says more than the node id already does. */
export function hasRouteLabel(summary: AudioNodeSummary): boolean {
  return summary.label !== '' && summary.label !== summary.id
}
