import { useEffect, useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { getShowSurface, listConfigObjects } from '../api'
import { ScopedButton } from '../components/ScopedButton'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showSurfaceNewPath, showSurfacePath } from '../components/showWorkspacePaths'
import '../styles/shows.css'
import type { ConfigObjectSummary, ConfigShowSurface } from '../app/types'

// Show Presentation.dc.html: Surfaces plus the virtual matrix map and the
// derived channel count. The map draws one band across every surface's
// claimed channel range, with an explicit "No overlaps" verdict -
// overlapping ranges are a real authoring error that three separate
// forms cannot reveal on their own.
//
// The mock's own band is drawn across a fixed 32,768-channel universe.
// That figure is the mock's illustrative scenario, not a field this
// API returns anywhere (grepped api/openapi.yaml, schema.d.ts,
// pkg/capability/id.go: no total-matrix-capacity field exists), so this
// view does not render it as if it were a coordinator fact. It instead
// derives the band's known extent from the surfaces actually configured
// (the highest claimed endChannel) and states plainly that no fixed
// capacity is reported, rather than inventing an "unallocated" segment
// sized against a number nothing confirms.
const CONFIG_WRITE_SCOPE = 'config:write'

interface SurfaceRow {
  summary: ConfigObjectSummary
  payload: ConfigShowSurface | null
}

export function ShowSurfaces() {
  const { showId = '' } = useParams<{ showId: string }>()
  const navigate = useNavigate()
  const data = useShowWorkspaceData(showId)

  const [rows, setRows] = useState<SurfaceRow[] | 'loading' | 'error'>('loading')

  useEffect(() => {
    if (data.kind !== 'loaded') return
    let cancelled = false
    setRows('loading')
    listConfigObjects('show.surface', showId)
      .then(async (resp) => {
        const payloads = await Promise.all(
          resp.objects.map((s) =>
            getShowSurface(s.id)
              .then((r) => r.payload)
              .catch(() => null),
          ),
        )
        if (cancelled) return
        setRows(resp.objects.map((summary, i) => ({ summary, payload: payloads[i] ?? null })))
      })
      .catch(() => {
        if (!cancelled) setRows('error')
      })
    return () => {
      cancelled = true
    }
  }, [data.kind, showId])

  const validRows = Array.isArray(rows) ? rows.filter((r): r is SurfaceRow & { payload: ConfigShowSurface } => r.payload !== null) : []
  const knownExtent = validRows.reduce((max, r) => Math.max(max, r.payload.channelRange.startChannel + r.payload.channelRange.channelCount - 1), 0)
  const overlaps: string[] = []
  for (let i = 0; i < validRows.length; i++) {
    for (let j = i + 1; j < validRows.length; j++) {
      const a = validRows[i]!.payload.channelRange
      const b = validRows[j]!.payload.channelRange
      const aEnd = a.startChannel + a.channelCount - 1
      const bEnd = b.startChannel + b.channelCount - 1
      if (a.startChannel <= bEnd && b.startChannel <= aEnd) {
        overlaps.push(`${validRows[i]!.summary.label} and ${validRows[j]!.summary.label}`)
      }
    }
  }

  return (
    <ShowWorkspaceFrame showId={showId} active="presentation" data={data}>
      <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap' }}>
        <h2 className="t-heading">Surfaces</h2>
        <ScopedButton requiredScope={CONFIG_WRITE_SCOPE} className="btn btn--primary" onClick={() => navigate(showSurfaceNewPath(showId))}>
          New surface
        </ScopedButton>
      </div>
      <p className="t-small shows-muted" style={{ maxWidth: '74ch', marginTop: 8 }}>
        Each surface extracts one channel range from the show&rsquo;s virtual matrix and renders it
        to one node, over exactly one transport. Select a surface to edit it.
      </p>

      {rows === 'loading' && (
        <p className="ruled-strip ruled-strip--loading" role="status">
          <span className="ruled-strip__state t-meta">Loading</span>
          <span className="ruled-strip__explanation">Reading this show&rsquo;s surfaces.</span>
        </p>
      )}
      {rows === 'error' && (
        <p className="ruled-strip ruled-strip--failed" role="alert">
          <span className="ruled-strip__state t-meta">Failed</span>
          <span className="ruled-strip__explanation">Could not load this show&rsquo;s surfaces.</span>
        </p>
      )}

      {validRows.length > 0 && (
        <div className="matrix-card">
          <div style={{ display: 'flex', alignItems: 'baseline', justifyContent: 'space-between', gap: 12, flexWrap: 'wrap' }}>
            <h3 className="t-meta shows-faint">Virtual matrix &middot; known extent {knownExtent.toLocaleString()} channels</h3>
            <span className="t-small" style={{ color: overlaps.length === 0 ? 'var(--good-fg)' : 'var(--bad-fg)' }}>
              {overlaps.length === 0 ? 'No overlaps' : `Overlap: ${overlaps.join('; ')}`}
            </span>
          </div>
          <div className="matrix-band">
            {validRows
              .slice()
              .sort((a, b) => a.payload.channelRange.startChannel - b.payload.channelRange.startChannel)
              .map((r) => (
                <div
                  key={r.summary.id}
                  className="matrix-band__segment matrix-band__segment--claimed"
                  style={{ width: `${(r.payload.channelRange.channelCount / knownExtent) * 100}%` }}
                >
                  <span className="matrix-band__label t-meta shows-faint">{r.summary.label.toUpperCase()}</span>
                </div>
              ))}
          </div>
          <p className="t-small shows-muted" style={{ marginTop: 10 }}>
            {validRows.length} surface{validRows.length === 1 ? '' : 's'} claim channels 1&ndash;{knownExtent.toLocaleString()}.
            This API reports no fixed matrix capacity, so no unallocated remainder is shown beyond
            the highest channel a surface actually claims.
          </p>
        </div>
      )}

      {validRows.length > 0 && (
        <div className="card" style={{ marginTop: 20 }}>
          <div className="table-wrap">
            <table className="table table--full" aria-label="Surfaces">
              <thead>
                <tr>
                  <th scope="col">Surface</th>
                  <th scope="col">Geometry</th>
                  <th scope="col">Output</th>
                </tr>
              </thead>
              <tbody>
                {validRows.map((r) => (
                  <tr key={r.summary.id} data-clickable onClick={() => navigate(showSurfacePath(showId, r.summary.id))}>
                    <td>
                      <a className="entity-link" href={showSurfacePath(showId, r.summary.id)} onClick={(e) => e.preventDefault()}>
                        {r.summary.label}
                      </a>
                      <br />
                      <span className="t-data shows-id-meta">
                        {r.payload.node} &middot; ch {r.payload.channelRange.startChannel.toLocaleString()}&ndash;
                        {(r.payload.channelRange.startChannel + r.payload.channelRange.channelCount - 1).toLocaleString()}
                      </span>
                    </td>
                    <td className="t-data shows-id-meta">
                      {r.payload.geometry.width}&times;{r.payload.geometry.height} {r.payload.geometry.pixelFormat}
                    </td>
                    <td>
                      <span className={`transport-chip${r.payload.output.transport === 'ndi' ? '' : ' transport-chip--accent'}`}>
                        {r.payload.output.transport.toUpperCase()}
                      </span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <div className="table__footer-note">Rendering evidence is not fetched here; see each surface&rsquo;s node for what it reports.</div>
        </div>
      )}

      {Array.isArray(rows) && rows.length === 0 && (
        <p className="ruled-strip ruled-strip--empty" role="status">
          <span className="ruled-strip__state t-meta">Empty</span>
          <span className="ruled-strip__explanation">No surfaces are configured in this show yet.</span>
        </p>
      )}
    </ShowWorkspaceFrame>
  )
}
