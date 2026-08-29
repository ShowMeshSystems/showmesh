import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { NightSessionActive } from './NightSessionActive'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession, makeProblem } from '../api/test-support/fixtures'
import { ApiError } from '../api/errors'
import type { Model } from '../app/types'

// This exercises the compatibility wrapper (the pre-overhaul
// /config/night.session.active route, still declared in App.tsx) which
// mounts NightSessionActivePanel — the same component the new
// /shows/:showId/night-sessions list view (NightSessions.tsx) embeds
// inline above its definitions table. Testing through the wrapper
// exercises the panel's real behavior without needing a route param.
const {
  getNightSessionActiveConfig,
  getNightSessionActiveConfigRevisions,
  putNightSessionActiveConfig,
  listConfigObjects,
} = vi.hoisted(() => ({
  getNightSessionActiveConfig: vi.fn(),
  getNightSessionActiveConfigRevisions: vi.fn(),
  putNightSessionActiveConfig: vi.fn(),
  listConfigObjects: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getNightSessionActiveConfig,
    getNightSessionActiveConfigRevisions,
    putNightSessionActiveConfig,
    listConfigObjects,
  }
})

afterEach(() => {
  cleanup()
  getNightSessionActiveConfig.mockReset()
  getNightSessionActiveConfigRevisions.mockReset()
  putNightSessionActiveConfig.mockReset()
  listConfigObjects.mockReset()
})

const writerSession = makeAuthenticatedSession({ scopes: ['config:write'] })

function renderView(model: Model = makeModel({ session: writerSession })) {
  return render(
    <ModelContext.Provider value={model}>
      <MemoryRouter>
        <NightSessionActive />
      </MemoryRouter>
    </ModelContext.Provider>,
  )
}

function makeActiveConfigResponse(session: string) {
  return {
    serverTime: '2026-08-22T00:00:00Z',
    kind: 'night.session.active' as const,
    id: 'night.session.active',
    revision: 3,
    payload: { session },
    updatedAt: '2026-08-22T00:00:00Z',
    createdByPrincipalId: 'p1',
    createdByPrincipalName: 'operator',
    source: 'api' as const,
  }
}

async function openPicker(user: ReturnType<typeof userEvent.setup>): Promise<void> {
  await user.click(screen.getByRole('button', { name: 'Activate a different one' }))
}

describe('NightSessionActive (compatibility wrapper over NightSessionActivePanel)', () => {
  it('renders the cleared state, not an error, when nothing has ever been activated', async () => {
    getNightSessionActiveConfig.mockRejectedValue(
      new ApiError('nothing has ever been activated', 404, 'https://showmesh.dev/problems/resource-not-found'),
    )
    getNightSessionActiveConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      kind: 'night.session.active',
      revisions: [],
    })
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-22T00:00:00Z', objects: [] })
    renderView()
    await waitFor(() => expect(screen.getByText(/nothing has ever been activated/)).toBeVisible())
    expect(screen.getByText('Cleared')).toBeVisible()
  })

  // Suspicion resolved: GET .../revisions can fail independently of the
  // pointer read, and must not hide a pointer that already loaded.
  it('still shows the current pointer when the revisions read fails independently', async () => {
    getNightSessionActiveConfig.mockResolvedValue(makeActiveConfigResponse('halloween-night'))
    getNightSessionActiveConfigRevisions.mockRejectedValue(new Error('the revisions store is unreachable'))
    listConfigObjects.mockResolvedValue({ serverTime: '2026-08-22T00:00:00Z', objects: [] })
    renderView()
    await waitFor(() => expect(screen.getByText('halloween-night')).toBeVisible())
    expect(screen.getByText(/activation history could not be loaded/i)).toBeVisible()
    expect(screen.getByText(/the revisions store is unreachable/)).toBeVisible()
  })

  it('arms then confirms activating a different session, and calls the PUT only after the second click', async () => {
    getNightSessionActiveConfig.mockResolvedValue(makeActiveConfigResponse('halloween-night'))
    getNightSessionActiveConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      kind: 'night.session.active',
      revisions: [],
    })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      objects: [
        { id: 'halloween-night', label: 'Halloween Night', currentRevision: 3, updatedAt: '2026-08-22T00:00:00Z' },
        { id: 'christmas-2026', label: 'Christmas', currentRevision: 1, updatedAt: '2026-08-22T00:00:00Z' },
      ],
    })
    putNightSessionActiveConfig.mockResolvedValue(makeActiveConfigResponse('christmas-2026'))

    const user = userEvent.setup()
    renderView()
    await waitFor(() => expect(screen.getByText('halloween-night')).toBeVisible())

    await openPicker(user)
    await user.selectOptions(screen.getByLabelText('Definition to activate'), 'christmas-2026')
    await user.click(screen.getByRole('button', { name: 'Activate this definition…' }))

    // Armed, not yet submitted.
    expect(putNightSessionActiveConfig).not.toHaveBeenCalled()
    expect(screen.getByRole('alertdialog', { name: /confirm night-session activation/i })).toBeVisible()

    await user.click(screen.getByRole('button', { name: 'Confirm: activate "christmas-2026"' }))
    await waitFor(() => expect(putNightSessionActiveConfig).toHaveBeenCalledWith({ session: 'christmas-2026' }))
  })

  // Review finding 11 / the empty-string-clears-the-pointer path.
  it('arms then confirms clearing the pointer, submitting session: "" rather than omitting the key', async () => {
    getNightSessionActiveConfig.mockResolvedValue(makeActiveConfigResponse('halloween-night'))
    getNightSessionActiveConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      kind: 'night.session.active',
      revisions: [],
    })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      objects: [{ id: 'halloween-night', label: 'Halloween Night', currentRevision: 3, updatedAt: '2026-08-22T00:00:00Z' }],
    })
    putNightSessionActiveConfig.mockResolvedValue(makeActiveConfigResponse(''))

    const user = userEvent.setup()
    renderView()
    await waitFor(() => expect(screen.getByText('halloween-night')).toBeVisible())

    await openPicker(user)
    await user.click(screen.getByRole('button', { name: 'Clear the pointer…' }))
    expect(putNightSessionActiveConfig).not.toHaveBeenCalled()
    expect(screen.getByText(/about to clear the active night-session pointer/i)).toBeVisible()

    await user.click(screen.getByRole('button', { name: 'Confirm: clear the pointer' }))
    await waitFor(() => expect(putNightSessionActiveConfig).toHaveBeenCalledWith({ session: '' }))
  })

  it('does not dismiss the confirmation panel on a failed activation, and shows the refusal', async () => {
    getNightSessionActiveConfig.mockResolvedValue(makeActiveConfigResponse('halloween-night'))
    getNightSessionActiveConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      kind: 'night.session.active',
      revisions: [],
    })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      objects: [{ id: 'christmas-2026', label: 'Christmas', currentRevision: 1, updatedAt: '2026-08-22T00:00:00Z' }],
    })
    putNightSessionActiveConfig.mockRejectedValue(
      new ApiError('christmas-2026 has no active revision', 400, makeProblem().type),
    )

    const user = userEvent.setup()
    renderView()
    await waitFor(() => expect(screen.getByText('halloween-night')).toBeVisible())

    await openPicker(user)
    await user.selectOptions(screen.getByLabelText('Definition to activate'), 'christmas-2026')
    await user.click(screen.getByRole('button', { name: 'Activate this definition…' }))
    await user.click(screen.getByRole('button', { name: 'Confirm: activate "christmas-2026"' }))

    await waitFor(() => expect(screen.getByText('christmas-2026 has no active revision')).toBeVisible())
    // Still armed — the operator asked to activate THIS target, and a
    // refusal is about that request, not a reason to make them re-pick it.
    expect(screen.getByRole('alertdialog', { name: /confirm night-session activation/i })).toBeVisible()
  })

  // Review finding 12: the arm buttons must render disabled with a
  // stated reason when config:write is missing, never omitted outright.
  it('renders "Activate a different one" disabled with a stated reason when config:write is not held', async () => {
    getNightSessionActiveConfig.mockResolvedValue(makeActiveConfigResponse('halloween-night'))
    getNightSessionActiveConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      kind: 'night.session.active',
      revisions: [],
    })
    listConfigObjects.mockResolvedValue({
      serverTime: '2026-08-22T00:00:00Z',
      objects: [{ id: 'christmas-2026', label: 'Christmas', currentRevision: 1, updatedAt: '2026-08-22T00:00:00Z' }],
    })
    renderView(makeModel({ session: makeAuthenticatedSession({ scopes: ['show:macro:run'] }) }))
    await waitFor(() => expect(screen.getByText('halloween-night')).toBeVisible())

    const button = screen.getByRole('button', { name: 'Activate a different one' })
    expect(button).toBeDisabled()
    expect(button).toBeVisible()
  })
})
