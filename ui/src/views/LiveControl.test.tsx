import { cleanup, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LiveControl } from './LiveControl'
import { ModelContext } from '../app/ModelContext'
import { makeConfigObjectSummary, makeCurrentRuns, makeFPPInstance, makeModel, makeResolumeInstance } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'

const { dispatchNightCommand, invokeAction, listConfigObjects, getShowModeConfig, getShowModeConfigRevisions } = vi.hoisted(() => ({
  dispatchNightCommand: vi.fn(),
  invokeAction: vi.fn(),
  listConfigObjects: vi.fn(),
  getShowModeConfig: vi.fn(),
  getShowModeConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, dispatchNightCommand, invokeAction, listConfigObjects, getShowModeConfig, getShowModeConfigRevisions }
})

afterEach(cleanup)
afterEach(() => dispatchNightCommand.mockReset())
afterEach(() => invokeAction.mockReset())
afterEach(() => listConfigObjects.mockReset())
afterEach(() => getShowModeConfig.mockReset())
afterEach(() => getShowModeConfigRevisions.mockReset())

function renderView(overrides: Parameters<typeof makeModel>[0] = {}) {
  if (listConfigObjects.getMockImplementation() === undefined) {
    listConfigObjects.mockImplementation(() => Promise.resolve({ objects: [] }))
  }
  return render(
    <ModelContext.Provider value={makeModel(overrides)}>
      <MemoryRouter>
        <LiveControl />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

// Rewritten wholesale for the Operator UI overhaul (Live Control.dc.html):
// the "Control status" strip and its repeated "Capability: X. Freshness:
// Y." captions are gone (UI-DESIGN-GUIDE.md section 5's verbosity fix),
// replaced by Transport / What each output is doing / Night lifecycle /
// Macros / Announcements / Actions / Resolume / Audio and render-node
// controls / Active show and mode / Not available on this coordinator,
// in that order. Behaviour these tests actually cover (scope gating,
// command outcomes, absence rendering) is preserved; assertions are
// rewritten against the new structure.
describe('LiveControl', () => {
  it('renders capability-driven unavailable states instead of blank control groups', () => {
    renderView()

    expect(screen.getByRole('heading', { name: 'Live Control' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Back to dashboard' })).toHaveClass('button--secondary')
    const transportUnavailable = screen.getByText('No FPP instance is configured on this coordinator.')
    expect(transportUnavailable).toBeInTheDocument()
    expect(transportUnavailable.closest('.shared-state-block')).toHaveClass('shared-state-block--unavailable')
    expect(transportUnavailable.closest('.shared-state-block')?.parentElement?.closest('section')).toHaveAttribute('aria-labelledby', 'transport-controls')
    expect(screen.getByText('No nodes are currently observed.')).toBeInTheDocument()
    expect(screen.getByText('Resolume is not configured on this coordinator.')).toBeInTheDocument()
    expect(screen.getByRole('note', { name: 'Not built: Brightness ceiling' })).toBeInTheDocument()
    for (const title of ['Transport', 'What each output is doing', 'Audio and render-node controls', 'Resolume']) {
      expect(screen.getByRole('heading', { name: title, level: 2 })).toBeVisible()
    }
    for (const title of ['Brightness ceiling', 'Site control', 'Interlock authoring', 'Installation-wide emergency stop']) {
      expect(screen.getByText(title)).toBeVisible()
    }
  })

  it('keeps lifecycle controls visible and explicitly permission-gated when the session is unknown', () => {
    renderView()

    expect(screen.getByRole('heading', { name: 'Night lifecycle' })).toBeInTheDocument()
    const prepare = screen.getByRole('button', { name: 'Prepare site' })
    expect(prepare).toBeDisabled()
    expect(screen.getAllByText(/Waiting to hear from the coordinator what this device may do/).length).toBeGreaterThan(0)
  })

  it('keeps failed live evidence distinct in the outputs table', () => {
    renderView({
      snapshotReceivedAt: 0,
      fpp: [makeFPPInstance('fpp-main', { health: 'failed' })],
      resolume: [makeResolumeInstance('resolume-main', { health: 'failed' })],
    })

    // FPP/Resolume health is not surfaced by a removed status strip
    // anymore; the outputs table reports render/audio evidence per
    // resource instead, and renders its own absence when none exists.
    expect(screen.getByText('No node currently reports a render or audio observation.')).toBeInTheDocument()
  })

  it('does not present configured evidence as live before a coordinator snapshot arrives', () => {
    renderView({
      connection: { kind: 'connecting' },
      snapshotReceivedAt: null,
    })

    expect(screen.getByText('No data received from the coordinator yet.')).toBeInTheDocument()
    expect(screen.getByRole('status', { name: 'Transport: unobserved' })).toBeInTheDocument()
    expect(screen.getByRole('status', { name: 'What each output is doing: unobserved' })).toBeInTheDocument()
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

  it('lists coordinator-exposed stored Actions and preserves their literal invoke scope and outcome handling', async () => {
    listConfigObjects.mockImplementation((kind: string) => Promise.resolve({
      objects: kind === 'show.action' ? [makeConfigObjectSummary({ id: 'blackout', label: 'Blackout', currentRevision: 4 })] : [],
    }))
    invokeAction.mockResolvedValue({
      id: 'invocation-1', idempotencyKey: 'key-1', actionId: 'blackout', revision: 4,
      replay: false, state: 'resolved', outcome: 'refused', outcomeReason: 'interlock active',
      dispatchAttribution: 'complete', dispatchAttributionReason: 'recorded',
      outcomeAttribution: 'complete', outcomeAttributionReason: 'recorded',
      attributionDegraded: false, dispatchedAt: '2026-08-27T00:00:00Z', resolvedAt: '2026-08-27T00:00:01Z',
    })
    const user = userEvent.setup()
    renderView({ session: makeAuthenticatedSession({ scopes: ['show:macro:run', 'show:action:invoke'] }) })

    expect(await screen.findByRole('heading', { name: 'Blackout', level: 3 })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Invoke' }))
    expect(invokeAction).toHaveBeenCalledWith('blackout')
    expect(await screen.findByText('Refused, nothing was dispatched: interlock active')).toBeInTheDocument()
  })

  it('keeps a listed Action visible but disabled when its literal invoke scope is absent', async () => {
    listConfigObjects.mockImplementation((kind: string) => Promise.resolve({
      objects: kind === 'show.action' ? [makeConfigObjectSummary({ id: 'blackout', label: 'Blackout' })] : [],
    }))
    renderView({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) })

    expect(await screen.findByRole('heading', { name: 'Blackout', level: 3 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Invoke' })).toBeDisabled()
    expect(screen.getByText(/does not include "show:action:invoke"/)).toBeInTheDocument()
    expect(invokeAction).not.toHaveBeenCalled()
  })

  it('puts the existing direct Mode selection on Live Control instead of linking to Settings', async () => {
    getShowModeConfig.mockResolvedValue({
      serverTime: '2026-08-27T00:00:00Z', kind: 'show.mode', revision: 0, payload: { mode: 'program' },
      updatedAt: '2026-08-27T00:00:00Z', createdByPrincipalId: null, createdByPrincipalName: null,
      source: 'default', resolumeWebSocketEffect: 'program mode: the Resolume WebSocket wake-up channel is held OPEN.',
    })
    getShowModeConfigRevisions.mockResolvedValue({ serverTime: '2026-08-27T00:00:00Z', kind: 'show.mode', revisions: [] })
    listConfigObjects.mockResolvedValue({ objects: [] })
    renderView({ session: makeAuthenticatedSession({ scopes: ['config:write'] }) })

    expect(await screen.findByLabelText('Operating mode')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save show mode' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Open Mode selection' })).not.toBeInTheDocument()
  })

  it('keeps operator task groups in command order and states that Select active show has no route yet', () => {
    renderView({ currentRuns: makeCurrentRuns(), currentRunsReceivedAt: 0 })

    expect(screen.getByText('Active: halloween-2026 (generation 7). Evidence: current-runs projection received by this browser.')).toBeInTheDocument()
    // The old /config/show.active destination is a blocked route
    // (ROUTE-MAP.md), not a working link: the control stays present and
    // disabled with a stated reason rather than pointing at a 404.
    const selectActiveShow = screen.getByRole('button', { name: 'Select active show' })
    expect(selectActiveShow).toBeDisabled()
    expect(screen.queryByRole('button', { name: /save show mode/i })).not.toBeInTheDocument()

    const headings = screen.getAllByRole('heading', { level: 2 }).map((heading) => heading.textContent)
    expect(headings).toEqual([
      'Transport',
      'What each output is doing',
      'Night lifecycle',
      'Macros',
      'Announcements',
      'Actions',
      'Resolume',
      'Audio and render-node controls',
      'Active show and mode',
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

  it('marks retained current-runs evidence as last known while disconnected', () => {
    renderView({
      connection: { kind: 'reconnecting', attempt: 1, nextAttemptAt: 1, lastError: 'network lost' },
      currentRuns: makeCurrentRuns(),
      currentRunsReceivedAt: 0,
    })

    expect(screen.getByText('Active: halloween-2026 (generation 7). Evidence: last known current-runs projection while the browser is reconnecting.')).toBeInTheDocument()
  })

  it('states the absence of an installation-wide E-Stop as a planned feature, never a working control', () => {
    renderView()

    const planned = screen.getByRole('note', { name: 'Not built: Installation-wide emergency stop' })
    expect(planned).toHaveTextContent('This coordinator advertises no installation-wide stop capability')
    // The drawn preview is inert: it must not be reachable as a real button.
    expect(screen.queryByRole('button', { name: 'EMERGENCY STOP' })).not.toBeInTheDocument()
  })
})
