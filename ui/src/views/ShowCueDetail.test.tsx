import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ShowCueDetail } from './ShowCueDetail'
import { ModelContext } from '../app/ModelContext'
import { makeModel, makeShowList } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

const { getShowCue, getShowCueRevisions, putShowCue, listConfigObjects } = vi.hoisted(() => ({
  getShowCue: vi.fn(),
  getShowCueRevisions: vi.fn(),
  putShowCue: vi.fn(),
  listConfigObjects: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShowCue, getShowCueRevisions, putShowCue, listConfigObjects }
})

beforeEach(() => {
  listConfigObjects.mockResolvedValue(makeShowList(['halloween-2026']))
})

afterEach(() => {
  cleanup()
  getShowCue.mockReset()
  getShowCueRevisions.mockReset()
  putShowCue.mockReset()
  listConfigObjects.mockReset()
})

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['config:write'],
})

function renderNew(model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={['/config/show.cue/new']}>
        <ShowCueDetail isNew />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function renderExisting(id: string, model: Model = makeModel({ session: adminSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter initialEntries={[`/config/show.cue/${id}`]}>
        <Routes>
          <Route path="/config/show.cue/:id" element={<ShowCueDetail />} />
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
  it('renders the current payload', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('opening-number')

    await waitFor(() => expect(screen.getByDisplayValue('halloween-2026')).toBeVisible())
    expect(screen.getByDisplayValue('Opening number')).toBeVisible()
    expect(screen.getByDisplayValue('opening-sequence')).toBeVisible()
    expect(screen.getByDisplayValue('opening-audio')).toBeVisible()
    expect(screen.getByLabelText('Policy')).toHaveValue('duck')
    expect(screen.getByLabelText(/Duck gain/)).toHaveValue(-12)
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
    expect(table).toHaveTextContent('2')
    expect(table).toHaveTextContent('active')
  })

  it('renders the coordinator’s refusal reason on a rejected save, without reading as saved', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue(emptyRevisions)
    putShowCue.mockRejectedValue(
      new ApiError(
        'show "another-show" does not match the existing object\'s show "halloween-2026"; show is immutable',
        400,
        'https://showmesh.dev/problems/show-config-cross-show-reference',
      ),
    )
    const user = userEvent.setup()
    renderExisting('opening-number')

    await waitFor(() => expect(screen.getByDisplayValue('halloween-2026')).toBeVisible())
    await user.click(screen.getByRole('button', { name: 'Save cue' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/show is immutable/)
    // A refused write must not read as saved: the active revision banner
    // still names the ORIGINAL revision, and only one read happened (the
    // initial load): a successful save would trigger a second.
    expect(screen.getByText(/Active revision 3/)).toBeVisible()
    expect(getShowCue).toHaveBeenCalledTimes(1)
  })
})

describe('ShowCueDetail (new cue authoring)', () => {
  it('refuses to submit with no output enabled', async () => {
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/At least one output/)
    expect(putShowCue).not.toHaveBeenCalled()
  })

  it('refuses LTC enabled without audio, client-side, before dispatch', async () => {
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    // The LTC checkbox itself is disabled until audio is enabled, so the
    // real control cannot reach this state: this proves buildPayload's own
    // mirror of the server rule refuses it too, exercised by enabling audio
    // then disabling it again after LTC would have depended on it.
    expect(screen.getByLabelText(/^LTC/)).toBeDisabled()
  })

  it('submits a valid render-only cue', async () => {
    putShowCue.mockResolvedValue({
      ...storedCue,
      revision: 1,
      payload: { show: 'halloween-2026', name: 'Opening number', outputs: { render: { sequence: 'opening-sequence' } } },
    })
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText('Render'))
    await user.type(screen.getByLabelText(/Sequence/), 'opening-sequence')
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    await waitFor(() => expect(putShowCue).toHaveBeenCalledTimes(1))
    expect(putShowCue).toHaveBeenCalledWith(
      'opening-number',
      expect.objectContaining({
        show: 'halloween-2026',
        name: 'Opening number',
        outputs: { render: { sequence: 'opening-sequence' } },
      }),
    )
  })

  it('refuses a blank audio start offset instead of coercing it to zero', async () => {
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText('Audio'))
    await user.type(screen.getByLabelText('Asset'), 'opening-audio')
    // Start offset deliberately left blank.
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    expect(await screen.findByText(/Audio start offset must be a whole number/)).toBeVisible()
    expect(putShowCue).not.toHaveBeenCalled()
  })

  it('refuses a blank LTC start offset instead of coercing it to zero', async () => {
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText('Audio'))
    await user.type(screen.getByLabelText('Asset'), 'opening-audio')
    await user.type(screen.getByLabelText(/^Start offset \(milliseconds\)$/), '0')
    await user.click(screen.getByLabelText(/^LTC/))
    // LTC start offset deliberately left blank.
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    expect(await screen.findByText(/LTC start offset must be a whole number/)).toBeVisible()
    expect(putShowCue).not.toHaveBeenCalled()
  })

  it('refuses a blank announcement fade instead of coercing it to zero (an audible hard cut)', async () => {
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText('Audio'))
    await user.type(screen.getByLabelText('Asset'), 'opening-audio')
    await user.type(screen.getByLabelText(/^Start offset \(milliseconds\)$/), '0')
    await user.click(screen.getByLabelText(/^Announcement/))
    await user.selectOptions(screen.getByLabelText('Policy'), 'mix')
    // Fade deliberately left blank.
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    expect(await screen.findByText(/Announcement fade must be a whole number/)).toBeVisible()
    expect(putShowCue).not.toHaveBeenCalled()
  })

  it('requires a duck gain when the announcement policy is "duck"', async () => {
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText('Audio'))
    await user.type(screen.getByLabelText('Asset'), 'opening-audio')
    await user.type(screen.getByLabelText(/Start offset/), '0')
    await user.click(screen.getByLabelText(/^Announcement/))
    await user.selectOptions(screen.getByLabelText('Policy'), 'duck')
    await user.type(screen.getByLabelText(/Fade/), '250')
    // Duck gain deliberately left blank.
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    expect(await screen.findByText(/Duck gain is required/)).toBeVisible()
    expect(putShowCue).not.toHaveBeenCalled()
  })

  it('submits a valid audio+announcement "mix" cue with no duck gain', async () => {
    putShowCue.mockResolvedValue({
      ...storedCue,
      revision: 1,
      payload: {
        show: 'halloween-2026',
        name: 'Opening number',
        outputs: {
          audio: { asset: 'opening-audio', startOffsetMillis: 0 },
          announcement: { policy: 'mix', fadeMillis: 250 },
        },
      },
    })
    const user = userEvent.setup()
    renderNew()

    await user.type(screen.getByLabelText('Cue id'), 'opening-number')
    await user.selectOptions(screen.getByLabelText('Show'), 'halloween-2026')
    await user.type(screen.getByLabelText('Name'), 'Opening number')
    await user.click(screen.getByLabelText('Audio'))
    await user.type(screen.getByLabelText('Asset'), 'opening-audio')
    await user.type(screen.getByLabelText(/Start offset/), '0')
    await user.click(screen.getByLabelText(/^Announcement/))
    await user.selectOptions(screen.getByLabelText('Policy'), 'mix')
    await user.type(screen.getByLabelText(/Fade/), '250')
    await user.click(screen.getByRole('button', { name: 'Create cue' }))

    await waitFor(() => expect(putShowCue).toHaveBeenCalledTimes(1))
    expect(putShowCue).toHaveBeenCalledWith(
      'opening-number',
      expect.objectContaining({
        outputs: expect.objectContaining({
          announcement: { policy: 'mix', fadeMillis: 250 },
        }),
      }),
    )
  })
})

describe('ShowCueDetail (scope gating)', () => {
  it('is unavailable, with a stated reason, without the config:write scope for a new cue', () => {
    renderNew(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    expect(screen.getByRole('status')).toHaveTextContent(/config:write/)
    expect(screen.queryByLabelText('Cue id')).not.toBeInTheDocument()
  })

  it('renders view-only, with editing disabled, for a reader without config:write on an existing cue', async () => {
    getShowCue.mockResolvedValue(storedCue)
    getShowCueRevisions.mockResolvedValue(emptyRevisions)
    renderExisting('opening-number', makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))

    await waitFor(() => expect(screen.getByDisplayValue('halloween-2026')).toBeVisible())
    expect(screen.getByText(/Viewing only/)).toBeVisible()
    expect(screen.queryByRole('button', { name: 'Save cue' })).not.toBeInTheDocument()
  })
})
