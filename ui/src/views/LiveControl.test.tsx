import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LiveControl } from './LiveControl'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'

const { dispatchNightCommand } = vi.hoisted(() => ({ dispatchNightCommand: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, dispatchNightCommand }
})

afterEach(cleanup)
afterEach(() => dispatchNightCommand.mockReset())

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

  it('preserves a successful Show Night command outcome on the control page', async () => {
    dispatchNightCommand.mockResolvedValue({
      serverTime: '2026-08-27T00:00:00Z',
      command: { command: 'start-night', outcome: 'applied', attributionDegraded: false },
      session: {},
    })
    const user = userEvent.setup()
    renderView({ session: makeAuthenticatedSession({ scopes: ['night:command'] }) })

    await user.click(screen.getByRole('button', { name: 'Start night' }))
    expect(await screen.findByText('Start night accepted and applied.')).toBeInTheDocument()
  })
})
