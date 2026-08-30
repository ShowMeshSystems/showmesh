import type { Asset, ConfigObjectSummary, ConfigShowPlaylist, FPPInstance, FPPPlaylistDefinitionMetadata, Model } from '../api'
import { formatClock } from '../domain/time'

/** The id of the show `currentRuns` reports as active, or null when unknown. */
export function activeShowId(model: Model): string | null {
  const activeShow = model.currentRuns?.activeShow
  if (activeShow === undefined || activeShow === null || !activeShow.configured) return null
  return activeShow.show
}

export type ShowRow = {
  id: string
  label: string
  revision: number
  updatedAt: string
  active: boolean
}

export function showRows(objects: readonly ConfigObjectSummary[], model: Model): ShowRow[] {
  const active = activeShowId(model)
  return objects.map((object) => ({
    id: object.id,
    label: object.label,
    revision: object.currentRevision,
    updatedAt: object.updatedAt,
    active: active !== null && active === object.id,
  }))
}

export function showSavedLabel(updatedAt: string): string {
  return formatClock(updatedAt) ?? 'an unrecorded time'
}

/**
 * What one show contains, per its own configured objects. A fetch that fails
 * leaves the whole bundle unfetched rather than substituting zero for one part.
 */
export type ShowContents = {
  playlists: ConfigObjectSummary[]
  cues: ConfigObjectSummary[]
  surfaces: ConfigObjectSummary[]
  actions: ConfigObjectSummary[]
  macros: ConfigObjectSummary[]
  assets: Asset[]
}

export type ShowContentsCounts = {
  playlists: number
  cues: number
  surfaces: number
  assets: number
  /** The workspace's Automation tab covers both action and macro objects. */
  automation: number
}

/** A show the lists returned nothing for holds nothing, which the lists did determine. */
export const EMPTY_CONTENTS: ShowContents = {
  playlists: [],
  cues: [],
  surfaces: [],
  actions: [],
  macros: [],
  assets: [],
}

export function contentsCounts(contents: ShowContents): ShowContentsCounts {
  return {
    playlists: contents.playlists.length,
    cues: contents.cues.length,
    surfaces: contents.surfaces.length,
    assets: contents.assets.length,
    automation: contents.actions.length + contents.macros.length,
  }
}

export function contentsSummary(counts: ShowContentsCounts): string {
  if (counts.playlists === 0 && counts.cues === 0 && counts.surfaces === 0 && counts.assets === 0) return 'Empty'
  return `${counts.playlists} ${counts.playlists === 1 ? 'playlist' : 'playlists'} · ${counts.cues} ${counts.cues === 1 ? 'cue' : 'cues'} · ${counts.surfaces} ${counts.surfaces === 1 ? 'surface' : 'surfaces'} · ${counts.assets} ${counts.assets === 1 ? 'asset' : 'assets'}`
}

/** A cue id resolved to the label its own config object carries, never invented. */
export function cueLabel(cues: readonly ConfigObjectSummary[], cueId: string): string {
  return cues.find((cue) => cue.id === cueId)?.label ?? cueId
}

export type PlaylistRow = {
  id: string
  label: string
  runner: ConfigShowPlaylist['runner']
  runnerLabel: string
  entriesBound: number
  detail: string
}

export function playlistRows(playlists: readonly { id: string; payload: ConfigShowPlaylist }[]): PlaylistRow[] {
  return playlists.map(({ id, payload }) => ({
    id,
    label: payload.name,
    runner: payload.runner,
    runnerLabel: payload.runner === 'fpp' ? 'FPP runner' : 'ShowMesh audio',
    entriesBound: payload.entries.length,
    detail:
      payload.runner === 'fpp'
        ? `FPP owns order and progression · ${payload.entries.length} ${payload.entries.length === 1 ? 'entry' : 'entries'} bound`
        : `ShowMesh owns order and progression · ${payload.entries.length} ${payload.entries.length === 1 ? 'entry' : 'entries'}${payload.showmeshAudio !== undefined && payload.showmeshAudio.repeat === 'all' ? ' · repeat all' : ''}`,
  }))
}

/** The FPP instance's own label for a binding's instanceUuid, or the raw uuid when it names nothing this coordinator has heard from. */
export function fppInstanceLabel(fpp: readonly FPPInstance[], instanceUuid: string): string {
  return fpp.find((instance) => instance.instanceUuid === instanceUuid)?.instanceId ?? instanceUuid
}

export function fppInstanceRoute(fpp: readonly FPPInstance[], instanceUuid: string): string | null {
  const instance = fpp.find((i) => i.instanceUuid === instanceUuid)
  return instance === undefined ? null : `/monitor/fleet/fpp/${instance.instanceId}`
}

/**
 * A newer definition, captured under a different hash for the same instance
 * and playlist name (TRACK-H-H2-SPEC.md §3.6), is the coordinator's own
 * evidence that FPP's definition moved, not a guess from comparing timestamps.
 */
export function newerDefinition(
  definitions: readonly FPPPlaylistDefinitionMetadata[],
  instanceUuid: string,
  playlistName: string,
  boundHash: string,
): FPPPlaylistDefinitionMetadata | null {
  const candidates = definitions.filter((d) => d.instanceUuid === instanceUuid && d.playlistName === playlistName && d.playlistHash !== boundHash)
  if (candidates.length === 0) return null
  return candidates.reduce((latest, entry) => (entry.capturedAt > latest.capturedAt ? entry : latest))
}
