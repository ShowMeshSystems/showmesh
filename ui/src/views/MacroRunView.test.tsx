import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MacroRunView } from './MacroRunView'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// This file now only resolves `showId` (from the route, or by discovering
// it off the run's own `show` field for this component's OLD un-scoped
// mounting) and hands off to `automation/MacroRunDetail.tsx` — see
// MacroRunView.tsx's own doc comment. MacroRunDetail's own polling/step
// rendering behavior is covered by automation/MacroRunDetail.test.tsx.
const { getMacroRun } = vi.hoisted(() => ({ getMacroRun: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getMacroRun }
})

afterEach(() => {
  cleanup()
  getMacroRun.mockReset()
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
          <Route path="/macros/:id/runs/:runId" element={<MacroRunView />} />
          <Route path="/shows/:showId/automation/macros/:macroId/runs/:runId" element={<MacroRunView />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('MacroRunView', () => {
  it('discovers the show from the run itself for the old un-scoped route, then renders the run detail', async () => {
    getMacroRun.mockResolvedValue({
      serverTime: '2026-08-28T21:07:00Z',
      kind: 'macro-run',
      run: {
        id: 'run-1',
        macroObjectId: 'preshow-lights',
        macroRevision: 3,
        show: 'winter-ridge-2026',
        trigger: 'ui',
        issuerPrincipalId: 'p-1',
        issuerPrincipalName: 'operator-1',
        createdAt: '2026-08-28T20:31:14Z',
        finishedAt: '2026-08-28T20:31:19Z',
        state: 'finished',
        completed: true,
        confirmed: false,
        reason: 'Step 3 expects no response.',
        attributionDegraded: false,
        steps: [],
      },
    })

    render_(makeModel({ session: operatorSession }), '/macros/preshow-lights/runs/run-1')

    expect(await screen.findByText('preshow-lights')).toBeVisible()
    expect(getMacroRun).toHaveBeenCalledWith('run-1')
  })
})
