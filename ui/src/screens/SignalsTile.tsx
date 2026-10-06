import { Fragment } from 'react'
import { StatTile, TileCount } from '../kit'
import { signalTileParts, type SignalCounts } from '../domain/evidence'

export function SignalsTile({ counts }: { counts: SignalCounts }) {
  const parts = signalTileParts(counts)
  const detail =
    counts.total === 0
      ? 'nothing collected yet'
      : parts.map((part, index) => (
          <Fragment key={part.text}>
            {index > 0 && ' · '}
            <TileCount tone={part.failed ? 'bad' : undefined}>{part.text}</TileCount>
          </Fragment>
        ))
  return <StatTile label="Signals current" value={`${counts.current} / ${counts.measurable}`} detail={detail} to="/monitor/signals" />
}
