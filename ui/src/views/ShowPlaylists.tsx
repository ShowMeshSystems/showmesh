import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowPlaylist, listConfigObjects } from '../api'
import { ScopedButton } from '../components/ScopedButton'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showPlaylistNewPath, showPlaylistPath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type { ConfigObjectSummary, ConfigShowPlaylist } from '../app/types'

// Show Authoring.dc.html's list section: the Playlists workspace tab.
// Playlists split by runner because the two runners behave differently
// (fpp mirrors an imported list order it does not own; showmesh-audio is
// authored and reorderable) - that split lives on the detail page
// (ShowPlaylistDetail.tsx); this tab is the card list that gets you
// there, plus New playlist.
const CONFIG_WRITE_SCOPE = 'config:write'

type PlaylistPayloadState = ConfigShowPlaylist | 'loading' | 'error'

export function ShowPlaylists() {
  const { showId = '' } = useParams<{ showId: string }>()
  const navigate = useNavigate()
  const data = useShowWorkspaceData(showId)

  const [list, setList] = useState<ConfigObjectSummary[] | 'loading' | 'error'>('loading')
  const [payloads, setPayloads] = useState<Record<string, PlaylistPayloadState>>({})

  useEffect(() => {
    if (data.kind !== 'loaded') return
    let cancelled = false
    setList('loading')
    listConfigObjects('show.playlist', showId)
      .then((resp) => {
        if (cancelled) return
        setList(resp.objects)
        for (const obj of resp.objects) {
          setPayloads((prev) => ({ ...prev, [obj.id]: 'loading' }))
          getShowPlaylist(obj.id)
            .then((full) => {
              if (!cancelled) setPayloads((prev) => ({ ...prev, [obj.id]: full.payload }))
            })
            .catch(() => {
              if (!cancelled) setPayloads((prev) => ({ ...prev, [obj.id]: 'error' }))
            })
        }
      })
      .catch(() => {
        if (!cancelled) setList('error')
      })
    return () => {
      cancelled = true
    }
  }, [data.kind, showId])

  return (
    <ShowWorkspaceFrame showId={showId} active="playlists" data={data}>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap' }}>
        <p className="t-meta shows-faint">Playlists in this show</p>
        <ScopedButton
          requiredScope={CONFIG_WRITE_SCOPE}
          className="btn btn--secondary"
          onClick={() => navigate(showPlaylistNewPath(showId))}
        >
          New playlist
        </ScopedButton>
      </div>

      {list === 'loading' && (
        <p className="ruled-strip ruled-strip--loading" role="status">
          <span className="ruled-strip__state t-meta">Loading</span>
          <span className="ruled-strip__explanation">Reading this show&rsquo;s playlists.</span>
        </p>
      )}
      {list === 'error' && (
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">Could not load this show&rsquo;s playlists.</span>
        </p>
      )}
      {Array.isArray(list) && list.length === 0 && (
        <p className="ruled-strip ruled-strip--empty" role="status">
          <span className="ruled-strip__state t-meta">Empty</span>
          <span className="ruled-strip__explanation">No playlists are authored in this show yet.</span>
        </p>
      )}
      {Array.isArray(list) && list.length > 0 && (
        <div className="playlist-card-list">
          {list.map((obj) => {
            const payload = payloads[obj.id]
            return (
              <a
                key={obj.id}
                className="playlist-card"
                href={showPlaylistPath(showId, obj.id)}
                onClick={(e) => {
                  e.preventDefault()
                  navigate(showPlaylistPath(showId, obj.id))
                }}
              >
                <div style={{ minWidth: 0 }}>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 9, flexWrap: 'wrap' }}>
                    <span className="playlist-card__title">{obj.label}</span>
                    {payload !== undefined && payload !== 'loading' && payload !== 'error' && (
                      <span className="playlist-runner-chip">{payload.runner === 'fpp' ? 'FPP runner' : 'ShowMesh audio'}</span>
                    )}
                  </div>
                  <p className="t-small shows-muted" style={{ margin: '4px 0 0' }}>
                    {payload === undefined || payload === 'loading'
                      ? 'Loading…'
                      : payload === 'error'
                        ? 'Could not load this playlist.'
                        : payload.runner === 'fpp'
                          ? `FPP owns order and progression · ${payload.entries.filter((e) => e.cue.trim() !== '').length} of ${payload.entries.length} entries bound`
                          : `ShowMesh owns order and progression · ${payload.entries.length} entries${payload.showmeshAudio ? ` · repeat ${payload.showmeshAudio.repeat}` : ''}`}
                  </p>
                </div>
              </a>
            )
          })}
        </div>
      )}

      {Array.isArray(list) && list.length > 1 && (
        <p className="playlist-concurrency-note">
          Playlists run <strong>concurrently</strong>, not in sequence. Two runners are never
          authoritative for the same playlist.
        </p>
      )}
    </ShowWorkspaceFrame>
  )
}
