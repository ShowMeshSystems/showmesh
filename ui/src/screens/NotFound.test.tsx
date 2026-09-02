import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { NotFound } from './NotFound'

afterEach(cleanup)

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route path="*" element={<NotFound />} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('NotFound · shape matches the mock, not a hatched plate', () => {
  it('renders an eyebrow, a fixed h1, the path, and the explanation sentence', () => {
    renderAt('/observations/barn-player')
    expect(screen.getByText('Not found')).toBeInTheDocument()
    expect(screen.getByRole('heading', { level: 1, name: 'No page at this address' })).toBeInTheDocument()
    expect(screen.getByText('/observations/barn-player')).toBeInTheDocument()
    expect(
      screen.getByText(
        'The show is running normally, the bar above is live. Nothing is wrong with the installation; this address just does not exist.',
      ),
    ).toBeInTheDocument()
  })

  it('carries no hatched plate: BlankingPlate is gone', () => {
    const { container } = renderAt('/observations/barn-player')
    expect(container.querySelector('.sm-plate')).toBeNull()
  })
})

describe('NotFound · the four folded addresses D-003 added after the mock was drawn', () => {
  it('/monitor/readiness folded into Shows › Playlists', () => {
    renderAt('/monitor/readiness')
    expect(screen.getByText('That screen is now Shows › Playlists')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Shows › Playlists' })).toHaveAttribute('href', '/shows/playlists')
  })

  it('/monitor/fleet/playlist-definitions/... folded into Shows › Playlists', () => {
    renderAt('/monitor/fleet/playlist-definitions/barn-player/main-show')
    expect(screen.getByText('That screen is now Shows › Playlists')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Shows › Playlists' })).toHaveAttribute('href', '/shows/playlists')
  })

  it('shows/:id/night-sessions folded into Show Night', () => {
    renderAt('/shows/winter-ridge-2026/night-sessions')
    expect(screen.getByText('That screen is now Show Night')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Show Night' })).toHaveAttribute('href', '/night')
  })

  it('shows/:id/night-sessions/:sessionId detail route folded into Show Night', () => {
    renderAt('/shows/winter-ridge-2026/night-sessions/2026-08-30')
    expect(screen.getByText('That screen is now Show Night')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Show Night' })).toHaveAttribute('href', '/night')
  })

  it('/assets/manifest folded into Monitor › Manifest', () => {
    renderAt('/assets/manifest')
    expect(screen.getByText('That screen is now Monitor › Manifest')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Go to Monitor › Manifest' })).toHaveAttribute('href', '/monitor/manifest')
  })
})
