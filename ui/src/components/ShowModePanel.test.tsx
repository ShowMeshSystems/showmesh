import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { ShowModePanel } from './ShowModePanel'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'

// ADR-033's write surface, mirroring RenderSettingsPanel.test.tsx's
// pattern: the three API functions are mocked to isolate this component's
// own branching, not the network behaviour.
const { getShowModeConfig, putShowModeConfig, getShowModeConfigRevisions } = vi.hoisted(() => ({
  getShowModeConfig: vi.fn(),
  putShowModeConfig: vi.fn(),
  getShowModeConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getShowModeConfig, putShowModeConfig, getShowModeConfigRevisions }
})

afterEach(() => {
  cleanup()
  getShowModeConfig.mockReset()
  putShowModeConfig.mockReset()
  getShowModeConfigRevisions.mockReset()
})

function renderPanel(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <ShowModePanel />
    </ModelContext.Provider>,
  )
}

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
  revision: 3,
  payload: { mode: 'show' },
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
  resolumeWebSocketEffect: 'show mode: the Resolume WebSocket wake-up channel is held CLOSED.',
}

const emptyRevisions = { serverTime: '2026-08-23T21:00:00Z', kind: 'show.mode', revisions: [] }
const oneRevision = {
  serverTime: '2026-08-23T21:00:00Z',
  kind: 'show.mode',
  revisions: [
    {
      revision: 3,
      createdAt: '2026-08-23T21:00:00Z',
      createdByPrincipalId: 'p-1',
      createdByPrincipalName: 'admin-1',
      source: 'api',
      note: '',
      active: true,
    },
  ],
}

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read', 'config:write'],
  scopesState: 'current',
})

const viewerSession = makeAuthenticatedSession({
  principal: { id: 'p-2', name: 'viewer-1', kind: 'human', role: 'viewer' },
  scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read'],
  scopesState: 'current',
})

describe('ShowModePanel', () => {
  it('renders the built-in default explicitly, distinct from a stored choice', async () => {
    getShowModeConfig.mockResolvedValue(programDefault)
    getShowModeConfigRevisions.mockResolvedValue(emptyRevisions)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByText(/never set/i))
    expect(screen.getByText(/built-in default/i)).toBeInTheDocument()
  })

  it('renders a loaded, previously-written revision with its metadata', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    getShowModeConfigRevisions.mockResolvedValue(oneRevision)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByText(/active revision 3/i))
    expect(screen.getAllByText(/admin-1/).length).toBeGreaterThan(0)
    expect(screen.getByText(/source api/i)).toBeInTheDocument()
  })

  // ADR-033 decision 3: a behaviour caused by the mode states the mode as
  // its reason, on the page where an operator changes it.
  it('states what the current mode does to the Resolume WebSocket', async () => {
    getShowModeConfig.mockResolvedValue(showConfigured)
    getShowModeConfigRevisions.mockResolvedValue(oneRevision)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByText(showConfigured.resolumeWebSocketEffect))
  })

  it('submits a full-replacement payload on save', async () => {
    getShowModeConfig.mockResolvedValue(programDefault)
    getShowModeConfigRevisions.mockResolvedValue(emptyRevisions)
    putShowModeConfig.mockResolvedValue(showConfigured)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByText(/never set/i))

    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText(/operating mode/i), 'show')
    await user.click(screen.getByRole('button', { name: /save show mode/i }))

    await waitFor(() => expect(putShowModeConfig).toHaveBeenCalledTimes(1))
    expect(putShowModeConfig).toHaveBeenCalledWith({ mode: 'show' })
  })

  it('offers exactly the two members of the closed vocabulary, and never unknown', async () => {
    getShowModeConfig.mockResolvedValue(programDefault)
    getShowModeConfigRevisions.mockResolvedValue(emptyRevisions)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByLabelText(/operating mode/i))
    const options = screen.getAllByRole('option') as HTMLOptionElement[]
    expect(options.map((o) => o.value)).toEqual(['program', 'show'])
  })

  it('does not fetch or offer a write for a principal without config:write', () => {
    renderPanel(makeModel({ session: viewerSession }))

    expect(getShowModeConfig).not.toHaveBeenCalled()
    expect(screen.queryByRole('button', { name: /save show mode/i })).not.toBeInTheDocument()
  })

  it('renders an error state distinctly from loading and never-set', async () => {
    getShowModeConfig.mockRejectedValue(new Error('network error requesting /config/show.mode'))
    getShowModeConfigRevisions.mockResolvedValue(emptyRevisions)
    renderPanel(makeModel({ session: adminSession }))

    await waitFor(() => screen.getByRole('alert'))
    expect(screen.getByRole('alert').textContent).toMatch(/network error/i)
  })
})
