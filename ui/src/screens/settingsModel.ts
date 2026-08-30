/**
 * Pure derivations for the Settings screen's seven tabs. No React, no
 * fetching: every function here takes already-loaded data and returns a
 * fact or a verdict, so each is testable without a coordinator.
 */
import type { FPPInstance, Node, NightSessionState, ResolumeInstance } from '../api'
import type { Tone } from '../kit'

/** Mirrors monitorModel.ts's identical HEALTH_TONE map, kept as its own copy: settingsModel has no dependency on monitorModel. */
export const HEALTH_TONE: Record<string, Tone> = {
  healthy: 'good',
  degraded: 'warn',
  failed: 'bad',
  unknown: 'unknown',
  suppressed: 'pending',
}

const AUDIO_OUTPUT_LOCAL = 'audio.output.local'
const AUDIO_OUTPUT_LTC = 'audio.output.ltc'

export function fppHealthFor(fpp: readonly FPPInstance[], id: string): FPPInstance | null {
  return fpp.find((instance) => instance.instanceId === id) ?? null
}

export function resolumeHealthFor(resolume: readonly ResolumeInstance[], id: string): ResolumeInstance | null {
  return resolume.find((instance) => instance.instanceId === id) ?? null
}

/** A node advertising neither audio capability is listed and marked, never hidden (guide §9 item 6). */
export function hasAudioCapability(node: Node): boolean {
  return node.capabilities.some((c) => c.id === AUDIO_OUTPUT_LOCAL || c.id === AUDIO_OUTPUT_LTC)
}

/** Mirrors the coordinator's own capabilityRoutesAttribute (audionode.go): a missing or malformed value is null, never a partial list. */
function routesAttribute(attributes: Record<string, unknown> | undefined): string[] | null {
  if (attributes === undefined) return null
  const raw = attributes['routes']
  if (!Array.isArray(raw)) return null
  const out: string[] = []
  for (const item of raw) {
    if (typeof item !== 'string') return null
    out.push(item)
  }
  return out
}

/** The routes a node has advertised for one of the two audio capabilities, or null when it has advertised none. */
export function advertisedRoutes(node: Node, capabilityId: 'audio.output.local' | 'audio.output.ltc'): string[] | null {
  const capability = node.capabilities.find((c) => c.id === capabilityId)
  if (capability === undefined) return null
  return routesAttribute(capability.attributes as Record<string, unknown> | undefined)
}

export type AudioNodeVerdict = { ok: true } | { ok: false; reason: string }

/**
 * The three save-time refusals `PUT /config/audio.node/{id}` names.
 * Built to `internal/coordinator/config/audionode.go`, not the
 * openapi.yaml Problem text, which names the route-mismatch condition
 * backwards: the code refuses `ltcRoute` differing from `programRoute`.
 */
export function audioNodeVerdict(payload: {
  programRoute: string
  programChannels: number[]
  ltcRoute: string
  ltcChannel: string
}): AudioNodeVerdict {
  const seen = new Set<number>()
  for (const raw of payload.programChannels) {
    if (seen.has(raw)) {
      return { ok: false, reason: `Channel ${raw} appears more than once in program channels.` }
    }
    seen.add(raw)
  }
  const hasLtcRoute = payload.ltcRoute.trim() !== ''
  const hasLtcChannel = payload.ltcChannel.trim() !== ''
  if (hasLtcRoute !== hasLtcChannel) {
    return { ok: false, reason: 'LTC route and LTC channel must be given together, or both left blank for a program-only node.' }
  }
  if (hasLtcRoute && payload.programRoute.trim() !== payload.ltcRoute.trim()) {
    return {
      ok: false,
      reason: 'Program and LTC leave through one interface in one clock domain. LTC route must name the same route as program route.',
    }
  }
  if (hasLtcChannel) {
    const ltc = Number(payload.ltcChannel)
    if (seen.has(ltc)) {
      return { ok: false, reason: `LTC channel ${ltc} is already claimed by program channels; LTC must be on a channel discrete from program.` }
    }
  }
  return { ok: true }
}

/** The session's currently live cycle, or null when no session reports one. Mode's in-progress warning renders only for this case. */
export function liveCycle(nightSession: NightSessionState | null): { cycle: number } | null {
  if (nightSession === null || nightSession.state !== 'live') return null
  return { cycle: nightSession.cycle }
}
