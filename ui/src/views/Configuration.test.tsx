import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
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
// real requests. Track D seam D-4 moved ResolumeCompositionUpload
// (formerly rendered inside this same admin-gated view) to
// views/ResolumeView.tsx (build contract §2.2: "moves here from
// Configuration.tsx ... not new work"), so this file no longer needs to
// mock the composition endpoints at all — its own tests live in
// ResolumeCompositionUpload.test.tsx and ResolumeView.test.tsx.
const { getFPPEndpointsConfig, putFPPEndpointsConfig, getFPPEndpointsConfigRevisions } = vi.hoisted(() => ({
  getFPPEndpointsConfig: vi.fn(),
  putFPPEndpointsConfig: vi.fn(),
  getFPPEndpointsConfigRevisions: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getFPPEndpointsConfig,
    putFPPEndpointsConfig,
    getFPPEndpointsConfigRevisions,
  }
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
  restartRequired: false,
  restartRequiredReason:
    'this change is already in effect: command dispatch resolves the endpoint list per request, and the collector set follows this configuration within about ten seconds. No restart is needed.',
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
    expect(screen.getByText(/already in effect/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /save configuration/i })).toBeEnabled()
  })

  it('renders the coordinator\'s own 404 reason with an empty editor, not as an error', async () => {
    getFPPEndpointsConfig.mockRejectedValue(
      new ApiError(
        'no fpp.endpoints configuration has been created yet; PUT one to create it',
        404,
        'https://showmesh.dev/problems/resource-not-found',
      ),
    )
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    expect(await screen.findByText(/configuration has been created yet/i)).toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  // The 404 this page treats as "nothing configured" carries two different
  // facts, and the second one is a warning. When the coordinator's startup
  // migration of SHOWMESH_FPP_ENDPOINTS could not be persisted, a
  // configuration IS in effect and the store holds no copy of it, so the
  // operator must not remove that variable. This page used to answer every
  // 404 with its own fixed sentence ("No fpp.endpoints configuration
  // exists yet"), which read as fine while the dashboard listed every host
  // being polled, and which discarded the warning entirely.
  it('renders the deferred-migration reason verbatim, including the warning not to remove the variable', async () => {
    const deferredDetail =
      'no fpp.endpoints configuration is stored, but this coordinator IS collecting from the endpoints named by ' +
      'SHOWMESH_FPP_ENDPOINTS: the startup migration of that variable into this store could not be ' +
      'persisted on this boot. Do NOT remove SHOWMESH_FPP_ENDPOINTS until it has succeeded.'
    getFPPEndpointsConfig.mockRejectedValue(
      new ApiError(deferredDetail, 404, 'https://showmesh.dev/problems/resource-not-found'),
    )
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    renderConfiguration(makeModel({ session: adminSession }))

    expect(await screen.findByText(/Do NOT remove SHOWMESH_FPP_ENDPOINTS/)).toBeInTheDocument()
    expect(screen.queryByText(/configuration exists yet/i)).not.toBeInTheDocument()
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

  // Step 7 seam A review defect 8: two fast clicks on Save must create at
  // most one revision. A bare `fireEvent.click(button)` called twice in a
  // row does NOT reproduce the race — Testing Library wraps every single
  // `fireEvent` call in its own `act()`, which flushes React's state
  // update synchronously before the next `fireEvent.click` runs, so by
  // the second call `saving` has already committed `true` and even the
  // OLD, buggy `if (saving) return` guard (react STATE, not a ref) would
  // have caught it — proven below by first trying exactly that and
  // watching it pass for the wrong reason. Both clicks are wrapped in ONE
  // outer `act()` instead, which is what actually holds React's update
  // un-flushed across both dispatches — the real shape of two clicks
  // landing before the first render commits, which is exactly the
  // scenario a `saving`-state-only guard cannot see (both onClick
  // closures read `saving` as it stood at the render that created them,
  // `false` for both) but the synchronous `savingRef` guard can.
  it('does not double-submit when clicked twice before the first request resolves', async () => {
    getFPPEndpointsConfig.mockResolvedValue(activeConfig)
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    let resolvePut: (value: typeof activeConfig) => void = () => {}
    putFPPEndpointsConfig.mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePut = resolve
        }),
    )
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('player-01')
    const button = screen.getByRole('button', { name: /save configuration/i })

    act(() => {
      fireEvent.click(button)
      fireEvent.click(button)
    })

    resolvePut({ ...activeConfig, revision: 2 })
    await waitFor(() => expect(getFPPEndpointsConfig).toHaveBeenCalledTimes(2))

    expect(putFPPEndpointsConfig).toHaveBeenCalledTimes(1)
  })

  // Step 7 seam A review defect 3c: the 409 SHOWMESH_FPP_ENDPOINTS-still-set
  // refusal must render as an actionable message, not a generic failure —
  // ADR-024 decision 12's "stated reason, never a blank or a bare error
  // code" pattern applied to a refused write rather than a disabled
  // control.
  it('renders the coordinator’s 409 refusal (SHOWMESH_FPP_ENDPOINTS still set) as an actionable message', async () => {
    getFPPEndpointsConfig.mockResolvedValue(activeConfig)
    getFPPEndpointsConfigRevisions.mockResolvedValue(emptyRevisions)
    putFPPEndpointsConfig.mockRejectedValue(
      new ApiError(
        "this coordinator's store cannot become authoritative for fpp.endpoints while SHOWMESH_FPP_ENDPOINTS is " +
          'still set in its process environment; remove it and restart this coordinator once, then retry this write.',
        409,
        'https://showmesh.dev/problems/conflict',
      ),
    )
    const user = userEvent.setup()
    renderConfiguration(makeModel({ session: adminSession }))

    await screen.findByDisplayValue('player-01')
    await user.click(screen.getByRole('button', { name: /save configuration/i }))

    // The static page description also mentions SHOWMESH_FPP_ENDPOINTS
    // (in a <code> tag), so this scopes to the alert specifically rather
    // than asserting the substring appears SOMEWHERE on the page.
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(/SHOWMESH_FPP_ENDPOINTS/)
    expect(alert).toHaveTextContent(/remove it and restart/i)
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
