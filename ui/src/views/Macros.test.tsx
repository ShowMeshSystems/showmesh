import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Macros } from './Macros'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// `Show Automation.dc.html` (UI-DESIGN-GUIDE.md's screen map) replaces the
// standalone macro list with the show workspace's Automation tab — see
// Macros.tsx's own doc comment. These tests cover ONLY this file's own
// remaining job (resolving `showId` and delegating, or stating there is
// none); AutomationWorkspace's own behavior is covered by
// automation/AutomationWorkspace.test.tsx.
const { listConfigObjects, listActionBindings } = vi.hoisted(() => ({ listConfigObjects: vi.fn(), listActionBindings: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects, listActionBindings }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
  listActionBindings.mockReset()
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
          <Route path="/macros" element={<Macros />} />
          <Route path="/shows/:showId/automation" element={<Macros />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Macros (old un-scoped mounting)', () => {
  it('states that Automation moved into a show workspace tab, rather than guessing a show', () => {
    render_(makeModel({ session: operatorSession }), '/macros')
    expect(screen.getByRole('status')).toHaveTextContent(/Automation is a show workspace tab now/)
    expect(listConfigObjects).not.toHaveBeenCalled()
  })
})

describe('Macros (show-scoped mounting)', () => {
  it('delegates to the Automation workspace once a showId is present in the route', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-28T00:00:00Z', kind: 'show.macro', objects: [] })
    listActionBindings.mockResolvedValue([])
    render_(makeModel({ session: operatorSession }), '/shows/winter-ridge-2026/automation')
    expect(await screen.findByText('Macros')).toBeVisible()
  })
})
