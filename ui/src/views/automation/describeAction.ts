import type { ActionIntegration, ConfigShowAction } from '../../app/types'

export const INTEGRATION_LABEL: Record<ActionIntegration, string> = {
  fpp: 'FPP',
  mqtt: 'MQTT',
  resolume: 'Resolume',
  audio: 'Audio',
}

/**
 * One line of mono detail under an action's name, e.g.
 * "startPlaylist · barn-player · WR26 Preshow" or
 * "audio.gain.fade · barn-player · -12 dB". Every field here is read
 * straight off `ConfigShowActionTarget` -- never invented (design guide
 * §7): a field this coordinator does not return is simply left out of
 * the line rather than guessed.
 */
export function describeActionTarget(target: ConfigShowAction['target']): string {
  const parts: string[] = []
  if (target.integration === 'fpp') {
    if (target.primitive !== undefined) parts.push(target.primitive)
    if (target.instanceId !== undefined) parts.push(target.instanceId)
    const params = target.params
    if (params !== undefined) {
      const playlist = params.playlist ?? params.playlistName
      if (typeof playlist === 'string') parts.push(playlist)
    }
  } else if (target.integration === 'mqtt') {
    if (target.publish !== undefined) parts.push(target.publish.topic)
    if (target.publish !== undefined) parts.push(`qos ${target.publish.qos}`)
    if (target.expect !== undefined) {
      parts.push(target.expect.kind === 'none' ? 'expect none' : `expect ${target.expect.kind}`)
      if (target.expect.kind !== 'none' && target.expect.deadlineSeconds !== undefined) {
        parts.push(`${target.expect.deadlineSeconds} s`)
      }
    }
  } else if (target.integration === 'resolume') {
    if (target.action !== undefined) parts.push(target.action)
    if (target.ref !== undefined) {
      for (const [key, value] of Object.entries(target.ref)) {
        if (typeof value === 'string' || typeof value === 'number' || typeof value === 'boolean') {
          parts.push(`${key} ${String(value)}`)
        }
      }
    }
  } else if (target.integration === 'audio') {
    if (target.audioAction !== undefined) parts.push(target.audioAction)
    if (target.audioNodeId !== undefined) parts.push(target.audioNodeId)
    const params = target.params
    if (params !== undefined) {
      const gainDb = params.gainDb ?? params.targetGainDb
      if (typeof gainDb === 'number') parts.push(`${gainDb} dB`)
    }
  }
  return parts.join(' · ')
}

/** Never confirms, by design (idea 3): an mqtt action whose expect.kind is "none". */
export function isUnconfirmableByDesign(target: ConfigShowAction['target']): boolean {
  return target.integration === 'mqtt' && target.expect?.kind === 'none'
}
