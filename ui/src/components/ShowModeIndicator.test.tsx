import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { ShowModeIndicator } from './ShowModeIndicator'

// Now a router Link (it takes the operator to /config, the route holding
// ShowModePanel), so rendering it needs a Router in scope, same as any
// other react-router-dom Link/NavLink test in this codebase.
function renderIndicator() {
  return render(
    <MemoryRouter>
      <ShowModeIndicator />
    </MemoryRouter>,
  )
}

// ADR-033 decision 3's own component test. The point under test is that
// the three states are rendered DISTINCTLY: loading is not program, and a
// failed read is not program either. Inventing a mode from no evidence is
// the specific failure this indicator must not have.
const { getShowModeConfig } = vi.hoisted(() => ({ getShowModeConfig: vi.fn() }))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShowModeConfig }
})

afterEach(() => {
  cleanup()
  getShowModeConfig.mockReset()
})

const programDefault = {
  serverTime: '2026-08-23T21:00:00Z',
  kind: 'show.mode',
  revision: 0,
  payload: { mode: 'program' },
  updatedAt: '2026-08-23T21:00:00Z',
  createdByPrincipalId: null,
  createdByPrincipalName: null,
  source: 'default',
  resolumeWebSocketEffect: 'program mode: the Resolume WebSocket wake-up channel is held OPEN.',
}

const showConfigured = {
  ...programDefault,
  revision: 4,
  payload: { mode: 'show' },
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
  resolumeWebSocketEffect: 'show mode: the Resolume WebSocket wake-up channel is held CLOSED.',
}

describe('ShowModeIndicator', () => {
  it('renders the mode once it has been read', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    renderIndicator()

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Show/))
    expect(screen.getByLabelText('Show mode').className).toContain('show-mode--show')
  })

  it('marks the built-in default as a default rather than as a stored choice', async () => {
    getShowModeConfig.mockResolvedValue(programDefault)
    renderIndicator()

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Program/))
    expect(screen.getByLabelText('Show mode').textContent).toMatch(/default/i)
  })

  it('does not render "program" while it is still loading', () => {
    getShowModeConfig.mockReturnValue(new Promise(() => {}))
    renderIndicator()

    const badge = screen.getByLabelText('Show mode')
    expect(badge.textContent).toMatch(/loading/i)
    expect(badge.textContent).not.toMatch(/Program/)
    expect(badge.className).toContain('show-mode--unknown')
  })

  it('says the mode could not be read rather than inventing one when the read fails', async () => {
    getShowModeConfig.mockRejectedValue(new Error('network error requesting /config/show.mode'))
    renderIndicator()

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/cannot be read/i))
    const badge = screen.getByLabelText('Show mode')
    expect(badge.textContent).not.toMatch(/Program/)
    expect(badge.textContent).not.toMatch(/Show/)
  })

  // ADR-033 decision 3: a behaviour caused by the mode states the mode as
  // its reason, and this is where an operator reads it.
  it('carries the mode-stated effect as the badge title', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    renderIndicator()

    await waitFor(() => screen.getByLabelText('Show mode'))
    expect(screen.getByLabelText('Show mode')).toHaveAttribute(
      'title',
      showConfigured.resolumeWebSocketEffect,
    )
  })

  // Operator-reported: this read as a clickable affordance but did nothing
  // when clicked. It must actually take the operator to /config, the route
  // holding ShowModePanel (the config:write-gated control), rather than
  // becoming the mode switch itself.
  it('links to /config, where the mode switch actually lives, rather than switching the mode itself', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    renderIndicator()

    await waitFor(() => screen.getByLabelText('Show mode'))
    const badge = screen.getByLabelText('Show mode')
    expect(badge.tagName).toBe('A')
    expect(badge).toHaveAttribute('href', '/config#show-mode')
  })

  // Operator-reported (round two): the badge became a link but kept
  // `role="status"` on the SAME element, which overrides the anchor's
  // implicit `link` role. A screen-reader user navigating by link can no
  // longer find it -- the only route to the mode control. It must be
  // discoverable by its link role, and mode changes must still be
  // announced by a live region somewhere in the component.
  it('is discoverable as a link, not just as a status region', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    renderIndicator()

    const link = await screen.findByRole('link', { name: 'Show mode' })
    expect(link).toHaveAttribute('href', '/config#show-mode')
  })

  it('still announces the mode as a live region, separate from the link', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    renderIndicator()

    const status = await screen.findByRole('status')
    expect(status.textContent).toMatch(/Show/)
    // The status element itself must not be the link (that would put us
    // back in the bug this test guards against).
    expect(status.tagName).not.toBe('A')
  })
})
