import { useEffect, useState } from 'react'
import { Link, NavLink, Outlet, useParams } from 'react-router-dom'
import { getFPPPlaylistReadiness, getShow, type ShowConfigResponse } from '../api'
import { BlankingPlate, Button, PageTitle, RuledStrip, StatusPair } from '../kit'
import { describeApiError } from '../domain/session'
import { formatClock } from '../domain/time'
import { fetchShowContents, fetchShowPlaylists } from './showsData'
import { contentsCounts, type ShowContentsCounts } from './showsModel'

type HeadState =
  | { kind: 'loading' }
  | { kind: 'loaded'; response: ShowConfigResponse }
  | { kind: 'failed'; reason: string; response: ShowConfigResponse | null }

function useShowHead(id: string): HeadState {
  const [state, setState] = useState<HeadState>({ kind: 'loading' })
  useEffect(() => {
    let cancelled = false
    getShow(id)
      .then((response) => {
        if (!cancelled) setState({ kind: 'loaded', response })
      })
      .catch((err: unknown) => {
        if (!cancelled) setState({ kind: 'failed', reason: describeApiError(err), response: null })
      })
    return () => {
      cancelled = true
    }
  }, [id])
  return state
}

function useTabCounts(id: string): ShowContentsCounts | null {
  const [counts, setCounts] = useState<ShowContentsCounts | null>(null)
  useEffect(() => {
    let cancelled = false
    fetchShowContents(id)
      .then((contents) => {
        if (!cancelled) setCounts(contentsCounts(contents))
      })
      .catch(() => {
        // A failed count leaves the tab strip showing no count rather than a
        // fabricated zero; the tab itself still routes.
      })
    return () => {
      cancelled = true
    }
  }, [id])
  return counts
}

type ReadinessResult = { playlistId: string; label: string; ready: boolean; failingCondition: string | null; reason: string | null; warning: string | null }

/** One button that runs `GET .../readiness` for every FPP-runner playlist in this show. There is no aggregate endpoint; this rolls up N real per-playlist reads, never a fabricated one. */
function useCheckReadiness(showId: string) {
  const [results, setResults] = useState<ReadinessResult[] | null>(null)
  const [state, setState] = useState<'idle' | 'checking' | 'failed'>('idle')
  const [error, setError] = useState<string | null>(null)

  const run = async () => {
    setState('checking')
    setError(null)
    try {
      const list = await fetchShowContents(showId)
      const full = await fetchShowPlaylists(list.playlists)
      const fppPlaylists = full.filter((p) => p.payload.runner === 'fpp')
      const readiness = await Promise.all(
        fppPlaylists.map(async (p) => {
          try {
            const r = await getFPPPlaylistReadiness(p.id)
            return {
              playlistId: p.id,
              label: p.payload.name,
              ready: r.ready,
              failingCondition: r.failingCondition ?? null,
              reason: r.reason ?? null,
              warning: r.warning ?? null,
            }
          } catch (err) {
            return { playlistId: p.id, label: p.payload.name, ready: false, failingCondition: null, reason: describeApiError(err), warning: null }
          }
        }),
      )
      setResults(readiness)
      setState('idle')
    } catch (err) {
      setError(describeApiError(err))
      setState('failed')
    }
  }

  return { results, state, error, run }
}

const TABS: readonly { path: string; label: string; built: boolean }[] = [
  { path: 'playlists', label: 'Playlists', built: true },
  { path: 'cues', label: 'Cues', built: false },
  { path: 'assets', label: 'Assets', built: false },
  { path: 'presentation', label: 'Presentation', built: false },
  { path: 'automation', label: 'Automation', built: false },
]

function tabCount(tab: string, counts: ShowContentsCounts | null): number | null {
  if (counts === null) return null
  switch (tab) {
    case 'playlists':
      return counts.playlists
    case 'cues':
      return counts.cues
    case 'presentation':
      return counts.surfaces
    case 'assets':
      return counts.assets
    case 'automation':
      return counts.automation
    default:
      return null
  }
}

export function ShowsWorkspace() {
  const { id = '' } = useParams<{ id: string }>()
  const head = useShowHead(id)
  const counts = useTabCounts(id)
  const readiness = useCheckReadiness(id)

  if (head.kind === 'loading') {
    return (
      <>
        <PageTitle title="Show" />
        <RuledStrip absence="loading" label="Reading" fact={`Asking the coordinator for ${id}.`} />
      </>
    )
  }

  if (head.kind === 'failed') {
    return (
      <>
        <PageTitle title="Show" />
        <RuledStrip absence="failed" label="Read failed" fact={head.reason} />
      </>
    )
  }

  const { payload, revision, updatedAt, createdByPrincipalName } = head.response

  return (
    <>
      <p className="sm-small sm-muted">
        <Link to="/shows" className="sm-muted">
          Shows
        </Link>{' '}
        <span className="sm-faint">/</span> {payload.name}
      </p>

      <div className="sm-page__head sm-stack-2">
        <div>
          <h1 className="sm-page__title">{payload.name}</h1>
          <p className="sm-page__lede">
            Revision <span className="sm-data">{revision}</span> · saved by {createdByPrincipalName ?? 'an unknown principal'} at{' '}
            {formatClock(updatedAt) ?? 'an unrecorded time'}
          </p>
        </div>
        <div className="sm-btn-row">
          <Link className="sm-btn" to={`/shows/${id}`}>
            Show details
          </Link>
          <Button onClick={readiness.run} disabled={readiness.state === 'checking'}>
            {readiness.state === 'checking' ? 'Checking…' : 'Check readiness'}
          </Button>
        </div>
      </div>

      {readiness.state === 'failed' && readiness.error !== null && (
        <RuledStrip absence="failed" label="Could not check" fact={readiness.error} />
      )}
      {readiness.results !== null && (
        <div className="sm-panel sm-stack-4">
          {readiness.results.length === 0 ? (
            <p className="sm-small sm-muted">No FPP-runner playlist in this show to check. ShowMesh-audio playlists have no readiness concept.</p>
          ) : (
            readiness.results.map((r) => (
              <p key={r.playlistId} className="sm-small">
                <StatusPair tone={r.ready ? 'good' : 'bad'} label={r.ready ? 'Ready' : (r.failingCondition ?? 'Not ready')} /> {r.label}
                {r.reason !== null && r.reason !== '' && <span className="sm-muted">: {r.reason}</span>}
                {r.warning !== null && r.warning !== '' && <span className="sm-muted"> ({r.warning})</span>}
              </p>
            ))
          )}
        </div>
      )}

      <nav className="sm-facets" aria-label="Show workspace tabs">
        {TABS.map((tab) => {
          const count = tabCount(tab.path, counts)
          return (
            <NavLink key={tab.path} to={`/shows/${id}/${tab.path}`} className="sm-facets__tab">
              {tab.label}
              {count !== null && <span className="sm-facets__count">{count}</span>}
              {!tab.built && (
                <span className="sm-nowire-tag__chip" title={`${tab.label} has not been rebuilt yet`}>
                  Soon
                </span>
              )}
            </NavLink>
          )
        })}
      </nav>

      <div data-panes>
        <Outlet />
      </div>
    </>
  )
}

export function ShowsTabPlaceholder({ tab }: { tab: string }) {
  return (
    <BlankingPlate
      absence="unavailable"
      stamp="Soon"
      eyebrow={`${tab} · not rebuilt`}
      title="This tab has not been rebuilt yet"
      detail="The operator UI is being rebuilt one screen at a time against Show Authoring.dc.html. This tab is still in the queue. Its controls are recorded in docs/ui-rebuild/CONTROL-INVENTORY.md and nothing has been dropped."
    />
  )
}
