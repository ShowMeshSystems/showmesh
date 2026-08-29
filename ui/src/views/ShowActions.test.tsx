import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowActions } from './ShowActions'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// An action never appears on its own in a running show (UI-DESIGN-GUIDE.md
// section 3), so the standalone action list folded into the Automation
// tab — see ShowActions.tsx's own doc comment and Macros.test.tsx's
// identical pattern.
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
          <Route path="/actions" element={<ShowActions />} />
          <Route path="/shows/:showId/automation" element={<ShowActions />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('ShowActions (old un-scoped mounting)', () => {
  it('states that actions moved into a show workspace tab, rather than guessing a show', () => {
    render_(makeModel({ session: operatorSession }), '/actions')
    expect(screen.getByRole('status')).toHaveTextContent(/Actions live inside a show/)
    expect(listConfigObjects).not.toHaveBeenCalled()
  })
})

describe('ShowActions (show-scoped mounting)', () => {
  it('delegates to the Automation workspace once a showId is present in the route', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-28T00:00:00Z', kind: 'show.macro', objects: [] })
    listActionBindings.mockResolvedValue([])
    render_(makeModel({ session: operatorSession }), '/shows/winter-ridge-2026/automation')
    expect(await screen.findByText('Actions')).toBeVisible()
  })
})
