import { useEffect, useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { getNightSessionConfig, listConfigObjects } from '../api'
import { evaluateAnyScope, evaluateScope } from '../app/session'
import { useModelContext } from '../app/ModelContext'
import { ScopedButton } from '../components/ScopedButton'
import { ShowWorkspaceFrame, useShowWorkspaceData } from '../components/ShowWorkspace'
import { showNightSessionNewPath, showNightSessionPath } from '../components/showWorkspacePaths'
import { NightSessionActivePanel } from './NightSessionActive'
import '../styles/shows.css'
import '../styles/night.css'
import type { ConfigNightSession, ConfigObjectSummary } from '../app/types'

// Show Night Session.dc.html's `view: 'list'` branch: the night-session
// definitions list, the active-pointer section (NightSessionActivePanel),
// and activation history. A definition says what happens when the night
// enters the show and when it returns to resting; it never says WHEN —
// the coordinator refuses any field named at, cron, schedule, time,
// date, weekday or timezone, and any field that restates the resting
// sequence's own duration.
const READ_SCOPES = ['show:macro:run', 'config:write']
const CONFIG_WRITE_SCOPE = 'config:write'

type PayloadState = ConfigNightSession | 'loading' | 'error'

export function NightSessions() {
  const { showId = '' } = useParams<{ showId: string }>()
  const navigate = useNavigate()
  const model = useModelContext()
  const readGate = evaluateAnyScope(model.session, model.sessionFetchFailed, READ_SCOPES)
  const writeGate = evaluateScope(model.session, model.sessionFetchFailed, CONFIG_WRITE_SCOPE)
  const data = useShowWorkspaceData(showId)

  const [list, setList] = useState<ConfigObjectSummary[] | 'loading' | 'error'>('loading')
  const [payloads, setPayloads] = useState<Record<string, PayloadState>>({})
  const [activeSessionId, setActiveSessionId] = useState<string | null>(null)

  useEffect(() => {
    if (data.kind !== 'loaded' || !readGate.allowed) return
    let cancelled = false
    setList('loading')
    listConfigObjects('night.session', showId)
      .then((resp) => {
        if (cancelled) return
        setList(resp.objects)
        for (const obj of resp.objects) {
          setPayloads((prev) => ({ ...prev, [obj.id]: 'loading' }))
          getNightSessionConfig(obj.id)
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data.kind, readGate.allowed, showId])

  const sessions = Array.isArray(list) ? list : []

  return (
    <ShowWorkspaceFrame showId={showId} active="night-sessions" data={data}>
      {!readGate.allowed && (
        <p className="ruled-strip ruled-strip--no-permission" role="status">
          <span className="ruled-strip__state t-meta">No permission</span>
          <span className="ruled-strip__explanation">{readGate.reason}</span>
        </p>
      )}

      {readGate.allowed && (
        <>
          <div style={{ display: 'flex', alignItems: 'flex-start', justifyContent: 'space-between', gap: 16, flexWrap: 'wrap' }}>
            <div style={{ minWidth: 0 }}>
              <h2 className="t-heading">Night session definitions</h2>
              <p className="t-small night-muted" style={{ maxWidth: '74ch' }}>
                A definition says what happens when the night enters the show and when it returns to resting. It
                never says <em>when</em>: the coordinator refuses any field named <span className="t-data">at</span>,{' '}
                <span className="t-data">cron</span>, <span className="t-data">schedule</span>,{' '}
                <span className="t-data">time</span>, <span className="t-data">date</span>,{' '}
                <span className="t-data">weekday</span> or <span className="t-data">timezone</span>, and any field
                that restates the resting sequence&rsquo;s own duration.
              </p>
            </div>
            <ScopedButton
              requiredScope={CONFIG_WRITE_SCOPE}
              className="btn btn--primary"
              onClick={() => navigate(showNightSessionNewPath(showId))}
            >
              New definition
            </ScopedButton>
          </div>

          <NightSessionActivePanel
            showId={showId}
            sessions={sessions}
            readAllowed={readGate.allowed}
            writeGate={writeGate}
            onCurrentSessionChange={setActiveSessionId}
          />

          <section aria-labelledby="ns-defs" className="night-section">
            <h3 id="ns-defs" className="t-meta night-eyebrow">
              Definitions <span className="night-muted">· {Array.isArray(list) ? list.length : ''}</span>
            </h3>

            {list === 'loading' && (
              <p className="ruled-strip ruled-strip--loading" role="status">
                <span className="ruled-strip__state t-meta">Loading</span>
                <span className="ruled-strip__explanation">Reading this show&rsquo;s night-session definitions.</span>
              </p>
            )}
            {list === 'error' && (
              <p className="ruled-strip ruled-strip--failed" role="alert">
                <span className="ruled-strip__state t-meta">Failed</span>
                <span className="ruled-strip__explanation">Could not load this show&rsquo;s night-session definitions.</span>
              </p>
            )}
            {Array.isArray(list) && list.length === 0 && (
              <p className="ruled-strip ruled-strip--empty" role="status">
                <span className="ruled-strip__state t-meta">Empty</span>
                <span className="ruled-strip__explanation">No night-session definitions are authored in this show yet.</span>
              </p>
            )}
            {Array.isArray(list) && list.length > 0 && (
              <div className="table-wrap">
                <table className="table table--full">
                  <thead>
                    <tr>
                      <th>Definition</th>
                      <th>Show playlist</th>
                      <th>Resting playlist</th>
                      <th>Revision</th>
                      <th>State</th>
                    </tr>
                  </thead>
                  <tbody>
                    {list.map((obj) => {
                      const payload = payloads[obj.id]
                      const isActive = obj.id === activeSessionId
                      return (
                        <tr
                          key={obj.id}
                          data-clickable="true"
                          aria-current={isActive ? 'true' : undefined}
                          onClick={() => navigate(showNightSessionPath(showId, obj.id))}
                        >
                          <td>
                            <Link className="entity-link" to={showNightSessionPath(showId, obj.id)} onClick={(e) => e.stopPropagation()}>
                              {obj.label}
                            </Link>
                            <br />
                            <span className="t-data night-faint" style={{ fontSize: '11px' }}>
                              {obj.id}
                            </span>
                          </td>
                          <td className="t-data night-muted" style={{ fontSize: '11px' }}>
                            {payload === undefined || payload === 'loading'
                              ? 'Loading…'
                              : payload === 'error'
                                ? 'Could not load.'
                                : `${payload.showPlaylist.fppInstanceId} · ${payload.showPlaylist.playlist}`}
                          </td>
                          <td className="t-data night-muted" style={{ fontSize: '11px' }}>
                            {payload === undefined || payload === 'loading'
                              ? 'Loading…'
                              : payload === 'error'
                                ? 'Could not load.'
                                : `${payload.resting.fppInstanceId} · ${payload.resting.playlist}`}
                          </td>
                          <td className="t-data night-muted">{obj.currentRevision}</td>
                          <td>
                            {isActive ? (
                              <span className="t-meta night-state night-state--active">Active</span>
                            ) : (
                              <span className="t-meta night-state">Inactive</span>
                            )}
                          </td>
                        </tr>
                      )
                    })}
                  </tbody>
                </table>
                <p className="table__footer-note">
                  Every cue action, asset and interlock signal a definition names must belong to the same show as
                  the definition itself.
                </p>
              </div>
            )}
          </section>
        </>
      )}
    </ShowWorkspaceFrame>
  )
}
