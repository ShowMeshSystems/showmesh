import { Link } from 'react-router-dom'

export type FPPPluginReportsRefusedInstance = {
  instanceId: string
}

/**
 * The shell-level, non-dismissible banner shown on every screen while an
 * FPP instance's playlist-entry reports are being refused (a sequence
 * regression). One block per instance, each linking to that instance's own
 * Monitor inspector, which carries the reason and the clear button.
 */
export function FPPPluginReportsRefusedBanner({ instances }: { instances: FPPPluginReportsRefusedInstance[] }) {
  if (instances.length === 0) return null
  return (
    <>
      {instances.map((instance) => (
        <div className="sm-wdbanner" role="region" aria-label={`FPP ${instance.instanceId} reports refused`} key={instance.instanceId}>
          <div className="sm-wdbanner__fact">
            <span className="sm-wdbanner__kind" role="alert">
              {`FPP ${instance.instanceId} reports are being refused. Open Monitor and clear the playlist observation.`}
            </span>
          </div>
          <div className="sm-wdbanner__actions">
            <Link to={`/monitor/fleet?resource=${encodeURIComponent(`fpp:${instance.instanceId}`)}`}>Open Monitor</Link>
          </div>
        </div>
      ))}
    </>
  )
}
