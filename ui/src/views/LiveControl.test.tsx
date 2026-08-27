import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it } from 'vitest'
import { LiveControl } from './LiveControl'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'

afterEach(cleanup)

function renderView(overrides: Parameters<typeof makeModel>[0] = {}) {
  return render(
    <ModelContext.Provider value={makeModel(overrides)}>
      <MemoryRouter>
        <LiveControl />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('LiveControl', () => {
  it('renders capability-driven unavailable states instead of blank control groups', () => {
    renderView()

    expect(screen.getByRole('heading', { name: 'Live Control' })).toBeInTheDocument()
    expect(screen.getByText('Unavailable: No FPP instance is configured on this coordinator.')).toBeInTheDocument()
    expect(screen.getByText('Unavailable: No nodes are currently observed.')).toBeInTheDocument()
    expect(screen.getByText('Unavailable: Resolume is not configured on this coordinator.')).toBeInTheDocument()
    expect(screen.getByText('Unavailable: No brightness control capability is advertised.')).toBeInTheDocument()
  })

  it('keeps lifecycle controls visible and explicitly permission-gated when the session is unknown', () => {
    renderView()

    expect(screen.getByRole('heading', { name: 'Show Night controls' })).toBeInTheDocument()
    const prepare = screen.getByRole('button', { name: 'Prepare site' })
    expect(prepare).toBeDisabled()
    expect(screen.getAllByText(/Waiting to hear from the coordinator what this device may do/).length).toBeGreaterThan(0)
  })
})
