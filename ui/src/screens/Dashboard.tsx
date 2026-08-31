import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { getCurrentNightSession, type NightSessionState } from '../api'
import {
  AttentionRow,
  BlankingPlate,
  PageTitle,
  RuledStrip,
  Section,
  StatTile,
  StatusPair,
  Tiles,
} from '../kit'
import { useModelContext } from '../app/ModelContext'
import { effectiveServerTimeIso, formatDuration } from '../domain/time'
import {
  attentionItems,
  fleetCounts,
  fppDetail,
  nodesDetail,
  staleSignalLeader,
  nextStartVerdict,
  showInProgress,
  type ReadinessVerdict,
} from './dashboardModel'

/**
 * The change stream only announces a night-session CHANGE, so the model
 * holds null until a frame arrives. One GET seeds it; a live frame always
 * wins over what this fetched.
 */
function useNightSession(): NightSessionState | null {
  const model = useModelContext()
  const [seeded, setSeeded] = useState<NightSessionState | null>(null)

  useEffect(() => {
    let cancelled = false
    getCurrentNightSession()
      .then((response) => {
        if (!cancelled) setSeeded(response.session)
      })
      .catch(() => {
        // A failed seed leaves the block reading "not heard from", which is
        // what it is. It never blanks a value the stream already delivered.
      })
    return () => {
      cancelled = true
    }
  }, [])

  return model.nightSession ?? seeded
}

function Verdict({ verdict, action }: { verdict: ReadinessVerdict; action?: React.ReactNode }) {
  return (
    <div>
      <StatusPair tone={verdict.tone} label={verdict.state} appearance="word" />
      <p className="sm-verdict">
        {verdict.fact}
        {verdict.detail !== null && <span className="sm-verdict__detail">{verdict.detail}</span>}
        {verdict.gated === true && (
          <span className="sm-verdict__detail">
            <code className="sm-data">start-night</code> is withheld until readiness runs again.
          </span>
        )}
        {verdict.action && action !== undefined && <> {action}</>}
      </p>
    </div>
  )
}

export function Dashboard() {
  const model = useModelContext()
  const nowIso = effectiveServerTimeIso(model.serverTime, model.serverTimeReceivedAt, Date.now())
  const session = useNightSession()
  const items = attentionItems(model, nowIso)
  const counts = fleetCounts(model)
  const staleLeader = staleSignalLeader(model)
  const inProgress = showInProgress(session)
  const nextStart = nextStartVerdict(session, nowIso)

  const snapshotAge =
    model.snapshotReceivedAt === null ? null : formatDuration(Date.now() - model.snapshotReceivedAt)
  const activeShow = model.currentRuns?.activeShow
  const showLine =
    activeShow === undefined
      ? 'No snapshot has arrived yet'
      : activeShow.configured && activeShow.show !== null
        ? `${activeShow.show} is the active show`
        : 'No show is active'

  return (
    <>
      <PageTitle
        title="Dashboard"
        lede={snapshotAge === null ? showLine : `Snapshot ${snapshotAge} ago · ${showLine}`}
      />

      <Section id="db-ready" title="Readiness">
        {session === null ? (
          <RuledStrip
            absence="loading"
            label="Not heard from"
            fact="The night session has not reported to this device yet."
            detail="The stream announces a change, not a state, so this stays blank until the session reports or a read succeeds."
          />
        ) : (
          <div className="sm-dashboard__verdicts">
            {inProgress !== null && <Verdict verdict={inProgress} />}
            {nextStart !== null && (
              <Verdict verdict={nextStart} action={<Link to="/night">Open Show Night</Link>} />
            )}
          </div>
        )}
      </Section>

      <Section
        id="db-attention"
        title="Needs you"
        aside={
          items.length > 0 ? (
            <span className="sm-small sm-muted">
              {items.length} {items.length === 1 ? 'item' : 'items'}
            </span>
          ) : undefined
        }
      >
        {items.length === 0 ? (
          <BlankingPlate
            absence="empty"
            stamp="Clear"
            eyebrow="Attention · empty"
            title="Nothing needs you"
            detail="No failed, held, or unknown conditions are reported. That is not proof the show looks right, only that nothing has asked for you."
          />
        ) : (
          <div className="sm-dashboard__attention">
            {items.map((item) => (
              <AttentionRow
                key={item.key}
                tone={item.tone}
                state={item.state}
                appearance="word"
                fact={
                  <>
                    <Link to={item.to}>{item.subject}</Link> {item.fact}
                  </>
                }
                detail={
                  <>
                    {item.detail} <Link to={item.to}>Open</Link>
                  </>
                }
              />
            ))}
          </div>
        )}
      </Section>

      <Section
        id="db-health"
        title="System health"
        aside={<Link to="/monitor/fleet">Open Monitor →</Link>}
      >
        <Tiles>
          <StatTile
            label="Nodes"
            value={`${counts.nodesOnline} / ${counts.nodesTotal}`}
            detail={nodesDetail(counts)}
            to="/monitor/fleet"
          />
          <StatTile
            label="FPP players"
            value={`${counts.fppHealthy} / ${counts.fppTotal}`}
            detail={fppDetail(model.fpp)}
            to="/monitor/fleet"
          />
          <StatTile
            label="Resolume"
            value={`${counts.resolumeHealthy} / ${counts.resolumeTotal}`}
            detail={counts.resolumeTotal === 0 ? 'none configured' : 'healthy'}
            to="/settings/resolume"
          />
          <StatTile
            label="Signals current"
            value={`${counts.signals.current} / ${counts.signals.total}`}
            detail={
              counts.signals.total === 0
                ? 'nothing collected yet'
                : `${counts.signals.stale} stale · ${counts.signals.unobserved} unobserved`
            }
            to="/monitor/signals"
          />
        </Tiles>
        <p className="sm-section__footnote">
          Health is each resource's own report, not a ShowMesh-side verdict.
          {staleLeader !== null && (
            <>
              {' '}
              {staleLeader.count} of the stale signals belong to {staleLeader.label} alone.
            </>
          )}
        </p>
      </Section>
    </>
  )
}
