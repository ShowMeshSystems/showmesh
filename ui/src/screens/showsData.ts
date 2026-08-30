/**
 * The on-demand reads the Shows workspace needs beyond the live model. Kept
 * separate from `showsModel.ts` (pure shaping functions) because these touch
 * the network, matching `MonitorManifest.tsx`'s own split.
 */
import {
  getShowPlaylist,
  listAssets,
  listConfigObjects,
  type ShowPlaylistConfigResponse,
} from '../api'
import type { ShowContents } from './showsModel'

/**
 * Counts for every show in six reads, not six per show. Each summary already
 * names the show it belongs to, so one unfiltered list per kind is enough:
 * a per-show fetch would open a show list on a large installation with
 * dozens of simultaneous requests at the coordinator.
 */
export async function fetchAllShowContents(): Promise<Map<string, ShowContents>> {
  const [playlists, cues, surfaces, actions, macros, assetsResponse] = await Promise.all([
    listConfigObjects('show.playlist'),
    listConfigObjects('show.cue'),
    listConfigObjects('show.surface'),
    listConfigObjects('show.action'),
    listConfigObjects('show.macro'),
    listAssets(),
  ])

  const byShow = new Map<string, ShowContents>()
  const of = (show: string): ShowContents => {
    const existing = byShow.get(show)
    if (existing !== undefined) return existing
    const fresh: ShowContents = { playlists: [], cues: [], surfaces: [], actions: [], macros: [], assets: [] }
    byShow.set(show, fresh)
    return fresh
  }

  for (const object of playlists.objects) of(object.show).playlists.push(object)
  for (const object of cues.objects) of(object.show).cues.push(object)
  for (const object of surfaces.objects) of(object.show).surfaces.push(object)
  for (const object of actions.objects) of(object.show).actions.push(object)
  for (const object of macros.objects) of(object.show).macros.push(object)
  for (const asset of assetsResponse.assets) of(asset.show).assets.push(asset)

  return byShow
}

export async function fetchShowContents(showId: string): Promise<ShowContents> {
  const [playlists, cues, surfaces, actions, macros, assetsResponse] = await Promise.all([
    listConfigObjects('show.playlist', showId),
    listConfigObjects('show.cue', showId),
    listConfigObjects('show.surface', showId),
    listConfigObjects('show.action', showId),
    listConfigObjects('show.macro', showId),
    listAssets({ show: showId }),
  ])
  return {
    playlists: playlists.objects,
    cues: cues.objects,
    surfaces: surfaces.objects,
    actions: actions.objects,
    macros: macros.objects,
    assets: assetsResponse.assets,
  }
}

/** The full config response, revision included: an edit form needs it to report a save, not just the payload. */
export async function fetchShowPlaylists(playlists: readonly { id: string }[]): Promise<ShowPlaylistConfigResponse[]> {
  return Promise.all(playlists.map((p) => getShowPlaylist(p.id)))
}
