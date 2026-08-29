import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { NotFound } from './NotFound'

afterEach(cleanup)

function renderAt(pathname: string) {
  return render(
    <MemoryRouter initialEntries={[pathname]}>
      <NotFound />
    </MemoryRouter>,
  )
}

describe('NotFound', () => {
  it('names the requested path and offers a way back to the dashboard', () => {
    renderAt('/some/unknown/path')
    expect(screen.getByRole('heading', { name: 'No page at this address' })).toBeInTheDocument()
    expect(screen.getByText('/some/unknown/path')).toBeInTheDocument()
    const link = screen.getByRole('link', { name: 'Go to Dashboard' })
    expect(link).toHaveAttribute('href', '/')
  })

  it('states plainly that old addresses are not redirected', () => {
    renderAt('/nodes')
    expect(screen.getAllByText(/not redirected/).length).toBeGreaterThan(0)
  })

  it('renders the full old-to-new route table with section heading "Where it probably went"', () => {
    renderAt('/nodes')
    expect(screen.getByRole('heading', { name: 'Where it probably went' })).toBeInTheDocument()
    expect(screen.getByRole('table')).toBeInTheDocument()
    // A representative sample across the table, not every row -- see
    // ROUTE-MAP.md for the authoritative full list this is generated from.
    expect(screen.getByRole('link', { name: 'Monitor › Signals' })).toHaveAttribute('href', '/monitor/signals')
    expect(screen.getByRole('link', { name: 'Monitor › Activity' })).toHaveAttribute('href', '/monitor/activity')
    expect(screen.getAllByRole('link', { name: 'Monitor › Fleet' }).length).toBeGreaterThan(0)
    expect(screen.getByRole('link', { name: 'Shows › Automation' })).toHaveAttribute('href', '/shows')
  })

  it('never links a route ROUTE-MAP.md marks BLOCKED, and says so in prose instead', () => {
    renderAt('/config/show.active')
    expect(screen.queryByRole('link', { name: /BLOCKED/ })).not.toBeInTheDocument()
    expect(screen.getAllByText('BLOCKED, awaiting owner').length).toBeGreaterThan(0)
  })

  it('offers a live "Open <destination>" shortcut when the requested path matches a known old address', () => {
    renderAt('/observations/barn-player')
    const shortcut = screen.getByRole('link', { name: 'Open Monitor › Signals' })
    expect(shortcut).toHaveAttribute('href', '/monitor/signals')
  })

  it('offers no live shortcut, only the dashboard link, for an address matching only a BLOCKED row', () => {
    renderAt('/playlists/readiness')
    expect(screen.queryByRole('link', { name: /^Open /i })).not.toBeInTheDocument()
  })

  it('offers no live shortcut for a path matching nothing on the route map', () => {
    renderAt('/totally/unknown')
    expect(screen.queryByRole('link', { name: /^Open /i })).not.toBeInTheDocument()
  })
})
