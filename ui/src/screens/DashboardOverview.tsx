import type { NightSessionState } from '../api'
import { Section, StatTile, Tiles } from '../kit'
import { useModelContext } from '../app/ModelContext'
import { audioRouteTile, lifecycleTile, runnerTile, smpteTiles } from './dashboardOverviewModel'

export function DashboardOverview({ session }: { session: NightSessionState | null }) {
  const model = useModelContext()
  const runs = model.currentRuns?.runs ?? []
  const lifecycle = lifecycleTile(session, model.macroRuns)
  const fpp = runnerTile(runs, 'fpp', 'No FPP playlist is running.')
  const audio = runnerTile(runs, 'showmesh-audio', 'No audio playlist is running.')
  const route = audioRouteTile(model.nodes)
  const smpte = smpteTiles(model.nodes, Date.now())

  return (
    <>
      <Section id="db-now" title="Show now">
        <Tiles>
          <StatTile label="Lifecycle" value={lifecycle.value} detail={lifecycle.detail} to="/night" />
          <StatTile label="FPP playback" value={fpp.value} detail={fpp.detail} to="/monitor/fleet" />
          <StatTile label="FPP schedule" value="" detail="The coordinator does not report the FPP schedule yet." notWired />
          <StatTile label="Audio playback" value={audio.value} detail={audio.detail} to="/monitor/fleet" />
          <StatTile label="Audio sync" value={route.value} detail={route.detail} to="/monitor/fleet" />
        </Tiles>
      </Section>

      <Section id="db-smpte" title="SMPTE timecode">
        <Tiles>
          <StatTile label="Source" value={smpte.source.value} detail={smpte.source.detail} />
          <StatTile label="Lock" value={smpte.lock.value} detail={smpte.lock.detail} />
          <StatTile label="Frame rate" value="" detail="The coordinator does not report the frame rate yet." notWired />
          <StatTile label="Offset" value="" detail="The coordinator does not report the offset yet." notWired />
        </Tiles>
      </Section>

      <Section id="db-temps" title="Temperatures">
        <Tiles>
          <StatTile label="Enclosure" value="" detail="No temperature source is connected yet." notWired />
        </Tiles>
      </Section>
    </>
  )
}
