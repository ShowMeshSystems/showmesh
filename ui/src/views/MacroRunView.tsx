import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { getMacroRun } from '../api'
import { describeApiError } from '../app/session'
import { LoadingBlock, FailedBlock } from '../components/SharedLayouts'
import { MacroRunDetail } from './automation/MacroRunDetail'

/**
 * `Show Automation.dc.html`'s run-detail inspector variant, reached at
 * `/shows/:showId/automation/macros/:macroId/runs/:runId` (ROUTE-MAP.md).
 * Same show-discovery fallback as `MacroDetail.tsx` for this file's OLD
 * un-scoped mounting (`/macros/:id/runs/:runId`): `MacroRunSummary` (and
 * so `MacroRun`) carries its own `show` field, fetched once via the run
 * itself rather than guessed.
 */
export function MacroRunView() {
  const params = useParams<{ id?: string; macroId?: string; runId?: string; showId?: string }>()
  const macroId = params.macroId ?? params.id
  const runId = params.runId
  const [discoveredShow, setDiscoveredShow] = useState<string | null>(null)
  const [discoverError, setDiscoverError] = useState<string | null>(null)

  useEffect(() => {
    if (params.showId !== undefined || runId === undefined) return
    let cancelled = false
    getMacroRun(runId)
      .then((resp) => {
        if (!cancelled) setDiscoveredShow(resp.run.show)
      })
      .catch((err: unknown) => {
        if (!cancelled) setDiscoverError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [params.showId, runId])

  const showId = params.showId ?? discoveredShow ?? undefined

  if (runId === undefined || macroId === undefined) {
    return <FailedBlock title="No run to show" reason="This route did not carry both a macro id and a run id." />
  }
  if (showId === undefined) {
    if (discoverError !== null) {
      return <FailedBlock title="Could not resolve this run's show" reason={discoverError} />
    }
    return <LoadingBlock title="Loading run" reason="Resolving this run's show…" />
  }
  return <MacroRunDetail showId={showId} macroId={macroId} runId={runId} />
}
