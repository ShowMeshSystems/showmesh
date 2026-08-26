import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FPPPlaylistDefinitions } from './FPPPlaylistDefinitions'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// Same isolation pattern as PlaylistReadiness.test.tsx: mock the one API
// call this view makes so its own branching (loaded vs none-reported vs a
// fetch failure) is what these tests exercise, not store.ts's own network
// behavior.
const { listFPPPlaylistDefinitions } = vi.hoisted(() => ({
  listFPPPlaylistDefinitions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listFPPPlaylistDefinitions }
})

afterEach(() => {
  cleanup()
  listFPPPlaylistDefinitions.mockReset()
})

function renderView(model: Model = makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <FPPPlaylistDefinitions />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('FPPPlaylistDefinitions', () => {
  it('renders one row per stored definition, with its hash', async () => {
    listFPPPlaylistDefinitions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      definitions: [
        {
          instanceUuid: 'uuid-1',
          playlistName: 'Opener',
          playlistHash: 'abc123',
          capturedAt: '2026-08-24T00:00:00Z',
          receivedAt: '2026-08-24T00:05:00Z',
          entryCount: 3,
          referenced: true,
        },
        {
          instanceUuid: 'uuid-2',
          playlistName: 'Finale',
          playlistHash: 'def456',
          capturedAt: '2026-08-24T01:00:00Z',
          receivedAt: '2026-08-24T01:05:00Z',
          entryCount: 5,
          referenced: false,
        },
      ],
    })

    renderView()

    expect(await screen.findByText('Opener')).toBeInTheDocument()
    expect(screen.getByText('Finale')).toBeInTheDocument()
    expect(screen.getByText('abc123')).toBeInTheDocument()
    expect(screen.getByText('def456')).toBeInTheDocument()
    expect(screen.getAllByRole('row')).toHaveLength(3) // header + 2 data rows
  })

  it('renders "none reported" as a plain statement, not an empty table', async () => {
    listFPPPlaylistDefinitions.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', definitions: [] })

    renderView()

    expect(await screen.findByText('No FPP instance has reported a playlist definition yet.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })

  it('renders a fetch failure distinctly from "none reported"', async () => {
    listFPPPlaylistDefinitions.mockRejectedValue(new Error('network unreachable'))

    renderView()

    await waitFor(() => expect(listFPPPlaylistDefinitions).toHaveBeenCalled())
    expect(await screen.findByRole('alert')).toHaveTextContent('The stored definitions could not be read')
    expect(screen.queryByText('No FPP instance has reported a playlist definition yet.')).not.toBeInTheDocument()
    expect(screen.queryByRole('table')).not.toBeInTheDocument()
  })
})
