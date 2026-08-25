import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowCues } from './ShowCues'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Same isolation pattern as ShowActions.test.tsx: mock the API call this
// view makes.
const { listConfigObjects } = vi.hoisted(() => ({
  listConfigObjects: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
})

function renderShowCues(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <ShowCues />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const operatorSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'operator-1', kind: 'human', role: 'operator' },
  scopes: ['show:macro:run'],
})

const listResponse = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.cue' as const,
  objects: [
    { id: 'opening-number', label: 'Opening number', show: 'halloween-2026', currentRevision: 1, updatedAt: '2026-08-25T00:00:00Z' },
    { id: 'closing-number', label: 'Closing number', show: 'halloween-2026', currentRevision: 2, updatedAt: '2026-08-25T00:00:00Z' },
  ],
}

describe('ShowCues', () => {
  it('renders one row per cue with no filter by default', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    renderShowCues(makeModel({ session: operatorSession }))

    await waitFor(() => expect(screen.getByText('Opening number')).toBeVisible())
    expect(screen.getByText('Closing number')).toBeVisible()
    expect(screen.getAllByRole('row')).toHaveLength(3) // header + 2 cues
    expect(listConfigObjects).toHaveBeenCalledWith('show.cue', undefined)
  })

  it('narrows the list by show when the operator types into the show filter', async () => {
    listConfigObjects.mockResolvedValue(listResponse)
    const user = userEvent.setup()
    renderShowCues(makeModel({ session: operatorSession }))

    await waitFor(() => expect(listConfigObjects).toHaveBeenCalledWith('show.cue', undefined))
    await user.type(screen.getByLabelText('Narrow by show'), 'halloween-2026')
    await waitFor(() => expect(listConfigObjects).toHaveBeenCalledWith('show.cue', 'halloween-2026'))
  })

  it('renders the missing-scope reason and does not fetch for a principal with neither read scope', () => {
    renderShowCues(makeModel({ session: makeAuthenticatedSession({ scopes: [] }) }))

    expect(screen.getByRole('status')).toBeVisible()
    expect(listConfigObjects).not.toHaveBeenCalled()
  })
})
