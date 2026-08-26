import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { PlaylistReadiness } from './PlaylistReadiness'
import { ModelContext } from '../app/ModelContext'
import { makeFPPInstance, makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api'
import { formatAbsolute } from '../app/time'
import type { Model } from '../app/types'

// Same isolation pattern as Macros.test.tsx: mock the API calls this view
// makes, so this component's own branching (ready vs not ready vs a fetch
// failure vs a real-but-inapplicable answer) is what these tests exercise,
// not store.ts's own network behavior.
const { listConfigObjects, getFPPPlaylistReadiness, getFPPPlaylistEntryReconciliation } = vi.hoisted(() => ({
  listConfigObjects: vi.fn(),
  getFPPPlaylistReadiness: vi.fn(),
  getFPPPlaylistEntryReconciliation: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    listConfigObjects,
    getFPPPlaylistReadiness,
    getFPPPlaylistEntryReconciliation,
  }
})

afterEach(() => {
  cleanup()
  listConfigObjects.mockReset()
  getFPPPlaylistReadiness.mockReset()
  getFPPPlaylistEntryReconciliation.mockReset()
})

function renderView(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <PlaylistReadiness />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const session = makeAuthenticatedSession({ scopes: ['show:macro:run'] })

const playlistListResponse = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.playlist' as const,
  objects: [{ id: 'opener', label: 'Opener', show: 'halloween-2026', currentRevision: 2, updatedAt: '2026-08-25T00:00:00Z' }],
}

describe('PlaylistReadiness', () => {
  it('renders a ready verdict as ready', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: true,
      serverTime: '2026-08-25T00:00:00Z',
    })
    renderView(makeModel({ session }))

    await waitFor(() => expect(getFPPPlaylistReadiness).toHaveBeenCalledWith('opener'))
    expect(await screen.findByText('ready')).toBeInTheDocument()
    expect(screen.queryByText('not ready')).not.toBeInTheDocument()
  })

  it('renders a not-ready verdict with its own reason, visually distinct from ready', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: false,
      failingCondition: 'entry-filename-mismatch',
      reason: 'entry 2 expects opener.fseq but the stored definition names opener-v2.fseq',
      serverTime: '2026-08-25T00:00:00Z',
    })
    renderView(makeModel({ session }))

    const badge = await screen.findByText('not ready')
    expect(badge).toBeInTheDocument()
    expect(screen.queryByText('ready', { selector: 'span' })).not.toBeInTheDocument()
    expect(
      screen.getByText('entry 2 expects opener.fseq but the stored definition names opener-v2.fseq'),
    ).toBeInTheDocument()
    expect(screen.getByText('entry filename mismatch')).toBeInTheDocument()
  })

  it('renders a fetch failure as a failure to read, distinct from a not-ready verdict', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockRejectedValue(new Error('network unreachable'))
    renderView(makeModel({ session }))

    const failures = await screen.findAllByRole('alert')
    expect(failures.length).toBeGreaterThan(0)
    expect(screen.getAllByText('Could not check').length).toBeGreaterThan(0)
    expect(screen.queryByText('not ready')).not.toBeInTheDocument()
    expect(screen.queryByText('ready')).not.toBeInTheDocument()
  })

  it('renders a playlist with no FPP binding as a sensible, non-fpp-runner answer rather than a broken panel', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockRejectedValue(
      new ApiError('this playlist runs on showmesh-audio, not fpp', 400, 'https://showmesh.dev/problems/invalid-parameter'),
    )
    renderView(makeModel({ session }))

    expect(await screen.findByText('Not FPP-runner')).toBeInTheDocument()
    expect(screen.getByText('this playlist runs on showmesh-audio, not fpp')).toBeInTheDocument()
    expect(screen.queryByText('not ready')).not.toBeInTheDocument()
  })

  it('renders the reconciliation verdict for an FPP instance', async () => {
    listConfigObjects.mockResolvedValue({ ...playlistListResponse, objects: [] })
    getFPPPlaylistEntryReconciliation.mockResolvedValue({
      instanceUuid: 'uuid-1',
      outcome: 'resolved',
      reason: 'matches the current binding',
      playlistId: 'opener',
      playlistRevision: 2,
      entryId: 'entry-1',
      cueId: 'cue-1',
      cueRevision: 1,
      definitionAvailable: true,
      serverTime: '2026-08-25T00:00:00Z',
    })
    const instance = makeFPPInstance('fpp-01', { instanceUuid: 'uuid-1' })
    renderView(makeModel({ session, fpp: [instance] }))

    await waitFor(() => expect(getFPPPlaylistEntryReconciliation).toHaveBeenCalledWith('uuid-1'))
    expect(await screen.findByText('resolved')).toBeInTheDocument()
    expect(screen.getByText('matches the current binding')).toBeInTheDocument()
    expect(screen.getByText(/Playlist opener/)).toBeInTheDocument()
  })

  it('renders an unbound reconciliation outcome distinctly, never as resolved', async () => {
    listConfigObjects.mockResolvedValue({ ...playlistListResponse, objects: [] })
    getFPPPlaylistEntryReconciliation.mockResolvedValue({
      instanceUuid: 'uuid-1',
      outcome: 'unbound',
      reason: 'no show.playlist binding names this instance and hash',
      definitionAvailable: false,
      serverTime: '2026-08-25T00:00:00Z',
    })
    const instance = makeFPPInstance('fpp-01', { instanceUuid: 'uuid-1' })
    renderView(makeModel({ session, fpp: [instance] }))

    expect(await screen.findByText('unbound')).toBeInTheDocument()
    expect(screen.getByText('no show.playlist binding names this instance and hash')).toBeInTheDocument()
    expect(screen.queryByText('resolved')).not.toBeInTheDocument()
  })

  it('renders the readiness verdict\'s own serverTime, so a stale verdict is dated', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: true,
      serverTime: '2026-08-25T18:40:00Z',
    })
    renderView(makeModel({ session }))

    await screen.findByText('ready')
    expect(screen.getByText(formatAbsolute('2026-08-25T18:40:00Z'))).toBeInTheDocument()
  })

  it('renders the reconciliation verdict\'s own serverTime', async () => {
    listConfigObjects.mockResolvedValue({ ...playlistListResponse, objects: [] })
    getFPPPlaylistEntryReconciliation.mockResolvedValue({
      instanceUuid: 'uuid-1',
      outcome: 'resolved',
      reason: 'matches the current binding',
      playlistId: 'opener',
      playlistRevision: 2,
      entryId: 'entry-1',
      cueId: 'cue-1',
      cueRevision: 1,
      definitionAvailable: true,
      serverTime: '2026-08-25T21:15:00Z',
    })
    const instance = makeFPPInstance('fpp-01', { instanceUuid: 'uuid-1' })
    renderView(makeModel({ session, fpp: [instance] }))

    await screen.findByText('resolved')
    expect(screen.getByText(formatAbsolute('2026-08-25T21:15:00Z'))).toBeInTheDocument()
  })

  it('refetches both verdicts on reconnect, so a re-import after the operator opened the tab is caught', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: true,
      serverTime: '2026-08-25T18:40:00Z',
    })
    const instance = makeFPPInstance('fpp-01', { instanceUuid: 'uuid-1' })
    getFPPPlaylistEntryReconciliation.mockResolvedValue({
      instanceUuid: 'uuid-1',
      outcome: 'resolved',
      reason: 'matches the current binding',
      definitionAvailable: true,
      serverTime: '2026-08-25T18:40:00Z',
    })
    const view = renderView(makeModel({ session, fpp: [instance], snapshotReceivedAt: 1000 }))

    await waitFor(() => expect(getFPPPlaylistReadiness).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(getFPPPlaylistEntryReconciliation).toHaveBeenCalledTimes(1))

    // A later resnapshot (store.ts's own applySnapshot: initial connect,
    // every reconnect, every stream.reset) bumps `snapshotReceivedAt` --
    // simulated here the same way a real reconnect would present itself
    // to this component: a changed `model.snapshotReceivedAt` prop, not
    // a new mechanism this test invents.
    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: false,
      reason: 'a binding hash went stale after re-import',
      serverTime: '2026-08-25T21:15:00Z',
    })
    view.rerender(
      <ModelContext.Provider value={makeModel({ session, fpp: [instance], snapshotReceivedAt: 2000 })}>
        <MemoryRouter>
          <PlaylistReadiness />
        </MemoryRouter>
      </ModelContext.Provider>,
    )

    await waitFor(() => expect(getFPPPlaylistReadiness).toHaveBeenCalledTimes(2))
    await waitFor(() => expect(getFPPPlaylistEntryReconciliation).toHaveBeenCalledTimes(2))
    expect(await screen.findByText('not ready')).toBeInTheDocument()
    expect(screen.getByText(formatAbsolute('2026-08-25T21:15:00Z'))).toBeInTheDocument()
  })

  it('SM-283: refetches the reconciliation verdict, but not the readiness verdict, when a live fppPlaylistEntry.changed observation arrives for the instance', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: true,
      serverTime: '2026-08-25T18:40:00Z',
    })
    const instance = makeFPPInstance('fpp-01', { instanceUuid: 'uuid-1' })
    getFPPPlaylistEntryReconciliation.mockResolvedValue({
      instanceUuid: 'uuid-1',
      outcome: 'resolved',
      playlistId: 'opener',
      playlistRevision: 1,
      entryId: 'entry-one',
      cueId: 'cue-one',
      cueRevision: 1,
      reason: 'matches the current binding',
      definitionAvailable: true,
      serverTime: '2026-08-25T18:40:00Z',
    })
    const view = renderView(
      makeModel({ session, fpp: [instance], snapshotReceivedAt: 1000, fppPlaylistEntryObservations: [] }),
    )

    await waitFor(() => expect(getFPPPlaylistEntryReconciliation).toHaveBeenCalledTimes(1))
    await waitFor(() => expect(getFPPPlaylistReadiness).toHaveBeenCalledTimes(1))
    expect(screen.getByText(/entry entry-one/)).toBeInTheDocument()

    // FPP advances: store.ts's applyFppPlaylistEntryChanged upserted a
    // new observation for this instanceUuid (SM-283). Presented to this
    // component the same way a real live frame would be -- a changed
    // `model.fppPlaylistEntryObservations` prop, not a new mechanism this
    // test invents, matching the reconnect test's own approach above.
    getFPPPlaylistEntryReconciliation.mockResolvedValue({
      instanceUuid: 'uuid-1',
      outcome: 'resolved',
      playlistId: 'opener',
      playlistRevision: 1,
      entryId: 'entry-two',
      cueId: 'cue-two',
      cueRevision: 1,
      reason: 'matches the current binding',
      definitionAvailable: true,
      serverTime: '2026-08-25T18:41:00Z',
    })
    view.rerender(
      <ModelContext.Provider
        value={makeModel({
          session,
          fpp: [instance],
          snapshotReceivedAt: 1000,
          fppPlaylistEntryObservations: [
            {
              instanceUuid: 'uuid-1',
              endpointId: 'fpp-01',
              schemaVersion: 1,
              sequence: 2,
              action: 'playing',
              observedAt: '2026-08-25T18:41:00Z',
              coalescedSincePreviousAcknowledged: 0,
              receivedAt: '2026-08-25T18:41:00Z',
            },
          ],
        })}
      >
        <MemoryRouter>
          <PlaylistReadiness />
        </MemoryRouter>
      </ModelContext.Provider>,
    )

    await waitFor(() => expect(getFPPPlaylistEntryReconciliation).toHaveBeenCalledTimes(2))
    expect(await screen.findByText(/entry entry-two/)).toBeInTheDocument()
    // The readiness verdict has no live trigger of its own (this file's
    // own comment on why): it must not be refetched just because the
    // reconciliation row's own observation moved.
    expect(getFPPPlaylistReadiness).toHaveBeenCalledTimes(1)
  })

  it('refetches the readiness verdict when the operator clicks Recheck readiness', async () => {
    listConfigObjects.mockResolvedValue(playlistListResponse)
    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: true,
      serverTime: '2026-08-25T18:40:00Z',
    })
    renderView(makeModel({ session }))

    await waitFor(() => expect(getFPPPlaylistReadiness).toHaveBeenCalledTimes(1))

    getFPPPlaylistReadiness.mockResolvedValue({
      playlistId: 'opener',
      ready: false,
      reason: 'a binding hash went stale after re-import',
      serverTime: '2026-08-25T21:15:00Z',
    })
    await userEvent.click(screen.getByRole('button', { name: 'Recheck readiness' }))

    expect(await screen.findByText('not ready')).toBeInTheDocument()
    expect(getFPPPlaylistReadiness).toHaveBeenCalledTimes(2)
  })

  it('renders sensibly, not a broken panel, when no FPP instance has reported a uuid yet', async () => {
    listConfigObjects.mockResolvedValue({ ...playlistListResponse, objects: [] })
    const instance = makeFPPInstance('fpp-01', { instanceUuid: null })
    renderView(makeModel({ session, fpp: [instance] }))

    expect(
      await screen.findByText('No FPP instance has reported a SystemUUID yet, so there is nothing to reconcile.'),
    ).toBeInTheDocument()
    expect(getFPPPlaylistEntryReconciliation).not.toHaveBeenCalled()
  })
})
