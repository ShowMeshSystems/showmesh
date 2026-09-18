/**
 * ADR-049 decision 10: `show.audioNodes`, and the per-object `excludeNodes`
 * on cue audio/announcement outputs, the night bed background, and a
 * show.action audio target. The coordinator half (PR #517) has landed and
 * `show.audioNodes`/`excludeNodes` are now part of the generated schema for
 * every shape except the night bed's two WRITE forms
 * (ConfigNightSessionBackgroundAudioInlineWrite/ReferenceWrite): the
 * coordinator's own decode path accepts `excludeNodes` there
 * (nightSessionBackgroundKeys/nightSessionBackgroundRefKeys in
 * internal/coordinator/config/nightsession.go), but api/openapi.yaml's
 * WRITE schemas for those two forms do not yet declare it, so
 * openapi-typescript does not generate it. Those two widened types stay
 * hand-written until that spec gap closes; every other type here is a
 * plain alias of the generated schema.
 *
 * No `resolvedFrom` field is reported anywhere: the coordinator team
 * confirmed the resolution order is not surfaced on GET responses, so
 * `ResolvedFrom` is a client-only concept computed by showsModel.ts's
 * `resolveAudioNodes`, never a wire field.
 */
import type {
  ConfigNightSessionBackgroundAudio,
  ConfigNightSessionBackgroundAudioInlineWrite,
  ConfigNightSessionBackgroundAudioReferenceWrite,
  ConfigShow,
  ConfigShowActionTarget,
  ConfigShowCueAnnouncementOutput,
  ConfigShowCueAudioOutput,
} from './domain'

/** Where the client resolved an audio-bearing object's nodes from (ADR-049 decision 10); never reported by the coordinator, always computed locally. */
export type ResolvedFrom = 'explicit' | 'show' | 'default'

export type ExcludeNodesFields = { excludeNodes?: string[] }
export type ConfigNightSessionBackgroundAudioInlineWriteWithExclude = ConfigNightSessionBackgroundAudioInlineWrite & ExcludeNodesFields
export type ConfigNightSessionBackgroundAudioReferenceWriteWithExclude = ConfigNightSessionBackgroundAudioReferenceWrite & ExcludeNodesFields

/** `show.audioNodes`, or empty when the show carries none (absent and empty both mean unset, per ConfigShow's own description). */
export function readShowAudioNodes(show: ConfigShow): string[] {
  return show.audioNodes ?? []
}

/** `outputs.audio.excludeNodes` or `outputs.announcement.excludeNodes`; only meaningful when the output carries no explicit `targets` of its own. */
export function readCueOutputExcludeNodes(output: ConfigShowCueAudioOutput | ConfigShowCueAnnouncementOutput | undefined): string[] {
  return output?.excludeNodes ?? []
}

/** The night bed's own `excludeNodes`, inline or reference form alike; only meaningful when the bed carries no explicit `targets` of its own. */
export function readBackgroundAudioExcludeNodes(bg: ConfigNightSessionBackgroundAudio | undefined): string[] {
  return bg?.excludeNodes ?? []
}

/** A show.action audio target's `excludeNodes`; only meaningful when the target carries no explicit `audioNodeId` list of its own. */
export function readActionTargetExcludeNodes(target: ConfigShowActionTarget): string[] {
  return target.excludeNodes ?? []
}
