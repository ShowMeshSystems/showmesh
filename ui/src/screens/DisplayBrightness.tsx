import { Link } from 'react-router-dom'
import { RuledStrip, StatusPair } from '../kit'
import { playerStateStatus } from './displayBrightnessModel'
import { useFPPBrightness } from './useFPPBrightness'

/** One FPP's place against the display ceiling, for the FPP drawer. The control lives on Settings > Brightness. */
export function DisplayBrightnessPlayerReadout({ instanceId }: { instanceId: string }) {
  const [read] = useFPPBrightness()
  const player = read.doc?.players.find((p) => p.instanceId === instanceId)
  if (read.doc === null) {
    return read.error !== null ? <RuledStrip absence="failed" label="Read failed" fact={read.error} /> : <RuledStrip absence="loading" label="Reading" fact="Asking the coordinator for the display brightness." />
  }
  if (player === undefined) {
    return <RuledStrip absence="unavailable" label="Not listed" fact="This player is not in the saved FPP players. Save the list to bring it into the display brightness." />
  }
  const status = playerStateStatus(player.state)
  const target = read.doc.ceiling?.targetPercent
  return (
    <>
      <p className="sm-body">
        <StatusPair tone={status.tone} label={status.word} />{' '}
        {target !== undefined ? `The display brightness is ${target}%.` : 'No display brightness has been set.'}{' '}
        <Link to="/settings/brightness">Change it on the Brightness tab</Link>
      </p>
      {player.reason !== undefined && <p className="sm-small sm-muted">{player.reason}</p>}
    </>
  )
}
