import { useEffect, useState } from 'react'
import { useParams } from 'react-router-dom'
import { getShowMacro } from '../api'
import { LoadingBlock, FailedBlock } from '../components/SharedLayouts'
import { describeApiError } from '../app/session'
import { MacroStepEditor } from './automation/MacroStepEditor'

export interface MacroDetailProps {
  isNew?: boolean
}

/**
 * `Show Automation.dc.html` reaches the macro step editor at
 * `/shows/:showId/automation/macros/:macroId` (ROUTE-MAP.md), which is
 * where `automation/MacroStepEditor.tsx` expects `showId` as a prop. This
 * file's OLD mounting (`/macros/:id`, `/macros/new`) carries no `showId`
 * in the URL at all: a new macro has no id to discover a show from (it
 * must be reached through the show-scoped route), and an existing one
 * discovers it by fetching the macro once (`payload.show`), the same
 * scoped-list-of-one lookup `ShowWorkspace.tsx`'s own overview does for
 * assets.
 */
export function MacroDetail({ isNew = false }: MacroDetailProps) {
  const params = useParams<{ id?: string; macroId?: string; showId?: string }>()
  const macroId = params.macroId ?? params.id
  const [discoveredShow, setDiscoveredShow] = useState<string | null>(null)
  const [discoverError, setDiscoverError] = useState<string | null>(null)

  useEffect(() => {
    if (params.showId !== undefined || isNew || macroId === undefined) return
    let cancelled = false
    getShowMacro(macroId)
      .then((resp) => {
        if (!cancelled) setDiscoveredShow(resp.payload.show)
      })
      .catch((err: unknown) => {
        if (!cancelled) setDiscoverError(describeApiError(err))
      })
    return () => {
      cancelled = true
    }
  }, [params.showId, isNew, macroId])

  const showId = params.showId ?? discoveredShow ?? undefined

  if (isNew) {
    if (showId === undefined) {
      return <FailedBlock title="No show selected" reason="A new macro must be created from a show's Automation tab, which supplies the show." />
    }
    return <MacroStepEditor showId={showId} isNew />
  }

  if (macroId === undefined) {
    return <FailedBlock title="No macro id" reason="This route did not carry a macro id." />
  }
  if (showId === undefined) {
    if (discoverError !== null) {
      return <FailedBlock title="Could not resolve this macro's show" reason={discoverError} />
    }
    return <LoadingBlock title="Loading macro" reason="Resolving this macro's show…" />
  }
  return <MacroStepEditor showId={showId} macroId={macroId} />
}
