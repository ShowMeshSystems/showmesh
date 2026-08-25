import { cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { CueCatalogPanel } from './CueCatalogPanel'
import { ModelContext } from '../app/ModelContext'
import { makeModel } from '../app/test-support/fixtures'
import type { CueCatalogDeployResult, CueCatalogResponse, Model, SessionResponse } from '../app/types'

// Mirrors RenderSurfacePanel.test.tsx's own mocking shape: mocked at the
// '../api' boundary, isolating this component's own job (the deploy
// control's ScopedButton wiring to "cuecatalog:deploy", and rendering
// confirmed/unconfirmed honestly per ADR-003) from store.ts's own network
// behavior.
const { getNodeCueCatalog, deployNodeCueCatalog } = vi.hoisted(() => ({
  getNodeCueCatalog: vi.fn(),
  deployNodeCueCatalog: vi.fn(),
}))
vi.mock('../api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api')>()
  return {
    ...actual,
    getNodeCueCatalog,
    deployNodeCueCatalog,
  }
})

afterEach(() => {
  cleanup()
  getNodeCueCatalog.mockReset()
  deployNodeCueCatalog.mockReset()
})

const NOW = '2026-08-25T00:00:00.000Z'

function signedIn(overrides: Partial<SessionResponse> = {}): SessionResponse {
  return {
    serverTime: NOW,
    authenticated: true,
    principal: { id: 'p1', name: 'alice', kind: 'human', role: 'admin' },
    session: { id: 's1', deviceLabel: 'porch tablet', createdAt: NOW },
    credentialForm: 'session',
    scopes: ['cuecatalog:deploy'],
    scopesState: 'current',
    bootstrapRequired: false,
    ...overrides,
  }
}

function catalog(overrides: Partial<CueCatalogResponse> = {}): CueCatalogResponse {
  return {
    serverTime: NOW,
    node: 'media-01',
    configured: true,
    show: 'halloween',
    generation: 3,
    revision: 'sha256:current',
    entries: [{ cueId: 'opener', cueRevision: 1, outputs: {} }],
    ...overrides,
  }
}

function deployResult(overrides: Partial<CueCatalogDeployResult> = {}): CueCatalogDeployResult {
  return {
    commandId: 'cmd-1',
    idempotencyKey: 'key-1',
    node: 'media-01',
    replay: false,
    show: 'halloween',
    generation: 3,
    revision: 'sha256:current',
    outcome: 'confirmed',
    acknowledgedRevision: 'sha256:current',
    dispatchedAt: NOW,
    resolvedAt: NOW,
    ...overrides,
  }
}

// A separate builder, not deployResult({ acknowledgedRevision: undefined,
// ... }): exactOptionalPropertyTypes rejects an explicit undefined on an
// optional field, matching CueCatalogDeployResult's own contract that
// acknowledgedRevision is present only when outcome is "confirmed" — an
// unconfirmed fixture must OMIT the field, not null it out.
function unconfirmedDeployResult(overrides: Partial<CueCatalogDeployResult> = {}): CueCatalogDeployResult {
  return {
    commandId: 'cmd-1',
    idempotencyKey: 'key-1',
    node: 'media-01',
    replay: false,
    show: 'halloween',
    generation: 3,
    revision: 'sha256:current',
    outcome: 'unconfirmed',
    dispatchedAt: NOW,
    resolvedAt: NOW,
    ...overrides,
  }
}

function renderPanel(model: Model) {
  render(
    <ModelContext.Provider value={model}>
      <CueCatalogPanel nodeId="media-01" />
    </ModelContext.Provider>,
  )
}

describe('CueCatalogPanel', () => {
  it('renders the required revision, generation and show, and the held state as not yet observed', async () => {
    getNodeCueCatalog.mockResolvedValue(catalog())
    renderPanel(makeModel({ session: signedIn() }))

    expect(await screen.findByText(/show halloween · generation 3 · revision/)).toBeInTheDocument()
    expect(screen.getByText(/Held revision: not observed from this panel yet/)).toBeInTheDocument()
    expect(getNodeCueCatalog).toHaveBeenCalledWith('media-01')
  })

  it('renders a node with no active show as sensible honest absence, not a broken panel', async () => {
    getNodeCueCatalog.mockResolvedValue({ serverTime: NOW, node: 'media-01', configured: false, entries: [] })
    renderPanel(makeModel({ session: signedIn() }))

    expect(await screen.findByText(/No active show is configured/)).toBeInTheDocument()
  })

  it('renders a fetch error distinctly rather than a blank panel', async () => {
    getNodeCueCatalog.mockRejectedValue(new Error('network unreachable'))
    renderPanel(makeModel({ session: signedIn() }))

    expect(await screen.findByRole('alert')).toHaveTextContent("Could not read this node's Cue catalog")
  })

  it('disables the deploy control, never enabled, when the operator lacks cuecatalog:deploy', async () => {
    getNodeCueCatalog.mockResolvedValue(catalog())
    renderPanel(makeModel({ session: signedIn({ scopes: ['node:read'] }) }))

    await screen.findByText(/show halloween/)
    expect(screen.getByRole('button', { name: 'Deploy catalog' })).toBeDisabled()
  })

  it('dispatches the deploy to the right node and renders a confirmed outcome honestly', async () => {
    getNodeCueCatalog.mockResolvedValue(catalog())
    deployNodeCueCatalog.mockResolvedValue(deployResult({ outcome: 'confirmed', acknowledgedRevision: 'sha256:current' }))
    renderPanel(makeModel({ session: signedIn() }))

    await screen.findByText(/show halloween/)
    await userEvent.click(screen.getByRole('button', { name: 'Deploy catalog' }))

    await waitFor(() => expect(deployNodeCueCatalog).toHaveBeenCalledWith('media-01'))
    expect(await screen.findByText(/Confirmed: the node reports holding revision sha256:current/)).toBeInTheDocument()
  })

  it('renders an accepted-but-unacknowledged deploy as unconfirmed with its reason, never as success', async () => {
    getNodeCueCatalog.mockResolvedValue(catalog())
    deployNodeCueCatalog.mockResolvedValue(
      unconfirmedDeployResult({ reason: 'no result arrived before the deadline' }),
    )
    renderPanel(makeModel({ session: signedIn() }))

    await screen.findByText(/show halloween/)
    await userEvent.click(screen.getByRole('button', { name: 'Deploy catalog' }))

    const unconfirmed = await screen.findByText(/Unconfirmed:/)
    expect(unconfirmed).toHaveTextContent('no result arrived before the deadline')
    expect(screen.queryByText(/Confirmed:/)).not.toBeInTheDocument()
  })

  it('renders a confirmed but generation-mismatched acknowledgement as stale, distinct from current', async () => {
    getNodeCueCatalog.mockResolvedValue(catalog({ generation: 4, revision: 'sha256:newer' }))
    deployNodeCueCatalog.mockResolvedValue(
      deployResult({ outcome: 'confirmed', generation: 3, revision: 'sha256:current', acknowledgedRevision: 'sha256:current' }),
    )
    renderPanel(makeModel({ session: signedIn() }))

    await screen.findByText(/show halloween/)
    await userEvent.click(screen.getByRole('button', { name: 'Deploy catalog' }))

    const stale = await screen.findByText(/stale: the active show now requires revision sha256:newer/)
    expect(stale).toBeInTheDocument()
    expect(screen.queryByText(/current: matches/)).not.toBeInTheDocument()
  })
})
