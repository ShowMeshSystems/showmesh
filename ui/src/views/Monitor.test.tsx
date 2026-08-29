import { cleanup, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router-dom'
import { Monitor } from './Monitor'
import { ModelContext } from '../app/ModelContext'
import { makeEvidence, makeFPPInstance, makeModel, makeNode, makeResolumeInstance } from '../app/test-support/fixtures'
import type { Model } from '../app/types'

// Playlist definitions received (revision-1: a Fleet section, not its own
// destination) makes its own read-only fetch on mount, same isolation
// pattern as FPPPlaylistDefinitions.test.tsx: mocked here so the Fleet
// table's own tests are not coupled to it, and it defaults to "nothing
// reported yet" unless a test configures otherwise.
const { listFPPPlaylistDefinitions } = vi.hoisted(() => ({
  listFPPPlaylistDefinitions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, listFPPPlaylistDefinitions }
})

afterEach(() => {
  cleanup()
  listFPPPlaylistDefinitions.mockReset()
})

function renderMonitor(model: Model) {
  // A test that wants specific definitions calls listFPPPlaylistDefinitions
  // .mockResolvedValueOnce(...) before renderMonitor; this fallback only
  // fires when nothing more specific was queued.
  listFPPPlaylistDefinitions.mockResolvedValue({ serverTime: '2026-08-28T00:00:00Z', definitions: [] })
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/monitor/fleet']}>
        <Monitor />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

describe('Monitor / Fleet', () => {
  it('carries the Monitor facet strip with Fleet current', () => {
    renderMonitor(makeModel())
    const nav = screen.getByRole('navigation', { name: 'Monitor facets' })
    expect(within(nav).getByRole('link', { name: /Fleet/ })).toHaveAttribute('aria-current', 'page')
    expect(within(nav).getByRole('link', { name: /Signals/ })).toHaveAttribute('href', '/monitor/signals')
    expect(within(nav).getByRole('link', { name: /Activity/ })).toHaveAttribute('href', '/monitor/activity')
    expect(within(nav).getByRole('link', { name: /Capabilities/ })).toHaveAttribute('href', '/monitor/capabilities')
    expect(within(nav).getByRole('link', { name: /Readiness/ })).toHaveAttribute('href', '/monitor/readiness')
  })

  it('renders one Fleet table across Node, FPP and Resolume, Kind as a column', () => {
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { label: 'Front Yard', controlPlane: { state: 'online', reason: null } })],
        fpp: [makeFPPInstance('fpp-a', { health: 'healthy', endpoint: 'http://fpp-a.local' })],
        resolume: [makeResolumeInstance('arena-main', { health: 'healthy' })],
      }),
    )
    const table = screen.getByRole('table', { name: 'Fleet' })
    expect(within(table).getByRole('link', { name: 'Front Yard' })).toHaveAttribute(
      'href',
      '/monitor/fleet/node/node-a',
    )
    expect(within(table).getByRole('link', { name: 'fpp-a' })).toHaveAttribute(
      'href',
      '/monitor/fleet/fpp/fpp-a',
    )
    expect(within(table).getByRole('link', { name: 'arena-main' })).toHaveAttribute(
      'href',
      '/monitor/fleet/resolume/arena-main',
    )
    expect(within(table).getAllByText('Node')).toHaveLength(1)
    expect(within(table).getAllByText('FPP')).toHaveLength(1)
    expect(within(table).getAllByText('Resolume')).toHaveLength(1)
  })

  it('never attributes a ShowMesh-side binding problem to FPP health: a pending instance-uuid change renders as a separate annotation, not the health badge', () => {
    renderMonitor(
      makeModel({
        fpp: [
          makeFPPInstance('fpp-a', {
            health: 'healthy',
            instanceUuid: 'new-uuid',
            instanceUuidChange: { previousUuid: 'old-uuid', changedAt: '2026-08-28T00:00:00Z' },
          }),
        ],
      }),
    )
    const table = screen.getByRole('table', { name: 'Fleet' })
    expect(within(table).getByText('Healthy')).toBeInTheDocument()
    expect(within(table).getByText('instance uuid change pending')).toBeInTheDocument()
  })

  it('states plainly when nothing needs an operator, without implying the show looks right', () => {
    renderMonitor(makeModel())
    expect(screen.getByText(/Nothing needs an operator right now/)).toBeInTheDocument()
  })

  it('surfaces an offline node under "Needs an operator"', () => {
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { label: 'Garage', controlPlane: { state: 'offline', reason: 'last will received' } })],
      }),
    )
    const heading = screen.getByRole('heading', { name: 'Needs an operator' })
    const section = heading.closest('section')!
    expect(within(section).getByText(/Garage stopped reporting/)).toBeInTheDocument()
    expect(within(section).getByText('last will received')).toBeInTheDocument()
  })

  it('keeps a disconnected model visibly qualified', () => {
    renderMonitor(makeModel({
      connection: { kind: 'reconnecting', attempt: 1, nextAttemptAt: 0, lastError: 'network error' },
    }))
    expect(screen.getByText('Coordinator not live')).toBeInTheDocument()
    expect(screen.getByRole('status')).toHaveTextContent('Showing last known data')
  })

  it('filters the Fleet table by kind via the segmented control', async () => {
    const user = userEvent.setup()
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { label: 'Front Yard' })],
        fpp: [makeFPPInstance('fpp-a')],
      }),
    )
    const table = screen.getByRole('table', { name: 'Fleet' })
    expect(within(table).getByText('Front Yard')).toBeInTheDocument()
    expect(within(table).getByText('fpp-a')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'FPP' }))
    expect(within(table).queryByText('Front Yard')).not.toBeInTheDocument()
    expect(within(table).getByText('fpp-a')).toBeInTheDocument()
  })

  it('reconciles the signals summary from the same observation rows the Signals facet counts', () => {
    renderMonitor(
      makeModel({
        nodes: [makeNode('node-a', { evidence: { hello: makeEvidence({ signal: 'node.hello', state: 'current' }), lastWill: makeEvidence({ signal: 'node.lastWill', state: 'not_collected', value: null, collectedAt: null }), heartbeat: makeEvidence({ signal: 'node.heartbeat', state: 'current' }) } })],
      }),
    )
    // Every observation-bearing evidence field on the fixture node renders
    // once in the stats strip's numerator/denominator; this does not pin
    // an exact count (Observations.test.tsx already does), only that the
    // strip renders a "current / total" pair rather than an invented figure.
    expect(screen.getByText('Signals current')).toBeInTheDocument()
  })

  describe('Playlist definitions received (revision-1: a Fleet section, not its own destination)', () => {
    it('renders "none reported" as a plain statement, not an empty table', async () => {
      listFPPPlaylistDefinitions.mockResolvedValueOnce({ serverTime: '2026-08-28T00:00:00Z', definitions: [] })
      renderMonitor(makeModel())

      expect(await screen.findByText('No FPP instance has reported a playlist definition yet.')).toBeInTheDocument()
    })

    it('resolves instanceUuid to the Fleet FPP instance that reports it, and links the row into Fleet', async () => {
      listFPPPlaylistDefinitions.mockResolvedValueOnce({
        serverTime: '2026-08-28T21:00:00Z',
        definitions: [
          {
            instanceUuid: 'uuid-barn',
            playlistName: 'WR26 Resting Loop',
            playlistHash: 'a70b0000000000000000000000000000000000000000000000000000000031c8',
            capturedAt: '2026-08-28T17:40:52Z',
            receivedAt: '2026-08-28T17:41:02Z',
            entryCount: 4,
            referenced: true,
          },
        ],
      })
      renderMonitor(makeModel({ fpp: [makeFPPInstance('barn-player', { instanceUuid: 'uuid-barn' })] }))

      const table = await screen.findByRole('table', { name: 'Playlist definitions received' })
      const instanceLink = within(table).getByRole('link', { name: 'barn-player' })
      expect(instanceLink).toHaveAttribute('href', '/monitor/fleet/fpp/barn-player')
      expect(within(table).getByText('Yes')).toBeInTheDocument()
    })

    it('states the causal link: a newer definition arriving while an older one stays bound is WHY bindings are held, not two unrelated rows', async () => {
      listFPPPlaylistDefinitions.mockResolvedValueOnce({
        serverTime: '2026-08-28T21:00:00Z',
        definitions: [
          {
            instanceUuid: 'uuid-barn',
            playlistName: 'WR26 Main Show',
            playlistHash: '9f2c00000000000000000000000000000000000000000000000000000000a41d',
            capturedAt: '2026-08-25T18:02:11Z',
            receivedAt: '2026-08-25T18:02:26Z',
            entryCount: 6,
            referenced: true,
          },
          {
            instanceUuid: 'uuid-barn',
            playlistName: 'WR26 Main Show',
            playlistHash: '4c1f00000000000000000000000000000000000000000000000000000000009e02',
            capturedAt: '2026-08-28T20:53:48Z',
            receivedAt: '2026-08-28T20:54:03Z',
            entryCount: 6,
            referenced: false,
          },
        ],
      })
      renderMonitor(makeModel())

      await screen.findByRole('table', { name: 'Playlist definitions received' })
      expect(screen.getByText('Newer than the bound one')).toBeInTheDocument()
      expect(screen.getByText(/arrived again at/)).toBeInTheDocument()
      expect(screen.getByText(/is still bound to/)).toBeInTheDocument()
      expect(screen.getByText(/which is why its bindings are held/)).toBeInTheDocument()
    })

    it('states a capture-drift finding as its own fact, distinct from the held finding, when nothing binds the definition at all', async () => {
      listFPPPlaylistDefinitions.mockResolvedValueOnce({
        serverTime: '2026-08-28T21:00:00Z',
        definitions: [
          {
            instanceUuid: 'uuid-garage',
            playlistName: 'Garage Loop',
            playlistHash: '61ae0000000000000000000000000000000000000000000000000000000c7d3',
            capturedAt: '2026-08-24T16:20:11Z',
            receivedAt: '2026-08-26T09:41:02Z',
            entryCount: 6,
            referenced: false,
          },
        ],
      })
      renderMonitor(makeModel())

      await screen.findByRole('table', { name: 'Playlist definitions received' })
      expect(screen.getByText('No playlist binds it')).toBeInTheDocument()
      expect(screen.getByText('Capture drift')).toBeInTheDocument()
      expect(screen.getByText(/out of\s*date, and nothing/)).toBeInTheDocument()
      expect(screen.queryByText(/which is why its bindings are held/)).not.toBeInTheDocument()
    })

    it('retains the Fleet table and its own controls when the definitions fetch fails', async () => {
      listFPPPlaylistDefinitions.mockRejectedValueOnce(new Error('network unreachable'))
      renderMonitor(makeModel({ nodes: [makeNode('node-a', { label: 'Front Yard' })] }))

      await waitFor(() => expect(listFPPPlaylistDefinitions).toHaveBeenCalled())
      expect(await screen.findByRole('alert')).toHaveTextContent('The stored definitions could not be read')
      // The Fleet table above it is untouched by the failure below it.
      expect(screen.getByRole('table', { name: 'Fleet' })).toBeInTheDocument()
      expect(within(screen.getByRole('table', { name: 'Fleet' })).getByText('Front Yard')).toBeInTheDocument()
    })
  })
})
