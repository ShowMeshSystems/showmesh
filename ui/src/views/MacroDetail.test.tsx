import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MacroDetail } from './MacroDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// This file now only resolves `showId` (from the route directly, or by
// discovering it off the macro itself for this component's OLD un-scoped
// mounting) and hands off to `automation/MacroStepEditor.tsx` — see
// MacroDetail.tsx's own doc comment. MacroStepEditor's own editing
// behavior is covered by automation/MacroStepEditor.test.tsx.
const { getShowMacro, listConfigObjects, listActionBindings, listMacroRuns, getShowMacroRevisions } = vi.hoisted(() => ({
  getShowMacro: vi.fn(),
  listConfigObjects: vi.fn(),
  listActionBindings: vi.fn(),
  listMacroRuns: vi.fn(),
  getShowMacroRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShowMacro, listConfigObjects, listActionBindings, listMacroRuns, getShowMacroRevisions }
})

afterEach(() => {
  cleanup()
  getShowMacro.mockReset()
  listConfigObjects.mockReset()
  listActionBindings.mockReset()
  listMacroRuns.mockReset()
  getShowMacroRevisions.mockReset()
})

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'],
})

function render_(model: Model, path: string) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/macros/new" element={<MacroDetail isNew />} />
          <Route path="/macros/:id" element={<MacroDetail />} />
          <Route path="/shows/:showId/automation/macros/:macroId" element={<MacroDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('MacroDetail (old un-scoped mounting)', () => {
  it('states that a new macro needs a show, since the old /macros/new route carries none', () => {
    render_(makeModel({ session: operatorSession }), '/macros/new')
    expect(screen.getByText(/must be created from a show/)).toBeVisible()
  })

  it('discovers the show from the macro itself, then renders the step editor', async () => {
    getShowMacro.mockResolvedValue({
      serverTime: '2026-08-28T00:00:00Z',
      kind: 'show.macro',
      id: 'preshow-lights',
      revision: 3,
      payload: { show: 'winter-ridge-2026', label: 'Preshow Lights Up', description: '', steps: [] },
      updatedAt: '2026-08-28T00:00:00Z',
      createdByPrincipalId: null,
      createdByPrincipalName: null,
      source: 'api',
    })
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-28T00:00:00Z', kind: 'show.action', objects: [] })
    listActionBindings.mockResolvedValue([])
    listMacroRuns.mockResolvedValue({ serverTime: '2026-08-28T00:00:00Z', kind: 'macro-run', runs: [] })
    getShowMacroRevisions.mockResolvedValue({ serverTime: '2026-08-28T00:00:00Z', kind: 'show.macro', revisions: [] })

    render_(makeModel({ session: operatorSession }), '/macros/preshow-lights')

    expect(await screen.findByText('Preshow Lights Up')).toBeVisible()
    expect(getShowMacro).toHaveBeenCalledWith('preshow-lights')
  })
})
