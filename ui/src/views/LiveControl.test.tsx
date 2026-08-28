import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LiveControl } from './LiveControl'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeFPPInstance, makeModel, makeNode, makeResolumeInstance } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'

const { dispatchNightCommand } = vi.hoisted(() => ({
  dispatchNightCommand: vi.fn(),
}))
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
    expect(screen.getByRole('link', { name: 'Back to dashboard' })).toHaveClass('button--secondary')
    expect(screen.getByText('Control status').closest('section')).toHaveClass('live-control-section')
    expect(screen.getByText('Coordinator').closest('.live-control-status')).toHaveClass('live-control-status--good')
    const fppUnavailable = screen.getByText('Unavailable: No FPP instance is configured on this coordinator.')
    expect(fppUnavailable).toBeInTheDocument()
    expect(fppUnavailable.closest('.section-notice')).toHaveClass('notice--warning')
    expect(fppUnavailable.closest('section')).toHaveAttribute('aria-labelledby', 'fpp-controls')
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

  it('marks retained FPP, audio, and Resolume evidence stale while the browser is disconnected', () => {
    renderView({
      connection: { kind: 'reconnecting', attempt: 1, nextAttemptAt: 1, lastError: 'network lost' },
      snapshotReceivedAt: 0,
      fpp: [makeFPPInstance('fpp-main')],
      nodes: [makeNode('audio-node', { audio: [{ ...makeEvidence({ signal: 'audio.session.state' }), resource: { kind: 'audio_session', id: 'program' } }] })],
      resolume: [makeResolumeInstance('resolume-main')],
    })

    expect(screen.getByText(/Showing last known data, received/)).toBeInTheDocument()
    for (const label of ['FPP', 'Audio', 'Resolume']) {
      const card = screen.getByText(label).closest('.live-control-status')
      expect(card).toHaveTextContent('Stale')
      expect(card).not.toHaveClass('live-control-status--good')
    }
    expect(screen.queryByText('Available')).not.toBeInTheDocument()
  })

  it('does not present configured evidence as live before a coordinator snapshot arrives', () => {
    renderView({
      connection: { kind: 'connecting' },
      snapshotReceivedAt: null,
      fpp: [makeFPPInstance('fpp-main')],
      nodes: [makeNode('audio-node', { audio: [{ ...makeEvidence({ signal: 'audio.session.state' }), resource: { kind: 'audio_session', id: 'program' } }] })],
      resolume: [makeResolumeInstance('resolume-main')],
    })

    expect(screen.getByText('No data received from the coordinator yet.')).toBeInTheDocument()
    for (const label of ['FPP', 'Audio', 'Resolume']) {
      const card = screen.getByText(label).closest('.live-control-status')
      expect(card).toHaveTextContent('Unobserved')
      expect(card).not.toHaveClass('live-control-status--good')
    }
  })

  it('keeps failed live evidence distinct from unavailable and unknown states', () => {
    renderView({
      fpp: [makeFPPInstance('fpp-main', { health: 'failed' })],
      resolume: [makeResolumeInstance('resolume-main', { health: 'failed' })],
    })

    expect(screen.getByText('FPP').closest('.live-control-status')).toHaveTextContent('Failed')
    expect(screen.getByText('FPP').closest('.live-control-status')).toHaveClass('live-control-status--bad')
    expect(screen.getByText('Resolume').closest('.live-control-status')).toHaveTextContent('Failed')
    expect(screen.getByText('Audio').closest('.live-control-status')).toHaveTextContent('Unobserved')
  })

  it('preserves a successful Show Night command outcome on the control page', async () => {
    dispatchNightCommand.mockResolvedValue({
      serverTime: '2026-08-27T00:00:00Z',
      command: {
        command: 'start-night',
        outcome: 'applied',
        attributionDegraded: false,
      },
      session: {},
    })
    const user = userEvent.setup()
    renderView({
      session: makeAuthenticatedSession({ scopes: ['night:command'] }),
    })

    await user.click(screen.getByRole('button', { name: 'Start night' }))
    expect(await screen.findByText('Start night accepted and applied.')).toBeInTheDocument()
  })
})
