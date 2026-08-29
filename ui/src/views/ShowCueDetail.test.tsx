import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ShowCueDetail } from './ShowCueDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

// The composer now reads its show from the real nested route
// (/shows/:showId/cues/...) instead of a Show <select>, and derives the
// new cue's id from its name (still editable) rather than asking for one
// up front.
const { getShow, getShowCue, getShowCueRevisions, putShowCue, listConfigObjects, listAssets } = vi.hoisted(() => ({
  getShow: vi.fn(),
  getShowCue: vi.fn(),
  getShowCueRevisions: vi.fn(),
  putShowCue: vi.fn(),
  listConfigObjects: vi.fn(),
  listAssets: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShow, getShowCue, getShowCueRevisions, putShowCue, listConfigObjects, listAssets }
})

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

beforeEach(() => {
  mockWorkspaceLists()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

const showResponse = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show' as const,
  id: 'halloween-2026',
  revision: 3,
  payload: { name: 'Halloween 2026', notes: '' },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

function emptyList(kind: string) {
  return { serverTime: '2026-08-25T00:00:00Z', kind, objects: [] }
}

function mockWorkspaceLists(): void {
  getShow.mockResolvedValue(showResponse)
  listAssets.mockResolvedValue({ serverTime: '2026-08-25T00:00:00Z', assets: [] })
  listConfigObjects.mockImplementation((kind: string) => Promise.resolve(emptyList(kind)))
}

function renderNew(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/shows/halloween-2026/cues/new']}>
        <Routes>
          <Route path="/shows/:showId/cues/new" element={<ShowCueDetail isNew />} />
          <Route path="/shows/:showId/cues/:cueId" element={<ShowCueDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExisting(id: string, model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/shows/halloween-2026/cues/${id}`]}>
        <Routes>
          <Route path="/shows/:showId/cues/:cueId" element={<ShowCueDetail />} />
        </Routes>
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

const storedCue = {
  serverTime: '2026-08-25T00:00:00Z',
  kind: 'show.cue' as const,
  id: 'opening-number',
  revision: 3,
  payload: {
    show: 'halloween-2026',
    name: 'Opening number',
    outputs: {
      render: { sequence: 'opening-sequence' },
      audio: { asset: 'opening-audio', startOffsetMillis: 500 },
      ltc: { startOffsetMillis: 500 },
      announcement: { policy: 'duck' as const, duckGainDb: -12, fadeMillis: 250 },
    },
  },
  updatedAt: '2026-08-25T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api' as const,
}

const emptyRevisions = { serverTime: '2026-08-25T00:00:00Z', revisions: [] }

describe('ShowCueDetail (viewing an existing cue)', () => {
  it('renders the current payload and warns editing changes every playlist that uses it', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('opening-number')

    await waitFor(() => expect(screen.getByDisplayValue('Opening number')).toBeVisible())
    expect(screen.getByDisplayValue('opening-sequence')).toBeVisible()
    expect(screen.getByDisplayValue('opening-audio')).toBeVisible()
    expect(screen.getByText(/edits here apply to all of them/)).toBeVisible()
  })

  // PUT /config/show.cue/{id} refuses any request that changes `show`
  // (`show-config-cross-show-reference`; api/openapi.yaml's own
  // `putShowCue` description says so in as many words: "`show` is
  // immutable"). No re-assignment picker is offered here because the
  // API has no working path for it, not because the capability was
  // overlooked.
  it('offers no show re-assignment control: show is immutable server-side for a cue', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('opening-number')

    await waitFor(() => expect(screen.getByDisplayValue('Opening number')).toBeVisible())
    expect(screen.queryByText(/Move to another show/i)).not.toBeInTheDocument()
  })

  it('renders the revision history', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue({
      serverTime: '2026-08-25T00:00:00Z',
      revisions: [
        { revision: 3, active: true, createdAt: '2026-08-25T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1' },
        { revision: 2, active: false, createdAt: '2026-08-20T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1' },
      ],
    })
    renderExisting('opening-number')

    await waitFor(() => expect(screen.getByText('Revision history')).toBeVisible())
    const table = screen.getByRole('table', { name: 'Revision history' })
    expect(table).toHaveTextContent('3')
    expect(table).toHaveTextContent('active')
  })

  it('renders the coordinator’s refusal reason on a rejected save, without reading as saved', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue(emptyRevisions)
    putShowCue.mockRejectedValue(
      new ApiError('show "another-show" does not match the existing object\'s show "halloween-2026"; show is immutable', 400, 'https://showmesh.dev/problems/show-config-cross-show-reference'),
    )
    const user = userEvent.setup()
    renderExisting('opening-number')

    await waitFor(() => expect(screen.getByDisplayValue('Opening number')).toBeVisible())
    await user.click(screen.getByRole('button', { name: 'Save cue' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/show is immutable/)
    expect(screen.getByText(/Cue rev 3/)).toBeVisible()
    expect(getShowCue).toHaveBeenCalledTimes(1)
  })
})

describe('ShowCueDetail (new cue authoring)', () => {
  it('derives the id from the name', async () => {
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Name'), 'Thank You Announcement')
    expect(screen.getByLabelText(/Id \(from the name/)).toHaveValue('thank-you-announcement')
  })

  it('refuses to submit with no output enabled', async () => {
    mockWorkspaceLists()
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/At least one output/)
    expect(putShowCue).not.toHaveBeenCalled()
  })

  it('LTC and Announcement are disabled until audio is enabled', async () => {
    renderNew()

    expect(screen.getByLabelText(/^LTC/)).toBeDisabled()
    expect(screen.getByLabelText(/^Announcement/)).toBeDisabled()
  })

  it('submits a valid render-only cue', async () => {
    mockWorkspaceLists()
    putShowCue.mockResolvedValue({
      ...storedCue,
      revision: 1,
      payload: { show: 'halloween-2026', name: 'Opening number', outputs: { render: { sequence: 'opening-sequence' } } },
    })
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText(/^Render/))
    await user.type(screen.getByLabelText(/Logical sequence/), 'opening-sequence')
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    await waitFor(() => expect(putShowCue).toHaveBeenCalledTimes(1))
    expect(putShowCue).toHaveBeenCalledWith(
      'opening-number',
      expect.objectContaining({ show: 'halloween-2026', name: 'Opening number', outputs: { render: { sequence: 'opening-sequence' } } }),
    )
  })

  it('requires a duck gain when the announcement policy is "duck"', async () => {
    mockWorkspaceLists()
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText(/Audience audio/))
    await user.type(screen.getByLabelText(/Audio asset/), 'opening-audio')
    await user.click(screen.getByLabelText(/^Announcement/))
    await user.click(screen.getByRole('button', { name: 'Duck' }))
    // Duck gain deliberately left blank.
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    expect(await screen.findByText(/Duck gain is required/)).toBeVisible()
    expect(putShowCue).not.toHaveBeenCalled()
  })

  it('submits a valid audio+announcement "mix" cue with no duck gain', async () => {
    mockWorkspaceLists()
    putShowCue.mockResolvedValue({
      ...storedCue,
      revision: 1,
      payload: {
        show: 'halloween-2026',
        name: 'Opening number',
        outputs: { audio: { asset: 'opening-audio', startOffsetMillis: 0 }, announcement: { policy: 'mix', fadeMillis: 400 } },
      },
    })
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText(/Audience audio/))
    await user.type(screen.getByLabelText(/Audio asset/), 'opening-audio')
    await user.click(screen.getByLabelText(/^Announcement/))
    await user.click(screen.getByRole('button', { name: 'Mix' }))
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    await waitFor(() => expect(putShowCue).toHaveBeenCalledTimes(1))
    expect(putShowCue).toHaveBeenCalledWith(
      'opening-number',
      expect.objectContaining({ outputs: expect.objectContaining({ announcement: { policy: 'mix', fadeMillis: 400 } }) }),
    )
  })
})

describe('ShowCueDetail (scope gating)', () => {
  it('is unavailable, with a stated reason, without the config:write scope for a new cue', () => {
    mockWorkspaceLists()
    renderNew(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(screen.queryByLabelText('Name')).not.toBeInTheDocument()
  })

  it('renders view-only, with editing disabled, for a reader without config:write on an existing cue', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('opening-number', makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    await waitFor(() => expect(screen.getByDisplayValue('Opening number')).toBeVisible())
    expect(screen.getByText(/Viewing only/)).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Save cue' })).not.toBeInTheDocument()
  })
})
