import { cleanup, render, screen, waitFor } from '@testing-library/react'
import { useState } from 'react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Configuration } from './Configuration'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import { makeAuthenticatedSession } from '../api/test-support/fixtures'
import type { Model } from '../app/types'
import { ApiError } from '../api/errors'

// Step 7 seam A's own view test, mirroring SessionPanel.test.tsx's
// pattern exactly: the three config API functions are mocked here to
// isolate this component's OWN branching (which state renders what, and
// which calls fire when), not the network behavior itself — store.test.ts
// and client.test.ts already prove getFPPEndpointsConfig/
// putFPPEndpointsConfig/getFPPEndpointsConfigRevisions issue the right
// real requests.
const { getFPPEndpointsConfig, putFPPEndpointsConfig, getFPPEndpointsConfigRevisions } = vi.hoisted(() => ({
  getFPPEndpointsConfig: vi.fn(),
  putFPPEndpointsConfig: vi.fn(),
  getFPPEndpointsConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return { ...actual, getFPPEndpointsConfig, putFPPEndpointsConfig, getFPPEndpointsConfigRevisions }
})

afterEach(() => {
  cleanup()
  getFPPEndpointsConfig.mockReset()
  putFPPEndpointsConfig.mockReset()
  getFPPEndpointsConfigRevisions.mockReset()
})

function renderConfiguration(model: Model) {
  return render(
    <ModelContext.Provider value={model}>
      <Configuration />
    </ModelContext.Provider>,
  )
}

const activeConfig = {
  serverTime: '2026-08-12T00:00:00Z',
  kind: 'fpp.endpoints',
  revision: 1,
  payload: { endpoints: [{ id: 'player-01', url: 'http://10.0.1.20' }] },
  updatedAt: '2026-08-12T00:00:00Z',
  createdByPrincipalId: 'p-1',
  createdByPrincipalName: 'admin-1',
  source: 'api',
  restartRequired: true,
  restartRequiredReason: 'this coordinator does not hot-reload configuration; restart to apply',
}

const emptyRevisions = { serverTime: '2026-08-12T00:00:00Z', kind: 'fpp.endpoints', revisions: [] }

const adminSession = makeAuthenticatedSession({
  principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
  scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read', 'config:write'],
  scopesState: 'current',
})

describe('Configuration', () => {
  it('renders "waiting" and does not fetch when no session has arrived yet', () => {
    renderConfiguration(makeModel({ session: null }))

    expect(screen.getByText(/waiting to hear/i)).toBeInTheDocument()
    expect(getFPPEndpointsConfig).not.toHaveBeenCalled()
  })

  it('renders the missing-scope reason and does not fetch for a principal without config:write', () => {
    const viewerSession = makeAuthenticatedSession({
      principal: { id: 'p-2', name: 'viewer-1', kind: 'human', role: 'viewer' },
      scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read'],
      scopesState: 'current',
    })
    renderConfiguration(makeModel({ session: viewerSession }))

    expect(screen.getByText(/does not include "config:write"/)).toBeInTheDocument()
    expect(getFPPEndpointsConfig).not.toHaveBeenCalled()
  })

  // Acceptance criterion 7's logic-level pin: a stale/unavailable scope
  // list must never render this page's data or its Save control as
  // available — verified against a real browser separately (BUILD-PLAN
  // Step 7 acceptance criteria are "verified against the running stack,
  // not only in the suite").
  it('treats scopesState "unknown" as not-permitted, never as permissive, and does not fetch', () => {
    const staleSession = makeAuthenticatedSession({
      principal: { id: 'p-1', name: 'admin-1', kind: 'human', role: 'admin' },
      scopes: ['config:write'],
      scopesState: 'unknown',
    })
    renderConfiguration(makeModel({ session: staleSession }))

    expect(screen.getByText(/permissions are unknown right now/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /save configuration/i })).not.toBeInTheDocument()
    expect(getFPPEndpointsConfig).not.toHaveBeenCalled()
  })

  it('treats a failed session refresh as not-permitted, never as permissive, and does not fetch', () => {
    renderConfiguration(makeModel({ session: adminSession, sessionFetchFailed: true }))

    expect(screen.getByText(/could not be confirmed/i)).toBeInTheDocument()
    expect(getFPPEndpointsConfig).not.toHaveBeenCalled()
  })

  it('fetches and renders the active configuration and revision history for an admin', async () => {
    getFPPEndpointsConfig.mockResolvedValue(activeConfig)
    getFPPEndpointsConfigRevisions.mockResolvedValue({
      serverTime: '2026-08-12T00:00:00Z',
      kind: 'fpp.endpoints',
      revisions: [
        { revision: 1, createdAt: '2026-08-12T00:00:00Z', createdByPrincipalId: 'p-1', createdByPrincipalName: 'admin-1', source: 'api', note: '', active: true },
      ],
    })
    renderConfiguration(makeModel({ session: adminSession }))

    await waitFor(() => expect(getFPPEndpointsConfig).toHaveBeenCalled())
    expect(await screen.findByDisplayValue('player-01')).toBeInTheDocument()
    expect(screen.getByDisplayValue('http://10.0.1.20')).toBeInTheDocument()
    expect(screen.getByText(/does not hot-reload configuration/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save configuration/i })).toBeEnabled()
  })

  it('renders "not configured yet" with an empty editor on 404, not as an error', async () => {
    getFPPEndpointsConfig.mockRejectedValue(new ApiError('not found', 404, 'https://showmesh.dev/problems/resource-not-found'))
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    expect(await screen.findByText(/configuration exists yet/i)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('renders a real fetch failure as an error, distinct from 404', async () => {
    getFPPEndpointsConfig.mockRejectedValue(new ApiError('the store is unreachable', 500))
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    expect(await screen.findByRole('alert')).toHaveTextContent(/unreachable/i)
  })

  it('saves the edited endpoint list via PUT and reloads afterward', async () => {
    getFPPEndpointsConfig.mockResolvedValue(activeConfig)
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    putFPPEndpointsConfig.mockResolvedValue({ ...activeConfig, revision: 2 })
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('player-01')

    const urlInput = screen.getByDisplayValue('http://10.0.1.20')
    await user.clear(urlInput)
    await user.type(urlInput, 'http://10.0.1.99')

    await user.click(screen.getByRole('button', { name: /save configuration/i }))

    await waitFor(() =>
      expect(putFPPEndpointsConfig).toHaveBeenCalledWith({
        endpoints: [{ id: 'player-01', url: 'http://10.0.1.99' }],
      }),
    )
    // A successful save triggers a reload (the reload-generation counter),
    // which must re-fetch the (now newly active) configuration rather
    // than leaving the page showing what it locally guesses happened.
    await waitFor(() => expect(getFPPEndpointsConfig).toHaveBeenCalledTimes(2))
  })

  it('renders the coordinator’s rejection reason on a failed save, without silently succeeding', async () => {
    getFPPEndpointsConfig.mockResolvedValue(activeConfig)
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    putFPPEndpointsConfig.mockRejectedValue(new ApiError('instance id "bad id" is not valid', 400))
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('player-01')
    await user.click(screen.getByRole('button', { name: /save configuration/i }))

    expect(await screen.findByText(/not valid/i)).toBeInTheDocument()
    // Exactly one fetch (the initial load) — a failed save must not
    // trigger a reload the way a successful one does.
    expect(getFPPEndpointsConfig).toHaveBeenCalledTimes(1)
  })

  // This test pins a defect found only by hand-testing a real, live
  // transition in a real browser (CLAUDE.md's standing lesson: a suite
  // that renders one fixed session per test cannot see this). The bug: an
  // earlier version stored the not-permitted REASON in React state, set by
  // an effect keyed on `scopeGate.allowed` — a boolean that is FALSE both
  // signed-out ("sign in to use this control") and signed-in-without-the-
  // scope ("role does not include config:write"), so the effect never
  // re-ran across that exact transition and the page kept showing "sign
  // in" to an operator who was, in fact, already signed in. The fix reads
  // the not-permitted reason straight from `scopeGate` on every render
  // instead of capturing it into state.
  it('updates the not-permitted reason on a live sign-in as a principal without config:write, without remounting', async () => {
    function Harness() {
      const [session, setSession] = useState<Model['session']>(null)
      const model = makeModel({ session })
      return (
        <ModelContext.Provider value={model}>
          <button type="button" onClick={() => setSession(makeAuthenticatedSession({
            principal: { id: 'p-2', name: 'viewer-1', kind: 'human', role: 'viewer' },
            scopes: ['node:read', 'fpp:read', 'observation:read', 'event:read'],
            scopesState: 'current',
          }))}>
            sign in as viewer
          </button>
          <Configuration />
        </ModelContext.Provider>
      )
    }
    render(<Harness />)

    expect(screen.getByText(/waiting to hear/i)).toBeInTheDocument()

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /sign in as viewer/i }))

    expect(screen.queryByText(/waiting to hear/i)).not.toBeInTheDocument()
    expect(await screen.findByText(/does not include "config:write"/)).toBeInTheDocument()
    expect(getFPPEndpointsConfig).not.toHaveBeenCalled()
  })

  it('adding a row and removing a row edits the local editor state', async () => {
    getFPPEndpointsConfig.mockResolvedValue(activeConfig)
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('player-01')
    await user.click(screen.getByRole('button', { name: /add instance/i }))

    expect(screen.getByLabelText('Instance 2 id')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: /remove instance 1/i }))
    expect(screen.queryByDisplayValue('player-01')).not.toBeInTheDocument()
  })
})
