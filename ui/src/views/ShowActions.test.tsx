import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowActions } from './ShowActions'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Same isolation pattern as Macros.test.tsx: mock the one API call this
// view makes.
const { listConfigObjects } = vi.hoisted(() => ({ listConfigObjects: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
})

function renderShowActions(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <ShowActions />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'],
})

const listResponse = {
  serverTime: '2026-08-18T00:00:00Z',
  kind: 'show.action' as const,
  objects: [
    { id: 'projectors-on', label: 'Projectors on', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-18T00:00:00Z' },
  ],
}

describe('ShowActions', () => {
  it('renders the action list with no filter by default', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    renderShowActions(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText('Projectors on')).toBeVisible())
    expect(listConfigObjects).toHaveBeenCalledWith('show.action', undefined)
  })

  // E7-3 deliverable 4: the UI half of `?show=` parity with the CLI and
  // API.
  it('narrows the list by show when the operator types into the show filter', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    const user = userEvent.setup()
    renderShowActions(makeModel({ session: operatorSession }))

    await waitFor(() => expect(listConfigObjects).toHaveBeenCalledWith('show.action', undefined))
    await user.type(screen.getByLabelText('Narrow by show'), 'halloween-2026')
    await waitFor(() => expect(listConfigObjects).toHaveBeenCalledWith('show.action', 'halloween-2026'))
  })
})
