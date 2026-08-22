import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NightSessions } from './NightSessions'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

const { listConfigObjects } = vi.hoisted(() => ({ listConfigObjects: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listConfigObjects }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
})

function renderView(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <NightSessions />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('NightSessions', () => {
  it('renders the list, requesting the night.session kind specifically', async () => {
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      objects: [{ id: 'halloween-night', label: 'Halloween Night', currentRevision: 3, updatedAt: '2026-08-22T00:00:00Z' }],
    })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    await waitFor(() => expect(screen.getByText('Halloween Night')).toBeVisible())
    expect(listConfigObjects).toHaveBeenCalledWith('night.session')
  })

  it('renders "no night sessions are configured yet" for an empty list', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-22T00:00:00Z', objects: [] })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    await waitFor(() => expect(screen.getByText(/no night sessions are configured yet/i)).toBeVisible())
  })

  it('renders a read-scope refusal without ever calling listConfigObjects', () => {
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['node:read'] }) }))
    expect(screen.getAllByText(/does not include/).length).toBeGreaterThan(0)
    expect(listConfigObjects).not.toHaveBeenCalled()
  })

  it('renders "New night session" as a real link when config:write is held', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-22T00:00:00Z', objects: [] })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run', 'config:write'] }) }))
    const link = await screen.findByRole('link', { name: 'New night session' })
    expect(link).toHaveAttribute('href', '/config/night.session/new')
  })

  it('renders "New night session" disabled with a stated reason when config:write is not held', async () => {
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-22T00:00:00Z', objects: [] })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    const button = await screen.findByRole('button', { name: 'New night session' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
  })
})
