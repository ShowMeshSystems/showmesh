import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowCue, getShowPlaylist, listConfigObjects } from '../api'
import { ScopedButton } from '../components/ScopedButton'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showCueNewPath, showCuePath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type { ConfigObjectSummary, ConfigShowCue } from '../app/types'

// Show Cues.dc.html: the cue library is grouped by REACHABILITY, not by
// output kind - "In a playlist" (bound to at least one playlist entry),
// "Not in any playlist" (authored but unreachable - a real authoring
// mistake or a deliberate safe-cue target), "Directly activatable"
// (announcement cues fired from Live Control, never a playlist entry).
// Cues are SHARED across playlists: a cue used by two playlists is one
// object, so the list states plainly that editing it changes both.
const CONFIG_WRITE_SCOPE = 'config:write'

type OutputFilter = 'all' | 'render' | 'audio' | 'unused'

interface CueRow {
  summary: ConfigObjectSummary
  payload: ConfigShowCue | null
  usedByPlaylists: string[]
}

export function ShowCues() {
  const { showId = '' } = useParams<{ showId: string }>()
  const navigate = useNavigate()
  const data = useShowWorkspaceData(showId)

  const [rows, setRows] = useState<CueRow[] | 'loading' | 'error'>('loading')
  const [filterText, setFilterText] = useState('')
  const [outputFilter, setOutputFilter] = useState<OutputFilter>('all')

  useEffect(() => {
    if (data.kind !== 'loaded') return
    let cancelled = false
    setRows('loading')
    Promise.all([listConfigObjects('show.cue', showId), listConfigObjects('show.playlist', showId)])
      .then(async ([cueList, playlistList]) => {
        const playlistPayloads = await Promise.all(
          playlistList.objects.map((p) =>
            getShowPlaylist(p.id)
              .then((r) => ({ label: p.label, cueIds: new Set(r.payload.entries.map((e) => e.cue)) }))
              .catch(() => ({ label: p.label, cueIds: new Set<string>() })),
          ),
        )
        const cuePayloads = await Promise.all(
          cueList.objects.map((c) =>
            getShowCue(c.id)
              .then((r) => r.payload)
              .catch(() => null),
          ),
        )
        if (cancelled) return
        setRows(
          cueList.objects.map((summary, i) => ({
            summary,
            payload: cuePayloads[i] ?? null,
            usedByPlaylists: playlistPayloads.filter((p) => p.cueIds.has(summary.id)).map((p) => p.label),
          })),
        )
      })
      .catch(() => {
        if (!cancelled) setRows('error')
      })
    return () => {
      cancelled = true
    }
  }, [data.kind, showId])

  const { inPlaylist, unreachable, activatable } = useMemo(() => {
    if (!Array.isArray(rows)) return { inPlaylist: [], unreachable: [], activatable: [] }
    const filtered = rows.filter((row) => {
      if (filterText.trim() !== '' && !row.summary.label.toLowerCase().includes(filterText.trim().toLowerCase())) return false
      if (outputFilter === 'render') return row.payload?.outputs.render !== undefined
      if (outputFilter === 'audio') return row.payload?.outputs.audio !== undefined
      if (outputFilter === 'unused') return row.usedByPlaylists.length === 0
      return true
    })
    return {
      inPlaylist: filtered.filter((r) => r.usedByPlaylists.length > 0),
      unreachable: filtered.filter((r) => r.usedByPlaylists.length === 0 && r.payload?.outputs.announcement === undefined),
      activatable: filtered.filter((r) => r.usedByPlaylists.length === 0 && r.payload?.outputs.announcement !== undefined),
    }
  }, [rows, filterText, outputFilter])

  function outputChips(payload: ConfigShowCue | null): ReactNode {
    if (payload === null) return <span className="output-chip output-chip--unavailable">?</span>
    const chips: string[] = []
    if (payload.outputs.render !== undefined) chips.push('RND')
    if (payload.outputs.audio !== undefined) chips.push('AUD')
    if (payload.outputs.ltc !== undefined) chips.push('LTC')
    if (payload.outputs.announcement !== undefined) chips.push('ANN')
    return (
      <span className="output-chip-group">
        {chips.map((c) => (
          <span key={c} className="output-chip">
            {c}
          </span>
        ))}
      </span>
    )
  }

  function announcementPolicySummary(payload: ConfigShowCue | null): string {
    const ann = payload?.outputs.announcement
    if (ann === undefined) return ''
    if (ann.policy === 'duck') return `Duck to ${ann.duckGainDb} dB`
    return ann.policy === 'mix' ? 'Mix' : 'Interrupt'
  }

  return (
    <ShowWorkspaceFrame showId={showId} active="cues" data={data}>
      <div className="cues-toolbar">
        <div className="cues-toolbar-left">
          <input
            className="cues-filter-input"
            type="text"
            placeholder="Filter cues…"
            value={filterText}
            onChange={(e) => setFilterText(e.target.value)}
            aria-label="Filter cues"
          />
          <div className="segmented" role="group" aria-label="Filter by output">
            {(['all', 'render', 'audio', 'unused'] as OutputFilter[]).map((f) => (
              <button
                key={f}
                type="button"
                className="segmented__option"
                aria-pressed={outputFilter === f}
                onClick={() => setOutputFilter(f)}
              >
                {f === 'all' ? 'All' : f === 'render' ? 'Render' : f === 'audio' ? 'Audio' : 'Unused'}
              </button>
            ))}
          </div>
        </div>
        <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} className="btn btn--primary" onClick={() => navigate(showCueNewPath(showId))}>
          New cue
        </ScopedButton>
      </div>

      <p className="t-small shows-muted" style={{ marginTop: 12 }}>
        Select a cue to edit it. Editing a cue changes every playlist that uses it.
      </p>

      {rows === 'loading' && (
        <p className="ruled-strip ruled-strip--loading" role="status">
          <span className="ruled-strip__state t-meta">Loading</span>
          <span className="ruled-strip__explanation">Reading this show&rsquo;s cues.</span>
        </p>
      )}
      {rows === 'error' && (
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">Could not load this show&rsquo;s cues.</span>
        </p>
      )}

      {Array.isArray(rows) && (
        <>
          <p className="t-meta cues-section-label">
            In a playlist <span className="shows-muted">· {inPlaylist.length}</span>
          </p>
          {inPlaylist.length === 0 ? (
            <p className="t-small shows-faint">None.</p>
          ) : (
            <div className="card">
              <div className="table-wrap">
                <table className="table table--full" aria-label="Cues in a playlist">
                  <thead>
                    <tr>
                      <th scope="col">Cue</th>
                      <th scope="col">Outputs</th>
                      <th scope="col">Used by</th>
                    </tr>
                  </thead>
                  <tbody>
                    {inPlaylist.map((row) => (
                      <tr key={row.summary.id} data-clickable onClick={() => navigate(showCuePath(showId, row.summary.id))}>
                        <td>
                          <a className="entity-link" href={showCuePath(showId, row.summary.id)} onClick={(e) => e.preventDefault()}>
                            {row.summary.label}
                          </a>
                          <br />
                          <span className="t-data shows-id-meta">{row.summary.id}</span>
                        </td>
                        <td>{outputChips(row.payload)}</td>
                        <td className="t-small shows-muted">{row.usedByPlaylists.join(', ')}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="table__footer-note">
                A cue used by more than one playlist is one object, editing it changes both.
              </div>
            </div>
          )}

          <p className="t-meta cues-section-label">
            Not in any playlist <span className="shows-muted">· {unreachable.length}</span>
          </p>
          <p className="t-small shows-muted" style={{ maxWidth: '70ch' }}>
            Authored but unreachable. Bind it to a playlist entry, or leave it as a safe-cue target.
          </p>
          {unreachable.length === 0 ? (
            <p className="t-small shows-faint">None.</p>
          ) : (
            <div className="card">
              <div className="table-wrap">
                <table className="table table--full" aria-label="Cues not in any playlist">
                  <thead>
                    <tr>
                      <th scope="col">Cue</th>
                      <th scope="col">Outputs</th>
                      <th scope="col">State</th>
                    </tr>
                  </thead>
                  <tbody>
                    {unreachable.map((row) => (
                      <tr key={row.summary.id} data-clickable onClick={() => navigate(showCuePath(showId, row.summary.id))}>
                        <td>
                          <a className="entity-link" href={showCuePath(showId, row.summary.id)} onClick={(e) => e.preventDefault()}>
                            {row.summary.label}
                          </a>
                          <br />
                          <span className="t-data shows-id-meta">{row.summary.id}</span>
                        </td>
                        <td>{outputChips(row.payload)}</td>
                        <td className="t-meta shows-faint">Unreachable</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}

          <p className="t-meta cues-section-label">
            Directly activatable <span className="shows-muted">· {activatable.length}</span>
          </p>
          <p className="t-small shows-muted" style={{ maxWidth: '70ch' }}>
            Announcements are not playlist entries. An operator fires them from Live Control, and
            they duck the background bed without touching FPP.
          </p>
          {activatable.length === 0 ? (
            <p className="t-small shows-faint">None.</p>
          ) : (
            <div className="card">
              <div className="table-wrap">
                <table className="table table--full" aria-label="Directly activatable cues">
                  <thead>
                    <tr>
                      <th scope="col">Cue</th>
                      <th scope="col">Outputs</th>
                      <th scope="col">Policy</th>
                    </tr>
                  </thead>
                  <tbody>
                    {activatable.map((row) => (
                      <tr key={row.summary.id} data-clickable onClick={() => navigate(showCuePath(showId, row.summary.id))}>
                        <td>
                          <a className="entity-link" href={showCuePath(showId, row.summary.id)} onClick={(e) => e.preventDefault()}>
                            {row.summary.label}
                          </a>
                          <br />
                          <span className="t-data shows-id-meta">{row.summary.id}</span>
                        </td>
                        <td>{outputChips(row.payload)}</td>
                        <td className="t-small shows-muted">{announcementPolicySummary(row.payload)}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </div>
          )}
        </>
      )}
    </ShowWorkspaceFrame>
  )
}
