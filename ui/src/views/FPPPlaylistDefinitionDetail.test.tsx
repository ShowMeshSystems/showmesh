import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FPPPlaylistDefinitionDetail } from './FPPPlaylistDefinitionDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

const { getFPPPlaylistDefinition, getFPPPlaylistDefinitionEntries } = vi.hoisted(() => ({
  getFPPPlaylistDefinition: vi.fn(),
  getFPPPlaylistDefinitionEntries: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getFPPPlaylistDefinition, getFPPPlaylistDefinitionEntries }
})

afterEach(() => {
  cleanup()
  getFPPPlaylistDefinition.mockReset()
  getFPPPlaylistDefinitionEntries.mockReset()
})

function renderView(
  instanceUuid = 'uuid-1',
  playlistHash = 'abc123',
  model: Model = makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }),
) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/config/fpp-playlist-definitions/${instanceUuid}/${playlistHash}`]}>
        <Routes>
          <Route
            path="/config/fpp-playlist-definitions/:instanceUuid/:playlistHash"
            element={<FPPPlaylistDefinitionDetail />}
          />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const definitionResponse = {
  instanceUuid: 'uuid-1',
  playlistName: 'Opener',
  playlistHash: 'abc123',
  definition: {},
  capturedAt: '2026-08-24T00:00:00Z',
  receivedAt: '2026-08-24T00:05:00Z',
  serverTime: '2026-08-25T00:00:00Z',
}

describe('FPPPlaylistDefinitionDetail', () => {
  it('renders parsed entries in order', async () => {
    getFPPPlaylistDefinition.mockResolvedValue(definitionResponse)
    getFPPPlaylistDefinitionEntries.mockResolvedValue({
      instanceUuid: 'uuid-1',
      playlistHash: 'abc123',
      serverTime: '2026-08-25T00:00:00Z',
      entries: [
        { section: 'leadIn', position: 0, type: 'sequence', sequenceName: 'intro.fseq', mediaName: '' },
        { section: 'mainPlaylist', position: 0, type: 'sequence', sequenceName: 'main.fseq', mediaName: '' },
        { section: 'mainPlaylist', position: 1, type: 'media', sequenceName: '', mediaName: 'song.mp3' },
        { section: 'leadOut', position: 0, type: 'sequence', sequenceName: 'outro.fseq', mediaName: '' },
      ],
    })

    renderView()

    expect(await screen.findByText('Opener')).toBeInTheDocument()
    expect(await screen.findByText('abc123')).toBeInTheDocument()

    const rows = await screen.findAllByRole('row')
    // header + 4 entry rows, in the exact order the response returned them
    expect(rows).toHaveLength(5)
    expect(rows[1]).toHaveTextContent('intro.fseq')
    expect(rows[2]).toHaveTextContent('main.fseq')
    expect(rows[3]).toHaveTextContent('song.mp3')
    expect(rows[4]).toHaveTextContent('outro.fseq')
  })

  it('renders a fetch failure on the entries call distinctly from a not-found', async () => {
    getFPPPlaylistDefinition.mockResolvedValue(definitionResponse)
    getFPPPlaylistDefinitionEntries.mockRejectedValue(new Error('network unreachable'))

    renderView()

    await waitFor(() => expect(getFPPPlaylistDefinitionEntries).toHaveBeenCalled())
    expect(await screen.findByText(/The entries could not be read/)).toBeInTheDocument()
    expect(screen.queryByText(/No stored definition for this instance and hash/)).not.toBeInTheDocument()
  })

  it('renders a 404 on the definition call as "no stored definition", not a fetch failure', async () => {
    getFPPPlaylistDefinition.mockRejectedValue(new ApiError('no definition for this pair', 404, 'not-found'))
    getFPPPlaylistDefinitionEntries.mockRejectedValue(new ApiError('no definition for this pair', 404, 'not-found'))

    renderView()

    expect(await screen.findAllByText(/No stored definition for this instance and hash/)).toHaveLength(2)
    expect(screen.queryByText(/could not be read/)).not.toBeInTheDocument()
  })
})
