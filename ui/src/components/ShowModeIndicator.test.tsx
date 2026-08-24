import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowModeIndicator } from './ShowModeIndicator'

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
    render(<ShowModeIndicator />)

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Show/))
    expect(screen.getByLabelText('Show mode').className).toContain('show-mode--show')
  })

  it('marks the built-in default as a default rather than as a stored choice', async () => {
    getShowModeConfig.mockResolvedValue(programDefault)
    render(<ShowModeIndicator />)

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/Program/))
    expect(screen.getByLabelText('Show mode').textContent).toMatch(/default/i)
  })

  it('does not render "program" while it is still loading', () => {
    getShowModeConfig.mockReturnValue(new Promise(() => {}))
    render(<ShowModeIndicator />)

    const badge = screen.getByLabelText('Show mode')
    expect(badge.textContent).toMatch(/loading/i)
    expect(badge.textContent).not.toMatch(/Program/)
    expect(badge.className).toContain('show-mode--unknown')
  })

  it('says the mode could not be read rather than inventing one when the read fails', async () => {
    getShowModeConfig.mockRejectedValue(new Error('network error requesting /config/show.mode'))
    render(<ShowModeIndicator />)

    await waitFor(() => expect(screen.getByLabelText('Show mode').textContent).toMatch(/cannot be read/i))
    const badge = screen.getByLabelText('Show mode')
    expect(badge.textContent).not.toMatch(/Program/)
    expect(badge.textContent).not.toMatch(/Show/)
  })

  // ADR-033 decision 3: a behaviour caused by the mode states the mode as
  // its reason, and this is where an operator reads it.
  it('carries the mode-stated effect as the badge title', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    render(<ShowModeIndicator />)

    await waitFor(() => screen.getByLabelText('Show mode'))
    expect(screen.getByLabelText('Show mode')).toHaveAttribute(
      'title',
      showConfigured.resolumeWebSocketEffect,
    )
  })
})
