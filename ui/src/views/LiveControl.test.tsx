import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LiveControl } from './LiveControl'
import { ModelContext } from '../app/ModelContext'
import { makeCurrentRuns, makeEvidence, makeFPPInstance, makeModel, makeNode, makeResolumeInstance } from '../app/test-support/fixtures'
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
    expect(screen.getByText('Control status').closest('section')).toHaveClass('shared-section')
    const statusStrip = screen.getByRole('group', { name: 'Control status' })
    expect(statusStrip).toHaveClass('shared-status-strip')
    expect(statusStrip.parentElement).toHaveClass('live-control-status-region')
    expect(screen.getByText('Coordinator').closest('.shared-status-strip__item')).toHaveClass('shared-status-strip__item--good')
    const runnerUnavailable = screen.getByText('Unavailable: No FPP instance is configured on this coordinator.')
    expect(runnerUnavailable).toBeInTheDocument()
    expect(runnerUnavailable.closest('.shared-state-block')).toHaveClass('shared-state-block--unavailable')
    expect(runnerUnavailable.closest('.shared-state-block')?.parentElement?.closest('section')).toHaveAttribute('aria-labelledby', 'fpp-controls')
    expect(screen.getByText('Unavailable: No nodes are currently observed.')).toBeInTheDocument()
    expect(screen.getByText('Unavailable: Resolume is not configured on this coordinator.')).toBeInTheDocument()
    expect(screen.getByText('Unavailable: No brightness control capability is advertised.')).toBeInTheDocument()
    for (const title of ['Runner playback', 'Audio and render-node controls', 'Resolume controls', 'Brightness ceiling', 'Site control', 'Interlocks', 'Global emergency stop']) {
      expect(screen.getByRole('heading', { name: title, level: 3 })).toBeVisible()
    }
  })

  it('keeps lifecycle controls visible and explicitly permission-gated when the session is unknown', () => {
    renderView()

    expect(screen.getByRole('heading', { name: 'Show Night lifecycle' })).toBeInTheDocument()
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
      const card = screen.getByText(label).closest('.shared-status-strip__item')
      expect(card).toHaveTextContent('Stale')
      expect(card).not.toHaveClass('shared-status-strip__item--good')
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
      const card = screen.getByText(label).closest('.shared-status-strip__item')
      expect(card).toHaveTextContent('Unobserved')
      expect(card).not.toHaveClass('shared-status-strip__item--good')
    }
  })

  it('keeps failed live evidence distinct from unavailable and unknown states', () => {
    renderView({
      fpp: [makeFPPInstance('fpp-main', { health: 'failed' })],
      resolume: [makeResolumeInstance('resolume-main', { health: 'failed' })],
    })

    expect(screen.getByText('FPP').closest('.shared-status-strip__item')).toHaveTextContent('Failed')
    expect(screen.getByText('FPP').closest('.shared-status-strip__item')).toHaveClass('shared-status-strip__item--bad')
    expect(screen.getByText('Resolume').closest('.shared-status-strip__item')).toHaveTextContent('Failed')
    expect(screen.getByText('Audio').closest('.shared-status-strip__item')).toHaveTextContent('Unobserved')
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

  it('keeps operator task groups in command order and reaches Active Show and Mode selection without placing configuration writes here', () => {
    renderView({ currentRuns: makeCurrentRuns() })

    expect(screen.getByText('Active: halloween-2026 (generation 7). Freshness: current-runs projection.')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Select active show' })).toHaveAttribute('href', '/config/show.active')
    expect(screen.getByRole('link', { name: 'Open Mode selection' })).toHaveAttribute('href', '/config#show-mode')
    expect(screen.queryByRole('button', { name: /save show mode/i })).not.toBeInTheDocument()

    const headings = screen.getAllByRole('heading', { level: 2 }).map((heading) => heading.textContent)
    expect(headings.slice(headings.indexOf('Control status'), headings.indexOf('Not available on this coordinator') + 1)).toEqual([
      'Control status',
      'Active Show and Mode',
      'Show Night lifecycle',
      'Runner playback',
      'Audio and render-node controls',
      'Resolume controls',
      'Macros and exposed Actions',
      'Not available on this coordinator',
    ])
  })

  it('keeps an unavailable active-show projection distinct from an unobserved one', () => {
    const { rerender } = renderView({ currentRuns: null, currentRunsFetchFailed: false })
    expect(screen.getByText('Unobserved: waiting for the coordinator current-runs projection.')).toBeInTheDocument()

    rerender(
      <ModelContext.Provider value={makeModel({ currentRuns: null, currentRunsFetchFailed: true })}>
        <MemoryRouter><LiveControl /></MemoryRouter>
      </ModelContext.Provider>,
    )
    expect(screen.getByText('Unavailable: the coordinator current-runs projection could not be read.')).toBeInTheDocument()
  })
})
