/**
 * ADR-049 decision 10: `show.audioNodes`, the per-object `excludeNodes` on
 * cue audio and announcement outputs, the night bed background, and a
 * show.action audio target, plus `resolvedFrom` beside a reported resolved
 * target list. Hand-written because the coordinator half of this decision
 * (tracked separately from this UI seam) had not landed in api/openapi.yaml
 * or generated/schema.d.ts as of this writing. Once it does, replace each
 * type below with a plain `components['schemas'][...]` alias in domain.ts
 * and delete this file; the read/write helpers below stay useful only if
 * the landed shape differs from what is assumed here.
 */
import type {
  ConfigNightSessionBackgroundAudio,
  ConfigNightSessionBackgroundAudioInlineWrite,
  ConfigNightSessionBackgroundAudioReferenceWrite,
  ConfigShow,
  ConfigShowActionTarget,
  ConfigShowCueAnnouncementOutput,
  ConfigShowCueAudioOutput,
  ConfigShowWrite,
} from './domain'

/** Where a reported resolved target list came from (ADR-049 decision 10). */
export type ResolvedFrom = 'explicit' | 'show' | 'default'

export type ShowAudioNodesFields = { audioNodes?: string[] }
export type ConfigShowWithAudioNodes = ConfigShow & ShowAudioNodesFields
export type ConfigShowWriteWithAudioNodes = ConfigShowWrite & ShowAudioNodesFields

export type ExcludeNodesFields = { excludeNodes?: string[] }
export type ConfigShowCueAudioOutputWithExclude = ConfigShowCueAudioOutput & ExcludeNodesFields
export type ConfigShowCueAnnouncementOutputWithExclude = ConfigShowCueAnnouncementOutput & ExcludeNodesFields
export type ConfigNightSessionBackgroundAudioInlineWriteWithExclude = ConfigNightSessionBackgroundAudioInlineWrite & ExcludeNodesFields
export type ConfigNightSessionBackgroundAudioReferenceWriteWithExclude = ConfigNightSessionBackgroundAudioReferenceWrite & ExcludeNodesFields
export type ConfigShowActionTargetWithExclude = ConfigShowActionTarget & ExcludeNodesFields

/** `show.audioNodes`, or empty when the show carries none (absent is not yet configured, same absent/empty distinction fppInstances already uses). */
export function readShowAudioNodes(show: ConfigShow): string[] {
  return (show as ConfigShowWithAudioNodes).audioNodes ?? []
}

/** `outputs.audio.excludeNodes` or `outputs.announcement.excludeNodes`; only meaningful when the output carries no explicit `targets` of its own. */
export function readCueOutputExcludeNodes(output: ConfigShowCueAudioOutput | ConfigShowCueAnnouncementOutput | undefined): string[] {
  if (output === undefined) return []
  return (output as (ConfigShowCueAudioOutput | ConfigShowCueAnnouncementOutput) & ExcludeNodesFields).excludeNodes ?? []
}

/** The night bed's own `excludeNodes`, inline or reference form alike; only meaningful when the bed carries no explicit `targets` of its own. */
export function readBackgroundAudioExcludeNodes(bg: ConfigNightSessionBackgroundAudio | undefined): string[] {
  if (bg === undefined) return []
  return (bg as ConfigNightSessionBackgroundAudio & ExcludeNodesFields).excludeNodes ?? []
}

/** A show.action audio target's `excludeNodes`; only meaningful when the target carries no explicit `audioNodeId` list of its own. */
export function readActionTargetExcludeNodes(target: ConfigShowActionTarget): string[] {
  return (target as ConfigShowActionTargetWithExclude).excludeNodes ?? []
}
