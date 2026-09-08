import { formatClock } from '../domain/time'
import { RuledStrip } from '../kit'
import type { SaveOutcome } from '../domain/save'

export type StaleWrite = Extract<SaveOutcome<unknown>, { kind: 'stale' }>

/** Every config editor's refusal for D-014 B, so the wording cannot drift between them. */
export function StaleWriteStrip({ stale, onReload }: { stale: StaleWrite; onReload: () => void }) {
  return (
    <RuledStrip
      absence="failed"
      label="Stale write"
      fact={`Revision ${stale.loadedRevision} was loaded, but revision ${stale.currentRevision} is now current, saved by ${stale.changedBy ?? 'unknown principal'} ${formatClock(stale.changedAt) ?? 'at an unrecorded time'}. Nothing was written.`}
      detail={
        <>
          {stale.changedFields.length === 0 ? (
            'No stored field differs, so the other write saved the same content.'
          ) : (
            <>
              Changed: <span className="sm-data">{stale.changedFields.join(', ')}</span>.
            </>
          )}{' '}
          <button type="button" className="sm-linkbutton" onClick={onReload}>
            Reload and start again
          </button>
        </>
      }
    />
  )
}
